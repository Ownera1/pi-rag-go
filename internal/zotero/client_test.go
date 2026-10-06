package zotero

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Ownera1/rag-go/internal/model"
)

func apiItem(key string) map[string]any {
	return map[string]any{"key": key, "version": 0, "library": map[string]any{"type": "user", "id": 42}, "data": map[string]any{"key": key, "version": 0, "itemType": "journalArticle", "title": "Channel estimation", "date": "2025-10", "DOI": "https://doi.org/10.1234/ABC", "creators": []any{map[string]any{"creatorType": "author", "name": "3GPP"}}, "tags": []any{map[string]any{"tag": "ISAC", "type": 1}}, "collections": []string{}}}
}

func TestFullSnapshotPaginationAndContentHash(t *testing.T) {
	items := make([]map[string]any, 140)
	for i := range items {
		items[i] = apiItem(fmt.Sprintf("%08d", i))
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Zotero-API-Version", "3")
		if r.Method != "GET" || r.URL.Query().Has("since") {
			t.Error("not a read-only full snapshot")
		}
		if r.URL.Path == "/api/" {
			return
		}
		w.Header().Set("Last-Modified-Version", "0")
		if strings.HasSuffix(r.URL.Path, "collections") {
			w.Header().Set("Total-Results", "0")
			if r.URL.Query().Get("format") != "keys" {
				fmt.Fprint(w, "[]")
			}
			return
		}
		w.Header().Set("Total-Results", "140")
		if r.URL.Query().Get("includeTrashed") != "1" {
			t.Error("trash omitted")
		}
		if r.URL.Query().Get("format") == "keys" {
			for _, v := range items {
				fmt.Fprintln(w, v["key"])
			}
			return
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		_ = json.NewEncoder(w).Encode(items[start:min(start+100, len(items))])
	}))
	defer s.Close()
	c, err := New(model.ZoteroConfig{BaseURL: s.URL + "/api/"})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Items) != 140 || snap.LibraryID != "42" {
		t.Fatalf("snapshot %+v", snap)
	}
	v := snap.Items[0]
	if v.Metadata.DOI != "10.1234/abc" || v.Metadata.Year == nil || *v.Metadata.Year != 2025 || v.Metadata.Creators[0].Name != "3GPP" {
		t.Fatalf("mapping %+v", v.Metadata)
	}
	before := v.Hash
	items[0]["version"] = 99
	data := items[0]["data"].(map[string]any)
	data["version"] = 99
	data["dateModified"] = "later"
	snap, err = c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Items[0].Hash != before {
		t.Fatal("version-only changes altered content hash")
	}
	data["title"] = "Unsynced local edit"
	snap, err = c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Items[0].Hash == before {
		t.Fatal("local edit missed despite unchanged version")
	}
}

func TestIncompleteOrChangingSnapshotIsRejected(t *testing.T) {
	for _, mode := range []string{"http_failure", "short_page", "keys_changed"} {
		t.Run(mode, func(t *testing.T) {
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
				w.Header().Set("Total-Results", "101")
				if mode == "keys_changed" {
					w.Header().Set("Total-Results", "1")
				}
				if r.URL.Query().Get("format") == "keys" {
					fmt.Fprintln(w, "99999999")
					return
				}
				if r.URL.Query().Get("start") == "100" {
					http.Error(w, "unavailable", 500)
					return
				}
				count := 100
				if mode == "short_page" || mode == "keys_changed" {
					count = 1
				}
				out := []map[string]any{}
				for i := 0; i < count; i++ {
					out = append(out, apiItem(fmt.Sprintf("%08d", i)))
				}
				_ = json.NewEncoder(w).Encode(out)
			}))
			defer s.Close()
			c, err := New(model.ZoteroConfig{BaseURL: s.URL + "/api/"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.Fetch(context.Background()); err == nil {
				t.Fatal("accepted partial/changing snapshot")
			}
		})
	}
}

func TestDisabledAPIAndRefusedConnectionAreDistinct(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "disabled", 403) }))
	c, _ := New(model.ZoteroConfig{BaseURL: s.URL + "/api/", StartOnDemand: true})
	launched := false
	c.Launch = func(context.Context) error { launched = true; return nil }
	if err := c.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "Settings") || launched {
		t.Fatalf("disabled API: %v launched=%v", err, launched)
	}
	s.Close()
	c.Config.StartOnDemand = false
	if err := c.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("refused connection: %v", err)
	}
	c.Config.StartOnDemand = true
	c.Launch = func(context.Context) error { launched = true; return fmt.Errorf("test launcher") }
	if err := c.Probe(context.Background()); err == nil || !launched || err.Error() != "test launcher" {
		t.Fatal("on-demand launcher not invoked")
	}
}

func TestReferencedDeletedCollectionsArePreserved(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(strconv.FormatBool(missing), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Zotero-API-Version", "3")
				w.Header().Set("Last-Modified-Version", "0")
				if r.URL.Path == "/api/" {
					return
				}
				if strings.HasSuffix(r.URL.Path, "collections/REMOVED1") {
					if missing {
						http.NotFound(w, r)
						return
					}
					fmt.Fprint(w, `{"key":"REMOVED1","library":{"type":"user","id":42},"data":{"name":"Deleted collection","deleted":true,"parentCollection":false}}`)
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
				v := apiItem("PAPER001")
				v["data"].(map[string]any)["collections"] = []string{"REMOVED1"}
				_ = json.NewEncoder(w).Encode([]any{v})
			}))
			defer s.Close()
			c, _ := New(model.ZoteroConfig{BaseURL: s.URL + "/api/"})
			snap, err := c.Fetch(context.Background())
			if err != nil || len(snap.Collections) != 1 || !snap.Collections[0].Deleted {
				t.Fatalf("tombstones %+v %v", snap.Collections, err)
			}
		})
	}
}

func TestEmptyUserAliasKeepsActualLibraryIdentity(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Zotero-API-Version", "3")
		if r.URL.Path == "/api/" {
			return
		}
		w.Header().Set("Total-Results", "0")
		w.Header().Set("Last-Modified-Version", "0")
		w.Header().Set("Link", `<https://www.zotero.org/users/42/items>; rel="alternate"`)
		if r.URL.Query().Get("format") != "keys" {
			fmt.Fprint(w, "[]")
		}
	}))
	defer s.Close()
	c, _ := New(model.ZoteroConfig{BaseURL: s.URL + "/api/"})
	snap, err := c.Fetch(context.Background())
	if err != nil || snap.LibraryID != "42" {
		t.Fatalf("empty library identity %+v %v", snap, err)
	}
}
