package rag

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Ownera1/pi-rag-go/internal/model"
)

type savedState struct {
	TrackedPaths []string            `json:"trackedPaths"`
	FailedFiles  []model.FileFailure `json:"failedFiles"`
}

func (c *Core) loadState() error {
	b, err := os.ReadFile(filepath.Join(c.root, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var s savedState
	if err = json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s.TrackedPaths != nil {
		c.trackedPaths = s.TrackedPaths
	}
	if s.FailedFiles != nil {
		c.failedFiles = s.FailedFiles
	}
	return nil
}

func (c *Core) stateSnapshot() ([]string, []model.FileFailure, model.Progress) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return append([]string{}, c.trackedPaths...), append([]model.FileFailure{}, c.failedFiles...), c.progress
}

func (c *Core) tracked() []string { paths, _, _ := c.stateSnapshot(); return paths }

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (c *Core) updateState(roots []string, failures []model.FileFailure) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	for _, root := range roots {
		if !contains(c.trackedPaths, root) {
			c.trackedPaths = append(c.trackedPaths, root)
		}
	}
	byPath := map[string]model.FileFailure{}
	for _, f := range c.failedFiles {
		covered := false
		for _, root := range roots {
			if within(root, f.Path) {
				covered = true
				break
			}
		}
		if !covered {
			byPath[f.Path] = f
		}
	}
	for _, f := range failures {
		byPath[f.Path] = f
	}
	c.failedFiles = []model.FileFailure{}
	for _, f := range byPath {
		c.failedFiles = append(c.failedFiles, f)
	}
	sort.Strings(c.trackedPaths)
	sort.Slice(c.failedFiles, func(i, j int) bool { return c.failedFiles[i].Path < c.failedFiles[j].Path })
}

func (c *Core) saveState() error {
	paths, failures, _ := c.stateSnapshot()
	b, err := json.MarshalIndent(savedState{paths, failures}, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(c.root, "state.json")
	if err = os.WriteFile(path+".tmp", b, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func addFailure(r *IndexResult, path, stage string, err error) {
	abs, e := filepath.Abs(path)
	if e == nil {
		path = abs
	}
	r.Failed++
	r.Errors = append(r.Errors, path+": "+err.Error())
	r.Failures = append(r.Failures, model.FileFailure{Path: path, Stage: stage, Error: err.Error()})
}

func (c *Core) beginProgress(operation string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.progress = model.Progress{Operation: operation, Phase: "scanning", Running: true, StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
}

func (c *Core) progressTotal(total int) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.progress.Total = total
	if total > 0 {
		c.progress.Phase = "processing"
	}
}

func (c *Core) progressFailures(failed int) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.progress.Failed = failed
}

func (c *Core) progressFile(path, phase string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.progress.CurrentFile, c.progress.Phase = path, phase
}

func (c *Core) progressResult(r IndexResult, path string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.progress.Processed++
	c.progress.Indexed, c.progress.Skipped, c.progress.Failed = r.Indexed, r.Skipped, r.Failed
	c.progress.CurrentFile = path
}

func (c *Core) finishProgress(err error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.progress.Running = false
	c.progress.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	c.progress.Phase = "done"
	if c.progress.Failed > 0 {
		c.progress.Phase = "partial"
	}
	if err != nil {
		c.progress.Phase, c.progress.Error = "failed", err.Error()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			c.progress.Phase = "canceled"
		}
	}
}
