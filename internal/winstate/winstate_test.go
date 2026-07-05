package winstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "window.json")
	want := Bounds{X: 120, Y: 80, Width: 1440, Height: 900}
	if err := Save(p, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := Load(p)
	if !ok || got != want {
		t.Fatalf("load = %+v ok=%v, want %+v", got, ok, want)
	}
}

func TestLoadMissingOrCorrupt(t *testing.T) {
	if _, ok := Load(filepath.Join(t.TempDir(), "absent.json")); ok {
		t.Fatal("missing file must not load")
	}
	p := filepath.Join(t.TempDir(), "window.json")
	if err := os.WriteFile(p, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load(p); ok {
		t.Fatal("corrupt file must not load")
	}
}

func TestInsaneBoundsRejected(t *testing.T) {
	cases := []Bounds{
		{Width: 10, Height: 800},             // degenerate width
		{Width: 1200, Height: 100},           // degenerate height
		{X: -99999, Width: 800, Height: 600}, // stranded off-desktop
		{Width: 50000, Height: 600},          // implausible size
	}
	for _, b := range cases {
		if b.Sane() {
			t.Fatalf("bounds %+v must be insane", b)
		}
	}
	p := filepath.Join(t.TempDir(), "window.json")
	if err := Save(p, Bounds{Width: 10, Height: 10}); err != nil {
		t.Fatalf("insane save must be a silent no-op, got %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("insane bounds must not be persisted")
	}
}
