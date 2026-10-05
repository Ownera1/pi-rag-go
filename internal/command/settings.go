package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"

	"github.com/Ownera1/rag-go/internal/model"
)

var Version = "dev"
var Commit = "unknown"

func DefaultStore() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "rag-go")
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "rag-go")
}

func settings(store string) (model.Config, error) {
	path := filepath.Join(store, "config.json")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return model.DefaultConfig(), nil
	}
	return model.LoadConfig(path)
}

func settingsForDaemon(store, config string, legacy bool) (model.Config, error) {
	if legacy {
		return model.DefaultConfig(), nil
	}
	if config != "" {
		return model.LoadConfig(config)
	}
	return settings(store)
}

func endpointFor(store string) (string, error) {
	cfg, err := settings(store)
	if err != nil {
		return "", err
	}
	addr := cfg.Runtime.Listen
	if addr == "" {
		addr = "127.0.0.1:7331"
	}
	if err = validateListen(addr); err != nil {
		return "", err
	}
	return "http://" + addr + "/mcp", nil
}

func validateListen(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("listen address must be loopback")
	}
	if port == "" || port == "0" {
		return errors.New("a fixed port is required")
	}
	return nil
}

func atomicJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".rag-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
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

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func loadCredentials(store string) (map[string]string, error) {
	path := filepath.Join(store, "credentials.json")
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
	var values map[string]string
	if err = json.Unmarshal(b, &values); err != nil {
		return nil, errors.New("invalid credentials.json")
	}
	for key := range values {
		if !envName.MatchString(key) {
			return nil, fmt.Errorf("invalid credential environment name %q", key)
		}
	}
	return values, nil
}

func applyCredentials(store string) error {
	values, err := loadCredentials(store)
	if err != nil {
		return err
	}
	for name, value := range values {
		if os.Getenv(name) == "" {
			if err = os.Setenv(name, value); err != nil {
				return err
			}
		}
	}
	return nil
}
