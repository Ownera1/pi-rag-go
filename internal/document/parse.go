package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Ownera1/pi-rag-go/internal/model"
)

const teiNS = "http://www.tei-c.org/ns/1.0"

var whitespace = regexp.MustCompile(`\s+`)
var heading = regexp.MustCompile(`^(#{1,6}) (.*)$`)

func ShortHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:])[:12] }

func Parse(ctx context.Context, path string) (model.Document, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return model.Document{}, err
	}
	d := model.Document{Path: path, Hash: ShortHash(string(b)), Size: int64(len(b))}
	if strings.HasSuffix(strings.ToLower(path), ".tei.xml") {
		d.Format = "grobid-tei"
		d.Blocks, err = ParseTEI(ctx, b)
		return d, err
	}
	text := string(b)
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".md" || ext == ".mdx" {
		d.Blocks = markdown(text)
	} else if strings.TrimSpace(text) != "" {
		lines := strings.Split(text, "\n")
		start, end := 1, max(1, len(lines))
		d.Blocks = []model.Block{{Text: text, LineStart: &start, LineEnd: &end}}
	}
	return d, nil
}

func markdown(text string) []model.Block {
	lines := strings.Split(text, "\n")
	type start struct {
		line    int
		section *string
	}
	starts := []start{}
	if !heading.MatchString(lines[0]) {
		starts = append(starts, start{line: 1})
	}
	for i, line := range lines {
		if m := heading.FindStringSubmatch(line); m != nil {
			s := strings.TrimSpace(m[2])
			starts = append(starts, start{line: i + 1, section: &s})
		}
	}
	blocks := []model.Block{}
	for i, p := range starts {
		end := len(lines)
		if i+1 < len(starts) {
			end = starts[i+1].line - 1
		}
		raw := strings.Join(lines[p.line-1:end], "\n")
		if strings.TrimSpace(raw) != "" {
			a, b := p.line, end
			blocks = append(blocks, model.Block{Text: raw, Section: p.section, LineStart: &a, LineEnd: &b})
		}
	}
	if len(blocks) == 0 && strings.TrimSpace(text) != "" {
		a, b := 1, len(lines)
		return []model.Block{{Text: text, LineStart: &a, LineEnd: &b}}
	}
	return blocks
}

func normalize(s string) string { return strings.TrimSpace(whitespace.ReplaceAllString(s, " ")) }

func section(path []string) *string {
	p := []string{}
	for _, v := range path {
		if v != "" {
			p = append(p, v)
		}
	}
	if len(p) == 0 {
		return nil
	}
	s := strings.Join(p, " / ")
	return &s
}

type frame struct {
	heading string
	skip    bool
	inBack  bool
}

func ParseTEI(ctx context.Context, b []byte) ([]model.Block, error) {
	if bytes.Contains(bytes.ToUpper(b), []byte("<!DOCTYPE")) {
		return nil, errors.New("TEI DOCTYPE is not supported")
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	blocks := []model.Block{}
	abstract := []string{}
	bodyCount := 0
	var abstractFallback strings.Builder
	stack := []xml.Name{}
	divs := []frame{}
	path := []string{}
	paragraphs := []string{}
	var capture strings.Builder
	capturing := ""
	captureDepth := 0
	skipDepth := 0
	inHeader, inAbstract, inBody, inBack := false, false, false, false
	rootChecked := false
	currentSection := func() *string { return section(path) }
	flush := func() {
		if len(paragraphs) > 0 {
			blocks = append(blocks, model.Block{Text: strings.Join(paragraphs, "\n\n"), Section: currentSection()})
			if inBody {
				bodyCount++
			}
			paragraphs = nil
		}
	}
	skipped := func() bool {
		for _, d := range divs {
			if d.skip {
				return true
			}
		}
		return false
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("Invalid TEI XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !rootChecked {
				rootChecked = true
				if t.Name.Local != "TEI" || t.Name.Space != teiNS {
					return nil, errors.New("Expected a TEI root in the TEI namespace")
				}
			}
			stack = append(stack, t.Name)
			if skipDepth > 0 {
				skipDepth++
				continue
			}
			if t.Name.Space != teiNS {
				continue
			}
			switch t.Name.Local {
			case "teiHeader":
				inHeader = true
			case "abstract":
				if inHeader {
					inAbstract = true
					abstractFallback.Reset()
				}
			case "body":
				inBody = true
			case "back":
				inBack = true
			case "div":
				if inBody || inBack {
					flush()
					typ := ""
					for _, a := range t.Attr {
						if a.Name.Local == "type" {
							typ = strings.ToLower(a.Value)
						}
					}
					skip := false
					if inBody {
						skip = typ == "references" || typ == "bibliography" || typ == "acknowledgement" || typ == "acknowledgments"
					}
					if inBack && len(divs) == 0 {
						skip = typ != "appendix" && typ != "annex"
					}
					divs = append(divs, frame{skip: skip, inBack: inBack})
					path = append(path, "")
				}
			case "p":
				if (inAbstract || inBody || inBack) && !skipped() {
					capturing = "p"
					captureDepth = len(stack)
					capture.Reset()
				}
			case "head":
				if len(divs) > 0 && !skipped() {
					capturing = "head"
					captureDepth = len(stack)
					capture.Reset()
				}
			case "formula", "figure", "table", "note":
				skipDepth = 1
			case "lb", "pb":
				if capturing != "" {
					capture.WriteByte(' ')
				}
			}
		case xml.CharData:
			if skipDepth == 0 {
				if capturing != "" {
					capture.Write([]byte(t))
				}
				if inAbstract {
					abstractFallback.Write([]byte(t))
				}
			}
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errors.New("Invalid TEI XML: unmatched end")
			}
			if skipDepth > 0 {
				skipDepth--
				stack = stack[:len(stack)-1]
				continue
			}
			if t.Name.Space == teiNS {
				if capturing != "" && len(stack) == captureDepth && t.Name.Local == capturing {
					value := normalize(capture.String())
					if value != "" {
						if capturing == "p" {
							if inAbstract {
								abstract = append(abstract, value)
							} else {
								paragraphs = append(paragraphs, value)
							}
						} else if len(divs) > 0 {
							divs[len(divs)-1].heading = value
							path[len(path)-1] = value
							if strings.HasPrefix(strings.ToLower(value), "references") || strings.HasPrefix(strings.ToLower(value), "bibliography") {
								divs[len(divs)-1].skip = true
							}
						}
					}
					capturing = ""
				}
				switch t.Name.Local {
				case "abstract":
					inAbstract = false
					if len(abstract) == 0 {
						if v := normalize(abstractFallback.String()); v != "" {
							abstract = append(abstract, v)
						}
					}
				case "teiHeader":
					inHeader = false
				case "div":
					if (inBody || inBack) && len(divs) > 0 {
						flush()
						divs = divs[:len(divs)-1]
						path = path[:len(path)-1]
					}
				case "body":
					flush()
					inBody = false
				case "back":
					flush()
					inBack = false
				}
			}
			stack = stack[:len(stack)-1]
		}
	}
	if !rootChecked {
		return nil, errors.New("Invalid TEI XML: missing root")
	}
	if bodyCount == 0 {
		return nil, errors.New("TEI has no indexable body paragraphs")
	}
	if len(abstract) > 0 {
		s := "Abstract"
		blocks = append([]model.Block{{Text: strings.Join(abstract, "\n\n"), Section: &s}}, blocks...)
	}
	return blocks, nil
}
