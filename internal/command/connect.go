package command

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/Ownera1/rag-go/internal/workspace"
	"github.com/Ownera1/rag-go/pkg/rag"
	"github.com/pelletier/go-toml/v2"
)

type executor func(context.Context, string, string, ...string) ([]byte, error)

func external(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func Connect(ctx context.Context, args []string, out, stderr io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	return connect(ctx, args, out, stderr, external, exe)
}

func connect(ctx context.Context, args []string, out, stderr io.Writer, execute executor, exe string) error {
	fs := flag.NewFlagSet("rag connect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	explicit := fs.String("workspace", "", "workspace root")
	replace := fs.Bool("replace", false, "replace a conflicting project registration")
	if err := fs.Parse(ReorderFlags(args, map[string]bool{"replace": true, "help": true, "h": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 || (fs.Arg(0) != "claude" && fs.Arg(0) != "codex") {
		return errors.New("usage: rag connect claude|codex [--workspace PATH] [--replace]")
	}
	core, err := rag.Open(rag.Options{WorkspaceDir: *explicit})
	if err != nil {
		return err
	}
	root := core.WorkspaceDir()
	core.Close()
	launch := []string{"mcp", "--workspace", root}
	release, err := workspace.Lock(ctx, root, true)
	if err != nil {
		return err
	}
	defer release()
	if fs.Arg(0) == "codex" {
		directory := filepath.Join(root, ".codex")
		path := filepath.Join(directory, "config.toml")
		cfg := map[string]any{}
		b, e := os.ReadFile(path)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if e == nil {
			if e = toml.Unmarshal(b, &cfg); e != nil {
				return fmt.Errorf("invalid project Codex configuration: %w", e)
			}
		}
		servers := map[string]any{}
		if existing, ok := cfg["mcp_servers"]; ok {
			var good bool
			servers, good = existing.(map[string]any)
			if !good {
				return errors.New("mcp_servers must be a TOML table")
			}
		}
		if existing, ok := servers["rag-go"]; ok {
			current, good := existing.(map[string]any)
			same := good && current["command"] == exe && reflect.DeepEqual(stringArray(current["args"]), launch) && current["url"] == nil
			if same {
				fmt.Fprintln(out, "rag-go is already registered with Codex for "+root)
				return nil
			}
			if !*replace {
				return errors.New("project has a different rag-go registration; use --replace")
			}
		}
		if b, e = withCodexServer(b, exe, launch); e != nil {
			return e
		}
		if e = os.MkdirAll(directory, 0700); e != nil {
			return e
		}
		if real, e := filepath.EvalSymlinks(path); e == nil {
			path = real
		}
		if e = workspace.AtomicFile(path, b, 0600); e != nil {
			return e
		}
	} else {
		body, e := execute(ctx, root, "claude", "mcp", "get", "rag-go")
		if e == nil {
			// Claude's get command reports both scope and the complete stdio launch.
			text := string(body)
			if strings.Contains(text, "Scope: Local") && claudeField(text, "Command") == exe && claudeField(text, "Args") == strings.Join(launch, " ") {
				fmt.Fprintln(out, "rag-go is already registered with Claude for "+root)
				return nil
			}
			if strings.Contains(text, "Scope: Local") {
				if !*replace {
					return errors.New("project has a different rag-go registration; use --replace")
				}
				if _, e = execute(ctx, root, "claude", "mcp", "remove", "--scope", "local", "rag-go"); e != nil {
					return errors.New("could not replace local Claude registration")
				}
			}
		} else {
			lower := strings.ToLower(string(body))
			if !strings.Contains(lower, "not found") && !strings.Contains(lower, "no mcp server") && !strings.Contains(lower, "no server") {
				return errors.New("could not inspect Claude configuration; ensure its CLI is installed and working")
			}
		}
		args := append([]string{"mcp", "add", "--transport", "stdio", "--scope", "local", "rag-go", "--", exe}, launch...)
		if _, e = execute(ctx, root, "claude", args...); e != nil {
			return errors.New("could not register local Claude MCP")
		}
	}
	fmt.Fprintf(out, "Registered rag-go with %s for %s. Reload the Agent to use it.\n", fs.Arg(0), root)
	return nil
}

func claudeField(text, name string) string {
	for _, line := range strings.Split(text, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), name+":"); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringArray(value any) []string {
	switch x := value.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, len(x))
		for i, v := range x {
			var ok bool
			out[i], ok = v.(string)
			if !ok {
				return nil
			}
		}
		return out
	}
	return nil
}
