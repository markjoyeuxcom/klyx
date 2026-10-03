package helmcli

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// redactedValue is what a masked helm value leaf renders as. Chosen to be an
// obvious, copy-safe placeholder (the masked document reaches the frontend AND
// the clipboard).
const redactedValue = "<redacted>"

// MaskValues parses a helm values YAML document and returns a copy with every
// sensitive leaf - a scalar under a password/secret/token/key/credential-shaped
// key - replaced by redactedValue, plus the sorted dotted paths of the redacted
// leaves. This upholds the ratified invariant that secret values never leak via
// helm values: the masked document is what reaches the UI and the clipboard,
// while an individual value is fetched on explicit request via ValueAtPath.
//
// Empty input returns empty. A YAML parse error is returned so the caller can
// FAIL CLOSED (render nothing) rather than fall back to the raw document. Keys
// are re-emitted in sorted order (map marshalling is not order-preserving); the
// masking is heuristic on free-form YAML, biased to over-mask - a false positive
// only hides a non-secret behind a reveal click, a false negative leaks a real
// secret.
func MaskValues(raw string) (masked string, paths []string, err error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil, nil
	}
	var root interface{}
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		return "", nil, fmt.Errorf("parse helm values: %w", err)
	}
	var collected []string
	red := maskNode(root, "", false, &collected)
	out, err := yaml.Marshal(red)
	if err != nil {
		return "", nil, fmt.Errorf("re-encode helm values: %w", err)
	}
	sort.Strings(collected)
	return strings.TrimRight(string(out), "\n"), collected, nil
}

// ValueAtPath returns the scalar leaf at the given dotted path (the same path
// form MaskValues emits) in a helm values document, for on-demand reveal. found
// is false when the path is absent or names a non-scalar.
func ValueAtPath(raw, path string) (value string, found bool, err error) {
	var root interface{}
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		return "", false, fmt.Errorf("parse helm values: %w", err)
	}
	walkLeaves(root, "", func(p string, v interface{}) {
		if p == path {
			value = scalarString(v)
			found = true
		}
	})
	return value, found, nil
}

func maskNode(v interface{}, path string, keySensitive bool, paths *[]string) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		nameLooksSecret := envEntryNameLooksSecret(t["name"])
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[k] = maskNode(val, joinPath(path, k), childSensitive(keySensitive, nameLooksSecret, k), paths)
		}
		return out
	case map[interface{}]interface{}:
		// yaml.v3 decodes a mapping with ANY non-string key (e.g. an integer
		// port) into map[interface{}]interface{}. Without this case the whole
		// subtree fell through to the scalar branch and every secret nested
		// under it was returned raw. Keys keep their original type so the
		// re-encoded document is unchanged; paths use their string form, the
		// same form walkLeaves uses, so ValueAtPath can still reveal them.
		nameLooksSecret := envEntryNameLooksSecret(t["name"])
		out := make(map[interface{}]interface{}, len(t))
		for k, val := range t {
			ks := fmt.Sprint(k)
			out[k] = maskNode(val, joinPath(path, ks), childSensitive(keySensitive, nameLooksSecret, ks), paths)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			// A sensitive key over a list (e.g. tokens: [a, b]) masks each element.
			out[i] = maskNode(val, fmt.Sprintf("%s[%d]", path, i), keySensitive, paths)
		}
		return out
	default:
		// Skip empty/nil scalars: masking "" as <redacted> would falsely imply a
		// secret is set (e.g. existingSecret: "").
		if keySensitive && !isEmptyScalar(t) {
			*paths = append(*paths, path)
			return redactedValue
		}
		return t
	}
}

func isEmptyScalar(v interface{}) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && s == ""
}

func walkLeaves(v interface{}, path string, visit func(string, interface{})) {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, val := range t {
			walkLeaves(val, joinPath(path, k), visit)
		}
	case map[interface{}]interface{}:
		for k, val := range t {
			walkLeaves(val, joinPath(path, fmt.Sprint(k)), visit)
		}
	case []interface{}:
		for i, val := range t {
			walkLeaves(val, fmt.Sprintf("%s[%d]", path, i), visit)
		}
	default:
		visit(path, v)
	}
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func scalarString(v interface{}) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

// childSensitive reports whether the value under key k must be masked. Masking
// is inherited down the whole subtree: once an ancestor key looks like a secret
// (credentials: {value: x}, tokens: [{id: a}]) every descendant leaf is masked,
// whatever its own key - a leaf named "value" or "username" under "credentials"
// is still part of the credential. Otherwise the key itself decides, plus the
// k8s-style env entry rule below.
func childSensitive(parentSensitive, nameLooksSecret bool, k string) bool {
	if parentSensitive || isSensitiveKey(k) {
		return true
	}
	// k8s-style env entry {name: FOO_PASSWORD, value: bar}: the secret is in
	// `value` but the KEY is "value", so key matching alone misses it. Treat
	// `value` (and `default`) as sensitive when the sibling `name` looks like a
	// secret.
	return nameLooksSecret && (k == "value" || k == "default")
}

// envEntryNameLooksSecret reports whether a mapping's `name` field (the k8s env
// entry shape) names a secret.
func envEntryNameLooksSecret(name interface{}) bool {
	nv, ok := name.(string)
	return ok && isSensitiveKey(nv)
}

// isSensitiveKey reports whether a values key names a secret. Deliberately broad
// (see MaskValues): substring match against common secret tokens, plus a "*Key"
// suffix at a WORD BOUNDARY (camelCase "apiKey", or "_key"/"-key"), so a plain
// word that merely ends in "key" (monkey, donkey) is not caught.
func isSensitiveKey(k string) bool {
	lk := strings.ToLower(k)
	for _, s := range []string{
		"password", "passwd", "pwd", "passphrase",
		"secret", "token", "apikey", "accesskey", "privatekey",
		"credential", "cred", "sessionkey", "encryptionkey",
	} {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return lk == "key" || strings.HasSuffix(k, "Key") || strings.HasSuffix(lk, "_key") || strings.HasSuffix(lk, "-key")
}
