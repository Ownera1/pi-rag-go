package rag

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	gitignore "github.com/sabhiram/go-gitignore"

	"github.com/Ownera1/rag-go/internal/chunk"
	"github.com/Ownera1/rag-go/internal/document"
	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/store"
	"github.com/Ownera1/rag-go/internal/workspace"
)

var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, ".next": true, "dist": true, "build": true,
	"__pycache__": true, ".venv": true, "venv": true, ".cache": true,
}

var allowed = map[string]bool{
	".md": true, ".mdx": true, ".txt": true, ".rst": true, ".ts": true,
	".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".py": true, ".rs": true, ".go": true, ".java": true, ".kt": true,
	".kts": true, ".scala": true, ".c": true, ".cc": true, ".cpp": true,
	".cxx": true, ".h": true, ".hpp": true, ".hxx": true, ".cs": true,
	".fs": true, ".vb": true, ".swift": true, ".m": true, ".mm": true,
	".rb": true, ".php": true, ".pl": true, ".lua": true, ".dart": true,
	".ex": true, ".exs": true, ".erl": true, ".clj": true, ".cljs": true,
	".edn": true, ".vue": true, ".svelte": true, ".astro": true, ".css": true,
	".scss": true, ".sass": true, ".less": true, ".json": true, ".jsonc": true,
	".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".xml": true,
	".csv": true, ".tsv": true, ".sh": true, ".bash": true, ".zsh": true,
	".fish": true, ".ps1": true, ".sql": true, ".graphql": true, ".gql": true,
	".proto": true, ".env": true, ".gitignore": true, ".dockerfile": true, ".tf": true,
	".hcl": true, ".docx": true, ".html": true, ".htm": true, ".nxml": true,
}

func allowedFile(path string) bool {
	// Oversized supported inputs are reported by the bounded loader.
	return allowed[strings.ToLower(filepath.Ext(path))]
}

func scan(ctx context.Context, root string, patterns []string, ignoredRoots ...string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	for _, ignored := range ignoredRoots {
		if within(ignored, root) {
			return nil, fmt.Errorf("store contents cannot be tracked: %s", root)
		}
	}
	st, e := os.Stat(root)
	if e != nil {
		return nil, e
	}
	if st.Mode().IsRegular() {
		if allowedFile(root) {
			return []string{root}, nil
		}
		return nil, fmt.Errorf("unsupported file: %s", root)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("not a file or directory: %s", root)
	}
	ig := gitignore.CompileIgnoreLines(patterns...)
	found := []string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		for _, ignored := range ignoredRoots {
			if within(ignored, p) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if p == root {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if ig.MatchesPath(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if allowedFile(p) {
			found = append(found, p)
		}
		return nil
	})
	sort.Strings(found)
	if err != nil {
		return nil, err
	}
	return document.CanonicalFiles(ctx, found)
}

type work struct {
	doc    model.Document
	chunks []model.Chunk
	skip   bool
	err    error
	path   string
	stage  string
}

func (c *session) indexSnapshot(ctx context.Context, db *store.DB, snap sourceSnapshot, force bool) IndexResult {
	result := IndexResult{Errors: []string{}, Failures: []model.FileFailure{}}
	paths := []string{}
	for _, f := range snap.failures {
		addFailure(&result, f.Path, f.Stage, errors.New(f.Error))
	}
	for _, p := range snap.paths {
		if _, ok := snap.inputs[p]; ok {
			paths = append(paths, p)
		}
	}
	previous, e := db.List(ctx)
	if e != nil {
		addFailure(&result, c.docs, "store", e)
		return result
	}
	if len(paths) == 0 {
		return result
	}
	jobs := make(chan string)
	done := make(chan work, 32)
	sem := make(chan struct{}, c.cfg.Indexing.SemanticWorkers)
	var wg sync.WaitGroup
	workers := min(c.cfg.Indexing.Workers, len(paths))
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				if e := ctx.Err(); e != nil {
					done <- work{path: p, stage: "parse", err: e}
					continue
				}
				if !force {
					hash, embedded, e := db.FileHash(ctx, p)
					if e != nil {
						done <- work{path: p, stage: "store", err: e}
						continue
					}
					if hash == snap.inputs[p] && embedded {
						done <- work{path: p, skip: true}
						continue
					}
				}
				doc, e := document.Parse(ctx, p)
				if e != nil {
					done <- work{path: p, stage: "parse", err: e}
					continue
				}
				if doc.Hash != snap.inputs[p] {
					done <- work{path: p, stage: "parse", err: errors.New("document changed during loading")}
					continue
				}
				logical := p
				if filepath.Base(p) == "rag-source.json" || document.IsMinerUFile(p) {
					logical = filepath.Dir(p)
					for _, old := range previous {
						if old != p && within(logical, old) {
							doc.Replaces = append(doc.Replaces, old)
						}
					}
				} else {
					// Removing the package's canonical artifact can expose ordinary
					// files. Retire its old representation in the same transaction,
					// even when another document prevents global deletion cleanup.
					for _, old := range previous {
						if (filepath.Base(old) == "rag-source.json" || document.IsMinerUFile(old)) && within(filepath.Dir(old), p) {
							doc.Replaces = append(doc.Replaces, old)
						}
					}
				}
				rel, _ := filepath.Rel(c.docs, logical)
				doc.ID = document.ShortHash(rel)
				semantic := c.cfg.Chunking.Mode == "semantic" &&
					(doc.Format != "text" ||
						filepath.Base(p) == "rag-source.json" ||
						strings.HasSuffix(strings.ToLower(p), ".txt"))
				var chunks []model.Chunk
				if semantic {
					select {
					case sem <- struct{}{}:
					case <-ctx.Done():
						done <- work{path: p, stage: "chunk", err: ctx.Err()}
						continue
					}
					chunks, e = chunk.Semantic(ctx, doc.Blocks, c.embedder, c.cfg)
					<-sem
				} else if doc.Format != "text" {
					for _, b := range doc.Blocks {
						for _, ch := range chunk.Legacy([]model.Block{b}, c.cfg.Chunking) {
							ch.ChunkIndex = len(chunks)
							chunks = append(chunks, ch)
						}
					}
				} else {
					chunks = chunk.Legacy(doc.Blocks, c.cfg.Chunking)
				}
				done <- work{path: p, doc: doc, chunks: chunks, stage: "chunk", err: e}
			}
		}()
	}
	go func() {
		for _, p := range paths {
			jobs <- p
		}
		close(jobs)
		wg.Wait()
		close(done)
	}()
	for w := range done {
		if w.err != nil {
			addFailure(&result, w.path, w.stage, w.err)
			continue
		}
		if w.skip {
			result.Skipped++
			continue
		}
		if c.embedder == nil {
			addFailure(&result, w.path, "embed", errors.New("embedding provider unavailable"))
			continue
		}
		for i := range w.chunks {
			w.chunks[i].ID = w.doc.ID + "-" + fmt.Sprint(w.chunks[i].ChunkIndex)
			w.chunks[i].Hash = document.ShortHash(w.chunks[i].Content)
			w.chunks[i].Tokens = chunk.Estimate(w.chunks[i].Content)
			w.chunks[i].Path = w.doc.Path
			w.chunks[i].SourcePath = w.doc.SourcePath
			w.chunks[i].Title = w.doc.Title
			w.chunks[i].Format = w.doc.Format
			w.chunks[i].ParserVersion = w.doc.ParserVersion
		}
		texts := make([]string, len(w.chunks))
		for i, ch := range w.chunks {
			texts[i] = ch.Content
		}
		vectors, e := c.embedder.EmbedDocuments(ctx, texts)
		stage := "embed"
		if e == nil {
			stage = "input"
			hash, checkErr := document.InputFingerprint(ctx, w.path)
			e = checkErr
			if e == nil && hash != w.doc.Hash {
				e = errors.New("document changed during embedding; existing content retained")
			}
		}
		if e == nil {
			stage = "store"
			e = db.Replace(ctx, w.doc, w.chunks, vectors)
		}
		if e != nil {
			addFailure(&result, w.path, stage, e)
			continue
		}
		result.Indexed++
		result.Chunks += len(w.chunks)
	}
	return result
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

func (c *session) publish(ctx context.Context, stage *store.DB, stageDir string) error {
	st, e := stage.Stats(ctx)
	if e != nil {
		return e
	}
	if st.Chunks != st.Vectors {
		return fmt.Errorf("vector coverage %d/%d; refusing to publish", st.Vectors, st.Chunks)
	}
	if e = c.stamp(ctx, stage); e != nil {
		return e
	}
	_, _ = stage.SQL.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	if e = stage.Close(); e != nil {
		return e
	}
	emb, proc := fingerprint(c.cfg)
	id := emb[:10] + "-" + proc[:10]
	generation := randomID()
	rel := filepath.Join("indexes", id, generation, "rag.db")
	dest := filepath.Join(c.root, filepath.Dir(rel))
	if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		return e
	}
	if e = os.Rename(stageDir, dest); e != nil {
		return e
	}
	next, e := store.Open(filepath.Join(c.root, rel), false, c.cfg.Embedding.Dimensions)
	if e != nil {
		return e
	}
	manifest := struct {
		Version               int    `json:"version"`
		IndexID               string `json:"indexId"`
		RelativeDBPath        string `json:"relativeDbPath"`
		EmbeddingFingerprint  string `json:"embeddingFingerprint"`
		ProcessingFingerprint string `json:"processingFingerprint"`
		CreatedAt             string `json:"createdAt"`
	}{1, id, rel, emb, proc, time.Now().UTC().Format(time.RFC3339Nano)}
	b, e := json.MarshalIndent(manifest, "", "  ")
	if e != nil {
		next.Close()
		return e
	}
	if e = workspace.AtomicFile(filepath.Join(c.root, "active.json"), b, 0600); e != nil {
		next.Close()
		return e
	}
	old := c.db
	c.db = next
	if old != nil {
		return old.Close()
	}
	return nil
}

func (c *session) rebuild(ctx context.Context) (result IndexResult, err error) {
	snap := c.snapshot(ctx)
	stageDir := filepath.Join(c.root, "staging", randomID())
	db, err := store.Open(filepath.Join(stageDir, "rag.db"), false, c.cfg.Embedding.Dimensions)
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stageDir)
	defer db.Close()
	result = c.indexSnapshot(ctx, db, snap, true)
	after := c.snapshot(ctx)
	if result.Failed == 0 && after.signature != snap.signature {
		addFailure(&result, c.docs, "scan", errors.New("documents changed during rebuild"))
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if result.Failed > 0 {
		// Preserve successful inputs from the still-active index on a failed rebuild.
		if e := c.record(snap, result); e != nil {
			return result, e
		}
		return result, errors.New("rebuild failed; active index preserved")
	}
	if err = c.publish(ctx, db, stageDir); err != nil {
		return result, err
	}
	if _, err = c.reconcileZotero(ctx); err != nil {
		return result, err
	}
	return result, c.record(snap, result)
}
