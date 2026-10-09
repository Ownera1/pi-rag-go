package rag

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ownera1/rag-go/internal/model"
)

// paperFixture indexes a MinerU paper over four pages and a second paper.
func paperFixture(t *testing.T) (*Core, DocumentInfo, DocumentInfo) {
	t.Helper()
	c := openTest(t, t.TempDir(), fakeEmbedding{})
	t.Cleanup(func() { c.Close() })
	items := []string{}
	add := func(page int, typ, text string, level int) {
		lvl := ""
		if level > 0 {
			lvl = fmt.Sprintf(`,"text_level":%d`, level)
		}
		items = append(items, fmt.Sprintf(`{"type":%q,"text":%q,"page_idx":%d%s}`, typ, text, page, lvl))
	}
	words := func(tag string) string { return strings.Repeat(tag+" evidence words ", 40) }
	add(0, "text", "Alpha Title", 1)
	add(0, "text", "Introduction", 2)
	add(0, "text", words("intro0"), 0)
	add(1, "text", words("intro1"), 0)
	add(1, "text", "Method", 2)
	add(1, "text", words("method1"), 0)
	add(2, "equation", `$$y = Hx \tag{7}$$`, 0)
	items = append(items, `{"type":"image","img_path":"images/fig1.jpg","image_caption":["Fig. 1. Signal flow of the proposed method."],"page_idx":2}`)
	add(2, "text", words("method2"), 0)
	add(3, "text", "Results", 2)
	add(3, "text", words("results3"), 0)
	sourceFile(t, docPath(c, "Alpha paper/alpha_content_list.json"), []byte("["+strings.Join(items, ",")+"]"))
	sourceFile(t, docPath(c, "Alpha paper/images/fig1.jpg"), []byte("jpg"))
	sourceFile(t, docPath(c, "Beta paper/beta.txt"), []byte("beta evidence words and \\tag{7} elsewhere"))
	if _, err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	docs, err := c.Documents(context.Background())
	if err != nil || len(docs) != 2 {
		t.Fatalf("documents %+v %v", docs, err)
	}
	return c, docs[0], docs[1]
}

func TestReadOutlineAndContinuation(t *testing.T) {
	ctx := context.Background()
	c, alpha, _ := paperFixture(t)
	if alpha.ID == "" || alpha.Version == "" || !strings.Contains(alpha.Path, "Alpha paper") {
		t.Fatalf("document info %+v", alpha)
	}
	all, err := c.Read(ctx, ReadOptions{Document: "alpha", MaxTokens: maxReadTokens})
	if err != nil || all.Truncated || len(all.Passages) != all.Chunks || all.Chunks < 5 {
		t.Fatalf("whole read: %+v %v", all, err)
	}
	images := 0
	for i, p := range all.Passages {
		if p.Index != i {
			t.Fatalf("passage %d has index %d", i, p.Index)
		}
		for _, img := range p.Images {
			images++
			if !strings.Contains(p.Content, "Fig. 1. Signal flow") || filepath.Base(img) != "fig1.jpg" || !filepath.IsAbs(img) {
				t.Fatalf("image %q on passage %+v", img, p)
			}
		}
	}
	if images != 1 {
		t.Fatalf("want the figure's image once, got %d", images)
	}

	// Small budgets page through the document without gaps or repeats.
	var paged []Passage
	from := 0
	for {
		r, err := c.Read(ctx, ReadOptions{Document: alpha.ID, From: &from, MaxTokens: 1})
		if err != nil || len(r.Passages) != 1 {
			t.Fatalf("paged read: %+v %v", r, err)
		}
		paged = append(paged, r.Passages...)
		if !r.Truncated {
			break
		}
		from = *r.Next
	}
	if len(paged) != len(all.Passages) {
		t.Fatalf("paged %d passages, want %d", len(paged), len(all.Passages))
	}
	for i := range paged {
		if paged[i].Content != all.Passages[i].Content {
			t.Fatalf("paged passage %d differs", i)
		}
	}

	out, err := c.Outline(ctx, alpha.ID)
	if err != nil || out.Pages != 4 || out.Chunks != all.Chunks {
		t.Fatalf("outline %+v %v", out, err)
	}
	names := []string{}
	next := 0
	for _, s := range out.Sections {
		if s.From != next || s.To < s.From {
			t.Fatalf("sections must tile the document: %+v", out.Sections)
		}
		next = s.To + 1
		names = append(names, s.Section)
	}
	// The title heading every section sits under is named once, not repeated.
	if next != out.Chunks || strings.Join(names, "|") != "Alpha Title|Introduction|Method|Results" {
		t.Fatalf("sections %q cover %d of %d", names, next, out.Chunks)
	}
	method := out.Sections[2]
	r, err := c.Read(ctx, ReadOptions{Document: alpha.ID, From: &method.From, To: &method.To})
	if err != nil || r.Passages[0].Section != "Method" || len(r.Passages) != method.To-method.From+1 {
		t.Fatalf("section read %+v %v", r, err)
	}
	for _, p := range r.Passages[1:] {
		if p.Section != "" {
			t.Fatalf("unchanged section repeated: %+v", p)
		}
	}

	// around needs no document: the chunk id names it.
	one, two := 1, 1
	r, err = c.Read(ctx, ReadOptions{Around: fmt.Sprintf("%s-%d", alpha.ID, 2), Before: &one, After: &two})
	if err != nil || len(r.Passages) != 3 || r.Passages[0].Index != 1 || r.Passages[2].Index != 3 {
		t.Fatalf("around read %+v %v", r, err)
	}

	r, err = c.Read(ctx, ReadOptions{Document: alpha.ID, Pages: "3"})
	if err != nil || len(r.Passages) == 0 {
		t.Fatalf("page read %+v %v", r, err)
	}
	for _, p := range r.Passages {
		if *p.PageEnd < 3 || *p.PageStart > 3 {
			t.Fatalf("passage outside page 3: %+v", p)
		}
	}
}

func TestTitlePrefix(t *testing.T) {
	chunks := func(sections ...string) []model.Chunk {
		out := []model.Chunk{}
		for _, s := range sections {
			out = append(out, model.Chunk{Section: &s})
		}
		return append(out, model.Chunk{})
	}
	for want, in := range map[string][]model.Chunk{
		"T / ": chunks("T", "T / A", "T / A / x", "T / B"),
		"":     chunks("A", "B / x"),
	} {
		if got := titlePrefix(in); got != want {
			t.Errorf("titlePrefix(%v) = %q, want %q", in, got, want)
		}
	}
	if got := titlePrefix(chunks("T", "T")); got != "" {
		t.Errorf("unnested title stripped: %q", got)
	}
}

func TestReadErrors(t *testing.T) {
	ctx := context.Background()
	c, alpha, beta := paperFixture(t)
	five := 500
	for name, opts := range map[string]ReadOptions{
		"missing document": {Document: "gamma"},
		"ambiguous":        {Document: "paper"},
		"stale version":    {Document: alpha.ID, Version: "000000000000"},
		"foreign chunk":    {Document: alpha.ID, Around: beta.ID + "-0"},
		"gone chunk":       {Around: alpha.ID + "-500"},
		"out of range":     {Document: alpha.ID, From: &five},
		"two locators":     {Document: alpha.ID, Around: alpha.ID + "-0", Pages: "1"},
		"pages off paper":  {Document: alpha.ID, Pages: "9-10"},
		"no page numbers":  {Document: beta.ID, Pages: "1"},
		"budget":           {Document: alpha.ID, MaxTokens: maxReadTokens + 1},
	} {
		if r, err := c.Read(ctx, opts); err == nil {
			t.Errorf("%s: no error, got %+v", name, r)
		}
	}
	if _, err := c.Read(ctx, ReadOptions{Document: alpha.ID, Version: alpha.Version}); err != nil {
		t.Fatalf("current version rejected: %v", err)
	}
}

func TestQueryWithinDocumentAndLiteral(t *testing.T) {
	ctx := context.Background()
	c, alpha, beta := paperFixture(t)
	for _, mode := range []string{"bm25", "vector", "hybrid"} {
		q, err := c.Query(ctx, "evidence words", QueryOptions{Mode: mode, TopK: 20, Document: beta.ID, DisableSync: true})
		if err != nil || len(q.Hits) != 1 || q.Hits[0].Chunk.Path != beta.Path {
			t.Fatalf("%s within beta: %+v %v", mode, q.Hits, err)
		}
	}
	q, err := c.Query(ctx, `\tag {7}`, QueryOptions{Mode: "literal", TopK: 20, DisableSync: true})
	if err != nil || len(q.Hits) != 2 || q.Method != "literal" || len(q.Documents) != 2 {
		t.Fatalf("literal: %+v %v", q, err)
	}
	q, err = c.Query(ctx, `\tag{7}`, QueryOptions{Mode: "literal", Document: "Alpha", DisableSync: true})
	if err != nil || len(q.Hits) != 1 || !strings.Contains(q.Hits[0].Chunk.Content, `\tag{7}`) || !strings.HasPrefix(q.Hits[0].Chunk.ID, alpha.ID+"-") {
		t.Fatalf("literal within alpha: %+v %v", q, err)
	}
	// Hits from one document share a single description of it.
	q, err = c.Query(ctx, "evidence words", QueryOptions{Mode: "bm25", TopK: 20, Document: alpha.ID, DisableSync: true})
	if err != nil || len(q.Hits) < 2 || len(q.Documents) != 1 || q.Documents[alpha.ID].Version != alpha.Version || q.Documents[alpha.ID].Metadata != nil {
		t.Fatalf("documents %+v %v", q.Documents, err)
	}
	for _, h := range q.Hits {
		if h.Document != alpha.ID {
			t.Fatalf("hit document %q, want %q", h.Document, alpha.ID)
		}
	}
	if _, err = c.Query(ctx, "evidence", QueryOptions{Document: "gamma", DisableSync: true}); err == nil {
		t.Fatal("unknown document accepted")
	}
}

func TestFixRecordsCorrectionForNextQuery(t *testing.T) {
	ctx := context.Background()
	c, alpha, beta := paperFixture(t)
	for name, opts := range map[string]FixOptions{
		"stale version": {Document: alpha.ID, Wrong: "y = Hx", Right: "y = Gx", Version: "000000000000"},
		"plain file":    {Document: beta.ID, Wrong: "beta", Right: "gamma"},
		"absent":        {Document: alpha.ID, Wrong: "y = Qx", Right: "y = Gx"},
	} {
		if r, err := c.Fix(ctx, opts); err == nil {
			t.Errorf("%s: no error, got %+v", name, r)
		}
	}
	r, err := c.Fix(ctx, FixOptions{Document: alpha.ID, Wrong: "y = Hx", Right: "y = Gx", Version: alpha.Version})
	if err != nil || r.Line != 1 || r.File != filepath.Join(filepath.Dir(alpha.Path), "rag-fixes.tsv") {
		t.Fatalf("fix %+v %v", r, err)
	}
	// Reading does not sync, so chunk ids stay valid until the next query.
	if read, err := c.Read(ctx, ReadOptions{Document: alpha.ID, Version: alpha.Version, MaxTokens: maxReadTokens}); err != nil || !strings.Contains(fmt.Sprint(read.Passages), "y = Hx") {
		t.Fatalf("read after fix: %v", err)
	}
	q, err := c.Query(ctx, "y = Gx", QueryOptions{Mode: "literal", Document: alpha.ID})
	if err != nil || len(q.Hits) != 1 || q.Documents[alpha.ID].Version == alpha.Version {
		t.Fatalf("query after fix: %+v %v", q, err)
	}
}
