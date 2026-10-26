package localstore

import (
	"path/filepath"
	"testing"
)

func TestPathIsBesideTheFleetFileLexically(t *testing.T) {
	r := Beside("/home/o/.config/klyx/fleet.yaml")
	for s, want := range map[Store]string{
		Window:       "/home/o/.config/klyx/window.json",
		ClaudeBuilds: "/home/o/.config/klyx/claude-builds.json",
		GitCache:     "/home/o/.config/klyx/git-cache",
	} {
		if got, err := r.Path(s); err != nil || got != filepath.FromSlash(want) {
			t.Errorf("%s: %q %v", s.Name(), got, err)
		}
	}
	// a relocated fleet file (KLYX_CONFIG) relocates its stores
	if got, _ := Beside("/lab/fleet.yaml").Path(Window); got != filepath.FromSlash("/lab/window.json") {
		t.Fatal(got)
	}
}

// Settings S1 §3.2: a zero or undeclared handle is refused before a path is
// formed, as is a root made from no fleet path.
func TestUndeclaredStoresAndEmptyRootsAreRefused(t *testing.T) {
	r := Beside("/x/fleet.yaml")
	if p, err := r.Path(Store{}); p != "" || err != ErrUndeclared {
		t.Fatal("a zero store")
	}
	if p, err := r.Path(Store{&store{name: "../escape", kind: "file"}}); p != "" || err != ErrUndeclared {
		t.Fatal("a non-member store")
	}
	if p, err := Beside("  ").Path(Window); p != "" || err != ErrNoRoot {
		t.Fatal("an empty root")
	}
	if p, err := (Root{}).Path(Window); p != "" || err != ErrNoRoot {
		t.Fatal("a zero root")
	}
	if (Store{}).Name() != "" {
		t.Fatal("a zero store's name")
	}
}

func TestDeclaredListsEveryStore(t *testing.T) {
	var names []string
	for _, d := range Declared() {
		names = append(names, d.Name+":"+d.Kind)
	}
	want := []string{"window.json:file", "claude-builds.json:file", "git-cache:dir"}
	if len(names) != len(want) {
		t.Fatal(names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatal(names)
		}
	}
}
