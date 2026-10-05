package document

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Ownera1/rag-go/internal/model"
)

const ParserVersion = "document-blocks-v3"
const MaxDocumentBytes = 64 << 20

type Manifest struct {
	Version     int    `json:"version"`
	Format      string `json:"format"`
	ContentPath string `json:"contentPath"`
	SourcePath  string `json:"sourcePath,omitempty"`
	SourceHash  string `json:"sourceHash,omitempty"`
	Title       string `json:"title,omitempty"`
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
	if filepath.Base(path) != "rag-source.json" {
		return hash, ctx.Err()
	}
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
	return ShortHash(string(b) + "\x00" + string(data)), ctx.Err()
}

func Parse(ctx context.Context, path string) (model.Document, error) {
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
		content, err := localContent(filepath.Dir(path), m.ContentPath)
		if err != nil {
			return d, err
		}
		data, err := readFile(content)
		if err != nil {
			return d, err
		}
		d.Hash = ShortHash(string(b) + "\x00" + string(data))
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
