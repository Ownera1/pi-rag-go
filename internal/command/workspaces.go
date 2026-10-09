package command

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"

	"github.com/Ownera1/rag-go/internal/workspace"
)

// Workspaces lists and edits the user-wide workspace registry. Removing an
// entry leaves the workspace itself untouched.
func Workspaces(ctx context.Context, args []string, out io.Writer) error {
	usage := errors.New("usage: rag workspace list | add [PATH] | remove NAME|PATH")
	if len(args) == 0 {
		return usage
	}
	switch {
	case args[0] == "list" && len(args) == 1:
		list, err := workspace.Workspaces()
		if err != nil {
			return err
		}
		if list == nil {
			list = []workspace.Entry{}
		}
		return json.NewEncoder(out).Encode(list)
	case args[0] == "add" && len(args) <= 2:
		explicit := ""
		if len(args) == 2 {
			explicit = args[1]
		}
		root, err := workspace.Discover(explicit)
		if err != nil {
			return err
		}
		if err = workspace.Register(ctx, root); err != nil {
			return err
		}
		list, err := workspace.Workspaces()
		if err != nil {
			return err
		}
		for _, e := range list {
			if e.Path == root {
				return json.NewEncoder(out).Encode(e)
			}
		}
		return nil
	case args[0] == "remove" && len(args) == 2:
		root, err := workspace.Resolve(args[1])
		if err != nil {
			return err
		}
		if root, err = filepath.Abs(root); err != nil {
			return err
		}
		if err = workspace.Unregister(ctx, root); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"removed": root})
	}
	return usage
}
