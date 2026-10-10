// Package workspace owns discovery, operation locks and private JSON files.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Ownera1/rag-go/internal/model"
)

func Store(root string) string { return filepath.Join(root, ".rag-go") }

// ErrNotFound reports a directory with no workspace at or above it.
var ErrNotFound = errors.New("no rag-go workspace")

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
			return "", fmt.Errorf("%w at %s; run rag init", ErrNotFound, start)
		}
		if filepath.Dir(root) == root {
			return "", fmt.Errorf("%w at or above %s; run rag init", ErrNotFound, start)
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
	return flock(ctx, f, mode)
}

// flock waits, cancellably, for a lock on f and closes f if it fails.
func flock(ctx context.Context, f *os.File, mode int) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err := syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB)
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
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	// Best effort: persist the rename; some filesystems reject directory fsync.
	if d, e := os.Open(filepath.Dir(path)); e == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

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

// GlobalConfig returns the user-wide defaults written by rag install.
func GlobalConfig() (model.Config, bool, error) {
	dir, err := GlobalDir()
	if err != nil {
		return model.Config{}, false, err
	}
	path := filepath.Join(dir, "config.json")
	if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return model.DefaultConfig(), false, nil
	}
	cfg, err := model.LoadConfig(path)
	if err != nil {
		return cfg, false, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, true, nil
}

// TrustedEndpoint reports whether p is Voyage's default endpoint or the one
// rag install recorded, the only ones that receive ambient credentials.
func TrustedEndpoint(p model.ProviderConfig) (bool, error) {
	d := model.DefaultConfig().Embedding
	if p.BaseURL == d.BaseURL && p.APIKeyEnv == d.APIKeyEnv {
		return true, nil
	}
	g, installed, err := GlobalConfig()
	return installed && p.BaseURL == g.Embedding.BaseURL && p.APIKeyEnv == g.Embedding.APIKeyEnv, err
}

// CheckEndpoint refuses an endpoint the user never chose. A cloned repository
// may carry .rag-go/config.json, so a workspace not registered on this machine
// by rag init or rag workspace add sends documents and queries only to a
// trusted endpoint.
func CheckEndpoint(root string, p model.ProviderConfig) error {
	trusted, err := TrustedEndpoint(p)
	if err != nil || trusted {
		return err
	}
	registered, err := Registered(root)
	if err != nil || registered {
		return err
	}
	return fmt.Errorf("refusing to send text to %s: %s is not a registered workspace; if you chose this endpoint, run rag workspace add %s", p.BaseURL, root, root)
}

// Credential resolves p's key from the environment, the workspace, then the
// user-wide file. A workspace config may come from a cloned repository, so the
// environment and user-wide file serve only a trusted endpoint; workspace
// credentials belong to that workspace.
func Credential(root string, p model.ProviderConfig) (string, error) {
	if p.APIKeyEnv == "" {
		return "", nil
	}
	local, err := LocalCredentials(root)
	if err != nil {
		return "", err
	}
	trusted, err := TrustedEndpoint(p)
	if err != nil || !trusted {
		return local[p.APIKeyEnv], err
	}
	if key := os.Getenv(p.APIKeyEnv); key != "" {
		return key, nil
	}
	if key := local[p.APIKeyEnv]; key != "" {
		return key, nil
	}
	global, err := GlobalCredentials()
	return global[p.APIKeyEnv], err
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
		if !model.EnvName.MatchString(key) {
			return nil, errors.New("invalid credential environment name")
		}
	}
	return values, nil
}
