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
	root := explicit
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	root, err := filepath.Abs(root)
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
		if explicit != "" || filepath.Dir(root) == root {
			return "", errors.New("workspace is not initialized; run rag init")
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

func Credentials(root string) (map[string]string, error) {
	path := filepath.Join(Store(root), "credentials.json")
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("credentials.json must be a regular file with permissions 0600")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	if json.Unmarshal(b, &values) != nil {
		return nil, errors.New("invalid credentials.json")
	}
	for key := range values {
		if !EnvName.MatchString(key) {
			return nil, errors.New("invalid credential environment name")
		}
	}
	return values, nil
}
