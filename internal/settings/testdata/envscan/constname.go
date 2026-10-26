package fixture

import "os"

const name = "KLYX_SECRET"

func read() string { return os.Getenv(name) }
