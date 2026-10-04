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
	"strings"
	"sync"
	"syscall"

	"github.com/Ownera1/pi-rag-go/internal/model"
	"github.com/Ownera1/pi-rag-go/internal/provider"
	"github.com/Ownera1/pi-rag-go/internal/store"
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
type Progress = model.Progress
type CleanupResult = model.CleanupResult
type EmbeddingProvider = model.EmbeddingProvider
type Reranker = model.Reranker

type Options struct {
	StoreDir       string
	ConfigPath     string
	LegacyReadOnly bool
	Embedder       EmbeddingProvider
	Reranker       Reranker
}

type Core struct {
	root                string
	configPath          string
	cfg                 Config
	legacy              bool
	legacyProviderID    string
	legacyContract      string
	legacyProviderError error
	embedder            EmbeddingProvider
	reranker            Reranker
	mu                  sync.RWMutex
	writeMu             sync.Mutex
	db                  *store.DB
	lock                *os.File
	closed              bool
	stateMu             sync.RWMutex
	trackedPaths        []string
	failedFiles         []model.FileFailure
	progress            model.Progress
}

func DefaultConfig() Config { return model.DefaultConfig() }

func Open(opts Options) (*Core, error) {
	if opts.StoreDir == "" {
		return nil, errors.New("StoreDir is required")
	}
	root, e := filepath.Abs(opts.StoreDir)
	if e != nil {
		return nil, e
	}
	path := opts.ConfigPath
	if path == "" {
		path = filepath.Join(root, "config.json")
	}
	cfg := model.DefaultConfig()
	legacyProviderID := ""
	if _, err := os.Stat(path); err == nil {
		if opts.LegacyReadOnly {
			b, e := os.ReadFile(path)
			if e != nil {
				return nil, e
			}
			var saved struct {
				Embedding struct {
					Provider, Model string
					Dimensions      int
				}
				Reranker               struct{ Provider, Model string }
				RagAlpha               float64
				CandidateTopK, RagTopK int
				TrackedPaths           []string
			}
			if e = json.Unmarshal(b, &saved); e != nil {
				return nil, e
			}
			legacyProviderID = saved.Embedding.Provider
			cfg.Embedding.Model = saved.Embedding.Model
			cfg.Embedding.Dimensions = saved.Embedding.Dimensions
			if saved.CandidateTopK > 0 {
				cfg.CandidateTopK = saved.CandidateTopK
			}
			if saved.RagTopK > 0 {
				cfg.TopK = saved.RagTopK
			}
			if saved.RagAlpha >= 0 && saved.RagAlpha <= 1 {
				cfg.Alpha = saved.RagAlpha
			}
			cfg.TrackedPaths = saved.TrackedPaths
		} else {
			cfg, e = model.LoadConfig(path)
			if e != nil {
				return nil, e
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	c := &Core{
		root:             root,
		configPath:       path,
		cfg:              cfg,
		legacy:           opts.LegacyReadOnly,
		legacyProviderID: legacyProviderID,
		embedder:         opts.Embedder,
		reranker:         opts.Reranker,
		trackedPaths:     append([]string{}, cfg.TrackedPaths...),
		failedFiles:      []model.FileFailure{},
	}
	if opts.LegacyReadOnly {
		c.cfg.Embedding, c.legacyContract, c.legacyProviderError = legacyProvider(
			root, legacyProviderID, cfg.Embedding.Model, cfg.Embedding.Dimensions,
		)
		cfg = c.cfg
	}
	if !opts.LegacyReadOnly {
		if e = os.MkdirAll(root, 0700); e != nil {
			return nil, e
		}
		lockPath := filepath.Join(root, ".ragd.lock")
		c.lock, e = os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return nil, e
		}
		if e = syscall.Flock(int(c.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
			c.lock.Close()
			return nil, fmt.Errorf("store is already owned by another writer: %w", e)
		}
	}
	dbPath, e := store.ResolvePath(root)
	if e != nil {
		c.Close()
		return nil, e
	}
	if _, e = os.Stat(dbPath); e == nil {
		if !opts.LegacyReadOnly {
			probe, probeErr := store.Open(dbPath, true, 0)
			if probeErr != nil {
				c.Close()
				return nil, fmt.Errorf("inspect existing store: %w", probeErr)
			}
			version := probe.GetMetadata(context.Background(), "go_storage_version")
			probe.Close()
			if version != "1" {
				c.Close()
				return nil, errors.New("existing store is not a Go v1 store; use --legacy-readonly or a separate store directory")
			}
		}
		c.db, e = store.Open(dbPath, opts.LegacyReadOnly, cfg.Embedding.Dimensions)
		if e != nil {
			c.Close()
			return nil, e
		}
	} else if !errors.Is(e, os.ErrNotExist) || opts.LegacyReadOnly {
		c.Close()
		return nil, e
	}
	if e = c.loadState(); e != nil {
		c.Close()
		return nil, e
	}
	if c.embedder == nil && cfg.Embedding.Type != "local" && (!c.legacy || c.legacyProviderError == nil) {
		c.embedder, e = provider.NewHTTP(cfg.Embedding, cfg.HTTPTimeoutMs, cfg.HTTPMaxRetries, cfg.Indexing.EmbeddingBatchSize)
		if e != nil {
			c.Close()
			return nil, e
		}
	}
	if c.reranker == nil && cfg.Reranker.Type != "none" {
		c.reranker, e = provider.NewHTTP(cfg.Reranker, cfg.HTTPTimeoutMs, cfg.HTTPMaxRetries, cfg.Indexing.EmbeddingBatchSize)
		if e != nil {
			c.Close()
			return nil, e
		}
	}
	return c, nil
}

func (c *Core) Close() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	var e error
	c.closed = true
	if c.db != nil {
		e = c.db.Close()
		c.db = nil
	}
	if c.lock != nil {
		_ = syscall.Flock(int(c.lock.Fd()), syscall.LOCK_UN)
		_ = c.lock.Close()
		c.lock = nil
	}
	return e
}

func (c *Core) Config() Config {
	cfg := c.cfg
	cfg.TrackedPaths = c.tracked()
	cfg.ExcludePatterns = append([]string{}, cfg.ExcludePatterns...)
	return cfg
}

func (c *Core) writable() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return errors.New("core is closed")
	}
	if c.legacy {
		return errors.New("legacy store is read-only")
	}
	return nil
}

func (c *Core) ensureDB() error {
	c.mu.RLock()
	closed, exists := c.closed, c.db != nil
	c.mu.RUnlock()
	if closed {
		return errors.New("core is closed")
	}
	if exists {
		return nil
	}
	if c.legacy {
		return errors.New("legacy store is read-only")
	}
	path := filepath.Join(c.root, "rag.db")
	db, e := store.Open(path, false, c.cfg.Embedding.Dimensions)
	if e != nil {
		return e
	}
	c.mu.Lock()
	c.db = db
	c.mu.Unlock()
	return c.stamp(context.Background(), db)
}

func fingerprint(cfg Config) (string, string) {
	emb, _ := json.Marshal(struct {
		Type, Model string
		Dimensions  int
		BaseURL     string
	}{cfg.Embedding.Type, cfg.Embedding.Model, cfg.Embedding.Dimensions, cfg.Embedding.BaseURL})
	proc, _ := json.Marshal(struct {
		Parser   string
		Search   string
		Chunking model.ChunkingConfig
	}{"go-blocks-tei-v2", "han-ngrams-v1", cfg.Chunking})
	eh := sha256.Sum256(emb)
	ph := sha256.Sum256(proc)
	return hex.EncodeToString(eh[:]), hex.EncodeToString(ph[:])
}

func (c *Core) stamp(ctx context.Context, d *store.DB) error {
	emb, proc := fingerprint(c.cfg)
	if e := d.SetMetadata(ctx, "embedding_fingerprint", emb); e != nil {
		return e
	}
	if e := d.SetMetadata(ctx, "processing_fingerprint", proc); e != nil {
		return e
	}
	if e := d.SetMetadata(ctx, "embedding_model", c.cfg.Embedding.Model); e != nil {
		return e
	}
	if e := d.SetMetadata(ctx, "embedding_dimensions", fmt.Sprint(c.cfg.Embedding.Dimensions)); e != nil {
		return e
	}
	return d.SetMetadata(ctx, "go_storage_version", "1")
}

func (c *Core) compatible(ctx context.Context, d *store.DB) error {
	if c.legacy {
		return nil
	}
	emb, proc := fingerprint(c.cfg)
	if got := d.GetMetadata(ctx, "embedding_fingerprint"); got != "" && got != emb {
		return errors.New("embedding contract changed; rebuild required")
	}
	if got := d.GetMetadata(ctx, "processing_fingerprint"); got != "" && got != proc {
		return errors.New("processing contract changed; rebuild required")
	}
	return nil
}

func (c *Core) Status(ctx context.Context) (Status, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	paths, failures, progress := c.stateSnapshot()
	s := Status{StoreDir: c.root, ReadOnly: c.legacy, TrackedPaths: paths, FailedFiles: failures, Progress: progress}
	if c.db == nil {
		return s, nil
	}
	x, e := c.db.Stats(ctx)
	if e != nil {
		return s, e
	}
	x.StoreDir = c.root
	x.TrackedPaths = s.TrackedPaths
	x.FailedFiles = failures
	x.Progress = progress
	if err := c.compatible(ctx, c.db); err != nil {
		x.NeedsRebuild = true
		x.RebuildReason = err.Error()
	}
	if c.legacy && strings.Contains(c.db.GetMetadata(ctx, "embedding_fingerprint"), `"provider":"local"`) {
		x.RebuildReason = "local MiniLM vectors require the old model; use explicit bm25 mode"
	}
	return x, nil
}

func (c *Core) ListDocuments(ctx context.Context) ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.db == nil {
		return []string{}, nil
	}
	return c.db.List(ctx)
}
