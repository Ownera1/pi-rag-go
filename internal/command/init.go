package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/store"
	"github.com/Ownera1/rag-go/internal/workspace"
	"github.com/Ownera1/rag-go/pkg/rag"
)

func Initialize(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("rag init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	explicit := fs.String("workspace", "", "workspace root")
	docs := fs.String("docs", "documents", "single documents directory")
	providerOptions := addProviderFlags(fs)
	offline := fs.Bool("offline", false, "skip embedding probe and initial indexing")
	noSync := fs.Bool("no-sync", false, "skip initial indexing of existing documents")
	if err := fs.Parse(ReorderFlags(args, map[string]bool{"offline": true, "no-sync": true, "h": true, "help": true})); err != nil {
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
	// Released early before the initial sync, which takes its own lock.
	locked := true
	defer func() {
		if locked {
			release()
		}
	}()
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
	// A new workspace copies the user-wide defaults from rag install, so later
	// global changes never invalidate an existing index.
	global, installed, err := workspace.GlobalConfig()
	if err != nil {
		return err
	}
	if fresh && installed {
		cfg = global
	}
	local, err := workspace.LocalCredentials(root)
	if err != nil {
		return err
	}
	shared, err := workspace.GlobalCredentials()
	if err != nil {
		return err
	}
	supplied := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { supplied[f.Name] = true })
	t := newTerminal(in, stderr)
	if fresh && t.interactive && !installed {
		if !supplied["docs"] {
			if *docs, err = t.ask("Documents directory", *docs); err != nil {
				return err
			}
		}
		if err = providerOptions.prompt(t, supplied); err != nil {
			return err
		}
	}
	if fresh || supplied["docs"] {
		cfg.Documents = *docs
	}
	providerOptions.apply(&cfg.Embedding, fresh && !installed, supplied)
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
	keys := map[string]string{}
	for _, p := range []model.ProviderConfig{cfg.Embedding, cfg.Reranker} {
		name := p.APIKeyEnv
		if name == "" {
			continue
		}
		if !workspace.EnvName.MatchString(name) {
			return errors.New("invalid apiKeyEnv")
		}
		// Persist environment and typed keys in the workspace for agents that
		// do not inherit the shell environment, unless rag install holds one.
		// Queries send the environment and user-wide keys only to a trusted
		// endpoint, so any other endpoint keeps its own copy.
		trusted, err := workspace.TrustedEndpoint(p)
		if err != nil {
			return err
		}
		value := os.Getenv(name)
		if value != "" && (shared[name] == "" || !trusted) {
			local[name] = value
		}
		if value == "" {
			value = local[name]
		}
		if value == "" && trusted {
			value = shared[name]
		}
		if value == "" && t.interactive {
			if value, err = t.secret(name); err != nil {
				return err
			}
			if value != "" {
				local[name] = value
			}
		}
		keys[name] = value
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
	if len(local) > 0 {
		if err = workspace.AtomicJSON(filepath.Join(storeDir, "credentials.json"), local); err != nil {
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
		checks["embedding"], problem = t.spin("Embedding endpoint", func() (string, error) { return probe(ctx, cfg, keys[cfg.Embedding.APIKeyEnv]) })
	}
	release()
	locked = false
	report := map[string]any{"workspace": root, "documents": resolvedDocs, "created": fresh, "checks": checks}
	if problem == nil && !*offline && !*noSync && hasDocuments(resolvedDocs) {
		core, e := rag.Open(rag.Options{WorkspaceDir: root})
		if e != nil {
			return e
		}
		r, e := core.Sync(ctx)
		core.Close()
		report["sync"] = r
		if e != nil {
			report["syncError"] = e.Error()
			problem = e
		}
	}
	if err = json.NewEncoder(out).Encode(report); err != nil {
		return err
	}
	if fresh && !installed {
		fmt.Fprintln(stderr, "Tip: run `rag install` once to reuse these settings in every project and connect Claude Code and Codex.")
	}
	return problem
}

// hasDocuments reports whether dir holds any visible file to index.
func hasDocuments(dir string) bool {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	found := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != dir && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() && !strings.HasPrefix(d.Name(), ".") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
