package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Ownera1/rag-go/internal/tui"
	"github.com/Ownera1/rag-go/internal/version"
	"github.com/Ownera1/rag-go/pkg/rag"
)

// ReorderFlags accepts options before or after positional arguments.
func ReorderFlags(args []string, booleans map[string]bool) []string {
	flags, positionals := []string{}, []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
			if !strings.Contains(a, "=") && !booleans[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positionals = append(positionals, a)
		}
	}
	return append(flags, positionals...)
}

func Run(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) error {
	if len(args) > 0 && args[0] == "--version" {
		args = []string{"version"}
	}
	if len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "--help" && args[0] != "-h" {
		i := 0
		for i < len(args) && strings.HasPrefix(args[i], "-") {
			if strings.Contains(args[i], "=") {
				i++
			} else {
				i += 2
			}
		}
		if i >= len(args) {
			return errors.New("a subcommand is required")
		}
		prefix := append([]string{}, args[:i]...)
		args = append([]string{args[i]}, append(prefix, args[i+1:]...)...)
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, "usage: rag install|uninstall|init|sync|query|list|outline|read|fix|status|rebuild|clean|tui|zotero|connect|mcp|eval|version [--workspace PATH] [options]")
		fmt.Fprintln(out, "\nGet started: rag install (once), then rag init in each project.")
		return nil
	}
	cmd, rest := args[0], args[1:]
	if cmd == "install" {
		return Install(ctx, rest, in, out, errout)
	}
	if cmd == "uninstall" {
		return Uninstall(ctx, rest, out, errout)
	}
	if cmd == "zotero" {
		return Zotero(ctx, rest, out, errout)
	}
	if cmd == "version" {
		fmt.Fprintf(out, "rag-go %s (%s)\n", version.Version, version.Commit)
		return nil
	}
	if cmd == "init" {
		return Initialize(ctx, rest, in, out, errout)
	}
	if cmd == "connect" {
		return Connect(ctx, rest, out, errout)
	}
	if cmd == "mcp" {
		return MCP(ctx, rest, errout)
	}
	if cmd == "eval" {
		return Evaluate(ctx, rest, out, errout)
	}
	if cmd == "tui" {
		fs := flag.NewFlagSet("rag tui", flag.ContinueOnError)
		fs.SetOutput(errout)
		root := fs.String("workspace", "", "explicit workspace root")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if !newTerminal(in, errout).interactive {
			return errors.New("rag tui requires an interactive terminal")
		}
		return tui.Run(ctx, *root)
	}
	if cmd != "sync" && cmd != "query" && cmd != "status" && cmd != "rebuild" && cmd != "clean" && cmd != "list" && cmd != "outline" && cmd != "read" && cmd != "fix" {
		return fmt.Errorf("unknown command %q; see rag --help and the v0.2 migration guide", cmd)
	}
	fs := flag.NewFlagSet("rag "+cmd, flag.ContinueOnError)
	fs.SetOutput(errout)
	root := fs.String("workspace", "", "explicit workspace root")
	opts := rag.QueryOptions{}
	read := rag.ReadOptions{}
	fix := rag.FixOptions{}
	before, after, from, to := 0, 0, 0, 0
	yearFrom, yearTo := 0, 0
	var tags, collections stringList
	keep, confirm, dryRun := 3, false, false
	if cmd == "query" {
		fs.IntVar(&yearFrom, "year-from", 0, "minimum publication year")
		fs.IntVar(&yearTo, "year-to", 0, "maximum publication year")
		fs.Var(&tags, "tag", "required Zotero tag; repeat for AND")
		fs.Var(&collections, "collection", "required collection key; repeat for AND")
		fs.StringVar(&opts.Mode, "mode", "hybrid", "hybrid, vector, bm25 or literal")
		fs.StringVar(&opts.Document, "document", "", "search only this document: id, path or unique part of its path or title")
		fs.IntVar(&opts.TopK, "top-k", 0, "returned hits")
		fs.IntVar(&opts.CandidateTopK, "candidate-top-k", 0, "rerank candidate count")
		fs.BoolVar(&opts.DisableRerank, "no-rerank", false, "disable rerank")
		fs.BoolVar(&opts.DisableSync, "no-sync", false, "query the existing index without synchronization")
	}
	if cmd == "outline" || cmd == "read" {
		fs.StringVar(&read.Document, "document", "", "document id, path or unique part of its path or title")
	}
	if cmd == "read" {
		fs.StringVar(&read.Around, "around", "", "chunk id to read around")
		fs.IntVar(&before, "before", 0, "chunks before --around (default 2)")
		fs.IntVar(&after, "after", 0, "chunks after --around (default 2)")
		fs.IntVar(&from, "from", 0, "first chunk index")
		fs.IntVar(&to, "to", 0, "last chunk index")
		fs.StringVar(&read.Pages, "pages", "", "page or range, such as 8-9")
		fs.IntVar(&read.MaxTokens, "max-tokens", 0, "token budget (default 4000, max 16000)")
		fs.StringVar(&read.Version, "version", "", "fail if the document's version differs")
	}
	if cmd == "fix" {
		fs.StringVar(&fix.Document, "document", "", "document id, path or unique part of its path or title")
		fs.StringVar(&fix.Wrong, "wrong", "", "wrong text as rag read shows it, occurring once")
		fs.StringVar(&fix.Right, "right", "", "text as the original PDF shows it")
		fs.StringVar(&fix.Version, "version", "", "fail if the document's version differs")
	}
	if cmd == "clean" {
		fs.IntVar(&keep, "keep", 3, "total generations to retain")
		fs.BoolVar(&confirm, "confirm", false, "permit generation deletion")
		fs.BoolVar(&dryRun, "dry-run", false, "force a preview")
	}
	if err := fs.Parse(ReorderFlags(rest, map[string]bool{"no-sync": true, "no-rerank": true, "confirm": true, "dry-run": true, "help": true, "h": true})); err != nil {
		return err
	}
	if cmd == "outline" && fs.NArg() > 0 && read.Document == "" {
		read.Document = strings.Join(fs.Args(), " ")
	} else if cmd != "query" && fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "before":
			read.Before = &before
		case "after":
			read.After = &after
		case "from":
			read.From = &from
		case "to":
			read.To = &to
		}
		if f.Name == "year-from" || f.Name == "year-to" || f.Name == "tag" || f.Name == "collection" {
			if opts.Filter == nil {
				opts.Filter = &rag.MetadataFilter{Tags: tags, Collections: collections}
			}
			if f.Name == "year-from" {
				opts.Filter.YearFrom = &yearFrom
			}
			if f.Name == "year-to" {
				opts.Filter.YearTo = &yearTo
			}
		}
	})
	core, err := rag.Open(rag.Options{WorkspaceDir: *root})
	if err != nil {
		return err
	}
	defer core.Close()
	var result any
	switch cmd {
	case "sync":
		result, err = core.Sync(ctx)
	case "query":
		result, err = core.Query(ctx, strings.Join(fs.Args(), " "), opts)
	case "list":
		result, err = core.Documents(ctx)
	case "outline":
		var r rag.Outline
		if r, err = core.Outline(ctx, read.Document); err == nil {
			result = r
		}
	case "read":
		var r rag.ReadResult
		if r, err = core.Read(ctx, read); err == nil {
			result = r
		}
	case "fix":
		var r rag.FixResult
		if r, err = core.Fix(ctx, fix); err == nil {
			result = r
		}
	case "status":
		result, err = core.Status(ctx)
	case "rebuild":
		result, err = core.Rebuild(ctx)
	case "clean":
		result, err = core.Cleanup(ctx, keep, dryRun || !confirm)
	}
	if result != nil {
		if e := json.NewEncoder(out).Encode(result); e != nil {
			return e
		}
	}
	return err
}
