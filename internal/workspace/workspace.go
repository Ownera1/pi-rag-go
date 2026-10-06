// Package workspace owns discovery, operation locks and private JSON files.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

func Store(root string) string { return filepath.Join(root, ".rag-go") }

// Discover uses the nearest workspace. An explicit path never searches parents.
func Discover(explicit string) (string, error) {
	if explicit != "" {
		return discover(explicit, false)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return discover(dir, true)
}

// DiscoverFrom returns the nearest workspace at or above dir.
func DiscoverFrom(dir string) (string, error) { return discover(dir, true) }

func discover(start string, walk bool) (string, error) {
	root, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		_, err = os.Stat(Store(root))
		if err == nil {
			if _, err = os.Stat(filepath.Join(Store(root), "config.json")); err != nil {
				return "", fmt.Errorf("incomplete workspace %s: %w", root, err)
			}
			return root, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if !walk {
			return "", fmt.Errorf("no rag-go workspace at %s; run rag init", start)
		}
		if filepath.Dir(root) == root {
			return "", fmt.Errorf("no rag-go workspace at or above %s; run rag init", start)
		}
		root = filepath.Dir(root)
	}
}

// Lock is operation-scoped; shared readers pin a generation until they close it.
func Lock(ctx context.Context, root string, write bool) (func(), error) {
	flag, mode := os.O_RDONLY, syscall.LOCK_SH
	if write {
		flag, mode = os.O_RDWR, syscall.LOCK_EX
	}
	f, err := os.OpenFile(filepath.Join(Store(root), ".lock"), flag, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err = ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func AtomicJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return AtomicFile(path, append(b, '\n'), 0600)
}

func AtomicFile(path string, b []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".rag-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

var EnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// GlobalDir holds user-wide defaults written by rag install. RAG_GO_CONFIG_DIR
// overrides it; otherwise XDG_CONFIG_HOME/rag-go or ~/.config/rag-go.
func GlobalDir() (string, error) {
	if dir := os.Getenv("RAG_GO_CONFIG_DIR"); dir != "" {
		return filepath.Abs(dir)
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "rag-go"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "rag-go"), nil
}

// Credentials resolves workspace credentials over user-wide ones. Environment
// variables still take precedence inside providers.
func Credentials(root string) (map[string]string, error) {
	values, err := GlobalCredentials()
	if err != nil {
		return nil, err
	}
	local, err := LocalCredentials(root)
	if err != nil {
		return nil, err
	}
	for k, v := range local {
		values[k] = v
	}
	return values, nil
}

func LocalCredentials(root string) (map[string]string, error) {
	return readCredentials(filepath.Join(Store(root), "credentials.json"))
}

func GlobalCredentials() (map[string]string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return nil, err
	}
	return readCredentials(filepath.Join(dir, "credentials.json"))
}

func readCredentials(path string) (map[string]string, error) {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%s must be a regular file with permissions 0600", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	if json.Unmarshal(b, &values) != nil {
		return nil, fmt.Errorf("invalid %s", path)
	}
	for key := range values {
		if !EnvName.MatchString(key) {
			return nil, errors.New("invalid credential environment name")
		}
	}
	return values, nil
}
