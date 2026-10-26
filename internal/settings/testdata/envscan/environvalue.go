package fixture

import "os"

var walk = os.Environ

func all() []string { return walk() }
