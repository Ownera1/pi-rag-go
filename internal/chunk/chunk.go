package chunk

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Ownera1/rag-go/internal/model"
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
		if isCJK(r) {
			cjk++
		} else {
			other++
		}
	}
	return estimate(cjk, other)
}

func estimate(cjk, other int) int { return max(1, cjk+(other+3)/4) }

func isCJK(r rune) bool {
	return (r >= 0x3400 && r <= 0x9fff) || (r >= 0xf900 && r <= 0xfaff) || (r >= 0x3040 && r <= 0x30ff) || (r >= 0xac00 && r <= 0xd7af)
}

// Version identifies chunk boundary behavior for the processing fingerprint.
const Version = "merged-blocks-v1"

func sameInt(a, b *int) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

func sameString(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// Merge joins consecutive blocks that share a section and page range, so
// paragraph-level exports (MinerU, DOCX, HTML, JATS) chunk to target size
// instead of one chunk per paragraph. Chunks never gain a wider page range.
// Line-numbered blocks join only across exactly one blank line, which keeps
// line arithmetic on the joined text exact.
func Merge(blocks []model.Block) []model.Block {
	out := []model.Block{}
	for _, b := range blocks {
		if strings.TrimSpace(b.Text) == "" {
			continue
		}
		if n := len(out); n > 0 {
			last := &out[n-1]
			lines := last.LineStart == nil && b.LineStart == nil
			if last.LineStart != nil && last.LineEnd != nil && b.LineStart != nil && b.LineEnd != nil {
				lines = *b.LineStart == *last.LineEnd+2 && strings.Count(last.Text, "\n") == *last.LineEnd-*last.LineStart
			}
			if lines && last.Kind == b.Kind && sameString(last.Section, b.Section) && sameInt(last.PageStart, b.PageStart) && sameInt(last.PageEnd, b.PageEnd) {
				last.Text += "\n\n" + b.Text
				last.LineEnd = b.LineEnd
				continue
			}
		}
		out = append(out, b)
	}
	return out
}

func newChunk(text string, b model.Block, index, start, end int) model.Chunk {
	return model.Chunk{
		Content:    text,
		LineStart:  start,
		LineEnd:    end,
		PageStart:  b.PageStart,
		PageEnd:    b.PageEnd,
		Section:    b.Section,
		ChunkIndex: index,
	}
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

func Legacy(blocks []model.Block, configs ...model.ChunkingConfig) []model.Chunk {
	cfg := model.DefaultChunking()
	if len(configs) > 0 {
		cfg = configs[0]
	}
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
		for _, part := range splitOversized(text, cfg.LegacyMax) {
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
		if cfg.LegacyOverlap > 0 && strings.TrimSpace(added) != "" {
			r := []rune(buf)
			keep := min(len(r), cfg.LegacyOverlap*2)
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
		if buf != "" && Estimate(joined(buf, piece)) > cfg.LegacyMax {
			flush()
			for buf != "" && Estimate(joined(buf, piece)) > cfg.LegacyMax {
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
		if Estimate(buf) > cfg.LegacyMax {
			push(buf)
			reset()
		} else if Estimate(buf) >= cfg.LegacyTarget {
			flush()
		}
	}
	for _, b := range blocks {
		if strings.TrimSpace(b.Text) == "" {
			continue
		}
		sectionChanged := (section == nil) != (b.Section == nil) || (section != nil && b.Section != nil && *section != *b.Section)
		if buf != "" && (sectionChanged || (b.PageStart != nil && pageEnd != nil && *b.PageStart > *pageEnd)) {
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
			for _, p := range splitOversized(piece, cfg.LegacyMax) {
				appendPiece(p, b, ls, le)
			}
		}
	}
	flush()
	return chunks
}

type unit struct{ start, end int }

func splitUnits(r []rune, unitMax int) []unit {
	all := []unit{}
	add := func(from, to int) {
		for from < to {
			// Any span over 4*unitMax runes exceeds unitMax, which bounds the
			// search on long unpunctuated lines such as minified code.
			n := min(to, from+4*unitMax+4)
			if n < to || Estimate(string(r[from:n])) > unitMax {
				lo, hi, best := from+1, n, from+1
				for lo <= hi {
					m := (lo + hi) / 2
					if Estimate(string(r[from:m])) <= unitMax {
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

// semanticChunk takes lines, the newline count of r[:from], so it scans only
// the chunk itself.
func semanticChunk(b model.Block, r []rune, from, to, index, lines int) model.Chunk {
	raw := string(r[from:to])
	left := len([]rune(raw)) - len([]rune(strings.TrimLeftFunc(raw, unicode.IsSpace)))
	trimmed := strings.TrimSpace(raw)
	start := from + left
	end := start + utf8.RuneCountInString(trimmed)
	ls, le := 0, 0
	if b.LineStart != nil {
		ls = *b.LineStart + lines + strings.Count(string(r[from:start]), "\n")
		le = ls + strings.Count(string(r[start:max(start, end-1)]), "\n")
	}
	return newChunk(trimmed, b, index, ls, le)
}

func Semantic(ctx context.Context, blocks []model.Block, provider model.EmbeddingProvider, configs ...model.Config) ([]model.Chunk, error) {
	cfg := model.DefaultConfig()
	if len(configs) > 0 {
		cfg = configs[0]
	}
	if provider == nil {
		return nil, errors.New("embedding provider unavailable")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	type split struct {
		block model.Block
		r     []rune
		units []unit
		first int // index of the block's first unit vector in all
	}
	splits := []split{}
	texts := []string{}
	for _, b := range blocks {
		r := []rune(b.Text)
		units := splitUnits(r, cfg.Chunking.SemanticUnitMax)
		if len(units) == 0 {
			continue
		}
		splits = append(splits, split{b, r, units, len(texts)})
		for _, u := range units {
			texts = append(texts, strings.TrimSpace(string(r[u.start:u.end])))
		}
	}
	// Batch units across blocks so short blocks share requests.
	all := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += cfg.Indexing.EmbeddingBatchSize {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		batch := texts[i:min(i+cfg.Indexing.EmbeddingBatchSize, len(texts))]
		v, e := provider.EmbedDocuments(ctx, batch)
		if e != nil {
			return nil, e
		}
		if len(v) != len(batch) {
			return nil, errors.New("semantic embedding count mismatch")
		}
		for _, x := range v {
			if len(x) != provider.Dimensions() {
				return nil, errors.New("semantic embedding dimension mismatch")
			}
		}
		all = append(all, v...)
	}
	chunks := []model.Chunk{}
	for _, sp := range splits {
		b, r, units := sp.block, sp.r, sp.units
		vectors := all[sp.first : sp.first+len(units)]
		// Counts at unit boundaries give every span estimate in O(1); rescanning
		// the remaining text per cursor was quadratic in the block length.
		type count struct{ cjk, other, lines int }
		starts, ends := make([]count, len(units)), make([]count, len(units))
		var c count
		pos := 0
		advance := func(to int) count {
			for ; pos < to; pos++ {
				if isCJK(r[pos]) {
					c.cjk++
				} else {
					c.other++
				}
				if r[pos] == '\n' {
					c.lines++
				}
			}
			return c
		}
		for i, u := range units {
			starts[i], ends[i] = advance(u.start), advance(u.end)
		}
		span := func(i, j int) int { // Estimate(r[units[i].start:units[j].end])
			return estimate(ends[j].cjk-starts[i].cjk, ends[j].other-starts[i].other)
		}
		last := len(units) - 1
		for cursor := 0; cursor < len(units); {
			if e := ctx.Err(); e != nil {
				return nil, e
			}
			if span(cursor, last) <= cfg.Chunking.SemanticTarget {
				chunks = append(chunks, semanticChunk(b, r, units[cursor].start, units[last].end, len(chunks), starts[cursor].lines))
				break
			}
			bestEnd, furthest := -1, cursor
			bestScore, bestDistance := math.Inf(1), math.MaxInt
			for end := cursor; end < len(units); end++ {
				tokens := span(cursor, end)
				if tokens > cfg.Chunking.SemanticMax {
					break
				}
				furthest = end
				if tokens < cfg.Chunking.SemanticMin || end == last {
					continue
				}
				if span(cursor, last) <= cfg.Chunking.SemanticMax && span(end+1, last) < cfg.Chunking.SemanticMin {
					continue
				}
				score := cosine(vectors[end], vectors[end+1])
				distance := tokens - cfg.Chunking.SemanticTarget
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
			chunks = append(chunks, semanticChunk(b, r, units[cursor].start, units[end].end, len(chunks), starts[cursor].lines))
			cursor = end + 1
		}
	}
	return chunks, nil
}
