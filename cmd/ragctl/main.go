package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}

func run() error {
	endpoint := flag.String("endpoint", "http://127.0.0.1:7331/mcp", "ragd MCP URL")
	mode := flag.String("mode", "hybrid", "query mode: hybrid or bm25")
	top := flag.Int("top-k", 0, "query result limit")
	confirm := flag.Bool("confirm", false, "confirm clear operation")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		return errors.New("usage: ragctl [flags] query|index|status|refresh|list|rebuild|clear [args]")
	}
	name := ""
	input := map[string]any{}
	switch args[0] {
	case "query":
		name = "rag_query"
		if len(args) < 2 {
			return errors.New("query text required")
		}
		input["query"] = strings.Join(args[1:], " ")
		input["mode"] = *mode
		if *top > 0 {
			input["top_k"] = *top
		}
	case "index":
		name = "rag_index"
		if len(args) < 2 {
			return errors.New("at least one file or directory required")
		}
		input["paths"] = args[1:]
	case "status":
		name = "rag_status"
	case "refresh":
		name = "rag_refresh"
	case "list":
		name = "rag_list_documents"
	case "rebuild":
		name = "rag_rebuild"
	case "clear":
		name = "rag_clear"
		input["confirm"] = *confirm
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "ragctl", Version: "0.1.0"}, nil)
	session, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: *endpoint, DisableStandaloneSSE: true}, nil)
	if e != nil {
		return e
	}
	defer session.Close()
	result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
	if e != nil {
		return e
	}
	if result.IsError {
		for _, content := range result.Content {
			if t, ok := content.(*mcp.TextContent); ok {
				return errors.New(t.Text)
			}
		}
		return errors.New("MCP tool failed")
	}
	if result.StructuredContent != nil {
		b, e := json.MarshalIndent(result.StructuredContent, "", "  ")
		if e != nil {
			return e
		}
		fmt.Println(string(b))
		return nil
	}
	for _, content := range result.Content {
		if t, ok := content.(*mcp.TextContent); ok {
			fmt.Println(t.Text)
		}
	}
	return nil
}
