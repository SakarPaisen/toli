//go:build !windows

package main

// detachConsole is only needed on Windows.
func detachConsole() {}
