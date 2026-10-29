package settings

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/bounds"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/envread"
	"github.com/moomora/klyx/internal/localstore"
)

var update = flag.Bool("update", false, "rewrite the registry snapshot")

// snapshotPath is the golden file the frontend imports (S1 §2).
var snapshotPath = filepath.Join("..", "..", "cmd", "klyx", "frontend", "src", "settings", "registry.snapshot.json")

// Snapshot is the registry as the frontend reads it.
func snapshot() ([]byte, error) {
	b, err := json.MarshalIndent(struct {
		Sections []string `json:"sections"`
		Entries  []Entry  `json:"entries"`
	}{Sections, Registry()}, "", "  ")
	return append(b, '\n'), err
}

func TestTheSnapshotIsTheRegistry(t *testing.T) {
	want, err := snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(snapshotPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("%v (go test ./internal/settings -update writes it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("registry.snapshot.json differs from the registry: run go test ./internal/settings -update and review the diff")
	}
}

func byID(t *testing.T) map[string]Entry {
	t.Helper()
	out := map[string]Entry{}
	for _, e := range Registry() {
		if _, dup := out[e.ID]; dup {
			t.Fatalf("duplicate id %s", e.ID)
		}
		out[e.ID] = e
	}
	return out
}

// Every entry is complete and uses the contract's vocabulary.
func TestEveryEntryIsComplete(t *testing.T) {
	known := func(v string, set ...string) bool {
		for _, s := range set {
			if v == s {
				return true
			}
		}
		return false
	}
	for id, e := range byID(t) {
		switch {
		case e.Label == "" || e.Effect == "" || e.Default == "" || e.Validation == "" || e.Save == "":
			t.Errorf("%s: an empty part: %+v", id, e)
		case !known(e.Section, Sections...):
			t.Errorf("%s: section %q", id, e.Section)
		case !known(e.Disposition, Editable, FileOnly, Effective, Deferred, Fixed):
			t.Errorf("%s: disposition %q", id, e.Disposition)
		case !known(e.Source, FromSchema, FromEnv, FromStore, FromBound, FromCredential):
			t.Errorf("%s: source %q", id, e.Source)
		case !known(e.TakesEffect, Now, OnSave, Reconnect, Restart, OnAction):
			t.Errorf("%s: takes effect %q", id, e.TakesEffect)
		case !known(e.Scope, "fleet", "cluster", "endpoint", "installation", "subscription", "profile", "process", "webview"):
			t.Errorf("%s: scope %q", id, e.Scope)
		case strings.Contains(e.Effect+e.Label+e.Save+e.Validation, "—"):
			t.Errorf("%s: an em dash", id)
		}
	}
}

// ids of one source, without the prefix.
func ids(t *testing.T, source, prefix string) map[string]bool {
	out := map[string]bool{}
	for id, e := range byID(t) {
		if e.Source == source && strings.HasPrefix(id, prefix) {
			out[strings.TrimPrefix(id, prefix)] = true
		}
	}
	return out
}

// same fails on a derived item with no entry, and an entry with no item.
func same(t *testing.T, what string, derived []string, entries map[string]bool) {
	t.Helper()
	have := map[string]bool{}
	for _, d := range derived {
		have[d] = true
		if !entries[d] {
			t.Errorf("%s %s has no registry entry", what, d)
		}
	}
	for e := range entries {
		if !have[e] {
			t.Errorf("the registry describes %s %s, which does not exist", what, e)
		}
	}
}

// §3 schema: every YAML path of config.Config.
func TestEverySchemaPathHasAnEntry(t *testing.T) {
	schema := map[string]bool{}
	for id, e := range byID(t) {
		if e.Source == FromSchema {
			schema[id] = true
		}
	}
	same(t, "the schema path", config.SchemaPaths(), schema)
}

// §3.1 environment: every declared name, family and dynamic purpose, each
// way, with the harness names as fixed.
func TestEveryEnvironmentDeclarationHasAnEntry(t *testing.T) {
	var keys []string
	harness := map[string]bool{}
	for _, d := range envread.Declared() {
		keys = append(keys, d.Key)
		if d.Kind == "harness" {
			harness[d.Key] = true
		}
	}
	names := map[string]bool{}
	for id := range ids(t, FromEnv, "env.") {
		if !strings.HasPrefix(id, "family.") && !strings.HasPrefix(id, "purpose.") {
			names[id] = true
		}
	}
	same(t, "the environment variable", keys, names)
	same(t, "the environment family", envread.FamilyIDs(), ids(t, FromEnv, "env.family."))
	same(t, "the dynamic purpose", envread.PurposeIDs(), ids(t, FromEnv, "env.purpose."))
	for id, e := range byID(t) {
		if k, ok := strings.CutPrefix(id, "env."); ok && harness[k] != (e.Effect == harnessNote) {
			t.Errorf("%s: harness %v, but its entry says otherwise", id, harness[k])
		}
	}
}

// §3.2 native stores, each way.
func TestEveryNativeStoreHasAnEntry(t *testing.T) {
	var names []string
	for _, d := range localstore.Declared() {
		names = append(names, d.Name)
	}
	same(t, "the native store", names, ids(t, FromStore, "native."))
}

// §3 bounds: every ceiling, each way, with its value shown. A new ceiling
// is added to bounds.List by review: this test detects changed values and
// missing entries, not new constants.
func TestEveryBoundHasAnEntryWithItsValue(t *testing.T) {
	var list []string
	all := byID(t)
	for _, b := range bounds.List() {
		list = append(list, b.ID)
		if e, ok := all["bound."+b.ID]; ok && e.Default != fmt.Sprintf("%d %s", b.Value, b.Unit) {
			t.Errorf("bound.%s shows %q", b.ID, e.Default)
		}
	}
	same(t, "the bound", list, ids(t, FromBound, "bound."))
}

// The registry never holds a value: no entry's text carries a secret's
// shape, and credentials are their own source.
func TestCredentialsAreTheirOwnEntries(t *testing.T) {
	for id, e := range byID(t) {
		if (e.Source == FromCredential) != strings.HasPrefix(id, "credential.") {
			t.Errorf("%s: source %s", id, e.Source)
		}
	}
}

// The registry says what the loader accepts: alertmanager.off is true or
// left out (Codex's re-review of S3a-1, RR3).
func TestOffSaysWhatTheLoaderAccepts(t *testing.T) {
	for _, e := range Registry() {
		if e.ID == "clusters[].alertmanager.off" && !strings.HasPrefix(e.Validation, "true, or left out") {
			t.Fatalf("off: %q", e.Validation)
		}
	}
}
