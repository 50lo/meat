//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly)

package main

// isatty is conservatively false on platforms without a known termios ioctl
// (e.g. windows, plan9, solaris, illumos, aix). Output is treated as
// non-interactive, so it stays plain.
func isatty(fd uintptr) bool { return false }
