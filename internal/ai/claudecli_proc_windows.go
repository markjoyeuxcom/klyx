//go:build windows

package ai

import (
	"os"
	"os/exec"
)

// Windows containment (a Job Object with kill-on-close) waits for a reviewed
// Windows contract; until then the adapter refuses on Windows before it
// starts anything (runContained), so these only keep the package building.

func setProcGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func fileInode(os.FileInfo) uint64 { return 0 }
