//go:build windows

package appbridge

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// newConsoleStartupInfo is the STARTUPINFO for a terminal launch: no
// STARTF_USESTDHANDLES, so the child takes the standard handles of the NEW
// console Windows creates for it.
func newConsoleStartupInfo() *windows.StartupInfo {
	return &windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
}

// startInNewConsole starts argv (argv[0] an absolute path) in a new console
// window wired to that console's own input and output.
//
// os/exec cannot do this (batch-3 review R4): Go's StartProcess always sets
// STARTF_USESTDHANDLES, passing NUL for a nil Stdin/Stdout/Stderr, and
// Windows honours those handles even under CREATE_NEW_CONSOLE - PowerShell
// would read EOF from NUL and exit, and kubectl -it would find no terminal.
// CreateProcess without STARTF_USESTDHANDLES and without handle inheritance
// gives the child the new console's handles. The command line follows the
// MSVC argv rules (ComposeCommandLine), which PowerShell parses as intended
// for arguments without embedded double quotes (psQuote refuses those).
func startInNewConsole(argv []string) error {
	app, err := windows.UTF16PtrFromString(argv[0])
	if err != nil {
		return err
	}
	cmdline, err := windows.UTF16FromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return err
	}
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(app, &cmdline[0], nil, nil, false, windows.CREATE_NEW_CONSOLE, nil, nil, newConsoleStartupInfo(), &pi); err != nil {
		return err
	}
	_ = windows.CloseHandle(pi.Thread)
	_ = windows.CloseHandle(pi.Process)
	return nil
}
