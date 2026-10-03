//go:build windows

package appbridge

import "syscall"

// createNewConsole is CREATE_NEW_CONSOLE: the child gets its own console window
// instead of inheriting (or, from a GUI parent, silently lacking) one.
const createNewConsole = 0x00000010

func newConsoleAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewConsole}
}
