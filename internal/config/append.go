package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// AppendClusters adds minimal cluster entries (name = context) to the fleet
// file by TEXTUAL insertion: the existing bytes - including comments and hand
// formatting - are preserved verbatim, and new `- name:` items are inserted at
// the actual end of the top-level `clusters:` list (clustersInsertion), with
// that list's indentation - wherever the list sits among the other fields. A
// missing file is created with a small header. The candidate result is parsed
// and validated - including that exactly the new clusters were added - BEFORE
// the file is replaced, so a bad insertion can never corrupt a working config.
func AppendClusters(path string, contexts []string) error {
	if len(contexts) == 0 {
		return fmt.Errorf("no contexts to add")
	}
	entries := make([]ClusterConfig, 0, len(contexts))
	for _, ctx := range contexts {
		entries = append(entries, ClusterConfigForContext(ctx))
	}

	original, readErr := os.ReadFile(path)
	exists := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return fmt.Errorf("read fleet config %q: %w", path, readErr)
	}

	// Refuse duplicates against the CURRENT file state (not the in-memory
	// startup config) so two adds in one session behave.
	seen := map[string]bool{}
	inFleet := map[string]bool{}
	if exists {
		cur, err := Load(path)
		switch {
		case errors.Is(err, errNoClusters):
			// An empty list (`clusters: []`, a bare `clusters:`, or none yet)
			// is exactly what adding clusters repairs; the result is fully
			// validated below.
		case err != nil:
			return fmt.Errorf("existing fleet config is invalid; fix it before adding clusters: %w", err)
		default:
			for _, c := range cur.Clusters {
				inFleet[c.Name] = true
				inFleet[c.Context] = true
			}
		}
	}
	for _, entry := range entries {
		keys := uniqueIdentityKeys(entry.Name, entry.Context)
		for _, key := range keys {
			if inFleet[key] {
				return fmt.Errorf("context %q is already in the fleet", entry.Context)
			}
			if seen[key] {
				return fmt.Errorf("context %q is duplicated in the add request", entry.Context)
			}
		}
		for _, key := range keys {
			seen[key] = true
		}
	}

	var candidate string
	if exists {
		var err error
		if candidate, err = insertClusters(string(original), entries); err != nil {
			return fmt.Errorf("fleet config %q: %w", path, err)
		}
	} else {
		var b strings.Builder
		b.WriteString("# Klyx fleet configuration - clusters Klyx connects to.\n")
		b.WriteString("# Docs: each entry needs a name; context defaults to the name.\n")
		b.WriteString("clusters:\n")
		for _, entry := range entries {
			writeClusterEntry(&b, entry, "  ")
		}
		candidate = b.String()
	}

	// Validate the candidate by round-tripping it through the real loader.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".fleet-*.yaml")
	if err != nil {
		// Directory may not exist yet for the create-new path.
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
			return fmt.Errorf("create config directory: %w", mkErr)
		}
		tmp, err = os.CreateTemp(filepath.Dir(path), ".fleet-*.yaml")
		if err != nil {
			return fmt.Errorf("create temp config: %w", err)
		}
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // best-effort temp cleanup
	if _, err := tmp.WriteString(candidate); err != nil {
		_ = tmp.Close() // already returning the write error; close is cleanup
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	got, err := Load(tmpPath)
	if err != nil {
		return fmt.Errorf("appended config failed validation (file unchanged): %w", err)
	}
	if exists {
		if err := onlyClustersAdded(original, []byte(candidate), len(entries)); err != nil {
			return fmt.Errorf("appended config would change existing settings (file unchanged): %w", err)
		}
	}
	// The insertion must have landed IN the clusters list: still-valid YAML
	// with the entries nested somewhere else would silently drop them.
	added := map[string]bool{}
	for _, c := range got.Clusters {
		if !inFleet[c.Name] {
			added[c.Name] = true
		}
	}
	for _, entry := range entries {
		if !added[entry.Name] {
			return fmt.Errorf("appended config does not list cluster %q (file unchanged)", entry.Name)
		}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace fleet config: %w", err)
	}
	return nil
}

// insertClusters inserts entries into the top-level clusters list of an
// existing fleet document (review 2026-10-03 #12: a plain EOF append broke
// whenever another field - e.g. `ai:` - followed the list, and a substring
// check accepted `clusters:` inside a comment). Formatting policy:
//   - block list: new items go at the end of its region - before the next
//     top-level field and any unindented comment lines leading into it -
//     indented like its existing items;
//   - `clusters:` with no value, or an empty inline `[]`: the items become the
//     list's block content under that key;
//   - no clusters field (or an empty document): a `clusters:` block is
//     appended at the end;
//   - a non-empty inline list or an alias is refused with how to fix it -
//     rewriting it would reformat the user's file.
func insertClusters(original string, entries []ClusterConfig) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(original), &doc); err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	lines := strings.SplitAfter(original, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	items := func(indent string) string {
		var b strings.Builder
		for _, entry := range entries {
			writeClusterEntry(&b, entry, indent)
		}
		return b.String()
	}
	// insertAfter splices text after line n (1-based; 0 = file start).
	insertAfter := func(n int, text string) string {
		var b strings.Builder
		for i, l := range lines {
			if i == n {
				b.WriteString(text)
			}
			b.WriteString(l)
		}
		if n >= len(lines) {
			if len(lines) > 0 && !strings.HasSuffix(lines[len(lines)-1], "\n") {
				b.WriteByte('\n')
			}
			b.WriteString(text)
		}
		return b.String()
	}

	var root *yaml.Node
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root = doc.Content[0]
	}
	if root == nil || (root.Kind == yaml.ScalarNode && root.Tag == "!!null") {
		return insertAfter(len(lines), "clusters:\n"+items("  ")), nil
	}
	if root.Kind != yaml.MappingNode {
		return "", fmt.Errorf("top level is not a mapping")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i], root.Content[i+1]
		if key.Value != "clusters" {
			continue
		}
		// Where the list's region ends: the next top-level field, or EOF.
		end := len(lines)
		if i+2 < len(root.Content) {
			end = root.Content[i+2].Line - 1
		}
		switch {
		case val.Kind == yaml.ScalarNode && val.Tag == "!!null":
			return insertAfter(key.Line, items("  ")), nil
		case val.Kind == yaml.SequenceNode && val.Style&yaml.FlowStyle != 0:
			if len(val.Content) > 0 {
				return "", fmt.Errorf("the clusters list is written inline ([...]); rewrite it as a block list (one `- name:` per line) to add clusters")
			}
			// `clusters: []` -> `clusters:` followed by the block items.
			l := lines[val.Line-1]
			col := val.Column - 1
			if col+2 > len(l) || l[col:col+2] != "[]" {
				return "", fmt.Errorf("cannot locate the empty clusters list to extend")
			}
			lines[val.Line-1] = strings.TrimRight(l[:col], " ") + l[col+2:]
			return insertAfter(val.Line, items("  ")), nil
		case val.Kind == yaml.SequenceNode:
			// Insert after the list's region, stepping back only over
			// UNINDENTED comment lines (they lead into the next field). Blank
			// lines and indented lines may be block-scalar content - a `|-`
			// line starting with '#', or the trailing newlines a `|+` keeps -
			// so they stay where they are (batch-4 review R1).
			last := end
			for last > val.Line && strings.HasPrefix(lines[last-1], "#") {
				last--
			}
			// Items are indented like the existing ones: the dash sits two
			// columns left of the item's content.
			indent := "  "
			if len(val.Content) > 0 && val.Content[0].Column >= 3 {
				indent = strings.Repeat(" ", val.Content[0].Column-3)
			}
			return insertAfter(last, items(indent)), nil
		default:
			return "", fmt.Errorf("the top-level clusters field is not a list")
		}
	}
	return insertAfter(len(lines), "clusters:\n"+items("  ")), nil
}

// onlyClustersAdded proves the textual insertion changed nothing but the
// clusters list's tail: decoded, every top-level field other than clusters is
// identical, and clusters is the original items, unchanged, followed by
// exactly added new ones. Loading the candidate only proves it is valid; a
// splice at the wrong line can still alter an existing value (batch-4 review
// R1: a block scalar lost content and still loaded).
func onlyClustersAdded(original, candidate []byte, added int) error {
	var before, after map[string]interface{}
	if err := yaml.Unmarshal(original, &before); err != nil {
		return err
	}
	if err := yaml.Unmarshal(candidate, &after); err != nil {
		return err
	}
	if len(after) != len(before) && !(len(after) == len(before)+1 && before["clusters"] == nil) {
		return fmt.Errorf("top-level fields differ")
	}
	for k, v := range before {
		if k == "clusters" {
			continue
		}
		if !reflect.DeepEqual(v, after[k]) {
			return fmt.Errorf("field %q changed", k)
		}
	}
	old, _ := before["clusters"].([]interface{})
	got, ok := after["clusters"].([]interface{})
	if !ok || len(got) != len(old)+added {
		return fmt.Errorf("clusters list has %d entries, want %d", len(got), len(old)+added)
	}
	for i := range old {
		if !reflect.DeepEqual(old[i], got[i]) {
			return fmt.Errorf("existing cluster entry %d changed", i)
		}
	}
	return nil
}

// writeClusterEntry writes one list item with its dash at indent.
func writeClusterEntry(b *strings.Builder, cc ClusterConfig, indent string) {
	field := indent + "  "
	fmt.Fprintf(b, "%s- name: %s\n", indent, yamlScalar(cc.Name))
	fmt.Fprintf(b, "%scontext: %s\n", field, yamlScalar(cc.Context))
	// Persist protection so a prod cluster imported mid-session stays protected
	// across restarts, not just for the current session.
	if cc.Protected {
		fmt.Fprintf(b, "%sprotected: true\n", field)
	}
	if len(cc.Tags) == 0 {
		return
	}
	keys := make([]string, 0, len(cc.Tags))
	for k := range cc.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(b, "%stags:\n", field)
	for _, k := range keys {
		fmt.Fprintf(b, "%s  %s: %s\n", field, yamlScalar(k), yamlTagScalar(cc.Tags[k]))
	}
}

func uniqueIdentityKeys(name, context string) []string {
	out := make([]string, 0, 2)
	if name != "" {
		out = append(out, name)
	}
	if context != "" && context != name {
		out = append(out, context)
	}
	return out
}

// yamlScalar quotes a value when it contains characters that would change its
// YAML meaning; kube context names like user@cluster are safe bare, but quote
// defensively for anything beyond the common shape.
func yamlScalar(s string) string {
	if strings.ContainsAny(s, ":#{}[]&*!|>'\"%@`, ") && !isSafeContext(s) {
		return fmt.Sprintf("%q", s)
	}
	return s
}

// isSafeContext allows the very common user@cluster shape without quotes.
func isSafeContext(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '.' || r == '_' || r == '@' || r == '/':
		default:
			return false
		}
	}
	return true
}

func yamlTagScalar(s string) string {
	if s == "" || isYAMLBoolLike(s) || isAllDigits(s) {
		return fmt.Sprintf("%q", s)
	}
	return yamlScalar(s)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isYAMLBoolLike(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return true
	default:
		return false
	}
}
