// Package zotero reads the desktop Local API. It never writes to Zotero.
package zotero

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Ownera1/rag-go/internal/model"
)

var keyPattern = regexp.MustCompile(`^[A-Z0-9]{8}$`)
var yearPattern = regexp.MustCompile(`(?:^|\D)([12][0-9]{3})(?:\D|$)`)

func ValidKey(key string) bool { return keyPattern.MatchString(key) }

func NormalizeDOI(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSpace(strings.TrimPrefix(s, "doi:"))
	for _, prefix := range []string{"https://doi.org/", "http://doi.org/", "https://dx.doi.org/", "http://dx.doi.org/"} {
		s = strings.TrimPrefix(s, prefix)
	}
	return strings.TrimSpace(s)
}

type Item struct {
	Key                                            string
	Version                                        int64
	Metadata                                       model.ZoteroMetadata
	ParentKey, LinkMode, Filename, Path, Hash, Raw string
}

type Collection struct {
	Deleted                    bool
	Key                        string
	Version                    int64
	Name, ParentKey, Hash, Raw string
}

func parseCollection(raw json.RawMessage, kind, id string) (Collection, string, error) {
	var v struct {
		Key     string         `json:"key"`
		Version int64          `json:"version"`
		Data    map[string]any `json:"data"`
		Library struct {
			Type string `json:"type"`
			ID   int64  `json:"id"`
		} `json:"library"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return Collection{}, "", err
	}
	actual := strconv.FormatInt(v.Library.ID, 10)
	name, _ := v.Data["name"].(string)
	parent, _ := v.Data["parentCollection"].(string)
	if v.Library.Type != kind || (id != "0" && actual != id) || !ValidKey(v.Key) || v.Data == nil || name == "" {
		return Collection{}, "", errors.New("invalid Zotero collection or library")
	}
	deleted, _ := v.Data["deleted"].(bool)
	if n, ok := v.Data["deleted"].(float64); ok {
		deleted = n != 0
	}
	return Collection{Key: v.Key, Version: v.Version, Name: name, ParentKey: parent, Hash: contentHash(v.Data), Raw: string(raw), Deleted: deleted}, actual, nil
}

type Snapshot struct {
	LibraryType, LibraryID, RequestedID string
	Items                               []Item
	Collections                         []Collection
}

type Client struct {
	observedID string
	Config     model.ZoteroConfig
	HTTP       *http.Client
	Launch     func(context.Context) error
}

func New(config model.ZoteroConfig) (*Client, error) {
	config = config.Defaults()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Client{Config: config, HTTP: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Launch: launchZotero}, nil
}

func launchZotero(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		return errors.New("automatic Zotero startup is available on macOS; start Zotero manually")
	}
	return exec.CommandContext(ctx, "open", "-g", "-a", "Zotero").Run()
}

type HTTPError struct {
	Status int
	Path   string
}

func (e *HTTPError) Error() string {
	if e.Status == http.StatusForbidden {
		return "Zotero Local API is disabled; enable Settings > Advanced > Allow other applications on this computer to communicate with Zotero"
	}
	return fmt.Sprintf("Zotero Local API HTTP %d: %s", e.Status, e.Path)
}

func (c *Client) get(ctx context.Context, path string) ([]byte, http.Header, error) {
	u := c.Config.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Zotero-API-Version", "3")
	r, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return nil, nil, &HTTPError{r.StatusCode, path}
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 16<<20+1))
	if err == nil && len(b) > 16<<20 {
		err = errors.New("Zotero response exceeds 16 MiB")
	}
	return b, r.Header, err
}

func (c *Client) Probe(ctx context.Context) error {
	_, h, err := c.get(ctx, "")
	if err != nil && errors.Is(err, syscall.ECONNREFUSED) {
		if !c.Config.StartOnDemand {
			return fmt.Errorf("Zotero is not running; start it or enable zotero.startOnDemand: %w", err)
		}
		if c.Launch == nil {
			return errors.New("Zotero launcher unavailable")
		}
		if err = c.Launch(ctx); err != nil {
			return err
		}
		deadline := time.NewTimer(15 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-deadline.C:
				return errors.New("Zotero startup timed out")
			case <-tick.C:
				_, h, err = c.get(ctx, "")
				if err == nil || !errors.Is(err, syscall.ECONNREFUSED) {
					goto ready
				}
			}
		}
	}
ready:
	if err != nil {
		return err
	}
	if h.Get("Zotero-API-Version") != "3" {
		return errors.New("Zotero Local API version 3 required")
	}
	return nil
}

func (c *Client) prefix() string { return c.Config.LibraryType + "s/" + c.Config.LibraryID + "/" }

// pages checks totals, duplicate keys and library versions across every page.
// Versions are consistency hints only; they are never used as an update cursor.
func (c *Client) pages(ctx context.Context, kind string) ([]json.RawMessage, string, error) {
	out := []json.RawMessage{}
	expected := -1
	version := ""
	seen := map[string]bool{}
	for start := 0; ; start += 100 {
		if start > 1000000 {
			return nil, "", errors.New("Zotero snapshot too large")
		}
		q := url.Values{"limit": {"100"}, "start": {strconv.Itoa(start)}}
		if kind == "items" {
			q.Set("includeTrashed", "1")
		}
		b, h, err := c.get(ctx, c.prefix()+kind+"?"+q.Encode())
		if err != nil {
			return nil, "", err
		}
		// An empty user/0 library still identifies the logged-in user in the
		// alternate Link. Do not confuse "now empty" with a new library named 0.
		if c.Config.LibraryType == "user" && c.Config.LibraryID == "0" {
			for _, part := range strings.Split(h.Get("Link"), ",") {
				if !strings.Contains(part, `rel="alternate"`) {
					continue
				}
				start, end := strings.Index(part, "<"), strings.Index(part, ">")
				if start < 0 || end <= start {
					continue
				}
				u, e := url.Parse(part[start+1 : end])
				if e != nil || u.Hostname() != "www.zotero.org" {
					continue
				}
				pieces := strings.Split(strings.Trim(u.Path, "/"), "/")
				if len(pieces) < 2 || pieces[0] != "users" {
					continue
				}
				id, e := strconv.ParseUint(pieces[1], 10, 64)
				if e != nil || id == 0 {
					continue
				}
				actual := strconv.FormatUint(id, 10)
				if c.observedID != "" && c.observedID != actual {
					return nil, "", errors.New("Zotero account changed during snapshot")
				}
				c.observedID = actual
			}
		}
		total, err := strconv.Atoi(h.Get("Total-Results"))
		if err != nil || total < 0 {
			return nil, "", errors.New("missing or invalid Zotero Total-Results")
		}
		if expected == -1 {
			expected = total
			version = h.Get("Last-Modified-Version")
		}
		if total != expected || h.Get("Last-Modified-Version") != version {
			return nil, "", errors.New("Zotero library changed during snapshot; retry")
		}
		var page []json.RawMessage
		if err = json.Unmarshal(b, &page); err != nil || page == nil {
			return nil, "", errors.New("invalid Zotero page")
		}
		for _, raw := range page {
			var v struct {
				Key string `json:"key"`
			}
			if err = json.Unmarshal(raw, &v); err != nil || !ValidKey(v.Key) || seen[v.Key] {
				return nil, "", errors.New("invalid or duplicate Zotero object key")
			}
			seen[v.Key] = true
		}
		out = append(out, page...)
		if len(out) == expected {
			return out, version, nil
		}
		if len(page) != 100 || len(out) > expected {
			return nil, "", errors.New("incomplete Zotero snapshot; existing catalog retained")
		}
	}
}

func (c *Client) verifyKeys(ctx context.Context, kind string, keys []string, version string) error {
	q := url.Values{"format": {"keys"}}
	if kind == "items" {
		q.Set("includeTrashed", "1")
	}
	b, h, err := c.get(ctx, c.prefix()+kind+"?"+q.Encode())
	if err != nil {
		return err
	}
	got := strings.Fields(string(b))
	sort.Strings(got)
	sort.Strings(keys)
	if strings.Join(got, "\n") != strings.Join(keys, "\n") || h.Get("Last-Modified-Version") != version {
		return errors.New("Zotero objects changed during snapshot; existing catalog retained")
	}
	return nil
}

func contentHash(data map[string]any) string {
	copy := map[string]any{}
	for k, v := range data {
		if k != "version" && k != "dateModified" {
			copy[k] = v
			if k == "tags" || k == "collections" {
				if values, ok := v.([]any); ok {
					canonical := append([]any{}, values...)
					sort.Slice(canonical, func(i, j int) bool {
						a, _ := json.Marshal(canonical[i])
						b, _ := json.Marshal(canonical[j])
						return string(a) < string(b)
					})
					copy[k] = canonical
				}
			}
		}
	}
	b, _ := json.Marshal(copy)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func parseItem(raw json.RawMessage, libraryType, libraryID string) (Item, string, error) {
	var v struct {
		Key     string `json:"key"`
		Version int64  `json:"version"`
		Library struct {
			Type string `json:"type"`
			ID   int64  `json:"id"`
		} `json:"library"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return Item{}, "", err
	}
	str := func(k string) string { s, _ := v.Data[k].(string); return s }
	if v.Data == nil || str("itemType") == "" || str("key") != v.Key {
		return Item{}, "", errors.New("invalid Zotero item data")
	}
	id := strconv.FormatInt(v.Library.ID, 10)
	if v.Library.Type != libraryType || (libraryID != "0" && id != libraryID) {
		return Item{}, "", errors.New("Zotero returned another library")
	}
	m := model.ZoteroMetadata{ZoteroReference: model.ZoteroReference{LibraryType: libraryType, LibraryID: id, ItemKey: v.Key}, ItemType: str("itemType"), Title: str("title"), Abstract: str("abstractNote"), Date: str("date"), DOI: NormalizeDOI(str("DOI")), CitationKey: str("citationKey"), Creators: []model.ZoteroCreator{}, Tags: []model.ZoteroTag{}, Collections: []string{}}
	for _, field := range []string{"publicationTitle", "proceedingsTitle", "bookTitle", "reportType", "university", "websiteTitle"} {
		if m.Publication == "" {
			m.Publication = str(field)
		}
	}
	if y := yearPattern.FindStringSubmatch(m.Date); len(y) > 1 {
		n, _ := strconv.Atoi(y[1])
		m.Year = &n
	}
	if n, ok := v.Data["deleted"].(float64); ok {
		m.Deleted = n != 0
	}
	if b, ok := v.Data["deleted"].(bool); ok {
		m.Deleted = b
	}
	for key, dest := range map[string]any{"creators": &m.Creators, "tags": &m.Tags, "collections": &m.Collections} {
		if value, ok := v.Data[key]; ok {
			b, _ := json.Marshal(value)
			if err := json.Unmarshal(b, dest); err != nil {
				return Item{}, "", err
			}
		}
	}
	return Item{Key: v.Key, Version: v.Version, Metadata: m, ParentKey: str("parentItem"), LinkMode: str("linkMode"), Filename: str("filename"), Path: str("path"), Hash: contentHash(v.Data), Raw: string(raw)}, id, nil
}

func (c *Client) Fetch(ctx context.Context) (Snapshot, error) {
	c.observedID = ""
	s := Snapshot{LibraryType: c.Config.LibraryType, LibraryID: c.Config.LibraryID, RequestedID: c.Config.LibraryID}
	if err := c.Probe(ctx); err != nil {
		return s, err
	}
	raw, iv, err := c.pages(ctx, "items")
	if err != nil {
		return s, err
	}
	keys := []string{}
	actualID := ""
	for _, r := range raw {
		item, id, e := parseItem(r, s.LibraryType, c.Config.LibraryID)
		if e != nil {
			return s, e
		}
		if actualID != "" && actualID != id {
			return s, errors.New("mixed Zotero libraries")
		}
		actualID = id
		keys = append(keys, item.Key)
		if item.Metadata.ItemType == "attachment" && (item.LinkMode == "imported_file" || item.LinkMode == "imported_url" || item.LinkMode == "linked_file") {
			b, _, e := c.get(ctx, c.prefix()+"items/"+item.Key+"/file/view/url")
			if e != nil {
				var he *HTTPError
				if !errors.As(e, &he) || (he.Status != 404 && he.Status != 400) {
					return s, e
				}
			} else {
				u, e := url.Parse(strings.TrimSpace(string(b)))
				if e != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
					return s, errors.New("invalid Zotero local attachment URL")
				}
				item.Path = filepath.Clean(u.Path)
			}
		}
		s.Items = append(s.Items, item)
	}
	if actualID != "" {
		s.LibraryID = actualID
	}
	if c.observedID != "" {
		if actualID != "" && actualID != c.observedID {
			return s, errors.New("Zotero account mismatch")
		}
		s.LibraryID = c.observedID
	}
	cols, cv, err := c.pages(ctx, "collections")
	if err != nil {
		return s, err
	}
	ckeys := []string{}
	knownCollections := map[string]bool{}
	for _, r := range cols {
		v, id, e := parseCollection(r, s.LibraryType, s.LibraryID)
		if e != nil {
			return s, e
		}
		if s.LibraryID == "0" {
			s.LibraryID = id
		}
		s.Collections = append(s.Collections, v)
		ckeys = append(ckeys, v.Key)
		knownCollections[v.Key] = true
	}
	// Zotero 9 can retain item membership in deleted collections while omitting
	// those collections from the list endpoint. Preserve referenced tombstones.
	queue := []string{}
	for _, item := range s.Items {
		queue = append(queue, item.Metadata.Collections...)
	}
	for _, col := range s.Collections {
		if col.ParentKey != "" {
			queue = append(queue, col.ParentKey)
		}
	}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if knownCollections[key] {
			continue
		}
		knownCollections[key] = true
		if !ValidKey(key) {
			return s, errors.New("invalid referenced collection key")
		}
		b, _, e := c.get(ctx, c.prefix()+"collections/"+key)
		col := Collection{Key: key, Deleted: true}
		if e != nil {
			var he *HTTPError
			if !errors.As(e, &he) || he.Status != 404 {
				return s, e
			}
		} else {
			col, _, e = parseCollection(b, s.LibraryType, s.LibraryID)
			if e != nil {
				return s, e
			}
			if col.Key != key || !col.Deleted {
				return s, errors.New("collection list changed during snapshot; retry")
			}
		}
		s.Collections = append(s.Collections, col)
		if col.ParentKey != "" {
			queue = append(queue, col.ParentKey)
		}
	}
	if iv != cv {
		return s, errors.New("Zotero library changed between items and collections")
	}
	if err = c.verifyKeys(ctx, "items", keys, iv); err != nil {
		return s, err
	}
	if err = c.verifyKeys(ctx, "collections", ckeys, cv); err != nil {
		return s, err
	}
	return s, nil
}
