package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/pkg/rag"
	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	core   *rag.Core
	root   string
	config model.AutoRefreshConfig
	mu     sync.Mutex
	status rag.AutoRefreshStatus
	done   chan struct{}
}

func NewWatcher(core *rag.Core, root string, config model.AutoRefreshConfig) *Watcher {
	return &Watcher{core: core, root: root, config: config, status: rag.AutoRefreshStatus{Enabled: config.Enabled}, done: make(chan struct{})}
}
func (w *Watcher) Status() rag.AutoRefreshStatus { w.mu.Lock(); defer w.mu.Unlock(); return w.status }
func (w *Watcher) set(running, dirty bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status.Running = running
	w.status.Pending = 0
	if dirty {
		w.status.Pending = 1
	}
}
func (w *Watcher) Wait() { <-w.done }
func contained(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func ignored(dir string) bool {
	name := filepath.Base(dir)
	switch name {
	case "node_modules", "dist", "build", "venv", "__pycache__":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// walk subscribes to directories (and root parents for removal/recreation).
// The store itself is excluded, including conversion artifacts and credentials.
func (w *Watcher) inventory(roots []string, add func(string)) string {
	h := sha256.New()
	paths := []string{}
	for _, root := range roots {
		add(filepath.Dir(root))
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if contained(w.root, path) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if path != root && ignored(path) {
					return filepath.SkipDir
				}
				add(path)
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			st, err := d.Info()
			if err != nil {
				return err
			}
			paths = append(paths, fmt.Sprintf("%s:%d:%d", path, st.Size(), st.ModTime().UnixNano()))
			return nil
		})
		if err != nil {
			paths = append(paths, root+":unavailable")
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		fmt.Fprintln(h, p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (w *Watcher) Run(ctx context.Context) {
	defer close(w.done)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		w.mu.Lock()
		w.status.LastError = err.Error()
		w.mu.Unlock()
		return
	}
	defer watcher.Close()
	debounce := time.Duration(w.config.DebounceMs) * time.Millisecond
	if debounce <= 0 {
		debounce = 3 * time.Second
	}
	rescan := time.Duration(w.config.RescanMs) * time.Millisecond
	if rescan <= 0 {
		rescan = 5 * time.Minute
	}
	reconcile := time.NewTicker(time.Second)
	defer reconcile.Stop()
	periodic := time.NewTicker(rescan)
	defer periodic.Stop()
	var timer *time.Timer
	var fire <-chan time.Time
	schedule := func(delay time.Duration) {
		if timer != nil {
			timer.Stop()
		}
		timer = time.NewTimer(delay)
		fire = timer.C
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	watched := map[string]bool{}
	roots := w.core.Config().TrackedPaths
	signature := ""
	syncWatches := func() {
		desired := map[string]bool{}
		signature = w.inventory(roots, func(path string) {
			desired[path] = true
			if !watched[path] {
				if err := watcher.Add(path); err == nil {
					watched[path] = true
				} else {
					w.mu.Lock()
					w.status.LastError = "watch registration failed: " + path
					w.mu.Unlock()
				}
			}
		})
		for path := range watched {
			if !desired[path] {
				watcher.Remove(path)
				delete(watched, path)
			}
		}
	}
	syncWatches()
	type outcome struct {
		result rag.IndexResult
		err    error
	}
	completed := make(chan outcome, 1)
	running, dirty := false, false
	retry := 30 * time.Second
	launch := func() {
		running = true
		dirty = false
		w.set(running, dirty)
		go func() { r, e := w.core.Refresh(ctx); completed <- outcome{r, e} }()
	}
	if len(roots) > 0 {
		launch()
	}
	for {
		select {
		case <-ctx.Done():
			if running {
				<-completed
			}
			w.set(false, false)
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			relevant := false
			for _, root := range roots {
				if contained(root, event.Name) {
					relevant = true
					break
				}
			}
			if !relevant || contained(w.root, event.Name) {
				continue
			}
			if event.Op&(fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
				syncWatches()
			}
			dirty = true
			w.set(running, dirty)
			schedule(debounce)
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			w.mu.Lock()
			w.status.LastError = err.Error()
			w.mu.Unlock()
			dirty = true
			w.set(running, dirty)
			schedule(retry)
		case <-reconcile.C:
			next := w.core.Config().TrackedPaths
			if strings.Join(next, "\x00") != strings.Join(roots, "\x00") {
				roots = next
				syncWatches()
				dirty = true
				w.set(running, dirty)
				schedule(debounce)
			}
		case <-periodic.C:
			before := signature
			syncWatches()
			if signature != before {
				dirty = true
				w.set(running, dirty)
				schedule(debounce)
			}
		case <-fire:
			fire = nil
			if running {
				dirty = true
				w.set(running, dirty)
			} else {
				launch()
			}
		case outcome := <-completed:
			running = false
			w.mu.Lock()
			w.status.LastCompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
			w.status.LastError = ""
			if outcome.err != nil {
				w.status.LastError = outcome.err.Error()
			} else if outcome.result.Failed > 0 {
				w.status.LastError = fmt.Sprintf("%d files failed; inspect failedFiles", outcome.result.Failed)
			}
			w.mu.Unlock()
			transient := outcome.err != nil && !strings.Contains(outcome.err.Error(), "rebuild required")
			for _, f := range outcome.result.Failures {
				if f.Retryable || f.Stage == "embed" || f.Stage == "scan" || f.Stage == "store" {
					transient = true
				}
			}
			if dirty {
				schedule(debounce)
			} else if transient {
				schedule(retry)
				retry *= 2
				if retry > 5*time.Minute {
					retry = 5 * time.Minute
				}
			} else {
				retry = 30 * time.Second
			}
			w.set(running, dirty)
		}
	}
}
