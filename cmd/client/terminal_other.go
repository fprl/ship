//go:build !darwin && !linux

package client

func isTerminalFD(fd uintptr) bool {
	return false
}
