package command

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitializeProbeCredentialsAndRepeat(t *testing.T) {
	t.Setenv("RAG_TEST_KEY", "private-value")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-value" {
			t.Error("credential missing")
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float64{1, 0}}}})
	}))
	defer server.Close()
	root := t.TempDir()
	var out, stderr bytes.Buffer
	args := []string{"--store", root, "--embedding-type", "openai", "--model", "test", "--dimensions", "2", "--base-url", server.URL, "--api-key-env", "RAG_TEST_KEY", "--pdf-backend", "pdftotext", "--pdf-command", "/usr/bin/true"}
	if err := Initialize(context.Background(), args, strings.NewReader(""), &out, &stderr); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(config, []byte("private-value")) || bytes.Contains(out.Bytes(), []byte("private-value")) {
		t.Fatal("secret leaked")
	}
	st, _ := os.Stat(filepath.Join(root, "credentials.json"))
	if st.Mode().Perm() != 0600 {
		t.Fatal("permissions")
	}
	if err := Initialize(context.Background(), []string{"--store", root}, strings.NewReader(""), &out, &stderr); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "config.json"))
	if !bytes.Equal(config, after) {
		t.Fatal("repeat overwrote settings")
	}
}

func TestInitializeRejectsInvalidDimensionsAndMalformedConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ioBody := `{"data":[{"index":0,"embedding":[1,0]}]}`
		w.Write([]byte(ioBody))
	}))
	defer server.Close()
	root := t.TempDir()
	var out, stderr bytes.Buffer
	err := Initialize(context.Background(), []string{"--store", root, "--embedding-type", "openai", "--model", "test", "--dimensions", "3", "--base-url", server.URL, "--api-key-env=", "--pdf-backend", "pdftotext", "--pdf-command", "/usr/bin/true"}, strings.NewReader(""), &out, &stderr)
	if err == nil || !strings.Contains(err.Error(), "embedding probe failed") {
		t.Fatalf("%v", err)
	}
	path := filepath.Join(root, "config.json")
	os.WriteFile(path, []byte("{broken"), 0600)
	if Initialize(context.Background(), []string{"--store", root}, strings.NewReader(""), &out, &stderr) == nil {
		t.Fatal("accepted broken config")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "{broken" {
		t.Fatal("overwrote broken config")
	}
}

func TestCredentialsDoNotOverwriteEnvironment(t *testing.T) {
	root := t.TempDir()
	atomicJSON(filepath.Join(root, "credentials.json"), map[string]string{"RAG_TEST_KEY": "saved"})
	t.Setenv("RAG_TEST_KEY", "current")
	if err := applyCredentials(root); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("RAG_TEST_KEY") != "current" {
		t.Fatal("overwrote environment")
	}
	os.Chmod(filepath.Join(root, "credentials.json"), 0644)
	if applyCredentials(root) == nil {
		t.Fatal("accepted public credentials")
	}
}
