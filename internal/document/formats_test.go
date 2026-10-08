package document

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ownera1/rag-go/internal/model"
)

func joined(blocks []model.Block) string {
	var b strings.Builder
	for _, block := range blocks {
		b.WriteString(block.Text)
	}
	return b.String()
}

func TestMarkdownASTHeadingsAndSourceLines(t *testing.T) {
	source := "# Main\n\n```go\n# inside code\n```\n\nSubsection\n----------\nbody\n"
	blocks, err := markdown(context.Background(), []byte(source))
	if err != nil || len(blocks) != 2 {
		t.Fatalf("blocks=%+v err=%v", blocks, err)
	}
	if *blocks[0].Section != "Main" || !strings.Contains(blocks[0].Text, "# inside code") ||
		*blocks[1].Section != "Main / Subsection" || *blocks[1].LineStart != 7 || *blocks[1].LineEnd != 9 {
		t.Fatalf("incorrect boundaries: %+v", blocks)
	}
	// Empty headings are legal CommonMark and must not panic.
	for _, source := range []string{"#\ntext", "## \ntext", "# *styled* `title`\nbody"} {
		if _, err := markdown(context.Background(), []byte(source)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMarkdownPaperFilters(t *testing.T) {
	source := "# Model\n\nThe signal is\n\n$$\n\\mathbf{Y}=\\mathbf{H}\\mathbf{X}\n$$\n\n$$ \\nu_l $$\n\n![Fig. 2 system](images/a.png)\n<table><tr><td>SNR &amp; BER</td></tr></table>\nafter\n\n# References\n\n[1] CITE_LEAK\n\n## Sub\n\nSUB_LEAK\n\n# Appendix\n\nproof\n"
	blocks, err := markdown(context.Background(), []byte(source))
	if err != nil || len(blocks) != 4 {
		t.Fatalf("blocks=%+v err=%v", blocks, err)
	}
	prose, eq, tail, app := blocks[0], blocks[1], blocks[2], blocks[3]
	if prose.Kind != "" || prose.Text != "# Model\n\nThe signal is" || *prose.LineEnd != 3 {
		t.Fatalf("prose: %+v", prose)
	}
	if eq.Kind != "equation" || *eq.LineStart != 5 || *eq.LineEnd != 9 || !strings.HasSuffix(eq.Text, "$$ \\nu_l $$") {
		t.Fatalf("equation: %+v", eq)
	}
	if tail.Kind != "" || tail.Text != "Fig. 2 system\nSNR & BER\nafter" || *tail.LineStart != 11 || *tail.LineEnd != 13 {
		t.Fatalf("figure/table: %+v", tail)
	}
	if *app.Section != "Appendix" || strings.Contains(joined(blocks), "LEAK") {
		t.Fatalf("references not skipped: %+v", blocks)
	}
}

func TestHTMLBodyFiltering(t *testing.T) {
	source := `<html><head><title>HEAD_LEAK</title></head><body><nav>NAV_LEAK</nav>
        <main><h1>Methods</h1><p>signal <b>estimation</b> &amp; retrieval.</p>
        <h2>Details</h2><p>script</p><p hidden>HIDDEN_LEAK</p>
        <script>SCRIPT_LEAK</script><aside>ASIDE_LEAK</aside><p>second evidence</p></main>
        <footer>FOOTER_LEAK</footer><article>OTHER_ARTICLE_LEAK</article></body></html>`
	blocks, err := parseHTML(context.Background(), []byte(source))
	if err != nil || len(blocks) != 3 {
		t.Fatalf("blocks=%+v err=%v", blocks, err)
	}
	if blocks[0].Text != "signal estimation & retrieval." || blocks[1].Text != "script" ||
		*blocks[2].Section != "Methods / Details" || strings.Contains(joined(blocks), "LEAK") {
		t.Fatalf("body filtering: %+v", blocks)
	}
	for _, b := range blocks {
		if b.PageStart != nil || b.LineStart != nil {
			t.Fatal("invented HTML source position")
		}
	}
}

func TestDOCXParagraphsHeadingsAndHiddenRuns(t *testing.T) {
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	files := map[string]string{
		"word/document.xml": `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
            <w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Methods</w:t></w:r></w:p>
            <w:p><w:r><w:t>Signal </w:t></w:r><w:r><w:rPr><w:vanish/></w:rPr><w:t>HIDDEN_LEAK</w:t></w:r>
                <w:r><w:t>estimation</w:t></w:r><w:r><w:rPr><w:vanish w:val="false"/></w:rPr><w:t> visible</w:t></w:r>
                <w:del><w:r><w:t>DELETED_LEAK</w:t></w:r></w:del></w:p>
            <w:p><w:pPr><w:pStyle w:val="CustomSection"/></w:pPr><w:r><w:t>Details</w:t></w:r></w:p>
            <w:tbl><w:tr><w:tc><w:p><w:r><w:t>Cell evidence</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
        </w:body></w:document>`,
		"word/styles.xml": `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
            <w:style w:styleId="CustomSection"><w:pPr><w:outlineLvl w:val="1"/></w:pPr></w:style></w:styles>`,
	}
	for name, xml := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(xml)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	blocks, err := parseDOCX(context.Background(), data.Bytes())
	if err != nil || len(blocks) != 4 {
		t.Fatalf("blocks=%+v err=%v", blocks, err)
	}
	if blocks[1].Text != "Signal estimation visible" || *blocks[3].Section != "Methods / Details" || strings.Contains(joined(blocks), "LEAK") {
		t.Fatalf("Word content: %+v", blocks)
	}
	for _, b := range blocks {
		if b.PageStart != nil || b.LineStart != nil {
			t.Fatal("Word page layout requires a renderer; parser invented a position")
		}
	}
	if _, err = parseDOCX(context.Background(), []byte("not zip")); err == nil {
		t.Fatal("accepted invalid DOCX")
	}
}

func TestJATSBodySectionsAndExclusions(t *testing.T) {
	x := `<!DOCTYPE article SYSTEM "https://example.invalid/not-fetched.dtd"><article>
        <front><article-meta><fpage>99</fpage><abstract><p>Abstract signal</p></abstract></article-meta></front>
        <body><sec><title>Methods</title><p>Before <italic>signal</italic><inline-formula>FORMULA_LEAK</inline-formula> after.</p>
        <sec><title>Details</title><p>Nested &alpha; evidence <xref>[1]</xref>.</p></sec>
        <fig><p>FIGURE_LEAK</p></fig><table-wrap><p>TABLE_LEAK</p></table-wrap></sec></body>
        <back><ref-list><p>REFERENCE_LEAK</p></ref-list></back></article>`
	blocks, err := ParseJATS(context.Background(), []byte(x))
	if err != nil || len(blocks) != 3 {
		t.Fatalf("blocks=%+v err=%v", blocks, err)
	}
	if *blocks[0].Section != "Abstract" || *blocks[2].Section != "Methods / Details" ||
		strings.Contains(joined(blocks), "LEAK") || !strings.Contains(blocks[2].Text, "α") || blocks[1].PageStart != nil {
		t.Fatalf("JATS provenance/filtering: %+v", blocks)
	}
	for _, bad := range []string{`<article><body/></article>`, `<root/>`, `<article><body><p>bad</body>`,
		`<!DOCTYPE article [<!ENTITY test "bad">]><article><body><p>&test;</p></body></article>`} {
		if _, err := ParseJATS(context.Background(), []byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestMinerUKeepsMathAndAlgorithms(t *testing.T) {
	for name, source := range map[string]string{
		"v1": `[{"type":"text","text":"Signal evidence","page_idx":0},{"type":"code","sub_type":"algorithm","code_caption":["Algorithm 1"],"code_body":"repeat update","page_idx":0}]`,
		"v2": `[[{"type":"paragraph","content":{"paragraph_content":[{"type":"text","content":"Signal evidence with"},{"type":"equation_inline","content":"\\mathbf{H}"}]}},` +
			`{"type":"code","content":{"code_caption":[{"type":"text","content":"Algorithm 1"}],"code_content":[{"type":"text","content":"repeat update"}]}}]]`,
		"legacy-middle": `{"pdf_info":[{"page_idx":0,"para_blocks":[` +
			`{"type":"text","lines":[{"spans":[{"type":"text","content":"Signal evidence with"},{"type":"inline_equation","content":"\\mathbf{H}"}]}]},` +
			`{"type":"interline_equation","lines":[{"spans":[{"type":"interline_equation","content":"y = Hx"}]}]},` +
			`{"type":"code","blocks":[{"type":"code_caption","lines":[{"spans":[{"type":"text","content":"Algorithm 1"}]}]},{"type":"code_body","lines":[{"spans":[{"type":"text","content":"repeat update"}]}]}]}]}]}`,
	} {
		blocks, err := ParseMinerU(context.Background(), []byte(source))
		text := joined(blocks)
		if err != nil || !strings.Contains(text, "Signal evidence") || !strings.Contains(text, "Algorithm 1") || !strings.Contains(text, "repeat update") ||
			name != "v1" && !strings.Contains(text, "$\\mathbf{H}$") || name == "legacy-middle" && !strings.Contains(text, "$$y = Hx$$") {
			t.Fatalf("%s: %q err=%v", name, text, err)
		}
	}
}

func TestMinerUContractsAndPhysicalPages(t *testing.T) {
	tests := []struct {
		name, source, text string
		page               int
	}{
		{"v1", `[{"type":"text","text":"Methods","text_level":1,"page_idx":2},{"type":"text","text":"Signal evidence","page_idx":2},{"type":"image","text":"LEAK","page_idx":2},` +
			`{"type":"ref_text","text":"LEAK","page_idx":2},{"type":"header","text":"LEAK","page_idx":2},` +
			`{"type":"footer","text":"LEAK","page_idx":2},{"type":"page_number","text":"LEAK","page_idx":2},` +
			`{"type":"page_footnote","text":"LEAK","page_idx":2}]`, "Signal evidence", 3},
		{"v2", `[[{"type":"title","content":{"title_content":[{"type":"text","content":"Methods"}],"level":1}},{"type":"paragraph","content":{"paragraph_content":[{"type":"text","content":"Signal evidence"}]}},{"type":"list","content":{"list_type":"reference_list","list_items":[{"item_content":"LEAK"}]}}]]`, "Signal evidence", 0},
		{"middle", `{"schema":"docvortex.middle","schema_version":"2.0","pages":[{"page_idx":7,"blocks":[{"type":"text","content":[{"type":"text","content":"Signal evidence"}]}]}]}`, "Signal evidence", 8},
		{"structured", `{"pages":[{"page_idx":7,"blocks":[{"type":"text","content":"Signal evidence"}]}]}`, "Signal evidence", 8},
		{"legacy-middle", `{"pdf_info":[{"page_idx":7,"para_blocks":[{"type":"text","lines":[{"spans":[{"type":"text","content":"Signal evidence"}]}]}]}]}`, "Signal evidence", 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, err := ParseMinerU(context.Background(), []byte(tt.source))
			if err != nil || !strings.Contains(joined(blocks), tt.text) || strings.Contains(joined(blocks), "LEAK") {
				t.Fatalf("blocks=%+v err=%v", blocks, err)
			}
			for _, b := range blocks {
				if tt.page == 0 {
					if b.PageStart != nil || b.PageEnd != nil {
						t.Fatal("page array order must not be presented as original PDF pages")
					}
				} else if b.PageStart == nil || *b.PageStart != tt.page || *b.PageEnd != tt.page {
					t.Fatalf("incorrect physical page: %+v", b)
				}
			}
		})
	}
	for _, bad := range []string{`[]`, `[{"type":"text","text":"x","page_idx":-1}]`,
		`[{"type":"text","text":"x","page_idx":0.2}]`, `{"pages":[{"blocks":[]}]}`,
		`{"schema":"docvortex.model","schema_version":"2.0","pages":[]}`,
		`{"schema":"docvortex.middle","schema_version":"3.0","pages":[]}`, `[] {}`} {
		if _, err := ParseMinerU(context.Background(), []byte(bad)); err == nil {
			t.Fatalf("accepted bad MinerU payload %s", bad)
		}
	}
}

func TestMinerUIndexesCaptionsTablesAndEquations(t *testing.T) {
	for name, source := range map[string]string{
		"v1": `[{"type":"text","text":"Results","text_level":1,"page_idx":0},` +
			`{"type":"image","img_path":"images/a.jpg","image_caption":["Fig. 1. Uplink layout."],"image_footnote":["Drawn to scale."],"page_idx":0},` +
			`{"type":"chart","chart_caption":["Fig. 2. MSE versus SINR."],"chart_footnote":[],"page_idx":0},` +
			`{"type":"table","table_caption":["TABLE I Parameters"],"table_body":"<table><tr><td>Carrier &amp; band</td><td>3.5 GHz</td></tr></table>","page_idx":0},` +
			`{"type":"equation","text":"$$y = Hx + n$$","text_format":"latex","page_idx":0}]`,
		"v2": `[[{"type":"title","content":{"title_content":[{"type":"text","content":"Results"}],"level":1}},` +
			`{"type":"image","content":{"image_source":{"path":"images/a.jpg"},"image_caption":[{"type":"text","content":"Fig. 1. Uplink layout."}],"image_footnote":[{"type":"text","content":"Drawn to scale."}]}},` +
			`{"type":"chart","content":{"chart_caption":[{"type":"text","content":"Fig. 2. MSE versus SINR."}],"chart_footnote":[]}},` +
			`{"type":"table","content":{"table_caption":[{"type":"text","content":"TABLE I Parameters"}],"html":"<table><tr><td>Carrier &amp; band</td><td>3.5 GHz</td></tr></table>"}},` +
			`{"type":"equation_interline","content":{"math_content":"y = Hx + n","math_type":"latex"}}]]`,
	} {
		t.Run(name, func(t *testing.T) {
			blocks, err := ParseMinerU(context.Background(), []byte(source))
			if err != nil {
				t.Fatal(err)
			}
			text := joined(blocks)
			for _, want := range []string{"Fig. 1. Uplink layout.", "Drawn to scale.", "Fig. 2. MSE versus SINR.",
				"TABLE I Parameters", "Carrier & band 3.5 GHz", "$$y = Hx + n$$"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q in %q", want, text)
				}
			}
			if strings.Contains(text, "<td>") || strings.Contains(text, "images/") {
				t.Fatalf("markup or image path leaked: %q", text)
			}
			for _, b := range blocks {
				if b.Section == nil || *b.Section != "Results" {
					t.Fatalf("figure lost its section: %+v", b)
				}
			}
		})
	}
}

func TestTEICoordinatesKeepPageBoundaries(t *testing.T) {
	x := `<TEI xmlns="http://www.tei-c.org/ns/1.0"><text><body><div><head>Methods</head>
        <p coords="2,10,20,30,40">Page two.</p><p coords="3,10,20,30,40;4,10,20,30,40">Spanning pages.</p>
        <p>Unknown page.</p></div></body></text></TEI>`
	blocks, err := ParseTEI(context.Background(), []byte(x))
	if err != nil || len(blocks) != 3 {
		t.Fatalf("blocks=%+v err=%v", blocks, err)
	}
	if *blocks[0].PageStart != 2 || *blocks[1].PageStart != 3 || *blocks[1].PageEnd != 4 || blocks[2].PageStart != nil {
		t.Fatalf("incorrect page provenance: %+v", blocks)
	}
	for _, coords := range []string{"0,1,2,3,4", "1,NaN,2,3,4", "1,2", "1,2,3,-1,4"} {
		bad := strings.Replace(x, "2,10,20,30,40", coords, 1)
		if _, err := ParseTEI(context.Background(), []byte(bad)); err == nil {
			t.Fatal("accepted invalid coordinates", coords)
		}
	}
	if _, err = ParseTEI(context.Background(), []byte(x+x)); err == nil {
		t.Fatal("accepted multiple TEI roots")
	}
}

func writeDocument(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestManifestIdentityChangeDetectionAndBoundary(t *testing.T) {
	dir := t.TempDir()
	content := filepath.Join(dir, "content.md")
	writeDocument(t, content, "# Methods\nFirst evidence")
	manifest := filepath.Join(dir, "rag-source.json")
	metadata := Manifest{Version: 1, Format: "markdown", ContentPath: "content.md", SourcePath: "original.pdf", Title: "Paper"}
	writeManifest := func() {
		b, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		writeDocument(t, manifest, string(b))
	}
	writeManifest()
	first, err := Parse(context.Background(), manifest)
	if err != nil || first.SourcePath != filepath.Join(dir, "original.pdf") || first.Title != "Paper" {
		t.Fatalf("document=%+v err=%v", first, err)
	}
	writeDocument(t, content, "# Methods\nChanged evidence")
	next, err := Parse(context.Background(), manifest)
	if err != nil || next.Hash == first.Hash || next.ID != first.ID {
		t.Fatalf("identity must remain stable as content changes: %+v err=%v", next, err)
	}
	out := filepath.Join(t.TempDir(), "outside.md")
	writeDocument(t, out, "outside")
	if err := os.Symlink(out, filepath.Join(dir, "escape.md")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{out, "../outside.md", "escape.md", "rag-source.json"} {
		metadata.ContentPath = path
		writeManifest()
		if _, err := Parse(context.Background(), manifest); err == nil {
			t.Fatalf("accepted manifest content path %q", path)
		}
	}
}

func TestMarkdownPagesFromMinerU(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("Pilot sequences are reused across neighbouring cells, which creates contamination. ", 4)
	items := []map[string]any{
		{"type": "text", "text": "Model", "text_level": 1, "page_idx": 0},
		{"type": "text", "text": "The base station serves several users over a shared wideband channel.", "page_idx": 0},
		{"type": "text", "text": "Interference from neighbouring cells dominates the received signal at", "page_idx": 0},
		{"type": "text", "text": "the cell edge, so the estimator must cancel it before decoding.", "page_idx": 1},
		{"type": "text", "text": long, "page_idx": 1},
		{"type": "text", "text": "Simulation results confirm the gain of interference cancellation over baselines.", "page_idx": 2},
	}
	b, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	writeDocument(t, filepath.Join(dir, "paper_content_list.json"), string(b))
	// The fourth paragraph is not in the export but starts like the last
	// one; the third was edited by hand and gained a stray NUL.
	writeDocument(t, filepath.Join(dir, "full.md"), "# Model\n\nThe base station serves several users over a shared wideband channel.\n\n"+
		"Interference from neighbouring cells dominates the received signal at the cell edge, so the estimator must cancel it before decoding.\n\n"+
		"Simulation results confirm.\n\n"+strings.Replace(long, "contamination", "pilot\x00 contamination", 1)+"\n\n"+
		"Simulation results confirm the gain of interference cancellation over baselines.\n")
	manifest := filepath.Join(dir, "rag-source.json")
	writeDocument(t, manifest, `{"version":1,"format":"markdown","contentPath":"full.md","pagesFrom":"paper_content_list.json"}`)
	d, err := Parse(context.Background(), manifest)
	if err != nil || len(d.Blocks) != 6 {
		t.Fatalf("blocks=%+v err=%v", d.Blocks, err)
	}
	want := [][3]int{{1, 1, 1}, {3, 1, 1}, {5, 1, 2}, {7, 2, 2}, {9, 2, 2}, {11, 3, 3}}
	for i, b := range d.Blocks {
		if *b.LineStart != want[i][0] || *b.LineEnd != want[i][0] || *b.PageStart != want[i][1] || *b.PageEnd != want[i][2] {
			t.Fatalf("block %d: lines %d-%d pages %d-%d, want %v", i, *b.LineStart, *b.LineEnd, *b.PageStart, *b.PageEnd, want[i])
		}
	}
	before, err := InputFingerprint(context.Background(), manifest)
	if err != nil || before != d.Hash {
		t.Fatalf("fingerprint %q hash %q err %v", before, d.Hash, err)
	}
	writeDocument(t, filepath.Join(dir, "paper_content_list.json"), `[{"type":"text","text":"An unrelated paper about protein folding and molecular dynamics.","page_idx":0}]`)
	if after, _ := InputFingerprint(context.Background(), manifest); after == before {
		t.Fatal("page export change not detected")
	}
	if _, err = Parse(context.Background(), manifest); err == nil || !strings.Contains(err.Error(), "matches 0 of") {
		t.Fatalf("another document's export was accepted: %v", err)
	}
	writeDocument(t, manifest, `{"version":1,"format":"text","contentPath":"full.md","pagesFrom":"paper_content_list.json"}`)
	if _, err = Parse(context.Background(), manifest); err == nil {
		t.Fatal("pagesFrom accepted for a non-Markdown format")
	}
}

func TestLayoutCrossPagePages(t *testing.T) {
	// Paper titles can contain glob metacharacters.
	dir := filepath.Join(t.TempDir(), "Paper [2025]")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p1 := "The base station serves several users over a shared wideband channel."
	p2a := "Interference from neighbouring cells dominates the received signal"
	p2b := "at the cell edge, so the estimator must cancel it before decoding."
	algo := "Algorithm 1 update every message until convergence"
	p3a, p3b := "Pilot sequences are reused across neighbouring cells with channel", "so contamination grows with the number of reused pilot sequences."
	// MinerU keeps a paragraph continuing onto page 2 whole on page 1;
	// layout.json marks the moved spans cross_page.
	content, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": p1, "page_idx": 0},
		{"type": "text", "text": p2a + " " + p2b, "page_idx": 0},
		{"type": "text", "text": p3a + " $\\mathbf{H}$ " + p3b, "page_idx": 1},
	})
	span := func(text string, extra ...any) map[string]any {
		s := map[string]any{"type": "text", "content": text}
		for i := 0; i < len(extra); i += 2 {
			s[extra[i].(string)] = extra[i+1]
		}
		return s
	}
	lines := func(spans ...map[string]any) []any {
		out := []any{}
		for _, s := range spans {
			out = append(out, map[string]any{"spans": []any{s}})
		}
		return out
	}
	layout, _ := json.Marshal(map[string]any{"pdf_info": []any{
		map[string]any{"page_idx": 0, "para_blocks": []any{
			map[string]any{"type": "text", "lines": lines(span(p1))},
			map[string]any{"type": "text", "lines": lines(span(p2a), span(p2b, "cross_page", true))},
		}, "discarded_blocks": []any{map[string]any{"type": "header", "lines": lines(span("JOURNAL HEADER LEAK"))}}},
		map[string]any{"page_idx": 1, "para_blocks": []any{
			map[string]any{"type": "text", "lines": []any{}, "lines_deleted": true},
			map[string]any{"type": "code", "blocks": []any{map[string]any{"type": "code_body", "lines": lines(span(algo))}}},
			map[string]any{"type": "text", "lines": []any{map[string]any{"spans": []any{span(p3a), span("\\mathbf{H}", "type", "inline_equation"), span(p3b)}}}},
		}},
	}})
	list := filepath.Join(dir, "paper_content_list.json")
	writeDocument(t, list, string(content))
	pages := func(blocks []model.Block) [][2]int {
		out := [][2]int{}
		for _, b := range blocks {
			out = append(out, [2]int{*b.PageStart, *b.PageEnd})
		}
		return out
	}
	d, err := Parse(context.Background(), list)
	if err != nil || fmt.Sprint(pages(d.Blocks)) != "[[1 1] [1 1] [2 2]]" {
		t.Fatalf("without layout.json: %v err=%v", pages(d.Blocks), err)
	}
	writeDocument(t, filepath.Join(dir, "layout.json"), string(layout))
	d, err = Parse(context.Background(), list)
	if err != nil || fmt.Sprint(pages(d.Blocks)) != "[[1 1] [1 2] [2 2]]" {
		t.Fatalf("content list repaged by layout.json: %v err=%v", pages(d.Blocks), err)
	}
	if fp, _ := InputFingerprint(context.Background(), list); fp != d.Hash {
		t.Fatal("layout.json is not part of the input fingerprint")
	}
	writeDocument(t, filepath.Join(dir, "full.md"), p1+"\n\n"+p2a+" "+p2b+"\n\n"+algo+"\n\n"+p3a+" $\\mathbf{H}$ "+p3b+"\n")
	manifest := filepath.Join(dir, "rag-source.json")
	writeDocument(t, manifest, `{"version":1,"format":"markdown","contentPath":"full.md","pagesFrom":"layout.json"}`)
	d, err = Parse(context.Background(), manifest)
	if err != nil || fmt.Sprint(pages(d.Blocks)) != "[[1 1] [1 2] [2 2] [2 2]]" {
		t.Fatalf("Markdown paged by layout.json: %v err=%v", pages(d.Blocks), err)
	}
	// A layout.json from another run leaves the content list's own pages.
	writeDocument(t, filepath.Join(dir, "layout.json"), `{"pdf_info":[{"page_idx":0,"para_blocks":[{"type":"text","lines":[{"spans":[{"type":"text","content":"An unrelated paper about protein folding and molecular dynamics."}]}]}]}]}`)
	if d, err = Parse(context.Background(), list); err != nil || fmt.Sprint(pages(d.Blocks)) != "[[1 1] [1 1] [2 2]]" {
		t.Fatalf("mismatched layout.json: %v err=%v", pages(d.Blocks), err)
	}
}

func TestCanonicalArtifactDirectories(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "a", "paper_content_list.json"), filepath.Join(dir, "a", "paper_content_list_v2.json"),
		filepath.Join(dir, "a", "paper_middle.json"), filepath.Join(dir, "a", "full.md"),
		filepath.Join(dir, "a", "images", "metadata.json"), filepath.Join(dir, "a", "paper.pdf"),
		filepath.Join(dir, "b", "rag-source.json"), filepath.Join(dir, "b", "content.md"),
		filepath.Join(dir, "note.md"),
	}
	got, err := CanonicalFiles(context.Background(), paths)
	if err != nil || len(got) != 3 || got[0] != paths[0] || got[1] != paths[6] {
		t.Fatalf("canonical=%v err=%v", got, err)
	}
	paths = append(paths, filepath.Join(dir, "a", "other_content_list.json"))
	if _, err := CanonicalFiles(context.Background(), paths); err == nil {
		t.Fatal("ambiguous multi-document artifact folder accepted")
	}
	current := []string{filepath.Join(dir, "middle_json.json"), filepath.Join(dir, "structured_content.json"), filepath.Join(dir, "markdown.md")}
	got, err = CanonicalFiles(context.Background(), current)
	if err != nil || len(got) != 1 || got[0] != current[1] {
		t.Fatalf("current MinerU package=%v err=%v", got, err)
	}
	got, err = CanonicalFiles(context.Background(), []string{paths[1], paths[2]})
	if err != nil || len(got) != 1 || got[0] != paths[2] {
		t.Fatalf("explicit MiddleJson pages should precede V2 order: %v err=%v", got, err)
	}
	// A manifest covers alternate exports in nested backend/backup directories.
	nested := []string{filepath.Join(dir, "rag-source.json"),
		filepath.Join(dir, "backup", "a_content_list.json"),
		filepath.Join(dir, "backup", "b_content_list.json"),
		filepath.Join(dir, "backup", "rag-source.json")}
	got, err = CanonicalFiles(context.Background(), nested)
	if err != nil || len(got) != 1 || got[0] != nested[0] {
		t.Fatalf("manifest directory was duplicated by nested exports: %v err=%v", got, err)
	}
}

func TestHTMLInlineContainersAndNestedLists(t *testing.T) {
	blocks, err := parseHTML(context.Background(), []byte(`<main><div>Signal <strong>channel</strong> evidence.</div><ul><li>first<ul><li>second</li></ul></li></ul></main>`))
	if err != nil || len(blocks) != 2 || blocks[0].Text != "Signal channel evidence." || blocks[1].Text != "first second" {
		t.Fatalf("inline/list grouping=%+v err=%v", blocks, err)
	}
}

func TestCancelledParsers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, parse := range map[string]func(context.Context, []byte) ([]model.Block, error){
		"html": parseHTML, "jats": ParseJATS, "mineru": ParseMinerU, "markdown": markdown,
	} {
		source := map[string]string{"html": "<p>x</p>", "jats": "<article><body><p>x</p></body></article>", "mineru": `[{"type":"text","text":"x"}]`, "markdown": "# x"}[name]
		if _, err := parse(ctx, []byte(source)); err == nil {
			t.Fatal("ignored cancellation", name)
		}
	}
}

func TestRagFixesCorrectPackageText(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "paper_content_list.json")
	writeDocument(t, path, `[{"type":"text","text":"pilot symbol \\nu_l here","page_idx":0},{"type":"text","text":"junk","page_idx":0}]`)
	before, err := Parse(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	stamp, _, _ := InputStamp(path)
	writeDocument(t, filepath.Join(dir, FixesFile), "# OCR\n\\nu_l\t\\nu_1\r\njunk\t\n")
	after, err := Parse(ctx, path)
	if err != nil || joined(after.Blocks) != `pilot symbol \nu_1 here` || len(after.Blocks) != 1 {
		t.Fatalf("blocks=%+v err=%v", after.Blocks, err)
	}
	if fp, _ := InputFingerprint(ctx, path); after.Hash == before.Hash || fp != after.Hash {
		t.Fatalf("hash %s fingerprint %s before %s", after.Hash, fp, before.Hash)
	}
	if next, _, _ := InputStamp(path); next == stamp {
		t.Fatal("stamp ignores fixes file")
	}
	writeDocument(t, filepath.Join(dir, FixesFile), "gone\tx\n")
	if _, err := Parse(ctx, path); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("stale fix accepted: %v", err)
	}
}
