package model

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strconv"
)

func (r ZoteroReference) Validate() error {
	if r.LibraryType != "user" && r.LibraryType != "group" {
		return errors.New("invalid Zotero reference libraryType")
	}
	n, err := strconv.ParseUint(r.LibraryID, 10, 64)
	if err != nil || (r.LibraryType == "group" && n == 0) {
		return errors.New("invalid Zotero reference libraryId")
	}
	valid := regexp.MustCompile(`^[A-Z0-9]{8}$`)
	if !valid.MatchString(r.ItemKey) || (r.AttachmentKey != "" && !valid.MatchString(r.AttachmentKey)) {
		return errors.New("invalid Zotero reference item key")
	}
	return nil
}

type CatalogDocument struct {
	DOI        string           `json:"doi,omitempty"`
	Key        string           `json:"documentKey"`
	Path       string           `json:"path"`
	SourcePath string           `json:"sourcePath,omitempty"`
	Title      string           `json:"title"`
	Zotero     *ZoteroReference `json:"zotero,omitempty"`
	// ID prefixes the document's chunk ids; Hash is its indexed content hash.
	ID   string `json:"-"`
	Hash string `json:"-"`
}

type ZoteroConfig struct {
	BaseURL       string `json:"baseUrl,omitempty"`
	LibraryType   string `json:"libraryType,omitempty"`
	LibraryID     string `json:"libraryId,omitempty"`
	StartOnDemand bool   `json:"startOnDemand,omitempty"`
}

func (c ZoteroConfig) Defaults() ZoteroConfig {
	if c.BaseURL == "" {
		c.BaseURL = "http://127.0.0.1:23119/api/"
	}
	if c.LibraryType == "" {
		c.LibraryType = "user"
	}
	if c.LibraryID == "" {
		c.LibraryID = "0"
	}
	return c
}

func (c ZoteroConfig) Validate() error {
	c = c.Defaults()
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/" {
		return errors.New("zotero baseUrl must be an HTTP loopback URL ending in /api/")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("zotero baseUrl must use loopback")
	}
	if c.LibraryType != "user" && c.LibraryType != "group" {
		return errors.New("zotero libraryType must be user or group")
	}
	n, err := strconv.ParseUint(c.LibraryID, 10, 64)
	if err != nil || (c.LibraryType == "group" && n == 0) {
		return errors.New("invalid Zotero libraryId")
	}
	return nil
}

type ZoteroReference struct {
	LibraryType   string `json:"libraryType"`
	LibraryID     string `json:"libraryId"`
	ItemKey       string `json:"itemKey"`
	AttachmentKey string `json:"attachmentKey,omitempty"`
}

type ZoteroCreator struct {
	Type   string `json:"creatorType"`
	Family string `json:"lastName,omitempty"`
	Given  string `json:"firstName,omitempty"`
	Name   string `json:"name,omitempty"`
}

type ZoteroTag struct {
	Tag  string `json:"tag"`
	Type int    `json:"type"`
}

type ZoteroMetadata struct {
	ZoteroReference
	ItemType    string          `json:"itemType"`
	Title       string          `json:"title"`
	Abstract    string          `json:"abstract,omitempty"`
	Date        string          `json:"date,omitempty"`
	Year        *int            `json:"year,omitempty"`
	Publication string          `json:"publication,omitempty"`
	DOI         string          `json:"doi,omitempty"`
	CitationKey string          `json:"citationKey,omitempty"`
	Creators    []ZoteroCreator `json:"creators"`
	Tags        []ZoteroTag     `json:"tags"`
	Collections []string        `json:"collections"`
	Deleted     bool            `json:"deleted"`
	MatchMethod string          `json:"matchMethod"`
	Locked      bool            `json:"locked"`
	Orphan      bool            `json:"orphan"`
}

type MetadataFilter struct {
	YearFrom    *int     `json:"year_from,omitempty"`
	YearTo      *int     `json:"year_to,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Collections []string `json:"collections,omitempty"`
	LibraryType string   `json:"library_type,omitempty"`
	LibraryID   string   `json:"library_id,omitempty"`
}

func (f *MetadataFilter) Validate() error {
	if f == nil {
		return nil
	}
	if f.YearFrom != nil && (*f.YearFrom < 1 || *f.YearFrom > 9999) {
		return errors.New("invalid year_from")
	}
	if f.YearTo != nil && (*f.YearTo < 1 || *f.YearTo > 9999) {
		return errors.New("invalid year_to")
	}
	if f.YearFrom != nil && f.YearTo != nil && *f.YearFrom > *f.YearTo {
		return errors.New("year_from exceeds year_to")
	}
	if f.LibraryType != "" && f.LibraryType != "user" && f.LibraryType != "group" {
		return errors.New("invalid filter library_type")
	}
	if f.LibraryID != "" {
		if _, err := strconv.ParseUint(f.LibraryID, 10, 64); err != nil {
			return errors.New("invalid filter library_id")
		}
	}
	for _, x := range append(append([]string{}, f.Tags...), f.Collections...) {
		if x == "" {
			return errors.New("empty metadata filter value")
		}
	}
	return nil
}

type ZoteroSyncResult struct {
	LibraryType string   `json:"libraryType"`
	LibraryID   string   `json:"libraryId"`
	Fetched     int      `json:"fetched"`
	Updated     int      `json:"updated"`
	Unchanged   int      `json:"unchanged"`
	Deleted     int      `json:"deleted"`
	Linked      int      `json:"linked"`
	Unmatched   int      `json:"unmatched"`
	Orphans     []string `json:"orphans"`
	SyncedAt    string   `json:"syncedAt"`
}

type ZoteroStatus struct {
	Items       int      `json:"items"`
	Attachments int      `json:"attachments"`
	Links       int      `json:"links"`
	Locked      int      `json:"locked"`
	Orphans     []string `json:"orphans"`
	SyncedAt    string   `json:"syncedAt,omitempty"`
}
