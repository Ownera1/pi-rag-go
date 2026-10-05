// Package searchtext adds Han ngrams without changing stored source text.
package searchtext

import (
	"strings"
	"unicode"
)

func Indexed(text string) string {
	out := []string{text}
	var run []rune
	flush := func() {
		for i, r := range run {
			out = append(out, string(r))
			if i+1 < len(run) {
				out = append(out, string(run[i:i+2]))
			}
		}
		run = nil
	}
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			run = append(run, r)
		} else {
			flush()
		}
	}
	flush()
	return strings.Join(out, " ")
}

// Query escapes FTS terms and uses bigrams for longer Han runs. Mixed-language
// queries retain non-Han words and require all terms. One-character queries
// use the unigram entries present in the index.
func Query(text string) (string, bool) {
	terms := []string{}
	var run []rune
	kind := 0
	hasHan := false
	flush := func() {
		if kind == 1 && len(run) > 1 {
			for i := 0; i+1 < len(run); i++ {
				terms = append(terms, string(run[i:i+2]))
			}
		} else if len(run) > 0 {
			terms = append(terms, string(run))
		}
		run = nil
	}
	for _, r := range text {
		next := 0
		if unicode.Is(unicode.Han, r) {
			next = 1
			hasHan = true
		} else if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' {
			next = 2
		}
		if next != kind || next == 0 {
			flush()
		}
		kind = next
		if next != 0 {
			run = append(run, r)
		}
	}
	flush()
	for i, term := range terms {
		terms[i] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	}
	return strings.Join(terms, " AND "), hasHan
}
