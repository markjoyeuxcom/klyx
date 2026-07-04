// Package winstate persists the main window's bounds across launches. A
// daily-driver that forgets its size and position every morning reads as a
// prototype; this is the ~40 lines that fix it. Best-effort by design: a
// missing or corrupt state file means default geometry, never a failed boot.
package winstate

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Bounds struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Sane rejects bounds that would boot the window degenerate or far off any
// plausible display - a monitor that was unplugged since the last run must
// not strand the window outside the desktop.
func (b Bounds) Sane() bool {
	if b.Width < 400 || b.Height < 300 || b.Width > 20000 || b.Height > 20000 {
		return false
	}
	if b.X < -20000 || b.X > 20000 || b.Y < -20000 || b.Y > 20000 {
		return false
	}
	return true
}

// Load reads persisted bounds. ok is false when the file is missing, corrupt,
// or the bounds fail the sanity check.
func Load(path string) (Bounds, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Bounds{}, false
	}
	var b Bounds
	if err := json.Unmarshal(raw, &b); err != nil {
		return Bounds{}, false
	}
	if !b.Sane() {
		return Bounds{}, false
	}
	return b, true
}

// Save writes bounds, creating the parent directory if needed. Insane bounds
// are dropped silently - persisting them would poison the next boot.
func Save(path string, b Bounds) error {
	if !b.Sane() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}
