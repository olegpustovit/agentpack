//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package paths

import "io/fs"

// changeStamp has no portable source here; size and modification time remain.
func changeStamp(fs.FileInfo) (uint64, int64) { return 0, 0 }
