//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package paths

import (
	"io/fs"
	"syscall"
)

// changeStamp is the inode and status-change time, which, as in Git's index,
// move on any rewrite even when size and modification time are put back.
func changeStamp(info fs.FileInfo) (uint64, int64) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Ino), stat.Ctimespec.Nano()
	}
	return 0, 0
}
