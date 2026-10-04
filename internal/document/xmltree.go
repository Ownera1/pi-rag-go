package document

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"regexp"
	"strings"
)

type xmlPart struct {
	text string
	node *xmlNode
}
type xmlNode struct {
	name  xml.Name
	attrs []xml.Attr
	parts []xmlPart
}

var namedEntity = regexp.MustCompile(`&([A-Za-z][A-Za-z0-9]+);`)

func (n *xmlNode) attr(name string) string {
	for _, a := range n.attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
func (n *xmlNode) children() []*xmlNode {
	out := []*xmlNode{}
	for _, p := range n.parts {
		if p.node != nil {
			out = append(out, p.node)
		}
	}
	return out
}
func (n *xmlNode) child(name string) *xmlNode {
	for _, c := range n.children() {
		if c.name.Local == name {
			return c
		}
	}
	return nil
}
func (n *xmlNode) text(skip map[string]bool) string {
	if skip[n.name.Local] {
		return ""
	}
	var b strings.Builder
	for _, p := range n.parts {
		if p.node != nil {
			b.WriteString(p.node.text(skip))
		} else {
			b.WriteString(p.text)
		}
	}
	return b.String()
}
func xmlTree(ctx context.Context, b []byte) (*xmlNode, error) {
	if bytes.Contains(bytes.ToUpper(b), []byte("<!ENTITY")) {
		return nil, errors.New("XML entity declarations are not supported")
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	// Resolve common scientific character entities locally. Never fetch an
	// external DTD or expand user-defined entities from a declaration.
	dec.Entity = map[string]string{}
	for _, match := range namedEntity.FindAllSubmatch(b, -1) {
		raw := string(match[0])
		if value := html.UnescapeString(raw); value != raw {
			dec.Entity[string(match[1])] = value
		}
	}
	stack := []*xmlNode{}
	var root *xmlNode
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			count++
			if len(stack) >= 128 || count > 200000 {
				return nil, errors.New("XML structure exceeds parser limits")
			}
			node := &xmlNode{name: t.Name, attrs: t.Attr}
			if len(stack) == 0 {
				if root != nil {
					return nil, errors.New("multiple XML roots")
				}
				root = node
			} else {
				parent := stack[len(stack)-1]
				parent.parts = append(parent.parts, xmlPart{node: node})
			}
			stack = append(stack, node)
		case xml.CharData:
			if len(stack) > 0 {
				n := stack[len(stack)-1]
				n.parts = append(n.parts, xmlPart{text: string(t)})
			} else if strings.TrimSpace(string(t)) != "" {
				return nil, errors.New("text outside XML root")
			}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		}
	}
	if root == nil {
		return nil, errors.New("missing XML root")
	}
	return root, nil
}
