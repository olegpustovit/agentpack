package network

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// x/sys/windows has no wrapper for GlobalMemoryStatusEx.
var globalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

type memoryStatusEx struct {
	Length, MemoryLoad                                 uint32
	TotalPhys, AvailPhys, TotalPageFile, AvailPageFile uint64
	TotalVirtual, AvailVirtual, AvailExtendedVirtual   uint64
}

func systemResources(directory string) (memory, diskFree, diskTotal uint64, err error) {
	status := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if ok, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status))); ok != 0 {
		memory = status.TotalPhys
	}
	path, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return memory, 0, 0, err
	}
	err = windows.GetDiskFreeSpaceEx(path, &diskFree, &diskTotal, nil)
	return memory, diskFree, diskTotal, err
}
