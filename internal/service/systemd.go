package service

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// systemd is a user unit: ~/.config/systemd/user/toli.service, logging to
// the journal.
type systemd struct{ c Config }

func (s systemd) unitPath() string {
	return filepath.Join(s.c.Home, ".config", "systemd", "user", Name+".service")
}

func (s systemd) Logs() string { return "journalctl --user -u " + Name }

func (s systemd) Installed() (bool, string) {
	_, err := os.Stat(s.unitPath())
	return err == nil, s.unitPath()
}

// unit is the service's definition: start at login, restart if it fails.
func (s systemd) unit() []byte {
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=toli daemon\n\n[Service]\n")
	b.WriteString("ExecStart=" + systemdQuote(s.c.Exe))
	for _, a := range s.c.Args {
		b.WriteString(" " + systemdQuote(a))
	}
	b.WriteString("\n")
	keys := make([]string, 0, len(s.c.Env))
	for k := range s.c.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		b.WriteString("Environment=" + systemdQuote(k+"="+s.c.Env[k]) + "\n")
	}
	b.WriteString("Restart=on-failure\nRestartSec=10\n\n[Install]\nWantedBy=default.target\n")
	return []byte(b.String())
}

// systemdQuote quotes a word for a unit file: double quotes with C-style
// escapes, and % and $ doubled so systemd doesn't expand them.
func systemdQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
	return `"` + r.Replace(s) + `"`
}

func (s systemd) Install() error {
	if err := os.MkdirAll(filepath.Dir(s.unitPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.unitPath(), s.unit(), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", Name}, {"restart", Name}} {
		if _, err := s.c.Run("systemctl", append([]string{"--user"}, args...)...); err != nil {
			return err
		}
	}
	return nil
}

func (s systemd) Uninstall() error {
	s.c.Run("systemctl", "--user", "disable", "--now", Name) // fails if it isn't installed
	if err := os.Remove(s.unitPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err := s.c.Run("systemctl", "--user", "daemon-reload")
	return err
}

func (s systemd) Start() error   { return s.systemctl("start") }
func (s systemd) Stop() error    { return s.systemctl("stop") }
func (s systemd) Restart() error { return s.systemctl("restart") }

func (s systemd) systemctl(verb string) error {
	_, err := s.c.Run("systemctl", "--user", verb, Name)
	return err
}
