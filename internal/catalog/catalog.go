// Package catalog owns durable bibliographic data, independently of index generations.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/zotero"
	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	SQL      *sql.DB
	ReadOnly bool
}

func Open(path string, readOnly bool) (*DB, error) {
	if !readOnly {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("_foreign_keys", "1")
	q.Set("_busy_timeout", "5000")
	if readOnly {
		q.Set("mode", "ro")
		q.Set("_query_only", "1")
	} else {
		q.Set("mode", "rwc")
	}
	u.RawQuery = q.Encode()
	s, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	s.SetMaxOpenConns(1)
	d := &DB{s, readOnly}
	if err = s.Ping(); err == nil {
		var version int
		err = s.QueryRow("PRAGMA user_version").Scan(&version)
		if err == nil && version > 1 {
			err = errors.New("catalog schema is newer than this rag-go")
		}
		if err == nil && readOnly && version != 1 {
			err = errors.New("catalog requires migration; run rag zotero sync")
		}
		if err == nil && version == 0 {
			var tables int
			err = s.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables)
			if err == nil && tables != 0 {
				err = errors.New("existing database is not a recognized Zotero catalog")
			}
		}
		if err == nil && !readOnly {
			err = d.migrate()
		}
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) Close() error { return d.SQL.Close() }

func (d *DB) migrate() error {
	if _, err := d.SQL.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return err
	}
	tx, err := d.SQL.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
CREATE TABLE IF NOT EXISTS zotero_libraries (
    library_type TEXT NOT NULL CHECK(library_type IN ('user','group')),
    library_id TEXT NOT NULL, requested_id TEXT NOT NULL,
    PRIMARY KEY(library_type,library_id)
);
CREATE TABLE IF NOT EXISTS zotero_items (
    library_type TEXT NOT NULL, library_id TEXT NOT NULL, item_key TEXT NOT NULL,
    version INTEGER NOT NULL, item_type TEXT NOT NULL, title TEXT NOT NULL,
    abstract TEXT NOT NULL, date_raw TEXT NOT NULL, year INTEGER,
    publication TEXT NOT NULL, doi TEXT NOT NULL, citation_key TEXT NOT NULL,
    parent_key TEXT NOT NULL, link_mode TEXT NOT NULL, filename TEXT NOT NULL, attachment_path TEXT NOT NULL,
    content_hash TEXT NOT NULL, raw_json TEXT NOT NULL, deleted INTEGER NOT NULL DEFAULT 0 CHECK(deleted IN (0,1)),
    PRIMARY KEY(library_type,library_id,item_key),
    FOREIGN KEY(library_type,library_id) REFERENCES zotero_libraries(library_type,library_id)
);
CREATE INDEX IF NOT EXISTS idx_zotero_year ON zotero_items(year);
CREATE INDEX IF NOT EXISTS idx_zotero_doi ON zotero_items(doi);
CREATE INDEX IF NOT EXISTS idx_zotero_citation ON zotero_items(citation_key);
CREATE INDEX IF NOT EXISTS idx_zotero_attachment ON zotero_items(attachment_path);
CREATE TABLE IF NOT EXISTS zotero_creators (
    library_type TEXT NOT NULL, library_id TEXT NOT NULL, item_key TEXT NOT NULL, position INTEGER NOT NULL,
    creator_type TEXT NOT NULL, family TEXT NOT NULL, given TEXT NOT NULL, name TEXT NOT NULL,
    PRIMARY KEY(library_type,library_id,item_key,position),
    FOREIGN KEY(library_type,library_id,item_key) REFERENCES zotero_items ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS zotero_tags (
    library_type TEXT NOT NULL, library_id TEXT NOT NULL, item_key TEXT NOT NULL, tag TEXT NOT NULL, type INTEGER NOT NULL,
    PRIMARY KEY(library_type,library_id,item_key,tag,type),
    FOREIGN KEY(library_type,library_id,item_key) REFERENCES zotero_items ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_zotero_tag ON zotero_tags(tag);
CREATE TABLE IF NOT EXISTS zotero_collections (
    library_type TEXT NOT NULL, library_id TEXT NOT NULL, collection_key TEXT NOT NULL,
    parent_key TEXT NOT NULL, name TEXT NOT NULL, version INTEGER NOT NULL,
    content_hash TEXT NOT NULL, raw_json TEXT NOT NULL, deleted INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(library_type,library_id,collection_key),
    FOREIGN KEY(library_type,library_id) REFERENCES zotero_libraries
);
CREATE TABLE IF NOT EXISTS zotero_item_collections (
    library_type TEXT NOT NULL, library_id TEXT NOT NULL, item_key TEXT NOT NULL, collection_key TEXT NOT NULL,
    PRIMARY KEY(library_type,library_id,item_key,collection_key),
    FOREIGN KEY(library_type,library_id,item_key) REFERENCES zotero_items ON DELETE CASCADE,
    FOREIGN KEY(library_type,library_id,collection_key) REFERENCES zotero_collections
);
CREATE TABLE IF NOT EXISTS catalog_documents (
    document_key TEXT PRIMARY KEY, last_path TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS document_zotero_links (
    document_key TEXT PRIMARY KEY REFERENCES catalog_documents,
    library_type TEXT NOT NULL, library_id TEXT NOT NULL, item_key TEXT NOT NULL, attachment_key TEXT NOT NULL,
    match_method TEXT NOT NULL, locked INTEGER NOT NULL CHECK(locked IN (0,1)),
    FOREIGN KEY(library_type,library_id,item_key) REFERENCES zotero_items
);
CREATE TABLE IF NOT EXISTS zotero_sync_state (
    library_type TEXT NOT NULL, library_id TEXT NOT NULL, synced_at TEXT NOT NULL,
    PRIMARY KEY(library_type,library_id), FOREIGN KEY(library_type,library_id) REFERENCES zotero_libraries
);
PRAGMA user_version=1;
`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Apply commits only a complete snapshot. Missing items are soft-deleted; links
// remain recoverable, including locked links to deleted/trashed parent items.
func (d *DB) Apply(ctx context.Context, s zotero.Snapshot) (r model.ZoteroSyncResult, err error) {
	r = model.ZoteroSyncResult{LibraryType: s.LibraryType, LibraryID: s.LibraryID, Fetched: len(s.Items), Orphans: []string{}}
	if d.ReadOnly {
		return r, errors.New("read-only catalog")
	}
	if s.LibraryID == "0" && s.RequestedID == "0" {
		id, e := d.ResolveLibrary(ctx, s.LibraryType, "0")
		if e != nil {
			return r, e
		}
		if id != "0" {
			return r, errors.New("Zotero user/0 snapshot has no library identity; retry with the explicit library ID to confirm an empty library")
		}
	}
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer func() {
		_ = tx.Rollback()
		if err != nil {
			r.Updated, r.Unchanged, r.Deleted = 0, 0, 0
			r.SyncedAt = ""
		}
	}()
	if _, err = tx.ExecContext(ctx, `INSERT INTO zotero_libraries VALUES (?,?,?)
ON CONFLICT(library_type,library_id) DO UPDATE SET requested_id=excluded.requested_id`, s.LibraryType, s.LibraryID, s.RequestedID); err != nil {
		return r, err
	}
	if _, err = tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS seen_items (key TEXT PRIMARY KEY);
CREATE TEMP TABLE IF NOT EXISTS seen_collections (key TEXT PRIMARY KEY);
DELETE FROM seen_items; DELETE FROM seen_collections;`); err != nil {
		return r, err
	}
	for _, v := range s.Collections {
		if _, err = tx.ExecContext(ctx, "INSERT INTO temp.seen_collections VALUES (?)", v.Key); err != nil {
			return r, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO zotero_collections VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(library_type,library_id,collection_key) DO UPDATE SET parent_key=excluded.parent_key,name=excluded.name,
version=excluded.version,content_hash=excluded.content_hash,raw_json=excluded.raw_json,deleted=excluded.deleted`, s.LibraryType, s.LibraryID, v.Key, v.ParentKey, v.Name, v.Version, v.Hash, v.Raw, v.Deleted)
		if err != nil {
			return r, err
		}
	}
	for _, v := range s.Items {
		m := v.Metadata
		if _, err = tx.ExecContext(ctx, "INSERT INTO temp.seen_items VALUES (?)", v.Key); err != nil {
			return r, err
		}
		var old string
		var deleted bool
		err = tx.QueryRowContext(ctx, "SELECT content_hash,deleted FROM zotero_items WHERE library_type=? AND library_id=? AND item_key=?", s.LibraryType, s.LibraryID, v.Key).Scan(&old, &deleted)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
		changed := old != v.Hash || deleted != m.Deleted
		_, err = tx.ExecContext(ctx, `INSERT INTO zotero_items
(library_type,library_id,item_key,version,item_type,title,abstract,date_raw,year,publication,doi,citation_key,
parent_key,link_mode,filename,attachment_path,content_hash,raw_json,deleted)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(library_type,library_id,item_key) DO UPDATE SET version=excluded.version,item_type=excluded.item_type,
title=excluded.title,abstract=excluded.abstract,date_raw=excluded.date_raw,year=excluded.year,publication=excluded.publication,
doi=excluded.doi,citation_key=excluded.citation_key,parent_key=excluded.parent_key,link_mode=excluded.link_mode,
filename=excluded.filename,attachment_path=excluded.attachment_path,content_hash=excluded.content_hash,
raw_json=excluded.raw_json,deleted=excluded.deleted`, s.LibraryType, s.LibraryID, v.Key, v.Version, m.ItemType, m.Title, m.Abstract, m.Date, m.Year, m.Publication, m.DOI, m.CitationKey, v.ParentKey, v.LinkMode, v.Filename, v.Path, v.Hash, v.Raw, m.Deleted)
		if err != nil {
			return r, err
		}
		if !changed {
			r.Unchanged++
			continue
		}
		r.Updated++
		if m.Deleted && !deleted && old != "" {
			r.Deleted++
		}
		for _, table := range []string{"zotero_creators", "zotero_tags", "zotero_item_collections"} {
			if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE library_type=? AND library_id=? AND item_key=?", s.LibraryType, s.LibraryID, v.Key); err != nil {
				return r, err
			}
		}
		for i, v := range m.Creators {
			if _, err = tx.ExecContext(ctx, "INSERT INTO zotero_creators VALUES (?,?,?,?,?,?,?,?)", s.LibraryType, s.LibraryID, m.ItemKey, i, v.Type, v.Family, v.Given, v.Name); err != nil {
				return r, err
			}
		}
		for _, v := range m.Tags {
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO zotero_tags VALUES (?,?,?,?,?)", s.LibraryType, s.LibraryID, m.ItemKey, v.Tag, v.Type); err != nil {
				return r, err
			}
		}
		for _, v := range m.Collections {
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO zotero_item_collections VALUES (?,?,?,?)", s.LibraryType, s.LibraryID, m.ItemKey, v); err != nil {
				return r, err
			}
		}
	}
	res, err := tx.ExecContext(ctx, "UPDATE zotero_items SET deleted=1 WHERE library_type=? AND library_id=? AND deleted=0 AND item_key NOT IN (SELECT key FROM temp.seen_items)", s.LibraryType, s.LibraryID)
	if err != nil {
		return r, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return r, err
	}
	r.Deleted += int(n)
	if _, err = tx.ExecContext(ctx, "UPDATE zotero_collections SET deleted=1 WHERE library_type=? AND library_id=? AND collection_key NOT IN (SELECT key FROM temp.seen_collections)", s.LibraryType, s.LibraryID); err != nil {
		return r, err
	}
	r.SyncedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, "INSERT INTO zotero_sync_state VALUES (?,?,?) ON CONFLICT(library_type,library_id) DO UPDATE SET synced_at=excluded.synced_at", s.LibraryType, s.LibraryID, r.SyncedAt); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	return r, nil
}

func (d *DB) ResolveLibrary(ctx context.Context, kind, id string) (string, error) {
	var resolved string
	err := d.SQL.QueryRowContext(ctx, `SELECT l.library_id FROM zotero_libraries l JOIN zotero_sync_state s USING(library_type,library_id)
WHERE l.library_type=? AND (l.library_id=? OR l.requested_id=?) ORDER BY s.synced_at DESC LIMIT 1`, kind, id, id).Scan(&resolved)
	if errors.Is(err, sql.ErrNoRows) {
		return id, nil
	}
	return resolved, err
}

func (d *DB) Link(ctx context.Context, doc model.CatalogDocument, ref model.ZoteroReference, method string, locked bool) error {
	if d.ReadOnly {
		return errors.New("read-only catalog")
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	id, err := d.ResolveLibrary(ctx, ref.LibraryType, ref.LibraryID)
	if err != nil {
		return err
	}
	ref.LibraryID = id
	var itemType string
	err = d.SQL.QueryRowContext(ctx, "SELECT item_type FROM zotero_items WHERE library_type=? AND library_id=? AND item_key=?", ref.LibraryType, ref.LibraryID, ref.ItemKey).Scan(&itemType)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("Zotero item is not in the catalog; sync its library first")
	}
	if err != nil {
		return err
	}
	if itemType == "attachment" || itemType == "note" || itemType == "annotation" {
		return errors.New("link to a bibliographic parent item")
	}
	if ref.AttachmentKey != "" {
		var parent string
		if err = d.SQL.QueryRowContext(ctx, "SELECT parent_key FROM zotero_items WHERE library_type=? AND library_id=? AND item_key=? AND item_type='attachment'", ref.LibraryType, id, ref.AttachmentKey).Scan(&parent); err != nil {
			return err
		}
		if parent != ref.ItemKey {
			return errors.New("attachment does not belong to the selected parent")
		}
	}
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO catalog_documents VALUES (?,?) ON CONFLICT(document_key) DO UPDATE SET last_path=excluded.last_path", doc.Key, doc.Path); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO document_zotero_links VALUES (?,?,?,?,?,?,?)
ON CONFLICT(document_key) DO UPDATE SET library_type=excluded.library_type,library_id=excluded.library_id,
item_key=excluded.item_key,attachment_key=excluded.attachment_key,match_method=excluded.match_method,locked=excluded.locked
WHERE document_zotero_links.locked=0 OR ?=1`, doc.Key, ref.LibraryType, id, ref.ItemKey, ref.AttachmentKey, method, locked, locked)
	if err != nil {
		return err
	}
	return tx.Commit()
}

type Candidate struct {
	DocumentKey string                `json:"documentKey"`
	Path        string                `json:"path"`
	Reference   model.ZoteroReference `json:"reference"`
	Title       string                `json:"title"`
	Score       float64               `json:"score"`
}
type MatchResult struct {
	Linked     int         `json:"linked"`
	Unmatched  int         `json:"unmatched"`
	Candidates []Candidate `json:"candidates"`
}

func (d *DB) Match(ctx context.Context, docs []model.CatalogDocument, kind, id string, includeCandidates ...bool) (MatchResult, error) {
	r := MatchResult{Candidates: []Candidate{}}
	id, err := d.ResolveLibrary(ctx, kind, id)
	if err != nil {
		return r, err
	}
	for _, doc := range docs {
		var locked bool
		var previousKind, previousID, previousItem, previousAttachment, previousMethod string
		err = d.SQL.QueryRowContext(ctx, "SELECT locked,library_type,library_id,item_key,attachment_key,match_method FROM document_zotero_links WHERE document_key=?", doc.Key).Scan(&locked, &previousKind, &previousID, &previousItem, &previousAttachment, &previousMethod)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
		// A manifest's reference follows the manifest; only a manual link stays.
		if locked && previousMethod == "explicit" && (doc.Zotero == nil || doc.Zotero.LibraryType != previousKind ||
			doc.Zotero.ItemKey != previousItem || doc.Zotero.AttachmentKey != previousAttachment) {
			if _, err = d.SQL.ExecContext(ctx, "DELETE FROM document_zotero_links WHERE document_key=?", doc.Key); err != nil {
				return r, err
			}
			locked, previousKind, previousID = false, "", ""
		}
		if locked {
			if _, err = d.SQL.ExecContext(ctx, "UPDATE catalog_documents SET last_path=? WHERE document_key=?", doc.Path, doc.Key); err != nil {
				return r, err
			}
			r.Linked++
			continue
		}
		// A one-off sync of another library must not erase its automatic links
		// when normal document synchronization uses the configured library.
		if previousKind != "" && (previousKind != kind || previousID != id) && doc.Zotero == nil {
			r.Linked++
			continue
		}
		ref := doc.Zotero
		method := "explicit"
		lock := true
		if ref == nil {
			column, value := "attachment_path", cleanPath(doc.SourcePath)
			if value == "" {
				column, value = "filename", convertedFrom(doc.Path)
			}
			method = "attachment_" + column
			lock = false
			rows, e := d.SQL.QueryContext(ctx, `SELECT a.item_key,a.parent_key FROM zotero_items a JOIN zotero_items p
ON p.library_type=a.library_type AND p.library_id=a.library_id AND p.item_key=a.parent_key
WHERE a.library_type=? AND a.library_id=? AND a.item_type='attachment' AND a.deleted=0 AND p.deleted=0
AND a.`+column+`<>'' AND a.`+column+`=? ORDER BY a.parent_key,a.item_key`, kind, id, value)
			if e != nil {
				return r, e
			}
			refs := []model.ZoteroReference{}
			for rows.Next() {
				v := model.ZoteroReference{LibraryType: kind, LibraryID: id}
				if e = rows.Scan(&v.AttachmentKey, &v.ItemKey); e != nil {
					rows.Close()
					return r, e
				}
				refs = append(refs, v)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return r, e
			}
			if same, e := d.sameWork(ctx, kind, id, refs); e != nil {
				return r, e
			} else if same {
				ref = &refs[0]
			}
		}
		if ref == nil && zotero.NormalizeDOI(doc.DOI) != "" {
			rows, e := d.SQL.QueryContext(ctx, "SELECT item_key FROM zotero_items WHERE library_type=? AND library_id=? AND doi=? AND deleted=0 AND item_type NOT IN ('attachment','note','annotation') ORDER BY item_key", kind, id, zotero.NormalizeDOI(doc.DOI))
			if e != nil {
				return r, e
			}
			refs := []model.ZoteroReference{}
			for rows.Next() {
				v := model.ZoteroReference{LibraryType: kind, LibraryID: id}
				if e = rows.Scan(&v.ItemKey); e != nil {
					rows.Close()
					return r, e
				}
				refs = append(refs, v)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return r, e
			}
			if same, e := d.sameWork(ctx, kind, id, refs); e != nil {
				return r, e
			} else if same {
				ref = &refs[0]
				method = "doi"
				lock = false
			}
		}
		if ref != nil {
			if err = d.Link(ctx, doc, *ref, method, lock); err != nil {
				return r, fmt.Errorf("link %s: %w", doc.Path, err)
			}
			r.Linked++
		} else {
			// Re-matching cannot retain an obsolete automatic association.
			if _, err = d.SQL.ExecContext(ctx, "DELETE FROM document_zotero_links WHERE document_key=? AND locked=0", doc.Key); err != nil {
				return r, err
			}
			r.Unmatched++
			if len(includeCandidates) == 0 || includeCandidates[0] {
				cs, e := d.titleCandidates(ctx, doc, kind, id)
				if e != nil {
					return r, e
				}
				r.Candidates = append(r.Candidates, cs...)
			}
		}
	}
	return r, nil
}

// convertedSuffix is the "-<uuid>" MinerU Desktop appends to the source file
// name when naming its output folder.
var convertedSuffix = regexp.MustCompile(`-[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// convertedFrom returns the source file name encoded in the nearest MinerU
// output folder above path, e.g. "paper.pdf" for ".../paper.pdf-<uuid>/x.json".
// ponytail: exact byte match; an NFC/NFD mismatch in accented names will not link.
func convertedFrom(path string) string {
	for dir := filepath.Dir(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if name := filepath.Base(dir); convertedSuffix.MatchString(name) {
			return convertedSuffix.ReplaceAllString(name, "")
		}
	}
	return ""
}

func cleanPath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

// sameWork reports whether refs name one paper: a single item, or Zotero
// duplicates that share a DOI and a title. Callers order refs, so the first
// one is a stable pick.
// ponytail: the pick is the lowest key, not the most complete duplicate.
func (d *DB) sameWork(ctx context.Context, kind, id string, refs []model.ZoteroReference) (bool, error) {
	if len(refs) < 2 {
		return len(refs) == 1, nil
	}
	var first string
	for i, ref := range refs {
		var doi, title string
		if err := d.SQL.QueryRowContext(ctx, "SELECT doi,title FROM zotero_items WHERE library_type=? AND library_id=? AND item_key=?", kind, id, ref.ItemKey).Scan(&doi, &title); err != nil {
			return false, err
		}
		doi, title = zotero.NormalizeDOI(doi), strings.Join(titleFields(title), " ")
		if doi == "" || title == "" {
			return false, nil
		}
		if i == 0 {
			first = doi + "\x00" + title
		} else if doi+"\x00"+title != first {
			return false, nil
		}
	}
	return true, nil
}

func titleFields(s string) []string {
	return strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, s))
}

func titleWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range titleFields(s) {
		out[w] = true
	}
	return out
}
func (d *DB) titleCandidates(ctx context.Context, doc model.CatalogDocument, kind, id string) ([]Candidate, error) {
	out := []Candidate{}
	words := titleWords(doc.Title)
	if len(words) < 3 {
		return out, nil
	}
	rows, err := d.SQL.QueryContext(ctx, "SELECT item_key,title FROM zotero_items WHERE library_type=? AND library_id=? AND deleted=0 AND item_type NOT IN ('attachment','note','annotation')", kind, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, title string
		if err = rows.Scan(&key, &title); err != nil {
			return nil, err
		}
		other := titleWords(title)
		overlap := 0
		for w := range words {
			if other[w] {
				overlap++
			}
		}
		score := float64(overlap) / float64(max(len(words), len(other)))
		if score >= 0.8 {
			out = append(out, Candidate{doc.Key, doc.Path, model.ZoteroReference{LibraryType: kind, LibraryID: id, ItemKey: key}, title, score})
		}
	}
	return out, rows.Err()
}

func (d *DB) AllowedDocuments(ctx context.Context, f *model.MetadataFilter) ([]string, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	q := `SELECT l.document_key FROM document_zotero_links l JOIN zotero_items i USING(library_type,library_id,item_key) WHERE i.deleted=0`
	args := []any{}
	if f.YearFrom != nil {
		q += " AND i.year>=?"
		args = append(args, *f.YearFrom)
	}
	if f.YearTo != nil {
		q += " AND i.year<=?"
		args = append(args, *f.YearTo)
	}
	if f.LibraryType != "" {
		q += " AND i.library_type=?"
		args = append(args, f.LibraryType)
	}
	if f.LibraryID != "" {
		q += " AND i.library_id=?"
		args = append(args, f.LibraryID)
	}
	for _, tag := range f.Tags {
		q += ` AND EXISTS (SELECT 1 FROM zotero_tags t WHERE t.library_type=i.library_type AND t.library_id=i.library_id AND t.item_key=i.item_key AND t.tag=?)`
		args = append(args, tag)
	}
	for _, col := range f.Collections {
		q += ` AND EXISTS (SELECT 1 FROM zotero_item_collections c JOIN zotero_collections z USING(library_type,library_id,collection_key)
WHERE c.library_type=i.library_type AND c.library_id=i.library_id AND c.item_key=i.item_key AND c.collection_key=? AND z.deleted=0)`
		args = append(args, col)
	}
	rows, err := d.SQL.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func (d *DB) Metadata(ctx context.Context, key string) (*model.ZoteroMetadata, error) {
	m := &model.ZoteroMetadata{Creators: []model.ZoteroCreator{}, Tags: []model.ZoteroTag{}, Collections: []string{}}
	var year sql.NullInt64
	err := d.SQL.QueryRowContext(ctx, `SELECT l.library_type,l.library_id,l.item_key,l.attachment_key,l.match_method,l.locked,
i.item_type,i.title,i.abstract,i.date_raw,i.year,i.publication,i.doi,i.citation_key,i.deleted
FROM document_zotero_links l JOIN zotero_items i USING(library_type,library_id,item_key) WHERE l.document_key=?`, key).Scan(&m.LibraryType, &m.LibraryID, &m.ItemKey, &m.AttachmentKey, &m.MatchMethod, &m.Locked, &m.ItemType, &m.Title, &m.Abstract, &m.Date, &year, &m.Publication, &m.DOI, &m.CitationKey, &m.Deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if year.Valid {
		n := int(year.Int64)
		m.Year = &n
	}
	m.Orphan = m.Deleted
	args := []any{m.LibraryType, m.LibraryID, m.ItemKey}
	rows, err := d.SQL.QueryContext(ctx, "SELECT creator_type,family,given,name FROM zotero_creators WHERE library_type=? AND library_id=? AND item_key=? ORDER BY position", args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v model.ZoteroCreator
		if err = rows.Scan(&v.Type, &v.Family, &v.Given, &v.Name); err != nil {
			rows.Close()
			return nil, err
		}
		m.Creators = append(m.Creators, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = d.SQL.QueryContext(ctx, "SELECT tag,type FROM zotero_tags WHERE library_type=? AND library_id=? AND item_key=? ORDER BY tag,type", args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v model.ZoteroTag
		if err = rows.Scan(&v.Tag, &v.Type); err != nil {
			rows.Close()
			return nil, err
		}
		m.Tags = append(m.Tags, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = d.SQL.QueryContext(ctx, "SELECT collection_key FROM zotero_item_collections WHERE library_type=? AND library_id=? AND item_key=? ORDER BY collection_key", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			return nil, err
		}
		m.Collections = append(m.Collections, v)
	}
	return m, rows.Err()
}

// Titles maps each linked document key to its Zotero item title.
func (d *DB) Titles(ctx context.Context) (map[string]string, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT l.document_key,i.title FROM document_zotero_links l
JOIN zotero_items i USING(library_type,library_id,item_key) WHERE i.title<>''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, title string
		if err = rows.Scan(&key, &title); err != nil {
			return nil, err
		}
		out[key] = title
	}
	return out, rows.Err()
}

func (d *DB) Status(ctx context.Context) (model.ZoteroStatus, error) {
	s := model.ZoteroStatus{Orphans: []string{}}
	for q, p := range map[string]*int{"SELECT COUNT(*) FROM zotero_items WHERE deleted=0 AND item_type NOT IN ('attachment','note','annotation')": &s.Items, "SELECT COUNT(*) FROM zotero_items WHERE deleted=0 AND item_type='attachment'": &s.Attachments, "SELECT COUNT(*) FROM document_zotero_links": &s.Links, "SELECT COUNT(*) FROM document_zotero_links WHERE locked=1": &s.Locked} {
		if err := d.SQL.QueryRowContext(ctx, q).Scan(p); err != nil {
			return s, err
		}
	}
	var synced sql.NullString
	if err := d.SQL.QueryRowContext(ctx, "SELECT MAX(synced_at) FROM zotero_sync_state").Scan(&synced); err != nil {
		return s, err
	}
	s.SyncedAt = synced.String
	rows, err := d.SQL.QueryContext(ctx, `SELECT l.document_key FROM document_zotero_links l JOIN zotero_items i USING(library_type,library_id,item_key) WHERE i.deleted=1 ORDER BY l.document_key`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			return s, err
		}
		s.Orphans = append(s.Orphans, key)
	}
	return s, rows.Err()
}

// ExportLinks is a portable, reviewable backup of manual and automatic links.
func (d *DB) ExportLinks(ctx context.Context) ([]map[string]any, error) {
	rows, err := d.SQL.QueryContext(ctx, `SELECT l.document_key,d.last_path,l.library_type,l.library_id,l.item_key,l.attachment_key,l.match_method,l.locked FROM document_zotero_links l JOIN catalog_documents d USING(document_key) ORDER BY l.document_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var key, path, kind, id, item, attachment, method string
		var locked bool
		if err = rows.Scan(&key, &path, &kind, &id, &item, &attachment, &method, &locked); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"documentKey": key, "path": path, "reference": model.ZoteroReference{LibraryType: kind, LibraryID: id, ItemKey: item, AttachmentKey: attachment}, "matchMethod": method, "locked": locked})
	}
	return out, rows.Err()
}
