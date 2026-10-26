package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Settings review slice 0: a nested key Klyx does not know is a load error
// that says where it is and what it likely means; a top-level one is a
// warning; map-valued fields take any key; the legacy spellings still load.
func TestUnknownKeys(t *testing.T) {
	for _, c := range []struct {
		name, doc string
		err       string // "" for a load
		warn      string
	}{
		{"a typo of match", "clusters:\n  - name: a\n    alertmanager:\n      endpoint: https://am.example\n      matches: {cluster: a}\n", `clusters[0] (a).alertmanager: "matches" at line 5 (did you mean "match"?)`, ""},
		{"a typo of protected", "clusters:\n  - name: a\n    protcted: true\n", `clusters[0] (a): "protcted" at line 3 (did you mean "protected"?)`, ""},
		{"an unknown ai key", "clusters:\n  - name: a\nai:\n  provider: off\n  temprature: 1\n", `ai: "temprature" at line 5`, ""},
		{"no likely meaning", "clusters:\n  - name: a\n    zzzzzz: 1\n", `clusters[0] (a): "zzzzzz" at line 3`, ""},
		{"an unknown top-level key warns", "clusters:\n  - name: a\nserve:\n  port: 1\n", "", `unknown top-level key "serve" (line 3) is ignored`},
		{"tags and match take any key", "clusters:\n  - name: a\n    tags: {anything: x, region: we}\n    alertmanager:\n      endpoint: https://am.example\n      match: {k8s_cluster: a}\n", "", ""},
		{"legacy spellings load", "clusters:\n  - name: a\n    metrics:\n      endpoint: https://m.example\n      token: t\nai:\n  backend: off\n", "", ""},
		{"a merge key", "x-base: &base\n  context: ctx\nclusters:\n  - name: a\n    <<: *base\n", "", `unknown top-level key "x-base"`},
		{"a merged unknown key", "x-base: &base\n  contxt: ctx\nclusters:\n  - name: a\n    <<: *base\n", `clusters[0] (a): "contxt" at line 2 (did you mean "context"?)`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fleet.yaml")
			if err := os.WriteFile(path, []byte(c.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("want an error with %q, got %v", c.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			w := strings.Join(cfg.Warnings(), "\n")
			if c.warn != "" && !strings.Contains(w, c.warn) || c.warn == "" && strings.Contains(w, "unknown top-level") {
				t.Fatalf("warnings: %s", w)
			}
		})
	}
}

// A snapshot decodes the same way: an endpoint write never adopts a file
// whose unknown key the loader refuses.
func TestASnapshotRefusesAnUnknownKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte("clusters:\n  - name: a\n    lokki: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(path); err == nil || !strings.Contains(err.Error(), `"lokki"`) {
		t.Fatal(err)
	}
}

// An error is shown with a long quoted value withheld - a key pasted into
// the wrong field is never put on screen - and a short one kept, to be
// fixed; a missing file is a first run.
func TestDisplayError(t *testing.T) {
	long := "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	got := DisplayError(errors.New("ai.provider must be off, anthropic (got \"" + long + "\"); line 3: cannot unmarshal !!str `" + long + "` into bool; (got \"antrhopic\")"))
	if strings.Contains(got, long) || !strings.Contains(got, `(got a value of 39 characters, withheld)`) || !strings.Contains(got, "`a value of 39 characters, withheld`") || !strings.Contains(got, `"antrhopic"`) {
		t.Fatal(got)
	}
	// the file's path and a key's name are never withheld
	msg := `invalid config "/Users/someone/.config/klyx/dotfiles/fleet.yaml": unknown field: clusters[0] (a).alertmanager: "matches" at line 4 (did you mean "match"?)`
	if got := DisplayError(errors.New(msg)); got != msg {
		t.Fatal(got)
	}
	_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if !IsMissing(err) {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte("clusters: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || IsMissing(err) {
		t.Fatal(err)
	}
}

// Settings review slice 0: every fleet-file writer keeps a symlink a link -
// its target changes - and keeps the target's permissions. A link that does
// not resolve, or that is repointed during the write, is refused.
func TestFleetWritersKeepASymlink(t *testing.T) {
	base := "clusters:\n  - name: a\n    context: a\n    metrics:\n      endpoint: https://m.example\n      token: tok\n"
	writers := map[string]func(path string) error{
		"add clusters": func(p string) error { return AppendClusters(p, []string{"b"}) },
		"ai":           func(p string) error { return SaveAI(p, AIConfig{Provider: "off"}) },
		"git":          func(p string) error { return SaveGit(p, GitConfig{}) },
		"notifications": func(p string) error {
			return SaveNotifications(p, NotificationsConfig{Enabled: true})
		},
		"metrics token": func(p string) error {
			snap, err := ReadSnapshot(p)
			if err != nil {
				return err
			}
			_, err = MoveMetricsTokenToAuth(snap, "a")
			return err
		},
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "dotfiles", "fleet.yaml")
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte(base), 0o644); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dir, "fleet.yaml")
			if err := os.Symlink(filepath.Join("dotfiles", "fleet.yaml"), link); err != nil {
				t.Fatal(err)
			}
			if err := write(link); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Lstat(link)
			if err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("the link was replaced: %v %v", fi.Mode(), err)
			}
			b, _ := os.ReadFile(target)
			if string(b) == base {
				t.Fatal("the target did not change")
			}
			if st, _ := os.Stat(target); st.Mode().Perm() != 0o644 {
				t.Fatalf("the target's mode changed: %v", st.Mode().Perm())
			}
		})
	}
}

func TestAFleetWriteRefusesABrokenOrRepointedLink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "fleet.yaml")
	if err := os.Symlink(filepath.Join(dir, "gone.yaml"), link); err != nil {
		t.Fatal(err)
	}
	if err := SaveAI(link, AIConfig{Provider: "off"}); err == nil || !strings.Contains(err.Error(), "symlink whose target cannot be resolved") {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "gone.yaml")); !os.IsNotExist(err) {
		t.Fatal("a write through a broken link created its target")
	}

	a, b := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
	doc := []byte("clusters:\n  - name: a\n")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, doc, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	beforeReplace = func() { // the link is repointed while Klyx writes
		_ = os.Remove(link)
		_ = os.Symlink(b, link)
	}
	defer func() { beforeReplace = nil }()
	if err := SaveAI(link, AIConfig{Provider: "off"}); !errors.Is(err, ErrFleetChangedOnDisk) {
		t.Fatal(err)
	}
	for _, p := range []string{a, b} {
		if got, _ := os.ReadFile(p); string(got) != string(doc) {
			t.Fatalf("%s changed", p)
		}
	}
}

// The metrics-token move refuses a link repointed while it writes, too.
func TestTheTokenMoveRefusesARepointedLink(t *testing.T) {
	dir := t.TempDir()
	a, b, link := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml"), filepath.Join(dir, "fleet.yaml")
	doc := []byte("clusters:\n  - name: a\n    metrics:\n      endpoint: https://m.example\n      token: tok\n")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, doc, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	snap, err := ReadSnapshot(link)
	if err != nil {
		t.Fatal(err)
	}
	beforeReplace = func() {
		_ = os.Remove(link)
		_ = os.Symlink(b, link)
	}
	defer func() { beforeReplace = nil }()
	if _, err := MoveMetricsTokenToAuth(snap, "a"); !errors.Is(err, ErrFileChangedSinceSnapshot) {
		t.Fatal(err)
	}
	for _, p := range []string{a, b} {
		if got, _ := os.ReadFile(p); string(got) != string(doc) {
			t.Fatalf("%s changed", p)
		}
	}
}

// Codex's review of slice 0, finding 2: every use of an alias is checked
// against the schema it is used in; one read as a free map, or as another
// block, certifies nothing about a cluster.
func TestAnAliasIsCheckedAtEveryUse(t *testing.T) {
	for _, c := range []struct {
		name, doc, err string
	}{
		{"read as tags, merged into a cluster", "common: &common {protcted: true}\nclusters:\n  - name: a\n    tags: *common\n    <<: *common\n", `"protcted"`},
		{"read as the ai block, merged into a cluster", "ai: &common {provider: off}\nclusters:\n  - name: a\n    <<: *common\n", `"provider"`},
		{"a valid alias reused for two clusters", "base: &base {context: ctx}\nclusters:\n  - name: a\n    <<: *base\n  - name: b\n    <<: *base\n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fleet.yaml")
			if err := os.WriteFile(path, []byte(c.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if c.err == "" && err != nil || c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
				t.Fatalf("want %q, got %v", c.err, err)
			}
		})
	}
}

// Finding 3: a quoted '<<' is a string key, not a merge - the decoder
// ignores it - so it is an unknown field; a plain << merges.
func TestAQuotedMergeKeyIsNoMerge(t *testing.T) {
	dir := t.TempDir()
	quoted, plain := filepath.Join(dir, "q.yaml"), filepath.Join(dir, "p.yaml")
	_ = os.WriteFile(quoted, []byte("clusters:\n  - name: a\n    '<<': {protected: true}\n"), 0o600)
	_ = os.WriteFile(plain, []byte("clusters:\n  - name: a\n    <<: {protected: true}\n"), 0o600)
	if _, err := Load(quoted); err == nil || !strings.Contains(err.Error(), `"<<"`) {
		t.Fatalf("quoted: %v", err)
	}
	c, err := Load(plain)
	if err != nil || !c.Clusters[0].Protected {
		t.Fatalf("plain: %+v %v", c, err)
	}
}

// Finding 4: a permission change made while a writer prepares its file is
// refused like a content change; the file keeps its new mode and content.
func TestAWriterRefusesAPermissionChange(t *testing.T) {
	base := "clusters:\n  - name: a\n    context: a\n"
	writers := map[string]func(path string) error{
		"add clusters": func(p string) error { return AppendClusters(p, []string{"b"}) },
		"ai":           func(p string) error { return SaveAI(p, AIConfig{Provider: "off"}) },
		"git":          func(p string) error { return SaveGit(p, GitConfig{}) },
		"notifications": func(p string) error {
			return SaveNotifications(p, NotificationsConfig{Enabled: true})
		},
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fleet.yaml")
			if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
				t.Fatal(err)
			}
			beforeReplace = func() { _ = os.Chmod(path, 0o600) }
			defer func() { beforeReplace = nil }()
			if err := write(path); !errors.Is(err, ErrFleetChangedOnDisk) {
				t.Fatal(err)
			}
			st, _ := os.Stat(path)
			if b, _ := os.ReadFile(path); string(b) != base || st.Mode().Perm() != 0o600 {
				t.Fatalf("%v %q", st.Mode().Perm(), b)
			}
		})
	}
}

// Finding 5: a long echoed value is withheld spaces and all - "Bearer
// <key>" pasted into an enum field - and the error Load returns is already
// as shown, so no consumer gets the value.
func TestAPastedValueWithSpacesIsWithheld(t *testing.T) {
	key := "sk-synthetic-0123456789abcdefghijklmnop"
	for _, v := range []string{"Bearer " + key, "pasted " + key + " value"} {
		got := DisplayError(errors.New(`ai.provider must be off or anthropic (got "` + v + `"); line 3: cannot unmarshal !!str ` + "`" + v + "`" + ` into bool`))
		if strings.Contains(got, key) || !strings.Contains(got, "withheld") {
			t.Fatalf("%q: %s", v, got)
		}
	}
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte("clusters:\n  - name: a\nai:\n  provider: Bearer "+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "withheld") {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(path); err == nil || strings.Contains(err.Error(), key) {
		t.Fatal(err)
	}
	// a YAML type error echoes it in backticks, which yaml.v3 itself
	// shortens: it never reaches the message either way
	typed := filepath.Join(t.TempDir(), "fleet.yaml")
	_ = os.WriteFile(typed, []byte("clusters:\n  - name: a\n    protected: Bearer "+key+"\n"), 0o600)
	if _, err := Load(typed); err == nil || strings.Contains(err.Error(), key) {
		t.Fatal(err)
	}
	if got := DisplayError(errors.New(`clusters[0] (a): "` + key + `" at line 3`)); strings.Contains(got, "withheld") {
		t.Fatalf("a quoted key name is not an echoed value: %s", got)
	}
	long := filepath.Join(t.TempDir(), "fleet.yaml")
	_ = os.WriteFile(long, []byte("clusters:\n  - name: a\n    "+key+": true\n"), 0o600)
	if _, err := Load(long); err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "a key of 39 characters") {
		t.Fatal(err)
	}
}

// Finding 6: a fleet.yaml symlink whose target is gone is an error, not a
// first run; an absent path still is one.
func TestABrokenFleetLinkIsNotAFirstRun(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "fleet.yaml")
	if err := os.Symlink(filepath.Join(dir, "gone.yaml"), link); err != nil {
		t.Fatal(err)
	}
	_, err := Load(link)
	if err == nil || IsMissing(err) || !strings.Contains(err.Error(), "symlink whose target does not exist") {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(link); err == nil || IsMissing(err) {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(dir, "absent.yaml")); !IsMissing(err) {
		t.Fatal(err)
	}
}

// Codex's second re-review of slice 0: no YAML error carries the file's
// content, from the loader or any writer's round-trip check; parser
// messages and ordinary type errors still read as YAML gave them.
func TestNoYAMLErrorCarriesTheFilesContent(t *testing.T) {
	key := "sk-synthetic-0123456789abcdefghijklmnop"
	write := func(t *testing.T, doc string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "fleet.yaml")
		if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, c := range []struct{ name, doc, want string }{
		{"an unknown anchor", "clusters:\n  - name: a\n    protected: *" + key + "\n", "an alias names an anchor the file does not define"},
		{"a custom tag", "clusters:\n  - name: a\n    protected: !" + key + " true\n", "cannot unmarshal a value with a custom tag into bool"},
		{"a key given twice", "clusters:\n  - name: a\n    " + key + ": 1\n    " + key + ": 2\n", "a key of 39 characters already defined at line 3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := write(t, c.doc)
			for _, err := range []error{func() error { _, err := Load(path); return err }(), func() error { _, err := ReadSnapshot(path); return err }()} {
				if err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("%v", err)
				}
			}
		})
	}
	// a valid file whose ignored extension uses a list as a key: every
	// writer's round-trip check refuses, without the key, and the file stays
	extra := "clusters:\n  - name: a\n    context: a\n    metrics:\n      endpoint: https://m.example\n      token: tok\nextra:\n  ? [" + key + "]\n  : ignored\n"
	writers := map[string]func(p string) error{
		"add clusters": func(p string) error { return AppendClusters(p, []string{"b"}) },
		"ai":           func(p string) error { return SaveAI(p, AIConfig{Provider: "off"}) },
		"git":          func(p string) error { return SaveGit(p, GitConfig{}) },
		"notifications": func(p string) error {
			return SaveNotifications(p, NotificationsConfig{Enabled: true})
		},
		"metrics token": func(p string) error {
			snap, err := ReadSnapshot(p)
			if err != nil {
				return err
			}
			_, err = MoveMetricsTokenToAuth(snap, "a")
			return err
		},
	}
	for name, w := range writers {
		t.Run(name, func(t *testing.T) {
			path := write(t, extra)
			if _, err := Load(path); err != nil {
				t.Fatalf("the file loads: %v", err)
			}
			err := w(path)
			if err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "a key is a list or a map") {
				t.Fatalf("%v", err)
			}
			if b, _ := os.ReadFile(path); string(b) != extra {
				t.Fatal("the file changed")
			}
		})
	}
	// an anchor containing itself, which only an untyped decode - a writer's
	// round-trip - reaches
	var m map[string]any
	if err := unmarshalFleet([]byte("extra: &"+key+" {x: *"+key+"}\n"), &m); err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "an anchor's value contains itself") {
		t.Fatalf("self anchor: %v", err)
	}
	// an aliased top-level extension that adding a cluster would change
	aliased := write(t, "clusters: &members\n  - name: a\n"+key+": *members\n")
	if err := AppendClusters(aliased, []string{"b"}); err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "field a key of 39 characters changed") {
		t.Fatalf("%v", err)
	}
	// controls: a parser message and a standard type error read as YAML gave them
	if _, err := Load(write(t, "clusters:\n  - name: a\n  context: [\n")); err == nil || !strings.Contains(err.Error(), "line ") || strings.Contains(err.Error(), "cannot read") {
		t.Fatalf("parser: %v", err)
	}
	if _, err := Load(write(t, "clusters:\n  - name: a\n    protected: maybe\n")); err == nil || !strings.Contains(err.Error(), "cannot unmarshal !!str `maybe` into bool") {
		t.Fatalf("type: %v", err)
	}
}

// An issue in a form yaml.v3 v3.0.1 does not produce for this schema is
// passed on as its kind and line alone, whatever it holds.
func TestAnUnknownYAMLIssueIsNotPassedOn(t *testing.T) {
	for in, want := range map[string]string{
		"line 4: something new about sk-synthetic-0123456789abcdefghijklmnop": "line 4: a value Klyx cannot read",
		"no line at all, sk-synthetic-0123456789abcdefghijklmnop":             "a value Klyx cannot read",
		"line 2: cannot unmarshal !!int `12` into bool":                       "line 2: cannot unmarshal !!int `12` into bool",
	} {
		if got := readableIssue(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// Codex's third re-review of slice 0: a tagged scalar its tag cannot hold
// (yaml.v3's resolver quotes it in full) is withheld everywhere - the
// loader, with a backtick inside the value too, and all five writers.
func TestATaggedScalarIsWithheld(t *testing.T) {
	key := "sk-synthetic-0123456789abcdefghijklmnop"
	write := func(t *testing.T, doc string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "fleet.yaml")
		if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	ticked := "sk-synthetic-012345`6789abcdefghijklmnop"
	for _, v := range []string{key, ticked} {
		path := write(t, "clusters:\n  - name: a\n    protected: !!int '"+v+"'\n")
		for _, err := range []error{func() error { _, err := Load(path); return err }(), func() error { _, err := ReadSnapshot(path); return err }()} {
			if err == nil || strings.Contains(err.Error(), "6789abcdefghijklmnop") || !strings.Contains(err.Error(), "a value tagged !!int is not one (its value is withheld)") {
				t.Fatalf("%q: %v", v, err)
			}
		}
	}
	extra := "clusters:\n  - name: a\n    context: a\n    metrics:\n      endpoint: https://m.example\n      token: tok\nextra: !!int " + key + "\n"
	for name, w := range map[string]func(p string) error{
		"add clusters": func(p string) error { return AppendClusters(p, []string{"b"}) },
		"ai":           func(p string) error { return SaveAI(p, AIConfig{Provider: "off"}) },
		"git":          func(p string) error { return SaveGit(p, GitConfig{}) },
		"notifications": func(p string) error {
			return SaveNotifications(p, NotificationsConfig{Enabled: true})
		},
		"metrics token": func(p string) error {
			snap, err := ReadSnapshot(p)
			if err != nil {
				return err
			}
			_, err = MoveMetricsTokenToAuth(snap, "a")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := write(t, extra)
			if _, err := Load(path); err != nil {
				t.Fatalf("the file loads: %v", err)
			}
			if err := w(path); err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "its value is withheld") {
				t.Fatalf("%v", err)
			}
			if b, _ := os.ReadFile(path); string(b) != extra {
				t.Fatal("the file changed")
			}
		})
	}
}

// The boundary is an allowlist: yaml.v3's fixed problems and content-free
// decoder messages pass, a form not reviewed is withheld with its line,
// and an error that is not YAML's is withheld too.
func TestTheYAMLBoundaryIsAnAllowlist(t *testing.T) {
	for in, want := range map[string]string{
		"yaml: line 3: did not find expected key":                             "yaml: line 3: did not find expected key",
		"yaml: mapping values are not allowed in this context":                "yaml: mapping values are not allowed in this context",
		"yaml: line 9: exceeded max depth of 10000":                           "yaml: line 9: exceeded max depth of 10000",
		"yaml: document contains excessive aliasing":                          "yaml: document contains excessive aliasing",
		"yaml: line 4: something new about sk-synthetic-0123456789abcd":       "yaml: line 4: " + withheldYAML,
		"yaml: a new form quoting sk-synthetic-0123456789abcd":                "yaml: " + withheldYAML,
		"not a yaml error at all: sk-synthetic-0123456789abcd":                withheldYAML,
		"yaml: cannot decode !!str `sk-synthetic-0123456789abcd` as a !weird": "yaml: a tagged value its tag cannot hold (its value is withheld)",
	} {
		if got := readableYAML(errors.New(in)); got.Error() != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
