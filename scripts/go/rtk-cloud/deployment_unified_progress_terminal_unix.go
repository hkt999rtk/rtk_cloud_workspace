//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package main

import "golang.org/x/sys/unix"

func environmentTerminalColumns(fd uintptr) int {
	if size, err := unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ); err == nil {
		return int(size.Col)
	}
	return 0
}
