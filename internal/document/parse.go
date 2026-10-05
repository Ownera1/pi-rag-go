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
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/Ownera1/rag-go/internal/model"
)

const teiNS = "http://www.tei-c.org/ns/1.0"

var whitespace = regexp.MustCompile(`\s+`)

func ShortHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:])[:12] }

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
	abstract := []model.Block{}
	bodyCount := 0
	var abstractFallback strings.Builder
	stack := []xml.Name{}
	divs := []frame{}
	path := []string{}
	paragraphs := []string{}
	var pageStart, pageEnd, captureStart, captureEnd *int
	var capture strings.Builder
	capturing := ""
	captureDepth := 0
	skipDepth := 0
	inHeader, inAbstract, inBody, inBack := false, false, false, false
	rootChecked := false
	rootClosed := false
	elements := 0
	currentSection := func() *string { return section(path) }
	flush := func() {
		if len(paragraphs) > 0 {
			blocks = append(blocks, model.Block{Text: strings.Join(paragraphs, "\n\n"), Section: currentSection(), PageStart: pageStart, PageEnd: pageEnd})
			if inBody {
				bodyCount++
			}
			paragraphs = nil
			pageStart, pageEnd = nil, nil
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
			elements++
			if rootClosed || len(stack) >= 128 || elements > 200000 {
				return nil, errors.New("invalid TEI root or nesting depth")
			}
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
					captureStart, captureEnd = nil, nil
					for _, a := range t.Attr {
						if a.Name.Local == "coords" {
							captureStart, captureEnd, err = coordinatePages(a.Value)
							if err != nil {
								return nil, err
							}
						}
					}
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
			if len(stack) == 0 && strings.TrimSpace(string(t)) != "" {
				return nil, errors.New("TEI text outside document root")
			}
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
								sec := "Abstract"
								if len(abstract) > 0 && samePage(abstract[len(abstract)-1].PageStart, captureStart) && samePage(abstract[len(abstract)-1].PageEnd, captureEnd) {
									abstract[len(abstract)-1].Text += "\n\n" + value
								} else {
									abstract = append(abstract, model.Block{Text: value, Section: &sec, PageStart: captureStart, PageEnd: captureEnd})
								}
							} else {
								if !samePage(pageStart, captureStart) || !samePage(pageEnd, captureEnd) {
									flush()
								}
								pageStart, pageEnd = captureStart, captureEnd
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
							sec := "Abstract"
							abstract = append(abstract, model.Block{Text: v, Section: &sec})
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
			if len(stack) == 0 {
				rootClosed = true
			}
		}
	}
	if !rootChecked {
		return nil, errors.New("Invalid TEI XML: missing root")
	}
	if bodyCount == 0 {
		return nil, errors.New("TEI has no indexable body paragraphs")
	}
	if len(abstract) > 0 {
		blocks = append(abstract, blocks...)
	}
	return blocks, nil
}

func samePage(a, b *int) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// GROBID coordinates use physical, one-based PDF page numbers. XML line
// numbers and printed journal pagination are deliberately not substituted.
func coordinatePages(coords string) (*int, *int, error) {
	if strings.TrimSpace(coords) == "" {
		return nil, nil, nil
	}
	first, last := 0, 0
	for _, box := range strings.Split(coords, ";") {
		parts := strings.Split(box, ",")
		if len(parts) != 5 {
			return nil, nil, errors.New("invalid TEI coordinates")
		}
		page, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || page < 1 {
			return nil, nil, errors.New("invalid TEI coordinate page")
		}
		for _, value := range parts[1:] {
			n, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
				return nil, nil, errors.New("invalid TEI coordinate box")
			}
		}
		if first == 0 || page < first {
			first = page
		}
		if page > last {
			last = page
		}
	}
	return &first, &last, nil
}
