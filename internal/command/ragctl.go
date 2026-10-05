package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func Control(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("ragctl", flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("endpoint", "http://127.0.0.1:7331/mcp", "ragd MCP URL")
	mode := flags.String("mode", "hybrid", "query mode: hybrid, vector or bm25")
	noRerank := flags.Bool("no-rerank", false, "disable reranking for this query")
	keep := flags.Int("keep", 3, "generations to retain during cleanup, including active")
	dryRun := flags.Bool("dry-run", false, "preview cleanup even when confirmed")
	top := flags.Int("top-k", 0, "query result limit")
	confirm := flags.Bool("confirm", false, "confirm clear or cleanup operation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	args = flags.Args()
	if len(args) == 0 {
		return errors.New("usage: ragctl [flags] query|index|status|refresh|list|rebuild|clear|cleanup [args]")
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
		input["disable_rerank"] = *noRerank
		if *top > 0 {
			input["top_k"] = *top
		}
	case "index":
		name = "rag_index"
		if len(args) < 2 {
			return errors.New("at least one file or directory required")
		}
		input["paths"] = args[1:]
	case "remove":
		name = "rag_remove"
		if len(args) < 2 {
			return errors.New("at least one tracked root required")
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
	case "cleanup":
		name = "rag_cleanup"
		input["keep"], input["dry_run"], input["confirm"] = *keep, *dryRun, *confirm
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
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
		fmt.Fprintln(stdout, string(b))
		if name == "rag_index" || name == "rag_refresh" || name == "rag_rebuild" {
			var outcome struct {
				Failed int `json:"failed"`
			}
			if e := json.Unmarshal(b, &outcome); e != nil {
				return e
			}
			if outcome.Failed > 0 {
				return fmt.Errorf("%d files failed; inspect failures in JSON output and rag_status", outcome.Failed)
			}
		}
		return nil
	}
	for _, content := range result.Content {
		if t, ok := content.(*mcp.TextContent); ok {
			fmt.Fprintln(stdout, t.Text)
		}
	}
	return nil
}
