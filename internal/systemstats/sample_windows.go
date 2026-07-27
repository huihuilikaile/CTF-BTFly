//go:build windows

package systemstats

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

type memoryStatusEx struct {
	length            uint32
	memoryLoad        uint32
	totalPhysical     uint64
	availablePhysical uint64
	totalPageFile     uint64
	availablePageFile uint64
	totalVirtual      uint64
	availableVirtual  uint64
	availableExtended uint64
}

func readHostSample() (hostSample, error) {
	var idle, kernel, user windows.Filetime
	ok, _, callErr := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if ok == 0 {
		return hostSample{}, fmt.Errorf("GetSystemTimes: %w", callErr)
	}
	memory := memoryStatusEx{length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	ok, _, callErr = procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&memory)))
	if ok == 0 {
		return hostSample{}, fmt.Errorf("GlobalMemoryStatusEx: %w", callErr)
	}
	totalPhysical := memory.totalPhysical
	usedPhysical := uint64(0)
	if memory.availablePhysical <= totalPhysical {
		usedPhysical = totalPhysical - memory.availablePhysical
	}
	return hostSample{
		idle:        filetimeValue(idle),
		total:       filetimeValue(kernel) + filetimeValue(user),
		memoryUsed:  usedPhysical,
		memoryTotal: totalPhysical,
	}, nil
}

func filetimeValue(value windows.Filetime) uint64 {
	return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
}
