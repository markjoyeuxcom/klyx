package fixture

import "syscall"

func read() string { v, _ := syscall.Getenv("X"); return v }
