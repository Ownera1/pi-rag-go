package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
		Indexing:        IndexingConfig{Workers: 32, SemanticWorkers: 2, EmbeddingBatchSize: 64},
		TrackedPaths:    []string{},
		ExcludePatterns: []string{},
		Alpha:           0.4,
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
	if _, ok := fields["embedding"]; ok {
		c.Embedding = ProviderConfig{}
	}
	if _, ok := fields["reranker"]; ok {
		c.Reranker = ProviderConfig{}
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, err
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
	if c.Runtime.AutoRefresh.DebounceMs < 0 || c.Runtime.AutoRefresh.RescanMs < 0 || c.Runtime.PDF.TimeoutMs < 0 {
		return errors.New("runtime intervals cannot be negative")
	}
	if backend := c.Runtime.PDF.Backend; backend != "" && backend != "grobid" && backend != "pdftotext" && backend != "mineru" {
		return errors.New("invalid PDF backend")
	}
	if c.Embedding.Type != "voyage" && c.Embedding.Type != "openai" {
		return fmt.Errorf("unsupported embedding type %q", c.Embedding.Type)
	}
	if c.Embedding.Model == "" || c.Embedding.Dimensions < 1 || c.Embedding.Dimensions > 4096 {
		return errors.New("embedding model and dimensions are required (1..4096)")
	}
	if c.Reranker.Type != "none" && c.Reranker.Type != "voyage" && c.Reranker.Type != "http" {
		return fmt.Errorf("unsupported reranker type %q", c.Reranker.Type)
	}
	if c.Chunking.Mode != "semantic" && c.Chunking.Mode != "legacy" {
		return errors.New("chunking mode must be semantic or legacy")
	}
	b := c.Chunking
	if b.LegacyTarget < 1 || b.LegacyMax < b.LegacyTarget || b.LegacyMax > 8192 ||
		b.LegacyOverlap < 0 || b.LegacyOverlap >= b.LegacyTarget ||
		b.SemanticMin < 1 || b.SemanticTarget < b.SemanticMin || b.SemanticMax < b.SemanticTarget ||
		b.SemanticMax > 8192 || b.SemanticUnitMax < 1 || b.SemanticUnitMax > b.SemanticMax {
		return errors.New("invalid chunking thresholds")
	}
	if c.Indexing.Workers < 1 || c.Indexing.Workers > 64 || c.Indexing.SemanticWorkers < 1 ||
		c.Indexing.SemanticWorkers > c.Indexing.Workers || c.Indexing.EmbeddingBatchSize < 1 || c.Indexing.EmbeddingBatchSize > 256 {
		return errors.New("invalid indexing concurrency or embedding batch size")
	}
	if c.Alpha < 0 || c.Alpha > 1 || c.TopK < 1 || c.CandidateTopK < c.TopK || c.CandidateTopK > 200 {
		return errors.New("invalid retrieval limits or alpha")
	}
	if c.HTTPTimeoutMs < 1000 || c.HTTPMaxRetries < 0 || c.HTTPMaxRetries > 8 {
		return errors.New("invalid HTTP policy")
	}
	return nil
}
