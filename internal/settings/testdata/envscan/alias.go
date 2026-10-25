package fixture

import o "os"

func read() string { return o.Getenv("X") }
