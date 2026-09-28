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

	"github.com/Ownera1/pi-rag-go/internal/model"
	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
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
		q.Set("_query_only", "1")
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
	schema := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS chunks(id TEXT PRIMARY KEY,file_path TEXT NOT NULL,chunk_content TEXT NOT NULL,line_start INTEGER NOT NULL,line_end INTEGER NOT NULL,chunk_hash TEXT NOT NULL,indexed_at TEXT NOT NULL,tokens INTEGER NOT NULL,page_start INTEGER,page_end INTEGER,section TEXT,chunk_index INTEGER NOT NULL DEFAULT 0);
 CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(chunk_content,file_path,content_rowid=rowid);
 CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN INSERT INTO chunks_fts(rowid,chunk_content,file_path) VALUES(new.rowid,new.chunk_content,new.file_path); END;
 CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN DELETE FROM chunks_fts WHERE rowid=old.rowid; END;
 CREATE VIRTUAL TABLE IF NOT EXISTS chunks_vec USING vec0(embedding float[%d]);
 CREATE TABLE IF NOT EXISTS files(path TEXT PRIMARY KEY,hash TEXT NOT NULL,chunks INTEGER NOT NULL,indexed TEXT NOT NULL,size INTEGER NOT NULL,embedded INTEGER NOT NULL DEFAULT 0,document_id TEXT,title TEXT);
 CREATE INDEX IF NOT EXISTS idx_chunks_file_path ON chunks(file_path);`, dim)
	_, err := d.SQL.Exec(schema)
	return err
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
func (d *DB) GetMetadata(ctx context.Context, key string) string {
	var v string
	_ = d.SQL.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", key).Scan(&v)
	return v
}
func (d *DB) SetMetadata(ctx context.Context, key, value string) error {
	_, e := d.SQL.ExecContext(ctx, "INSERT OR REPLACE INTO metadata(key,value) VALUES(?,?)", key, value)
	return e
}
func (d *DB) Stats(ctx context.Context) (model.Status, error) {
	if d == nil {
		return model.Status{TrackedPaths: []string{}}, nil
	}
	s := model.Status{ReadOnly: d.ReadOnly, ActiveDB: d.Path, TrackedPaths: []string{}}
	if err := d.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM files").Scan(&s.Files); err != nil {
		return s, err
	}
	if err := d.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM chunks").Scan(&s.Chunks); err != nil {
		return s, err
	}
	if err := d.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM chunks_vec").Scan(&s.Vectors); err != nil {
		return s, err
	}
	s.EmbeddingModel = d.GetMetadata(ctx, "embedding_model")
	s.Dimensions, _ = strconv.Atoi(d.GetMetadata(ctx, "embedding_dimensions"))
	return s, nil
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
	if _, e = tx.ExecContext(ctx, "DELETE FROM chunks_vec WHERE rowid IN (SELECT rowid FROM chunks WHERE file_path=?)", doc.Path); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM chunks WHERE file_path=?", doc.Path); e != nil {
		return e
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i, c := range chunks {
		r, err := tx.ExecContext(ctx, `INSERT INTO chunks(id,file_path,chunk_content,line_start,line_end,chunk_hash,indexed_at,tokens,page_start,page_end,section,chunk_index) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, doc.Path, c.Content, c.LineStart, c.LineEnd, c.Hash, now, c.Tokens, c.PageStart, c.PageEnd, c.Section, c.ChunkIndex)
		if err != nil {
			return err
		}
		rowid, err := r.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO chunks_vec(rowid,embedding) VALUES(CAST(? AS INTEGER),?)", rowid, vecBytes(vectors[i])); err != nil {
			return err
		}
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO files(path,hash,chunks,indexed,size,embedded,document_id,title) VALUES(?,?,?,?,?,1,?,?) ON CONFLICT(path) DO UPDATE SET hash=excluded.hash,chunks=excluded.chunks,indexed=excluded.indexed,size=excluded.size,embedded=1,document_id=excluded.document_id,title=excluded.title`, doc.Path, doc.Hash, len(chunks), now, doc.Size, doc.Hash, filepath.Base(doc.Path)); e != nil {
		return e
	}
	return tx.Commit()
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

func (d *DB) FTS(ctx context.Context, query string, limit int) ([]Match, error) {
	rows, e := d.SQL.QueryContext(ctx, "SELECT rowid,bm25(chunks_fts) FROM chunks_fts WHERE chunks_fts MATCH ? ORDER BY bm25(chunks_fts) LIMIT ?", query, limit)
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
func (d *DB) Vectors(ctx context.Context, vector []float32, limit int) ([]Match, error) {
	rows, e := d.SQL.QueryContext(ctx, "SELECT rowid,distance FROM chunks_vec WHERE embedding MATCH ? LIMIT ?", vecBytes(vector), limit)
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
	q := `SELECT rowid,id,file_path,chunk_content,line_start,line_end,chunk_hash,indexed_at,tokens,page_start,page_end,section,chunk_index FROM chunks WHERE rowid IN (` + strings.Join(placeholders, ",") + `)`
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
		if e = rows.Scan(&id, &c.ID, &c.Path, &c.Content, &c.LineStart, &c.LineEnd, &c.Hash, &indexed, &c.Tokens, &pageA, &pageB, &sec, &c.ChunkIndex); e != nil {
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
