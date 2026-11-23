package gateway

import "golang.org/x/sys/unix"

func systemResources(directory string) (memory, diskFree, diskTotal uint64, err error) {
	memory, _ = unix.SysctlUint64("hw.memsize")
	var disk unix.Statfs_t
	if err = unix.Statfs(directory, &disk); err != nil {
		return memory, 0, 0, err
	}
	return memory, disk.Bavail * uint64(disk.Bsize), disk.Blocks * uint64(disk.Bsize), nil
}
