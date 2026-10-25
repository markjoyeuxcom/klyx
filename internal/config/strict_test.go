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
			return SaveNotifications(p, NotificationsConfig{Enabled: true}, NotificationsConfig{})
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
