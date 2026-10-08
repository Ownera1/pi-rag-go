package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/searchtext"
)

var loadOnce sync.Once

func loadVec() { loadOnce.Do(func() { sqlite_vec.Auto() }) }

type DB struct {
	SQL      *sql.DB
	Path     string
	ReadOnly bool
}

func Open(path string, readOnly bool, dimensions int) (*DB, error) {
	loadVec()
	if !readOnly {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	if readOnly {
		q.Set("mode", "ro")
		// mode=ro protects the main index while permitting connection-local TEMP
		// tables for metadata prefilters. Nothing is attached or written to main.
	} else {
		q.Set("mode", "rwc")
		q.Set("_foreign_keys", "1")
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	out := &DB{SQL: db, Path: path, ReadOnly: readOnly}
	if !readOnly {
		if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
			db.Close()
			return nil, err
		}
		if err = out.Init(dimensions); err != nil {
			db.Close()
			return nil, err
		}
	}
	return out, nil
}

func (d *DB) Close() error { return d.SQL.Close() }

func (d *DB) Init(dim int) error {
	if dim < 1 || dim > 4096 {
		return errors.New("invalid vector dimensions")
	}
	schema := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS chunks (
    id TEXT PRIMARY KEY,
    file_path TEXT NOT NULL,
    chunk_content TEXT NOT NULL,
    line_start INTEGER NOT NULL,
    line_end INTEGER NOT NULL,
    chunk_hash TEXT NOT NULL,
    indexed_at TEXT NOT NULL,
    tokens INTEGER NOT NULL,
    page_start INTEGER,
    page_end INTEGER,
    section TEXT,
    chunk_index INTEGER NOT NULL DEFAULT 0
);
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
    chunk_content, file_path, heading, content_rowid=rowid
);
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_cjk USING fts5(search_text);
CREATE TRIGGER IF NOT EXISTS chunks_cjk_ad AFTER DELETE ON chunks BEGIN
    DELETE FROM chunks_cjk WHERE rowid=old.rowid;
END;
CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
    DELETE FROM chunks_fts WHERE rowid=old.rowid;
END;
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_vec USING vec0(embedding float[%d]);
CREATE TABLE IF NOT EXISTS files (
    path TEXT PRIMARY KEY,
    hash TEXT NOT NULL,
    chunks INTEGER NOT NULL,
    indexed TEXT NOT NULL,
    size INTEGER NOT NULL,
    embedded INTEGER NOT NULL DEFAULT 0,
    document_id TEXT,
    title TEXT
);
CREATE INDEX IF NOT EXISTS idx_chunks_file_path ON chunks(file_path);
`, dim)
	_, err := d.SQL.Exec(schema)
	if err == nil {
		err = d.ensureDocumentColumns()
	}
	return err
}

func (d *DB) documentColumns() (map[string]bool, error) {
	rows, err := d.SQL.Query("PRAGMA table_info(files)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func (d *DB) ensureDocumentColumns() error {
	cols, err := d.documentColumns()
	if err != nil {
		return err
	}
	for _, name := range []string{"document_key", "source_path", "zotero_ref", "doi", "format", "parser_version"} {
		if !cols[name] {
			if _, err = d.SQL.Exec("ALTER TABLE files ADD COLUMN " + name + " TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *DB) Documents(ctx context.Context) ([]model.CatalogDocument, error) {
	cols, err := d.documentColumns()
	if err != nil {
		return nil, err
	}
	q := "SELECT path,COALESCE(title,''),'' AS document_key,'' AS source_path,'' AS zotero_ref,'' AS doi FROM files"
	if cols["document_key"] && cols["source_path"] && cols["zotero_ref"] && cols["doi"] {
		q = "SELECT path,COALESCE(title,''),document_key,source_path,zotero_ref,doi FROM files"
	}
	rows, err := d.SQL.QueryContext(ctx, q+" ORDER BY path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.CatalogDocument{}
	for rows.Next() {
		var v model.CatalogDocument
		var ref string
		if err = rows.Scan(&v.Path, &v.Title, &v.Key, &v.SourcePath, &ref, &v.DOI); err != nil {
			return nil, err
		}
		if ref != "" {
			if err = json.Unmarshal([]byte(ref), &v.Zotero); err != nil {
				return nil, err
			}
		}
		if v.Key == "" {
			v.Key = "path:" + v.Path
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func ResolvePath(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "active.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return filepath.Join(root, "rag.db"), nil
		}
		return "", err
	}
	var m struct {
		Version        int    `json:"version"`
		RelativeDBPath string `json:"relativeDbPath"`
	}
	if err = json.Unmarshal(b, &m); err != nil {
		return "", fmt.Errorf("invalid active manifest: %w", err)
	}
	if m.Version != 1 || m.RelativeDBPath == "" || filepath.IsAbs(m.RelativeDBPath) {
		return "", errors.New("invalid active manifest")
	}
	p := filepath.Join(root, m.RelativeDBPath)
	rel, e := filepath.Rel(root, p)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("active manifest escapes store")
	}
	if _, err = os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

// GetMetadata returns "" for a missing key or a database without rag-go's
// metadata table, and any other error, such as I/O, as is.
func (d *DB) GetMetadata(ctx context.Context, key string) (string, error) {
	var v string
	e := d.SQL.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", key).Scan(&v)
	if errors.Is(e, sql.ErrNoRows) || e != nil && strings.Contains(e.Error(), "no such table") {
		return "", nil
	}
	return v, e
}

func (d *DB) SetMetadata(ctx context.Context, key, value string) error {
	_, e := d.SQL.ExecContext(ctx, "INSERT OR REPLACE INTO metadata(key,value) VALUES(?,?)", key, value)
	return e
}

func (d *DB) Stats(ctx context.Context) (model.Status, error) {
	if d == nil {
		return model.Status{FailedFiles: []model.FileFailure{}}, nil
	}
	s := model.Status{ReadOnly: d.ReadOnly, ActiveDB: d.Path, FailedFiles: []model.FileFailure{}}
	if err := d.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM files").Scan(&s.Files); err != nil {
		return s, err
	}
	if err := d.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM chunks").Scan(&s.Chunks); err != nil {
		return s, err
	}
	if err := d.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM chunks_vec").Scan(&s.Vectors); err != nil {
		return s, err
	}
	var err error
	if s.EmbeddingModel, err = d.GetMetadata(ctx, "embedding_model"); err != nil {
		return s, err
	}
	dims, err := d.GetMetadata(ctx, "embedding_dimensions")
	s.Dimensions, _ = strconv.Atoi(dims)
	return s, err
}

func (d *DB) FileHash(ctx context.Context, path string) (string, bool, error) {
	var hash string
	var embedded int
	e := d.SQL.QueryRowContext(ctx, "SELECT hash,embedded FROM files WHERE path=?", path).Scan(&hash, &embedded)
	if errors.Is(e, sql.ErrNoRows) {
		return "", false, nil
	}
	return hash, embedded != 0, e
}

// Embedded returns the content hash of every fully embedded file.
func (d *DB) Embedded(ctx context.Context) (map[string]string, error) {
	rows, e := d.SQL.QueryContext(ctx, "SELECT path,hash FROM files WHERE embedded<>0")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var path, hash string
		if e = rows.Scan(&path, &hash); e != nil {
			return nil, e
		}
		out[path] = hash
	}
	return out, rows.Err()
}

func vecBytes(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return b
}

func (d *DB) Replace(ctx context.Context, doc model.Document, chunks []model.Chunk, vectors [][]float32) error {
	if d.ReadOnly {
		return errors.New("read-only store")
	}
	if len(chunks) != len(vectors) {
		return errors.New("vector coverage mismatch")
	}
	tx, e := d.SQL.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// Replace alternate artifacts of the same logical document in one transaction.
	obsolete := append([]string{doc.Path}, doc.Replaces...)
	rows, e := tx.QueryContext(ctx, "SELECT path FROM files WHERE document_id=?", doc.ID)
	if e != nil {
		return e
	}
	for rows.Next() {
		var path string
		if e = rows.Scan(&path); e != nil {
			rows.Close()
			return e
		}
		obsolete = append(obsolete, path)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, path := range obsolete {
		if _, e = tx.ExecContext(ctx, "DELETE FROM chunks_vec WHERE rowid IN (SELECT rowid FROM chunks WHERE file_path=?)", path); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM chunks WHERE file_path=?", path); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM files WHERE path=?", path); e != nil {
			return e
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ref := ""
	if doc.Zotero != nil {
		b, err := json.Marshal(doc.Zotero)
		if err != nil {
			return err
		}
		ref = string(b)
	}
	insertChunk, e := tx.PrepareContext(ctx, `
INSERT INTO chunks (
    id, file_path, chunk_content, line_start, line_end, chunk_hash,
    indexed_at, tokens, page_start, page_end, section, chunk_index
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if e != nil {
		return e
	}
	defer insertChunk.Close()
	insertFTS, e := tx.PrepareContext(ctx, "INSERT INTO chunks_fts(rowid,chunk_content,file_path,heading) VALUES(?,?,?,?)")
	if e != nil {
		return e
	}
	defer insertFTS.Close()
	insertHan, e := tx.PrepareContext(ctx, "INSERT INTO chunks_cjk(rowid,search_text) VALUES(?,?)")
	if e != nil {
		return e
	}
	defer insertHan.Close()
	insertVector, e := tx.PrepareContext(ctx, "INSERT INTO chunks_vec(rowid,embedding) VALUES(CAST(? AS INTEGER),?)")
	if e != nil {
		return e
	}
	defer insertVector.Close()
	for i, c := range chunks {
		r, err := insertChunk.ExecContext(ctx,
			c.ID, doc.Path, c.Content, c.LineStart, c.LineEnd, c.Hash,
			now, c.Tokens, c.PageStart, c.PageEnd, c.Section, c.ChunkIndex,
		)
		if err != nil {
			return err
		}
		rowid, err := r.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = insertFTS.ExecContext(ctx, rowid, c.Content, doc.SearchPath, c.Heading); err != nil {
			return err
		}
		if _, err = insertHan.ExecContext(ctx, rowid, searchtext.Indexed(c.Content+" "+doc.SearchPath+" "+c.Heading)); err != nil {
			return err
		}
		if _, err = insertVector.ExecContext(ctx, rowid, vecBytes(vectors[i])); err != nil {
			return err
		}
	}
	// Source attribution is per document, so it lives on the file row and is
	// joined into chunks at query time.
	if _, e = tx.ExecContext(ctx, `
INSERT INTO files (path, hash, chunks, indexed, size, embedded, document_id, title, document_key, source_path, zotero_ref, doi, format, parser_version)
VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(path) DO UPDATE SET
    hash=excluded.hash,
    chunks=excluded.chunks,
    indexed=excluded.indexed,
    size=excluded.size,
    embedded=1,
    document_id=excluded.document_id,
    title=excluded.title,
    document_key=excluded.document_key,
    source_path=excluded.source_path,
    zotero_ref=excluded.zotero_ref,
    doi=excluded.doi,
    format=excluded.format,
    parser_version=excluded.parser_version`,
		doc.Path, doc.Hash, len(chunks), now, doc.Size, doc.ID, doc.Title, doc.DocumentKey, doc.SourcePath, ref, doc.DOI, doc.Format, doc.ParserVersion,
	); e != nil {
		return e
	}
	return tx.Commit()
}

// Hydrate bibliographic identity on a pre-feature index without embedding again.
// The caller must prove the source still matches the indexed content hash.
func (d *DB) SetDocumentIdentity(ctx context.Context, doc model.Document) error {
	ref := ""
	if doc.Zotero != nil {
		b, err := json.Marshal(doc.Zotero)
		if err != nil {
			return err
		}
		ref = string(b)
	}
	_, err := d.SQL.ExecContext(ctx, "UPDATE files SET document_key=?,source_path=?,zotero_ref=?,doi=? WHERE path=? AND hash=?", doc.DocumentKey, doc.SourcePath, ref, doc.DOI, doc.Path, doc.Hash)
	return err
}

func (d *DB) Delete(ctx context.Context, path string) error {
	if d.ReadOnly {
		return errors.New("read-only store")
	}
	tx, e := d.SQL.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "DELETE FROM chunks_vec WHERE rowid IN (SELECT rowid FROM chunks WHERE file_path=?)", path); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM chunks WHERE file_path=?", path); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM files WHERE path=?", path); e != nil {
		return e
	}
	return tx.Commit()
}

func (d *DB) List(ctx context.Context) ([]string, error) {
	rows, e := d.SQL.QueryContext(ctx, "SELECT path FROM files ORDER BY path")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var p string
		if e = rows.Scan(&p); e != nil {
			return nil, e
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

type Match struct {
	RowID int64
	Score float64
}

func (d *DB) FTS(ctx context.Context, query string, limit int, filtered ...bool) ([]Match, error) {
	rows, e := d.SQL.QueryContext(ctx, "SELECT rowid,bm25(chunks_fts) FROM chunks_fts WHERE chunks_fts MATCH ?"+filterSQL(filtered)+" ORDER BY bm25(chunks_fts) LIMIT ?", query, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Match{}
	for rows.Next() {
		var m Match
		if e = rows.Scan(&m.RowID, &m.Score); e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (d *DB) FTSHan(ctx context.Context, query string, limit int, filtered ...bool) ([]Match, error) {
	rows, err := d.SQL.QueryContext(ctx, "SELECT rowid,bm25(chunks_cjk) FROM chunks_cjk WHERE chunks_cjk MATCH ?"+filterSQL(filtered)+" ORDER BY bm25(chunks_cjk) LIMIT ?", query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Match{}
	for rows.Next() {
		var m Match
		if err = rows.Scan(&m.RowID, &m.Score); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (d *DB) Vectors(ctx context.Context, vector []float32, limit int, filtered ...bool) ([]Match, error) {
	rows, e := d.SQL.QueryContext(ctx, "SELECT rowid,distance FROM chunks_vec WHERE embedding MATCH ?"+filterSQL(filtered)+" LIMIT ?", vecBytes(vector), limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Match{}
	for rows.Next() {
		var m Match
		if e = rows.Scan(&m.RowID, &m.Score); e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func filterSQL(filtered []bool) string {
	if len(filtered) > 0 && filtered[0] {
		return " AND rowid IN (SELECT rowid FROM temp.allowed_chunks)"
	}
	return ""
}

// A nil filter is unrestricted; an empty filter explicitly permits no chunks.
func (d *DB) SetDocumentFilter(ctx context.Context, keys []string) error {
	if _, err := d.SQL.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS allowed_documents (document_key TEXT PRIMARY KEY);
CREATE TEMP TABLE IF NOT EXISTS allowed_chunks (rowid INTEGER PRIMARY KEY);
DELETE FROM temp.allowed_documents; DELETE FROM temp.allowed_chunks;`); err != nil {
		return err
	}
	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, "INSERT OR IGNORE INTO temp.allowed_documents VALUES (?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, key := range keys {
		if _, err = stmt.ExecContext(ctx, key); err != nil {
			return err
		}
	}
	cols, err := d.documentColumnsInTx(ctx, tx)
	if err != nil {
		return err
	}
	identity := "'path:'||f.path"
	if cols {
		identity = "CASE WHEN f.document_key='' THEN 'path:'||f.path ELSE f.document_key END"
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO temp.allowed_chunks SELECT c.rowid FROM chunks c JOIN files f ON f.path=c.file_path JOIN temp.allowed_documents a ON a.document_key="+identity)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) documentColumnsInTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info(files)")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var a, b, c, e, f any
		var name string
		if err = rows.Scan(&a, &name, &b, &c, &e, &f); err != nil {
			return false, err
		}
		if name == "document_key" {
			found = true
		}
	}
	return found, rows.Err()
}

func (d *DB) Chunks(ctx context.Context, ids []int64) (map[int64]model.Chunk, error) {
	out := map[int64]model.Chunk{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		placeholders[i] = "?"
	}
	cols, err := d.documentColumns()
	if err != nil {
		return nil, err
	}
	// Readers tolerate a store written before these columns existed.
	field := func(name string) string {
		if cols[name] {
			return "COALESCE(f." + name + ",'')"
		}
		return "''"
	}
	columns := `chunks.rowid, chunks.id, file_path, chunk_content, line_start, line_end,
        chunk_hash, indexed_at, tokens, page_start, page_end, section, chunk_index`
	columns += ", " + field("source_path") + ", COALESCE(f.title,''), " + field("format") + ", " + field("parser_version")
	join := " LEFT JOIN files f ON f.path=chunks.file_path"
	q := "SELECT " + columns + " FROM chunks" + join + " WHERE chunks.rowid IN (" + strings.Join(placeholders, ",") + ")"
	rows, e := d.SQL.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var c model.Chunk
		var indexed string
		var pageA, pageB sql.NullInt64
		var sec sql.NullString
		dest := []any{
			&id, &c.ID, &c.Path, &c.Content, &c.LineStart, &c.LineEnd, &c.Hash,
			&indexed, &c.Tokens, &pageA, &pageB, &sec, &c.ChunkIndex,
		}
		dest = append(dest, &c.SourcePath, &c.Title, &c.Format, &c.ParserVersion)
		if e = rows.Scan(dest...); e != nil {
			return nil, e
		}
		if pageA.Valid {
			v := int(pageA.Int64)
			c.PageStart = &v
		}
		if pageB.Valid {
			v := int(pageB.Int64)
			c.PageEnd = &v
		}
		if sec.Valid {
			v := sec.String
			c.Section = &v
		}
		out[id] = c
	}
	return out, rows.Err()
}
