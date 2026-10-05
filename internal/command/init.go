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
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/provider"
	"github.com/Ownera1/rag-go/internal/store"
	"github.com/Ownera1/rag-go/internal/workspace"
	"golang.org/x/term"
)

func Initialize(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("rag init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	explicit := fs.String("workspace", "", "workspace root")
	docs := fs.String("docs", "documents", "single documents directory")
	kind := fs.String("embedding-type", "voyage", "voyage or openai")
	name := fs.String("model", "voyage-4-lite", "embedding model")
	dimensions := fs.Int("dimensions", 1024, "embedding dimensions")
	base := fs.String("base-url", "https://api.voyageai.com/v1", "embedding endpoint prefix")
	keyEnv := fs.String("api-key-env", "VOYAGE_API_KEY", "credential environment name; empty for no authentication")
	offline := fs.Bool("offline", false, "skip embedding probe")
	if err := fs.Parse(ReorderFlags(args, map[string]bool{"offline": true, "h": true, "help": true})); err != nil {
		return err
	}
	if fs.NArg() > 1 || fs.NArg() == 1 && *explicit != "" {
		return errors.New("usage: rag init [workspace] [--docs PATH]")
	}
	root := *explicit
	if fs.NArg() == 1 {
		root = fs.Arg(0)
	}
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	storeDir := workspace.Store(root)
	if err = os.MkdirAll(storeDir, 0700); err != nil {
		return err
	}
	if err = os.Chmod(storeDir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(storeDir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	lock.Close()
	release, err := workspace.Lock(ctx, root, true)
	if err != nil {
		return err
	}
	defer release()
	cfg := model.DefaultConfig()
	fresh := false
	path := filepath.Join(storeDir, "config.json")
	if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fresh = true
	} else if err != nil {
		return err
	} else {
		cfg, err = model.LoadConfig(path)
		if err != nil {
			return err
		}
	}
	values, err := workspace.Credentials(root)
	if err != nil {
		return err
	}
	supplied := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { supplied[f.Name] = true })
	inputFile, ok := in.(*os.File)
	interactive := ok && term.IsTerminal(int(inputFile.Fd()))
	reader := bufio.NewReader(in)
	ask := func(label, current string) (string, error) {
		fmt.Fprintf(stderr, "%s [%s]: ", label, current)
		s, e := reader.ReadString('\n')
		if e != nil && !errors.Is(e, io.EOF) {
			return "", e
		}
		if s = strings.TrimSpace(s); s == "" {
			s = current
		}
		return s, nil
	}
	if fresh && interactive {
		if !supplied["docs"] {
			*docs, err = ask("Documents directory", *docs)
			if err != nil {
				return err
			}
		}
		if !supplied["embedding-type"] {
			*kind, err = ask("Embedding provider (voyage/openai)", *kind)
			if err != nil {
				return err
			}
		}
		if *kind == "openai" {
			if !supplied["model"] {
				*name = ""
			}
			if !supplied["base-url"] {
				*base = ""
			}
			if !supplied["api-key-env"] {
				*keyEnv = ""
			}
		}
		if !supplied["model"] {
			*name, err = ask("Embedding model", *name)
			if err != nil {
				return err
			}
		}
		if *kind == "openai" {
			if !supplied["base-url"] {
				*base, err = ask("Embedding API prefix", *base)
				if err != nil {
					return err
				}
			}
			if !supplied["dimensions"] {
				v, e := ask("Embedding dimensions", strconv.Itoa(*dimensions))
				if e != nil {
					return e
				}
				*dimensions, err = strconv.Atoi(v)
				if err != nil {
					return err
				}
			}
			if !supplied["api-key-env"] {
				*keyEnv, err = ask("API key environment name", *keyEnv)
				if err != nil {
					return err
				}
			}
		}
	}
	if fresh || supplied["docs"] {
		cfg.Documents = *docs
	}
	if fresh {
		cfg.Embedding = model.ProviderConfig{Type: *kind, Model: *name, Dimensions: *dimensions, BaseURL: *base, APIKeyEnv: *keyEnv}
	} else {
		if supplied["embedding-type"] {
			cfg.Embedding.Type = *kind
		}
		if supplied["model"] {
			cfg.Embedding.Model = *name
		}
		if supplied["dimensions"] {
			cfg.Embedding.Dimensions = *dimensions
		}
		if supplied["base-url"] {
			cfg.Embedding.BaseURL = *base
		}
		if supplied["api-key-env"] {
			cfg.Embedding.APIKeyEnv = *keyEnv
		}
	}
	if err = cfg.Validate(); err != nil {
		return err
	}
	resolvedDocs := cfg.Documents
	if !filepath.IsAbs(resolvedDocs) {
		resolvedDocs = filepath.Join(root, resolvedDocs)
	}
	rel, _ := filepath.Rel(storeDir, resolvedDocs)
	if rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("documents cannot be inside .rag-go")
	}
	for _, p := range []model.ProviderConfig{cfg.Embedding, cfg.Reranker} {
		name := p.APIKeyEnv
		if name == "" {
			continue
		}
		if !workspace.EnvName.MatchString(name) {
			return errors.New("invalid apiKeyEnv")
		}
		value := os.Getenv(name)
		if value == "" {
			value = values[name]
		}
		if value == "" && interactive {
			fmt.Fprintf(stderr, "%s (hidden): ", name)
			b, e := term.ReadPassword(int(inputFile.Fd()))
			fmt.Fprintln(stderr)
			if e != nil {
				return e
			}
			value = string(b)
		}
		if value != "" {
			values[name] = value
		}
	}
	// Refuse unknown existing data before publishing any configuration changes.
	dbPath, e := store.ResolvePath(storeDir)
	if e != nil {
		return e
	}
	if _, e = os.Stat(dbPath); e == nil {
		db, e := store.Open(dbPath, true, 0)
		if e != nil {
			return e
		}
		version := db.GetMetadata(ctx, "go_storage_version")
		db.Close()
		if version != "1" {
			return errors.New("existing database is not a recognized Go store; initialize a separate workspace")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if err = os.MkdirAll(resolvedDocs, 0755); err != nil {
		return err
	}
	if err = workspace.AtomicJSON(path, cfg); err != nil {
		return err
	}
	if len(values) > 0 {
		if err = workspace.AtomicJSON(filepath.Join(storeDir, "credentials.json"), values); err != nil {
			return err
		}
	}
	if _, err = os.Stat(filepath.Join(storeDir, "state.json")); errors.Is(err, os.ErrNotExist) {
		if err = workspace.AtomicJSON(filepath.Join(storeDir, "state.json"), map[string]any{"inputs": map[string]string{}, "failedFiles": []any{}}); err != nil {
			return err
		}
	}
	if err = workspace.AtomicFile(filepath.Join(storeDir, ".gitignore"), []byte("*\n"), 0600); err != nil {
		return err
	}
	checks := map[string]string{"embedding": "not checked (offline)"}
	var problem error
	if !*offline {
		p, e := provider.NewHTTP(cfg.Embedding, cfg.HTTPTimeoutMs, 0)
		if e == nil {
			p.SetCredential(values[cfg.Embedding.APIKeyEnv])
			_, e = p.EmbedQuery(ctx, "rag-go initialization probe")
		}
		if e != nil {
			checks["embedding"] = "failed"
			problem = errors.New("embedding probe failed; check endpoint, credential, model and dimensions")
		} else {
			checks["embedding"] = "reachable; dimensions verified"
		}
	}
	if err = json.NewEncoder(out).Encode(map[string]any{"workspace": root, "documents": resolvedDocs, "created": fresh, "checks": checks}); err != nil {
		return err
	}
	return problem
}
