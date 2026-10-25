package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// saveTopLevelBlock replaces the fleet file's top-level key block with block
// (or appends one) by TEXTUAL replacement: every other byte - comments,
// formatting, the clusters list - is preserved; comments inside the old
// block are not. The candidate is validated through the real loader, must
// decode with every other top-level key unchanged, and must pass check
// (reading back exactly the intended settings) before it replaces the file.
// It shares the fleet write lock with AppendClusters and refuses to
// overwrite an edit made on disk meanwhile (spec 2026-10-13 §2.3). SaveAI
// and SaveGit write through it (The Line slice 6 note §5.1). pre, when set,
// checks the file as it is before the write: a writer whose block came from
// an earlier read refuses an edit made since (Settings review slice 0).
func saveTopLevelBlock(path, key, block, what string, pre, check func(*Config) error) error {
	fleetWriteMu.Lock()
	defer fleetWriteMu.Unlock()

	target, mode, _, err := fleetTarget(path)
	if err != nil {
		return err
	}
	original, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("there is no fleet config at %q yet - add a cluster first", path)
		}
		return fmt.Errorf("read fleet config %q: %w", path, err)
	}
	if pre != nil {
		cur, err := decode(original)
		if err != nil {
			return fmt.Errorf("fleet config %q does not load, so the %s were not saved: %w", path, what, err)
		}
		if err := pre(cur); err != nil {
			return err
		}
	}
	candidate, err := replaceTopLevelBlock(string(original), key, block)
	if err != nil {
		return fmt.Errorf("fleet config %q: %w", path, err)
	}

	tmp, err := fleetTemp(target, mode)
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // best-effort temp cleanup
	if _, err := tmp.WriteString(candidate); err != nil {
		_ = tmp.Close() // already returning the write error
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	got, err := Load(tmpPath)
	if err != nil {
		return fmt.Errorf("the %s failed validation (file unchanged): %w", what, err)
	}
	if err := onlyKeyChanged(original, []byte(candidate), key); err != nil {
		return fmt.Errorf("saving %s would change other settings (file unchanged): %w", what, err)
	}
	if err := check(got); err != nil {
		return err
	}
	return replaceUnchanged(path, target, tmpPath, original, true)
}

// replaceTopLevelBlock swaps the top-level key block in doc for block, or
// appends block when there is none. The old block runs from its key line to
// the next column-0 line (another key, a column-0 comment, a document
// marker); trailing blank lines stay where they were.
func replaceTopLevelBlock(doc, key, block string) (string, error) {
	lines := strings.SplitAfter(doc, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start := -1
	for i, l := range lines {
		if isTopLevelKey(l, key) {
			if start >= 0 {
				return "", fmt.Errorf("the file has more than one top-level %s: key", key)
			}
			start = i
		}
	}
	if start < 0 {
		out := doc
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		return out + block, nil
	}
	end := start + 1
	for end < len(lines) {
		l := lines[end]
		if strings.TrimSpace(l) == "" || l[0] == ' ' || l[0] == '\t' {
			end++
			continue
		}
		break
	}
	for end > start+1 && strings.TrimSpace(lines[end-1]) == "" {
		end-- // trailing blank lines belong to what follows
	}
	return strings.Join(lines[:start], "") + block + strings.Join(lines[end:], ""), nil
}

// isTopLevelKey reports whether line is the column-0 mapping key name.
func isTopLevelKey(line, name string) bool {
	if !strings.HasPrefix(line, name) {
		return false
	}
	rest := strings.TrimLeft(line[len(name):], " \t")
	return strings.HasPrefix(rest, ":")
}

// onlyKeyChanged confirms that, decoded, every top-level key except key is
// identical before and after.
func onlyKeyChanged(before, after []byte, key string) error {
	var a, b map[string]any
	if err := yaml.Unmarshal(before, &a); err != nil {
		return fmt.Errorf("decode current config: %w", err)
	}
	if err := yaml.Unmarshal(after, &b); err != nil {
		return fmt.Errorf("decode new config: %w", err)
	}
	delete(a, key)
	delete(b, key)
	if !reflect.DeepEqual(a, b) {
		return errors.New("a setting outside " + key + " would change")
	}
	return nil
}
