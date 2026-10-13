package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// strictObject decodes data as exactly one JSON object and returns its
// top-level members undecoded. A duplicate key, a value that is not an
// object, or anything after the object other than whitespace is an error:
// encoding/json alone keeps the last of two duplicate keys and stops reading
// at the end of the first value, and either could hide a second, contrary
// answer from a pinned CLI contract (spec §1.5).
func strictObject(data []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, ok := tok.(string)
		if !ok {
			return nil, errors.New("malformed JSON object key")
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("duplicate key %q", label(k))
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("data after the JSON object")
	}
	return out, nil
}

// rawString reads member k as a string; ok is false when it is absent or not
// a string.
func rawString(m map[string]json.RawMessage, k string) (string, bool) {
	raw, present := m[k]
	if !present {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// rawBool reads member k as a boolean; ok is false when it is absent or not
// a boolean.
func rawBool(m map[string]json.RawMessage, k string) (v, ok bool) {
	raw, present := m[k]
	if !present {
		return false, false
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, false
	}
	return v, true
}

// rawNull reports whether member k is absent or JSON null.
func rawNull(m map[string]json.RawMessage, k string) bool {
	raw, present := m[k]
	return !present || string(bytes.TrimSpace(raw)) == "null"
}

// rawEmpty reports whether member k is absent, null, zero, or an empty
// array, object or string: nothing happened.
func rawEmpty(m map[string]json.RawMessage, k string) bool {
	if rawNull(m, k) {
		return true
	}
	switch string(bytes.TrimSpace(m[k])) {
	case "[]", "{}", "0", `""`:
		return true
	}
	return false
}

// label bounds a vendor-supplied identifier (an event type, a key name) to a
// short run of identifier characters before it goes into an error message:
// it names what was refused without echoing vendor-controlled prose.
func label(s string) string {
	var b []byte
	for i := 0; i < len(s) && len(b) < 40; i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == '/' {
			b = append(b, c)
		}
	}
	if len(b) == 0 {
		return "?"
	}
	return string(b)
}
