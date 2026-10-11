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

func TestSettleUndoesTheToolkitShrink(t *testing.T) {
	req := Bounds{X: 10, Y: 20, Width: 1523, Height: 1122}
	// Untouched window: created 1px smaller - persist the requested size.
	if got := Settle(req, Bounds{X: 10, Y: 20, Width: 1522, Height: 1121}, true); got.Width != 1523 || got.Height != 1122 {
		t.Fatalf("untouched window drifted: %+v", got)
	}
	// Moved but not resized: position follows the window, size stays.
	if got := Settle(req, Bounds{X: 300, Y: 40, Width: 1522, Height: 1121}, true); got != (Bounds{X: 300, Y: 40, Width: 1523, Height: 1122}) {
		t.Fatalf("move lost: %+v", got)
	}
	// A resize to any other size is persisted as observed.
	if got := Settle(req, Bounds{Width: 1280, Height: 900}, true); got.Width != 1280 || got.Height != 900 {
		t.Fatalf("resize lost: %+v", got)
	}
	// A toolkit that no longer shrinks: observed == requested, unchanged.
	if got := Settle(req, req, true); got != req {
		t.Fatalf("no-shrink toolkit changed: %+v", got)
	}
	// No requested size (first launch on defaults): observed as is.
	if got := Settle(Bounds{}, Bounds{Width: 1023, Height: 767}, true); got.Width != 1023 {
		t.Fatalf("first launch altered: %+v", got)
	}
}

func TestSettleOnlyCompensatesWhereTheToolkitShrinks(t *testing.T) {
	req := Bounds{Width: 1523, Height: 1122}
	observed := Bounds{Width: 1522, Height: 1121}
	// Linux/Windows: no startup shrink, so requested-1 is a real size.
	if got := Settle(req, observed, false); got != observed {
		t.Fatalf("non-darwin bounds altered: %+v", got)
	}
}
