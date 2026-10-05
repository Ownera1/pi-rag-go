package command

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Ownera1/rag-go/internal/mcpserver"
	"github.com/Ownera1/rag-go/pkg/rag"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFacadeGlobalAndTrailingOptionsAndLiteralQuery(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	type in struct {
		DisableRerank bool   `json:"disable_rerank"`
		Query         string `json:"query"`
		Mode          string `json:"mode"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "rag_query", Description: "fixture"}, func(ctx context.Context, _ *mcp.CallToolRequest, args in) (*mcp.CallToolResult, rag.QueryResult, error) {
		if args.Mode != "bm25" {
			t.Error(args.Mode)
		}
		return nil, rag.QueryResult{Query: args.Query, Method: "bm25"}, nil
	})
	server := httptest.NewServer(mcpserver.Handler(s))
	defer server.Close()
	endpoint := server.URL + "/mcp"
	cases := []struct {
		args  []string
		query string
	}{
		{[]string{"query", "hello world", "--mode", "bm25", "--endpoint", endpoint}, "hello world"},
		{[]string{"--endpoint", endpoint, "query", "hello world", "--mode", "bm25"}, "hello world"},
		{[]string{"query", "--endpoint", endpoint, "--mode", "bm25", "--", "--store", "literal"}, "--store literal"},
	}
	for _, test := range cases {
		var out, stderr bytes.Buffer
		if err := Run(context.Background(), test.args, strings.NewReader(""), &out, &stderr); err != nil {
			t.Fatal(err, stderr.String())
		}
		var result rag.QueryResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Query != test.query {
			t.Fatal(out.String(), err)
		}
	}
}
