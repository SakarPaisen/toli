//go:build !windows

package wake

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
)

// dialInbox connects to a Claude Code session's inbox: a Unix socket.
func dialInbox(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return nil, fmt.Errorf("%w: %w", errGone, err)
	}
	return conn, err
}
