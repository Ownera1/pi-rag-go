package rag

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	gitignore "github.com/sabhiram/go-gitignore"

	"github.com/Ownera1/pi-rag-go/internal/chunk"
	"github.com/Ownera1/pi-rag-go/internal/document"
	"github.com/Ownera1/pi-rag-go/internal/model"
	"github.com/Ownera1/pi-rag-go/internal/store"
)

var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, ".next": true, "dist": true, "build": true,
	"__pycache__": true, ".venv": true, "venv": true, ".cache": true,
}

var allowed = map[string]bool{
	".md": true, ".mdx": true, ".txt": true, ".rst": true, ".ts": true,
	".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".py": true, ".rs": true, ".go": true, ".java": true, ".kt": true,
	".kts": true, ".scala": true, ".c": true, ".cc": true, ".cpp": true,
	".cxx": true, ".h": true, ".hpp": true, ".hxx": true, ".cs": true,
	".fs": true, ".vb": true, ".swift": true, ".m": true, ".mm": true,
	".rb": true, ".php": true, ".pl": true, ".lua": true, ".dart": true,
	".ex": true, ".exs": true, ".erl": true, ".clj": true, ".cljs": true,
	".edn": true, ".vue": true, ".svelte": true, ".astro": true, ".css": true,
	".scss": true, ".sass": true, ".less": true, ".json": true, ".jsonc": true,
	".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".xml": true,
	".csv": true, ".tsv": true, ".sh": true, ".bash": true, ".zsh": true,
	".fish": true, ".ps1": true, ".sql": true, ".graphql": true, ".gql": true,
	".proto": true, ".env": true, ".gitignore": true, ".dockerfile": true, ".tf": true,
	".hcl": true,
}

func allowedFile(path string, size int64) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if !allowed[ext] {
		return false
	}
	if strings.HasSuffix(strings.ToLower(path), ".tei.xml") {
		return size < 10_000_000
	}
	return size < 500_000
}

func scan(root string, patterns []string) ([]string, error) {
	root, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	st, e := os.Stat(root)
	if e != nil {
		return nil, e
	}
	if st.Mode().IsRegular() {
		if allowedFile(root, st.Size()) {
			return []string{root}, nil
		}
		return nil, fmt.Errorf("unsupported or oversized file: %s", root)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("not a file or directory: %s", root)
	}
	ig := gitignore.CompileIgnoreLines(patterns...)
	found := []string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == root {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if ig.MatchesPath(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		if allowedFile(p, st.Size()) {
			found = append(found, p)
		}
		return nil
	})
	sort.Strings(found)
	return found, err
}

func (c *Core) loadState() {
	b, e := os.ReadFile(filepath.Join(c.root, "state.json"))
	if e == nil {
		var s struct {
			TrackedPaths []string `json:"trackedPaths"`
		}
		if json.Unmarshal(b, &s) == nil && s.TrackedPaths != nil {
			c.cfg.TrackedPaths = s.TrackedPaths
		}
	}
}

func (c *Core) saveState() error {
	b, e := json.MarshalIndent(struct {
		TrackedPaths []string `json:"trackedPaths"`
	}{c.cfg.TrackedPaths}, "", "  ")
	if e != nil {
		return e
	}
	path := filepath.Join(c.root, "state.json")
	tmp := path + ".tmp"
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}

func (c *Core) Index(ctx context.Context, paths []string) (IndexResult, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.legacy {
		return IndexResult{}, errors.New("legacy store is read-only")
	}
	if e := c.ensureDB(); e != nil {
		return IndexResult{}, e
	}
	if e := c.compatible(ctx, c.db); e != nil {
		return IndexResult{}, e
	}
	result := c.indexInto(ctx, c.db, paths, false)
	if result.Failed == 0 {
		for _, p := range paths {
			abs, e := filepath.Abs(p)
			if e == nil && !contains(c.cfg.TrackedPaths, abs) {
				c.cfg.TrackedPaths = append(c.cfg.TrackedPaths, abs)
			}
		}
		if e := c.saveState(); e != nil {
			return result, e
		}
	}
	return result, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

type work struct {
	doc    model.Document
	chunks []model.Chunk
	skip   bool
	err    error
	path   string
}

func (c *Core) indexInto(ctx context.Context, db *store.DB, roots []string, force bool) IndexResult {
	result := IndexResult{Errors: []string{}}
	all := map[string]bool{}
	for _, root := range roots {
		found, e := scan(root, c.cfg.ExcludePatterns)
		if e != nil {
			result.Failed++
			result.Errors = append(result.Errors, e.Error())
			continue
		}
		for _, p := range found {
			all[p] = true
		}
	}
	paths := make([]string, 0, len(all))
	for p := range all {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return result
	}
	jobs := make(chan string)
	done := make(chan work, 32)
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	workers := min(32, len(paths))
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				if e := ctx.Err(); e != nil {
					done <- work{path: p, err: e}
					continue
				}
				doc, e := document.Parse(ctx, p)
				if e != nil {
					done <- work{path: p, err: e}
					continue
				}
				if !force {
					hash, embedded, err := db.FileHash(ctx, p)
					if err != nil {
						done <- work{path: p, err: err}
						continue
					}
					if hash == doc.Hash && embedded {
						done <- work{path: p, skip: true}
						continue
					}
				}
				semantic := c.cfg.Chunking.Mode == "semantic" &&
					(doc.Format == "grobid-tei" ||
						strings.HasSuffix(strings.ToLower(p), ".md") ||
						strings.HasSuffix(strings.ToLower(p), ".mdx") ||
						strings.HasSuffix(strings.ToLower(p), ".txt"))
				var chunks []model.Chunk
				if semantic {
					sem <- struct{}{}
					chunks, e = chunk.Semantic(ctx, doc.Blocks, c.embedder)
					<-sem
				} else if doc.Format == "grobid-tei" {
					for _, b := range doc.Blocks {
						for _, ch := range chunk.Legacy([]model.Block{b}) {
							ch.ChunkIndex = len(chunks)
							chunks = append(chunks, ch)
						}
					}
				} else {
					chunks = chunk.Legacy(doc.Blocks)
				}
				done <- work{path: p, doc: doc, chunks: chunks, err: e}
			}
		}()
	}
	go func() {
		for _, p := range paths {
			jobs <- p
		}
		close(jobs)
		wg.Wait()
		close(done)
	}()
	for w := range done {
		if w.err != nil {
			result.Failed++
			result.Errors = append(result.Errors, w.path+": "+w.err.Error())
			continue
		}
		if w.skip {
			result.Skipped++
			continue
		}
		if c.embedder == nil {
			result.Failed++
			result.Errors = append(result.Errors, w.path+": embedding provider unavailable")
			continue
		}
		for i := range w.chunks {
			w.chunks[i].ID = document.ShortHash(w.doc.Path) + "-" + fmt.Sprint(w.chunks[i].ChunkIndex)
			w.chunks[i].Hash = document.ShortHash(w.chunks[i].Content)
			w.chunks[i].Tokens = chunk.Estimate(w.chunks[i].Content)
			w.chunks[i].Path = w.doc.Path
		}
		texts := make([]string, len(w.chunks))
		for i, ch := range w.chunks {
			texts[i] = ch.Content
		}
		vectors, e := c.embedder.EmbedDocuments(ctx, texts)
		if e == nil {
			e = db.Replace(ctx, w.doc, w.chunks, vectors)
		}
		if e != nil {
			result.Failed++
			result.Errors = append(result.Errors, w.path+": "+e.Error())
			continue
		}
		result.Indexed++
		result.Chunks += len(w.chunks)
	}
	return result
}

func (c *Core) Refresh(ctx context.Context) (IndexResult, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.legacy {
		return IndexResult{}, errors.New("legacy store is read-only")
	}
	if len(c.cfg.TrackedPaths) == 0 {
		return IndexResult{Errors: []string{}}, nil
	}
	if e := c.ensureDB(); e != nil {
		return IndexResult{}, e
	}
	if e := c.compatible(ctx, c.db); e != nil {
		return IndexResult{}, e
	}
	result := c.indexInto(ctx, c.db, c.cfg.TrackedPaths, false)
	if result.Failed > 0 {
		return result, nil
	}
	present := map[string]bool{}
	for _, root := range c.cfg.TrackedPaths {
		paths, e := scan(root, c.cfg.ExcludePatterns)
		if e != nil {
			return result, e
		}
		for _, p := range paths {
			present[p] = true
		}
	}
	indexed, e := c.db.List(ctx)
	if e != nil {
		return result, e
	}
	for _, p := range indexed {
		if !present[p] {
			if e = c.db.Delete(ctx, p); e != nil {
				return result, e
			}
		}
	}
	return result, nil
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

func (c *Core) publish(ctx context.Context, stage *store.DB, stageDir string) error {
	st, e := stage.Stats(ctx)
	if e != nil {
		return e
	}
	if st.Chunks != st.Vectors {
		return fmt.Errorf("vector coverage %d/%d; refusing to publish", st.Vectors, st.Chunks)
	}
	if e = c.stamp(ctx, stage); e != nil {
		return e
	}
	_, _ = stage.SQL.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	if e = stage.Close(); e != nil {
		return e
	}
	emb, proc := fingerprint(c.cfg)
	id := emb[:10] + "-" + proc[:10]
	generation := randomID()
	rel := filepath.Join("indexes", id, generation, "rag.db")
	dest := filepath.Join(c.root, filepath.Dir(rel))
	if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		return e
	}
	if e = os.Rename(stageDir, dest); e != nil {
		return e
	}
	next, e := store.Open(filepath.Join(c.root, rel), false, c.cfg.Embedding.Dimensions)
	if e != nil {
		return e
	}
	manifest := struct {
		Version               int    `json:"version"`
		IndexID               string `json:"indexId"`
		RelativeDBPath        string `json:"relativeDbPath"`
		EmbeddingFingerprint  string `json:"embeddingFingerprint"`
		ProcessingFingerprint string `json:"processingFingerprint"`
		CreatedAt             string `json:"createdAt"`
	}{1, id, rel, emb, proc, time.Now().UTC().Format(time.RFC3339Nano)}
	b, e := json.MarshalIndent(manifest, "", "  ")
	if e != nil {
		next.Close()
		return e
	}
	tmp := filepath.Join(c.root, "active.json.tmp")
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		next.Close()
		return e
	}
	if e = os.Rename(tmp, filepath.Join(c.root, "active.json")); e != nil {
		next.Close()
		return e
	}
	c.mu.Lock()
	old := c.db
	c.db = next
	c.mu.Unlock()
	if old != nil {
		return old.Close()
	}
	return nil
}

func (c *Core) Rebuild(ctx context.Context) (IndexResult, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.legacy {
		return IndexResult{}, errors.New("legacy store is read-only")
	}
	if len(c.cfg.TrackedPaths) == 0 {
		return IndexResult{}, errors.New("no tracked paths to rebuild")
	}
	stageDir := filepath.Join(c.root, "staging", randomID())
	db, e := store.Open(filepath.Join(stageDir, "rag.db"), false, c.cfg.Embedding.Dimensions)
	if e != nil {
		return IndexResult{}, e
	}
	open := true
	defer func() {
		if open {
			db.Close()
		}
		_ = os.RemoveAll(stageDir)
	}()
	result := c.indexInto(ctx, db, c.cfg.TrackedPaths, true)
	if result.Failed > 0 {
		return result, errors.New("rebuild failed; active index preserved")
	}
	if e = ctx.Err(); e != nil {
		return result, e
	}
	if e = c.publish(ctx, db, stageDir); e != nil {
		return result, e
	}
	open = false
	return result, nil
}

func (c *Core) Clear(ctx context.Context) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.legacy {
		return errors.New("legacy store is read-only")
	}
	stageDir := filepath.Join(c.root, "staging", randomID())
	db, e := store.Open(filepath.Join(stageDir, "rag.db"), false, c.cfg.Embedding.Dimensions)
	if e != nil {
		return e
	}
	defer os.RemoveAll(stageDir)
	return c.publish(ctx, db, stageDir)
}
