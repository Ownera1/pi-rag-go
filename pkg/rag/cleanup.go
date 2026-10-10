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

	"github.com/Ownera1/rag-go/internal/store"
)

var indexName = regexp.MustCompile(`^[0-9a-f]{10}-[0-9a-f]{10}$`)
var generationName = regexp.MustCompile(`^[0-9]+-[0-9a-f]{16}$`)

// Cleanup retains the active generation and the newest inactive generations,
// keeping at least keep generations in total. Unknown files and symlinks are
// never removed; fingerprint directories left empty are. dryRun reports
// candidates without deleting them.
func (c *session) cleanup(ctx context.Context, keep int, dryRun bool) (result CleanupResult, err error) {
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
	// A rebuild killed mid-way leaves its staging database behind; the exclusive
	// lock held here means no rebuild is in progress.
	staging := filepath.Join(c.root, "staging")
	if info, e := os.Lstat(staging); e == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return result, errors.New("staging must be a real directory")
	}
	staged, err := os.ReadDir(staging)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	for _, entry := range staged {
		path := filepath.Join(staging, entry.Name())
		if entry.Name() == finderMetadata {
			continue
		}
		if !entry.IsDir() || !generationName.MatchString(entry.Name()) || !dbDir(path) {
			result.Skipped = append(result.Skipped, path)
			continue
		}
		if !dryRun {
			if err = os.RemoveAll(path); err != nil {
				return result, err
			}
		}
		result.Removed = append(result.Removed, path)
	}
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
	active := ""
	if c.db != nil {
		active = filepath.Dir(c.db.Path)
	}
	type generation struct {
		path    string
		created int64
	}
	all := []generation{}
	// The first sync writes .rag-go/rag.db, which nothing reads once a rebuild
	// has published a generation. It is the oldest one.
	legacy := filepath.Join(c.root, "rag.db")
	if active != "" && active != c.root && knownDB(legacy) {
		all = append(all, generation{legacy, 0})
	}
	idPaths := []string{}
	ids, err := os.ReadDir(root)
	if err != nil {
		return result, err
	}
	for _, id := range ids {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		idPath := filepath.Join(root, id.Name())
		if id.Name() == finderMetadata {
			continue
		}
		if !id.IsDir() || !indexName.MatchString(id.Name()) {
			result.Skipped = append(result.Skipped, idPath)
			continue
		}
		idPaths = append(idPaths, idPath)
		entries, e := os.ReadDir(idPath)
		if e != nil {
			return result, e
		}
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			path := filepath.Join(idPath, entry.Name())
			if entry.Name() == finderMetadata {
				continue
			}
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
			known, remove := knownGeneration, os.RemoveAll
			if g.path == legacy {
				known, remove = knownDB, removeDB
			}
			if !known(g.path) {
				result.Skipped = append(result.Skipped, g.path)
				continue
			}
			if err = remove(g.path); err != nil {
				return result, err
			}
		}
		result.Removed = append(result.Removed, g.path)
	}
	if !dryRun {
		for _, p := range idPaths {
			removeEmptyDir(p)
		}
	}
	return result, nil
}

// finderMetadata is the file macOS Finder writes into every folder it opens.
const finderMetadata = ".DS_Store"

// removeEmptyDir removes dir when it holds nothing but Finder metadata.
func removeEmptyDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Name() != finderMetadata || !entry.Type().IsRegular() {
			return
		}
	}
	_ = os.Remove(filepath.Join(dir, finderMetadata))
	_ = os.Remove(dir)
}

func knownGeneration(path string) bool {
	return dbDir(path) && knownDB(filepath.Join(path, "rag.db"))
}

// knownDB reports whether path is a regular file holding a rag-go store.
func knownDB(path string) bool {
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return false
	}
	ok, err := store.Recognized(context.Background(), path)
	return err == nil && ok
}

// dbDir reports whether path is a real directory holding rag.db and nothing
// but its WAL files and Finder metadata.
func dbDir(path string) bool {
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
		case "rag.db-wal", "rag.db-shm", finderMetadata:
		default:
			return false
		}
	}
	return hasDB
}
