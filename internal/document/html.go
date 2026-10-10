package document

import (
	"bytes"
	"context"
	"strings"

	"github.com/Ownera1/rag-go/internal/model"
	"golang.org/x/net/html"
)

var excludedHTML = map[string]bool{"head": true, "script": true, "style": true, "nav": true, "footer": true, "aside": true, "form": true, "button": true, "noscript": true, "svg": true, "math": true}
var blockHTML = map[string]bool{
	"p": true, "li": true, "pre": true, "blockquote": true, "dt": true, "dd": true,
	"div": true, "section": true, "article": true, "main": true, "ul": true, "ol": true,
	"table": true, "tr": true, "td": true, "th": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
}

func hiddenHTML(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if excludedHTML[n.Data] {
		return true
	}
	for _, a := range n.Attr {
		if a.Key == "hidden" || (a.Key == "aria-hidden" && a.Val == "true") {
			return true
		}
	}
	return false
}
func inlineHTML(n *html.Node) string {
	if hiddenHTML(n) {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	if n.Data == "br" {
		return "\n"
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if b.Len() > 0 && c.Type == html.ElementNode && blockHTML[c.Data] {
			b.WriteByte('\n')
		}
		b.WriteString(inlineHTML(c))
		if c.Type == html.ElementNode && blockHTML[c.Data] {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func hasHTMLBlocks(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if hiddenHTML(c) {
			continue
		}
		if (c.Type == html.ElementNode && blockHTML[c.Data]) || hasHTMLBlocks(c) {
			return true
		}
	}
	return false
}
func parseHTML(ctx context.Context, b []byte) ([]model.Block, error) {
	root, err := html.Parse(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	var main, article, body *html.Node
	var choose func(*html.Node)
	choose = func(n *html.Node) {
		if hiddenHTML(n) {
			return
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "main":
				if main == nil {
					main = n
				}
			case "article":
				if article == nil {
					article = n
				}
			case "body":
				body = n
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			choose(c)
		}
	}
	choose(root)
	if main != nil {
		root = main
	} else if article != nil {
		root = article
	} else if body != nil {
		root = body
	}
	blocks := []model.Block{}
	levels := headings{}
	current := func() *string { return section(levels.path()) }
	var walk func(*html.Node) error
	walk = func(n *html.Node) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if hiddenHTML(n) {
			return nil
		}
		if n.Type == html.ElementNode && len(n.Data) == 2 && n.Data[0] == 'h' && n.Data[1] >= '1' && n.Data[1] <= '6' {
			level := int(n.Data[1] - '0')
			levels.open(level, normalize(inlineHTML(n)))
			return nil
		}
		switch n.Data {
		case "p", "li", "pre", "blockquote", "dt", "dd":
			text := inlineHTML(n)
			if n.Data != "pre" {
				text = normalize(text)
			} else {
				text = strings.TrimSpace(text)
			}
			if text != "" {
				blocks = append(blocks, model.Block{Text: text, Section: current()})
			}
			return nil
		}
		if n.Type == html.ElementNode && !hasHTMLBlocks(n) {
			if text := normalize(inlineHTML(n)); text != "" {
				blocks = append(blocks, model.Block{Text: text, Section: current()})
			}
			return nil
		}
		if n.Type == html.TextNode {
			if text := normalize(n.Data); text != "" {
				blocks = append(blocks, model.Block{Text: text, Section: current()})
			}
			return nil
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err = walk(root); err != nil {
		return nil, err
	}
	if err = validBlocks(blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}
