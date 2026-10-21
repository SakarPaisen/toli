//go:build !windows

package wake

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// listenInbox listens where Claude Code's inbox is on macOS and Linux: a Unix
// socket.
func listenInbox(t *testing.T) (string, net.Listener) {
	t.Helper()
	dir, err := os.MkdirTemp("", "toli") // short: Unix socket paths are limited to ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "inbox.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	return path, ln
}
