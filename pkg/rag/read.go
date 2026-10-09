package rag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Ownera1/rag-go/internal/document"
	"github.com/Ownera1/rag-go/internal/model"
)

// DocumentInfo identifies an indexed document. ID prefixes its chunk ids and
// Version changes whenever its indexed content does.
type DocumentInfo struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Version string `json:"version"`
	// PDF is the original PDF beside the document, when one is found.
	PDF string `json:"pdf,omitempty"`
}

// ReadOptions selects a contiguous range of one document's chunks: Around a
// chunk id with Before/After neighbours, a From/To chunk index range, or the
// chunks on Pages ("8" or "8-9"). No locator reads from the start.
type ReadOptions struct {
	Document  string `json:"document,omitempty" jsonschema:"document id, path, or a unique part of its path or title; optional with around"`
	Around    string `json:"around,omitempty" jsonschema:"chunk id from a hit, such as d34e11184e03-62"`
	Before    *int   `json:"before,omitempty" jsonschema:"chunks before around (default 2)"`
	After     *int   `json:"after,omitempty" jsonschema:"chunks after around (default 2)"`
	From      *int   `json:"from,omitempty" jsonschema:"first chunk index, inclusive"`
	To        *int   `json:"to,omitempty" jsonschema:"last chunk index, inclusive"`
	Pages     string `json:"pages,omitempty" jsonschema:"physical PDF page or range, such as 8 or 8-9"`
	MaxTokens int    `json:"max_tokens,omitempty" jsonschema:"token budget (default 4000, max 16000)"`
	// Version, when set, must match the document's current version.
	Version string `json:"version,omitempty" jsonschema:"fail if the document's version differs"`
}

// Passage is one chunk in document order. Section is set where it changes.
type Passage struct {
	Index     int    `json:"index"`
	PageStart *int   `json:"pageStart,omitempty"`
	PageEnd   *int   `json:"pageEnd,omitempty"`
	Section   string `json:"section,omitempty"`
	Content   string `json:"content"`
	// Images are figure files whose captions this passage contains.
	Images []string `json:"images,omitempty"`
}

// ReadResult holds the passages read. When Truncated, Next is the index to
// continue from.
type ReadResult struct {
	DocumentInfo
	Chunks    int       `json:"chunks"`
	Passages  []Passage `json:"passages"`
	Truncated bool      `json:"truncated"`
	Next      *int      `json:"next,omitempty"`
}

// Section is a run of consecutive chunks sharing a section; N tells apart
// runs with the same name.
type Section struct {
	N         int    `json:"n"`
	Section   string `json:"section"`
	From      int    `json:"from"`
	To        int    `json:"to"`
	PageStart *int   `json:"pageStart,omitempty"`
	PageEnd   *int   `json:"pageEnd,omitempty"`
}

type Outline struct {
	DocumentInfo
	Chunks   int       `json:"chunks"`
	Pages    int       `json:"pages,omitempty"`
	Sections []Section `json:"sections"`
}

const (
	defaultReadTokens = 4000
	maxReadTokens     = 16000
	defaultNeighbours = 2
)

// Documents lists indexed documents with their ids and titles.
func (c *Core) Documents(ctx context.Context) ([]DocumentInfo, error) {
	s, err := c.operation(ctx, false)
	if err != nil {
		return nil, err
	}
	defer s.close()
	docs, err := s.documentInfos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DocumentInfo, len(docs))
	for i, d := range docs {
		out[i] = d.info
	}
	return out, nil
}

// Read returns a range of one document's chunks in document order, within a
// token budget. It reads the index only: no sync and no model calls.
func (c *Core) Read(ctx context.Context, opts ReadOptions) (ReadResult, error) {
	s, err := c.operation(ctx, false)
	if err != nil {
		return ReadResult{}, err
	}
	defer s.close()
	ref := opts.Document
	if ref == "" {
		ref, _, _ = strings.Cut(opts.Around, "-")
	}
	doc, chunks, err := s.documentChunks(ctx, ref)
	if err != nil {
		return ReadResult{}, err
	}
	out := ReadResult{DocumentInfo: doc, Chunks: len(chunks), Passages: []Passage{}}
	if opts.Version != "" && opts.Version != doc.Version {
		return out, fmt.Errorf("document %s changed: version %s is now %s; repeat the query or outline for current chunk ids", doc.ID, opts.Version, doc.Version)
	}
	budget := opts.MaxTokens
	if budget == 0 {
		budget = defaultReadTokens
	}
	if budget < 1 || budget > maxReadTokens {
		return out, fmt.Errorf("max_tokens must be between 1 and %d", maxReadTokens)
	}
	lo, hi, err := readRange(doc, chunks, opts)
	if err != nil {
		return out, err
	}
	tokens, section, prefix := 0, "", titlePrefix(chunks)
	for i := lo; i <= hi; i++ {
		ch := chunks[i]
		tokens += ch.Tokens
		if tokens > budget && len(out.Passages) > 0 {
			out.Truncated, out.Next = true, &i
			break
		}
		p := Passage{Index: ch.ChunkIndex, PageStart: ch.PageStart, PageEnd: ch.PageEnd, Content: ch.Content}
		if sec := strings.TrimPrefix(deref(ch.Section), prefix); sec != section || i == lo {
			p.Section, section = sec, sec
		}
		out.Passages = append(out.Passages, p)
	}
	if len(out.Passages) > 0 {
		// Best effort: a source that no longer parses leaves passages as
		// indexed, without images.
		texts := make([]string, len(chunks))
		for i, ch := range chunks {
			texts[i] = ch.Content
		}
		images, _ := document.FigureChunks(ctx, doc.Path, texts)
		for i := range out.Passages {
			out.Passages[i].Images = images[out.Passages[i].Index]
		}
	}
	return out, ctx.Err()
}

// FixOptions replaces Wrong, text as Read shows it, with Right, the text the
// original PDF has.
type FixOptions struct {
	Document string `json:"document" jsonschema:"document id, path, or a unique part of its path or title"`
	Wrong    string `json:"wrong" jsonschema:"the wrong text exactly as rag_read shows it, within one line, occurring once in the document"`
	Right    string `json:"right" jsonschema:"the text as the original PDF page or image shows it"`
	Version  string `json:"version,omitempty" jsonschema:"fail if the document's version differs"`
}

// FixResult names the corrections file and the line the pair went to.
type FixResult struct {
	DocumentInfo
	File string `json:"file"`
	Line int    `json:"line"`
}

// Fix records a correction in the document's rag-fixes.tsv. It does not sync,
// so chunk ids stay stable while reading; the next query reindexes the
// document once for all corrections made meanwhile.
func (c *Core) Fix(ctx context.Context, opts FixOptions) (FixResult, error) {
	s, err := c.operation(ctx, true)
	if err != nil {
		return FixResult{}, err
	}
	defer s.close()
	if s.db == nil {
		return FixResult{}, errors.New("no index yet; run rag sync")
	}
	d, err := s.resolveDocument(ctx, opts.Document)
	if err != nil {
		return FixResult{}, err
	}
	out := FixResult{DocumentInfo: d.info}
	if opts.Version != "" && opts.Version != d.info.Version {
		return out, fmt.Errorf("document %s changed: version %s is now %s; read the passage again", d.info.ID, opts.Version, d.info.Version)
	}
	out.File, out.Line, err = document.AddFix(ctx, d.info.Path, opts.Wrong, opts.Right)
	return out, err
}

// Outline lists a document's sections as chunk ranges for Read.
func (c *Core) Outline(ctx context.Context, document string) (Outline, error) {
	s, err := c.operation(ctx, false)
	if err != nil {
		return Outline{}, err
	}
	defer s.close()
	doc, chunks, err := s.documentChunks(ctx, document)
	if err != nil {
		return Outline{}, err
	}
	out := Outline{DocumentInfo: doc, Chunks: len(chunks), Sections: []Section{}}
	seen, prefix := map[string]int{}, titlePrefix(chunks)
	for i, ch := range chunks {
		if ch.PageEnd != nil {
			out.Pages = max(out.Pages, *ch.PageEnd)
		}
		sec := strings.TrimPrefix(deref(ch.Section), prefix)
		if n := len(out.Sections); n > 0 && out.Sections[n-1].Section == sec {
			last := &out.Sections[n-1]
			last.To = i
			if ch.PageEnd != nil {
				last.PageEnd = ch.PageEnd
				if last.PageStart == nil {
					last.PageStart = ch.PageStart
				}
			}
			continue
		}
		seen[sec]++
		out.Sections = append(out.Sections, Section{N: seen[sec], Section: sec, From: i, To: i, PageStart: ch.PageStart, PageEnd: ch.PageEnd})
	}
	return out, nil
}

// titlePrefix returns "<heading> / " when every section sits under one top
// heading, usually the paper title, so outline and read drop that repeat; the
// title's own section keeps its name.
func titlePrefix(chunks []model.Chunk) string {
	top, nested := "", false
	for _, ch := range chunks {
		head, _, sub := strings.Cut(deref(ch.Section), " / ")
		if head == "" {
			continue
		}
		if top == "" {
			top = head
		} else if head != top {
			return ""
		}
		nested = nested || sub
	}
	if top == "" || !nested {
		return ""
	}
	return top + " / "
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// readRange resolves the locator of opts to an inclusive chunk index range.
func readRange(doc DocumentInfo, chunks []model.Chunk, opts ReadOptions) (int, int, error) {
	n := len(chunks)
	locators := 0
	for _, set := range []bool{opts.Around != "", opts.From != nil || opts.To != nil, opts.Pages != ""} {
		if set {
			locators++
		}
	}
	if locators > 1 {
		return 0, 0, errors.New("use one of around, from/to or pages")
	}
	if opts.Around == "" && (opts.Before != nil || opts.After != nil) {
		return 0, 0, errors.New("before/after require around")
	}
	switch {
	case opts.Around != "":
		prefix, suffix, _ := strings.Cut(opts.Around, "-")
		i, err := strconv.Atoi(suffix)
		if prefix != doc.ID || err != nil || i < 0 {
			return 0, 0, fmt.Errorf("around %q is not a chunk id of document %s (ids look like %s-12)", opts.Around, doc.ID, doc.ID)
		}
		if i >= n {
			return 0, 0, fmt.Errorf("chunk %s no longer exists: document %s has chunks 0-%d; it may have changed, so repeat the query", opts.Around, doc.ID, n-1)
		}
		before, after := defaultNeighbours, defaultNeighbours
		if opts.Before != nil {
			before = *opts.Before
		}
		if opts.After != nil {
			after = *opts.After
		}
		if before < 0 || after < 0 {
			return 0, 0, errors.New("before/after must not be negative")
		}
		return max(0, i-before), min(n-1, i+after), nil
	case opts.Pages != "":
		first, last, err := pageRange(opts.Pages)
		if err != nil {
			return 0, 0, err
		}
		lo, hi, minPage, maxPage := -1, -1, 0, 0
		for i, ch := range chunks {
			if ch.PageStart == nil || ch.PageEnd == nil {
				continue
			}
			if minPage == 0 {
				minPage = *ch.PageStart
			}
			maxPage = max(maxPage, *ch.PageEnd)
			if *ch.PageEnd >= first && *ch.PageStart <= last {
				if lo < 0 {
					lo = i
				}
				hi = i
			}
		}
		if maxPage == 0 {
			return 0, 0, fmt.Errorf("document %s has no page numbers; read by chunk range with from/to", doc.ID)
		}
		if lo < 0 {
			return 0, 0, fmt.Errorf("document %s has no chunks on pages %s; it spans pages %d-%d", doc.ID, opts.Pages, minPage, maxPage)
		}
		return lo, hi, nil
	default:
		lo, hi := 0, n-1
		if opts.From != nil {
			lo = *opts.From
		}
		if opts.To != nil {
			hi = *opts.To
		}
		if lo < 0 || hi >= n || lo > hi {
			return 0, 0, fmt.Errorf("chunk range %d-%d is outside document %s, which has chunks 0-%d", lo, hi, doc.ID, n-1)
		}
		return lo, hi, nil
	}
}

func pageRange(s string) (int, int, error) {
	a, b, found := strings.Cut(strings.TrimSpace(s), "-")
	first, err := strconv.Atoi(strings.TrimSpace(a))
	last := first
	if err == nil && found {
		last, err = strconv.Atoi(strings.TrimSpace(b))
	}
	if err != nil || first < 1 || last < first {
		return 0, 0, fmt.Errorf("pages %q must be a page such as 8 or a range such as 8-9", s)
	}
	return first, last, nil
}

type documentEntry struct {
	info DocumentInfo
	key  string
	rel  string
	src  string
}

// documentInfos lists indexed documents, titled from Zotero when linked.
func (s *session) documentInfos(ctx context.Context) ([]documentEntry, error) {
	if s.db == nil {
		return []documentEntry{}, nil
	}
	docs, err := s.db.Documents(ctx)
	if err != nil {
		return nil, err
	}
	cat, err := s.openCatalog(false)
	if err != nil {
		return nil, err
	}
	titles := map[string]string{}
	if cat != nil {
		if titles, err = cat.Titles(ctx); err != nil {
			return nil, err
		}
	}
	out := make([]documentEntry, len(docs))
	for i, d := range docs {
		title := d.Title
		if t := titles[d.Key]; t != "" {
			title = t
		}
		rel, err := filepath.Rel(s.docs, d.Path)
		if err != nil {
			rel = d.Path
		}
		out[i] = documentEntry{info: DocumentInfo{ID: d.ID, Title: title, Path: d.Path, Version: shortVersion(d.Hash)}, key: d.Key, rel: rel, src: d.SourcePath}
	}
	return out, nil
}

func shortVersion(hash string) string { return hash[:min(12, len(hash))] }

// resolveDocument finds a document by id, path (absolute or below the
// documents root), or a unique case-insensitive part of its path or title.
func (s *session) resolveDocument(ctx context.Context, ref string) (documentEntry, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return documentEntry{}, errors.New("document is required: an id, path or title from rag_list_documents")
	}
	docs, err := s.documentInfos(ctx)
	if err != nil {
		return documentEntry{}, err
	}
	for _, d := range docs {
		if d.info.ID == ref || d.info.Path == ref || d.rel == ref {
			return d, nil
		}
	}
	lower := strings.ToLower(ref)
	matches := []documentEntry{}
	for _, d := range docs {
		if strings.Contains(strings.ToLower(d.rel), lower) || strings.Contains(strings.ToLower(d.info.Title), lower) {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return documentEntry{}, fmt.Errorf("no indexed document matches %q; rag_list_documents lists ids and titles", ref)
	}
	names := []string{}
	for _, d := range matches[:min(5, len(matches))] {
		names = append(names, d.info.ID+" "+d.info.Title)
	}
	return documentEntry{}, fmt.Errorf("%d documents match %q; use an id: %s", len(matches), ref, strings.Join(names, "; "))
}

// documentChunks resolves ref and returns its chunks in document order.
// ponytail: loads the whole document per call; fine for papers and books of a
// few thousand chunks, page through SQL if documents grow far larger.
func (s *session) documentChunks(ctx context.Context, ref string) (DocumentInfo, []model.Chunk, error) {
	if s.db == nil {
		return DocumentInfo{}, nil, errors.New("no index yet; run rag sync")
	}
	d, err := s.resolveDocument(ctx, ref)
	if err != nil {
		return DocumentInfo{}, nil, err
	}
	ids, err := s.db.ChunkRows(ctx, d.info.Path)
	if err != nil {
		return DocumentInfo{}, nil, err
	}
	byID, err := s.db.Chunks(ctx, ids)
	if err != nil {
		return DocumentInfo{}, nil, err
	}
	chunks := make([]model.Chunk, len(ids))
	for i, id := range ids {
		chunks[i] = byID[id]
	}
	d.info.PDF = findPDF(d.info.Path, d.src)
	return d.info, chunks, nil
}

// findPDF returns the document's source PDF when it exists, else the only PDF
// in its folder, such as MinerU's <id>_origin.pdf.
func findPDF(path, source string) string {
	dir := filepath.Dir(path)
	if source != "" && strings.EqualFold(filepath.Ext(source), ".pdf") {
		if !filepath.IsAbs(source) {
			source = filepath.Join(dir, source)
		}
		if _, err := os.Stat(source); err == nil {
			return source
		}
	}
	found, _ := filepath.Glob(filepath.Join(dir, "*.pdf"))
	if len(found) == 1 {
		return found[0]
	}
	return ""
}
