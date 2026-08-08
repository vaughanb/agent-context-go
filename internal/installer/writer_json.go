package installer

import (
	"encoding/json"
	"fmt"
)

// jsonWriter merges a server into a JSON config whose servers live under a
// top-level object key. It covers the two JSON shapes in the wild:
//
//   - key "mcpServers", no type field: Claude Desktop, Cursor, Windsurf, Cline,
//     Claude Code.
//   - key "servers", with "type": "stdio": VS Code (GitHub Copilot).
//
// It preserves the order and contents of every key it does not own.
type jsonWriter struct {
	key      string // top-level key holding the servers object
	withType bool   // include "type": "stdio" on each entry (VS Code)
}

func (w jsonWriter) upsert(existing []byte, srv Server) ([]byte, error) {
	top, err := parseOMap(existing)
	if err != nil {
		return nil, err
	}
	section := newOMap()
	if raw, ok := top.get(w.key); ok {
		if section, err = parseOMap(raw); err != nil {
			return nil, fmt.Errorf("parse %q section: %w", w.key, err)
		}
	}
	entry, err := w.entry(srv)
	if err != nil {
		return nil, err
	}
	section.set(srv.Name, entry)
	secRaw, err := section.MarshalJSON()
	if err != nil {
		return nil, err
	}
	top.set(w.key, secRaw)
	return top.indented()
}

func (w jsonWriter) remove(existing []byte, name string) ([]byte, bool, error) {
	top, err := parseOMap(existing)
	if err != nil {
		return existing, false, err
	}
	raw, ok := top.get(w.key)
	if !ok {
		return existing, false, nil
	}
	section, err := parseOMap(raw)
	if err != nil {
		return existing, false, fmt.Errorf("parse %q section: %w", w.key, err)
	}
	if !section.delete(name) {
		return existing, false, nil
	}
	secRaw, err := section.MarshalJSON()
	if err != nil {
		return existing, false, err
	}
	top.set(w.key, secRaw)
	out, err := top.indented()
	return out, true, err
}

// entry builds one server object with a readable field order.
func (w jsonWriter) entry(srv Server) (json.RawMessage, error) {
	e := newOMap()
	if w.withType {
		e.set("type", json.RawMessage(`"stdio"`))
	}
	cmd, err := json.Marshal(srv.Command)
	if err != nil {
		return nil, err
	}
	e.set("command", cmd)

	args := srv.Args
	if args == nil {
		args = []string{}
	}
	argsRaw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	e.set("args", argsRaw)

	if len(srv.Env) > 0 {
		envRaw, err := json.Marshal(srv.Env)
		if err != nil {
			return nil, err
		}
		e.set("env", envRaw)
	}
	return e.MarshalJSON()
}
