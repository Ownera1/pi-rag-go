package document

import (
	"context"
	"strings"

	"github.com/Ownera1/pi-rag-go/internal/model"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

func markdown(ctx context.Context, source []byte) ([]model.Block, error) {
	tree := goldmark.New().Parser().Parse(text.NewReader(source))
	type boundary struct {
		offset  int
		section *string
	}
	starts := []boundary{{offset: 0}}
	levels := map[int]string{}
	err := ast.Walk(tree, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if err := ctx.Err(); err != nil {
			return ast.WalkStop, err
		}
		h, ok := n.(*ast.Heading)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		if h.Lines().Len() == 0 {
			return ast.WalkSkipChildren, nil
		}
		offset := h.Lines().At(0).Start
		for offset > 0 && source[offset-1] != '\n' {
			offset--
		}
		for level := range levels {
			if level >= h.Level {
				delete(levels, level)
			}
		}
		levels[h.Level] = strings.TrimSpace(string(h.Text(source)))
		path := []string{}
		for level := 1; level <= 6; level++ {
			if title := levels[level]; title != "" {
				path = append(path, title)
			}
		}
		sec := section(path)
		if offset == 0 {
			starts[0].section = sec
		} else {
			starts = append(starts, boundary{offset, sec})
		}
		return ast.WalkSkipChildren, nil
	})
	if err != nil {
		return nil, err
	}
	blocks := []model.Block{}
	for i, start := range starts {
		end := len(source)
		if i+1 < len(starts) {
			end = starts[i+1].offset
		}
		raw := strings.TrimRight(string(source[start.offset:end]), "\r\n")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		a := 1 + strings.Count(string(source[:start.offset]), "\n")
		z := a + strings.Count(raw, "\n")
		blocks = append(blocks, model.Block{Text: raw, Section: start.section, LineStart: &a, LineEnd: &z})
	}
	return blocks, nil
}
