package rag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/Ownera1/pi-rag-go/internal/model"
	"github.com/Ownera1/pi-rag-go/internal/store"
)

func quotedQuery(query string) string {
	terms := strings.Fields(query)
	out := make([]string, len(terms))
	for i, t := range terms {
		out[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(out, " ")
}

func normalizeBM25(rows []store.Match) map[int64]float64 {
	out := map[int64]float64{}
	if len(rows) == 0 {
		return out
	}
	minV, maxV := rows[0].Score, rows[0].Score
	for _, x := range rows {
		minV = math.Min(minV, x.Score)
		maxV = math.Max(maxV, x.Score)
	}
	for _, x := range rows {
		if maxV == minV {
			out[x.RowID] = 1
		} else {
			out[x.RowID] = (maxV - x.Score) / (maxV - minV)
		}
	}
	return out
}

func normalizeVector(rows []store.Match) map[int64]float64 {
	out := map[int64]float64{}
	if len(rows) == 0 {
		return out
	}
	minV, maxV := math.Inf(1), math.Inf(-1)
	for _, x := range rows {
		v := 1 - x.Score*x.Score/2
		out[x.RowID] = v
		minV = math.Min(minV, v)
		maxV = math.Max(maxV, v)
	}
	for id, v := range out {
		if maxV == minV {
			out[id] = 1
		} else {
			out[id] = (v - minV) / (maxV - minV)
		}
	}
	return out
}

func transient(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "model http 429") ||
		strings.Contains(s, "model http 5") ||
		strings.Contains(s, "timeout") ||
		strings.Contains(s, "connection") ||
		strings.Contains(s, "network") ||
		strings.Contains(s, "no such host")
}

func (c *Core) Query(ctx context.Context, query string, opts QueryOptions) (QueryResult, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := QueryResult{Query: query, Hits: []model.Hit{}, Method: "hybrid"}
	if strings.TrimSpace(query) == "" {
		return out, errors.New("query is required")
	}
	if c.db == nil {
		return out, nil
	}
	if err := c.compatible(ctx, c.db); err != nil {
		return out, err
	}
	topK := opts.TopK
	if topK == 0 {
		topK = c.cfg.TopK
	}
	candidate := opts.CandidateTopK
	if candidate == 0 {
		candidate = c.cfg.CandidateTopK
	}
	if topK < 1 || topK > 200 || candidate < topK || candidate > 200 {
		return out, errors.New("invalid top_k/candidate_top_k")
	}
	alpha := c.cfg.Alpha
	if opts.Alpha != nil {
		alpha = *opts.Alpha
	}
	if alpha < 0 || alpha > 1 {
		return out, errors.New("alpha must be in [0,1]")
	}
	mode := opts.Mode
	if mode == "" {
		mode = "hybrid"
	}
	if mode != "hybrid" && mode != "bm25" {
		return out, errors.New("mode must be hybrid or bm25")
	}
	if c.legacy && mode == "hybrid" {
		raw := c.db.GetMetadata(ctx, "embedding_fingerprint")
		var fp struct {
			Provider         string `json:"provider"`
			Model            string `json:"model"`
			Dimensions       int    `json:"dimensions"`
			ProviderContract string `json:"providerContract"`
			Contract         string `json:"contract"`
		}
		if err := json.Unmarshal([]byte(raw), &fp); err != nil {
			return out, errors.New("legacy embedding fingerprint missing; use bm25 or rebuild")
		}
		if fp.Provider == "local" {
			return out, errors.New("legacy MiniLM vectors require the original model; use explicit bm25 mode")
		}
		if c.legacyProviderError != nil {
			return out, fmt.Errorf("legacy vector provider unavailable: %w; use bm25", c.legacyProviderError)
		}
		if fp.Provider != c.legacyProviderID ||
			fp.Model != c.cfg.Embedding.Model ||
			fp.Dimensions != c.cfg.Embedding.Dimensions ||
			fp.Contract != "l2-unit-v1" ||
			(fp.ProviderContract != "" && fp.ProviderContract != c.legacyContract) {
			return out, errors.New("legacy embedding contract does not match configured provider")
		}
	}
	recall := topK
	if c.reranker != nil {
		recall = candidate
	}
	ftsLimit := min(200, max(recall*20, 200))
	fts, e := c.db.FTS(ctx, quotedQuery(query), ftsLimit)
	if e != nil {
		return out, e
	}
	vec := []store.Match{}
	if mode == "hybrid" {
		if c.embedder == nil {
			return out, errors.New("embedding provider unavailable")
		}
		vector, err := c.embedder.EmbedQuery(ctx, query)
		if err != nil {
			if !transient(err) {
				return out, err
			}
			out.Degraded = "query embedding failed, BM25 only: " + err.Error()
			out.Method = "bm25-fallback"
		} else {
			vec, e = c.db.Vectors(ctx, vector, min(200, max(recall*10, 100)))
			if e != nil {
				return out, e
			}
		}
	} else {
		out.Method = "bm25"
		if c.legacy && strings.Contains(c.db.GetMetadata(ctx, "embedding_fingerprint"), `"provider":"local"`) {
			out.Degraded = "legacy local MiniLM vectors unavailable; explicit BM25 query"
		}
	}
	bm := normalizeBM25(fts)
	vnorm := normalizeVector(vec)
	ids := []int64{}
	seen := map[int64]bool{}
	for _, x := range fts {
		if !seen[x.RowID] {
			ids = append(ids, x.RowID)
			seen[x.RowID] = true
		}
	}
	for _, x := range vec {
		if !seen[x.RowID] {
			ids = append(ids, x.RowID)
			seen[x.RowID] = true
		}
	}
	chunks, e := c.db.Chunks(ctx, ids)
	if e != nil {
		return out, e
	}
	hits := []model.Hit{}
	terms := strings.Fields(strings.ToLower(query))
	first := ""
	for _, term := range terms {
		if len([]rune(term)) > 1 {
			first = term
			break
		}
	}
	for _, id := range ids {
		ch, ok := chunks[id]
		if !ok {
			continue
		}
		b := bm[id]
		if first != "" && strings.Contains(strings.ToLower(ch.Path), first) {
			b = math.Min(1, b*1.5)
		}
		v := vnorm[id]
		score := b
		if len(vec) > 0 {
			score = alpha*b + (1-alpha)*v
		}
		if score > 0 {
			hits = append(hits, model.Hit{Chunk: ch, BM25: b, Vector: v, Hybrid: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Hybrid > hits[j].Hybrid })
	if len(hits) > recall {
		hits = hits[:recall]
	}
	if c.reranker != nil && len(hits) > 0 {
		docs := make([]model.RerankDoc, len(hits))
		for i, h := range hits {
			docs[i] = model.RerankDoc{ID: h.Chunk.ID, Text: h.Chunk.Content}
		}
		ranked, err := c.reranker.Rerank(ctx, query, docs, topK)
		if err != nil {
			if e := ctx.Err(); e != nil {
				return out, e
			}
			if out.Degraded != "" {
				out.Degraded += "; "
			}
			out.Degraded += "rerank failed: " + err.Error()
			out.Method = "rerank-fallback"
		} else {
			byID := map[string]model.Hit{}
			for _, h := range hits {
				byID[h.Chunk.ID] = h
			}
			reranked := []model.Hit{}
			for _, r := range ranked {
				h, ok := byID[r.ID]
				if ok {
					score := r.Score
					h.Rerank = &score
					reranked = append(reranked, h)
				}
			}
			hits = reranked
			if out.Method == "hybrid" {
				out.Method = "rerank"
			}
		}
	}
	if len(hits) > topK {
		hits = hits[:topK]
	}
	out.Hits = hits
	return out, nil
}
