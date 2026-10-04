package document

import (
	"context"
	"errors"
	"strings"

	"github.com/Ownera1/pi-rag-go/internal/model"
)

var excludedJATS = map[string]bool{"ref-list": true, "ack": true, "fn": true, "fn-group": true, "fig": true, "table-wrap": true, "table": true, "disp-formula": true, "inline-formula": true, "math": true, "supplementary-material": true}

func ParseJATS(ctx context.Context, b []byte) ([]model.Block, error) {
	root, err := xmlTree(ctx, b)
	if err != nil {
		return nil, err
	}
	if root.name.Local != "article" {
		return nil, errors.New("expected JATS article root")
	}
	body := root.child("body")
	if body == nil {
		return nil, errors.New("JATS has no body")
	}
	blocks := []model.Block{}
	var walk func(*xmlNode, []string) error
	walk = func(n *xmlNode, path []string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if excludedJATS[n.name.Local] {
			return nil
		}
		if n.name.Local == "sec" {
			if title := n.child("title"); title != nil {
				path = append(append([]string{}, path...), normalize(title.text(excludedJATS)))
			}
		}
		if n.name.Local == "p" {
			if text := normalize(n.text(excludedJATS)); text != "" {
				blocks = append(blocks, model.Block{Text: text, Section: section(path)})
			}
			return nil
		}
		for _, child := range n.children() {
			if err := walk(child, path); err != nil {
				return err
			}
		}
		return nil
	}
	if front := root.child("front"); front != nil {
		if meta := front.child("article-meta"); meta != nil {
			for _, n := range meta.children() {
				if n.name.Local == "abstract" {
					if err = walk(n, []string{"Abstract"}); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	abstractCount := len(blocks)
	if err = walk(body, nil); err != nil {
		return nil, err
	}
	if len(blocks) == abstractCount {
		return nil, errors.New("JATS contains no indexable body paragraphs")
	}
	// Section paths remain exact labels; no PDF page is inferred from fpage/lpage.
	for i := range blocks {
		blocks[i].Text = strings.TrimSpace(blocks[i].Text)
	}
	return blocks, nil
}
