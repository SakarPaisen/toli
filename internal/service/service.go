// Package service runs the toli daemon as an always-on service for the
// current user, the way other local daemons do (e.g. OpenClaw's Gateway): a
// launchd agent on macOS, a systemd user unit on Linux, a Task Scheduler task
// at logon on Windows. Each starts the daemon at login and restarts it if it
// exits. No admin rights needed.
package service

import (
	"fmt"
	"os/exec"
	"strings"
)

// Name is the service's name: the systemd unit (toli.service) and the
// Windows task.
const Name = "toli"

// Config is what to run, and where things go.
type Config struct {
	Exe     string            // absolute path to the toli binary
	Args    []string          // its arguments: "daemon" and daemon flags
	Env     map[string]string // environment for the daemon (macOS, Linux)
	Home    string            // the user's home directory
	LogDir  string            // where the daemon's log goes (macOS, Windows)
	UID     int               // the user's id (macOS)
	User    string            // the user's name, DOMAIN\user (Windows)
	TempDir string            // for the task definition (Windows)
	// Run runs a command and returns its combined output.
	Run func(name string, args ...string) (string, error)
}

// Manager installs and controls the service on one OS.
type Manager interface {
	// Install writes the service definition and starts the daemon; run it
	// again to update it (e.g. after toli moved).
	Install() error
	// Uninstall stops the daemon and removes the definition.
	Uninstall() error
	Start() error
	Stop() error
	Restart() error
	// Installed reports whether the service is installed, and where its
	// definition is.
	Installed() (bool, string)
	// Logs says where the daemon's log is.
	Logs() string
}

// For returns the Manager for an OS (runtime.GOOS).
func For(goos string, c Config) (Manager, error) {
	switch goos {
	case "darwin":
		return launchd{c}, nil
	case "linux":
		return systemd{c}, nil
	case "windows":
		return schtasks{c}, nil
	default:
		return nil, fmt.Errorf("no service support on %s; run `toli daemon` under your own supervisor", goos)
	}
}

// Run runs a command and returns its combined output; errors include it.
func Run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
