package fixture

import "path/filepath"

// a clean file: a path built under a directory it was given
func under(dir string) string { return filepath.Join(dir, "x.yaml") }
