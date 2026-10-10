package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Ownera1/rag-go/internal/model"
)

// preset is a known endpoint. Choosing one sets the provider's protocol,
// address, credential name and first model together, and its models, the
// endpoint's two newest generations, become the model field's choices.
// Embedding presets also set the dimensions their models default to and the
// largest batch all their models accept.
type preset struct {
	name, typ, baseURL, keyEnv string
	models                     []string
	dims, batch                int
}

const voyageURL = "https://api.voyageai.com/v1"

var rerankPresets = []preset{
	{name: "none", typ: "none", models: []string{"none"}},
	{name: "voyage", typ: "voyage", baseURL: voyageURL, keyEnv: "VOYAGE_API_KEY",
		models: []string{"rerank-3", "rerank-3-lite", "rerank-2.5", "rerank-2.5-lite"}},
	{name: "dashscope", typ: "dashscope", baseURL: "https://dashscope.aliyuncs.com/compatible-api/v1", keyEnv: "DASHSCOPE_API_KEY",
		models: []string{"qwen3.7-text-rerank", "qwen3-rerank"}},
}

var embedPresets = []preset{
	{name: "voyage", typ: "voyage", baseURL: voyageURL, keyEnv: "VOYAGE_API_KEY",
		models: []string{"voyage-4-lite", "voyage-4", "voyage-4-large", "voyage-3.5", "voyage-3.5-lite"}, dims: 1024},
	{name: "dashscope", typ: "openai", baseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", keyEnv: "DASHSCOPE_API_KEY",
		models: []string{"qwen3.7-text-embedding", "text-embedding-v4"}, dims: 1024, batch: 10},
}

// presetOf returns the preset p is configured from, nil for a custom endpoint.
func presetOf(ps []preset, p model.ProviderConfig) *preset {
	i := slices.IndexFunc(ps, func(x preset) bool { return x.typ == p.Type && x.baseURL == p.BaseURL })
	if i < 0 {
		return nil
	}
	return &ps[i]
}

// provider is the field that picks a preset. Its value is derived from the
// protocol and address, so editing either by hand shows "custom".
func provider(group, name string, im impact, ps []preset, p func(*model.Config) *model.ProviderConfig) field {
	names := make([]string, len(ps))
	for i, x := range ps {
		names[i] = x.name
	}
	return field{group: group, name: name, impact: im, options: names,
		get: func(c model.Config) string {
			if x := presetOf(ps, *p(&c)); x != nil {
				return x.name
			}
			return "custom"
		},
		set: func(c *model.Config, s string) error {
			i := slices.IndexFunc(ps, func(x preset) bool { return x.name == s })
			if i < 0 {
				return fmt.Errorf("%s: unknown provider %q", name, s)
			}
			x, pc := ps[i], p(c)
			pc.Type, pc.BaseURL, pc.APIKeyEnv, pc.Model = x.typ, x.baseURL, x.keyEnv, x.models[0]
			if x.dims > 0 {
				pc.Dimensions = x.dims
			}
			if x.batch > 0 {
				c.Indexing.EmbeddingBatchSize = min(c.Indexing.EmbeddingBatchSize, x.batch)
			}
			return nil
		},
		restore: func(c *model.Config, saved model.Config) { *p(c) = *p(&saved) }}
}

// models returns the preset models of the provider p points to, nil when it
// is custom, for a model field's choices.
func models(ps []preset, p func(*model.Config) *model.ProviderConfig) func(model.Config) []string {
	return func(c model.Config) []string {
		if x := presetOf(ps, *p(&c)); x != nil {
			return x.models
		}
		return nil
	}
}

// maxMatches bounds the choices shown under a model being typed.
const maxMatches = 5

// rank keeps the choices containing q, ignoring case: those starting with it
// first, then those with a hyphenated word starting with it, then the rest,
// each in the given order.
func rank(choices []string, q string) []string {
	q = strings.ToLower(q)
	var tiers [3][]string
	for _, c := range choices {
		l := strings.ToLower(c)
		switch {
		case strings.HasPrefix(l, q):
			tiers[0] = append(tiers[0], c)
		case strings.Contains(l, "-"+q):
			tiers[1] = append(tiers[1], c)
		case strings.Contains(l, q):
			tiers[2] = append(tiers[2], c)
		}
	}
	return slices.Concat(tiers[:]...)
}

// matches lists what enter can pick while a field with choices is typed: the
// current value first when nothing is typed, otherwise the ranked choices and
// then the typed text itself, so a model missing from the list stays
// enterable.
func (m *Model) matches() []string {
	f := m.fields[m.cursor]
	choices, q := f.offered(m.draft), strings.TrimSpace(m.input.Value())
	if !m.editing || choices == nil {
		return nil
	}
	if q == "" {
		cur := f.get(m.draft)
		out := append([]string{cur}, slices.DeleteFunc(slices.Clone(choices), func(s string) bool { return s == cur })...)
		return out[:min(len(out), maxMatches)]
	}
	out, n := rank(choices, q), maxMatches
	exact := slices.Contains(out, q)
	if !exact {
		n--
	}
	out = out[:min(len(out), n)]
	if !exact {
		out = append(out, q)
	}
	return out
}
