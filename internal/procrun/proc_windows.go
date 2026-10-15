//go:build windows

package procrun

import "os/exec"

// Windows containment (a Job Object with kill-on-close) waits for a reviewed
// Windows contract; Run refuses before it starts anything (Supported), so
// these only keep the package building.

func setProcGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
