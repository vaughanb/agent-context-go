package installer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testSrv = Server{
	Name:    "agent-context-go",
	Command: `C:\dev\agent-context-go\agent-context-go.exe`,
}

func TestOrderedMapPreservesOrder(t *testing.T) {
	in := []byte(`{"z":1,"a":{"nested":true},"m":"x"}`)
	m, err := parseOMap(in)
	require.NoError(t, err)
	assert.Equal(t, []string{"z", "a", "m"}, m.keys)

	m.set("new", json.RawMessage(`2`))
	out, err := m.MarshalJSON()
	require.NoError(t, err)
	// Existing order preserved, new key appended, nested object untouched.
	assert.Equal(t, `{"z":1,"a":{"nested":true},"m":"x","new":2}`, string(out))
}

func TestParseOMapRejectsNonObject(t *testing.T) {
	_, err := parseOMap([]byte(`[1,2,3]`))
	require.Error(t, err)
	_, err = parseOMap([]byte(`// a comment
{"a":1}`))
	require.Error(t, err, "JSONC comments are not valid strict JSON")
}

func TestJSONWriterCreatesAndPreserves(t *testing.T) {
	// Into an existing config with unrelated keys: they and their order survive.
	existing := []byte(`{
  "coworkUserFilesPath": "C:\\Users\\me\\Claude",
  "preferences": {"a": 1}
}`)
	w := jsonWriter{key: "mcpServers"}
	out, err := w.upsert(existing, testSrv)
	require.NoError(t, err)

	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &doc))
	assert.Contains(t, doc, "coworkUserFilesPath")
	assert.Contains(t, doc, "preferences")
	assert.Contains(t, doc, "mcpServers")
	// Top-level order preserved with the new key appended last.
	assert.Less(t, strings.Index(string(out), "coworkUserFilesPath"), strings.Index(string(out), "mcpServers"))
	assert.Less(t, strings.Index(string(out), "preferences"), strings.Index(string(out), "mcpServers"))

	var parsed struct {
		MCP map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Type    string   `json:"type"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(out, &parsed))
	entry := parsed.MCP["agent-context-go"]
	assert.Equal(t, testSrv.Command, entry.Command)
	assert.Empty(t, entry.Args)
	assert.Empty(t, entry.Type, "mcpServers family has no type field")
}

func TestJSONWriterIdempotentAndUpdate(t *testing.T) {
	w := jsonWriter{key: "mcpServers"}
	first, err := w.upsert(nil, testSrv)
	require.NoError(t, err)
	// Re-applying identical input yields identical bytes.
	second, err := w.upsert(first, testSrv)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(second))

	// Changing the command updates in place; entry is present once.
	updated, err := w.upsert(first, Server{Name: testSrv.Name, Command: "/new/path"})
	require.NoError(t, err)
	assert.Contains(t, string(updated), "/new/path")
	assert.Equal(t, 1, strings.Count(string(updated), `"agent-context-go"`))
}

func TestJSONWriterVSCodeShape(t *testing.T) {
	w := jsonWriter{key: "servers", withType: true}
	out, err := w.upsert(nil, testSrv)
	require.NoError(t, err)

	var parsed struct {
		Servers map[string]struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"servers"`
	}
	require.NoError(t, json.Unmarshal(out, &parsed))
	e := parsed.Servers["agent-context-go"]
	assert.Equal(t, "stdio", e.Type)
	assert.Equal(t, testSrv.Command, e.Command)
}

func TestJSONWriterRemove(t *testing.T) {
	w := jsonWriter{key: "mcpServers"}
	withServer, err := w.upsert(nil, testSrv)
	require.NoError(t, err)

	out, found, err := w.remove(withServer, testSrv.Name)
	require.NoError(t, err)
	assert.True(t, found)
	assert.NotContains(t, string(out), "agent-context-go")

	_, found, err = w.remove(out, testSrv.Name)
	require.NoError(t, err)
	assert.False(t, found, "removing a second time reports not found")
}

func TestOpenCodeWriterShape(t *testing.T) {
	w := openCodeWriter{}
	srv := Server{Name: "agent-context-go", Command: "/bin/acg", Env: map[string]string{"CCG_EMBED_PROVIDER": "ollama"}}
	out, err := w.upsert(nil, srv)
	require.NoError(t, err)

	var parsed struct {
		Schema string `json:"$schema"`
		MCP    map[string]struct {
			Type        string            `json:"type"`
			Command     []string          `json:"command"`
			Enabled     bool              `json:"enabled"`
			Environment map[string]string `json:"environment"`
		} `json:"mcp"`
	}
	require.NoError(t, json.Unmarshal(out, &parsed))
	assert.NotEmpty(t, parsed.Schema)
	e := parsed.MCP["agent-context-go"]
	assert.Equal(t, "local", e.Type)
	assert.Equal(t, []string{"/bin/acg"}, e.Command, "command is an argv array")
	assert.True(t, e.Enabled)
	assert.Equal(t, "ollama", e.Environment["CCG_EMBED_PROVIDER"], "env uses the environment key")
}

func TestFileAgentInstallLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	agent := fileAgent{
		id: "test", name: "Test", reload: "restart",
		pathFn: func() (string, bool) { return path, true },
		writer: jsonWriter{key: "mcpServers"},
		manual: manualMcpServers,
	}

	// Dry run writes nothing.
	res := agent.Install(testSrv, true)
	assert.Equal(t, ActionInstalled, res.Action)
	assert.NoFileExists(t, path)

	// Real install creates the file, no backup (nothing existed).
	res = agent.Install(testSrv, false)
	assert.Equal(t, ActionInstalled, res.Action)
	assert.Empty(t, res.Backup)
	assert.FileExists(t, path)

	// Re-install is unchanged.
	res = agent.Install(testSrv, false)
	assert.Equal(t, ActionUnchanged, res.Action)

	// Changing the command updates and backs up the prior file.
	res = agent.Install(Server{Name: testSrv.Name, Command: "/other"}, false)
	assert.Equal(t, ActionUpdated, res.Action)
	assert.FileExists(t, res.Backup)

	// Uninstall removes the entry.
	res = agent.Uninstall(testSrv.Name, false)
	assert.Equal(t, ActionRemoved, res.Action)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "/other")
}

func TestFileAgentRefusesUnparseableConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	// A JSONC file with a comment: our strict parser can't handle it, so the
	// installer must skip rather than clobber it.
	original := "// user comment\n{\n  \"servers\": {}\n}\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	agent := fileAgent{
		id: "vscode", name: "VS Code", reload: "restart",
		pathFn: func() (string, bool) { return path, true },
		writer: jsonWriter{key: "servers", withType: true},
		manual: manualVSCode,
	}
	res := agent.Install(testSrv, false)
	assert.Equal(t, ActionSkipped, res.Action)
	assert.Contains(t, res.Note, "left untouched")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(after), "the file must be unchanged")
}

func TestFileAgentUnsupportedOS(t *testing.T) {
	agent := fileAgent{
		id: "x", name: "X",
		pathFn: func() (string, bool) { return "", false },
		writer: jsonWriter{key: "mcpServers"},
	}
	res := agent.Install(testSrv, false)
	assert.Equal(t, ActionSkipped, res.Action)
}

func TestCLIAgentBuildsAndRuns(t *testing.T) {
	var calls [][]string
	agent := cliAgent{
		id: "claude-code", name: "Claude Code", bin: "claude", reload: "restart",
		addArgs:    claudeAddArgs,
		removeArgs: func(name string) []string { return []string{"mcp", "remove", name, "-s", "user"} },
		lookPath:   func(string) (string, error) { return "/usr/bin/claude", nil },
		run: func(bin string, args ...string) (string, error) {
			calls = append(calls, append([]string{bin}, args...))
			return "", nil
		},
	}
	require.True(t, agent.Detected())

	res := agent.Install(testSrv, false)
	require.NoError(t, res.Err)
	assert.Equal(t, ActionInstalled, res.Action)
	// Remove-then-add for idempotency: two calls, add carries -s user and the command after --.
	require.Len(t, calls, 2)
	assert.Equal(t, []string{"claude", "mcp", "remove", "agent-context-go", "-s", "user"}, calls[0])
	add := calls[1]
	assert.Equal(t, []string{"claude", "mcp", "add", "agent-context-go", "-s", "user", "--", testSrv.Command}, add)
}

func TestCLIAgentSkippedWhenAbsent(t *testing.T) {
	agent := cliAgent{
		id: "codex", name: "Codex", bin: "codex",
		addArgs:  func(srv Server) []string { return []string{"mcp", "add", srv.Name} },
		manual:   codexManual,
		lookPath: func(string) (string, error) { return "", assertNotFound{} },
	}
	assert.False(t, agent.Detected())
	res := agent.Install(testSrv, false)
	assert.Equal(t, ActionSkipped, res.Action)
	assert.Contains(t, res.Note, "not found on PATH")
	assert.Contains(t, res.Note, "mcp_servers", "includes manual TOML instructions")
}

func TestSelectAndDetected(t *testing.T) {
	agents := New()
	assert.NotEmpty(t, agents)
	assert.Contains(t, IDs(agents), "claude-desktop")
	assert.Contains(t, IDs(agents), "codex")

	sel, unknown := Select(agents, []string{"cursor", "nope"})
	require.Len(t, sel, 1)
	assert.Equal(t, "cursor", sel[0].ID())
	assert.Equal(t, []string{"nope"}, unknown)
}

type assertNotFound struct{}

func (assertNotFound) Error() string { return "not found" }
