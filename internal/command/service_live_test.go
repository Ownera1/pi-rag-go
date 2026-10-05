package command

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Ownera1/rag-go/pkg/rag"
)

// Opt-in: a native user session and a built rag binary are required. launchd uses
// a temporary plist; systemd uses the ephemeral CI user's configuration directory.
func TestRealUserServiceLifecycle(t *testing.T) {
	if !((runtime.GOOS == "darwin" && os.Getenv("RAG_TEST_LAUNCHAGENT") == "1") || (runtime.GOOS == "linux" && os.Getenv("RAG_TEST_SYSTEMD") == "1")) {
		t.Skip("opt-in native user-service acceptance")
	}
	binary := os.Getenv("RAG_TEST_BINARY")
	if binary == "" {
		t.Fatal("RAG_TEST_BINARY is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	job := "gui/" + strconv.Itoa(os.Getuid()) + "/" + serviceLabel
	if runtime.GOOS == "darwin" {
		if _, err := exec.Command("launchctl", "print", job).Output(); err == nil {
			t.Skip("an existing rag-go user service must be preserved")
		}
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-only" {
			w.WriteHeader(401)
			return
		}
		var request struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		data := []any{}
		for i := range request.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float64{1, 0}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer provider.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := listener.Addr().String()
	listener.Close()
	home := t.TempDir()
	store := filepath.Join(home, "store")
	os.MkdirAll(store, 0700)
	cfg := rag.DefaultConfig()
	cfg.Embedding = rag.ProviderConfig{Type: "openai", Model: "live-fixture", Dimensions: 2, BaseURL: provider.URL, APIKeyEnv: "RAG_LIVE_TEST_KEY"}
	cfg.Chunking.Mode = "legacy"
	cfg.Runtime.Listen = listen
	cfg.Runtime.AutoRefresh.Enabled = true
	cfg.Runtime.AutoRefresh.DebounceMs = 50
	cfg.Runtime.AutoRefresh.RescanMs = 150
	atomicJSON(filepath.Join(store, "config.json"), cfg)
	atomicJSON(filepath.Join(store, "credentials.json"), map[string]string{"RAG_LIVE_TEST_KEY": "fixture-only"})
	configDir := home
	if runtime.GOOS == "linux" {
		configDir, err = os.UserConfigDir()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(filepath.Join(configDir, "systemd", "user", "rag-go.service")); err == nil {
			t.Skip("an existing user unit must be preserved")
		}
	}
	m := serviceManager{platform: runtime.GOOS, home: home, configDir: configDir, binary: binary, execute: external}
	var out, stderr bytes.Buffer
	defer func() {
		if _, err := os.Stat(m.path() + ".json"); err == nil {
			if err = m.run(context.Background(), []string{"uninstall"}, store, &out, &stderr); err != nil {
				t.Errorf("temporary service cleanup failed: %v", err)
			}
		}
	}()
	if err = m.run(ctx, []string{"install"}, store, &out, &stderr); err != nil {
		t.Fatalf("install: %v", err)
	}
	source := filepath.Join(home, "papers")
	os.Mkdir(source, 0700)
	file := filepath.Join(source, "evidence.txt")
	os.WriteFile(file, []byte("launchagent credentials confirmed evidence"), 0600)
	endpoint := "http://" + listen + "/mcp"
	if err = Control(ctx, []string{"--endpoint", endpoint, "index", source}, &out, &stderr); err != nil {
		t.Fatalf("index with background credentials: %v", err)
	}
	out.Reset()
	if err = Control(ctx, []string{"--endpoint", endpoint, "query", "credentials confirmed"}, &out, &stderr); err != nil || !strings.Contains(out.String(), "launchagent credentials confirmed") {
		t.Fatalf("query: %v %s", err, out.String())
	}
	for _, verb := range []string{"stop", "start", "restart"} {
		if err = m.run(ctx, []string{verb}, store, &out, &stderr); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
	}
	os.Remove(file)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		s, e := statusAt(ctx, endpoint)
		if e == nil && s.Files == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("watcher deletion did not settle after service restart")
}
