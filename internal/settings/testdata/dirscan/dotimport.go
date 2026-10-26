package fixture

import (
	"os"
	. "path/filepath"
)

// Codex's re-review, RR1: a dot-imported Dir is unqualified
func save(p string) error {
	return os.WriteFile(Join(Dir(p), "new-preference.json"), []byte("{}"), 0o600)
}
