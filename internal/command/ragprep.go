package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Ownera1/rag-go/internal/document"
	"github.com/Ownera1/rag-go/internal/ingest"
)

func Prep(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ragprep convert|import-mineru|inspect [options]")
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if args[0] == "inspect" {
		if len(args) != 2 {
			return errors.New("usage: ragprep inspect FILE")
		}
		doc, err := document.Parse(ctx, args[1])
		if err != nil {
			return err
		}
		return enc.Encode(doc)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "PDF file/directory, or MinerU JSON for import-mineru")
	output := fs.String("output", "", "directory for canonical document packages")
	switch args[0] {
	case "convert":
		backend := fs.String("backend", "grobid", "grobid, mineru or pdftotext")
		url := fs.String("url", "http://localhost:8070", "GROBID service base URL")
		timeout := fs.Duration("timeout", 10*time.Minute, "conversion timeout per PDF")
		mineru := fs.String("mineru-command", "mineru-kit", "MinerU 4 kit executable")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *input == "" || *output == "" || fs.NArg() != 0 || *timeout <= 0 {
			return errors.New("convert requires --input, --output and a positive --timeout")
		}
		files, err := pdfFiles(ctx, *input)
		if err != nil {
			return err
		}
		report := struct {
			Converted []ingest.Result `json:"converted"`
			Failures  []failure       `json:"failures"`
		}{Converted: []ingest.Result{}, Failures: []failure{}}
		for _, file := range files {
			if err := ctx.Err(); err != nil {
				return err
			}
			result, err := ingest.Convert(ctx, ingest.Options{
				Backend: *backend, Input: file, Output: *output, URL: *url,
				Timeout: *timeout, MinerUCommand: *mineru,
			})
			if err != nil {
				report.Failures = append(report.Failures, failure{file, err.Error()})
			} else {
				report.Converted = append(report.Converted, result)
			}
		}
		if err := enc.Encode(report); err != nil {
			return err
		}
		if len(report.Failures) > 0 {
			return fmt.Errorf("%d PDF conversions failed", len(report.Failures))
		}
		return nil
	case "import-mineru":
		source := fs.String("source", "", "original PDF path for result attribution")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *input == "" || *output == "" || fs.NArg() != 0 {
			return errors.New("import-mineru requires --input and --output")
		}
		result, err := ingest.ImportMinerU(ctx, *input, *source, *output)
		if err != nil {
			return err
		}
		return enc.Encode(result)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

type failure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

func pdfFiles(ctx context.Context, root string) ([]string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if st.Mode().IsRegular() {
		return []string{root}, nil
	}
	files := []string{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != root && strings.HasPrefix(entry.Name(), ".") {
			return filepath.SkipDir
		}
		if entry.Type().IsRegular() && strings.EqualFold(filepath.Ext(path), ".pdf") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("input directory contains no PDF files")
	}
	sort.Strings(files)
	return files, nil
}
