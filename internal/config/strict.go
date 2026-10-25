package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Keys Klyx does not know (Settings review slice 0): yaml.Unmarshal drops
// them, so a misspelled field silently became its default - `matches:` for
// an endpoint's `match:` made every alert of a shared Alertmanager this
// cluster's. A nested unknown key is now a load error naming where it is and
// the key it is likely meant to be. An unknown top-level key is a warning:
// a block an earlier Klyx read, or a YAML anchor's home, can sit there
// harmlessly. Map-valued fields (tags, match) take any key, and the legacy
// spellings Klyx still reads (ai.backend, ai.apiKey, metrics.token) are
// fields, so they are known.

const maxUnknownListed = 8

// decode parses, checks the keys, defaults and validates a fleet file's
// bytes: the one decoding both Load and a snapshot use.
func decode(raw []byte) (*Config, error) {
	var c Config
	if err := unmarshalFleet(raw, &c); err != nil {
		return nil, shown(err)
	}
	nested, top, err := unknownKeys(raw)
	if err != nil {
		return nil, shown(err)
	}
	if len(nested) > 0 {
		shown := nested
		if len(shown) > maxUnknownListed {
			shown = shown[:maxUnknownListed]
		}
		msg := strings.Join(shown, "; ")
		if more := len(nested) - len(shown); more > 0 {
			msg += fmt.Sprintf("; and %d more", more)
		}
		return nil, fmt.Errorf("unknown field%s: %s", plural(len(nested)), msg)
	}
	c.unknownTop = top
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, shown(err)
	}
	return &c, nil
}

// shownError is a decoding error whose message is already as Klyx shows
// it (DisplayError): every consumer - Settings, the panels, a writer's
// refusal, the log - gets it without the value it echoed (Codex's review of
// slice 0, finding 5).
type shownError struct{ err error }

func (e shownError) Error() string { return DisplayError(e.err) }
func (e shownError) Unwrap() error { return e.err }

func shown(err error) error { return shownError{err} }

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// unknownKeys walks the document against the Config type: nested are the
// unknown keys below the top level, each with its place, line and likely
// meaning; top the unknown top-level keys, for a warning.
func unknownKeys(raw []byte) (nested, top []string, err error) {
	var doc yaml.Node
	if err := unmarshalFleet(raw, &doc); err != nil {
		return nil, nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil, nil
	}
	w := keyWalk{}
	w.walk(doc.Content[0], reflect.TypeOf(Config{}), "")
	return w.nested, w.top, nil
}

type keyWalk struct {
	nested, top []string
	// walking are the aliases on the path being walked: every use of an
	// alias is checked against the schema it is used in - one read as a
	// free map certifies nothing about a struct (Codex's review of slice 0,
	// finding 2) - and only a cycle stops the walk
	walking map[*yaml.Node]bool
}

func (w *keyWalk) walk(n *yaml.Node, t reflect.Type, path string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if n.Kind == yaml.AliasNode {
		if n.Alias == nil || w.walking[n.Alias] {
			return // a cycle, which the decoder refuses
		}
		if w.walking == nil {
			w.walking = map[*yaml.Node]bool{}
		}
		w.walking[n.Alias] = true
		defer delete(w.walking, n.Alias)
		n = n.Alias
	}
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return // a type mismatch is the decoder's error
		}
		fields := yamlFields(t)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			// a merge key: the merged mappings are this struct's. Only a plain
			// << is one; a quoted '<<' is a string key the decoder ignores
			// (Codex's review of slice 0, finding 3)
			if k.Kind == yaml.ScalarNode && k.ShortTag() == "!!merge" {
				w.merge(v, t, path)
				continue
			}
			ft, ok := fields[k.Value]
			if !ok {
				if path == "" {
					w.top = append(w.top, fmt.Sprintf("unknown top-level key %s (line %d) is ignored", describeKey(k.Value), k.Line))
				} else {
					w.nested = append(w.nested, unknownAt(path, k, fields))
				}
				continue
			}
			w.walk(v, ft, joinPath(path, k.Value))
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return
		}
		for i, item := range n.Content {
			w.walk(item, t.Elem(), itemPath(path, i, item))
		}
	}
	// maps and scalars take any key or value
}

func (w *keyWalk) merge(v *yaml.Node, t reflect.Type, path string) {
	if v.Kind == yaml.SequenceNode {
		for _, m := range v.Content {
			w.walk(m, t, path)
		}
		return
	}
	w.walk(v, t, path)
}

// yamlFields are a struct's YAML keys and their types, as yaml.v3 names
// them: the tag's name, or the lowercased field name.
func yamlFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = strings.ToLower(f.Name)
		}
		out[name] = f.Type
	}
	return out
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// itemPath names a list item, with its name when it has one: clusters[2]
// (k8s-b).
func itemPath(path string, i int, item *yaml.Node) string {
	p := fmt.Sprintf("%s[%d]", path, i)
	if item.Kind == yaml.MappingNode {
		for j := 0; j+1 < len(item.Content); j += 2 {
			if item.Content[j].Value == "name" && item.Content[j+1].Kind == yaml.ScalarNode && item.Content[j+1].Value != "" {
				return p + " (" + clip(item.Content[j+1].Value, 64) + ")"
			}
		}
	}
	return p
}

func unknownAt(path string, k *yaml.Node, fields map[string]reflect.Type) string {
	s := fmt.Sprintf("%s: %s at line %d", path, describeKey(k.Value), k.Line)
	if near := nearest(k.Value, fields); near != "" {
		s += fmt.Sprintf(" (did you mean %q?)", near)
	}
	return s
}

// describeKey names a key in a diagnostic: quoted, or, at 25 characters or
// more, by its length alone - a choice of what a diagnostic shows, not a
// limit on what the file may hold: a key that long may be a pasted value. Every diagnostic that names a key the file
// holds uses it (Codex's re-review of slice 0).
func describeKey(k string) string {
	if n := len([]rune(k)); n >= 25 {
		return fmt.Sprintf("a key of %d characters", n)
	}
	return fmt.Sprintf("%q", k)
}

// nearest is the known key within two edits of key, if one is.
func nearest(key string, fields map[string]reflect.Type) string {
	best, bestD := "", 3
	for name := range fields {
		if d := editDistance(strings.ToLower(key), strings.ToLower(name)); d < bestD || d == bestD && name < best {
			best, bestD = name, d
		}
	}
	if bestD > 2 {
		return ""
	}
	return best
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// echoedValue is a value an error echoes back: a YAML type error quotes it
// in backticks, a validation error as (got "..."), escaped. A long one may
// be a credential pasted into the wrong field - "Bearer <key>" included -
// so it is withheld, spaces and all.
var echoedValue = regexp.MustCompile("`[^`]{25,}`|\\(got \"(?:[^\"\\\\]|\\\\.){25,}\"\\)")

// DisplayError is a fleet-file error as Klyx shows it: an echoed long value
// is withheld, so a key pasted into the wrong field is never put on screen;
// a short one (a typo of an enum) and the file's path stay, to be fixed.
func DisplayError(err error) string {
	if err == nil {
		return ""
	}
	return echoedValue.ReplaceAllStringFunc(err.Error(), func(m string) string {
		if strings.HasPrefix(m, "`") {
			return fmt.Sprintf("`a value of %d characters, withheld`", len([]rune(m))-2)
		}
		return fmt.Sprintf("(got a value of %d characters, withheld)", len([]rune(m))-len(`(got "")`))
	})
}

// unmarshalFleet decodes the fleet file's bytes: every decode of it - the
// loader's, the key walk's and each writer's round-trip check - goes
// through here, so a YAML error reaches no one with the file's content in
// it (Codex's second re-review of slice 0).
func unmarshalFleet(b []byte, v any) error {
	return readableYAML(yaml.Unmarshal(b, v))
}

// readableYAML is a YAML error as Klyx passes it on. yaml.v3's parser and
// scanner messages are fixed strings and pass unchanged. The decoder's few
// messages that carry the file's content - an anchor's name, a list or map
// used as a key, a custom tag, a key given twice - are rewritten to their
// kind and line, the key by describeKey. Reviewed against yaml.v3 v3.0.1's
// producers (decode.go); a version change re-reviews them.
func readableYAML(err error) error {
	if err == nil {
		return nil
	}
	var te *yaml.TypeError
	if errors.As(err, &te) {
		issues := make([]string, len(te.Errors))
		for i, e := range te.Errors {
			issues[i] = readableIssue(e)
		}
		return errors.New("yaml: unmarshal errors:\n  " + strings.Join(issues, "\n  "))
	}
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "yaml: unknown anchor "):
		return errors.New("yaml: an alias names an anchor the file does not define")
	case strings.HasPrefix(msg, "yaml: anchor ") && strings.HasSuffix(msg, " value contains itself"):
		return errors.New("yaml: an anchor's value contains itself")
	case strings.HasPrefix(msg, "yaml: invalid map key:"):
		return errors.New("yaml: a key is a list or a map, which a fleet file cannot use")
	}
	return err
}

var (
	issueLine    = regexp.MustCompile(`^line (\d+): `)
	issueDupKey  = regexp.MustCompile(`^line (\d+): mapping key ("(?:[^"\\]|\\.)*") already defined at line (\d+)$`)
	issueUnmarsh = regexp.MustCompile(`^line (\d+): cannot unmarshal (\S+)( .*)? into (.+)$`)
)

// standardTags are YAML's own tags: a type error naming one names nothing
// of the file's beyond a value yaml.v3 already cut to seven characters.
var standardTags = map[string]bool{"!!str": true, "!!int": true, "!!bool": true, "!!float": true, "!!null": true,
	"!!map": true, "!!seq": true, "!!timestamp": true, "!!binary": true, "!!merge": true}

func readableIssue(e string) string {
	if m := issueDupKey.FindStringSubmatch(e); m != nil {
		k, err := strconv.Unquote(m[2])
		if err != nil {
			k = m[2]
		}
		return fmt.Sprintf("line %s: mapping key %s already defined at line %s", m[1], describeKey(k), m[3])
	}
	if m := issueUnmarsh.FindStringSubmatch(e); m != nil {
		if standardTags[m[2]] {
			return e
		}
		return fmt.Sprintf("line %s: cannot unmarshal a value with a custom tag into %s", m[1], m[4])
	}
	if m := issueLine.FindStringSubmatch(e); m != nil {
		return fmt.Sprintf("line %s: a value Klyx cannot read", m[1])
	}
	return "a value Klyx cannot read"
}

// IsMissing reports a fleet file that does not exist yet: a first run, not
// an error.
func IsMissing(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// readFleetFile reads the fleet file. Only an absent path is a first run: a
// symlink whose target is gone is an error, said like any other (Codex's
// review of slice 0, finding 6).
func readFleetFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("it is a symlink whose target does not exist")
		}
	}
	return b, err
}
