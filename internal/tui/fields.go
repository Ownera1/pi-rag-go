package tui

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Ownera1/rag-go/internal/model"
)

// impact says when a saved change takes effect.
type impact int

const (
	immediate impact = iota // the next query or operation reads it
	onSync                  // the next sync uses it
	onZotero                // the next Zotero synchronization uses it
	onRebuild               // changes the index fingerprint or documents root
)

var impactLabel = [...]string{"即时", "sync", "zotero", "rebuild"}

// field is one editable configuration value. Fields with options cycle with
// ←/→ and enter, fields with a step adjust numerically, and every other field
// accepts typed input.
type field struct {
	group, name string
	impact      impact
	options     []string
	step        float64
	get         func(model.Config) string
	set         func(*model.Config, string) error
}

func (f field) adjust(c *model.Config, dir int) error {
	cur := f.get(*c)
	if len(f.options) > 0 {
		i := 0
		for j, o := range f.options {
			if o == cur {
				i = j
			}
		}
		return f.set(c, f.options[(i+dir+len(f.options))%len(f.options)])
	}
	if f.step == 0 {
		return nil
	}
	v, err := strconv.ParseFloat(cur, 64)
	if err != nil {
		return err
	}
	return f.set(c, strconv.FormatFloat(math.Round((v+float64(dir)*f.step)*100)/100, 'f', -1, 64))
}

func text(group, name string, im impact, p func(*model.Config) *string, options ...string) field {
	return field{group: group, name: name, impact: im, options: options,
		get: func(c model.Config) string { return *p(&c) },
		set: func(c *model.Config, s string) error { *p(c) = strings.TrimSpace(s); return nil }}
}

func integer(group, name string, im impact, step int, p func(*model.Config) *int) field {
	return field{group: group, name: name, impact: im, step: float64(step),
		get: func(c model.Config) string { return strconv.Itoa(*p(&c)) },
		set: func(c *model.Config, s string) error {
			v, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				return fmt.Errorf("%s must be an integer", name)
			}
			*p(c) = v
			return nil
		}}
}

// zotero returns the Zotero settings, materializing the defaults that a
// missing block implies. get works on a copy, so reading never adds the block.
func zotero(c *model.Config) *model.ZoteroConfig {
	if c.Zotero == nil {
		z := model.ZoteroConfig{}.Defaults()
		c.Zotero = &z
	}
	return c.Zotero
}

func fields() []field {
	const (
		retrieval = "检索默认值"
		rerank    = "Reranker"
		embed     = "嵌入模型"
		chunking  = "分块"
		indexing  = "增量嵌入 / 并发"
		docs      = "文档"
		http      = "HTTP"
		zot       = "Zotero"
	)
	return []field{
		integer(retrieval, "topK", immediate, 1, func(c *model.Config) *int { return &c.TopK }),
		integer(retrieval, "candidateTopK", immediate, 5, func(c *model.Config) *int { return &c.CandidateTopK }),
		{group: retrieval, name: "alpha (BM25 权重)", impact: immediate, step: 0.05,
			get: func(c model.Config) string { return strconv.FormatFloat(c.Alpha, 'f', 2, 64) },
			set: func(c *model.Config, s string) error {
				v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
				if err != nil {
					return errors.New("alpha must be a number")
				}
				c.Alpha = v
				return nil
			}},
		text(rerank, "reranker.type", immediate, func(c *model.Config) *string { return &c.Reranker.Type }, "none", "voyage", "http"),
		text(rerank, "reranker.model", immediate, func(c *model.Config) *string { return &c.Reranker.Model }),
		text(rerank, "reranker.baseUrl", immediate, func(c *model.Config) *string { return &c.Reranker.BaseURL }),
		text(rerank, "reranker.apiKeyEnv", immediate, func(c *model.Config) *string { return &c.Reranker.APIKeyEnv }),
		text(embed, "embedding.type", onRebuild, func(c *model.Config) *string { return &c.Embedding.Type }, "voyage", "openai"),
		text(embed, "embedding.model", onRebuild, func(c *model.Config) *string { return &c.Embedding.Model }),
		integer(embed, "embedding.dimensions", onRebuild, 0, func(c *model.Config) *int { return &c.Embedding.Dimensions }),
		text(embed, "embedding.baseUrl", onRebuild, func(c *model.Config) *string { return &c.Embedding.BaseURL }),
		text(embed, "embedding.apiKeyEnv", immediate, func(c *model.Config) *string { return &c.Embedding.APIKeyEnv }),
		text(chunking, "chunking.mode", onRebuild, func(c *model.Config) *string { return &c.Chunking.Mode }, "semantic", "legacy"),
		integer(chunking, "chunking.semanticMin", onRebuild, 10, func(c *model.Config) *int { return &c.Chunking.SemanticMin }),
		integer(chunking, "chunking.semanticTarget", onRebuild, 10, func(c *model.Config) *int { return &c.Chunking.SemanticTarget }),
		integer(chunking, "chunking.semanticMax", onRebuild, 10, func(c *model.Config) *int { return &c.Chunking.SemanticMax }),
		integer(chunking, "chunking.semanticUnitMax", onRebuild, 10, func(c *model.Config) *int { return &c.Chunking.SemanticUnitMax }),
		integer(chunking, "chunking.legacyTarget", onRebuild, 10, func(c *model.Config) *int { return &c.Chunking.LegacyTarget }),
		integer(chunking, "chunking.legacyMax", onRebuild, 10, func(c *model.Config) *int { return &c.Chunking.LegacyMax }),
		integer(chunking, "chunking.legacyOverlap", onRebuild, 5, func(c *model.Config) *int { return &c.Chunking.LegacyOverlap }),
		integer(indexing, "indexing.workers", onSync, 1, func(c *model.Config) *int { return &c.Indexing.Workers }),
		integer(indexing, "indexing.semanticWorkers", onSync, 1, func(c *model.Config) *int { return &c.Indexing.SemanticWorkers }),
		integer(indexing, "indexing.embeddingWorkers", onSync, 1, func(c *model.Config) *int { return &c.Indexing.EmbeddingWorkers }),
		integer(indexing, "indexing.embeddingBatchSize", onSync, 8, func(c *model.Config) *int { return &c.Indexing.EmbeddingBatchSize }),
		text(docs, "documents", onRebuild, func(c *model.Config) *string { return &c.Documents }),
		// ponytail: comma-separated, so a pattern cannot contain a comma.
		{group: docs, name: "excludePatterns", impact: onSync,
			get: func(c model.Config) string { return strings.Join(c.ExcludePatterns, ", ") },
			set: func(c *model.Config, s string) error {
				c.ExcludePatterns = []string{}
				for _, p := range strings.Split(s, ",") {
					if p = strings.TrimSpace(p); p != "" {
						c.ExcludePatterns = append(c.ExcludePatterns, p)
					}
				}
				return nil
			}},
		integer(http, "httpTimeoutMs", immediate, 1000, func(c *model.Config) *int { return &c.HTTPTimeoutMs }),
		integer(http, "httpMaxRetries", immediate, 1, func(c *model.Config) *int { return &c.HTTPMaxRetries }),
		text(zot, "zotero.baseUrl", onZotero, func(c *model.Config) *string { return &zotero(c).BaseURL }),
		text(zot, "zotero.libraryType", onZotero, func(c *model.Config) *string { return &zotero(c).LibraryType }, "user", "group"),
		text(zot, "zotero.libraryId", onZotero, func(c *model.Config) *string { return &zotero(c).LibraryID }),
		{group: zot, name: "zotero.startOnDemand", impact: onZotero, options: []string{"false", "true"},
			get: func(c model.Config) string { return strconv.FormatBool(zotero(&c).StartOnDemand) },
			set: func(c *model.Config, s string) error {
				v, err := strconv.ParseBool(strings.TrimSpace(s))
				if err == nil {
					zotero(c).StartOnDemand = v
				}
				return err
			}},
	}
}
