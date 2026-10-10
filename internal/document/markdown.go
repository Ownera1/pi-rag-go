package document

import (
	"bytes"
	"context"
	"regexp"
	"strings"

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"golang.org/x/net/html"
)

var mdImage = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)

// mdLine drops what is not text from one line without changing the line
// count: an image becomes its alt text and an HTML table its cell text.
func mdLine(line string) string {
	line = mdImage.ReplaceAllString(strings.TrimSuffix(line, "\r"), "$1")
	// ponytail: one-line tables only, as MinerU writes them; cell structure is flattened.
	if t := strings.TrimSpace(line); strings.HasPrefix(t, "<") && strings.Contains(t, "<table") {
		line = normalize(html.UnescapeString(htmlTag.ReplaceAllString(line, " ")))
	}
	return line
}

func markdown(ctx context.Context, source []byte) ([]model.Block, error) {
	tree := goldmark.New().Parser().Parse(text.NewReader(source))
	type boundary struct {
		offset  int
		section *string
		skip    bool
	}
	starts := []boundary{{offset: 0}}
	levels := headings{}
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
		levels.open(h.Level, strings.TrimSpace(string(h.Text(source))))
		path := levels.path()
		b := boundary{offset, section(path), inReferences(path)}
		if offset == 0 {
			starts[0] = b
		} else {
			starts = append(starts, b)
		}
		return ast.WalkSkipChildren, nil
	})
	if err != nil {
		return nil, err
	}
	blocks := []model.Block{}
	// Sections start in document order, so lines are counted once overall.
	first, counted := 1, 0
	for i, start := range starts {
		if start.skip {
			continue
		}
		end := len(source)
		if i+1 < len(starts) {
			end = starts[i+1].offset
		}
		first += bytes.Count(source[counted:start.offset], []byte("\n"))
		counted = start.offset
		lines := strings.Split(string(source[start.offset:end]), "\n")
		// One block per section, trimmed of blank lines at its ends.
		s, last := -1, -1
		for j := range lines {
			lines[j] = mdLine(lines[j])
			if strings.TrimSpace(lines[j]) != "" {
				if s < 0 {
					s = j
				}
				last = j
			}
		}
		if s >= 0 {
			a, z := first+s, first+last
			blocks = append(blocks, model.Block{Text: strings.Join(lines[s:last+1], "\n"), Section: start.section, LineStart: &a, LineEnd: &z})
		}
	}
	return blocks, nil
}
