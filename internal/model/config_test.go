package model

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestPartialChunkingConfigPreservesDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"chunking":{"mode":"legacy","legacyOverlap":0},"indexing":{"workers":4}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Chunking.SemanticMax != 420 || c.Chunking.LegacyTarget != 180 || c.Chunking.LegacyOverlap != 0 || c.Indexing.EmbeddingBatchSize != 64 || c.Indexing.EmbeddingWorkers != 4 {
		t.Fatalf("defaults lost: %+v", c)
	}
}

func TestConfigRejectsInvalidThresholdsAndConcurrency(t *testing.T) {
	cases := []func(*Config){
		func(c *Config) { c.Chunking.SemanticMax = 100 }, func(c *Config) { c.Chunking.SemanticUnitMax = 0 },
		func(c *Config) { c.Chunking.LegacyOverlap = c.Chunking.LegacyTarget }, func(c *Config) { c.Indexing.Workers = 0 },
		func(c *Config) { c.Indexing.SemanticWorkers = c.Indexing.Workers + 1 }, func(c *Config) { c.Indexing.EmbeddingBatchSize = 0 },
		func(c *Config) { c.Indexing.EmbeddingWorkers = 0 },
		func(c *Config) { c.Alpha = math.NaN() }, func(c *Config) { c.HTTPTimeoutMs = 600001 },
		func(c *Config) { c.Embedding.APIKeyEnv = "pa-1234567890abcdef" }, func(c *Config) { c.Reranker.BaseURL = "api.voyageai.com/v1" },
		func(c *Config) { c.Reranker.Type, c.Reranker.Model = "voyage", "" },
	}
	for i, change := range cases {
		c := DefaultConfig()
		change(&c)
		if c.Validate() == nil {
			t.Fatalf("accepted invalid case %d", i)
		}
	}
	c := DefaultConfig()
	c.Chunking.SemanticMin = 300
	if err := c.Validate(); err == nil || err.Error() != "chunking.semanticTarget (280) must be at least chunking.semanticMin (300)" {
		t.Fatalf("error does not name the bound: %v", err)
	}
}

func TestMalformedWorkspaceConfigIsRejected(t *testing.T) {
	for _, input := range []string{"null", "[]", "{} {}", `{"trackedPaths":[]}`, `{"documents":""}`} {
		p := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(p, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(p); err == nil {
			t.Fatalf("accepted malformed configuration: %s", input)
		}
	}
}
