package document

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Ownera1/rag-go/internal/model"
	"golang.org/x/net/html"
)

var htmlTag = regexp.MustCompile(`<[^>]*>`)

func IsMinerUFile(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return strings.HasSuffix(name, ".mineru.json") || strings.HasSuffix(name, "_content_list.json") || strings.HasSuffix(name, "_content_list_v2.json") || strings.HasSuffix(name, "_middle.json") || name == "content_list.json" || name == "content_list_v2.json" || name == "middle.json" || name == "middle_json.json" || name == "structured_content.json"
}
func number(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(string(n))
	return i, err == nil
}
func jsonText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var b strings.Builder
		for _, item := range x {
			b.WriteString(jsonText(item))
		}
		return b.String()
	case map[string]any:
		typ, _ := x["type"].(string)
		switch typ {
		case "", "text":
			return jsonText(x["content"])
		case "inline_equation", "equation_inline":
			if math := jsonText(x["content"]); math != "" {
				return " $" + math + "$ "
			}
		case "interline_equation", "equation_interline":
			if math := jsonText(x["content"]); math != "" {
				return " $$" + math + "$$ "
			}
		}
	}
	return ""
}

// linesText returns the span text of a middle JSON block's lines, one line
// per row, including nested blocks such as an algorithm's caption and body.
func linesText(item map[string]any) string {
	value := ""
	if lines, ok := item["lines"].([]any); ok {
		for _, line := range lines {
			if m, ok := line.(map[string]any); ok {
				value += jsonText(m["spans"]) + "\n"
			}
		}
	}
	if children, ok := item["blocks"].([]any); ok {
		for _, c := range children {
			if m, ok := c.(map[string]any); ok {
				value += linesText(m)
			}
		}
	}
	return value
}

// tableText keeps a table's rows as lines and its cells as " | "-separated
// columns, so a row still pairs a method with its values. Input without rows
// falls back to its bare text.
// ponytail: rowspan/colspan cells are not repeated, so a spanned column shifts left.
func tableText(s string) string {
	root, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return html.UnescapeString(htmlTag.ReplaceAllString(s, " "))
	}
	rows := []string{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			cells := []string{}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && (c.Data == "td" || c.Data == "th") {
					cells = append(cells, normalize(inlineHTML(c)))
				}
			}
			rows = append(rows, strings.Join(cells, " | "))
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	if len(rows) == 0 {
		return html.UnescapeString(htmlTag.ReplaceAllString(s, " "))
	}
	return strings.Join(rows, "\n")
}

// normalizeLines normalizes each line and drops blank ones, keeping a
// table's rows apart.
func normalizeLines(s string) string {
	lines := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l = normalize(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

// numberLabel matches a heading that is only a number such as "A." or "2.1.".
var numberLabel = regexp.MustCompile(`^(?:[0-9]+|[A-Z]|[IVXLC]+)(?:\.[0-9]+)*\.$`)

// sentenceHeading reports a heading that ends a sentence. MinerU tags run-in
// lines such as "Proof: See Appendix A." as headings, and every later block
// would inherit one as its section; real headings do not end with a period.
func sentenceHeading(s string) bool {
	return (strings.HasSuffix(s, ".") || strings.HasSuffix(s, "。")) && !numberLabel.MatchString(s)
}

// figureText returns a figure, chart or table's caption, footnote and, for a
// table, its rows. content_list v1 keeps these on the item, v2 under
// "content".
func figureText(item map[string]any, typ string) string {
	src := item
	if c, ok := item["content"].(map[string]any); ok {
		src = c
	}
	parts := []string{}
	add := func(v any) {
		if list, ok := v.([]any); ok {
			for _, x := range list {
				parts = append(parts, jsonText(x))
			}
		} else if s, ok := v.(string); ok {
			parts = append(parts, s)
		}
	}
	add(src[typ+"_caption"])
	if typ == "code" {
		add(src["code_body"])
		add(src["code_content"])
	}
	if typ == "table" {
		for _, key := range []string{"table_body", "html"} {
			if s, ok := src[key].(string); ok {
				add(tableText(s))
			}
		}
	}
	add(src[typ+"_footnote"])
	return strings.Join(parts, "\n")
}

func ParseMinerU(ctx context.Context, b []byte) ([]model.Block, error) {
	var root any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing MinerU JSON")
	}
	blocks := []model.Block{}
	levels := map[int]string{}
	add := func(item map[string]any, page *int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if raw, exists := item["page_idx"]; exists {
			n, ok := number(raw)
			if !ok || n < 0 || n > 1000000 {
				return errors.New("invalid MinerU page_idx")
			}
			p := n + 1
			page = &p
		}
		typ, _ := item["type"].(string)
		value := ""
		kind := ""
		level := 0
		content, _ := item["content"].(map[string]any)
		switch typ {
		case "text", "paragraph", "title", "doc_title", "paragraph_title":
			value = jsonText(item["text"])
			if value == "" {
				value = jsonText(item["content"])
			}
			if content != nil {
				key := "paragraph_content"
				if typ == "title" {
					key = "title_content"
				}
				value = jsonText(content[key])
				level, _ = number(content["level"])
			}
			if v, ok := number(item["text_level"]); ok {
				level = v
			}
			if v, ok := number(item["level"]); ok {
				level = v
			}
			if typ == "title" || typ == "doc_title" || typ == "paragraph_title" {
				if level < 1 {
					level = 1
				}
			}
			if value == "" {
				value = linesText(item)
			}
		case "list":
			if content != nil {
				if content["list_type"] == "reference_list" {
					return nil
				}
				if items, ok := content["list_items"].([]any); ok {
					for _, item := range items {
						if m, ok := item.(map[string]any); ok {
							value += jsonText(m["item_content"]) + "\n"
						}
					}
				}
			} else {
				if items, ok := item["list_items"].([]any); ok {
					for _, v := range items {
						value += jsonText(v) + "\n"
					}
				}
			}
		case "image", "chart", "table":
			// Pixels are not indexed; captions and table text are. A table
			// keeps its own block so its header and rows share a chunk.
			if typ == "table" {
				kind = "table"
			}
			value = figureText(item, typ)
			if strings.TrimSpace(value) == "" {
				value = linesText(item)
			}
		case "code", "algorithm":
			// Algorithm listings are body text in their own chunks.
			kind = "code"
			value = figureText(item, "code")
			if strings.TrimSpace(value) == "" {
				value = linesText(item)
			}
		case "equation", "equation_interline", "interline_equation":
			// Display equations join their prose; chunking keeps $$…$$ whole
			// and with the sentence that introduces it.
			value = jsonText(item["text"])
			if content != nil {
				if math := jsonText(content["math_content"]); math != "" {
					value = "$$" + math + "$$"
				}
			}
			if value == "" {
				value = linesText(item)
			}
		default:
			// References and page furniture (header, footer, page_number,
			// page_footnote) are skipped, as are types MinerU may add later.
			return nil
		}
		if typ == "table" {
			value = normalizeLines(value)
		} else {
			value = normalize(value)
		}
		if value == "" {
			return nil
		}
		if level > 0 && sentenceHeading(value) {
			level = 0
		}
		if level > 6 {
			return errors.New("MinerU heading level exceeds 6")
		}
		if level > 0 {
			for l := range levels {
				if l >= level {
					delete(levels, l)
				}
			}
			levels[level] = value
		}
		path := []string{}
		for l := 1; l <= 6; l++ {
			if title := levels[l]; title != "" {
				path = append(path, title)
			}
		}
		if inReferences(path) {
			return nil
		}
		blocks = append(blocks, model.Block{Text: value, Section: section(path), PageStart: page, PageEnd: page, Kind: kind})
		return nil
	}
	var list func([]any, *int) error
	list = func(items []any, page *int) error {
		for _, item := range items {
			switch v := item.(type) {
			case map[string]any:
				if err := add(v, page); err != nil {
					return err
				}
			case []any:
				if err := list(v, page); err != nil {
					return err
				}
			default:
				return errors.New("invalid MinerU content item")
			}
		}
		return nil
	}
	switch data := root.(type) {
	case []any:
		if err := list(data, nil); err != nil {
			return nil, err
		}
	case map[string]any:
		schema, _ := data["schema"].(string)
		if schema != "" && (schema != "docvortex.middle" || data["schema_version"] != "2.0") {
			return nil, fmt.Errorf("unsupported MinerU schema %q/version %v", schema, data["schema_version"])
		}
		pages, ok := data["pages"].([]any)
		legacy := false
		if !ok {
			pages, ok = data["pdf_info"].([]any)
			legacy = true
		}
		if !ok {
			return nil, errors.New("expected MinerU pages, pdf_info or content list")
		}
		seen := map[int]bool{}
		for _, raw := range pages {
			page, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("invalid MinerU page")
			}
			idx, ok := number(page["page_idx"])
			if !ok || idx < 0 || idx > 1000000 || seen[idx] {
				return nil, errors.New("missing, duplicate or invalid MinerU source page_idx")
			}
			seen[idx] = true
			p := idx + 1
			key := "blocks"
			if legacy {
				key = "para_blocks"
			}
			items, ok := page[key].([]any)
			if !ok {
				return nil, fmt.Errorf("MinerU page has no %s", key)
			}
			if err := list(items, &p); err != nil {
				return nil, err
			}
		}
	default:
		return nil, errors.New("invalid MinerU JSON root")
	}
	if err := validBlocks(blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}
