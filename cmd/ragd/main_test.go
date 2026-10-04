package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Ownera1/pi-rag-go/pkg/rag"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRagdProcessHelper(t *testing.T) {
	if os.Getenv("RAGD_TEST_HELPER") != "1" {
		return
	}
	os.Args = []string{os.Args[0], "serve", "--store", os.Getenv("RAGD_TEST_STORE"), "--listen", os.Getenv("RAGD_TEST_LISTEN")}
	flag.CommandLine = flag.NewFlagSet("ragd", flag.ContinueOnError)
	if err := run(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestSIGTERMCancelsActiveIndexAndReleasesWriter(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer model.Close()
	root := t.TempDir()
	cfg := rag.DefaultConfig()
	cfg.Embedding = rag.ProviderConfig{Type: "openai", Model: "test", Dimensions: 2, BaseURL: model.URL}
	cfg.Chunking.Mode = "legacy"
	cfg.HTTPTimeoutMs, cfg.HTTPMaxRetries = 30000, 0
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(root, "config.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(file, []byte("known evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestRagdProcessHelper$")
	command.Env = append(os.Environ(), "RAGD_TEST_HELPER=1", "RAGD_TEST_STORE="+root, "RAGD_TEST_LISTEN="+addr)
	var logs bytes.Buffer
	command.Stderr = &logs
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "shutdown-test", Version: "1"}, nil)
	var session *mcp.ClientSession
	for ctx.Err() == nil {
		session, err = client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: "http://" + addr + "/mcp", DisableStandaloneSSE: true}, nil)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()
	queryDone := make(chan struct{})
	go func() {
		defer close(queryDone)
		_, _ = session.CallTool(ctx, &mcp.CallToolParams{Name: "rag_index", Arguments: map[string]any{"paths": []string{file}}})
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("index did not reach provider")
	}
	if err = command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	select {
	case err = <-exited:
		if err != nil {
			t.Fatalf("shutdown: %v\n%s", err, logs.String())
		}
	case <-time.After(5 * time.Second):
		command.Process.Kill()
		<-exited
		t.Fatalf("shutdown blocked on model call\n%s", logs.String())
	}
	select {
	case <-queryDone:
	case <-ctx.Done():
		t.Fatal("client call did not terminate")
	}
	core, err := rag.Open(rag.Options{StoreDir: root})
	if err != nil {
		t.Fatalf("writer lock not released: %v", err)
	}
	core.Close()
}
