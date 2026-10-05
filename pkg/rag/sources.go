package rag

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/Ownera1/rag-go/internal/document"
)

type PreparedSource struct {
	SourcePath   string
	DocumentPath string
}
type PreparationResult struct {
	Sources  []PreparedSource
	Failures []FileFailure
}

// SourcePreparer resolves inputs into canonical documents without opening the store.
// Core invokes it while holding its write lock. Failure paths refer to original inputs.
type SourcePreparer interface {
	Prepare(context.Context, []string) (PreparationResult, error)
}

func (c *Core) sourceSnapshot() map[string]string {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	out := map[string]string{}
	for k, v := range c.sourcePaths {
		out[k] = v
	}
	return out
}
func (c *Core) sourceFor(path string) string {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if p := c.sourcePaths[path]; p != "" {
		return p
	}
	return path
}
func (c *Core) prepare(ctx context.Context, paths []string, result *IndexResult) ([]string, error) {
	if c.preparer == nil {
		return document.CanonicalFiles(ctx, paths)
	}
	c.progressFile("", "converting")
	prepared, err := c.preparer.Prepare(ctx, paths)
	if err != nil {
		return nil, err
	}
	for _, f := range prepared.Failures {
		result.Failed++
		result.Failures = append(result.Failures, f)
		result.Errors = append(result.Errors, f.Path+": "+f.Error)
	}
	resolved := []string{}
	c.stateMu.Lock()
	for _, source := range prepared.Sources {
		resolved = append(resolved, source.DocumentPath)
		if source.SourcePath != source.DocumentPath {
			c.sourcePaths[source.DocumentPath] = source.SourcePath
		}
	}
	c.stateMu.Unlock()
	return document.CanonicalFiles(ctx, resolved)
}

func (c *Core) Remove(ctx context.Context, paths []string) (RemoveResult, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	result := RemoveResult{UntrackedPaths: []string{}}
	if err := c.writable(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if len(paths) == 0 {
		return result, errors.New("at least one tracked root required")
	}
	tracked := c.tracked()
	removed := map[string]bool{}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return result, err
		}
		if !contains(tracked, abs) {
			return result, fmt.Errorf("path is not a tracked root: %s", abs)
		}
		removed[abs] = true
	}
	remaining := []string{}
	for _, p := range tracked {
		if removed[p] {
			result.UntrackedPaths = append(result.UntrackedPaths, p)
		} else {
			remaining = append(remaining, p)
		}
	}
	covered := func(source string, roots []string) bool {
		for _, root := range roots {
			if within(root, source) {
				return true
			}
		}
		return false
	}
	deleted := []string{}
	if c.db != nil {
		indexed, err := c.db.List(ctx)
		if err != nil {
			return result, err
		}
		for _, p := range indexed {
			source := c.sourceFor(p)
			if covered(source, result.UntrackedPaths) && !covered(source, remaining) {
				if err = c.db.Delete(ctx, p); err != nil {
					return result, err
				}
				result.RemovedDocuments++
				deleted = append(deleted, p)
			}
		}
	}
	c.stateMu.Lock()
	c.trackedPaths = remaining
	failures := []FileFailure{}
	for _, f := range c.failedFiles {
		if !covered(f.Path, result.UntrackedPaths) || covered(f.Path, remaining) {
			failures = append(failures, f)
		}
	}
	c.failedFiles = failures
	for _, p := range deleted {
		delete(c.sourcePaths, p)
	}
	c.stateMu.Unlock()
	return result, c.saveState()
}
