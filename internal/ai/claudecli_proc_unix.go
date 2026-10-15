//go:build !windows

package ai

import (
	"os"
	"syscall"
)

// fileInode is part of the executable hash cache's key.
func fileInode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
