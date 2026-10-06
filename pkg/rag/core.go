// Package rag provides a workspace-scoped retrieval engine shared by CLI and MCP.
package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/Ownera1/rag-go/internal/catalog"
	"github.com/Ownera1/rag-go/internal/chunk"
	"github.com/Ownera1/rag-go/internal/document"
	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/provider"
	"github.com/Ownera1/rag-go/internal/store"
	"github.com/Ownera1/rag-go/internal/workspace"
)

type Block = model.Block
type Document = model.Document
type Chunk = model.Chunk
type Hit = model.Hit
type QueryOptions = model.QueryOptions
type QueryResult = model.QueryResult
type QueryUsage = model.QueryUsage
type IndexResult = model.IndexResult
type Status = model.Status
type Config = model.Config
type ProviderConfig = model.ProviderConfig
type ChunkingConfig = model.ChunkingConfig
type IndexingConfig = model.IndexingConfig
type FileFailure = model.FileFailure
type CleanupResult = model.CleanupResult
type EmbeddingProvider = model.EmbeddingProvider
type Reranker = model.Reranker

type Options struct {
	WorkspaceDir string
	ReadOnly     bool
	Embedder     EmbeddingProvider
	Reranker     Reranker
}

// Core retains immutable options only. Each call obtains a new locked session.
// Injected providers must support concurrent calls when Core is used concurrently.
type Core struct {
	opts   Options
	closed atomic.Bool
}

type session struct {
	bibliography *catalog.DB
	workspace    string
	root         string
	docs         string
	cfg          Config
	state        savedState
	stamps       map[string]inputStamp
	db           *store.DB
	embedder     EmbeddingProvider
	reranker     Reranker
	readOnly     bool
	release      func()
}

func DefaultConfig() Config { return model.DefaultConfig() }

func Open(opts Options) (*Core, error) {
	root, err := workspace.Discover(opts.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	if _, err = model.LoadConfig(filepath.Join(workspace.Store(root), "config.json")); err != nil {
		return nil, err
	}
	opts.WorkspaceDir = root
	return &Core{opts: opts}, nil
}

func (c *Core) Close() error { c.closed.Store(true); return nil }

func (c *Core) ReadOnly() bool { return c.opts.ReadOnly }

func (c *Core) WorkspaceDir() string { return c.opts.WorkspaceDir }

func (c *Core) operation(ctx context.Context, write bool) (*session, error) {
	if c.closed.Load() {
		return nil, errors.New("core is closed")
	}
	if write && c.opts.ReadOnly {
		return nil, errors.New("workspace is read-only")
	}
	release, err := workspace.Lock(ctx, c.opts.WorkspaceDir, write)
	if err != nil {
		return nil, err
	}
	s := &session{workspace: c.opts.WorkspaceDir, root: workspace.Store(c.opts.WorkspaceDir), readOnly: !write, release: release}
	fail := func(err error) (*session, error) { s.close(); return nil, err }
	s.cfg, err = model.LoadConfig(filepath.Join(s.root, "config.json"))
	if err != nil {
		return fail(err)
	}
	s.docs = s.cfg.Documents
	if !filepath.IsAbs(s.docs) {
		s.docs = filepath.Join(s.workspace, s.docs)
	}
	s.docs = filepath.Clean(s.docs)
	if within(s.root, s.docs) {
		return fail(errors.New("documents cannot be inside .rag-go"))
	}
	s.state, err = loadState(s.root)
	if err != nil {
		return fail(err)
	}
	s.stamps = s.state.Fingerprints
	dbPath, err := store.ResolvePath(s.root)
	if err != nil {
		return fail(err)
	}
	if _, err = os.Stat(dbPath); err == nil {
		// Inspect before any schema initialization, including writable operations.
		probe, e := store.Open(dbPath, true, 0)
		if e != nil {
			return fail(e)
		}
		version := probe.GetMetadata(ctx, "go_storage_version")
		probe.Close()
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if version != "1" {
			return fail(errors.New("existing database is not a recognized Go store; initialize a separate workspace and rebuild"))
		}
		s.db, err = store.Open(dbPath, !write, s.cfg.Embedding.Dimensions)
		if err != nil {
			return fail(err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	credentials, err := workspace.Credentials(s.workspace)
	if err != nil {
		return fail(err)
	}
	s.embedder, s.reranker = c.opts.Embedder, c.opts.Reranker
	if s.embedder == nil {
		p, e := provider.NewHTTP(s.cfg.Embedding, s.cfg.HTTPTimeoutMs, s.cfg.HTTPMaxRetries, s.cfg.Indexing.EmbeddingBatchSize)
		if e != nil {
			return fail(e)
		}
		p.SetCredential(credentials[s.cfg.Embedding.APIKeyEnv])
		s.embedder = p
	}
	if s.reranker == nil && s.cfg.Reranker.Type != "none" {
		p, e := provider.NewHTTP(s.cfg.Reranker, s.cfg.HTTPTimeoutMs, s.cfg.HTTPMaxRetries)
		if e != nil {
			return fail(e)
		}
		p.SetCredential(credentials[s.cfg.Reranker.APIKeyEnv])
		s.reranker = p
	}
	return s, nil
}

func (s *session) close() {
	if s.bibliography != nil {
		_ = s.bibliography.Close()
		s.bibliography = nil
	}
	if s.db != nil {
		_ = s.db.Close()
		s.db = nil
	}
	if s.release != nil {
		s.release()
		s.release = nil
	}
}

func (s *session) writable() error {
	if s.readOnly {
		return errors.New("workspace is read-only")
	}
	return nil
}

func (s *session) ensureDB(ctx context.Context) error {
	if s.db != nil {
		return nil
	}
	db, err := store.Open(filepath.Join(s.root, "rag.db"), false, s.cfg.Embedding.Dimensions)
	if err != nil {
		return err
	}
	s.db = db
	if err = s.stamp(ctx, db); err != nil {
		// This database did not exist when the exclusive operation began. A
		// canceled initial stamp must not leave an unrecognized partial store.
		_ = db.Close()
		s.db = nil
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if e := os.Remove(db.Path + suffix); e != nil && !errors.Is(e, os.ErrNotExist) {
				err = errors.Join(err, e)
			}
		}
		return err
	}
	return nil
}

func fingerprint(cfg Config) (string, string) {
	emb, _ := json.Marshal(struct {
		Type, Model string
		Dimensions  int
		BaseURL     string
	}{cfg.Embedding.Type, cfg.Embedding.Model, cfg.Embedding.Dimensions, cfg.Embedding.BaseURL})
	proc, _ := json.Marshal(struct {
		Parser, Search, Chunker, Embed string
		Chunking                       model.ChunkingConfig
	}{document.ParserVersion, "han-ngrams-heading-v1", chunk.Version, "title-section-v1", cfg.Chunking})
	eh, ph := sha256.Sum256(emb), sha256.Sum256(proc)
	return hex.EncodeToString(eh[:]), hex.EncodeToString(ph[:])
}

func (s *session) stamp(ctx context.Context, d *store.DB) error {
	emb, proc := fingerprint(s.cfg)
	values := map[string]string{"go_storage_version": "1", "embedding_fingerprint": emb, "processing_fingerprint": proc, "embedding_model": s.cfg.Embedding.Model, "embedding_dimensions": fmt.Sprint(s.cfg.Embedding.Dimensions), "documents_root": s.docs}
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range values {
		if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO metadata(key,value) VALUES(?,?)", k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *session) compatible(ctx context.Context, d *store.DB) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d == nil {
		return nil
	}
	emb, proc := fingerprint(s.cfg)
	actualEmb := d.GetMetadata(ctx, "embedding_fingerprint")
	actualProc := d.GetMetadata(ctx, "processing_fingerprint")
	actualDocs := d.GetMetadata(ctx, "documents_root")
	if err := ctx.Err(); err != nil {
		return err
	}
	if actualEmb != emb {
		return errors.New("embedding contract changed; rebuild required")
	}
	if actualProc != proc {
		return errors.New("processing contract changed; rebuild required")
	}
	if actualDocs != s.docs {
		return errors.New("documents directory changed; rebuild required")
	}
	return ctx.Err()
}

func (c *Core) Sync(ctx context.Context) (IndexResult, error) { return c.sync(ctx, false) }

func (c *Core) sync(ctx context.Context, automatic bool) (IndexResult, error) {
	s, err := c.operation(ctx, true)
	if err != nil {
		return IndexResult{}, err
	}
	defer s.close()
	if err = s.compatible(ctx, s.db); err != nil {
		return IndexResult{}, err
	}
	snap := s.snapshot(ctx)
	if ctx.Err() != nil {
		return IndexResult{}, ctx.Err()
	}
	if automatic && !s.needsSync(snap) {
		return IndexResult{Skipped: len(snap.inputs)}, nil
	}
	if automatic && snap.signature == s.state.LastAttemptSignature && len(s.state.FailedFiles) > 0 && time.Since(s.state.LastAttemptAt) < 60*time.Second {
		r := s.state.LastSync
		if r == nil {
			r = &IndexResult{Failures: s.state.FailedFiles, Failed: len(s.state.FailedFiles)}
		}
		return *r, errors.New("automatic sync retry is cooling down; run rag sync to retry immediately")
	}
	if err = s.ensureDB(ctx); err != nil {
		return IndexResult{}, err
	}
	r := s.indexSnapshot(ctx, s.db, snap, false)
	// Only a complete, stable scan can remove deleted documents.
	after := s.snapshot(ctx)
	if r.Failed == 0 && ctx.Err() == nil && after.signature == snap.signature && len(after.failures) == 0 {
		indexed, e := s.db.List(ctx)
		if e != nil {
			return r, e
		}
		for _, path := range indexed {
			if _, ok := snap.inputs[path]; !ok {
				if e = s.db.Delete(ctx, path); e != nil {
					return r, e
				}
				r.Removed++
			}
		}
	} else if r.Failed == 0 && ctx.Err() == nil {
		addFailure(&r, s.docs, "scan", errors.New("documents changed during sync; retry after input settles"))
	}
	if err = s.record(snap, r); err != nil {
		return r, err
	}
	if r.Failed == 0 && ctx.Err() == nil {
		if _, err = s.reconcileZotero(ctx); err != nil {
			return r, err
		}
	}
	if ctx.Err() != nil {
		return r, ctx.Err()
	}
	if r.Failed > 0 {
		return r, errors.New("sync incomplete; existing documents retained")
	}
	return r, nil
}

func (c *Core) Query(ctx context.Context, text string, opts QueryOptions) (out QueryResult, err error) {
	started := time.Now()
	defer func() { out.ElapsedMs = float64(time.Since(started).Microseconds()) / 1000 }()
	s, err := c.operation(ctx, false)
	if err != nil {
		return out, err
	}
	plan, err := validateQuery(s.cfg, text, opts, s.reranker != nil)
	if err != nil {
		s.close()
		return out, err
	}
	if opts.Filter != nil {
		db, e := s.openCatalog(false)
		if e != nil || db == nil {
			s.close()
			if e != nil {
				return out, e
			}
			return out, errors.New("metadata filter requires a catalog; run rag zotero sync")
		}
	}
	if err = s.compatible(ctx, s.db); err != nil {
		s.close()
		return out, err
	}
	snap := s.snapshot(ctx)
	var synced *IndexResult
	syncError := ""
	if s.needsSync(snap) && !c.opts.ReadOnly && !opts.DisableSync {
		s.close()
		r, e := c.sync(ctx, true)
		synced = &r
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if e != nil {
			syncError = e.Error()
		}
		s, err = c.operation(ctx, false)
		if err != nil {
			return out, err
		}
		// Defaults come from the configuration reloaded by this operation.
		if plan, err = validateQuery(s.cfg, text, opts, s.reranker != nil); err != nil {
			s.close()
			return out, err
		}
		snap = s.snapshot(ctx)
	}
	defer s.close()
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if err = s.compatible(ctx, s.db); err != nil {
		return out, err
	}
	if syncError != "" {
		if s.db == nil {
			return out, errors.New(syncError)
		}
		stats, e := s.db.Stats(ctx)
		if e != nil {
			return out, e
		}
		if stats.Files == 0 {
			return out, errors.New(syncError)
		}
	}
	out, err = s.query(ctx, text, opts, plan)
	out.Freshness = "fresh"
	if s.needsSync(snap) {
		out.Freshness = "stale"
	}
	if len(snap.failures) > 0 {
		out.Freshness = "unknown"
		if syncError == "" {
			syncError = snap.failures[0].Error
		}
	}
	out.Sync, out.SyncError = synced, syncError
	return out, err
}

func (c *Core) Status(ctx context.Context) (Status, error) {
	s, err := c.operation(ctx, false)
	if err != nil {
		return Status{}, err
	}
	defer s.close()
	status := Status{WorkspaceDir: s.workspace, StoreDir: s.root, DocumentsRoot: s.docs, ReadOnly: c.opts.ReadOnly, FailedFiles: s.state.FailedFiles, LastSync: s.state.LastSync, EmbeddingModel: s.cfg.Embedding.Model, Dimensions: s.cfg.Embedding.Dimensions}
	cat, e := s.openCatalog(false)
	if e != nil {
		return status, e
	}
	if cat != nil {
		v, e := cat.Status(ctx)
		if e != nil {
			return status, e
		}
		status.Zotero = &v
	}
	if !s.state.LastAttemptAt.IsZero() {
		status.LastAttemptAt = s.state.LastAttemptAt.UTC().Format(time.RFC3339Nano)
	}
	if s.db != nil {
		x, e := s.db.Stats(ctx)
		if e != nil {
			return status, e
		}
		status.Files, status.Chunks, status.Vectors, status.ActiveDB = x.Files, x.Chunks, x.Vectors, x.ActiveDB
	}
	if e := s.compatible(ctx, s.db); e != nil {
		status.NeedsRebuild = true
		status.RebuildReason = e.Error()
	}
	snap := s.snapshot(ctx)
	status.NeedsSync = s.needsSync(snap)
	if len(snap.failures) > 0 {
		status.FreshnessError = snap.failures[0].Error
	}
	return status, ctx.Err()
}

func (c *Core) ListDocuments(ctx context.Context) ([]string, error) {
	s, err := c.operation(ctx, false)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if s.db == nil {
		return []string{}, nil
	}
	return s.db.List(ctx)
}

func (c *Core) Cleanup(ctx context.Context, keep int, dryRun bool) (CleanupResult, error) {
	s, err := c.operation(ctx, true)
	if err != nil {
		return CleanupResult{}, err
	}
	defer s.close()
	return s.cleanup(ctx, keep, dryRun)
}

func (c *Core) Rebuild(ctx context.Context) (IndexResult, error) {
	s, err := c.operation(ctx, true)
	if err != nil {
		return IndexResult{}, err
	}
	defer s.close()
	return s.rebuild(ctx)
}
