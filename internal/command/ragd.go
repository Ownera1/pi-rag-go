package command

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Ownera1/rag-go/internal/mcpserver"
	"github.com/Ownera1/rag-go/pkg/rag"
)

func Serve(ctx context.Context, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("ragd", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := "serve"
	if len(args) > 0 && (args[0] == "serve" || args[0] == "stdio") {
		mode = args[0]
		args = args[1:]
	}
	store := flags.String("store", "", "knowledge store directory")
	config := flags.String("config", "", "configuration JSON path")
	addr := flags.String("listen", "127.0.0.1:7331", "loopback HTTP address")
	endpoint := flags.String("endpoint", "http://127.0.0.1:7331/mcp", "remote MCP URL for stdio mode")
	legacy := flags.Bool("legacy-readonly", false, "query a TypeScript store without changing it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected serve arguments")
	}
	if mode == "stdio" {
		return mcpserver.Proxy(ctx, *endpoint)
	}
	if *store == "" {
		return fmt.Errorf("--store is required")
	}
	if e := applyCredentials(*store); e != nil {
		return e
	}
	if cfg, e := settingsForDaemon(*store, *config, *legacy); e != nil {
		return e
	} else {
		explicit := false
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "listen" {
				explicit = true
			}
		})
		if !explicit && cfg.Runtime.Listen != "" {
			*addr = cfg.Runtime.Listen
		}
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
	mux.Handle("/mcp", mcpserver.Handler(mcpserver.New(core, ctx)))
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
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
