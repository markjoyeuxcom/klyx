package fixture

import "path/filepath"

func configPath() string { return "/x/fleet.yaml" }

func store() string { return filepath.Join(filepath.Dir(configPath()), "window.json") }
