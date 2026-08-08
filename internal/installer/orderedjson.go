package installer

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// omap is a JSON object that preserves key insertion order across a
// read-modify-write. Config files belong to the user, so merging our entry must
// not reorder or drop their existing keys. Values are kept as raw JSON, so any
// nested object we do not touch is preserved byte-for-byte (including its own
// key order and formatting choices for scalars).
type omap struct {
	keys []string
	vals map[string]json.RawMessage
}

func newOMap() *omap {
	return &omap{vals: map[string]json.RawMessage{}}
}

// parseOMap parses a JSON object, preserving key order. Empty input yields an
// empty map. It errors if the input is non-empty and not a JSON object.
func parseOMap(b []byte) (*omap, error) {
	m := newOMap()
	if len(bytes.TrimSpace(b)) == 0 {
		return m, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected a JSON object at top level")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("read object key: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("object key is not a string")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("read value for %q: %w", key, err)
		}
		m.set(key, raw)
	}
	return m, nil
}

func (m *omap) get(key string) (json.RawMessage, bool) {
	v, ok := m.vals[key]
	return v, ok
}

// set replaces the value for key in place, or appends it if new.
func (m *omap) set(key string, v json.RawMessage) {
	if _, ok := m.vals[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = v
}

// delete removes key, reporting whether it was present.
func (m *omap) delete(key string) bool {
	if _, ok := m.vals[key]; !ok {
		return false
	}
	delete(m.vals, key)
	for i, k := range m.keys {
		if k == key {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			break
		}
	}
	return true
}

func (m *omap) len() int { return len(m.keys) }

// MarshalJSON renders the object with keys in their preserved order.
func (m *omap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(m.vals[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// indented returns the object pretty-printed with two-space indentation and a
// trailing newline, the conventional shape for these config files.
func (m *omap) indented() ([]byte, error) {
	raw, err := m.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}
