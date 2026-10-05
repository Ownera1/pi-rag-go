package command

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Ownera1/rag-go/internal/provider"
	"github.com/Ownera1/rag-go/pkg/rag"
	"golang.org/x/term"
)

func Initialize(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) error {
	fs := flag.NewFlagSet("rag init", flag.ContinueOnError)
	fs.SetOutput(errout)
	store := fs.String("store", DefaultStore(), "store directory")
	embedding := fs.String("embedding-type", "voyage", "voyage or openai")
	modelName := fs.String("model", "voyage-4-lite", "embedding model")
	dimensions := fs.Int("dimensions", 1024, "embedding dimensions")
	base := fs.String("base-url", "https://api.voyageai.com/v1", "embedding API prefix")
	keyEnv := fs.String("api-key-env", "VOYAGE_API_KEY", "credential environment variable; empty for unauthenticated provider")
	backend := fs.String("pdf-backend", "", "pdftotext, grobid or mineru")
	url := fs.String("pdf-url", "http://127.0.0.1:8070", "GROBID URL")
	converter := fs.String("pdf-command", "", "converter executable path")
	listen := fs.String("listen", "127.0.0.1:7331", "loopback service address")
	offline := fs.Bool("offline", false, "skip live embedding probe")
	enable := fs.Bool("enable-auto-refresh", false, "enable automatic refresh on an existing store")
	legacy := fs.Bool("legacy-readonly", false, "inspect an existing TypeScript store without initializing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected init arguments")
	}
	root, err := filepath.Abs(*store)
	if err != nil {
		return err
	}
	if *legacy {
		core, err := rag.Open(rag.Options{StoreDir: root, LegacyReadOnly: true})
		if err != nil {
			return err
		}
		defer core.Close()
		status, err := core.Status(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(status)
	}
	cfg, err := settings(root)
	if err != nil {
		return err
	}
	_, statErr := os.Stat(filepath.Join(root, "config.json"))
	fresh := errors.Is(statErr, os.ErrNotExist)
	interactive := false
	inputFile, ok := in.(*os.File)
	if ok {
		interactive = term.IsTerminal(int(inputFile.Fd()))
	}
	reader := bufio.NewReader(in)
	ask := func(label, fallback string) (string, error) {
		fmt.Fprintf(errout, "%s [%s]: ", label, fallback)
		s, e := reader.ReadString('\n')
		if e != nil && !errors.Is(e, io.EOF) {
			return "", e
		}
		s = strings.TrimSpace(s)
		if s == "" {
			s = fallback
		}
		return s, nil
	}
	if fresh {
		if interactive {
			if *backend == "" {
				*backend, err = ask("PDF backend (pdftotext/grobid/mineru)", "pdftotext")
				if err != nil {
					return err
				}
			}
		}
		cfg.Embedding = rag.ProviderConfig{Type: *embedding, Model: *modelName, Dimensions: *dimensions, BaseURL: *base, APIKeyEnv: *keyEnv}
		cfg.Runtime.Listen = *listen
		cfg.Runtime.AutoRefresh.Enabled = true
		cfg.Runtime.AutoRefresh.DebounceMs = 3000
		cfg.Runtime.AutoRefresh.RescanMs = 300000
	}
	if fresh || *enable {
		cfg.Runtime.AutoRefresh.Enabled = true
	}
	if *backend != "" {
		cfg.Runtime.PDF.Backend = *backend
		cfg.Runtime.PDF.URL = *url
		cfg.Runtime.PDF.Command = *converter
		cfg.Runtime.PDF.TimeoutMs = 600000
	}
	if err = cfg.Validate(); err != nil {
		return err
	}
	addr := cfg.Runtime.Listen
	if addr == "" {
		addr = "127.0.0.1:7331"
	}
	if err = validateListen(addr); err != nil {
		return err
	}
	// Probe the existing store before writing config or credentials. This also enforces the legacy marker.
	core, err := rag.Open(rag.Options{StoreDir: root, Embedder: nil})
	if err != nil {
		return err
	}
	core.Close()
	values, err := loadCredentials(root)
	if err != nil {
		return err
	}
	for _, p := range []rag.ProviderConfig{cfg.Embedding, cfg.Reranker} {
		name := p.APIKeyEnv
		if name == "" {
			continue
		}
		if !envName.MatchString(name) {
			return errors.New("invalid apiKeyEnv")
		}
		value := os.Getenv(name)
		if value == "" {
			value = values[name]
		}
		if value == "" && interactive {
			fmt.Fprintf(errout, "%s (hidden): ", name)
			b, e := term.ReadPassword(int(inputFile.Fd()))
			fmt.Fprintln(errout)
			if e != nil {
				return e
			}
			value = string(b)
		}
		if value != "" {
			values[name] = value
		}
	}
	checks := map[string]string{}
	problems := []string{}
	switch cfg.Runtime.PDF.Backend {
	case "pdftotext", "mineru":
		name := cfg.Runtime.PDF.Command
		if name == "" {
			name = "pdftotext"
			if cfg.Runtime.PDF.Backend == "mineru" {
				name = "mineru-kit"
			}
		}
		path, e := exec.LookPath(name)
		if e != nil {
			checks["pdf"] = "missing " + name
			problems = append(problems, "PDF converter is missing: "+name)
		} else {
			cfg.Runtime.PDF.Command = path
			checks["pdf"] = "executable available"
		}
	case "grobid":
		checks["pdf"] = "configured; service probe follows"
	case "":
		checks["pdf"] = "not configured"
		problems = append(problems, "select a PDF backend with --pdf-backend")
	default:
		return errors.New("pdf-backend must be pdftotext, grobid or mineru")
	}
	if fresh || *enable || *backend != "" {
		if err = atomicJSON(filepath.Join(root, "config.json"), cfg); err != nil {
			return err
		}
	}
	if len(values) > 0 {
		if err = atomicJSON(filepath.Join(root, "credentials.json"), values); err != nil {
			return err
		}
	}
	if err = applyCredentials(root); err != nil {
		return err
	}
	if *offline {
		checks["embedding"] = "not checked (offline)"
	} else {
		p, e := provider.NewHTTP(cfg.Embedding, cfg.HTTPTimeoutMs, 0)
		if e == nil {
			_, e = p.EmbedQuery(ctx, "rag-go initialization probe")
		}
		if e != nil {
			checks["embedding"] = "failed"
			problems = append(problems, "embedding probe failed; check endpoint, credential, model and dimensions")
		} else {
			checks["embedding"] = "reachable; dimensions verified"
		}
	}
	if cfg.Runtime.PDF.Backend == "grobid" {
		if err = probeGrobid(ctx, cfg.Runtime.PDF.URL); err != nil {
			checks["pdf"] = "unreachable"
			problems = append(problems, "GROBID service is unavailable")
		} else {
			checks["pdf"] = "reachable"
		}
	}
	if err = json.NewEncoder(out).Encode(map[string]any{"store": root, "created": fresh, "checks": checks, "problems": problems}); err != nil {
		return err
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
