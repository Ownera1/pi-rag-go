package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type executor func(context.Context, string, ...string) ([]byte, error)

func external(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	// Inspect personal registrations from a neutral directory, avoiding project overrides.
	cmd.Dir, _ = os.UserHomeDir()
	return cmd.CombinedOutput()
}

func Connect(ctx context.Context, args []string, endpoint string, out, stderr io.Writer) error {
	return connect(ctx, args, endpoint, out, stderr, external)
}
func connect(ctx context.Context, args []string, endpoint string, out, stderr io.Writer, execute executor) error {
	fs := flag.NewFlagSet("rag connect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	replace := fs.Bool("replace", false, "replace a conflicting rag-go registration")
	targetURL := fs.String("endpoint", endpoint, "MCP endpoint")
	if err := fs.Parse(ReorderFlags(args, map[string]bool{"replace": true, "help": true, "h": true})); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rag connect claude|codex [--replace]")
	}
	target := fs.Arg(0)
	if target != "claude" && target != "codex" {
		return errors.New("target must be claude or codex")
	}
	if err := validateEndpoint(*targetURL); err != nil {
		return err
	}
	get := []string{"mcp", "get", "rag-go"}
	if target == "codex" {
		get = append(get, "--json")
	}
	body, err := execute(ctx, target, get...)
	current := ""
	if target == "codex" && err == nil {
		var entry struct {
			Transport struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"transport"`
		}
		if json.Unmarshal(body, &entry) != nil {
			return errors.New("cannot inspect Codex registration")
		}
		if entry.Transport.Type == "streamable_http" {
			current = entry.Transport.URL
		}
	}
	if target == "claude" {
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "URL:") {
				current = strings.TrimSpace(strings.TrimPrefix(line, "URL:"))
			}
		}
	}
	if current == *targetURL {
		fmt.Fprintln(out, "rag-go is already registered with "+target)
		return nil
	}
	if err != nil && current == "" {
		lower := strings.ToLower(string(body))
		if !strings.Contains(lower, "not found") && !strings.Contains(lower, "no mcp server") && !strings.Contains(lower, "no server") {
			return fmt.Errorf("could not inspect %s MCP configuration; ensure its CLI is installed and working", target)
		}
	}
	exists := err == nil || current != ""
	if exists {
		if !*replace {
			return fmt.Errorf("%s already has a different rag-go registration; use --replace", target)
		}
		remove := []string{"mcp", "remove", "rag-go"}
		if target == "claude" {
			remove = append(remove, "--scope", "user")
		}
		if _, err = execute(ctx, target, remove...); err != nil {
			return fmt.Errorf("could not remove %s registration", target)
		}
	}
	add := []string{"mcp", "add", "rag-go", "--url", *targetURL}
	if target == "claude" {
		add = []string{"mcp", "add", "--transport", "http", "--scope", "user", "rag-go", *targetURL}
	}
	if _, err = execute(ctx, target, add...); err != nil {
		return fmt.Errorf("could not register %s MCP server", target)
	}
	fmt.Fprintf(out, "Registered rag-go with %s at %s. Reload the Agent to use it.\n", target, *targetURL)
	return nil
}
