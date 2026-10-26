package fixture

import . "os"

func read() string { return Getenv("X") }
