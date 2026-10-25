package fixture

import "path/filepath"

// a clean file: filepath.Dir of something that is not the fleet path
func parent(gitDir string) string { return filepath.Dir(gitDir) }
