//go:build linux

package helper

import (
	"syscall"
	"unsafe"
)

const (
	renameExchange = 0x2
	atFDCWD        = ^uintptr(99)
)

func exchangeDirs(left, right string) error {
	leftPtr, err := syscall.BytePtrFromString(left)
	if err != nil {
		return err
	}
	rightPtr, err := syscall.BytePtrFromString(right)
	if err != nil {
		return err
	}
	_, _, errno := syscall.RawSyscall6(
		renameat2Syscall,
		atFDCWD,
		uintptr(unsafe.Pointer(leftPtr)),
		atFDCWD,
		uintptr(unsafe.Pointer(rightPtr)),
		renameExchange,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}
