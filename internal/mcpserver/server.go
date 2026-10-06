package mcpserver

import (
	"context"
	"net"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Ownera1/rag-go/pkg/rag"
)

func New(core *rag.Core, lifecycle ...context.Context) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "rag-go", Version: "0.2.0"}, nil)
	if len(lifecycle) > 0 {
		// Stateful MCP sessions detach tool contexts from the initiating HTTP
		// request. Tie each call to this MCP process's lifetime explicitly.
		service := lifecycle[0]
		s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
				if err := service.Err(); err != nil {
					return nil, err
				}
				ctx, cancel := context.WithCancel(ctx)
				stop := context.AfterFunc(service, cancel)
				defer stop()
				defer cancel()
				return next(ctx, method, request)
			}
		})
	}
	type queryIn struct {
		Filter        *rag.MetadataFilter `json:"filter,omitempty"`
		Query         string              `json:"query"`
		TopK          int                 `json:"top_k,omitempty"`
		CandidateTopK int                 `json:"candidate_top_k,omitempty"`
		Alpha         *float64            `json:"alpha,omitempty"`
		Mode          string              `json:"mode,omitempty"`
		DisableSync   bool                `json:"disable_sync,omitempty"`
		DisableRerank bool                `json:"disable_rerank,omitempty"`
		RequireRerank bool                `json:"require_rerank,omitempty"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "rag_query",
		Description: "Search this workspace; local queries synchronize changed documents unless disable_sync=true",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: core.ReadOnly()},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queryIn) (*mcp.CallToolResult, rag.QueryResult, error) {
		r, e := core.Query(ctx, in.Query, rag.QueryOptions{
			Filter:        in.Filter,
			DisableSync:   in.DisableSync,
			TopK:          in.TopK,
			CandidateTopK: in.CandidateTopK,
			Alpha:         in.Alpha,
			Mode:          in.Mode,
			DisableRerank: in.DisableRerank,
			RequireRerank: in.RequireRerank,
		})
		return nil, r, e
	})

	type empty struct{}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "rag_status",
		Description: "Show workspace and index status without triggering model calls",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, rag.Status, error) {
		r, e := core.Status(ctx)
		return nil, r, e
	})

	type listOut struct {
		Documents []string `json:"documents"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "rag_list_documents",
		Description: "List indexed canonical document paths",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, listOut, error) {
		r, e := core.ListDocuments(ctx)
		return nil, listOut{r}, e
	})

	if !core.ReadOnly() {
		mcp.AddTool(s, &mcp.Tool{Name: "rag_zotero_sync", Description: "Read a complete Zotero Local API metadata snapshot into this workspace catalog; no document embedding or Zotero writes"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, rag.ZoteroSyncResult, error) {
			r, e := core.SyncZotero(ctx, nil)
			if e != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: e.Error()}}}, r, nil
			}
			return nil, r, e
		})
		mcp.AddTool(s, &mcp.Tool{Name: "rag_zotero_match", Description: "Match indexed documents to cached Zotero metadata; fuzzy title matches are candidates only"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, rag.ZoteroMatchResult, error) {
			r, e := core.MatchZotero(ctx)
			if e != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: e.Error()}}}, r, nil
			}
			return nil, r, e
		})
		type linkIn struct {
			Path      string              `json:"path"`
			Reference rag.ZoteroReference `json:"reference"`
		}
		mcp.AddTool(s, &mcp.Tool{Name: "rag_zotero_link", Description: "Save a manually confirmed, locked document-to-Zotero association in the catalog"}, func(ctx context.Context, _ *mcp.CallToolRequest, in linkIn) (*mcp.CallToolResult, *rag.ZoteroMetadata, error) {
			r, e := core.LinkZotero(ctx, in.Path, in.Reference, false)
			return nil, r, e
		})
		mcp.AddTool(s, &mcp.Tool{
			Name:        "rag_sync",
			Description: "Synchronize this workspace documents directory",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, rag.IndexResult, error) {
			r, e := core.Sync(ctx)
			return indexResponse(r, e)
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "rag_rebuild",
			Description: "Build a new generation from workspace documents and atomically publish it",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, rag.IndexResult, error) {
			r, e := core.Rebuild(ctx)
			return indexResponse(r, e)
		})

	}
	return s
}

func indexResponse(r rag.IndexResult, err error) (*mcp.CallToolResult, rag.IndexResult, error) {
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, r, nil
	}
	return nil, r, nil
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
