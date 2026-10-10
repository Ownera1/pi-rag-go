package command

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/Ownera1/rag-go/internal/mcpserver"
	"github.com/Ownera1/rag-go/internal/workspace"
	"github.com/Ownera1/rag-go/pkg/rag"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func MCP(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("rag mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := workspaceFlag(fs)
	transport := fs.String("transport", "stdio", "stdio or http")
	readOnly := fs.Bool("read-only", false, "expose only query/status/list; disable automatic sync")
	listen := fs.String("listen", "127.0.0.1:0", "HTTP loopback address; 0 chooses a free port")
	if err := fs.Parse(ReorderFlags(args, map[string]bool{"read-only": true, "h": true, "help": true})); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *transport != "stdio" && *transport != "http" {
		return errors.New("transport must be stdio or http")
	}
	if *transport == "stdio" && *root == "" {
		// A user-wide registration starts here in any directory, including
		// projects without a workspace; each call finds its own workspace.
		err := mcpserver.NewDynamic(*readOnly, ctx).Run(ctx, &mcp.StdioTransport{})
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	dir, err := workspace.Resolve(*root)
	if err != nil {
		return err
	}
	core, err := rag.Open(rag.Options{WorkspaceDir: dir, ReadOnly: *readOnly || *transport == "http"})
	if err != nil {
		return explainMissing(err)
	}
	defer core.Close()
	server := mcpserver.New(core, ctx)
	if *transport == "stdio" {
		err = server.Run(ctx, &mcp.StdioTransport{})
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("HTTP listen must be loopback")
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	httpServer := &http.Server{Handler: mcpserver.Handler(server), ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	stop := context.AfterFunc(ctx, func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	})
	defer stop()
	fmt.Fprintf(stderr, "rag-go read-only MCP: http://%s/mcp\n", listener.Addr())
	err = httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) || ctx.Err() != nil {
		return nil
	}
	return err
}
