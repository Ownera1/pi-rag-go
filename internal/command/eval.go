package command

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Ownera1/rag-go/internal/evaluate"
	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/version"
	"github.com/Ownera1/rag-go/internal/workspace"
	"github.com/Ownera1/rag-go/pkg/rag"
)

func rate(value string) (*float64, error) {
	if value == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(value, 64)
	return &v, err
}

func Evaluate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("rag eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := workspaceFlag(flags)
	dataset := flags.String("dataset", "", "annotated JSONL evaluation dataset")
	modes := flags.String("modes", "", "comma-separated modes (default bm25,vector,hybrid, plus rerank when a reranker is configured)")
	topK := flags.Int("top-k", 5, "retrieval evaluation cutoff")
	output := flags.String("output", "", "optional JSON report path")
	embRate := flags.String("embedding-usd-per-million-tokens", "", "optional rate for estimated embedding cost")
	rerankRate := flags.String("rerank-usd-per-million-tokens", "", "optional rate for estimated rerank cost")
	if err := flags.Parse(ReorderFlags(args, map[string]bool{"h": true, "help": true})); err != nil {
		return err
	}
	if *dataset == "" {
		return errors.New("--dataset is required")
	}
	data, err := os.ReadFile(*dataset)
	if err != nil {
		return err
	}
	cases, err := evaluate.ReadCases(bytes.NewReader(data))
	if err != nil {
		return err
	}
	eRate, err := rate(*embRate)
	if err != nil {
		return err
	}
	rRate, err := rate(*rerankRate)
	if err != nil {
		return err
	}
	dir, err := workspace.Resolve(*root)
	if err != nil {
		return err
	}
	core, err := rag.Open(rag.Options{WorkspaceDir: dir, ReadOnly: true})
	if err != nil {
		return explainMissing(err)
	}
	defer core.Close()
	status, err := core.Status(ctx)
	if err != nil {
		return err
	}
	if status.NeedsSync || status.NeedsRebuild {
		return errors.New("evaluation requires a synchronized compatible index; run rag sync or rag rebuild")
	}
	cfg, err := model.LoadConfig(filepath.Join(workspace.Store(core.WorkspaceDir()), "config.json"))
	if err != nil {
		return err
	}
	docs, err := core.Documents(ctx)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	env := &evaluate.Environment{Version: version.Version + " (" + version.Commit + ")", Config: cfg, ActiveDB: status.ActiveDB,
		Documents: map[string]string{}, DatasetSHA256: hex.EncodeToString(sum[:])}
	for _, d := range docs {
		env.Documents[d.ID] = d.Version
	}
	query := func(ctx context.Context, text string, options rag.QueryOptions) (rag.QueryResult, error) {
		options.DisableSync = true
		return core.Query(ctx, text, options)
	}
	if *modes == "" {
		*modes = "bm25,vector,hybrid"
		if cfg.Reranker.Type != "none" {
			*modes += ",rerank"
		}
	}
	selected := strings.Split(*modes, ",")
	for i := range selected {
		selected[i] = strings.TrimSpace(selected[i])
	}
	report, err := evaluate.Run(ctx, cases, query, evaluate.Options{TopK: *topK, Modes: selected, Dataset: *dataset, EmbeddingUSDPerMillionTokens: eRate, RerankUSDPerMillionTokens: rRate})
	if err != nil {
		return err
	}
	report.Environment = env
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if *output != "" {
		if err = os.MkdirAll(filepath.Dir(*output), 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(*output), ".rag-eval-*.json")
		if err != nil {
			return err
		}
		name := f.Name()
		defer os.Remove(name)
		if _, err = f.Write(b); err != nil {
			f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = os.Rename(name, *output); err != nil {
			return err
		}
	}
	if _, err = stdout.Write(b); err != nil {
		return err
	}
	for _, summary := range report.Summaries {
		if summary.Failed > 0 {
			return fmt.Errorf("evaluation recorded failures in mode %s; inspect JSON report", summary.Mode)
		}
	}
	return nil
}
