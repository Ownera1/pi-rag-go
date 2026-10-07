package command

import (
	"cmp"
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
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
	"github.com/charmbracelet/lipgloss"
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
		if *p.kind, err = t.choose("Embedding provider", *p.kind, "voyage", "openai"); err != nil {
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

// terminal draws prompts and progress on stderr, keeping stdout for the JSON
// report. Its methods other than spin are called only when interactive.
type terminal struct {
	file        *os.File
	stderr      io.Writer
	interactive bool
}

func newTerminal(in io.Reader, stderr io.Writer) *terminal {
	file, ok := in.(*os.File)
	return &terminal{file: file, stderr: stderr, interactive: ok && term.IsTerminal(int(file.Fd()))}
}

func (t *terminal) show(field huh.Field) error {
	return huh.NewForm(huh.NewGroup(field)).WithInput(t.file).WithOutput(t.stderr).Run()
}

// mark leaves a one-line record of a step, since huh clears finished prompts.
func (t *terminal) mark(ok bool, label, value string) {
	r := lipgloss.NewRenderer(t.stderr)
	sign := r.NewStyle().Foreground(lipgloss.Color("2")).Render("✓")
	if !ok {
		sign = r.NewStyle().Foreground(lipgloss.Color("1")).Render("✗")
	}
	fmt.Fprintf(t.stderr, "%s %s: %s\n", sign, label, value)
}

func (t *terminal) ask(label, current string) (string, error) {
	s := current
	if err := t.show(huh.NewInput().Title(label).Value(&s)); err != nil {
		return "", err
	}
	if s = strings.TrimSpace(s); s == "" {
		s = current
	}
	t.mark(true, label, s)
	return s, nil
}

func (t *terminal) choose(label, current string, options ...string) (string, error) {
	s := current
	if err := t.show(huh.NewSelect[string]().Title(label).Options(huh.NewOptions(options...)...).Value(&s)); err != nil {
		return "", err
	}
	t.mark(true, label, s)
	return s, nil
}

func (t *terminal) pick(label string, chosen []string, options ...string) ([]string, error) {
	if err := t.show(huh.NewMultiSelect[string]().Title(label).Value(&chosen).Options(huh.NewOptions(options...)...)); err != nil {
		return nil, err
	}
	t.mark(true, label, cmp.Or(strings.Join(chosen, ", "), "none"))
	return chosen, nil
}

func (t *terminal) secret(label string) (string, error) {
	var s string
	if err := t.show(huh.NewInput().Title(label).EchoMode(huh.EchoModePassword).Value(&s)); err != nil {
		return "", err
	}
	if s != "" {
		t.mark(true, label, "entered")
	}
	return s, nil
}

// spin runs fn behind a spinner and records its outcome when interactive.
func (t *terminal) spin(label string, fn func() (string, error)) (string, error) {
	if !t.interactive {
		return fn()
	}
	var status string
	err := spinner.New().Title(label).Output(t.stderr).ActionWithErr(func(context.Context) (e error) {
		status, e = fn()
		return e
	}).Run()
	if err != nil {
		t.mark(false, label, "failed")
	} else {
		t.mark(true, label, status)
	}
	return status, err
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
