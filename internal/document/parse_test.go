package document

import (
	"context"
	"strings"
	"testing"
)

func TestTEISectionsAndExclusions(t *testing.T) {
	xml := `<tei:TEI xmlns:tei="http://www.tei-c.org/ns/1.0"><tei:teiHeader><tei:profileDesc><tei:abstract>` +
		`<tei:p>Abstract <tei:hi>signal</tei:hi> result.</tei:p></tei:abstract></tei:profileDesc></tei:teiHeader><tei:text>` +
		`<tei:body><tei:div><tei:head>IV Proposed Method</tei:head><tei:p>Parent first <tei:ref>[1]</tei:ref>.</tei:p>` +
		`<tei:p>Parent second paragraph.</tei:p><tei:div><tei:head>B Channel Estimation</tei:head>` +
		`<tei:p>Child pilot data.</tei:p><tei:p>Child second <tei:formula>E=mc2</tei:formula> end.</tei:p></tei:div>` +
		`<tei:p>Parent final paragraph.</tei:p></tei:div><tei:figure><tei:head>Figure excluded</tei:head></tei:figure>` +
		`</tei:body><tei:back><tei:div type="references"><tei:p>Reference excluded</tei:p></tei:div>` +
		`<tei:div type="appendix"><tei:head>Appendix A Proof</tei:head><tei:p>Appendix derivation.</tei:p></tei:div>` +
		`</tei:back></tei:text></tei:TEI>`
	blocks, e := ParseTEI(context.Background(), []byte(xml))
	if e != nil {
		t.Fatal(e)
	}
	want := []string{"Abstract", "IV Proposed Method", "IV Proposed Method / B Channel Estimation", "IV Proposed Method", "Appendix A Proof"}
	if len(blocks) != len(want) {
		t.Fatalf("blocks=%+v", blocks)
	}
	for i, b := range blocks {
		if b.Section == nil || *b.Section != want[i] {
			t.Fatalf("section %d = %v", i, b.Section)
		}
		if b.PageStart != nil || b.LineStart != nil {
			t.Fatal("invented source position")
		}
	}
	if blocks[1].Text != "Parent first [1].\n\nParent second paragraph." {
		t.Fatal(blocks[1].Text)
	}
	combined := ""
	for _, b := range blocks {
		combined += b.Text
	}
	for _, v := range []string{"E=mc2", "Reference excluded", "Figure excluded"} {
		if strings.Contains(combined, v) {
			t.Fatalf("included %s", v)
		}
	}
}

func TestTEIInvalid(t *testing.T) {
	for _, x := range []string{`<!DOCTYPE TEI><TEI/>`, `<root xmlns="http://www.tei-c.org/ns/1.0"/>`, `<TEI xmlns="http://www.tei-c.org/ns/1.0"><text><body/></text></TEI>`} {
		if _, e := ParseTEI(context.Background(), []byte(x)); e == nil {
			t.Fatalf("accepted %s", x)
		}
	}
}

func TestTEIMixedNamespaceExclusions(t *testing.T) {
	x := `<TEI xmlns="http://www.tei-c.org/ns/1.0" xmlns:m="http://www.w3.org/1998/Math/MathML"><text><body><div><head>Method</head><p>before ` +
		`<formula><m:math><m:mi>x</m:mi><m:mi>FORMULA_LEAK</m:mi></m:math></formula>` +
		`<note><m:math><m:mi>x</m:mi><m:mi>NOTE_LEAK</m:mi></m:math></note> after.</p>` +
		`<figure><m:math><m:mi>x</m:mi><m:mi>FIGURE_LEAK</m:mi></m:math><p>FIGURE_BODY</p></figure>` +
		`<p>final evidence.</p></div></body></text></TEI>`
	blocks, err := ParseTEI(context.Background(), []byte(x))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Text != "before after.\n\nfinal evidence." {
		t.Fatalf("excluded subtrees leaked or removed body: %+v", blocks)
	}
}
