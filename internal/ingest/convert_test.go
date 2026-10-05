package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ownera1/rag-go/internal/document"
)

const sampleTEI = `<TEI xmlns="http://www.tei-c.org/ns/1.0"><text><body><div><head>Methods</head><p coords="3,10,20,30,40">Signal evidence.</p></div></body></text></TEI>`
const sampleMinerU = `{"schema":"docvortex.middle","schema_version":"2.0","is_full_document":true,"pages":[{"page_idx":2,"blocks":[{"type":"text","content":"Signal evidence"}]}]}`

func inputPDF(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paper.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4\nmock document"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGROBIDProtocolAndFailedPublication(t *testing.T) {
	var response atomic.Value
	response.Store(sampleTEI)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/processFulltextDocument" {
			t.Error("incorrect GROBID endpoint", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		f, _, err := r.FormFile("input")
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		pdf, _ := io.ReadAll(f)
		if !bytes.HasPrefix(pdf, []byte("%PDF-")) || strings.Join(r.Form["teiCoordinates"], ",") != "p,s,head" || r.FormValue("segmentSentences") != "1" {
			t.Error("incorrect GROBID input/coordinates", r.Form)
		}
		fmt.Fprint(w, response.Load().(string))
	}))
	defer server.Close()
	opt := Options{Input: inputPDF(t), Output: t.TempDir(), Backend: "grobid", URL: server.URL}
	result, err := Convert(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := document.Parse(context.Background(), result.Manifest)
	if err != nil || doc.SourcePath != opt.Input || *doc.Blocks[0].PageStart != 3 || doc.Title != "paper.pdf" {
		t.Fatalf("published=%+v err=%v", doc, err)
	}
	before, err := os.ReadFile(result.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	response.Store(`<bad/>`)
	if _, err = Convert(context.Background(), opt); err == nil {
		t.Fatal("published invalid TEI")
	}
	after, _ := os.ReadFile(result.Manifest)
	if !bytes.Equal(before, after) {
		t.Fatal("failed conversion replaced the previous manifest")
	}
	response.Store("")
	if _, err = Convert(context.Background(), opt); err == nil {
		t.Fatal("published empty output")
	}
}

func TestGROBIDCancellationAndHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if _, err := Convert(context.Background(), Options{Input: inputPDF(t), Output: t.TempDir(), Backend: "grobid", URL: server.URL}); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatal("HTTP failure lost", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Convert(ctx, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the uploaded body, then wait for client cancellation.
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server2.Close()
	if _, err := Convert(context.Background(), Options{Input: inputPDF(t), Output: t.TempDir(), Backend: "grobid", URL: server2.URL, Timeout: 50 * time.Millisecond}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("request timeout lost", err)
	}
}

// A child test executable acts as a deterministic converter. No real model or
// network is involved in protocol/cancellation tests.
func TestConverterProcess(t *testing.T) {
	mode := os.Getenv("RAGPREP_TEST_HELPER")
	if mode == "" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	switch mode {
	case "mineru":
		if len(args) != 8 || args[0] != "parse" || args[2] != "--format" || args[3] != "middle_json" || args[4] != "--pages" || args[5] != "all" || args[6] != "-o" {
			fmt.Fprintln(os.Stderr, "unexpected MinerU arguments", args)
			os.Exit(2)
		}
		if err := os.WriteFile(args[7], []byte(sampleMinerU), 0600); err != nil {
			os.Exit(3)
		}
	case "pdftotext":
		if len(args) != 4 || args[0] != "-enc" || args[1] != "UTF-8" || args[3] != "-" {
			os.Exit(2)
		}
		fmt.Print("Page one\f\fPage three\f")
	case "wait":
		time.Sleep(10 * time.Second)
	}
	os.Exit(0)
}

func converterHelper(t *testing.T, mode string) string {
	t.Helper()
	t.Setenv("RAGPREP_TEST_HELPER", mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "converter")
	quoted := "'" + strings.ReplaceAll(exe, "'", "'\"'\"'") + "'"
	script := "#!/bin/sh\nexec " + quoted + " -test.run=TestConverterProcess -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativeConverterProtocolsAndPageGaps(t *testing.T) {
	t.Run("mineru", func(t *testing.T) {
		result, err := Convert(context.Background(), Options{Input: inputPDF(t), Output: t.TempDir(), Backend: "mineru", MinerUCommand: converterHelper(t, "mineru")})
		if err != nil {
			t.Fatal(err)
		}
		doc, err := document.Parse(context.Background(), result.Manifest)
		if err != nil || len(doc.Blocks) != 1 || *doc.Blocks[0].PageStart != 3 {
			t.Fatalf("document=%+v err=%v", doc, err)
		}
	})
	t.Run("pdftotext", func(t *testing.T) {
		result, err := Convert(context.Background(), Options{Input: inputPDF(t), Output: t.TempDir(), Backend: "pdftotext", PDFToTextCommand: converterHelper(t, "pdftotext")})
		if err != nil {
			t.Fatal(err)
		}
		doc, err := document.Parse(context.Background(), result.Manifest)
		if err != nil || len(doc.Blocks) != 2 || *doc.Blocks[1].PageStart != 3 {
			t.Fatalf("blank page was dropped from source numbering: %+v err=%v", doc, err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if _, err := run(ctx, converterHelper(t, "wait")); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("subprocess cancellation lost", err)
		}
	})
}

func TestImportMinerUAndInvalidPDF(t *testing.T) {
	input := filepath.Join(t.TempDir(), "middle.json")
	if err := os.WriteFile(input, []byte(sampleMinerU), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := ImportMinerU(context.Background(), input, "/archive/original.pdf", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := document.Parse(context.Background(), result.Manifest)
	if err != nil || doc.SourcePath != "/archive/original.pdf" || *doc.Blocks[0].PageStart != 3 {
		t.Fatalf("import=%+v err=%v", doc, err)
	}
	if _, err = Convert(context.Background(), Options{Input: input, Output: t.TempDir(), Backend: "pdftotext"}); err == nil {
		t.Fatal("non-PDF accepted")
	}
}

// A valid, deterministic PDF with text on pages 1 and 3 and a blank page 2.
// This exercises Poppler itself instead of assuming form-feed behavior.
func threePagePDF() []byte {
	objects := []string{
		`<< /Type /Catalog /Pages 2 0 R >>`,
		`<< /Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 >>`,
		`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 6 0 R >> >> /Contents 7 0 R >>`,
		`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 6 0 R >> >> /Contents 8 0 R >>`,
		`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 6 0 R >> >> /Contents 9 0 R >>`,
		`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>`,
	}
	for _, text := range []string{"First page signal evidence", "", "Third page retrieval evidence"} {
		stream := "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET\n"
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, object := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return b.Bytes()
}

func TestRealPDFToTextPhysicalPages(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("Poppler is not installed; CI installs it for this integration test")
	}
	input := filepath.Join(t.TempDir(), "three-pages.pdf")
	if err := os.WriteFile(input, threePagePDF(), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Convert(context.Background(), Options{Input: input, Output: t.TempDir(), Backend: "pdftotext"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := document.Parse(context.Background(), result.Manifest)
	if err != nil || len(doc.Blocks) != 2 || *doc.Blocks[0].PageStart != 1 || *doc.Blocks[1].PageStart != 3 || !strings.Contains(doc.Blocks[1].Text, "Third page retrieval evidence") {
		t.Fatalf("real extraction=%+v err=%v", doc, err)
	}
}
