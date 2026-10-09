package evaluate

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/pkg/rag"
)

func TestMetricsIncludeFailuresAndCountEachLabelOnce(t *testing.T) {
	cases := []Case{
		{ID: "rank", Query: "rank", Relevant: []Relevant{{Contains: "gold"}, {Contains: "missing"}}},
		{ID: "failure", Query: "failure", Relevant: []Relevant{{Contains: "gold"}}},
	}
	rate := 1.0
	query := func(_ context.Context, text string, opts rag.QueryOptions) (rag.QueryResult, error) {
		if opts.Mode != "hybrid" || !opts.DisableRerank {
			t.Fatalf("baseline should disable reranking: %+v", opts)
		}
		if text == "failure" {
			return rag.QueryResult{}, errors.New("model unavailable")
		}
		return rag.QueryResult{Method: "bm25-fallback", Degraded: "timeout", Usage: rag.QueryUsage{EmbeddingCalls: 1, EstimatedEmbeddingTokens: 10}, Hits: []rag.Hit{
			{Chunk: model.Chunk{Content: "wrong"}},
			{Chunk: model.Chunk{Content: "gold"}},
			{Chunk: model.Chunk{Content: "gold duplicate"}},
		}}, nil
	}
	r, err := Run(context.Background(), cases, query, Options{TopK: 3, Modes: []string{"hybrid"}, EmbeddingUSDPerMillionTokens: &rate})
	if err != nil {
		t.Fatal(err)
	}
	s := r.Summaries[0]
	if s.RecallAtK != .25 || s.MRR != .25 || s.Failed != 1 || s.Successful != 1 || s.Degraded != 1 || s.Usage.EmbeddingCalls != 1 {
		t.Fatalf("incorrect aggregate: %+v", s)
	}
	if s.EstimatedCostUSD == nil || math.Abs(*s.EstimatedCostUSD-.00001) > 1e-10 {
		t.Fatalf("incorrect cost: %v", s.EstimatedCostUSD)
	}
}

func TestRerankReportsCandidateRecall(t *testing.T) {
	cases := []Case{
		{ID: "late", Query: "late", Relevant: []Relevant{{Contains: "gold"}}},
		{ID: "failure", Query: "failure", Relevant: []Relevant{{Contains: "gold"}}},
	}
	query := func(_ context.Context, text string, opts rag.QueryOptions) (rag.QueryResult, error) {
		if opts.TopK != 30 || opts.CandidateTopK != 30 {
			t.Fatalf("rerank should keep every candidate: %+v", opts)
		}
		if text == "failure" {
			return rag.QueryResult{}, errors.New("rerank failed")
		}
		return rag.QueryResult{Hits: []rag.Hit{{Chunk: model.Chunk{Content: "wrong"}}, {Chunk: model.Chunk{Content: "gold"}}}}, nil
	}
	r, err := Run(context.Background(), cases, query, Options{TopK: 1, Modes: []string{"rerank"}})
	if err != nil {
		t.Fatal(err)
	}
	s := r.Summaries[0]
	if s.RecallAtK != 0 || s.CandidateRecall == nil || *s.CandidateRecall != .5 || r.CandidateTopK != 30 {
		t.Fatalf("candidate recall: %+v", s)
	}
	if late := s.Results[0]; len(late.Hits) != 1 || *late.CandidateRecall != 1 || *s.Results[1].CandidateRecall != 0 {
		t.Fatalf("case results: %+v", s.Results)
	}
	base, err := Run(context.Background(), cases[:1], func(context.Context, string, rag.QueryOptions) (rag.QueryResult, error) {
		return rag.QueryResult{}, nil
	}, Options{TopK: 1, Modes: []string{"hybrid"}})
	if err != nil || base.Summaries[0].CandidateRecall != nil || base.Summaries[0].Results[0].CandidateRecall != nil {
		t.Fatalf("baseline should omit candidate recall: %+v %v", base, err)
	}
}

func TestDatasetRejectsAmbiguousInput(t *testing.T) {
	valid := `{"id":"1","query":"q","relevant":[{"contains":"evidence"}]}`
	for _, input := range []string{"", valid + "\n" + valid, valid + `{}`, `{"id":"1","query":"q","relevant":[{}]}`, `{"id":"1","query":"q","relevant":[{"pathSuffix":"p","anyOf":[{"contains":"x"},{}]}]}`, `{"id":"1","query":"q","relevant":[{"contains":"x"}],"extra":true}`} {
		if _, err := ReadCases(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted invalid dataset: %s", input)
		}
	}
	if cases, err := ReadCases(strings.NewReader(valid)); err != nil || len(cases) != 1 || cases[0].Provenance != "unspecified" {
		t.Fatalf("read valid dataset: %+v %v", cases, err)
	}
}

func TestRerankModeAndValidation(t *testing.T) {
	cases := []Case{{ID: "one", Query: "q", Relevant: []Relevant{{Contains: "x"}}}}
	query := func(_ context.Context, _ string, opts rag.QueryOptions) (rag.QueryResult, error) {
		if opts.Mode != "hybrid" || !opts.RequireRerank || opts.DisableRerank {
			t.Fatalf("rerank options: %+v", opts)
		}
		return rag.QueryResult{Usage: rag.QueryUsage{EmbeddingCalls: 1}}, nil
	}
	r, err := Run(context.Background(), cases, query, Options{TopK: 1, Modes: []string{"rerank"}})
	if err != nil || r.Summaries[0].EstimatedCostUSD != nil {
		t.Fatalf("missing rate should yield unknown cost: %+v %v", r, err)
	}
	nan := math.NaN()
	for _, opts := range []Options{{TopK: 1, Modes: []string{"bm25", "bm25"}}, {TopK: 1, Modes: []string{"bad"}}, {TopK: 1, Modes: []string{"bm25"}, EmbeddingUSDPerMillionTokens: &nan}} {
		if _, err := Run(context.Background(), cases, query, opts); err == nil {
			t.Fatalf("accepted invalid options: %+v", opts)
		}
	}
	hit := rag.Hit{Chunk: model.Chunk{Path: "/tmp/notpaper.txt", Content: "x"}}
	if matches(hit, Relevant{PathSuffix: "paper.txt"}) {
		t.Fatal("path suffix matched part of filename")
	}
	if !matches(hit, Relevant{PathSuffix: "notpaper.txt", AnyOf: []Relevant{{Contains: "y"}, {Contains: "x"}}}) || matches(hit, Relevant{PathSuffix: "paper.txt", AnyOf: []Relevant{{Contains: "x"}}}) || matches(hit, Relevant{AnyOf: []Relevant{{Contains: "y"}}}) {
		t.Fatal("anyOf should need the label's selectors and one alternative")
	}
	if percentile([]float64{4, 1, 3, 2}, .5) != 2 || percentile([]float64{4, 1, 3, 2}, .95) != 4 {
		t.Fatal("incorrect percentile")
	}
}
