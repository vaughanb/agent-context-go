package installer

import (
	"fmt"
	"os"
	"path/filepath"
)

// New returns the registry of supported agents in a stable display order. Each
// agent knows how to detect itself and how to write its own config format.
func New() []Agent {
	return []Agent{
		fileAgent{
			id:     "claude-desktop",
			name:   "Claude Desktop",
			reload: "Quit and reopen Claude Desktop (from the tray/menu, not just the window).",
			pathFn: userConfigPath("Claude", "claude_desktop_config.json"),
			writer: jsonWriter{key: "mcpServers"},
			manual: manualMcpServers,
		},
		cliAgent{
			id:         "claude-code",
			name:       "Claude Code (CLI)",
			bin:        "claude",
			reload:     "Start a new Claude Code session (run /mcp to confirm).",
			addArgs:    claudeAddArgs,
			removeArgs: func(name string) []string { return []string{"mcp", "remove", name, "-s", "user"} },
			manual:     claudeManual,
		},
		fileAgent{
			id:     "cursor",
			name:   "Cursor",
			reload: "In Cursor: Settings → MCP → refresh (or toggle the server off/on).",
			pathFn: homePath(".cursor", "mcp.json"),
			writer: jsonWriter{key: "mcpServers"},
			manual: manualMcpServers,
		},
		fileAgent{
			id:     "windsurf",
			name:   "Windsurf",
			reload: "In Windsurf Cascade: open the MCP panel → Refresh.",
			pathFn: homePath(".codeium", "windsurf", "mcp_config.json"),
			writer: jsonWriter{key: "mcpServers"},
			manual: manualMcpServers,
		},
		fileAgent{
			id:     "vscode",
			name:   "VS Code (Copilot)",
			reload: "In VS Code: run \"MCP: List Servers\" → Start/Restart.",
			pathFn: userConfigPath("Code", "User", "mcp.json"),
			writer: jsonWriter{key: "servers", withType: true},
			manual: manualVSCode,
		},
		cliAgent{
			id:     "codex",
			name:   "Codex CLI",
			bin:    "codex",
			reload: "Start a new Codex session (it reads ~/.codex/config.toml).",
			addArgs: func(srv Server) []string {
				return append([]string{"mcp", "add", srv.Name, "--", srv.Command}, srv.Args...)
			},
			removeArgs: func(name string) []string { return []string{"mcp", "remove", name} },
			manual:     codexManual,
		},
		fileAgent{
			id:     "opencode",
			name:   "OpenCode",
			reload: "Restart your OpenCode session.",
			pathFn: xdgConfigPath("opencode", "opencode.json"),
			writer: openCodeWriter{},
			manual: manualOpenCode,
		},
		fileAgent{
			id:     "cline",
			name:   "Cline (VS Code)",
			reload: "Cline reloads its MCP settings automatically.",
			pathFn: userConfigPath("Code", "User", "globalStorage", "saoudrizwan.claude-dev", "settings", "cline_mcp_settings.json"),
			writer: jsonWriter{key: "mcpServers"},
			manual: manualMcpServers,
		},
	}
}

// IDs returns the ID of every agent, for help text and validation.
func IDs(agents []Agent) []string {
	out := make([]string, len(agents))
	for i, a := range agents {
		out[i] = a.ID()
	}
	return out
}

// claudeAddArgs builds `claude mcp add <name> -s user [-e K=V ...] -- <cmd> <args...>`.
func claudeAddArgs(srv Server) []string {
	args := []string{"mcp", "add", srv.Name, "-s", "user"}
	for k, v := range srv.Env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, "--", srv.Command)
	return append(args, srv.Args...)
}

// Manual-instruction snippets, reused from the real writers so they can never
// drift from what the installer would write.

func manualMcpServers(srv Server, _ string) string {
	b, _ := jsonWriter{key: "mcpServers"}.upsert(nil, srv)
	return string(b)
}

func manualVSCode(srv Server, _ string) string {
	b, _ := jsonWriter{key: "servers", withType: true}.upsert(nil, srv)
	return string(b)
}

func manualOpenCode(srv Server, _ string) string {
	b, _ := openCodeWriter{}.upsert(nil, srv)
	return string(b)
}

func codexManual(srv Server) string {
	// A single-quoted TOML literal string needs no backslash escaping, which
	// matters for Windows paths.
	return fmt.Sprintf("[mcp_servers.%s]\ncommand = '%s'\nargs = []\n", srv.Name, srv.Command)
}

func claudeManual(srv Server) string {
	return fmt.Sprintf("claude mcp add %s -s user -- \"%s\"", srv.Name, srv.Command)
}

// Path resolvers. Each returns (path, supported) where supported is false only
// when the OS user directories cannot be resolved.

func homePath(parts ...string) func() (string, bool) {
	return func() (string, bool) {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		return filepath.Join(append([]string{h}, parts...)...), true
	}
}

func userConfigPath(parts ...string) func() (string, bool) {
	return func() (string, bool) {
		c, err := os.UserConfigDir()
		if err != nil {
			return "", false
		}
		return filepath.Join(append([]string{c}, parts...)...), true
	}
}

// xdgConfigPath resolves an XDG-style path (~/.config/... or $XDG_CONFIG_HOME)
// on every OS, which some cross-platform CLIs use instead of the native config
// dir.
func xdgConfigPath(parts ...string) func() (string, bool) {
	return func() (string, bool) {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			h, err := os.UserHomeDir()
			if err != nil {
				return "", false
			}
			base = filepath.Join(h, ".config")
		}
		return filepath.Join(append([]string{base}, parts...)...), true
	}
}
