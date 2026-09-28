package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/Ownera1/pi-rag-go/pkg/rag"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func New(core *rag.Core) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "pi-rag-go", Version: "0.1.0"}, nil)
	type queryIn struct {
		Query         string   `json:"query"`
		TopK          int      `json:"top_k,omitempty"`
		CandidateTopK int      `json:"candidate_top_k,omitempty"`
		Alpha         *float64 `json:"alpha,omitempty"`
		Mode          string   `json:"mode,omitempty"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "rag_query", Description: "Search the shared knowledge store and return structured source hits"}, func(ctx context.Context, _ *mcp.CallToolRequest, in queryIn) (*mcp.CallToolResult, rag.QueryResult, error) {
		r, e := core.Query(ctx, in.Query, rag.QueryOptions{TopK: in.TopK, CandidateTopK: in.CandidateTopK, Alpha: in.Alpha, Mode: in.Mode})
		return nil, r, e
	})
	type indexIn struct {
		Paths []string `json:"paths"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "rag_index", Description: "Index files or directories and track them for refresh"}, func(ctx context.Context, _ *mcp.CallToolRequest, in indexIn) (*mcp.CallToolResult, rag.IndexResult, error) {
		r, e := core.Index(ctx, in.Paths)
		return nil, r, e
	})
	type empty struct{}
	mcp.AddTool(s, &mcp.Tool{Name: "rag_status", Description: "Show store and index status without triggering model calls"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, rag.Status, error) {
		r, e := core.Status(ctx)
		return nil, r, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "rag_refresh", Description: "Rescan tracked paths and update changed documents"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, rag.IndexResult, error) {
		r, e := core.Refresh(ctx)
		return nil, r, e
	})
	type listOut struct {
		Documents []string `json:"documents"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "rag_list_documents", Description: "List indexed source paths"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, listOut, error) {
		r, e := core.ListDocuments(ctx)
		return nil, listOut{r}, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "rag_rebuild", Description: "Build a new generation from tracked paths and atomically publish it"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, rag.IndexResult, error) {
		r, e := core.Rebuild(ctx)
		return nil, r, e
	})
	type clearIn struct {
		Confirm bool `json:"confirm"`
	}
	type clearOut struct {
		Cleared bool `json:"cleared"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "rag_clear", Description: "Publish an empty index while retaining tracked paths and old generations; requires confirm=true"}, func(ctx context.Context, _ *mcp.CallToolRequest, in clearIn) (*mcp.CallToolResult, clearOut, error) {
		if !in.Confirm {
			return nil, clearOut{}, errors.New("confirm=true is required")
		}
		e := core.Clear(ctx)
		return nil, clearOut{e == nil}, e
	})
	return s
}
func Handler(s *mcp.Server) http.Handler {
	base := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, e := net.SplitHostPort(r.Host)
		if e != nil {
			host = r.Host
		}
		if host != "localhost" && net.ParseIP(host) == nil {
			http.Error(w, "loopback host required", http.StatusForbidden)
			return
		}
		if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
			http.Error(w, "loopback host required", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host || !(u.Scheme == "http" || u.Scheme == "https") {
				http.Error(w, "origin mismatch", http.StatusForbidden)
				return
			}
		}
		base.ServeHTTP(w, r)
	})
}
func Proxy(ctx context.Context, endpoint string) error {
	return proxy(ctx, endpoint, &mcp.StdioTransport{})
}

func proxy(ctx context.Context, endpoint string, transport mcp.Transport) error {
	client := mcp.NewClient(&mcp.Implementation{Name: "pi-rag-go-stdio", Version: "0.1.0"}, nil)
	remote, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, DisableStandaloneSSE: true}, nil)
	if e != nil {
		return fmt.Errorf("connect to ragd: %w", e)
	}
	defer remote.Close()
	tools, e := remote.ListTools(ctx, nil)
	if e != nil {
		return e
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "pi-rag-go-stdio", Version: "0.1.0"}, nil)
	for _, t := range tools.Tools {
		tool := t
		server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return remote.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: req.Params.Arguments})
		})
	}
	if len(tools.Tools) == 0 {
		return errors.New("remote MCP server has no tools")
	}
	return server.Run(ctx, transport)
}
func Endpoint(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	return "http://" + addr + "/mcp"
}
