package rag

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Ownera1/pi-rag-go/internal/store"
)

var indexName = regexp.MustCompile(`^[0-9a-f]{10}-[0-9a-f]{10}$`)
var generationName = regexp.MustCompile(`^[0-9]+-[0-9a-f]{16}$`)

// Cleanup retains the active generation and the newest inactive generations,
// keeping at least keep generations in total. Unknown files and symlinks are
// never removed. dryRun reports candidates without deleting them.
func (c *Core) Cleanup(ctx context.Context, keep int, dryRun bool) (result CleanupResult, err error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	result = CleanupResult{DryRun: dryRun, Retained: []string{}, Removed: []string{}, Skipped: []string{}}
	if err = c.writable(); err != nil {
		return result, err
	}
	if keep < 1 || keep > 1000 {
		return result, errors.New("keep must be in [1,1000]")
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	c.beginProgress("cleanup")
	defer func() { c.finishProgress(err) }()
	root := filepath.Join(c.root, "indexes")
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("indexes must be a real directory")
	}
	c.mu.RLock()
	active := ""
	if c.db != nil {
		active = filepath.Dir(c.db.Path)
	}
	c.mu.RUnlock()
	type generation struct {
		path    string
		created int64
	}
	all := []generation{}
	ids, err := os.ReadDir(root)
	if err != nil {
		return result, err
	}
	for _, id := range ids {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		idPath := filepath.Join(root, id.Name())
		if !id.IsDir() || !indexName.MatchString(id.Name()) {
			result.Skipped = append(result.Skipped, idPath)
			continue
		}
		entries, e := os.ReadDir(idPath)
		if e != nil {
			return result, e
		}
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			path := filepath.Join(idPath, entry.Name())
			if path == active {
				result.Retained = append(result.Retained, path)
				continue
			}
			if !entry.IsDir() || !generationName.MatchString(entry.Name()) || !knownGeneration(path) {
				result.Skipped = append(result.Skipped, path)
				continue
			}
			created, e := strconv.ParseInt(strings.SplitN(entry.Name(), "-", 2)[0], 10, 64)
			if e != nil {
				result.Skipped = append(result.Skipped, path)
				continue
			}
			all = append(all, generation{path, created})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].created == all[j].created {
			return all[i].path > all[j].path
		}
		return all[i].created > all[j].created
	})
	retained := len(result.Retained)
	if active != "" && retained == 0 {
		result.Retained = append(result.Retained, active)
		retained++
	}
	for _, g := range all {
		if retained < keep {
			result.Retained = append(result.Retained, g.path)
			retained++
			continue
		}
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if !dryRun {
			// Revalidate after enumeration before deleting a recognized generation.
			if !knownGeneration(g.path) {
				result.Skipped = append(result.Skipped, g.path)
				continue
			}
			if err = os.RemoveAll(g.path); err != nil {
				return result, err
			}
		}
		result.Removed = append(result.Removed, g.path)
	}
	return result, nil
}

func knownGeneration(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	hasDB := false
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return false
		}
		switch entry.Name() {
		case "rag.db":
			hasDB = true
		case "rag.db-wal", "rag.db-shm":
		default:
			return false
		}
	}
	if !hasDB {
		return false
	}
	db, err := store.Open(filepath.Join(path, "rag.db"), true, 0)
	if err != nil {
		return false
	}
	defer db.Close()
	return db.GetMetadata(context.Background(), "go_storage_version") == "1"
}
