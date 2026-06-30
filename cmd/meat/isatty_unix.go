//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package main

import (
	"syscall"
	"unsafe"
)

// isatty reports whether fd refers to a terminal, via the TCGETS ioctl (the
// same mechanism isatty(3) uses). A successful termios fetch means fd is a tty.
func isatty(fd uintptr) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlReadTermios, uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}

// ioctlReadTermios is the "get terminal attributes" ioctl number, which differs
// across Unix variants (TCGETS on Linux, TIOCGETA on the BSDs/macOS).
