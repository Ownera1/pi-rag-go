package chunk

import (
	"context"
	"strings"
	"testing"

	"github.com/Ownera1/pi-rag-go/internal/model"
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
