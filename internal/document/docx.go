package document

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/Ownera1/pi-rag-go/internal/model"
)

func wordNS(space string) bool {
	return space == "http://schemas.openxmlformats.org/wordprocessingml/2006/main" || space == "http://purl.oclc.org/ooxml/wordprocessingml/main"
}

func parseDOCX(ctx context.Context, b []byte) ([]model.Block, error) {
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}
	if len(z.File) > 10000 {
		return nil, errors.New("DOCX contains too many ZIP entries")
	}
	read := func(name string) ([]byte, error) {
		var file *zip.File
		for _, f := range z.File {
			if f.Name == name {
				if file != nil {
					return nil, errors.New("duplicate DOCX XML entry")
				}
				file = f
			}
		}
		if file == nil {
			return nil, nil
		}
		if file.UncompressedSize64 > MaxDocumentBytes {
			return nil, errors.New("DOCX XML exceeds size limit")
		}
		f, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, MaxDocumentBytes+1))
		if len(data) > MaxDocumentBytes {
			return nil, errors.New("DOCX XML exceeds size limit")
		}
		return data, err
	}
	data, err := read("word/document.xml")
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("DOCX is missing word/document.xml")
	}
	root, err := xmlTree(ctx, data)
	if err != nil {
		return nil, err
	}
	if root.name.Local != "document" || !wordNS(root.name.Space) {
		return nil, errors.New("invalid WordprocessingML document")
	}
	body := root.child("body")
	if body == nil {
		return nil, errors.New("DOCX is missing body")
	}
	styles := map[string]int{}
	if data, err = read("word/styles.xml"); err != nil {
		return nil, err
	} else if len(data) > 0 {
		s, err := xmlTree(ctx, data)
		if err != nil {
			return nil, err
		}
		for _, style := range s.children() {
			if p := style.child("pPr"); p != nil {
				if outline := p.child("outlineLvl"); outline != nil {
					level, e := strconv.Atoi(outline.attr("val"))
					if e == nil && level >= 0 && level < 6 {
						styles[style.attr("styleId")] = level + 1
					}
				}
			}
		}
	}
	blocks := []model.Block{}
	levels := map[int]string{}
	var text func(*xmlNode) string
	text = func(n *xmlNode) string {
		if !wordNS(n.name.Space) || n.name.Local == "del" || n.name.Local == "drawing" || n.name.Local == "instrText" {
			return ""
		}
		if n.name.Local == "r" {
			if pr := n.child("rPr"); pr != nil {
				if hidden := pr.child("vanish"); hidden != nil {
					value := strings.ToLower(hidden.attr("val"))
					if value != "false" && value != "0" && value != "off" {
						return ""
					}
				}
			}
		}
		if n.name.Local == "t" {
			return n.text(nil)
		}
		if n.name.Local == "tab" {
			return "\t"
		}
		if n.name.Local == "br" {
			return "\n"
		}
		var s strings.Builder
		for _, c := range n.children() {
			s.WriteString(text(c))
		}
		return s.String()
	}
	var walk func(*xmlNode) error
	walk = func(n *xmlNode) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n.name.Local == "del" || n.name.Local == "drawing" {
			return nil
		}
		if n.name.Local == "p" && wordNS(n.name.Space) {
			value := normalize(text(n))
			if value == "" {
				return nil
			}
			level := 0
			if pr := n.child("pPr"); pr != nil {
				if style := pr.child("pStyle"); style != nil {
					key := style.attr("val")
					level = styles[key]
					lower := strings.ToLower(key)
					if level == 0 && strings.HasPrefix(lower, "heading") {
						level, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(lower, "heading")))
					}
				}
				if outline := pr.child("outlineLvl"); outline != nil {
					if v, e := strconv.Atoi(outline.attr("val")); e == nil && v >= 0 && v < 6 {
						level = v + 1
					}
				}
			}
			if level >= 1 && level <= 6 {
				for l := range levels {
					if l >= level {
						delete(levels, l)
					}
				}
				levels[level] = value
			}
			path := []string{}
			for l := 1; l <= 6; l++ {
				if v := levels[l]; v != "" {
					path = append(path, v)
				}
			}
			blocks = append(blocks, model.Block{Text: value, Section: section(path)})
			return nil
		}
		for _, c := range n.children() {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err = walk(body); err != nil {
		return nil, err
	}
	if err = validBlocks(blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}
