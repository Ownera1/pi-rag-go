package tui

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Ownera1/rag-go/internal/model"
	httpprovider "github.com/Ownera1/rag-go/internal/provider"
	"github.com/Ownera1/rag-go/internal/workspace"
)

// preset is a known endpoint. Choosing one sets the provider's protocol,
// address, credential name and first model together, and its models, the
// endpoint's two newest generations, become the model field's choices, with
// any newer ones the endpoint lists at list/models. Embedding presets also set
// the dimensions their models default to and the largest batch all their
// models accept.
type preset struct {
	name, typ, baseURL, keyEnv string
	models                     []string
	dims, batch                int
	list                       string
}

const voyageURL = "https://api.voyageai.com/v1"

var rerankPresets = []preset{
	{name: "none", typ: "none", models: []string{"none"}},
	{name: "voyage", typ: "voyage", baseURL: voyageURL, keyEnv: "VOYAGE_API_KEY",
		models: []string{"rerank-3", "rerank-3-lite", "rerank-2.5", "rerank-2.5-lite"}},
	// The rerank endpoint has no model list; the embedding one lists rerankers
	// too, though not every model it serves.
	{name: "dashscope", typ: "dashscope", baseURL: "https://dashscope.aliyuncs.com/compatible-api/v1", keyEnv: "DASHSCOPE_API_KEY",
		models: []string{"qwen3.7-text-rerank", "qwen3-rerank"}, list: dashscopeURL},
}

var embedPresets = []preset{
	{name: "voyage", typ: "voyage", baseURL: voyageURL, keyEnv: "VOYAGE_API_KEY",
		models: []string{"voyage-4-lite", "voyage-4", "voyage-4-large", "voyage-3.5", "voyage-3.5-lite"}, dims: 1024},
	{name: "dashscope", typ: "openai", baseURL: dashscopeURL, keyEnv: "DASHSCOPE_API_KEY",
		models: []string{"qwen3.7-text-embedding", "text-embedding-v4"}, dims: 1024, batch: 10, list: dashscopeURL},
}

const dashscopeURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

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

// listURL is where p's endpoint lists its models: the preset's list, or a
// custom endpoint's own address.
func listURL(ps []preset, p model.ProviderConfig) string {
	if x := presetOf(ps, p); x != nil {
		return x.list
	}
	if p.Type == "none" {
		return ""
	}
	return p.BaseURL
}

// listing is a model list fetched from an endpoint; done is false while the
// request runs.
type listing struct {
	ids  []string
	err  error
	done bool
}

// models gives a model field its choices: the preset's models, then those the
// endpoint lists whose names say they serve role ("embed" or "rerank"),
// keeping each family's two newest generations. A custom endpoint that lists
// nothing has no choices, so its model is typed freely.
func models(f field, ps []preset, p func(*model.Config) *model.ProviderConfig, role string, fetched map[string]listing) field {
	f.source = func(c model.Config) string { return listURL(ps, *p(&c)) }
	f.choices = func(c model.Config) []string {
		var out []string
		if x := presetOf(ps, *p(&c)); x != nil {
			out = slices.Clone(x.models)
		}
		for _, id := range fetched[f.source(c)].ids {
			if strings.Contains(strings.ToLower(id), role) && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
		if out == nil {
			return nil
		}
		return recent(out)
	}
	return f
}

var version = regexp.MustCompile(`\d+(\.\d+)?`)

// recent keeps the models of each family's two newest versions, in order. A
// family is the name before its first number and the version is that number,
// so voyage-3-large drops once voyage-4 and voyage-3.5 exist; names without a
// number stay.
// ponytail: naming heuristic; a vendor that puts dates or sizes before the
// version needs its own rule.
func recent(ids []string) []string {
	parse := func(id string) (string, float64, bool) {
		at := version.FindStringIndex(id)
		if at == nil {
			return "", 0, false
		}
		v, _ := strconv.ParseFloat(id[at[0]:at[1]], 64)
		return id[:at[0]], v, true
	}
	newest := map[string][]float64{}
	for _, id := range ids {
		if f, v, ok := parse(id); ok && !slices.Contains(newest[f], v) {
			newest[f] = append(newest[f], v)
		}
	}
	for f, vs := range newest {
		slices.SortFunc(vs, func(a, b float64) int { return cmp.Compare(b, a) })
		newest[f] = vs[:min(2, len(vs))]
	}
	return slices.DeleteFunc(slices.Clone(ids), func(id string) bool {
		f, v, ok := parse(id)
		return ok && !slices.Contains(newest[f], v)
	})
}

// listModels fetches the models at url with p's credential, under the same
// endpoint and credential rules as queries.
func listModels(ctx context.Context, root string, p model.ProviderConfig, url string, timeoutMs int) ([]string, error) {
	if err := workspace.CheckEndpoint(root, p); err != nil {
		return nil, err
	}
	key, err := workspace.Credential(root, p)
	if err != nil {
		return nil, err
	}
	p.BaseURL = url
	h, err := httpprovider.NewHTTP(p, timeoutMs, 0)
	if err != nil {
		return nil, err
	}
	h.SetCredential(key)
	return h.Models(ctx)
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
