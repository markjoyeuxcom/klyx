package fixture

import "os"

func read() bool { _, ok := os.LookupEnv("X"); return ok }
