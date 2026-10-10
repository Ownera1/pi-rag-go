package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"regexp"
)

func DefaultConfig() Config {
	return Config{
		Embedding: ProviderConfig{
			Type:       "voyage",
			Model:      "voyage-4-lite",
			Dimensions: 1024,
			BaseURL:    "https://api.voyageai.com/v1",
			APIKeyEnv:  "VOYAGE_API_KEY",
		},
		Reranker:        ProviderConfig{Type: "none", Model: "none"},
		Chunking:        DefaultChunking(),
		Indexing:        IndexingConfig{Workers: 32, SemanticWorkers: 2, EmbeddingWorkers: 4, EmbeddingBatchSize: 64},
		Documents:       "documents",
		ExcludePatterns: []string{},
		Alpha:           0.3,
		CandidateTopK:   30,
		TopK:            5,
		HTTPTimeoutMs:   30000,
		HTTPMaxRetries:  3,
	}
}

func DefaultChunking() ChunkingConfig {
	return ChunkingConfig{Mode: "semantic", LegacyTarget: 180, LegacyMax: 240, LegacyOverlap: 30,
		SemanticMin: 120, SemanticTarget: 280, SemanticMax: 420, SemanticUnitMax: 140}
}

func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	if path == "" {
		return c, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(b, &fields); err != nil {
		return c, err
	}
	if fields == nil {
		return c, errors.New("configuration must be a JSON object")
	}
	if _, ok := fields["embedding"]; ok {
		c.Embedding = ProviderConfig{}
	}
	if _, ok := fields["reranker"]; ok {
		c.Reranker = ProviderConfig{}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&c); err != nil {
		return c, err
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return c, errors.New("trailing configuration JSON")
	}
	if c.Embedding.Type == "voyage" && c.Embedding.BaseURL == "" {
		c.Embedding.BaseURL = "https://api.voyageai.com/v1"
	}
	if c.Embedding.Type == "voyage" && c.Embedding.APIKeyEnv == "" && c.Embedding.BaseURL == "https://api.voyageai.com/v1" {
		c.Embedding.APIKeyEnv = "VOYAGE_API_KEY"
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if c.Zotero != nil {
		if err := c.Zotero.Validate(); err != nil {
			return err
		}
	}
	if c.Documents == "" {
		return errors.New("documents directory is required")
	}
	if c.Embedding.Type != "voyage" && c.Embedding.Type != "openai" {
		return fmt.Errorf("unsupported embedding type %q", c.Embedding.Type)
	}
	if c.Embedding.Model == "" {
		return errors.New("embedding.model is required")
	}
	if c.Reranker.Type != "none" && c.Reranker.Type != "voyage" && c.Reranker.Type != "http" {
		return fmt.Errorf("unsupported reranker type %q", c.Reranker.Type)
	}
	if c.Reranker.Type != "none" && c.Reranker.Model == "" {
		return errors.New("reranker.model is required unless reranker.type is none")
	}
	for _, p := range []struct {
		name string
		ProviderConfig
	}{{"embedding", c.Embedding}, {"reranker", c.Reranker}} {
		if u, err := url.Parse(p.BaseURL); p.BaseURL != "" && (err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "") {
			return fmt.Errorf("%s.baseUrl must be an http(s) URL", p.name)
		}
		// Catches a key pasted in place of the variable that holds it.
		if p.APIKeyEnv != "" && !EnvName.MatchString(p.APIKeyEnv) {
			return fmt.Errorf("%s.apiKeyEnv must be an environment variable name, not the key itself", p.name)
		}
	}
	if c.Chunking.Mode != "semantic" && c.Chunking.Mode != "legacy" {
		return errors.New("chunking mode must be semantic or legacy")
	}
	if !(c.Alpha >= 0 && c.Alpha <= 1) { // also rejects NaN
		return errors.New("alpha must be between 0 and 1")
	}
	b, n := c.Chunking, c.Indexing
	for _, l := range []limit{
		{"embedding.dimensions", c.Embedding.Dimensions, 1, 4096, "", ""},
		{"chunking.legacyTarget", b.LegacyTarget, 1, math.MaxInt, "", ""},
		{"chunking.legacyMax", b.LegacyMax, b.LegacyTarget, 8192, "chunking.legacyTarget", ""},
		{"chunking.legacyOverlap", b.LegacyOverlap, 0, b.LegacyTarget - 1, "", "chunking.legacyTarget - 1"},
		{"chunking.semanticMin", b.SemanticMin, 1, math.MaxInt, "", ""},
		{"chunking.semanticTarget", b.SemanticTarget, b.SemanticMin, math.MaxInt, "chunking.semanticMin", ""},
		{"chunking.semanticMax", b.SemanticMax, b.SemanticTarget, 8192, "chunking.semanticTarget", ""},
		{"chunking.semanticUnitMax", b.SemanticUnitMax, 1, b.SemanticMax, "", "chunking.semanticMax"},
		{"indexing.workers", n.Workers, 1, 64, "", ""},
		{"indexing.semanticWorkers", n.SemanticWorkers, 1, n.Workers, "", "indexing.workers"},
		{"indexing.embeddingWorkers", n.EmbeddingWorkers, 1, 64, "", ""},
		{"indexing.embeddingBatchSize", n.EmbeddingBatchSize, 1, 256, "", ""},
		{"topK", c.TopK, 1, math.MaxInt, "", ""},
		{"candidateTopK", c.CandidateTopK, c.TopK, 200, "topK", ""},
		{"httpTimeoutMs", c.HTTPTimeoutMs, 1000, 600000, "", ""},
		{"httpMaxRetries", c.HTTPMaxRetries, 0, 8, "", ""},
	} {
		if err := l.check(); err != nil {
			return err
		}
	}
	return nil
}

// EnvName matches an environment variable name.
var EnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// limit bounds an integer setting to [lo, hi]. loName or hiName names the
// setting a bound comes from, so the error says which value to change.
type limit struct {
	name           string
	v, lo, hi      int
	loName, hiName string
}

func (l limit) check() error {
	side, bound, by := "least", l.lo, l.loName
	if l.v >= l.lo {
		if l.v <= l.hi {
			return nil
		}
		side, bound, by = "most", l.hi, l.hiName
	}
	if by != "" {
		return fmt.Errorf("%s (%d) must be at %s %s (%d)", l.name, l.v, side, by, bound)
	}
	return fmt.Errorf("%s (%d) must be at %s %d", l.name, l.v, side, bound)
}
