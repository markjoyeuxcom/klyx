package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// strictObject decodes data as exactly one JSON object and returns its
// top-level members undecoded. A duplicate key at any depth, a value that is
// not an object, or anything after the object other than whitespace is an
// error: encoding/json alone keeps the last of two duplicate keys and stops
// reading at the end of the first value, and either could hide a second,
// contrary answer from a pinned CLI contract (spec §1.5).
func strictObject(data []byte) (map[string]json.RawMessage, error) {
	if err := noDuplicateKeys(data); err != nil {
		return nil, err
	}
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

// noDuplicateKeys walks a JSON text and refuses a key repeated in any one
// object, at any depth (Codex review of 09cb2d8: a content block with two
// "type" keys must not read as whichever came last).
func noDuplicateKeys(data []byte) error {
	type frame struct {
		obj     bool
		wantKey bool
		keys    map[string]bool
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok && (d == '}' || d == ']') {
			stack = stack[:len(stack)-1]
			continue
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		if top != nil && top.obj && top.wantKey {
			k, ok := tok.(string)
			if !ok {
				return errors.New("malformed JSON object key")
			}
			if top.keys[k] {
				return fmt.Errorf("duplicate key %q", label(k))
			}
			top.keys[k], top.wantKey = true, false
			continue
		}
		if top != nil && top.obj {
			top.wantKey = true // after this value, the next token is a key
		}
		if d, ok := tok.(json.Delim); ok {
			stack = append(stack, &frame{obj: d == '{', wantKey: d == '{', keys: map[string]bool{}})
		}
	}
}

// rawString reads member k as a string; ok is false when it is absent, null
// or not a string.
func rawString(m map[string]json.RawMessage, k string) (string, bool) {
	raw := bytes.TrimSpace(m[k])
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// rawBool reads member k as a literal boolean; ok is false when it is
// absent, null or anything else (json.Unmarshal alone reads null as false).
func rawBool(m map[string]json.RawMessage, k string) (v, ok bool) {
	switch string(bytes.TrimSpace(m[k])) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
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
