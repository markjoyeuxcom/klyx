//go:build !windows

package appbridge

import "errors"

// startInNewConsole is Windows-only: terminalLaunchArgv never asks for a new
// console elsewhere, where terminals are separate apps.
func startInNewConsole([]string) error {
	return errors.New("new-console launch is Windows-only")
}
