package clock

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	qpc      = kernel32.NewProc("QueryPerformanceCounter")
	freq     int64
)

func init() {
	if r, _, _ := kernel32.NewProc("QueryPerformanceFrequency").Call(uintptr(unsafe.Pointer(&freq))); r == 0 {
		freq = 0
	}
}

func now() int64 {
	var c int64
	if freq == 0 {
		return portable()
	}
	if r, _, _ := qpc.Call(uintptr(unsafe.Pointer(&c))); r == 0 {
		return portable()
	}
	// Split, so that c*1e9 cannot overflow.
	return c/freq*1e9 + c%freq*1e9/freq
}
