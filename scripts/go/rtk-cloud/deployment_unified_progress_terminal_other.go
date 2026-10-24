//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package main

func environmentTerminalColumns(uintptr) int { return 0 }
