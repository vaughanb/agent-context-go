// Package installer registers this MCP server into the configuration of AI
// coding tools ("agents") that speak the Model Context Protocol, and removes it
// again. It detects which agents are installed on the machine and writes each
// one's config in that agent's own format, idempotently and with a backup.
//
// Adding support for a new agent is a single entry in registry.go plus, if it
// uses an unseen file format, one configWriter implementation.
package installer

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// Server is the MCP server to register. Command is an absolute path to the
// binary; Args and Env are optional.
type Server struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

// Action is what happened (or would happen) for one agent.
type Action string

const (
	// ActionInstalled means a new server entry was added.
	ActionInstalled Action = "installed"
	// ActionUpdated means an existing entry was changed.
	ActionUpdated Action = "updated"
	// ActionRemoved means an entry was deleted.
	ActionRemoved Action = "removed"
	// ActionUnchanged means the config already matched; nothing was written.
	ActionUnchanged Action = "unchanged"
	// ActionSkipped means the agent was not detected or is unsupported here.
	ActionSkipped Action = "skipped"
)

// Result reports the outcome for one agent.
type Result struct {
	AgentID   string
	AgentName string
	Location  string // config file path, or a CLI description
	Action    Action
	Backup    string // path to the .bak written, if any
	Note      string // extra context (reload hint, skip reason)
	Err       error
}

// Agent is one MCP-consuming tool that can be configured. Implementations live
// in this package; callers use the Registry.
type Agent interface {
	// ID is a short stable identifier (e.g. "claude-desktop").
	ID() string
	// Name is the human-readable name (e.g. "Claude Desktop").
	Name() string
	// Location is a human-readable description of what would be configured
	// (usually the config file path), valid even when the agent is absent.
	Location() string
	// Detected reports whether the agent appears to be installed.
	Detected() bool
	// Install registers srv, returning what happened. When dryRun is true it
	// computes the outcome without writing.
	Install(srv Server, dryRun bool) Result
	// Uninstall removes the server named name.
	Uninstall(name string, dryRun bool) Result
}

// Install registers srv into each agent, returning one Result per agent in the
// same order.
func Install(agents []Agent, srv Server, dryRun bool) []Result {
	results := make([]Result, len(agents))
	for i, a := range agents {
		results[i] = a.Install(srv, dryRun)
	}
	return results
}

// Uninstall removes the server named name from each agent.
func Uninstall(agents []Agent, name string, dryRun bool) []Result {
	results := make([]Result, len(agents))
	for i, a := range agents {
		results[i] = a.Uninstall(name, dryRun)
	}
	return results
}

// Detected returns only the agents that appear installed.
func Detected(agents []Agent) []Agent {
	var out []Agent
	for _, a := range agents {
		if a.Detected() {
			out = append(out, a)
		}
	}
	return out
}

// Select returns the agents whose IDs are in ids, preserving registry order. It
// also returns any ids that matched no agent.
func Select(agents []Agent, ids []string) (selected []Agent, unknown []string) {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	seen := map[string]bool{}
	for _, a := range agents {
		if want[a.ID()] {
			selected = append(selected, a)
			seen[a.ID()] = true
		}
	}
	for _, id := range ids {
		if !seen[id] {
			unknown = append(unknown, id)
		}
	}
	return selected, unknown
}

// configWriter merges a server into (or removes it from) a config file's raw
// bytes. existing is empty when the file does not yet exist.
type configWriter interface {
	// upsert returns the file contents with srv added or updated.
	upsert(existing []byte, srv Server) ([]byte, error)
	// remove returns the contents with name removed; found reports whether an
	// entry was present.
	remove(existing []byte, name string) (out []byte, found bool, err error)
}

// writeConfig applies newData to path, backing up any existing file first.
// Parent directories are created as needed.
func writeConfig(path string, existing, newData []byte) (backup string, err error) {
	if len(existing) > 0 {
		backup = path + ".bak"
		if err := os.WriteFile(backup, existing, 0o644); err != nil {
			return "", fmt.Errorf("write backup %q: %w", backup, err)
		}
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create config dir %q: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, newData, 0o644); err != nil {
		return "", fmt.Errorf("write config %q: %w", path, err)
	}
	return backup, nil
}

// readFileOrEmpty returns the file's contents, or empty bytes if it is absent.
func readFileOrEmpty(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	return b, nil
}

// sameContent reports whether two config byte slices are equivalent ignoring a
// trailing newline, so an idempotent write is reported as unchanged.
func sameContent(a, b []byte) bool {
	return bytes.Equal(bytes.TrimRight(a, "\n"), bytes.TrimRight(b, "\n"))
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
