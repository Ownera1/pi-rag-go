package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Ownera1/rag-go/internal/evaluate"
	"github.com/Ownera1/rag-go/pkg/rag"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func rate(value string) (*float64, error) {
	if value == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(value, 64)
	return &v, err
}

func Evaluate(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("rageval", flag.ContinueOnError)
	endpoint := flags.String("endpoint", "http://127.0.0.1:7331/mcp", "ragd MCP URL")
	dataset := flags.String("dataset", "", "annotated JSONL evaluation dataset")
	modes := flags.String("modes", "bm25,vector,hybrid", "comma-separated modes; add rerank when a reranker is configured")
	topK := flags.Int("top-k", 5, "retrieval evaluation cutoff")
	output := flags.String("output", "", "optional JSON report path")
	embRate := flags.String("embedding-usd-per-million-tokens", "", "optional rate for estimated embedding cost")
	rerankRate := flags.String("rerank-usd-per-million-tokens", "", "optional rate for estimated rerank cost")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dataset == "" {
		return errors.New("--dataset is required")
	}
	f, err := os.Open(*dataset)
	if err != nil {
		return err
	}
	cases, err := evaluate.ReadCases(f)
	f.Close()
	if err != nil {
		return err
	}
	eRate, err := rate(*embRate)
	if err != nil {
		return err
	}
	rRate, err := rate(*rerankRate)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "rageval", Version: "0.2.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: *endpoint, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return err
	}
	defer session.Close()
	query := func(ctx context.Context, text string, options rag.QueryOptions) (rag.QueryResult, error) {
		response, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "rag_query", Arguments: map[string]any{
			"query": text, "top_k": options.TopK, "candidate_top_k": options.CandidateTopK, "mode": options.Mode,
			"disable_rerank": options.DisableRerank, "require_rerank": options.RequireRerank,
		}})
		if err != nil {
			return rag.QueryResult{}, err
		}
		if response.IsError {
			for _, content := range response.Content {
				if text, ok := content.(*mcp.TextContent); ok {
					return rag.QueryResult{}, errors.New(text.Text)
				}
			}
			return rag.QueryResult{}, errors.New("query tool failed")
		}
		b, err := json.Marshal(response.StructuredContent)
		if err != nil {
			return rag.QueryResult{}, err
		}
		var result rag.QueryResult
		err = json.Unmarshal(b, &result)
		return result, err
	}
	selected := strings.Split(*modes, ",")
	for i := range selected {
		selected[i] = strings.TrimSpace(selected[i])
	}
	report, err := evaluate.Run(ctx, cases, query, evaluate.Options{TopK: *topK, Modes: selected, Dataset: *dataset, EmbeddingUSDPerMillionTokens: eRate, RerankUSDPerMillionTokens: rRate})
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if *output != "" {
		if err = os.MkdirAll(filepath.Dir(*output), 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(*output), ".rageval-*.json")
		if err != nil {
			return err
		}
		name := f.Name()
		defer os.Remove(name)
		if _, err = f.Write(b); err != nil {
			f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = os.Rename(name, *output); err != nil {
			return err
		}
	}
	if _, err = stdout.Write(b); err != nil {
		return err
	}
	for _, summary := range report.Summaries {
		if summary.Failed > 0 {
			return fmt.Errorf("evaluation recorded failures in mode %s; inspect JSON report", summary.Mode)
		}
	}
	return nil
}
