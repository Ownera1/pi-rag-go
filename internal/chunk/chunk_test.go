package chunk

import (
	"context"
	"strings"
	"testing"

	"github.com/Ownera1/rag-go/internal/model"
)

type fake struct{}

func (fake) Model() string { return "fake" }

func (fake) Dimensions() int { return 2 }

func (fake) EmbedQuery(context.Context, string) ([]float32, error) { return []float32{1, 0}, nil }

func (fake) EmbedDocuments(_ context.Context, in []string) ([][]float32, error) {
	v := make([][]float32, len(in))
	for i, s := range in {
		if strings.Contains(s, "ALPHA") {
			v[i] = []float32{1, 0}
		} else {
			v[i] = []float32{0, 1}
		}
	}
	return v, nil
}

func TestSemanticGapAndProvenance(t *testing.T) {
	s := strings.Join([]string{
		"ALPHA_ONE " + strings.Repeat("apples ", 38) + ".",
		"ALPHA_TWO " + strings.Repeat("orchards ", 38) + ".",
		"BETA_ONE " + strings.Repeat("circuits ", 38) + ".",
		"BETA_TWO " + strings.Repeat("voltage ", 38) + ".",
	}, "\n")
	line := 1
	section := "topic"
	chunks, e := Semantic(context.Background(), []model.Block{{Text: s, LineStart: &line, Section: &section}}, fake{})
	if e != nil {
		t.Fatal(e)
	}
	if len(chunks) < 2 || !strings.Contains(chunks[0].Content, "ALPHA_TWO") || strings.Contains(chunks[0].Content, "BETA_ONE") {
		t.Fatalf("boundary: %+v", chunks)
	}
	if chunks[1].LineStart != 3 {
		t.Fatalf("line=%d", chunks[1].LineStart)
	}
	for _, marker := range []string{"ALPHA_ONE", "ALPHA_TWO", "BETA_ONE", "BETA_TWO"} {
		count := 0
		for _, c := range chunks {
			if strings.Contains(c.Content, marker) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%s appears %d times", marker, count)
		}
	}
}

func TestConfiguredChunkLimitsAndSectionBoundaries(t *testing.T) {
	cfg := model.DefaultConfig()
	cfg.Chunking.SemanticMin, cfg.Chunking.SemanticTarget, cfg.Chunking.SemanticMax, cfg.Chunking.SemanticUnitMax = 8, 12, 16, 8
	a, b := "A", "B"
	blocks := []model.Block{{Text: strings.Repeat("甲", 40), Section: &a}, {Text: strings.Repeat("乙", 40), Section: &b}}
	chunks, err := Semantic(context.Background(), blocks, fake{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		if Estimate(c.Content) > 16 || (strings.Contains(c.Content, "甲") && strings.Contains(c.Content, "乙")) {
			t.Fatalf("limit or section crossed: %+v", c)
		}
	}
	legacy := model.DefaultChunking()
	legacy.LegacyTarget, legacy.LegacyMax, legacy.LegacyOverlap = 10, 15, 0
	for _, c := range Legacy(blocks, legacy) {
		if Estimate(c.Content) > 15 || (strings.Contains(c.Content, "甲") && strings.Contains(c.Content, "乙")) {
			t.Fatalf("legacy boundary: %+v", c)
		}
	}
}

func TestMergeJoinsParagraphsWithinSectionAndPage(t *testing.T) {
	a, b := "A", "B"
	one, two := 1, 2
	blocks := []model.Block{
		{Text: "first", Section: &a, PageStart: &one, PageEnd: &one},
		{Text: "second", Section: &a, PageStart: &one, PageEnd: &one},
		{Text: "   "},
		{Text: "next page", Section: &a, PageStart: &two, PageEnd: &two},
		{Text: "next section", Section: &b, PageStart: &two, PageEnd: &two},
		{Text: "unpaged"},
		{Text: "unpaged too"},
	}
	got := Merge(blocks)
	want := []string{"first\n\nsecond", "next page", "next section", "unpaged\n\nunpaged too"}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i, b := range got {
		if b.Text != want[i] {
			t.Fatalf("%d: %q", i, b.Text)
		}
	}
	if *got[0].PageStart != 1 || *got[0].PageEnd != 1 || *blocks[0].PageEnd != 1 || blocks[0].Text != "first" {
		t.Fatal("page range widened or input mutated")
	}
}

func TestMergeKeepsLineNumbersExact(t *testing.T) {
	l1, l1e, l3, l4, l7 := 1, 1, 3, 4, 7
	blocks := []model.Block{
		{Text: "one", LineStart: &l1, LineEnd: &l1e},
		{Text: "three\nfour", LineStart: &l3, LineEnd: &l4},
		{Text: "seven", LineStart: &l7, LineEnd: &l7}, // two blank lines before it
	}
	got := Merge(blocks)
	if len(got) != 2 || got[0].Text != "one\n\nthree\nfour" || *got[0].LineStart != 1 || *got[0].LineEnd != 4 {
		t.Fatalf("%+v", got)
	}
	chunks := Legacy(got[:1], model.ChunkingConfig{LegacyTarget: 1, LegacyMax: 2, LegacyOverlap: 0})
	if last := chunks[len(chunks)-1]; last.LineEnd != 4 {
		t.Fatalf("line drift: %+v", chunks)
	}
}

type countingFake struct {
	fake
	calls int
}

func (c *countingFake) EmbedDocuments(ctx context.Context, in []string) ([][]float32, error) {
	c.calls++
	return c.fake.EmbedDocuments(ctx, in)
}

func TestSemanticBatchesUnitsAcrossBlocks(t *testing.T) {
	cfg := model.DefaultConfig()
	a, b := "A", "B"
	blocks := []model.Block{}
	for i := range 10 {
		s := &a
		if i%2 == 1 {
			s = &b
		}
		blocks = append(blocks, model.Block{Text: "ALPHA short paragraph. Another sentence here.", Section: s})
	}
	p := &countingFake{}
	chunks, err := Semantic(context.Background(), blocks, p, cfg)
	if err != nil || len(chunks) != 10 {
		t.Fatalf("%d chunks: %v", len(chunks), err)
	}
	if p.calls != 1 {
		t.Fatalf("%d embedding calls for 20 units, want 1", p.calls)
	}
}
