package rag

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Ownera1/rag-go/internal/chunk"
	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/searchtext"
	"github.com/Ownera1/rag-go/internal/store"
)

// quotedQuery matches any term so natural-language questions still recall;
// BM25 ranks chunks containing more and rarer terms first.
func quotedQuery(query string) string {
	terms := strings.Fields(query)
	out := make([]string, len(terms))
	for i, t := range terms {
		out[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(out, " OR ")
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// pathHas reports whether a word of path below root starts with term, so "to"
// no longer boosts "history" and the absolute prefix shared by every document
// boosts nothing.
func pathHas(root, path, term string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	for _, w := range words(rel) {
		if strings.HasPrefix(w, term) {
			return true
		}
	}
	return false
}

// minMax rescales one retriever's scores to [0,1] over its own results, so
// a weighted sum compares how strongly each retriever prefers a passage.
// Reciprocal rank fusion credited ranks alone: BM25, which matches any query
// term, ranks noise as highly as the vector head, and on real papers the fused
// ranking fell below vector search alone (Bruch et al., 2023, compare both).
func minMax(scores map[int64]float64) map[int64]float64 {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range scores {
		lo, hi = min(lo, v), max(hi, v)
	}
	out := make(map[int64]float64, len(scores))
	for id, v := range scores {
		out[id] = 1
		if hi > lo {
			out[id] = (v - lo) / (hi - lo)
		}
	}
	return out
}

func transient(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "model http 429") ||
		strings.Contains(s, "model http 5") ||
		strings.Contains(s, "timeout") ||
		strings.Contains(s, "connection") ||
		strings.Contains(s, "network") ||
		strings.Contains(s, "no such host")
}

func (c *session) query(ctx context.Context, query string, opts QueryOptions, plan queryPlan) (out QueryResult, err error) {
	started := time.Now()
	defer func() { out.ElapsedMs = float64(time.Since(started).Microseconds()) / 1000 }()
	out = QueryResult{Query: query, Documents: map[string]HitDocument{}, Hits: []model.Hit{}, Method: plan.mode}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	topK, candidate, alpha, mode := plan.topK, plan.candidate, plan.alpha, plan.mode
	reranker := c.reranker
	if opts.DisableRerank {
		reranker = nil
	}
	filtered, syncedAt, err := c.queryMetadata(ctx, opts.Filter)
	if err != nil {
		return out, err
	}
	out.MetadataSyncedAt = syncedAt
	if c.db == nil {
		return out, nil
	}
	if ok, err := c.db.HasChunks(ctx); err != nil || !ok {
		return out, err
	}
	if opts.Document != "" {
		d, err := c.resolveDocument(ctx, opts.Document)
		if err != nil {
			return out, err
		}
		if err = c.db.RestrictDocument(ctx, d.info.Path, filtered); err != nil {
			return out, err
		}
		filtered = true
	}
	if mode == "literal" {
		ids, err := c.db.Literal(ctx, query, topK, filtered)
		if err != nil {
			return out, err
		}
		chunks, err := c.db.Chunks(ctx, ids)
		if err != nil {
			return out, err
		}
		for _, id := range ids {
			out.Hits = append(out.Hits, model.Hit{Chunk: chunks[id]})
		}
		return out, c.describeHits(ctx, &out)
	}
	recall := topK
	if reranker != nil {
		recall = candidate
	}
	fts := []store.Match{}
	bigrams := ""
	if mode != "vector" {
		// The Han index also holds the original text, so it serves Latin
		// terms of a mixed query too.
		hanQuery, pairs, hasHan := searchtext.Query(query)
		if hasHan {
			bigrams = pairs
			fts, err = c.db.FTSHan(ctx, hanQuery, 200, filtered)
		} else {
			fts, err = c.db.FTS(ctx, quotedQuery(query), 200, filtered)
		}
		if err != nil {
			return out, err
		}
	}
	var e error
	vec := []store.Match{}
	if mode != "bm25" {
		if c.embedder == nil {
			return out, errors.New("embedding provider unavailable")
		}
		out.Usage.EmbeddingCalls++
		out.Usage.EstimatedEmbeddingTokens += chunk.Estimate(query)
		vector, err := c.embedder.EmbedQuery(ctx, query)
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			if mode == "vector" || !transient(err) {
				return out, err
			}
			out.Degraded = "query embedding failed, BM25 only: " + err.Error()
			out.Method = "bm25-fallback"
		} else {
			vec, e = c.db.Vectors(ctx, vector, min(200, max(recall*10, 100)), filtered)
			if e != nil {
				return out, e
			}
		}
	} else {
		out.Method = "bm25"
	}
	// A Chinese question about English papers matches none of its bigrams,
	// and BM25 then ranks chunks by stray Latin terms such as "LoS". Without
	// a reranker to undo that, rank by the vector list alone.
	if reranker == nil && mode == "hybrid" && len(vec) > 0 && bigrams != "" && len(fts) > 0 {
		matched, err := c.db.FTSHan(ctx, bigrams, 1, filtered)
		if err != nil {
			return out, err
		}
		if len(matched) == 0 {
			fts = nil
		}
	}
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
	first := ""
	for _, term := range words(query) {
		if len([]rune(term)) > 1 {
			first = term
			break
		}
	}
	// Hits keep the raw relevance values; only the fused score is normalized.
	bm := map[int64]float64{}
	for _, x := range fts {
		ch, ok := chunks[x.RowID]
		if !ok {
			continue
		}
		b := -x.Score // FTS5 bm25() is negative; more negative is better.
		if first != "" && pathHas(c.docs, ch.Path, first) {
			b *= 1.5
		}
		bm[x.RowID] = b
	}
	cosine := map[int64]float64{}
	for _, x := range vec {
		cosine[x.RowID] = 1 - x.Score*x.Score/2 // L2 distance of unit vectors.
	}
	normBM, normVec := minMax(bm), minMax(cosine)
	bmWeight, vecWeight := alpha, 1-alpha
	if len(vec) == 0 {
		bmWeight = 1
	}
	if mode == "vector" {
		bmWeight, vecWeight = 0, 1
	}
	for _, id := range ids {
		ch, ok := chunks[id]
		if !ok {
			continue
		}
		score := bmWeight*normBM[id] + vecWeight*normVec[id]
		hits = append(hits, model.Hit{Chunk: ch, BM25: bm[id], Vector: cosine[id], Hybrid: score})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Hybrid > hits[j].Hybrid })
	if len(hits) > recall {
		hits = hits[:recall]
	}
	if reranker != nil && len(hits) > 0 {
		entries, err := c.documentInfos(ctx)
		if err != nil {
			return out, err
		}
		titles := map[string]string{}
		for _, d := range entries {
			titles[d.info.Path] = d.info.Title
		}
		// Prefix "title > section" as indexing did for the embedding, so the
		// reranker can tell which paper a shared section such as Complexity
		// Analysis belongs to. MinerU sections already start with the paper
		// title; the embedding keeps that repeat, which helped vector recall,
		// but the reranker reads the title once.
		docs := make([]model.RerankDoc, len(hits))
		for i, h := range hits {
			text, title := h.Chunk.Content, titles[h.Chunk.Path]
			if top, _, _ := strings.Cut(deref(h.Chunk.Section), " / "); strings.EqualFold(top, title) {
				title = ""
			}
			if head := heading(title, h.Chunk.Section); head != "" {
				text = head + "\n\n" + text
			}
			docs[i] = model.RerankDoc{ID: h.Chunk.ID, Text: text}
		}
		out.Usage.RerankCalls++
		out.Usage.EstimatedRerankTokens += chunk.Estimate(query)
		for _, doc := range docs {
			out.Usage.EstimatedRerankTokens += chunk.Estimate(doc.Text)
		}
		ranked, err := reranker.Rerank(ctx, query, docs, topK)
		if err != nil {
			if e := ctx.Err(); e != nil {
				return out, e
			}
			if opts.RequireRerank {
				return out, fmt.Errorf("rerank failed: %w", err)
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
	if err = c.describeHits(ctx, &out); err != nil {
		return out, err
	}
	return out, nil
}

// queryPlan holds validated query parameters with configuration defaults applied.
type queryPlan struct {
	topK, candidate int
	alpha           float64
	mode            string
}

func validateQuery(cfg Config, text string, opts QueryOptions, hasReranker bool) (queryPlan, error) {
	p := queryPlan{topK: opts.TopK, candidate: opts.CandidateTopK, alpha: cfg.Alpha, mode: opts.Mode}
	if err := opts.Filter.Validate(); err != nil {
		return p, err
	}
	if strings.TrimSpace(text) == "" {
		return p, errors.New("query is required")
	}
	if p.topK == 0 {
		p.topK = cfg.TopK
	}
	if p.candidate == 0 {
		p.candidate = cfg.CandidateTopK
	}
	if p.topK < 1 || p.topK > 200 || p.candidate < p.topK || p.candidate > 200 {
		return p, errors.New("invalid top_k/candidate_top_k")
	}
	if opts.Alpha != nil {
		p.alpha = *opts.Alpha
	}
	if math.IsNaN(p.alpha) || p.alpha < 0 || p.alpha > 1 {
		return p, errors.New("alpha must be in [0,1]")
	}
	if p.mode == "" {
		p.mode = "hybrid"
	}
	if p.mode != "bm25" && p.mode != "vector" && p.mode != "hybrid" && p.mode != "literal" {
		return p, errors.New("mode must be hybrid, vector, bm25 or literal")
	}
	if opts.RequireRerank && (!hasReranker || opts.DisableRerank || p.mode == "literal") {
		return p, errors.New("reranker required but unavailable")
	}
	return p, nil
}
