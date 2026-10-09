// Package evaluate measures passage retrieval against explicit relevance labels.
package evaluate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Ownera1/rag-go/pkg/rag"
)

type Relevant struct {
	PathSuffix string `json:"pathSuffix,omitempty"`
	Contains   string `json:"contains,omitempty"`
	Section    string `json:"section,omitempty"`
}

type Case struct {
	ID         string     `json:"id"`
	Query      string     `json:"query"`
	Provenance string     `json:"provenance"`
	Relevant   []Relevant `json:"relevant"`
}

type QueryFunc func(context.Context, string, rag.QueryOptions) (rag.QueryResult, error)

type Options struct {
	TopK                         int
	Modes                        []string
	Dataset                      string
	EmbeddingUSDPerMillionTokens *float64
	RerankUSDPerMillionTokens    *float64
}

type CaseResult struct {
	ID             string         `json:"id"`
	Query          string         `json:"query"`
	Recall         float64        `json:"recallAtK"`
	ReciprocalRank float64        `json:"reciprocalRank"`
	LatencyMs      float64        `json:"latencyMs"`
	Method         string         `json:"method"`
	Degraded       string         `json:"degraded,omitempty"`
	Error          string         `json:"error,omitempty"`
	Hits           []rag.Hit      `json:"hits"`
	Usage          rag.QueryUsage `json:"usage"`
}

type Summary struct {
	Mode             string         `json:"mode"`
	Cases            int            `json:"cases"`
	Successful       int            `json:"successful"`
	Failed           int            `json:"failed"`
	Degraded         int            `json:"degraded"`
	RecallAtK        float64        `json:"recallAtK"`
	MRR              float64        `json:"mrr"`
	P50LatencyMs     float64        `json:"p50LatencyMs"`
	P95LatencyMs     float64        `json:"p95LatencyMs"`
	Usage            rag.QueryUsage `json:"usage"`
	EstimatedCostUSD *float64       `json:"estimatedCostUsd"`
	Results          []CaseResult   `json:"results"`
}

// Environment identifies what produced a report, so a score change between
// two reports can be traced to the binary, configuration, documents or labels.
type Environment struct {
	Version  string     `json:"version"`
	Config   rag.Config `json:"config"`
	ActiveDB string     `json:"activeDb"`
	// Documents maps each document id to its version, which changes whenever
	// the document's indexed content does.
	Documents     map[string]string `json:"documents"`
	DatasetSHA256 string            `json:"datasetSha256"`
}

type Report struct {
	CreatedAt    string       `json:"createdAt"`
	Dataset      string       `json:"dataset"`
	Environment  *Environment `json:"environment,omitempty"`
	Provenance   []string     `json:"provenance"`
	TopK         int          `json:"topK"`
	MetricPolicy string       `json:"metricPolicy"`
	CostPolicy   string       `json:"costPolicy"`
	Summaries    []Summary    `json:"summaries"`
}

func ReadCases(r io.Reader) ([]Case, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	cases := []Case{}
	seen := map[string]bool{}
	line := 0
	for scanner.Scan() {
		line++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var c Case
		dec := json.NewDecoder(strings.NewReader(scanner.Text()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return nil, fmt.Errorf("dataset line %d: %w", line, err)
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			return nil, fmt.Errorf("dataset line %d: trailing JSON", line)
		}
		if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Query) == "" || len(c.Relevant) == 0 || seen[c.ID] {
			return nil, fmt.Errorf("dataset line %d: require unique id, query and relevance labels", line)
		}
		for _, gold := range c.Relevant {
			if gold.PathSuffix == "" && gold.Contains == "" && gold.Section == "" {
				return nil, fmt.Errorf("dataset line %d: empty relevance label", line)
			}
		}
		if c.Provenance == "" {
			c.Provenance = "unspecified"
		}
		seen[c.ID] = true
		cases = append(cases, c)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, errors.New("evaluation dataset is empty")
	}
	return cases, nil
}

func matches(hit rag.Hit, gold Relevant) bool {
	if gold.PathSuffix != "" {
		path, suffix := filepath.ToSlash(filepath.Clean(hit.Chunk.Path)), filepath.ToSlash(filepath.Clean(gold.PathSuffix))
		if path != suffix && !strings.HasSuffix(path, "/"+strings.TrimPrefix(suffix, "/")) {
			return false
		}
	}
	if gold.Contains != "" && !strings.Contains(hit.Chunk.Content, gold.Contains) {
		return false
	}
	if gold.Section != "" && (hit.Chunk.Section == nil || *hit.Chunk.Section != gold.Section) {
		return false
	}
	return true
}

func score(hits []rag.Hit, golds []Relevant, k int) (float64, float64) {
	found := map[int]bool{}
	rr := 0.0
	for rank, hit := range hits[:min(k, len(hits))] {
		for i, gold := range golds {
			if matches(hit, gold) {
				found[i] = true
				if rr == 0 {
					rr = 1 / float64(rank+1)
				}
			}
		}
	}
	return float64(len(found)) / float64(len(golds)), rr
}

func addUsage(to *rag.QueryUsage, from rag.QueryUsage) {
	to.EmbeddingCalls += from.EmbeddingCalls
	to.RerankCalls += from.RerankCalls
	to.EstimatedEmbeddingTokens += from.EstimatedEmbeddingTokens
	to.EstimatedRerankTokens += from.EstimatedRerankTokens
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	return values[max(0, int(math.Ceil(p*float64(len(values))))-1)]
}

func cost(usage rag.QueryUsage, opts Options) *float64 {
	if usage.EmbeddingCalls > 0 && opts.EmbeddingUSDPerMillionTokens == nil {
		return nil
	}
	if usage.RerankCalls > 0 && opts.RerankUSDPerMillionTokens == nil {
		return nil
	}
	v := 0.0
	if opts.EmbeddingUSDPerMillionTokens != nil {
		v += float64(usage.EstimatedEmbeddingTokens) * *opts.EmbeddingUSDPerMillionTokens / 1e6
	}
	if opts.RerankUSDPerMillionTokens != nil {
		v += float64(usage.EstimatedRerankTokens) * *opts.RerankUSDPerMillionTokens / 1e6
	}
	return &v
}

func Run(ctx context.Context, cases []Case, query QueryFunc, opts Options) (Report, error) {
	report := Report{CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Dataset: opts.Dataset, TopK: opts.TopK,
		MetricPolicy: "Recall and MRR include failed queries as zero; latency measures the query call at the evaluator; degraded calls are counted explicitly.",
		CostPolicy:   "Optional estimate from supplied USD rates and heuristic token counts; logical calls exclude HTTP retries; this is not provider billing.", Summaries: []Summary{}}
	if len(cases) == 0 || query == nil || opts.TopK < 1 || opts.TopK > 200 || len(opts.Modes) == 0 {
		return report, errors.New("require cases, modes and topK in [1,200]")
	}
	for _, rate := range []*float64{opts.EmbeddingUSDPerMillionTokens, opts.RerankUSDPerMillionTokens} {
		if rate != nil && (*rate < 0 || math.IsNaN(*rate) || math.IsInf(*rate, 0)) {
			return report, errors.New("cost rates must be finite and nonnegative")
		}
	}
	seen := map[string]bool{}
	for _, mode := range opts.Modes {
		if seen[mode] || (mode != "bm25" && mode != "vector" && mode != "hybrid" && mode != "rerank") {
			return report, fmt.Errorf("invalid or duplicate mode %q", mode)
		}
		seen[mode] = true
	}
	provenance := map[string]bool{}
	for _, c := range cases {
		if len(c.Relevant) == 0 {
			return report, errors.New("missing relevance labels")
		}
		provenance[c.Provenance] = true
	}
	for p := range provenance {
		report.Provenance = append(report.Provenance, p)
	}
	sort.Strings(report.Provenance)
	for _, mode := range opts.Modes {
		summary := Summary{Mode: mode, Cases: len(cases), Results: []CaseResult{}}
		latencies := []float64{}
		for _, c := range cases {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			options := rag.QueryOptions{TopK: opts.TopK, CandidateTopK: max(30, opts.TopK), Mode: mode, DisableRerank: mode != "rerank"}
			if mode == "rerank" {
				options.Mode = "hybrid"
				options.RequireRerank = true
			}
			started := time.Now()
			result, err := query(ctx, c.Query, options)
			r := CaseResult{ID: c.ID, Query: c.Query, LatencyMs: float64(time.Since(started).Microseconds()) / 1000, Method: result.Method, Degraded: result.Degraded, Hits: result.Hits, Usage: result.Usage}
			if err != nil {
				r.Error = err.Error()
				summary.Failed++
			} else {
				r.Recall, r.ReciprocalRank = score(result.Hits, c.Relevant, opts.TopK)
				summary.Successful++
			}
			if result.Degraded != "" {
				summary.Degraded++
			}
			summary.RecallAtK += r.Recall
			summary.MRR += r.ReciprocalRank
			addUsage(&summary.Usage, result.Usage)
			latencies = append(latencies, r.LatencyMs)
			summary.Results = append(summary.Results, r)
		}
		summary.RecallAtK /= float64(len(cases))
		summary.MRR /= float64(len(cases))
		summary.P50LatencyMs = percentile(latencies, .5)
		summary.P95LatencyMs = percentile(latencies, .95)
		summary.EstimatedCostUSD = cost(summary.Usage, opts)
		report.Summaries = append(report.Summaries, summary)
	}
	return report, nil
}
