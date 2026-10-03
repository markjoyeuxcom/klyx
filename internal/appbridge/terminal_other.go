//go:build !windows

package appbridge

import "syscall"

// newConsoleAttr is Windows-only; elsewhere terminals are separate apps.
func newConsoleAttr() *syscall.SysProcAttr { return nil }
