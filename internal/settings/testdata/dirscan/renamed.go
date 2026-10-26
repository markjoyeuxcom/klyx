package fixture

import (
	"os"
	"path/filepath"

	"github.com/moomora/klyx/internal/envread"
)

// Codex's S1 review, R4: a renamed fleet path joined with a new store name
func write() error {
	p := envread.Get(envread.KlyxConfig)
	return os.WriteFile(filepath.Join(filepath.Dir(p), "new-preference.json"), []byte("{}"), 0o600)
}
