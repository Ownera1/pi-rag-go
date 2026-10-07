package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"

	"github.com/Ownera1/rag-go/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

// host is what agent registration touches outside rag-go, injectable in tests.
type host struct {
	execute  executor
	exe      string
	home     string
	lookPath func(string) (string, error)
}

func currentHost() (host, error) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.Abs(exe)
	}
	if err != nil {
		return host{}, err
	}
	// Linux resolves the executable through symlinks, which would pin a
	// versioned package path that an upgrade removes. Prefer the PATH entry
	// when it is the same file.
	if onPath, e := exec.LookPath(filepath.Base(exe)); e == nil {
		if abs, e := filepath.Abs(onPath); e == nil {
			a, e1 := os.Stat(abs)
			b, e2 := os.Stat(exe)
			if e1 == nil && e2 == nil && os.SameFile(a, b) {
				exe = abs
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return host{}, err
	}
	return host{execute: external, exe: exe, home: home, lookPath: exec.LookPath}, nil
}

func Install(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
	h, err := currentHost()
	if err != nil {
		return err
	}
	return install(ctx, args, in, out, stderr, h)
}

func Uninstall(ctx context.Context, args []string, out, stderr io.Writer) error {
	h, err := currentHost()
	if err != nil {
		return err
	}
	return uninstall(ctx, args, out, stderr, h)
}

// install records user-wide embedding defaults and a credential, then
// registers one workspace-agnostic MCP server with each agent.
func install(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer, h host) error {
	fs := flag.NewFlagSet("rag install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerOptions := addProviderFlags(fs)
	offline := fs.Bool("offline", false, "skip the embedding probe")
	agentList := fs.String("agents", "auto", "comma-separated "+agentNames+"; auto detects installed agents; none skips")
	if err := fs.Parse(ReorderFlags(args, map[string]bool{"offline": true, "h": true, "help": true})); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: rag install [--agents auto|none|" + agentNames + "] [provider options]")
	}
	agents, err := selectAgents(*agentList, h)
	if err != nil {
		return err
	}
	dir, err := workspace.GlobalDir()
	if err != nil {
		return err
	}
	cfgPath, err := globalConfigPath()
	if err != nil {
		return err
	}
	cfg, installed, err := loadGlobalConfig()
	if err != nil {
		return err
	}
	credentials, err := workspace.GlobalCredentials()
	if err != nil {
		return err
	}
	supplied := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { supplied[f.Name] = true })
	t := newTerminal(in, stderr)
	if !installed && t.interactive {
		if err = providerOptions.prompt(t, supplied); err != nil {
			return err
		}
	}
	providerOptions.apply(&cfg.Embedding, !installed, supplied)
	if err = cfg.Validate(); err != nil {
		return err
	}
	key := ""
	if name := cfg.Embedding.APIKeyEnv; name != "" {
		if !workspace.EnvName.MatchString(name) {
			return errors.New("invalid apiKeyEnv")
		}
		// Agents launched from a desktop app rarely inherit the shell
		// environment, so an environment key is saved like a typed one.
		key = os.Getenv(name)
		if key == "" {
			key = credentials[name]
		}
		if key == "" && t.interactive {
			if key, err = t.secret(name); err != nil {
				return err
			}
		}
		if key == "" {
			return fmt.Errorf("%s is unset; export it or run rag install in a terminal", name)
		}
		credentials[name] = key
	}
	if !supplied["agents"] && t.interactive {
		if agents, err = t.pick("Register rag-go with", agents, strings.Split(agentNames, ",")...); err != nil {
			return err
		}
	}
	checks := map[string]string{"embedding": "not checked (offline)"}
	if !*offline {
		var problem error
		if checks["embedding"], problem = t.spin("Embedding endpoint", func() (string, error) { return probe(ctx, cfg, key) }); problem != nil {
			// Nothing is saved, so the next run prompts again.
			return problem
		}
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err = workspace.AtomicJSON(cfgPath, cfg); err != nil {
		return err
	}
	credentialPath := ""
	if len(credentials) > 0 {
		credentialPath = filepath.Join(dir, "credentials.json")
		if err = workspace.AtomicJSON(credentialPath, credentials); err != nil {
			return err
		}
	}
	results, problem := map[string]string{}, error(nil)
	for _, agent := range agents {
		status, e := t.spin(agent, func() (string, error) { return register(ctx, agent, h, true) })
		if e != nil {
			status = "failed: " + e.Error()
			problem = errors.Join(problem, fmt.Errorf("%s: %w", agent, e))
		}
		results[agent] = status
	}
	report := map[string]any{"config": cfgPath, "credentials": credentialPath, "checks": checks, "agents": results,
		"embedding": map[string]any{"type": cfg.Embedding.Type, "model": cfg.Embedding.Model, "dimensions": cfg.Embedding.Dimensions}}
	if err = json.NewEncoder(out).Encode(report); err != nil {
		return err
	}
	if len(agents) > 0 {
		fmt.Fprintln(stderr, "Restart the agents to load rag-go. Then, in each project: rag init")
	} else {
		fmt.Fprintln(stderr, "In each project: rag init")
	}
	return problem
}

func uninstall(ctx context.Context, args []string, out, stderr io.Writer, h host) error {
	fs := flag.NewFlagSet("rag uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agentList := fs.String("agents", "auto", "comma-separated "+agentNames+"; auto detects installed agents")
	purge := fs.Bool("purge", false, "also delete the user-wide configuration and credentials")
	if err := fs.Parse(ReorderFlags(args, map[string]bool{"purge": true, "h": true, "help": true})); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: rag uninstall [--agents auto|" + agentNames + "] [--purge]")
	}
	agents, err := selectAgents(*agentList, h)
	if err != nil {
		return err
	}
	results, problem := map[string]string{}, error(nil)
	for _, agent := range agents {
		status, e := register(ctx, agent, h, false)
		if e != nil {
			status = "failed: " + e.Error()
			problem = errors.Join(problem, fmt.Errorf("%s: %w", agent, e))
		}
		results[agent] = status
	}
	report := map[string]any{"agents": results}
	if *purge {
		dir, err := workspace.GlobalDir()
		if err != nil {
			return err
		}
		removed := []string{}
		for _, name := range []string{"config.json", "credentials.json"} {
			path := filepath.Join(dir, name)
			if e := os.Remove(path); e == nil {
				removed = append(removed, path)
			} else if !errors.Is(e, os.ErrNotExist) {
				problem = errors.Join(problem, e)
			}
		}
		report["removed"] = removed
	}
	if err = json.NewEncoder(out).Encode(report); err != nil {
		return err
	}
	fmt.Fprintln(stderr, "Workspaces and project-level rag connect registrations are unchanged.")
	return problem
}

func selectAgents(list string, h host) ([]string, error) {
	switch list {
	case "none":
		return nil, nil
	case "auto":
		agents := []string{}
		if _, err := h.lookPath("claude"); err == nil {
			agents = append(agents, "claude")
		}
		_, err := h.lookPath("codex")
		if _, e := os.Stat(codexHome(h)); err == nil || e == nil {
			agents = append(agents, "codex")
		}
		for _, agent := range []string{"claude-desktop", "antigravity", "pi"} {
			if _, e := os.Stat(filepath.Dir(jsonAgentPath(agent, h))); e == nil {
				agents = append(agents, agent)
			}
		}
		return agents, nil
	}
	agents := []string{}
	for _, agent := range strings.Split(list, ",") {
		agent = strings.TrimSpace(agent)
		if !strings.Contains(","+agentNames+",", ","+agent+",") {
			return nil, fmt.Errorf("unknown agent %q; use %s, auto or none", agent, agentNames)
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

const agentNames = "claude,codex,claude-desktop,antigravity,pi"

func register(ctx context.Context, agent string, h host, add bool) (string, error) {
	switch agent {
	case "claude":
		if add {
			return registerClaude(ctx, h)
		}
		return unregisterClaude(ctx, h)
	case "codex":
		if add {
			return registerCodex(h)
		}
		return unregisterCodex(h)
	}
	return editJSONAgent(jsonAgentPath(agent, h), h, add)
}

// jsonAgentPath is the "mcpServers" JSON file of an agent configured by file:
// Claude Desktop, Antigravity (shared by the IDE and agy) and pi.
func jsonAgentPath(agent string, h host) string {
	switch agent {
	case "claude-desktop":
		if runtime.GOOS == "darwin" {
			return filepath.Join(h.home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
		}
		return filepath.Join(h.home, ".config", "Claude", "claude_desktop_config.json")
	case "antigravity":
		return filepath.Join(h.home, ".gemini", "config", "mcp_config.json")
	}
	return filepath.Join(h.home, ".pi", "agent", "mcp.json")
}

// editJSONAgent adds or removes mcpServers["rag-go"], keeping every other key.
func editJSONAgent(path string, h host, add bool) (string, error) {
	cfg := map[string]json.RawMessage{}
	mode := os.FileMode(0600)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if !add {
			return "not registered", nil
		}
	} else if err != nil {
		return "", err
	} else {
		if st, e := os.Stat(path); e == nil {
			mode = st.Mode().Perm()
		}
		if len(bytes.TrimSpace(b)) > 0 && json.Unmarshal(b, &cfg) != nil {
			return "", fmt.Errorf("%s is not a JSON object; fix it or add rag-go manually", path)
		}
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := cfg["mcpServers"]; ok && json.Unmarshal(raw, &servers) != nil {
		return "", fmt.Errorf("%s: mcpServers is not an object", path)
	}
	want, _ := json.Marshal(map[string]any{"command": h.exe, "args": launch})
	current, exists := servers["rag-go"]
	status := "removed"
	if add {
		var a, b any
		if exists && json.Unmarshal(current, &a) == nil && json.Unmarshal(want, &b) == nil && reflect.DeepEqual(a, b) {
			return "already registered", nil
		}
		servers["rag-go"] = want
		status = "registered"
		if exists {
			status = "updated"
		}
	} else {
		if !exists {
			return "not registered", nil
		}
		delete(servers, "rag-go")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if cfg["mcpServers"], err = json.Marshal(servers); err != nil {
		return "", err
	}
	if err = enc.Encode(cfg); err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	return status, workspace.AtomicFile(path, buf.Bytes(), mode)
}

// launch is the workspace-agnostic server command every agent registers.
var launch = []string{"mcp"}

func claudeUser(ctx context.Context, h host) (text string, registered bool, err error) {
	body, e := h.execute(ctx, h.home, "claude", "mcp", "get", "rag-go")
	text = string(body)
	if e == nil {
		return text, strings.Contains(text, "Scope: User"), nil
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "not found") || strings.Contains(lower, "no mcp server") || strings.Contains(lower, "no server") {
		return text, false, nil
	}
	return text, false, errors.New("could not inspect Claude configuration; ensure its CLI is installed and working")
}

func registerClaude(ctx context.Context, h host) (string, error) {
	text, registered, err := claudeUser(ctx, h)
	if err != nil {
		return "", err
	}
	status := "registered"
	if registered {
		if claudeField(text, "Command") == h.exe && claudeField(text, "Args") == strings.Join(launch, " ") {
			return "already registered", nil
		}
		if _, err = h.execute(ctx, h.home, "claude", "mcp", "remove", "--scope", "user", "rag-go"); err != nil {
			return "", errors.New("could not replace the user-scope Claude registration")
		}
		status = "updated"
	}
	args := append([]string{"mcp", "add", "--transport", "stdio", "--scope", "user", "rag-go", "--", h.exe}, launch...)
	if _, err = h.execute(ctx, h.home, "claude", args...); err != nil {
		return "", errors.New("could not register user-scope Claude MCP")
	}
	return status, nil
}

func unregisterClaude(ctx context.Context, h host) (string, error) {
	_, registered, err := claudeUser(ctx, h)
	if err != nil || !registered {
		return "not registered", err
	}
	if _, err = h.execute(ctx, h.home, "claude", "mcp", "remove", "--scope", "user", "rag-go"); err != nil {
		return "", errors.New("could not remove the user-scope Claude registration")
	}
	return "removed", nil
}

func codexHome(h host) string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(h.home, ".codex")
}

// codexTable matches rag-go's server table and its subtables, such as env.
var codexTable = regexp.MustCompile(`^\s*\[\s*mcp_servers\s*\.\s*(?:rag-go|"rag-go"|'rag-go')\s*[\].]`)

// withoutCodexServer drops rag-go's tables line by line, so comments and
// formatting elsewhere in the user's configuration survive.
func withoutCodexServer(text string) (string, bool) {
	lines := strings.SplitAfter(text, "\n")
	kept := make([]string, 0, len(lines))
	dropping, dropped := false, false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			dropping = codexTable.MatchString(line)
		}
		if dropping {
			dropped = true
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, ""), dropped
}

func codexServers(b []byte) (map[string]any, error) {
	cfg := map[string]any{}
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("invalid Codex configuration: %w", err)
	}
	servers, _ := cfg["mcp_servers"].(map[string]any)
	return servers, nil
}

func readCodex(h host) (string, os.FileMode, []byte, error) {
	path := filepath.Join(codexHome(h), "config.toml")
	mode := os.FileMode(0600)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, mode, nil, nil
	}
	if err != nil {
		return path, mode, nil, err
	}
	if st, e := os.Stat(path); e == nil {
		mode = st.Mode().Perm()
	}
	return path, mode, b, nil
}

func registerCodex(h host) (string, error) {
	path, mode, original, err := readCodex(h)
	if err != nil {
		return "", err
	}
	servers, err := codexServers(original)
	if err != nil {
		return "", err
	}
	current, exists := servers["rag-go"].(map[string]any)
	if exists && len(current) == 2 && current["command"] == h.exe && reflect.DeepEqual(stringArray(current["args"]), launch) {
		return "already registered", nil
	}
	text, _ := withoutCodexServer(string(original))
	if text = strings.TrimRight(text, "\n"); text != "" {
		text += "\n\n"
	}
	command, _ := json.Marshal(h.exe)
	args, _ := json.Marshal(launch)
	text += "[mcp_servers.rag-go]\ncommand = " + string(command) + "\nargs = " + string(args) + "\n"
	// Confirm the edit produced exactly the intended entry before writing.
	servers, err = codexServers([]byte(text))
	if err != nil {
		return "", errors.New("could not edit Codex configuration safely; add [mcp_servers.rag-go] manually")
	}
	entry, _ := servers["rag-go"].(map[string]any)
	if entry["command"] != h.exe || !reflect.DeepEqual(stringArray(entry["args"]), launch) || len(entry) != 2 {
		return "", errors.New("could not edit Codex configuration safely; add [mcp_servers.rag-go] manually")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err = workspace.AtomicFile(path, []byte(text), mode); err != nil {
		return "", err
	}
	if exists {
		return "updated", nil
	}
	return "registered", nil
}

func unregisterCodex(h host) (string, error) {
	path, mode, original, err := readCodex(h)
	if err != nil {
		return "", err
	}
	servers, err := codexServers(original)
	if err != nil {
		return "", err
	}
	if _, exists := servers["rag-go"]; !exists {
		return "not registered", nil
	}
	text, _ := withoutCodexServer(string(original))
	// Drop the blank separator that install added before the table.
	if text = strings.TrimRight(text, "\n"); text != "" {
		text += "\n"
	}
	if servers, err = codexServers([]byte(text)); err != nil || servers["rag-go"] != nil {
		return "", errors.New("could not edit Codex configuration safely; remove [mcp_servers.rag-go] manually")
	}
	if err = workspace.AtomicFile(path, []byte(text), mode); err != nil {
		return "", err
	}
	return "removed", nil
}
