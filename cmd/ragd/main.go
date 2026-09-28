package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/Ownera1/pi-rag-go/internal/mcpserver"
	"github.com/Ownera1/pi-rag-go/pkg/rag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("ragd failed", "error", e)
		os.Exit(1)
	}
}
func run() error {
	mode := "serve"
	if len(os.Args) > 1 && (os.Args[1] == "serve" || os.Args[1] == "stdio") {
		mode = os.Args[1]
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}
	store := flag.String("store", "", "knowledge store directory")
	config := flag.String("config", "", "configuration JSON path")
	addr := flag.String("listen", "127.0.0.1:7331", "loopback HTTP address")
	endpoint := flag.String("endpoint", "http://127.0.0.1:7331/mcp", "remote MCP URL for stdio mode")
	legacy := flag.Bool("legacy-readonly", false, "query a TypeScript store without changing it")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if mode == "stdio" {
		return mcpserver.Proxy(ctx, *endpoint)
	}
	if *store == "" {
		return fmt.Errorf("--store is required")
	}
	host, _, e := net.SplitHostPort(*addr)
	if e != nil {
		return e
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("--listen must use a loopback address")
		}
	}
	core, e := rag.Open(rag.Options{StoreDir: *store, ConfigPath: *config, LegacyReadOnly: *legacy})
	if e != nil {
		return e
	}
	defer core.Close()
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.Handler(mcpserver.New(core)))
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(closeCtx)
	}()
	slog.Info("RAG MCP server ready", "address", *addr, "store", *store, "legacyReadOnly", *legacy)
	e = srv.ListenAndServe()
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}
