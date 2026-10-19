package main

import (
	"fmt"
	"net"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/arjunvlama/toli/internal/api"
	"github.com/arjunvlama/toli/internal/tui"
)

// runTUI opens the terminal UI against the running daemon's /v1/ API.
func runTUI() error {
	c, err := adminClient()
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(tui.New(c, mcpAddr())).Run()
	return err
}

// adminClient is a client of the running daemon's /v1/ API, with the admin
// token from $TOLI_ADMIN_TOKEN or the toli home.
func adminClient() (*api.Client, error) {
	conn, err := net.DialTimeout("tcp", adminAddr(), time.Second)
	if err != nil {
		return nil, fmt.Errorf("the daemon isn't running on %s: start it with `toli daemon install` (always on) or `toli daemon`", adminAddr())
	}
	conn.Close()

	admin := os.Getenv("TOLI_ADMIN_TOKEN")
	if admin == "" {
		home, err := homeDir()
		if err != nil {
			return nil, err
		}
		admin, err = readAdminToken(home)
		if err != nil {
			return nil, err
		}
	}
	return api.NewClient("http://"+adminAddr(), admin), nil
}
