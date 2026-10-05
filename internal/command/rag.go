package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ReorderFlags permits options after positional arguments, preserving -- as a literal boundary.
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
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, "usage: rag init|serve|stdio|add|index|remove|query|status|refresh|rebuild|list|clear|cleanup|connect|service|prep|eval|version [options]")
		return nil
	}
	cmd, rest := args[0], args[1:]
	if cmd == "version" {
		fmt.Fprintf(out, "rag-go %s (%s)\n", Version, Commit)
		return nil
	}
	if cmd == "init" {
		return Initialize(ctx, rest, in, out, errout)
	}
	if cmd == "prep" {
		return Prep(ctx, rest, out, errout)
	}
	booleans := map[string]bool{"no-rerank": true, "confirm": true, "dry-run": true, "legacy-readonly": true, "h": true, "help": true, "replace": true}
	rest = ReorderFlags(rest, booleans)
	// --store is a facade option; older clients still use --endpoint directly.
	store := DefaultStore()
	filtered := []string{}
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--store" {
			if i+1 == len(rest) {
				return errors.New("--store requires a value")
			}
			i++
			store = rest[i]
		} else if strings.HasPrefix(rest[i], "--store=") {
			store = strings.TrimPrefix(rest[i], "--store=")
		} else {
			filtered = append(filtered, rest[i])
		}
	}
	rest = filtered
	if cmd == "serve" || cmd == "stdio" {
		return Serve(ctx, append([]string{cmd, "--store", store}, rest...), errout)
	}
	endpoint, err := endpointFor(store)
	if err != nil {
		return err
	}
	if cmd == "eval" {
		return Evaluate(append([]string{"--endpoint", endpoint}, rest...), out)
	}
	if cmd == "connect" {
		return Connect(ctx, rest, endpoint, out, errout)
	}
	if cmd == "service" {
		return Service(ctx, rest, store, out, errout)
	}
	if cmd == "add" {
		cmd = "index"
	}
	rest = ReorderFlags(rest, booleans)
	// All control options precede the legacy positional command.
	boundary := 0
	for boundary < len(rest) && strings.HasPrefix(rest[boundary], "-") {
		a := rest[boundary]
		boundary++
		name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
		if !strings.Contains(a, "=") && !booleans[name] && boundary < len(rest) {
			boundary++
		}
	}
	ctl := append([]string{"--endpoint", endpoint}, rest[:boundary]...)
	ctl = append(ctl, cmd)
	ctl = append(ctl, rest[boundary:]...)
	return Control(ctx, ctl, out, errout)
}
