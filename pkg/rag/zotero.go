package rag

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Ownera1/rag-go/internal/catalog"
	"github.com/Ownera1/rag-go/internal/document"
	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/zotero"
)

type ZoteroConfig = model.ZoteroConfig
type ZoteroReference = model.ZoteroReference
type ZoteroMetadata = model.ZoteroMetadata
type MetadataFilter = model.MetadataFilter
type ZoteroSyncResult = model.ZoteroSyncResult
type ZoteroStatus = model.ZoteroStatus
type ZoteroMatchResult = catalog.MatchResult

func (s *session) documentReferences(ctx context.Context) ([]model.CatalogDocument, error) {
	docs, err := s.db.Documents(ctx)
	if err != nil {
		return nil, err
	}
	for i, v := range docs {
		if !strings.HasPrefix(v.Key, "path:") {
			continue
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		parsed, e := document.Parse(ctx, v.Path)
		if e != nil {
			continue
		}
		hash, _, e := s.db.FileHash(ctx, v.Path)
		if e != nil {
			return nil, e
		}
		if hash != parsed.Hash {
			continue
		}
		if err = s.db.SetDocumentIdentity(ctx, parsed); err != nil {
			return nil, err
		}
		docs[i] = model.CatalogDocument{Key: parsed.DocumentKey, Path: parsed.Path, SourcePath: parsed.SourcePath, Title: parsed.Title, Zotero: parsed.Zotero, DOI: parsed.DOI}
	}
	return docs, nil
}

func (s *session) openCatalog(write bool) (*catalog.DB, error) {
	if s.bibliography != nil {
		return s.bibliography, nil
	}
	path := filepath.Join(s.root, "catalog.db")
	if !write {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
	}
	db, err := catalog.Open(path, !write)
	if err != nil {
		return nil, err
	}
	s.bibliography = db
	return db, nil
}

// zoteroTitles uses its own read-only handle: openCatalog caches its first
// mode, and indexing is followed by a read-write reconcile.
func (s *session) zoteroTitles(ctx context.Context) (map[string]string, error) {
	path := filepath.Join(s.root, "catalog.db")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	} else if err != nil {
		return nil, err
	}
	db, err := catalog.Open(path, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return db.Titles(ctx)
}

func (s *session) zoteroConfig() ZoteroConfig {
	if s.cfg.Zotero != nil {
		return s.cfg.Zotero.Defaults()
	}
	return (ZoteroConfig{}).Defaults()
}

func (c *Core) SyncZotero(ctx context.Context, override *ZoteroConfig) (ZoteroSyncResult, error) {
	r := ZoteroSyncResult{Orphans: []string{}}
	s, err := c.operation(ctx, true)
	if err != nil {
		return r, err
	}
	defer s.close()
	cfg := s.zoteroConfig()
	if override != nil {
		cfg = override.Defaults()
	}
	client, err := zotero.New(cfg)
	if err != nil {
		return r, err
	}
	snapshot, err := client.Fetch(ctx)
	if err != nil {
		return r, err
	}
	db, err := s.openCatalog(true)
	if err != nil {
		return r, err
	}
	r, err = db.Apply(ctx, snapshot)
	if err != nil {
		return r, err
	}
	if s.db != nil {
		docs, e := s.documentReferences(ctx)
		if e != nil {
			return r, e
		}
		matched, e := db.Match(ctx, docs, snapshot.LibraryType, snapshot.LibraryID, false)
		if e != nil {
			return r, e
		}
		r.Linked, r.Unmatched = matched.Linked, matched.Unmatched
	}
	status, err := db.Status(ctx)
	r.Orphans = status.Orphans
	return r, err
}

func (c *Core) ZoteroStatus(ctx context.Context) (ZoteroStatus, error) {
	s, err := c.operation(ctx, false)
	if err != nil {
		return ZoteroStatus{}, err
	}
	defer s.close()
	db, err := s.openCatalog(false)
	if err != nil {
		return ZoteroStatus{}, err
	}
	if db == nil {
		return ZoteroStatus{Orphans: []string{}}, nil
	}
	return db.Status(ctx)
}

func (s *session) reconcileZotero(ctx context.Context, includeCandidates ...bool) (ZoteroMatchResult, error) {
	if s.db == nil {
		return ZoteroMatchResult{}, nil
	}
	if _, err := os.Stat(filepath.Join(s.root, "catalog.db")); errors.Is(err, os.ErrNotExist) {
		return ZoteroMatchResult{}, nil
	} else if err != nil {
		return ZoteroMatchResult{}, err
	}
	db, err := s.openCatalog(true)
	if err != nil {
		return ZoteroMatchResult{}, err
	}
	docs, err := s.documentReferences(ctx)
	if err != nil {
		return ZoteroMatchResult{}, err
	}
	cfg := s.zoteroConfig()
	candidates := len(includeCandidates) > 0 && includeCandidates[0]
	return db.Match(ctx, docs, cfg.LibraryType, cfg.LibraryID, candidates)
}

func (c *Core) MatchZotero(ctx context.Context, overrides ...*ZoteroConfig) (ZoteroMatchResult, error) {
	s, err := c.operation(ctx, true)
	if err != nil {
		return ZoteroMatchResult{}, err
	}
	defer s.close()
	if _, err := os.Stat(filepath.Join(s.root, "catalog.db")); errors.Is(err, os.ErrNotExist) {
		return ZoteroMatchResult{}, errors.New("run rag zotero sync before matching")
	}
	if len(overrides) > 0 && overrides[0] != nil {
		cfg := overrides[0].Defaults()
		if err = cfg.Validate(); err != nil {
			return ZoteroMatchResult{}, err
		}
		db, e := s.openCatalog(true)
		if e != nil {
			return ZoteroMatchResult{}, e
		}
		if s.db == nil {
			return ZoteroMatchResult{Candidates: []catalog.Candidate{}}, nil
		}
		docs, e := s.documentReferences(ctx)
		if e != nil {
			return ZoteroMatchResult{}, e
		}
		return db.Match(ctx, docs, cfg.LibraryType, cfg.LibraryID, true)
	}
	return s.reconcileZotero(ctx, true)
}

func (c *Core) LinkZotero(ctx context.Context, path string, ref ZoteroReference, writeManifest bool) (*ZoteroMetadata, error) {
	s, err := c.operation(ctx, true)
	if err != nil {
		return nil, err
	}
	defer s.close()
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.workspace, path)
	}
	path = filepath.Clean(path)
	if err = ref.Validate(); err != nil {
		return nil, err
	}
	if writeManifest {
		if filepath.Base(path) != "rag-source.json" || !within(s.docs, path) {
			return nil, errors.New("--write-manifest requires a rag-source.json inside documents")
		}
		real, e := filepath.EvalSymlinks(path)
		if e != nil {
			return nil, e
		}
		root, e := filepath.EvalSymlinks(s.docs)
		if e != nil {
			return nil, e
		}
		if !within(root, real) || real != path {
			return nil, errors.New("cannot write an external or symlinked manifest")
		}
		if _, e = document.Parse(ctx, path); e != nil {
			return nil, e
		}
	}
	var doc model.CatalogDocument
	if s.db != nil {
		docs, e := s.documentReferences(ctx)
		if e != nil {
			return nil, e
		}
		for _, v := range docs {
			if v.Path == path {
				doc = v
				break
			}
		}
	}
	if doc.Key == "" {
		v, e := document.Parse(ctx, path)
		if e != nil {
			return nil, e
		}
		doc = model.CatalogDocument{Key: v.DocumentKey, Path: v.Path, SourcePath: v.SourcePath, Title: v.Title, Zotero: v.Zotero, DOI: v.DOI}
	}
	db, err := s.openCatalog(true)
	if err != nil {
		return nil, err
	}
	if err = db.Link(ctx, doc, ref, "manual", true); err != nil {
		return nil, err
	}
	m, err := db.Metadata(ctx, doc.Key)
	if err != nil {
		return nil, err
	}
	if writeManifest {
		if err = document.WriteZoteroReference(path, m.ZoteroReference); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (c *Core) ZoteroLinks(ctx context.Context) ([]map[string]any, error) {
	s, err := c.operation(ctx, false)
	if err != nil {
		return nil, err
	}
	defer s.close()
	db, err := s.openCatalog(false)
	if err != nil {
		return nil, err
	}
	if db == nil {
		return []map[string]any{}, nil
	}
	return db.ExportLinks(ctx)
}

func (s *session) queryMetadata(ctx context.Context, f *MetadataFilter) (bool, string, error) {
	db, err := s.openCatalog(false)
	if err != nil {
		return false, "", err
	}
	if db == nil {
		if f != nil {
			return false, "", errors.New("metadata filter requires a catalog; run rag zotero sync")
		}
		return false, "", nil
	}
	status, err := db.Status(ctx)
	if err != nil {
		return false, "", err
	}
	if f == nil {
		return false, status.SyncedAt, nil
	}
	keys, err := db.AllowedDocuments(ctx, f)
	if err != nil {
		return false, "", err
	}
	if s.db != nil {
		if err = s.db.SetDocumentFilter(ctx, keys); err != nil {
			return false, "", err
		}
	}
	return true, status.SyncedAt, nil
}

func (s *session) enrichMetadata(ctx context.Context, hits []Hit) error {
	if s.bibliography == nil || len(hits) == 0 {
		return nil
	}
	docs, err := s.db.Documents(ctx)
	if err != nil {
		return err
	}
	keys := map[string]string{}
	for _, v := range docs {
		keys[v.Path] = v.Key
	}
	cache := map[string]*ZoteroMetadata{}
	for i := range hits {
		key := keys[hits[i].Chunk.Path]
		m, ok := cache[key]
		if !ok {
			m, err = s.bibliography.Metadata(ctx, key)
			if err != nil {
				return err
			}
			cache[key] = m
		}
		hits[i].Metadata = m
	}
	return nil
}
