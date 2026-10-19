package main

import "syscall"

// detachConsole closes the console window Windows opens for the daemon when
// Task Scheduler starts it; it logs to a file instead.
func detachConsole() {
	syscall.NewLazyDLL("kernel32.dll").NewProc("FreeConsole").Call()
}
