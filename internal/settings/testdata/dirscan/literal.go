package fixture

import "path/filepath"

func cache(dir string) string { return filepath.Join(dir, "git-cache") }
