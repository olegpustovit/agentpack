package network

import "golang.org/x/sys/unix"

func systemResources(directory string) (memory, diskFree, diskTotal uint64, err error) {
	var system unix.Sysinfo_t
	if unix.Sysinfo(&system) == nil {
		memory = uint64(system.Totalram) * uint64(system.Unit)
	}
	var disk unix.Statfs_t
	if err = unix.Statfs(directory, &disk); err != nil {
		return memory, 0, 0, err
	}
	return memory, disk.Bavail * uint64(disk.Bsize), disk.Blocks * uint64(disk.Bsize), nil
}
