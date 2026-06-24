package main

import (
	"io"
	"os"
)

// stdinIsPiped reports whether stdin has data piped/redirected into it (as
// opposed to a terminal). When false, meat falls back to summarizing HEAD.
func stdinIsPiped() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	// A char device is an interactive terminal; anything else (pipe, regular
	// file, socket) means data was redirected in.
	return fi.Mode()&os.ModeCharDevice == 0
}

func readAllStdin() ([]byte, error) {
	return io.ReadAll(os.Stdin)
}
