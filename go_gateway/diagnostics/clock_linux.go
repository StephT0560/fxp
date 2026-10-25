//go:build linux

// SPDX-License-Identifier: MIT
//
package diagnostics

import (
	"syscall"
	"unsafe"
)

func monotonicNS() int64 {
	var ts syscall.Timespec
	_, _, err := syscall.RawSyscall(syscall.SYS_CLOCK_GETTIME, 1, uintptr(unsafe.Pointer(&ts)), 0)
	if err != 0 {
		return 0
	}
	return ts.Nano()
}
