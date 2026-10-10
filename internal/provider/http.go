package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Ownera1/rag-go/internal/model"
)

type HTTP struct {
	credential string
	refusal    error
	cfg        model.ProviderConfig
	client     *http.Client
	retries    int
	batchSize  int
}

func NewHTTP(cfg model.ProviderConfig, timeoutMs, retries int, batchSizes ...int) (*HTTP, error) {
	if cfg.Type != "voyage" && cfg.Type != "openai" && cfg.Type != "http" && cfg.Type != "dashscope" {
		return nil, fmt.Errorf("unsupported protocol %q", cfg.Type)
	}
	if cfg.BaseURL == "" {
		return nil, errors.New("provider baseUrl is required")
	}
	batch := 64
	if len(batchSizes) > 0 {
		batch = batchSizes[0]
	}
	if batch < 1 || batch > 256 {
		return nil, errors.New("embedding batch size must be in [1,256]")
	}
	return &HTTP{cfg: cfg, client: &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}, retries: retries, batchSize: batch}, nil
}

// SetCredential supplies the key resolved by workspace.Credential; the provider
// never reads the environment itself. Call before publishing the provider to
// other goroutines.
func (p *HTTP) SetCredential(key string) { p.credential = key }

// Refuse makes every request fail with err before anything is sent; nil
// allows requests. Call before publishing the provider to other goroutines.
func (p *HTTP) Refuse(err error) { p.refusal = err }

func (p *HTTP) Model() string { return p.cfg.Model }

func (p *HTTP) Dimensions() int { return p.cfg.Dimensions }

// call sends body as JSON, or nothing when body is nil, and decodes the
// response into out.
func (p *HTTP) call(ctx context.Context, method, path string, body any, out any) error {
	if p.refusal != nil {
		return p.refusal
	}
	var data []byte
	var err error
	if body != nil {
		if data, err = json.Marshal(body); err != nil {
			return err
		}
	}
	url := strings.TrimRight(p.cfg.BaseURL, "/") + path
	for attempt := 0; attempt <= p.retries; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		req, e := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(data))
		if e != nil {
			return e
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if p.cfg.APIKeyEnv != "" {
			if p.credential == "" {
				return fmt.Errorf("%s is unset; environment and user-wide keys go only to the endpoint rag install recorded", p.cfg.APIKeyEnv)
			}
			req.Header.Set("Authorization", "Bearer "+p.credential)
		}
		res, e := p.client.Do(req)
		retry := false
		if e != nil {
			err = e
			retry = true
		} else {
			raw, readErr := io.ReadAll(io.LimitReader(res.Body, 4<<20))
			res.Body.Close()
			if readErr != nil {
				err = readErr
				retry = true
			} else if res.StatusCode < 200 || res.StatusCode >= 300 {
				message := string(raw)
				if p.credential != "" {
					message = strings.ReplaceAll(message, p.credential, "[redacted]")
				}
				err = fmt.Errorf("model HTTP %d: %s", res.StatusCode, message[:min(len(message), 200)])
				retry = res.StatusCode == 429 || res.StatusCode >= 500
			} else {
				return json.Unmarshal(raw, out)
			}
		}
		if !retry || attempt == p.retries {
			return err
		}
		// Jitter keeps concurrent indexing workers from retrying in lockstep.
		delay := time.Duration(min(1<<attempt, 8)) * time.Second
		delay = delay/2 + rand.N(delay/2)
		if res != nil {
			if d, ok := retryAfter(res.Header.Get("Retry-After"), time.Now()); ok {
				delay = d
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return err
}

// retryAfter reads a Retry-After header, in seconds or as an HTTP date,
// capped so a misbehaving server cannot stall indexing.
func retryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	var d time.Duration
	if s, err := strconv.Atoi(v); err == nil {
		d = time.Duration(s) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	} else {
		return 0, false
	}
	return min(max(d, 0), time.Minute), true
}

func normalize(v []float32, dim int) ([]float32, error) {
	if len(v) != dim {
		return nil, fmt.Errorf("vector dimension %d, expected %d", len(v), dim)
	}
	var norm float64
	for _, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return nil, errors.New("non-finite embedding")
		}
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return nil, errors.New("zero embedding")
	}
	d := float32(math.Sqrt(norm))
	for i := range v {
		v[i] /= d
	}
	return v, nil
}

func (p *HTTP) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	v, e := p.embed(ctx, []string{text}, "query")
	if e != nil {
		return nil, e
	}
	return v[0], nil
}

func (p *HTTP) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return p.embed(ctx, texts, "document")
}

func (p *HTTP) embed(ctx context.Context, texts []string, role string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	all := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += p.batchSize {
		batch := texts[start:min(start+p.batchSize, len(texts))]
		body := map[string]any{"model": p.cfg.Model, "input": batch}
		if p.cfg.Type == "voyage" {
			body["input_type"] = role
			body["truncation"] = false
			body["output_dtype"] = "float"
			body["output_dimension"] = p.cfg.Dimensions
		} else {
			body["encoding_format"] = "float"
		}
		var result struct {
			Data []struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		if err := p.call(ctx, http.MethodPost, "/embeddings", body, &result); err != nil {
			return nil, err
		}
		if len(result.Data) != len(batch) {
			return nil, fmt.Errorf("embedding returned %d vectors for %d inputs", len(result.Data), len(batch))
		}
		ordered := make([][]float32, len(batch))
		seen := make([]bool, len(batch))
		for _, item := range result.Data {
			if item.Index < 0 || item.Index >= len(batch) || seen[item.Index] {
				return nil, errors.New("invalid embedding response index")
			}
			seen[item.Index] = true
			v, e := normalize(item.Embedding, p.cfg.Dimensions)
			if e != nil {
				return nil, e
			}
			ordered[item.Index] = v
		}
		all = append(all, ordered...)
	}
	return all, nil
}

func (p *HTTP) Rerank(ctx context.Context, query string, docs []model.RerankDoc, topK int) ([]model.RerankResult, error) {
	if len(docs) == 0 {
		return []model.RerankResult{}, nil
	}
	texts := make([]string, len(docs))
	for i, d := range docs {
		texts[i] = d.Text
	}
	body := map[string]any{"model": p.cfg.Model, "query": query, "documents": texts}
	var out struct {
		Data []struct {
			Index int     `json:"index"`
			Score float64 `json:"relevance_score"`
		} `json:"data"`
		Results []struct {
			Index int     `json:"index"`
			Score float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if p.cfg.Type == "voyage" {
		body["top_k"] = topK
		body["truncation"] = false
	} else {
		body["top_n"] = topK
	}
	path := "/rerank"
	if p.cfg.Type == "dashscope" {
		// Model Studio names its compatible endpoint in the plural; the body
		// and results are the generic HTTP ones.
		path = "/reranks"
	}
	if err := p.call(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	type pair struct {
		idx   int
		score float64
	}
	items := []pair{}
	if p.cfg.Type == "voyage" {
		for _, x := range out.Data {
			items = append(items, pair{x.Index, x.Score})
		}
	} else {
		for _, x := range out.Results {
			items = append(items, pair{x.Index, x.Score})
		}
	}
	if len(items) == 0 {
		return nil, errors.New("rerank response has no results")
	}
	seen := map[int]bool{}
	result := make([]model.RerankResult, 0, len(items))
	for _, x := range items {
		if x.idx < 0 || x.idx >= len(docs) || seen[x.idx] || math.IsNaN(x.score) || math.IsInf(x.score, 0) {
			return nil, errors.New("invalid rerank response")
		}
		seen[x.idx] = true
		result = append(result, model.RerankResult{ID: docs[x.idx].ID, Score: x.score})
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Score > result[j].Score })
	if len(result) > topK {
		return result[:topK], nil
	}
	return result, nil
}

// Models lists the model ids the endpoint serves at GET {baseUrl}/models, in
// the OpenAI list format.
func (p *HTTP) Models(ctx context.Context) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := p.call(ctx, http.MethodGet, "/models", nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}
