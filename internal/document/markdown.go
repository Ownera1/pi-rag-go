package document

import (
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
	for i, start := range starts {
		if start.skip {
			continue
		}
		end := len(source)
		if i+1 < len(starts) {
			end = starts[i+1].offset
		}
		first := 1 + strings.Count(string(source[:start.offset]), "\n")
		lines := strings.Split(string(source[start.offset:end]), "\n")
		// Display equations ($$ lines) get their own blocks: LaTeX merged
		// into prose dilutes its embedding.
		kinds := make([]string, len(lines))
		math := false
		for j := range lines {
			lines[j] = mdLine(lines[j])
			t := strings.TrimSpace(lines[j])
			if math {
				kinds[j] = "equation"
				math = !strings.Contains(t, "$$")
			} else if strings.HasPrefix(t, "$$") {
				kinds[j] = "equation"
				math = len(t) < 4 || !strings.HasSuffix(t, "$$")
			}
		}
		// Each block is a run of one kind, trimmed of blank lines at its ends.
		s, last := -1, -1
		flush := func() {
			if s >= 0 {
				a, z := first+s, first+last
				blocks = append(blocks, model.Block{Text: strings.Join(lines[s:last+1], "\n"), Section: start.section, LineStart: &a, LineEnd: &z, Kind: kinds[s]})
			}
			s = -1
		}
		for j, line := range lines {
			if kinds[j] == "" && strings.TrimSpace(line) == "" {
				continue
			}
			if s >= 0 && kinds[s] != kinds[j] {
				flush()
			}
			if s < 0 {
				s = j
			}
			last = j
		}
		flush()
	}
	return blocks, nil
}
