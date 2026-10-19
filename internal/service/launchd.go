package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// launchdLabel names the launchd agent.
const launchdLabel = "io.github.arjunvlama.toli"

// launchd is a LaunchAgent: ~/Library/LaunchAgents/<label>.plist, loaded
// into the user's GUI domain, with its output in ~/Library/Logs/toli/.
type launchd struct{ c Config }

func (l launchd) plistPath() string {
	return filepath.Join(l.c.Home, "Library", "LaunchAgents", launchdLabel+".plist")
}

func (l launchd) target() string { return fmt.Sprintf("gui/%d/%s", l.c.UID, launchdLabel) }

func (l launchd) Logs() string { return filepath.Join(l.c.LogDir, "daemon.log") }

func (l launchd) Installed() (bool, string) {
	_, err := os.Stat(l.plistPath())
	return err == nil, l.plistPath()
}

// plist is the agent's definition: start at login, restart if it exits (at
// most every 10s).
func (l launchd) plist() []byte {
	esc := func(s string) string {
		var b bytes.Buffer
		xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range append([]string{l.c.Exe}, l.c.Args...) {
		b.WriteString("\t\t<string>" + esc(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	if len(l.c.Env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		keys := make([]string, 0, len(l.c.Env))
		for k := range l.c.Env {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			b.WriteString("\t\t<key>" + esc(k) + "</key>\n\t\t<string>" + esc(l.c.Env[k]) + "</string>\n")
		}
		b.WriteString("\t</dict>\n")
	}
	b.WriteString(`	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>StandardOutPath</key>
	<string>` + esc(l.Logs()) + `</string>
	<key>StandardErrorPath</key>
	<string>` + esc(l.Logs()) + `</string>
</dict>
</plist>
`)
	return []byte(b.String())
}

func (l launchd) Install() error {
	if err := os.MkdirAll(l.c.LogDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.plistPath()), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(l.plistPath(), l.plist(), 0o644); err != nil {
		return err
	}
	l.c.Run("launchctl", "bootout", l.target()) // unload an earlier version; fails if there's none
	l.c.Run("launchctl", "enable", l.target())  // in case it was disabled
	_, err := l.c.Run("launchctl", "bootstrap", fmt.Sprintf("gui/%d", l.c.UID), l.plistPath())
	return err
}

func (l launchd) Uninstall() error {
	l.c.Run("launchctl", "bootout", l.target()) // fails if it isn't loaded
	if err := os.Remove(l.plistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Start loads the agent (which starts the daemon), or starts it if it's
// loaded but not running.
func (l launchd) Start() error {
	if _, err := l.c.Run("launchctl", "print", l.target()); err == nil {
		_, err := l.c.Run("launchctl", "kickstart", l.target())
		return err
	}
	_, err := l.c.Run("launchctl", "bootstrap", fmt.Sprintf("gui/%d", l.c.UID), l.plistPath())
	return err
}

// Stop unloads the agent, so KeepAlive doesn't restart the daemon; it loads
// again at the next login, or with Start.
func (l launchd) Stop() error {
	_, err := l.c.Run("launchctl", "bootout", l.target())
	return err
}

func (l launchd) Restart() error {
	_, err := l.c.Run("launchctl", "kickstart", "-k", l.target())
	return err
}
