package config

import (
	"errors"
	"os"
	"testing"
)

// Settings slice F §5: a write from a plan carries the file version the
// plan was made from; any other version refuses it and leaves the file as
// it is, and the written file's version is returned.
func TestAPlansWriteRefusesAnotherFileVersion(t *testing.T) {
	path := writeFleet(t, "clusters:\n  - name: a\n")
	v0, err := FileVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	// a hand edit after the plan
	if err := os.WriteFile(path, []byte("clusters:\n  - name: a\n# edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	edited := readFleet(t, path)
	if _, err := SaveAIAt(path, v0, AIConfig{Provider: "anthropic", KeySource: "keychain"}); !errors.Is(err, ErrFileChanged) {
		t.Fatalf("a write under an old version: %v", err)
	}
	if readFleet(t, path) != edited {
		t.Fatal("the refused write changed the file")
	}
	v1, _ := FileVersion(path)
	written, err := SaveAIAt(path, v1, AIConfig{Provider: "anthropic", KeySource: "keychain"})
	if err != nil {
		t.Fatal(err)
	}
	if now, _ := FileVersion(path); now != written || written == v1 {
		t.Fatalf("the written version %q, the file's %q, before %q", written, now, v1)
	}
	// "" checks no version
	if _, err := SaveAIAt(path, "", AIConfig{Provider: "openai", KeySource: "keychain"}); err != nil {
		t.Fatal(err)
	}
}
