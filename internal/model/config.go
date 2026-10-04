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
		Chunking:        ChunkingConfig{Mode: "semantic"},
		TrackedPaths:    []string{},
		ExcludePatterns: []string{},
		Alpha:           0.4,
		CandidateTopK:   30,
		TopK:            5,
		HTTPTimeoutMs:   30000,
		HTTPMaxRetries:  3,
	}
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
	if _, ok := fields["chunking"]; ok {
		c.Chunking = ChunkingConfig{}
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
	if c.Alpha < 0 || c.Alpha > 1 || c.TopK < 1 || c.CandidateTopK < c.TopK || c.CandidateTopK > 200 {
		return errors.New("invalid retrieval limits or alpha")
	}
	if c.HTTPTimeoutMs < 1000 || c.HTTPMaxRetries < 0 || c.HTTPMaxRetries > 8 {
		return errors.New("invalid HTTP policy")
	}
	return nil
}
