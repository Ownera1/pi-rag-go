package chunk

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Ownera1/pi-rag-go/internal/model"
)

const (
	LegacyTarget    = 180
	LegacyMax       = 240
	LegacyOverlap   = 30
	SemanticMin     = 120
	SemanticTarget  = 280
	SemanticMax     = 420
	SemanticUnitMax = 140
)

func Estimate(s string) int {
	cjk, other := 0, 0
	for _, r := range s {
		if (r >= 0x3400 && r <= 0x9fff) || (r >= 0xf900 && r <= 0xfaff) || (r >= 0x3040 && r <= 0x30ff) || (r >= 0xac00 && r <= 0xd7af) {
			cjk++
		} else {
			other++
		}
	}
	return max(1, cjk+(other+3)/4)
}
func newChunk(text string, b model.Block, index, start, end int) model.Chunk {
	return model.Chunk{Content: text, LineStart: start, LineEnd: end, PageStart: b.PageStart, PageEnd: b.PageEnd, Section: b.Section, ChunkIndex: index}
}
func line(b model.Block, offset int) int {
	if b.LineStart == nil {
		return 0
	}
	return *b.LineStart + strings.Count(b.Text[:offset], "\n")
}
func spans(text string) []string {
	parts := []string{}
	var start int
	for i := 0; i < len(text); {
		if text[i] == '\n' {
			j := i
			for j < len(text) && text[j] == '\n' {
				j++
			}
			if j-i >= 2 {
				parts = append(parts, text[start:i])
				start = j
			}
			i = j
		} else {
			i++
		}
	}
	parts = append(parts, text[start:])
	return parts
}
func hardSplit(text string, maxTokens int) []string {
	r := []rune(text)
	out := []string{}
	for len(r) > 0 {
		lo, hi, best := 1, len(r), 1
		for lo <= hi {
			mid := (lo + hi) / 2
			if Estimate(string(r[:mid])) <= maxTokens {
				best = mid
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		out = append(out, string(r[:best]))
		r = r[best:]
	}
	return out
}
func splitOversized(s string, maxTokens int) []string {
	if Estimate(s) <= maxTokens {
		return []string{s}
	}
	r := []rune(s)
	parts := []string{}
	start := 0
	for i := 0; i < len(r); {
		if r[i] == '\n' {
			if i > start {
				parts = append(parts, string(r[start:i]))
			}
			for i < len(r) && r[i] == '\n' {
				i++
			}
			start = i
			continue
		}
		if strings.ContainsRune("。！？.!?", r[i]) && i+1 < len(r) && unicode.IsSpace(r[i+1]) {
			if i+1 > start {
				parts = append(parts, string(r[start:i+1]))
			}
			j := i + 1
			for j < len(r) && unicode.IsSpace(r[j]) {
				j++
			}
			start = j
			i = j
			continue
		}
		i++
	}
	if start < len(r) {
		parts = append(parts, string(r[start:]))
	}
	out := []string{}
	buf := ""
	push := func() {
		if buf == "" {
			return
		}
		if Estimate(buf) <= maxTokens {
			out = append(out, buf)
		} else {
			out = append(out, hardSplit(buf, maxTokens)...)
		}
		buf = ""
	}
	for _, part := range parts {
		if part == "" {
			continue
		}
		if Estimate(part) > maxTokens {
			push()
			out = append(out, hardSplit(part, maxTokens)...)
			continue
		}
		if buf == "" {
			buf = part
			continue
		}
		joined := buf + " " + part
		if Estimate(joined) > maxTokens {
			push()
			buf = part
		} else {
			buf = joined
		}
	}
	push()
	if len(out) == 0 {
		return hardSplit(s, maxTokens)
	}
	return out
}
func Legacy(blocks []model.Block) []model.Chunk {
	chunks := []model.Chunk{}
	buf, added := "", ""
	start, end := 0, 0
	var pageStart, pageEnd *int
	var section *string
	reset := func() { buf, added = "", ""; start, end = 0, 0; pageStart, pageEnd = nil, nil }
	push := func(text string) {
		text = strings.TrimRightFunc(text, unicode.IsSpace)
		if text == "" {
			return
		}
		for _, part := range splitOversized(text, LegacyMax) {
			c := model.Chunk{Content: part, LineStart: start, LineEnd: end, PageStart: pageStart, PageEnd: pageEnd, Section: section, ChunkIndex: len(chunks)}
			chunks = append(chunks, c)
		}
	}
	flush := func() {
		if strings.TrimSpace(buf) == "" {
			return
		}
		if strings.TrimSpace(added) != "" {
			push(buf)
		}
		if LegacyOverlap > 0 && strings.TrimSpace(added) != "" {
			r := []rune(buf)
			keep := min(len(r), LegacyOverlap*2)
			buf = string(r[len(r)-keep:])
			added = ""
			if end > 0 {
				start = max(1, end-strings.Count(buf, "\n"))
			}
		} else {
			reset()
		}
	}
	appendPiece := func(piece string, b model.Block, ls, le int) {
		joined := func(base, p string) string {
			if base == "" {
				return p
			}
			return base + "\n\n" + p
		}
		if buf != "" && Estimate(joined(buf, piece)) > LegacyMax {
			flush()
			for buf != "" && Estimate(joined(buf, piece)) > LegacyMax {
				r := []rune(buf)
				if len(r) <= 1 {
					reset()
					break
				}
				buf = string(r[(len(r)+1)/2:])
			}
		}
		if buf == "" {
			start = ls
			pageStart = b.PageStart
			section = b.Section
		}
		buf = joined(buf, piece)
		added = joined(added, piece)
		if le > 0 {
			end = le
		}
		if b.PageStart != nil && pageStart == nil {
			pageStart = b.PageStart
		}
		if b.PageEnd != nil {
			pageEnd = b.PageEnd
		}
		if Estimate(buf) > LegacyMax {
			push(buf)
			reset()
		} else if Estimate(buf) >= LegacyTarget {
			flush()
		}
	}
	for _, b := range blocks {
		if strings.TrimSpace(b.Text) == "" {
			continue
		}
		if buf != "" && b.PageStart != nil && pageEnd != nil && *b.PageStart > *pageEnd {
			flush()
			reset()
		}
		cursor := 0
		for _, part := range spans(b.Text) {
			pos := strings.Index(b.Text[cursor:], part) + cursor
			if pos < cursor {
				pos = cursor
			}
			cursor = pos + len(part)
			piece := strings.TrimSpace(part)
			if piece == "" {
				continue
			}
			ls, le := 0, 0
			if b.LineStart != nil {
				ls = line(b, pos)
				le = ls + strings.Count(piece, "\n")
			}
			for _, p := range splitOversized(piece, LegacyMax) {
				appendPiece(p, b, ls, le)
			}
		}
	}
	flush()
	return chunks
}

type unit struct{ start, end int }

func splitUnits(r []rune) []unit {
	all := []unit{}
	add := func(from, to int) {
		for from < to {
			n := to
			if Estimate(string(r[from:n])) > SemanticUnitMax {
				lo, hi, best := from+1, to, from+1
				for lo <= hi {
					m := (lo + hi) / 2
					if Estimate(string(r[from:m])) <= SemanticUnitMax {
						best = m
						lo = m + 1
					} else {
						hi = m - 1
					}
				}
				n = best
				space := -1
				for j := best - 1; j >= from; j-- {
					if r[j] == ' ' {
						space = j
						break
					}
				}
				if space > from+(best-from)/2 {
					n = space + 1
				}
			}
			if strings.TrimSpace(string(r[from:n])) != "" {
				all = append(all, unit{from, n})
			}
			from = n
		}
	}
	start := 0
	for i := 0; i < len(r); {
		if r[i] == '\n' {
			j := i
			for j < len(r) && r[j] == '\n' {
				j++
			}
			add(start, j)
			start = j
			i = j
			continue
		}
		if strings.ContainsRune(".!?。！？", r[i]) && i+1 < len(r) && unicode.IsSpace(r[i+1]) {
			j := i + 1
			for j < len(r) && unicode.IsSpace(r[j]) && r[j] != '\n' {
				j++
			}
			add(start, j)
			start = j
			i = j
			continue
		}
		i++
	}
	if start < len(r) {
		add(start, len(r))
	}
	return all
}
func cosine(a, b []float32) float64 {
	var dot, aa, bb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		aa += float64(a[i]) * float64(a[i])
		bb += float64(b[i]) * float64(b[i])
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return dot / math.Sqrt(aa*bb)
}
func semanticChunk(b model.Block, r []rune, from, to, index int) model.Chunk {
	raw := string(r[from:to])
	left := len([]rune(raw)) - len([]rune(strings.TrimLeftFunc(raw, unicode.IsSpace)))
	trimmed := strings.TrimSpace(raw)
	start := from + left
	end := start + utf8.RuneCountInString(trimmed)
	ls, le := 0, 0
	if b.LineStart != nil {
		ls = *b.LineStart + strings.Count(string(r[:start]), "\n")
		le = *b.LineStart + strings.Count(string(r[:max(start, end-1)]), "\n")
	}
	return newChunk(trimmed, b, index, ls, le)
}
func Semantic(ctx context.Context, blocks []model.Block, provider model.EmbeddingProvider) ([]model.Chunk, error) {
	chunks := []model.Chunk{}
	for _, b := range blocks {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		r := []rune(b.Text)
		units := splitUnits(r)
		if len(units) == 0 {
			continue
		}
		vectors := make([][]float32, 0, len(units))
		for i := 0; i < len(units); i += 64 {
			texts := []string{}
			for _, u := range units[i:min(i+64, len(units))] {
				texts = append(texts, strings.TrimSpace(string(r[u.start:u.end])))
			}
			v, e := provider.EmbedDocuments(ctx, texts)
			if e != nil {
				return nil, e
			}
			if len(v) != len(texts) {
				return nil, errors.New("semantic embedding count mismatch")
			}
			for _, x := range v {
				if len(x) != provider.Dimensions() {
					return nil, errors.New("semantic embedding dimension mismatch")
				}
			}
			vectors = append(vectors, v...)
		}
		for cursor := 0; cursor < len(units); {
			if e := ctx.Err(); e != nil {
				return nil, e
			}
			remaining := string(r[units[cursor].start:units[len(units)-1].end])
			if Estimate(remaining) <= SemanticTarget {
				chunks = append(chunks, semanticChunk(b, r, units[cursor].start, units[len(units)-1].end, len(chunks)))
				break
			}
			bestEnd, furthest := -1, cursor
			bestScore, bestDistance := math.Inf(1), math.MaxInt
			for end := cursor; end < len(units); end++ {
				tokens := Estimate(string(r[units[cursor].start:units[end].end]))
				if tokens > SemanticMax {
					break
				}
				furthest = end
				if tokens < SemanticMin || end == len(units)-1 {
					continue
				}
				if Estimate(remaining) <= SemanticMax && Estimate(string(r[units[end+1].start:units[len(units)-1].end])) < SemanticMin {
					continue
				}
				score := cosine(vectors[end], vectors[end+1])
				distance := tokens - SemanticTarget
				if distance < 0 {
					distance = -distance
				}
				if score < bestScore-1e-12 || (math.Abs(score-bestScore) <= 1e-12 && distance < bestDistance) {
					bestScore, bestDistance, bestEnd = score, distance, end
				}
			}
			end := furthest
			if bestEnd >= cursor {
				end = bestEnd
			}
			chunks = append(chunks, semanticChunk(b, r, units[cursor].start, units[end].end, len(chunks)))
			cursor = end + 1
		}
	}
	return chunks, nil
}
