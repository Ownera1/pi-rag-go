package model

import (
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
	if c.Chunking.SemanticMax != 420 || c.Chunking.LegacyTarget != 180 || c.Chunking.LegacyOverlap != 0 || c.Indexing.EmbeddingBatchSize != 64 {
		t.Fatalf("defaults lost: %+v", c)
	}
}

func TestConfigRejectsInvalidThresholdsAndConcurrency(t *testing.T) {
	cases := []func(*Config){
		func(c *Config) { c.Chunking.SemanticMax = 100 }, func(c *Config) { c.Chunking.SemanticUnitMax = 0 },
		func(c *Config) { c.Chunking.LegacyOverlap = c.Chunking.LegacyTarget }, func(c *Config) { c.Indexing.Workers = 0 },
		func(c *Config) { c.Indexing.SemanticWorkers = c.Indexing.Workers + 1 }, func(c *Config) { c.Indexing.EmbeddingBatchSize = 0 },
	}
	for i, change := range cases {
		c := DefaultConfig()
		change(&c)
		if c.Validate() == nil {
			t.Fatalf("accepted invalid case %d", i)
		}
	}
}
