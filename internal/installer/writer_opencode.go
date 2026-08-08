package installer

import (
	"encoding/json"
	"fmt"
)

// openCodeWriter merges a server into OpenCode's config, whose shape differs
// from the mcpServers family: servers live under an "mcp" key, the command is
// an argv array, environment variables use the "environment" key, and each
// entry carries "type": "local" and "enabled": true.
type openCodeWriter struct{}

func (openCodeWriter) upsert(existing []byte, srv Server) ([]byte, error) {
	top, err := parseOMap(existing)
	if err != nil {
		return nil, err
	}
	// For a fresh file, add the schema hint OpenCode documents.
	if top.len() == 0 {
		top.set("$schema", json.RawMessage(`"https://opencode.ai/config.json"`))
	}

	section := newOMap()
	if raw, ok := top.get("mcp"); ok {
		if section, err = parseOMap(raw); err != nil {
			return nil, fmt.Errorf("parse mcp section: %w", err)
		}
	}
	entry, err := openCodeEntry(srv)
	if err != nil {
		return nil, err
	}
	section.set(srv.Name, entry)
	secRaw, err := section.MarshalJSON()
	if err != nil {
		return nil, err
	}
	top.set("mcp", secRaw)
	return top.indented()
}

func (openCodeWriter) remove(existing []byte, name string) ([]byte, bool, error) {
	top, err := parseOMap(existing)
	if err != nil {
		return existing, false, err
	}
	raw, ok := top.get("mcp")
	if !ok {
		return existing, false, nil
	}
	section, err := parseOMap(raw)
	if err != nil {
		return existing, false, fmt.Errorf("parse mcp section: %w", err)
	}
	if !section.delete(name) {
		return existing, false, nil
	}
	secRaw, err := section.MarshalJSON()
	if err != nil {
		return existing, false, err
	}
	top.set("mcp", secRaw)
	out, err := top.indented()
	return out, true, err
}

// openCodeEntry builds one OpenCode server object: {type, command[], enabled, environment?}.
func openCodeEntry(srv Server) (json.RawMessage, error) {
	e := newOMap()
	e.set("type", json.RawMessage(`"local"`))

	argv := append([]string{srv.Command}, srv.Args...)
	cmdRaw, err := json.Marshal(argv)
	if err != nil {
		return nil, err
	}
	e.set("command", cmdRaw)
	e.set("enabled", json.RawMessage(`true`))

	if len(srv.Env) > 0 {
		envRaw, err := json.Marshal(srv.Env)
		if err != nil {
			return nil, err
		}
		e.set("environment", envRaw)
	}
	return e.MarshalJSON()
}
