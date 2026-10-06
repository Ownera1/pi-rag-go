package command

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/provider"
	"github.com/Ownera1/rag-go/internal/workspace"
	"golang.org/x/term"
)

// providerFlags are the embedding options shared by rag install and rag init.
type providerFlags struct {
	kind, name, base, keyEnv *string
	dimensions               *int
}

func addProviderFlags(fs *flag.FlagSet) providerFlags {
	return providerFlags{
		kind:       fs.String("embedding-type", "voyage", "voyage or openai"),
		name:       fs.String("model", "voyage-4-lite", "embedding model"),
		dimensions: fs.Int("dimensions", 1024, "embedding dimensions"),
		base:       fs.String("base-url", "https://api.voyageai.com/v1", "embedding endpoint prefix"),
		keyEnv:     fs.String("api-key-env", "VOYAGE_API_KEY", "credential environment name; empty for no authentication"),
	}
}

// prompt asks for every option not supplied on the command line.
func (p providerFlags) prompt(t *terminal, supplied map[string]bool) error {
	var err error
	if !supplied["embedding-type"] {
		if *p.kind, err = t.ask("Embedding provider (voyage/openai)", *p.kind); err != nil {
			return err
		}
	}
	if *p.kind == "openai" {
		// Voyage defaults do not describe an OpenAI-compatible endpoint.
		if !supplied["model"] {
			*p.name = ""
		}
		if !supplied["base-url"] {
			*p.base = ""
		}
		if !supplied["api-key-env"] {
			*p.keyEnv = ""
		}
	}
	if !supplied["model"] {
		if *p.name, err = t.ask("Embedding model", *p.name); err != nil {
			return err
		}
	}
	if *p.kind != "openai" {
		return nil
	}
	if !supplied["base-url"] {
		if *p.base, err = t.ask("Embedding API prefix", *p.base); err != nil {
			return err
		}
	}
	if !supplied["dimensions"] {
		v, e := t.ask("Embedding dimensions", strconv.Itoa(*p.dimensions))
		if e != nil {
			return e
		}
		if *p.dimensions, err = strconv.Atoi(v); err != nil {
			return err
		}
	}
	if !supplied["api-key-env"] {
		if *p.keyEnv, err = t.ask("API key environment name", *p.keyEnv); err != nil {
			return err
		}
	}
	return nil
}

// apply replaces the provider for a new configuration; otherwise it changes
// only options supplied on the command line.
func (p providerFlags) apply(cfg *model.ProviderConfig, replace bool, supplied map[string]bool) {
	if replace {
		*cfg = model.ProviderConfig{Type: *p.kind, Model: *p.name, Dimensions: *p.dimensions, BaseURL: *p.base, APIKeyEnv: *p.keyEnv}
		return
	}
	if supplied["embedding-type"] {
		cfg.Type = *p.kind
	}
	if supplied["model"] {
		cfg.Model = *p.name
	}
	if supplied["dimensions"] {
		cfg.Dimensions = *p.dimensions
	}
	if supplied["base-url"] {
		cfg.BaseURL = *p.base
	}
	if supplied["api-key-env"] {
		cfg.APIKeyEnv = *p.keyEnv
	}
}

type terminal struct {
	file        *os.File
	reader      *bufio.Reader
	stderr      io.Writer
	interactive bool
}

func newTerminal(in io.Reader, stderr io.Writer) *terminal {
	file, ok := in.(*os.File)
	return &terminal{file: file, reader: bufio.NewReader(in), stderr: stderr, interactive: ok && term.IsTerminal(int(file.Fd()))}
}

func (t *terminal) ask(label, current string) (string, error) {
	fmt.Fprintf(t.stderr, "%s [%s]: ", label, current)
	s, e := t.reader.ReadString('\n')
	if e != nil && !errors.Is(e, io.EOF) {
		return "", e
	}
	if s = strings.TrimSpace(s); s == "" {
		s = current
	}
	return s, nil
}

func (t *terminal) secret(label string) (string, error) {
	fmt.Fprintf(t.stderr, "%s (hidden): ", label)
	b, e := term.ReadPassword(int(t.file.Fd()))
	fmt.Fprintln(t.stderr)
	return string(b), e
}

// probe embeds one query to verify the endpoint, credential and dimensions.
func probe(ctx context.Context, cfg model.Config, key string) (string, error) {
	p, e := provider.NewHTTP(cfg.Embedding, cfg.HTTPTimeoutMs, 0)
	if e == nil {
		p.SetCredential(key)
		_, e = p.EmbedQuery(ctx, "rag-go initialization probe")
	}
	if e != nil {
		return "failed", errors.New("embedding probe failed; check endpoint, credential, model and dimensions")
	}
	return "reachable; dimensions verified", nil
}

func globalConfigPath() (string, error) {
	dir, err := workspace.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// loadGlobalConfig returns the user-wide defaults written by rag install.
func loadGlobalConfig() (model.Config, bool, error) {
	path, err := globalConfigPath()
	if err != nil {
		return model.Config{}, false, err
	}
	if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return model.DefaultConfig(), false, nil
	}
	cfg, err := model.LoadConfig(path)
	if err != nil {
		return cfg, false, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, true, nil
}
