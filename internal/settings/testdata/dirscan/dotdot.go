package fixture

import "path/filepath"

// a parent reached by a ".." segment instead of Dir
func beside(file string) string { return filepath.Join(file, "..", "new-preference.json") }
