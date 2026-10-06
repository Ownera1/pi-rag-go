package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestZoteroCLIWorksWithoutDocumentEmbedding(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Zotero-API-Version", "3")
		w.Header().Set("Last-Modified-Version", "0")
		if r.URL.Path == "/api/" {
			return
		}
		if strings.HasSuffix(r.URL.Path, "collections") {
			w.Header().Set("Total-Results", "0")
			if r.URL.Query().Get("format") != "keys" {
				fmt.Fprint(w, "[]")
			}
			return
		}
		w.Header().Set("Total-Results", "1")
		if r.URL.Query().Get("format") == "keys" {
			fmt.Fprintln(w, "PAPER001")
			return
		}
		fmt.Fprint(w, `[{"key":"PAPER001","version":0,"library":{"type":"user","id":42},"data":{"key":"PAPER001","itemType":"journalArticle","title":"Local paper","creators":[],"tags":[],"collections":[]}}]`)
	}))
	defer s.Close()
	ctx := context.Background()
	root := t.TempDir()
	var out, stderr bytes.Buffer
	if err := Run(ctx, []string{"init", root, "--offline"}, strings.NewReader(""), &out, &stderr); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	// Exercise prefix flags and options following the Zotero subcommand.
	if err := Run(ctx, []string{"--workspace", root, "zotero", "sync", "--base-url", s.URL + "/api/"}, strings.NewReader(""), &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var synced map[string]any
	if err := json.Unmarshal(out.Bytes(), &synced); err != nil {
		t.Fatal(err)
	}
	if synced["fetched"] != float64(1) {
		t.Fatal(synced)
	}
	out.Reset()
	if err := Run(ctx, []string{"zotero", "status", "--workspace", root}, strings.NewReader(""), &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["items"] != float64(1) {
		t.Fatal(status)
	}
	if err := Run(ctx, []string{"query", "signal", "--workspace", root, "--year-from", "0"}, strings.NewReader(""), &out, &stderr); err == nil {
		t.Fatal("invalid CLI year filter accepted")
	}
}
