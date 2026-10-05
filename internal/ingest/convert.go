// Package ingest converts external document formats into validated, immutable
// artifacts. Conversion is independent of the database and embedding service.
package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ownera1/rag-go/internal/document"
	"github.com/Ownera1/rag-go/internal/model"
)

const maxPDFBytes = 100 << 20

type Options struct {
	Backend          string
	Input            string
	Output           string
	URL              string
	Timeout          time.Duration
	MinerUCommand    string
	PDFToTextCommand string
}

type Result struct {
	SourcePath string `json:"sourcePath"`
	Manifest   string `json:"manifest"`
	Format     string `json:"format"`
	Blocks     int    `json:"blocks"`
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func boundedRead(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = errors.New("input exceeds size limit")
	}
	return b, err
}

func Convert(ctx context.Context, opt Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	input, err := filepath.Abs(opt.Input)
	if err != nil {
		return Result{}, err
	}
	pdf, err := boundedRead(input, maxPDFBytes)
	if err != nil {
		return Result{}, err
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return Result{}, errors.New("input is not a PDF")
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()
	var content []byte
	var format, suffix string
	switch opt.Backend {
	case "grobid":
		content, err = grobid(ctx, opt.URL, filepath.Base(input), pdf)
		format, suffix = "grobid-tei", ".tei.xml"
	case "pdftotext":
		content, err = pdfText(ctx, opt.PDFToTextCommand, input)
		format, suffix = "paged-text", ".rag-blocks.json"
	case "mineru":
		content, err = mineru(ctx, opt.MinerUCommand, input)
		format, suffix = "mineru", ".mineru.json"
	default:
		return Result{}, fmt.Errorf("unknown conversion backend %q", opt.Backend)
	}
	if err != nil {
		return Result{}, err
	}
	return publish(ctx, opt.Output, input, hash(pdf), format, suffix, content)
}

func ImportMinerU(ctx context.Context, input, source, output string) (Result, error) {
	content, err := boundedRead(input, document.MaxDocumentBytes)
	if err != nil {
		return Result{}, err
	}
	if _, err = document.ParseMinerU(ctx, content); err != nil {
		return Result{}, err
	}
	if source == "" {
		source = input
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return Result{}, err
	}
	// Imported packages may refer to a PDF on another machine. Its hash is
	// deliberately left unknown rather than hashing the JSON as the PDF.
	return publish(ctx, output, source, "", "mineru", ".mineru.json", content)
}

func grobid(ctx context.Context, base, filename string, pdf []byte) ([]byte, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, errors.New("grobid URL must be an HTTP(S) service URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/processFulltextDocument"
	u.RawQuery, u.Fragment = "", ""
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	f, err := w.CreateFormFile("input", filename)
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(pdf); err != nil {
		return nil, err
	}
	for _, key := range []string{"consolidateHeader", "consolidateCitations", "consolidateFunders"} {
		if err = w.WriteField(key, "0"); err != nil {
			return nil, err
		}
	}
	for _, element := range []string{"p", "s", "head"} {
		if err = w.WriteField("teiCoordinates", element); err != nil {
			return nil, err
		}
	}
	if err = w.WriteField("segmentSentences", "1"); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Accept", "application/xml")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("grobid returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, document.MaxDocumentBytes+1))
	if err == nil && len(b) > document.MaxDocumentBytes {
		err = errors.New("grobid response exceeds size limit")
	}
	return b, err
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("converter output exceeds size limit")
	}
	return b.Buffer.Write(p)
}

func run(ctx context.Context, command string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	out := &limitedBuffer{limit: document.MaxDocumentBytes}
	stderr := &limitedBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = out, stderr
	// Bound waits even when a subprocess leaves inherited output pipes open.
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s failed: %w: %s", filepath.Base(command), err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

func pdfText(ctx context.Context, command, input string) ([]byte, error) {
	if command == "" {
		command = "pdftotext"
	}
	text, err := run(ctx, command, "-enc", "UTF-8", input, "-")
	if err != nil {
		return nil, err
	}
	blocks := []model.Block{}
	for index, page := range strings.Split(string(text), "\f") {
		page = strings.TrimSpace(page)
		if page == "" {
			continue
		}
		n := index + 1
		blocks = append(blocks, model.Block{Text: page, PageStart: &n, PageEnd: &n})
	}
	return json.Marshal(struct {
		Version int           `json:"version"`
		Blocks  []model.Block `json:"blocks"`
	}{1, blocks})
}

func mineru(ctx context.Context, command, input string) ([]byte, error) {
	if command == "" {
		command = "mineru-kit"
	}
	dir, err := os.MkdirTemp("", "ragprep-mineru-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	output := filepath.Join(dir, "content.json")
	// MinerU 4's bounded `mineru parse` is not a complete-document export.
	// The kit's middle_json preserves explicit physical page_idx values.
	if _, err = run(ctx, command, "parse", input, "--format", "middle_json", "--pages", "all", "-o", output); err != nil {
		return nil, err
	}
	return boundedRead(output, document.MaxDocumentBytes)
}

func publish(ctx context.Context, output, source, sourceHash, format, suffix string, content []byte) (Result, error) {
	if output == "" {
		return Result{}, errors.New("output directory is required")
	}
	output, err := filepath.Abs(output)
	if err != nil {
		return Result{}, err
	}
	stem := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
	dir := filepath.Join(output, stem+"-"+document.ShortHash(source))
	if err = os.MkdirAll(dir, 0700); err != nil {
		return Result{}, err
	}
	name := "content-" + hash(content) + suffix
	if err = atomicWrite(filepath.Join(dir, name), content); err != nil {
		return Result{}, err
	}
	// Parse the same artifact the indexer will consume before replacing the
	// manifest. A failed conversion leaves the previously published one intact.
	doc, err := document.Parse(ctx, filepath.Join(dir, name))
	if err != nil {
		return Result{}, err
	}
	manifest := document.Manifest{Version: 1, Format: format, ContentPath: name,
		SourcePath: source, SourceHash: sourceHash, Title: filepath.Base(source)}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Result{}, err
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	path := filepath.Join(dir, "rag-source.json")
	if err = atomicWrite(path, b); err != nil {
		return Result{}, err
	}
	return Result{SourcePath: source, Manifest: path, Format: format, Blocks: len(doc.Blocks)}, nil
}

func atomicWrite(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".ragprep-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
