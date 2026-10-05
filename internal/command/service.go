package command

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Ownera1/rag-go/pkg/rag"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serviceLabel = "com.ownera1.rag-go"

type serviceManager struct {
	platform, home, configDir, binary string
	execute                           executor
}
type serviceRecord struct {
	Store  string `json:"store"`
	Binary string `json:"binary"`
}

func Service(ctx context.Context, args []string, store string, out, stderr io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	binary, err := exec.LookPath("rag")
	if err != nil {
		binary, err = os.Executable()
	}
	if err != nil {
		return err
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return err
	}
	manager := serviceManager{runtime.GOOS, home, config, binary, external}
	return manager.run(ctx, args, store, out, stderr)
}

func validateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Scheme != "http" || u.Path != "/mcp" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("endpoint must be a loopback HTTP /mcp URL")
	}
	return validateListen(u.Host)
}

func statusAt(ctx context.Context, endpoint string) (rag.Status, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "rag-service", Version: Version}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return rag.Status{}, err
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "rag_status", Arguments: map[string]any{}})
	if err != nil {
		return rag.Status{}, err
	}
	if result.IsError {
		return rag.Status{}, errors.New("rag_status failed")
	}
	b, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return rag.Status{}, err
	}
	var status rag.Status
	err = json.Unmarshal(b, &status)
	return status, err
}

func (m serviceManager) path() string {
	if m.platform == "darwin" {
		return filepath.Join(m.home, "Library", "LaunchAgents", serviceLabel+".plist")
	}
	return filepath.Join(m.configDir, "systemd", "user", "rag-go.service")
}
func xmlText(s string) string     { var b bytes.Buffer; xml.EscapeText(&b, []byte(s)); return b.String() }
func systemdText(s string) string { return strconv.Quote(strings.ReplaceAll(s, "%", "%%")) }
func (m serviceManager) definition(store string) string {
	if m.platform == "darwin" {
		return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>%s</string><key>ProgramArguments</key><array><string>%s</string><string>serve</string><string>--store</string><string>%s</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>ThrottleInterval</key><integer>10</integer><key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string></dict></plist>
`, serviceLabel, xmlText(m.binary), xmlText(store), xmlText(filepath.Join(store, "logs", "stdout.log")), xmlText(filepath.Join(store, "logs", "stderr.log")))
	}
	return fmt.Sprintf("[Unit]\nDescription=rag-go local knowledge service\n[Service]\nType=simple\nExecStart=%s serve --store %s\nRestart=on-failure\nRestartSec=5\n[Install]\nWantedBy=default.target\n", systemdText(m.binary), systemdText(store))
}

func (m serviceManager) action(ctx context.Context, verb string) error {
	var args []string
	tool := "systemctl"
	if m.platform == "darwin" {
		tool = "launchctl"
		domain := "gui/" + strconv.Itoa(os.Getuid())
		job := domain + "/" + serviceLabel
		switch verb {
		case "start":
			if _, e := m.execute(ctx, tool, "print", job); e == nil {
				args = []string{"kickstart", job}
			} else {
				args = []string{"bootstrap", domain, m.path()}
			}
		case "stop":
			args = []string{"bootout", job}
		case "restart":
			args = []string{"kickstart", "-k", job}
		case "status":
			args = []string{"print", job}
		}
	} else {
		switch verb {
		case "reload":
			args = []string{"--user", "daemon-reload"}
		case "enable":
			args = []string{"--user", "enable", "rag-go.service"}
		case "disable":
			args = []string{"--user", "disable", "--now", "rag-go.service"}
		case "status":
			args = []string{"--user", "is-active", "rag-go.service"}
		default:
			args = []string{"--user", verb, "rag-go.service"}
		}
	}
	_, err := m.execute(ctx, tool, args...)
	return err
}

func (m serviceManager) state(ctx context.Context) string {
	if m.platform == "linux" {
		b, e := m.execute(ctx, "systemctl", "--user", "show", "rag-go.service", "--property=ActiveState", "--value")
		if e != nil {
			return "failed"
		}
		switch strings.TrimSpace(string(b)) {
		case "active":
			return "running"
		case "failed":
			return "failed"
		case "activating":
			return "starting"
		default:
			return "stopped"
		}
	}
	domain := "gui/" + strconv.Itoa(os.Getuid()) + "/" + serviceLabel
	b, e := m.execute(ctx, "launchctl", "print", domain)
	if e != nil {
		return "stopped"
	}
	s := string(b)
	if strings.Contains(s, "state = running") || strings.Contains(s, "pid = ") {
		return "running"
	}
	if strings.Contains(s, "last exit code = ") && !strings.Contains(s, "last exit code = 0") {
		return "failed"
	}
	return "stopped"
}

func (m serviceManager) ready(ctx context.Context, endpoint, store string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		probe, c := context.WithTimeout(ctx, time.Second)
		status, err := statusAt(probe, endpoint)
		c()
		if err == nil {
			if status.StoreDir != store {
				return errors.New("endpoint belongs to another store")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("service did not become ready; inspect %s", filepath.Join(store, "logs", "stderr.log"))
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (m serviceManager) run(ctx context.Context, args []string, store string, out, stderr io.Writer) error {
	if m.platform != "darwin" && m.platform != "linux" {
		return errors.New("service management supports macOS and Linux")
	}
	fs := flag.NewFlagSet("rag service", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rag service install|start|stop|restart|status|uninstall")
	}
	verb := fs.Arg(0)
	root, err := filepath.Abs(store)
	if err != nil {
		return err
	}
	endpoint, err := endpointFor(root)
	if err != nil {
		return err
	}
	path := m.path()
	_, err = os.Stat(path)
	installed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if installed {
		b, e := os.ReadFile(path + ".json")
		if e != nil {
			return errors.New("service file exists without a rag-go ownership record")
		}
		var record serviceRecord
		if json.Unmarshal(b, &record) != nil || record.Store != root {
			return errors.New("service is installed for another store")
		}
	}
	if verb == "status" {
		state := "not-installed"
		if installed {
			state = "stopped"
			state = m.state(ctx)
			if state == "running" {
				state = "starting"
				probe, c := context.WithTimeout(ctx, time.Second)
				s, e := statusAt(probe, endpoint)
				c()
				if e == nil && s.StoreDir == root {
					state = "ready"
				}
			}
		}
		lifetime := "login session"
		if m.platform == "linux" {
			b, e := m.execute(ctx, "loginctl", "show-user", strconv.Itoa(os.Getuid()), "--property=Linger", "--value")
			if e == nil && strings.TrimSpace(string(b)) == "yes" {
				lifetime = "linger enabled"
			}
		}
		logs := filepath.Join(root, "logs", "stderr.log")
		if m.platform == "linux" {
			logs = "journalctl --user -u rag-go.service"
		}
		return json.NewEncoder(out).Encode(map[string]any{"state": state, "store": root, "endpoint": endpoint, "logs": logs, "lifetime": lifetime})
	}
	switch verb {
	case "install":
		if installed {
			if m.state(ctx) == "running" {
				return m.ready(ctx, endpoint, root)
			}
			if err = m.action(ctx, "start"); err != nil {
				return err
			}
			return m.ready(ctx, endpoint, root)
		}
		if _, err = os.Stat(filepath.Join(root, "config.json")); err != nil {
			return errors.New("run rag init before installing the service")
		}
		probe, c := context.WithTimeout(ctx, time.Second)
		_, existing := statusAt(probe, endpoint)
		c()
		if existing == nil {
			return errors.New("endpoint is already serving a foreground instance; stop it before service installation")
		}
		if err = os.MkdirAll(filepath.Join(root, "logs"), 0700); err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err = os.WriteFile(path, []byte(m.definition(root)), 0600); err != nil {
			return err
		}
		if err = atomicJSON(path+".json", serviceRecord{root, m.binary}); err != nil {
			return err
		}
		if m.platform == "linux" {
			if err = m.action(ctx, "reload"); err != nil {
				return err
			}
			if err = m.action(ctx, "enable"); err != nil {
				return err
			}
		}
		if err = m.action(ctx, "start"); err != nil {
			return err
		}
		return m.ready(ctx, endpoint, root)
	case "start":
		if !installed {
			return errors.New("service is not installed")
		}
		if m.state(ctx) != "running" {
			if err = m.action(ctx, "start"); err != nil {
				return err
			}
		}
		return m.ready(ctx, endpoint, root)
	case "restart":
		if !installed {
			return errors.New("service is not installed")
		}
		if m.state(ctx) != "running" {
			err = m.action(ctx, "start")
		} else {
			err = m.action(ctx, "restart")
		}
		if err != nil {
			return err
		}
		return m.ready(ctx, endpoint, root)
	case "stop":
		if !installed {
			return errors.New("service is not installed")
		}
		if m.action(ctx, "status") == nil {
			return m.action(ctx, "stop")
		}
		return nil
	case "uninstall":
		if !installed {
			return nil
		}
		if m.platform == "linux" {
			if err = m.action(ctx, "disable"); err != nil {
				return err
			}
		} else if m.action(ctx, "status") == nil {
			if err = m.action(ctx, "stop"); err != nil {
				return err
			}
		}
		if err = os.Remove(path); err != nil {
			return err
		}
		if err = os.Remove(path + ".json"); err != nil {
			return err
		}
		if m.platform == "linux" {
			return m.action(ctx, "reload")
		}
		return nil
	default:
		return errors.New("unknown service action")
	}
}
