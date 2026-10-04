package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestImportAndInspectCommands(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "content.json")
	if err := os.WriteFile(input, []byte(`[{"type":"text","text":"Signal evidence","page_idx":2}]`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{"import-mineru", "--input", input, "--source", "/archive/paper.pdf", "--output", filepath.Join(dir, "output")}, &out, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	var result struct{ Manifest string }
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Manifest == "" {
		t.Fatalf("result=%s err=%v", out.String(), err)
	}
	out.Reset()
	if err := run(context.Background(), []string{"inspect", result.Manifest}, &out, &stderr); err != nil || !bytes.Contains(out.Bytes(), []byte(`"pageStart": 3`)) {
		t.Fatalf("inspect=%s err=%v", out.String(), err)
	}
}

func TestBatchConversionReportsFailures(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "invalid.pdf"), []byte("not a PDF"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	err := run(context.Background(), []string{"convert", "--input", dir, "--output", t.TempDir(), "--backend", "pdftotext"}, &out, &stderr)
	if err == nil || !bytes.Contains(out.Bytes(), []byte(`"failures"`)) || !bytes.Contains(out.Bytes(), []byte("invalid.pdf")) {
		t.Fatalf("failure report=%s err=%v", out.String(), err)
	}
}
