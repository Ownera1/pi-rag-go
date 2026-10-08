package mcpserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Ownera1/rag-go/internal/version"
	"github.com/Ownera1/rag-go/internal/workspace"
	"github.com/Ownera1/rag-go/pkg/rag"
)

// resolve opens the workspace a tool call targets; dir is the workspace
// argument. done releases the workspace after the call.
type resolve func(dir string) (core *rag.Core, done func(), err error)

// absolute rejects relative workspace arguments: they would resolve against
// the directory this server was launched in, not the caller's project.
func absolute(dir string) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("workspace must be the absolute path of the current project, got %q", dir)
	}
	return nil
}

// New serves one fixed workspace. Remote clients cannot know its local path,
// so the workspace argument is optional and only checked when given.
func New(core *rag.Core, lifecycle ...context.Context) *mcp.Server {
	root := core.WorkspaceDir()
	return serve(func(dir string) (*rag.Core, func(), error) {
		if dir != "" {
			if err := absolute(dir); err != nil {
				return nil, nil, err
			}
			found, err := workspace.DiscoverFrom(dir)
			if err != nil {
				return nil, nil, err
			}
			if found != root {
				return nil, nil, fmt.Errorf("this server serves only workspace %s", root)
			}
		}
		return core, func() {}, nil
	}, core.ReadOnly(), lifecycle...)
}

// NewDynamic resolves the workspace argument on every call, so one
// registration serves every project. The argument is required: calls carry no
// working directory, and the server's own stays where it was launched after
// the agent moves to another project, so a default could silently search the
// wrong workspace.
func NewDynamic(readOnly bool, lifecycle ...context.Context) *mcp.Server {
	return serve(func(dir string) (*rag.Core, func(), error) {
		if err := absolute(dir); err != nil {
			return nil, nil, err
		}
		root, err := workspace.DiscoverFrom(dir)
		if err != nil {
			return nil, nil, err
		}
		core, err := rag.Open(rag.Options{WorkspaceDir: root, ReadOnly: readOnly})
		if err != nil {
			return nil, nil, err
		}
		return core, func() { _ = core.Close() }, nil
	}, readOnly, lifecycle...)
}

func serve(open resolve, readOnly bool, lifecycle ...context.Context) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "rag-go", Version: version.Version}, &mcp.ServerOptions{Instructions: instructions})
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
		Workspace     string              `json:"workspace,omitempty" jsonschema:"absolute path of the current project, or any directory inside it; required unless this server is pinned to one project with --workspace"`
		Filter        *rag.MetadataFilter `json:"filter,omitempty"`
		Document      string              `json:"document,omitempty" jsonschema:"search only this document: its id, path, or a unique part of its path or title"`
		Query         string              `json:"query"`
		TopK          int                 `json:"top_k,omitempty"`
		CandidateTopK int                 `json:"candidate_top_k,omitempty"`
		Alpha         *float64            `json:"alpha,omitempty"`
		Mode          string              `json:"mode,omitempty" jsonschema:"hybrid (default), bm25, vector, or literal: exact text such as \\tag{28}, ignoring whitespace, in document order"`
		DisableSync   bool                `json:"disable_sync,omitempty"`
		DisableRerank bool                `json:"disable_rerank,omitempty"`
		RequireRerank bool                `json:"require_rerank,omitempty"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "rag_query",
		Description: "Search this workspace; local queries synchronize changed documents unless disable_sync=true",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in queryIn) (*mcp.CallToolResult, rag.QueryResult, error) {
		core, done, err := open(in.Workspace)
		if err != nil {
			return nil, rag.QueryResult{}, err
		}
		defer done()
		r, e := core.Query(ctx, in.Query, rag.QueryOptions{
			Filter:        in.Filter,
			Document:      in.Document,
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

	type workspaceIn struct {
		Workspace string `json:"workspace,omitempty" jsonschema:"absolute path of the current project, or any directory inside it; required unless this server is pinned to one project with --workspace"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "rag_status",
		Description: "Show workspace and index status without triggering model calls",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in workspaceIn) (*mcp.CallToolResult, rag.Status, error) {
		core, done, err := open(in.Workspace)
		if err != nil {
			return nil, rag.Status{}, err
		}
		defer done()
		r, e := core.Status(ctx)
		return nil, r, e
	})

	type listOut struct {
		Documents []rag.DocumentInfo `json:"documents"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "rag_list_documents",
		Description: "List indexed documents with their ids, titles and versions",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in workspaceIn) (*mcp.CallToolResult, listOut, error) {
		core, done, err := open(in.Workspace)
		if err != nil {
			return nil, listOut{}, err
		}
		defer done()
		r, e := core.Documents(ctx)
		return nil, listOut{r}, e
	})

	type documentIn struct {
		Workspace string `json:"workspace,omitempty" jsonschema:"absolute path of the current project, or any directory inside it; required unless this server is pinned to one project with --workspace"`
		Document  string `json:"document" jsonschema:"document id, path, or a unique part of its path or title"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "rag_outline",
		Description: "Show one document's sections as chunk ranges and pages, to read a section with rag_read from/to",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in documentIn) (*mcp.CallToolResult, rag.Outline, error) {
		core, done, err := open(in.Workspace)
		if err != nil {
			return nil, rag.Outline{}, err
		}
		defer done()
		r, e := core.Outline(ctx, in.Document)
		return nil, r, e
	})

	type readIn struct {
		Workspace string `json:"workspace,omitempty" jsonschema:"absolute path of the current project, or any directory inside it; required unless this server is pinned to one project with --workspace"`
		rag.ReadOptions
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "rag_read",
		Description: "Read one document's chunks in order: around a hit's chunk id (before/after neighbours, default 2), " +
			"a from/to chunk index range, or pages such as \"8-9\". Returns at most max_tokens (default 4000, max 16000); " +
			"when truncated, continue from next. Reads the index only: no sync and no model calls",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in readIn) (*mcp.CallToolResult, rag.ReadResult, error) {
		core, done, err := open(in.Workspace)
		if err != nil {
			return nil, rag.ReadResult{}, err
		}
		defer done()
		r, e := core.Read(ctx, in.ReadOptions)
		return nil, r, e
	})

	if !readOnly {
		mcp.AddTool(s, &mcp.Tool{Name: "rag_zotero_sync", Description: "Read a complete Zotero Local API metadata snapshot into this workspace catalog; no document embedding or Zotero writes"}, func(ctx context.Context, _ *mcp.CallToolRequest, in workspaceIn) (*mcp.CallToolResult, rag.ZoteroSyncResult, error) {
			core, done, err := open(in.Workspace)
			if err != nil {
				return nil, rag.ZoteroSyncResult{}, err
			}
			defer done()
			r, e := core.SyncZotero(ctx, nil)
			if e != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: e.Error()}}}, r, nil
			}
			return nil, r, e
		})
		mcp.AddTool(s, &mcp.Tool{Name: "rag_zotero_match", Description: "Match indexed documents to cached Zotero metadata; fuzzy title matches are candidates only"}, func(ctx context.Context, _ *mcp.CallToolRequest, in workspaceIn) (*mcp.CallToolResult, rag.ZoteroMatchResult, error) {
			core, done, err := open(in.Workspace)
			if err != nil {
				return nil, rag.ZoteroMatchResult{}, err
			}
			defer done()
			r, e := core.MatchZotero(ctx)
			if e != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: e.Error()}}}, r, nil
			}
			return nil, r, e
		})
		type linkIn struct {
			Workspace string              `json:"workspace,omitempty" jsonschema:"absolute path of the current project, or any directory inside it; required unless this server is pinned to one project with --workspace"`
			Path      string              `json:"path"`
			Reference rag.ZoteroReference `json:"reference"`
		}
		mcp.AddTool(s, &mcp.Tool{Name: "rag_zotero_link", Description: "Save a manually confirmed, locked document-to-Zotero association in the catalog"}, func(ctx context.Context, _ *mcp.CallToolRequest, in linkIn) (*mcp.CallToolResult, *rag.ZoteroMetadata, error) {
			core, done, err := open(in.Workspace)
			if err != nil {
				return nil, nil, err
			}
			defer done()
			r, e := core.LinkZotero(ctx, in.Path, in.Reference, false)
			return nil, r, e
		})
		mcp.AddTool(s, &mcp.Tool{
			Name:        "rag_sync",
			Description: "Synchronize this workspace documents directory",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in workspaceIn) (*mcp.CallToolResult, rag.IndexResult, error) {
			core, done, err := open(in.Workspace)
			if err != nil {
				return nil, rag.IndexResult{}, err
			}
			defer done()
			r, e := core.Sync(ctx)
			return indexResponse(r, e)
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "rag_rebuild",
			Description: "Build a new generation from workspace documents and atomically publish it",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in workspaceIn) (*mcp.CallToolResult, rag.IndexResult, error) {
			core, done, err := open(in.Workspace)
			if err != nil {
				return nil, rag.IndexResult{}, err
			}
			defer done()
			r, e := core.Rebuild(ctx)
			return indexResponse(r, e)
		})

	}
	return s
}

// instructions reach every MCP client at initialization.
const instructions = `rag-go searches and reads the documents of a workspace, typically research papers.

To explain a paper:
1. Identify it: rag_list_documents gives ids and titles; rag_query hits carry chunk ids "<document id>-<index>".
2. See its structure with rag_outline, then read whole sections with rag_read from/to rather than relying on search snippets.
3. Search within it with rag_query document=<id>; jump to an equation number with mode=literal, e.g. query "\tag{28}".
4. Expand a hit with rag_read around=<chunk id>; follow definitions, assumptions and cited equations the same way.
5. For figures, read the pages of the returned pdf path when the client can open files.

Cite page numbers and sections from the passages. Distinguish the authors' text, your own derivations, and points the documents do not support.`

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
