package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"path/filepath"

	"github.com/Ownera1/rag-go/internal/model"
	"github.com/Ownera1/rag-go/internal/workspace"
	"github.com/Ownera1/rag-go/pkg/rag"
)

func Zotero(ctx context.Context, args []string, out, errout io.Writer) error {
	fs := flag.NewFlagSet("rag zotero", flag.ContinueOnError)
	fs.SetOutput(errout)
	root := workspaceFlag(fs)
	kind := fs.String("library-type", "", "user or group")
	id := fs.String("library-id", "", "library ID; 0 means current user")
	base := fs.String("base-url", "", "Zotero loopback Local API URL")
	start := fs.Bool("start-zotero", false, "start Zotero on macOS if its port refuses connections")
	manifest := fs.Bool("write-manifest", false, "also save a manual reference in rag-source.json")
	attachment := fs.String("attachment-key", "", "associated attachment key")
	if err := fs.Parse(ReorderFlags(args, map[string]bool{"start-zotero": true, "write-manifest": true, "h": true, "help": true})); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: rag zotero sync|status|match|links|link DOCUMENT ITEM_KEY [options]")
	}
	cmd := fs.Arg(0)
	if cmd != "sync" && cmd != "status" && cmd != "match" && cmd != "links" && cmd != "link" {
		return errors.New("unknown zotero subcommand")
	}
	if (cmd == "link" && fs.NArg() != 3) || (cmd != "link" && fs.NArg() != 1) {
		return errors.New("unexpected Zotero arguments; link requires DOCUMENT ITEM_KEY")
	}
	dir, err := workspace.Resolve(*root)
	if err != nil {
		return err
	}
	resolved, err := workspace.Discover(dir)
	if err != nil {
		return err
	}
	cfg, err := model.LoadConfig(filepath.Join(workspace.Store(resolved), "config.json"))
	if err != nil {
		return err
	}
	z := rag.ZoteroConfig{}
	if cfg.Zotero != nil {
		z = *cfg.Zotero
	}
	z = z.Defaults()
	if *kind != "" {
		z.LibraryType = *kind
	}
	if *id != "" {
		z.LibraryID = *id
	}
	if *base != "" {
		z.BaseURL = *base
	}
	if *start {
		z.StartOnDemand = true
	}
	if err = z.Validate(); err != nil {
		return err
	}
	core, err := rag.Open(rag.Options{WorkspaceDir: resolved})
	if err != nil {
		return err
	}
	defer core.Close()
	var result any
	switch cmd {
	case "sync":
		result, err = core.SyncZotero(ctx, &z)
	case "status":
		result, err = core.ZoteroStatus(ctx)
	case "match":
		result, err = core.MatchZotero(ctx, &z)
	case "links":
		result, err = core.ZoteroLinks(ctx)
	case "link":
		result, err = core.LinkZotero(ctx, fs.Arg(1), rag.ZoteroReference{LibraryType: z.LibraryType, LibraryID: z.LibraryID, ItemKey: fs.Arg(2), AttachmentKey: *attachment}, *manifest)
	}
	if result != nil {
		if e := json.NewEncoder(out).Encode(result); e != nil {
			return e
		}
	}
	return err
}

type stringList []string

func (s *stringList) String() string { return "" }
func (s *stringList) Set(value string) error {
	if value == "" {
		return errors.New("filter value cannot be empty")
	}
	*s = append(*s, value)
	return nil
}
