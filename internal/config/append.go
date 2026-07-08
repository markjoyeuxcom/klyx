package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AppendClusters adds minimal cluster entries (name = context) to the fleet
// file by TEXTUAL append: the existing bytes - including comments and hand
// formatting - are preserved verbatim, and new `- name:` items are appended
// under the existing top-level `clusters:` list. A missing file is created
// with a small header. The candidate result is parsed and validated BEFORE the
// file is replaced, so a bad append can never corrupt a working config.
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
		if err != nil {
			return fmt.Errorf("existing fleet config is invalid; fix it before adding clusters: %w", err)
		}
		for _, c := range cur.Clusters {
			inFleet[c.Name] = true
			inFleet[c.Context] = true
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

	var b strings.Builder
	if exists {
		b.Write(original)
		if len(original) > 0 && original[len(original)-1] != '\n' {
			b.WriteByte('\n')
		}
		// The file must already carry a top-level clusters: list for a plain
		// item append to land in the right place.
		if !strings.Contains(string(original), "clusters:") {
			return fmt.Errorf("fleet config %q has no top-level clusters: list", path)
		}
	} else {
		b.WriteString("# Klyx fleet configuration - clusters Klyx connects to.\n")
		b.WriteString("# Docs: each entry needs a name; context defaults to the name.\n")
		b.WriteString("clusters:\n")
	}
	for _, entry := range entries {
		writeClusterEntry(&b, entry)
	}
	candidate := b.String()

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
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(candidate); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if _, err := Load(tmpPath); err != nil {
		return fmt.Errorf("appended config failed validation (file unchanged): %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace fleet config: %w", err)
	}
	return nil
}

func writeClusterEntry(b *strings.Builder, cc ClusterConfig) {
	fmt.Fprintf(b, "  - name: %s\n", yamlScalar(cc.Name))
	fmt.Fprintf(b, "    context: %s\n", yamlScalar(cc.Context))
	// Persist protection so a prod cluster imported mid-session stays protected
	// across restarts, not just for the current session.
	if cc.Protected {
		b.WriteString("    protected: true\n")
	}
	if len(cc.Tags) == 0 {
		return
	}
	keys := make([]string, 0, len(cc.Tags))
	for k := range cc.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString("    tags:\n")
	for _, k := range keys {
		fmt.Fprintf(b, "      %s: %s\n", yamlScalar(k), yamlTagScalar(cc.Tags[k]))
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
