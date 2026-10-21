package wake

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/Microsoft/go-winio"
)

// dialInbox connects to a Claude Code session's inbox: on Windows, a named
// pipe.
func dialInbox(ctx context.Context, path string) (net.Conn, error) {
	if strings.HasPrefix(path, `\\.\pipe\`) {
		conn, err := winio.DialPipeContext(ctx, path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %w", errGone, err)
		}
		return conn, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	// Windows refuses a dead socket with WSAECONNREFUSED (10061), not ECONNREFUSED.
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.Errno(10061)) {
		return nil, fmt.Errorf("%w: %w", errGone, err)
	}
	return conn, err
}
