package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// The registry lists workspaces by absolute path in GlobalDir's
// workspaces.json, like git maintenance's maintenance.repo, so commands can
// name a workspace from anywhere. Names are derived from the paths on read.
// Listing also trusts the workspace's endpoints (see CheckEndpoint).

// Entry is a registered workspace. Missing means its configuration is gone.
type Entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Missing bool   `json:"missing,omitempty"`
}

func registryPath() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "workspaces.json"), nil
}

func readRegistry(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	if json.Unmarshal(b, &paths) != nil {
		return nil, fmt.Errorf("invalid %s", path)
	}
	return paths, nil
}

// Workspaces lists the registered workspaces in registration order. A name is
// the directory's base name, prefixed by its parent's when two share it, and
// the full path when that still collides.
func Workspaces() ([]Entry, error) {
	path, err := registryPath()
	if err != nil {
		return nil, err
	}
	paths, err := readRegistry(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(paths))
	count := map[string]int{}
	for i, p := range paths {
		names[i] = filepath.Base(p)
		count[names[i]]++
	}
	again := map[string]int{}
	for i, p := range paths {
		if count[names[i]] > 1 {
			names[i] = filepath.Join(filepath.Base(filepath.Dir(p)), names[i])
		}
		again[names[i]]++
	}
	out := make([]Entry, len(paths))
	for i, p := range paths {
		if again[names[i]] > 1 {
			names[i] = p
		}
		_, err := os.Stat(filepath.Join(Store(p), "config.json"))
		out[i] = Entry{Name: names[i], Path: p, Missing: err != nil}
	}
	return out, nil
}

// Resolve maps a registered name to its workspace path and returns anything
// else unchanged as a path. Names win, so ./name selects a directory.
func Resolve(arg string) (string, error) {
	if arg == "" || filepath.IsAbs(arg) {
		return arg, nil
	}
	list, err := Workspaces()
	if err != nil {
		return "", err
	}
	for _, e := range list {
		if e.Name == arg {
			return e.Path, nil
		}
	}
	return arg, nil
}

// Registered reports whether the absolute path root is listed.
func Registered(root string) (bool, error) {
	path, err := registryPath()
	if err != nil {
		return false, err
	}
	paths, err := readRegistry(path)
	return slices.Contains(paths, root), err
}

// Register adds the workspace at the absolute path root, if not yet listed.
func Register(ctx context.Context, root string) error {
	return updateRegistry(ctx, func(paths []string) ([]string, error) {
		for _, p := range paths {
			if p == root {
				return paths, nil
			}
		}
		return append(paths, root), nil
	})
}

// Unregister removes root from the registry; the workspace itself is kept.
func Unregister(ctx context.Context, root string) error {
	return updateRegistry(ctx, func(paths []string) ([]string, error) {
		for i, p := range paths {
			if p == root {
				return append(paths[:i], paths[i+1:]...), nil
			}
		}
		return nil, fmt.Errorf("%s is not registered", root)
	})
}

func updateRegistry(ctx context.Context, change func([]string) ([]string, error)) error {
	path, err := registryPath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(path), "workspaces.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	release, err := flock(ctx, f, syscall.LOCK_EX)
	if err != nil {
		return err
	}
	defer release()
	paths, err := readRegistry(path)
	if err != nil {
		return err
	}
	if paths, err = change(paths); err != nil {
		return err
	}
	if paths == nil {
		paths = []string{}
	}
	return AtomicJSON(path, paths)
}
