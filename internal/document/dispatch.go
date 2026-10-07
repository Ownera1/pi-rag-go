package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/workspace"
)

const ParserVersion = "document-blocks-v4"
const MaxDocumentBytes = 64 << 20

type Manifest struct {
	DOI         string                 `json:"doi,omitempty"`
	Zotero      *model.ZoteroReference `json:"zotero,omitempty"`
	Version     int                    `json:"version"`
	Format      string                 `json:"format"`
	ContentPath string                 `json:"contentPath"`
	SourcePath  string                 `json:"sourcePath,omitempty"`
	SourceHash  string                 `json:"sourceHash,omitempty"`
	Title       string                 `json:"title,omitempty"`
}

func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxDocumentBytes {
		return nil, errors.New("document exceeds 64 MiB limit")
	}
	return b, nil
}

// InputFingerprint hashes exactly the bytes Parse consumes, without parsing body
// structure. Manifest metadata and canonical content both participate.
func InputFingerprint(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	b, err := readFile(path)
	if err != nil {
		return "", err
	}
	hash := ShortHash(string(b))
	if filepath.Base(path) == "rag-source.json" {
		var m Manifest
		if err = json.Unmarshal(b, &m); err != nil {
			return hash, err
		}
		content, err := localContent(filepath.Dir(path), m.ContentPath)
		if err != nil {
			return hash, err
		}
		data, err := readFile(content)
		if err != nil {
			return hash, err
		}
		hash = ShortHash(string(b) + "\x00" + string(data))
	}
	fix, err := fixes(path)
	if err != nil {
		return hash, err
	}
	return withFixes(hash, fix), ctx.Err()
}

// InputStamp identifies the files InputFingerprint reads by metadata alone and
// returns their newest modification time. A manifest is read to find content.
func InputStamp(path string) (string, time.Time, error) {
	files := []string{path}
	if filepath.Base(path) == "rag-source.json" {
		b, err := readFile(path)
		if err != nil {
			return "", time.Time{}, err
		}
		var m Manifest
		if err = json.Unmarshal(b, &m); err != nil {
			return "", time.Time{}, err
		}
		content, err := localContent(filepath.Dir(path), m.ContentPath)
		if err != nil {
			return "", time.Time{}, err
		}
		files = append(files, content)
	}
	if hasFixes(path) {
		files = append(files, filepath.Join(filepath.Dir(path), FixesFile))
	}
	var stamp strings.Builder
	var newest time.Time
	for _, f := range files {
		st, err := os.Stat(f)
		if errors.Is(err, fs.ErrNotExist) && filepath.Base(f) == FixesFile {
			continue
		}
		if err != nil {
			return "", time.Time{}, err
		}
		var ino uint64
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			ino = uint64(sys.Ino)
		}
		fmt.Fprintf(&stamp, "%s\x00%d\x00%d\x00%d\x00", f, st.Size(), st.ModTime().UnixNano(), ino)
		if st.ModTime().After(newest) {
			newest = st.ModTime()
		}
	}
	return stamp.String(), newest, nil
}

// FixesFile holds a package's hand corrections: one "wrong<TAB>right" pair per
// line, applied in order to the parsed text. Lines starting with # are comments.
const FixesFile = "rag-fixes.tsv"

// hasFixes reports whether path is a MinerU or manifest package, the only
// documents a directory-wide correction file can belong to.
func hasFixes(path string) bool {
	return filepath.Base(path) == "rag-source.json" || IsMinerUFile(path)
}

func fixes(path string) ([]byte, error) {
	if !hasFixes(path) {
		return nil, nil
	}
	b, err := readFile(filepath.Join(filepath.Dir(path), FixesFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

func withFixes(hash string, fix []byte) string {
	if fix == nil {
		return hash
	}
	return ShortHash(hash + "\x00" + string(fix))
}

// applyFixes fails on a pair that matches nothing, so a typo or a fix made
// stale by a MinerU rerun surfaces instead of being silently kept.
func applyFixes(blocks []model.Block, fix []byte) ([]model.Block, error) {
	for i, line := range strings.Split(string(fix), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		wrong, right, ok := strings.Cut(line, "\t")
		if !ok || wrong == "" {
			return nil, fmt.Errorf("%s line %d: want wrong<TAB>right", FixesFile, i+1)
		}
		found := false
		for j := range blocks {
			if strings.Contains(blocks[j].Text, wrong) {
				blocks[j].Text = strings.ReplaceAll(blocks[j].Text, wrong, right)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("%s line %d: %q not found", FixesFile, i+1, wrong)
		}
	}
	out := blocks[:0]
	for _, b := range blocks {
		if strings.TrimSpace(b.Text) != "" {
			out = append(out, b)
		}
	}
	return out, nil
}

func Parse(ctx context.Context, path string) (model.Document, error) {
	d, err := parse(ctx, path)
	if err != nil {
		return d, err
	}
	fix, err := fixes(d.Path)
	if err != nil || fix == nil {
		return d, err
	}
	d.Hash = withFixes(d.Hash, fix)
	d.Blocks, err = applyFixes(d.Blocks, fix)
	return d, err
}

func parse(ctx context.Context, path string) (model.Document, error) {
	if err := ctx.Err(); err != nil {
		return model.Document{}, err
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return model.Document{}, err
	}
	b, err := readFile(path)
	if err != nil {
		return model.Document{}, err
	}
	d := model.Document{ID: ShortHash(path), Path: path, Hash: ShortHash(string(b)), Size: int64(len(b)), Title: filepath.Base(path), ParserVersion: ParserVersion}
	d.DocumentKey = contentKey(b)
	if filepath.Base(path) == "rag-source.json" {
		var m Manifest
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&m); err != nil {
			return d, err
		}
		if err = dec.Decode(new(any)); err != io.EOF {
			return d, errors.New("trailing manifest JSON")
		}
		if m.Version != 1 {
			return d, errors.New("unsupported source manifest version")
		}
		if m.Zotero != nil {
			if err = m.Zotero.Validate(); err != nil {
				return d, err
			}
		}
		content, err := localContent(filepath.Dir(path), m.ContentPath)
		if err != nil {
			return d, err
		}
		data, err := readFile(content)
		if err != nil {
			return d, err
		}
		d.Hash = ShortHash(string(b) + "\x00" + string(data))
		d.DocumentKey = contentKey(data)
		if key := sourceKey(m.SourceHash); key != "" {
			d.DocumentKey = key
		}
		d.Zotero = m.Zotero
		d.DOI = m.DOI
		d.Size += int64(len(data))
		d.SourcePath = m.SourcePath
		if d.SourcePath != "" && !filepath.IsAbs(d.SourcePath) {
			d.SourcePath = filepath.Join(filepath.Dir(path), d.SourcePath)
		}
		if m.Title != "" {
			d.Title = m.Title
		}
		d.Format = m.Format
		d.Blocks, err = parseBytes(ctx, content, data, m.Format)
		return d, err
	}
	d.Format = detectFormat(path, b)
	if d.Format == "mineru" {
		d.Title = filepath.Base(filepath.Dir(path))
	}
	d.Blocks, err = parseBytes(ctx, path, b, d.Format)
	return d, err
}

func contentKey(b []byte) string {
	h := sha256.Sum256(b)
	return "content:sha256:" + hex.EncodeToString(h[:])
}

func sourceKey(value string) string {
	s := strings.ToLower(strings.TrimSpace(value))
	algorithm := ""
	if a, b, ok := strings.Cut(s, ":"); ok {
		algorithm = a
		s = b
	}
	if _, err := hex.DecodeString(s); err != nil {
		return ""
	}
	if len(s) == 64 && (algorithm == "" || algorithm == "sha256") {
		return "source:sha256:" + s
	}
	if len(s) == 32 && (algorithm == "" || algorithm == "md5") {
		return "source:md5:" + s
	}
	return ""
}

func WriteZoteroReference(path string, ref model.ZoteroReference) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	b, err := readFile(path)
	if err != nil {
		return err
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&m); err != nil {
		return err
	}
	if m.Version != 1 {
		return errors.New("unsupported source manifest version")
	}
	m.Zotero = &ref
	return workspace.AtomicJSON(path, m)
}

func localContent(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", errors.New("contentPath must be a relative file path")
	}
	path := filepath.Join(root, relative)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("contentPath escapes document directory")
	}
	if filepath.Base(realPath) == "rag-source.json" {
		return "", errors.New("manifest cannot reference another manifest")
	}
	return realPath, nil
}

func detectFormat(path string, b []byte) string {
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".tei.xml") {
		return "grobid-tei"
	}
	if IsMinerUFile(path) {
		return "mineru"
	}
	if strings.HasSuffix(lower, ".rag-blocks.json") {
		return "paged-text"
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return "pdf"
	case ".docx":
		return "docx"
	case ".html", ".htm":
		return "html"
	case ".md", ".mdx":
		return "markdown"
	case ".xml", ".nxml":
		dec := xml.NewDecoder(bytes.NewReader(b))
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			if root, ok := tok.(xml.StartElement); ok {
				if root.Name.Local == "article" {
					return "jats"
				}
				if root.Name.Local == "TEI" && root.Name.Space == teiNS {
					return "grobid-tei"
				}
				break
			}
		}
	}
	return "text"
}

func parseBytes(ctx context.Context, path string, b []byte, format string) ([]model.Block, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if format != "docx" && format != "pdf" && (!utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0) {
		return nil, errors.New("input is not UTF-8 text")
	}
	switch format {
	case "grobid-tei":
		return ParseTEI(ctx, b)
	case "jats":
		return ParseJATS(ctx, b)
	case "markdown":
		return markdown(ctx, b)
	case "html":
		return parseHTML(ctx, b)
	case "docx":
		return parseDOCX(ctx, b)
	case "mineru":
		return ParseMinerU(ctx, b)
	case "paged-text":
		var data struct {
			Version int           `json:"version"`
			Blocks  []model.Block `json:"blocks"`
		}
		if err := json.Unmarshal(b, &data); err != nil {
			return nil, err
		}
		if data.Version != 1 {
			return nil, errors.New("unsupported paged text version")
		}
		if err := validBlocks(data.Blocks); err != nil {
			return nil, err
		}
		return data.Blocks, nil
	case "pdf":
		return nil, errors.New("PDF is not an indexable document; convert it to Markdown or structured JSON with an external tool")
	case "text":
		if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
			return nil, errors.New("input is not UTF-8 text")
		}
		text := string(b)
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		a, z := 1, strings.Count(text, "\n")+1
		return []model.Block{{Text: text, LineStart: &a, LineEnd: &z}}, nil
	default:
		return nil, fmt.Errorf("unsupported document format %q for %s", format, path)
	}
}

func validBlocks(blocks []model.Block) error {
	if len(blocks) == 0 {
		return errors.New("document contains no indexable body text")
	}
	for _, b := range blocks {
		if strings.TrimSpace(b.Text) == "" || !utf8.ValidString(b.Text) {
			return errors.New("invalid body text")
		}
		if (b.PageStart == nil) != (b.PageEnd == nil) || (b.PageStart != nil && (*b.PageStart < 1 || *b.PageEnd < *b.PageStart)) {
			return errors.New("invalid source page range")
		}
	}
	return nil
}
