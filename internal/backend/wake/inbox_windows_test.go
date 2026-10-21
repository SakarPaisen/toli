package wake

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"testing"

	"github.com/Microsoft/go-winio"
)

// listenInbox listens on a named pipe, as Claude Code's inbox is on Windows.
func listenInbox(t *testing.T) (string, net.Listener) {
	t.Helper()
	b := make([]byte, 8)
	rand.Read(b)
	path := `\\.\pipe\LOCAL\toli-test-` + hex.EncodeToString(b)
	ln, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return path, ln
}
