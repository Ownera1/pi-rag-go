package document

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Ownera1/rag-go/internal/model"
)

// pageAnchor is the length of the skeleton windows that locate a block in the
// page-bearing export; pageWindow bounds how far past the previous match one
// is searched for, so a phrase repeated later in the paper cannot pull the
// alignment ahead. pageSlack is how far an edit may shift the next window
// within a block.
const pageAnchor, pageWindow, pageSlack = 24, 20000, 200

// minPageMatch is the share of blocks that must be found in the export.
// Below it the export is likely another document's.
const minPageMatch = 0.7

// skeleton keeps lower-cased letters and digits, so MinerU's JSON and Markdown
// renderings of the same text compare equal despite markup and spacing.
func skeleton(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(htmlTag.ReplaceAllString(s, "")) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// alignPages returns the pages of each block's text in ref, the same
// document's page-bearing text in reading order, or {0, 0} for a block it
// cannot locate. Blocks are located by exact skeleton windows, so a hand edit
// breaks only the windows it touches.
func alignPages(blocks, ref []model.Block) ([][2]int, error) {
	var text strings.Builder
	pages := []int{}
	for _, b := range ref {
		if b.PageStart == nil {
			continue
		}
		s := skeleton(b.Text)
		text.WriteString(s)
		for range len(s) {
			pages = append(pages, *b.PageStart)
		}
	}
	if len(pages) == 0 {
		return nil, errors.New("page export has no page numbers")
	}
	src := text.String()
	out := make([][2]int, len(blocks))
	cursor, eligible, matched := 0, 0, 0
	for i := range blocks {
		s := skeleton(blocks[i].Text)
		if len(s) < pageAnchor {
			continue
		}
		eligible++
		first, last, hits, at := -1, -1, 0, cursor
		for o := 0; o+pageAnchor <= len(s); o += pageAnchor {
			// Once the block is located, later windows must follow it
			// closely; only an edit may shift them.
			end := at + pageWindow
			if first >= 0 {
				end = at + pageAnchor + pageSlack
			}
			n := strings.Index(src[at:min(len(src), end)], s[o:o+pageAnchor])
			if n < 0 {
				continue
			}
			if first < 0 {
				first = at + n
			}
			last, at, hits = at+n+pageAnchor-1, at+n+pageAnchor, hits+1
		}
		// Text the export lacks can match a later passage by chance: a
		// location needs two agreeing windows, or a one-window block close
		// to the previous match.
		if hits >= 2 || hits == 1 && len(s) < 2*pageAnchor && first-cursor <= pageSlack {
			out[i] = [2]int{pages[first], pages[last]}
			matched++
			cursor = at
		}
	}
	if eligible > 0 && float64(matched) < minPageMatch*float64(eligible) {
		return nil, fmt.Errorf("page export matches %d of %d paragraphs; is it this document's export?", matched, eligible)
	}
	return out, nil
}

// withPages splits Markdown blocks into paragraphs and gives each the pages of
// its text in ref. A paragraph that cannot be located spans the pages of its
// located neighbours. chunk.Merge rejoins paragraphs on the same pages.
func withPages(blocks, ref []model.Block) ([]model.Block, error) {
	paras := []model.Block{}
	for _, b := range blocks {
		lines := strings.Split(b.Text, "\n")
		start := -1
		for i := 0; i <= len(lines); i++ {
			if i < len(lines) && strings.TrimSpace(lines[i]) != "" {
				if start < 0 {
					start = i
				}
				continue
			}
			if start >= 0 {
				p := b
				p.Text = strings.Join(lines[start:i], "\n")
				a, z := *b.LineStart+start, *b.LineStart+i-1
				p.LineStart, p.LineEnd = &a, &z
				paras = append(paras, p)
				start = -1
			}
		}
	}
	found, err := alignPages(paras, ref)
	if err != nil {
		return nil, err
	}
	for i := range paras {
		if found[i][0] > 0 {
			a, z := found[i][0], found[i][1]
			paras[i].PageStart, paras[i].PageEnd = &a, &z
		}
	}
	for i := range paras {
		if found[i][0] > 0 {
			continue
		}
		var prev, next *int
		for j := i - 1; j >= 0 && prev == nil; j-- {
			if found[j][0] > 0 {
				prev = paras[j].PageEnd
			}
		}
		for j := i + 1; j < len(paras) && next == nil; j++ {
			if found[j][0] > 0 {
				next = paras[j].PageStart
			}
		}
		if prev == nil {
			prev = next
		}
		if next == nil || *next < *prev {
			next = prev
		}
		paras[i].PageStart, paras[i].PageEnd = prev, next
	}
	return paras, nil
}

// pageRef reads a MinerU export as page-bearing text for alignPages. A legacy
// MiddleJson (MinerU Desktop's layout.json) is read span by span: MinerU moves
// the lines of a paragraph continuing onto the next page to the paragraph's
// first block and marks their spans cross_page, so those spans are given the
// next page. Its inline math and algorithm listings, which content lists
// omit, are kept as well. Other exports are parsed as ParseMinerU does.
func pageRef(ctx context.Context, b []byte) ([]model.Block, error) {
	var root struct {
		PDFInfo []struct {
			PageIdx    *int             `json:"page_idx"`
			ParaBlocks []map[string]any `json:"para_blocks"`
		} `json:"pdf_info"`
	}
	if !bytes.HasPrefix(bytes.TrimSpace(b), []byte("{")) || json.Unmarshal(b, &root) != nil || root.PDFInfo == nil {
		return ParseMinerU(ctx, b)
	}
	ref := []model.Block{}
	var walk func(block map[string]any, page int)
	walk = func(block map[string]any, page int) {
		lines, _ := block["lines"].([]any)
		for _, l := range lines {
			line, _ := l.(map[string]any)
			spans, _ := line["spans"].([]any)
			for _, sp := range spans {
				span, _ := sp.(map[string]any)
				text, _ := span["content"].(string)
				if h, ok := span["html"].(string); ok {
					text += " " + h
				}
				p := page
				if cross, _ := span["cross_page"].(bool); cross {
					p++
				}
				if strings.TrimSpace(text) != "" {
					ref = append(ref, model.Block{Text: text, PageStart: &p, PageEnd: &p})
				}
			}
		}
		children, _ := block["blocks"].([]any)
		for _, c := range children {
			if child, ok := c.(map[string]any); ok {
				walk(child, page)
			}
		}
	}
	for _, pg := range root.PDFInfo {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if pg.PageIdx == nil || *pg.PageIdx < 0 || *pg.PageIdx > 1000000 {
			return nil, errors.New("invalid MinerU page_idx")
		}
		for _, block := range pg.ParaBlocks {
			walk(block, *pg.PageIdx+1)
		}
	}
	return ref, nil
}

// pageSource returns the legacy MiddleJson beside a MinerU export, whose
// cross-page marks correct the export's page numbers, or "" when none exists.
func pageSource(path string) string {
	dir := filepath.Dir(path)
	entries, _ := os.ReadDir(dir)
	middle := ""
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() {
			continue
		}
		if name == "layout.json" {
			return filepath.Join(dir, name)
		}
		if middle == "" && (name == "middle.json" || strings.HasSuffix(name, "_middle.json")) {
			middle = filepath.Join(dir, name)
		}
	}
	return middle
}

// minerUPages returns pageSource for a MinerU export and "" for any other
// file, without listing the directory of every indexed file.
func minerUPages(path string) string {
	if !IsMinerUFile(path) {
		return ""
	}
	return pageSource(path)
}

// repage moves MinerU blocks to the pages alignPages finds for them in the
// sibling MiddleJson, so a paragraph continuing onto the next page spans
// both. Blocks it cannot locate keep their own pages; an export that does not
// match is ignored, since the blocks' own pages remain valid.
func repage(ctx context.Context, blocks []model.Block, middle []byte) ([]model.Block, error) {
	ref, err := pageRef(ctx, middle)
	if err != nil {
		return blocks, ctx.Err()
	}
	found, err := alignPages(blocks, ref)
	if err != nil {
		return blocks, ctx.Err()
	}
	for i := range blocks {
		if found[i][0] > 0 {
			a, z := found[i][0], found[i][1]
			blocks[i].PageStart, blocks[i].PageEnd = &a, &z
		}
	}
	return blocks, ctx.Err()
}
