package command

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/Ownera1/rag-go/internal/daemon"
	"github.com/Ownera1/rag-go/internal/mcpserver"
	"github.com/Ownera1/rag-go/internal/model"
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
	absStore, e := filepath.Abs(*store)
	if e != nil {
		return e
	}
	*store = absStore
	if e := applyCredentials(*store); e != nil {
		return e
	}
	cfg, e := settingsForDaemon(*store, *config, *legacy)
	if e != nil {
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
	var automatic *daemon.Watcher
	var preparer rag.SourcePreparer
	if !*legacy && cfg.Runtime.PDF.Backend != "" {
		preparer = &daemon.PDFPreparer{Store: *store, Config: cfg.Runtime.PDF}
	}
	core, e := rag.Open(rag.Options{StoreDir: *store, ConfigPath: *config, LegacyReadOnly: *legacy, SourcePreparer: preparer, AutoRefreshStatus: func() model.AutoRefreshStatus {
		if automatic != nil {
			return automatic.Status()
		}
		return model.AutoRefreshStatus{}
	}})
	if e != nil {
		return e
	}
	defer core.Close()
	watchCtx, stopWatch := context.WithCancel(ctx)
	if !*legacy && cfg.Runtime.AutoRefresh.Enabled {
		automatic = daemon.NewWatcher(core, *store, cfg.Runtime.AutoRefresh)
		go automatic.Run(watchCtx)
	}
	defer func() {
		stopWatch()
		if automatic != nil {
			automatic.Wait()
		}
	}()
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
