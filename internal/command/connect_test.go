package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectOfficialCLIRegistration(t *testing.T) {
	for _, target := range []string{"claude", "codex"} {
		t.Run(target, func(t *testing.T) {
			var calls []string
			execute := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				calls = append(calls, name+" "+strings.Join(args, " "))
				if args[1] == "get" {
					return []byte("No MCP server found"), errors.New("missing")
				}
				return nil, nil
			}
			var out bytes.Buffer
			if err := connect(context.Background(), []string{target}, "http://127.0.0.1:7331/mcp", &out, &out, execute); err != nil {
				t.Fatal(err)
			}
			expected := "codex mcp add rag-go --url http://127.0.0.1:7331/mcp"
			if target == "claude" {
				expected = "claude mcp add --transport http --scope user rag-go http://127.0.0.1:7331/mcp"
			}
			if len(calls) != 2 || calls[1] != expected {
				t.Fatal(calls)
			}
		})
	}
}
func TestConnectIdempotencyConflictAndUnavailableCLI(t *testing.T) {
	for _, scenario := range []string{"same", "conflict", "replace", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			count := 0
			execute := func(ctx context.Context, name string, args ...string) ([]byte, error) {
				count++
				if args[1] != "get" {
					return nil, nil
				}
				if scenario == "unavailable" {
					return nil, os.ErrNotExist
				}
				endpoint := "http://127.0.0.1:7331/mcp"
				if scenario != "same" {
					endpoint = "http://127.0.0.1:7332/mcp"
				}
				return []byte(`{"transport":{"type":"streamable_http","url":"` + endpoint + `"}}`), nil
			}
			args := []string{"codex"}
			if scenario == "replace" {
				args = append(args, "--replace")
			}
			var out bytes.Buffer
			err := connect(context.Background(), args, "http://127.0.0.1:7331/mcp", &out, &out, execute)
			if (scenario == "same" || scenario == "replace") && err != nil {
				t.Fatal(err)
			}
			if (scenario == "conflict" || scenario == "unavailable") && err == nil {
				t.Fatal("expected error")
			}
			expected := 1
			if scenario == "replace" {
				expected = 3
			}
			if count != expected {
				t.Fatal(count)
			}
		})
	}
}
func TestServiceDefinitionsAndOwnership(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			root := t.TempDir()
			m := serviceManager{platform: platform, home: root, configDir: root, binary: "/stable/a b&c/rag", execute: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("stopped") }}
			def := m.definition("/data/papers & notes")
			if strings.Contains(def, "credentials") || !strings.Contains(def, "serve") {
				t.Fatal(def)
			}
			if platform == "darwin" && !strings.Contains(def, "&amp;") {
				t.Fatal("unescaped plist")
			}
			if platform == "linux" && !strings.Contains(def, `ExecStart="/stable/a b&c/rag" serve --store "/data/papers & notes"`) {
				t.Fatal(def)
			}
			var out bytes.Buffer
			if err := m.run(context.Background(), []string{"status"}, root, &out, &out); err != nil || !strings.Contains(out.String(), "not-installed") {
				t.Fatal(err, out.String())
			}
			os.MkdirAll(filepath.Dir(m.path()), 0700)
			os.WriteFile(m.path(), []byte(def), 0600)
			if m.run(context.Background(), []string{"uninstall"}, root, &out, &out) == nil {
				t.Fatal("removed unowned service")
			}
		})
	}
}
