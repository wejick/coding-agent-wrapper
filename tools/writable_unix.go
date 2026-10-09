//go:build unix

package tools

import "syscall"

// writable reports whether the current user may create files in dir.
func writable(dir string) error {
	const wOK = 0x2
	return syscall.Access(dir, wOK)
}
