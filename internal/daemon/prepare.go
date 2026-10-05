package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ownera1/rag-go/internal/document"
	"github.com/Ownera1/rag-go/internal/ingest"
	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/pkg/rag"
)

type PDFPreparer struct {
	Store  string
	Config model.PDFConfig
}
type conversionCache struct {
	SourceHash string `json:"sourceHash"`
	Contract   string `json:"contract"`
	Manifest   string `json:"manifest"`
}

func sourceHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (100<<20)+1))
	if err != nil {
		return "", err
	}
	if n > 100<<20 {
		return "", errors.New("PDF exceeds 100 MiB limit")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func transient(err error) bool {
	if errors.Is(err, ingest.ErrSourceChanged) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	message := err.Error()
	for _, permanent := range []string{"input is not a PDF", "exceeds", "no extractable", "no body", "HTTP 400", "HTTP 401", "HTTP 403", "HTTP 404", "unknown conversion"} {
		if strings.Contains(message, permanent) {
			return false
		}
	}
	return true
}
func (p *PDFPreparer) Prepare(ctx context.Context, paths []string) (rag.PreparationResult, error) {
	result := rag.PreparationResult{Sources: []rag.PreparedSource{}, Failures: []rag.FileFailure{}}
	// Only manifests explicitly included in the current source scan suppress matching raw PDFs.
	explicit := map[string]string{}
	for _, path := range paths {
		if filepath.Base(path) == "rag-source.json" {
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var m document.Manifest
			if json.Unmarshal(b, &m) == nil && m.SourcePath != "" {
				source, err := filepath.Abs(m.SourcePath)
				if err == nil {
					if other := explicit[source]; other != "" && other != path {
						return result, fmt.Errorf("multiple canonical packages for %s", source)
					}
					explicit[source] = path
				}
			}
		}
	}
	contractBytes, _ := json.Marshal(p.Config)
	sum := sha256.Sum256(contractBytes)
	contract := hex.EncodeToString(sum[:])
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if strings.ToLower(filepath.Ext(path)) != ".pdf" || p.Config.Backend == "" {
			result.Sources = append(result.Sources, rag.PreparedSource{SourcePath: path, DocumentPath: path})
			continue
		}
		if explicit[path] != "" {
			continue
		}
		hash, err := sourceHash(path)
		packageDir := filepath.Join(p.Store, "prepared", strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))+"-"+document.ShortHash(path))
		cachePath := filepath.Join(packageDir, "conversion.json")
		manifest := filepath.Join(packageDir, "rag-source.json")
		var cache conversionCache
		b, readErr := os.ReadFile(cachePath)
		if err == nil && readErr == nil && json.Unmarshal(b, &cache) == nil && cache.SourceHash == hash && cache.Contract == contract && cache.Manifest == manifest {
			if _, cachedErr := document.Parse(ctx, manifest); cachedErr == nil {
				result.Sources = append(result.Sources, rag.PreparedSource{SourcePath: path, DocumentPath: manifest})
				continue
			}
		}
		if err == nil {
			before, e := os.Stat(path)
			if e != nil {
				err = e
			} else {
				select {
				case <-ctx.Done():
					return result, ctx.Err()
				case <-time.After(250 * time.Millisecond):
				}
				after, e := os.Stat(path)
				if e != nil {
					err = e
				} else if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
					err = ingest.ErrSourceChanged
				}
			}
		}
		if err == nil {
			timeout := time.Duration(p.Config.TimeoutMs) * time.Millisecond
			if timeout <= 0 {
				timeout = 10 * time.Minute
			}
			converted, e := ingest.Convert(ctx, ingest.Options{Backend: p.Config.Backend, Input: path, Output: filepath.Join(p.Store, "prepared"), URL: p.Config.URL, Timeout: timeout, MinerUCommand: p.Config.Command, PDFToTextCommand: p.Config.Command})
			err = e
			if err == nil {
				manifest = converted.Manifest
				cache = conversionCache{hash, contract, manifest}
				data, _ := json.Marshal(cache)
				err = os.WriteFile(cachePath, data, 0600)
			}
		}
		if err != nil {
			result.Failures = append(result.Failures, rag.FileFailure{Path: path, Stage: "convert", Error: err.Error(), Retryable: transient(err)})
			continue
		}
		result.Sources = append(result.Sources, rag.PreparedSource{SourcePath: path, DocumentPath: manifest})
	}
	return result, nil
}
