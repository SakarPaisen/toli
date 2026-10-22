package service

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// schtasks is a Task Scheduler task that runs the daemon when the user logs
// on and restarts it if it fails: the per-user way (a Windows service needs
// admin rights). Task Scheduler doesn't capture output, so the daemon writes
// its log itself (--log-file).
type schtasks struct{ c Config }

func (s schtasks) Logs() string { return filepath.Join(s.c.LogDir, "daemon.log") }

func (s schtasks) Installed() (bool, string) {
	_, err := s.c.Run("schtasks", "/Query", "/TN", Name)
	return err == nil, `Task Scheduler \` + Name
}

// task is the task's definition. No time limit (the default stops a task
// after 3 days), runs on battery, and restarts every minute if it fails.
func (s schtasks) task() string {
	esc := func(v string) string {
		var b bytes.Buffer
		xml.EscapeText(&b, []byte(v))
		return b.String()
	}
	// A task can't set environment variables, so the PATH goes as a flag.
	extra := []string{"--log-file", s.Logs()}
	if p := s.c.Env["PATH"]; p != "" {
		extra = append(extra, "--path", p)
	}
	var args []string
	for _, a := range append(s.c.Args, extra...) {
		args = append(args, windowsQuote(a))
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>toli daemon</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + esc(s.c.User) + `</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + esc(s.c.User) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <StartWhenAvailable>true</StartWhenAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
    </IdleSettings>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + esc(s.c.Exe) + `</Command>
      <Arguments>` + esc(strings.Join(args, " ")) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

// windowsQuote quotes an argument for a Windows command line, if needed.
func windowsQuote(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\"") {
		return a
	}
	return `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
}

// utf16File is s as UTF-16 with a byte order mark, which schtasks /XML
// expects.
func utf16File(s string) []byte {
	b := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

func (s schtasks) Install() error {
	if err := os.MkdirAll(s.c.LogDir, 0o755); err != nil {
		return err
	}
	def := filepath.Join(s.c.TempDir, "toli-task.xml")
	if err := os.WriteFile(def, utf16File(s.task()), 0o644); err != nil {
		return err
	}
	defer os.Remove(def)
	s.c.Run("schtasks", "/End", "/TN", Name) // stop an earlier version; fails if there's none
	if _, err := s.c.Run("schtasks", "/Create", "/TN", Name, "/XML", def, "/F"); err != nil {
		return err
	}
	return s.Start()
}

func (s schtasks) Uninstall() error {
	s.c.Run("schtasks", "/End", "/TN", Name) // fails if it isn't running
	_, err := s.c.Run("schtasks", "/Delete", "/TN", Name, "/F")
	return err
}

func (s schtasks) Start() error {
	_, err := s.c.Run("schtasks", "/Run", "/TN", Name)
	return err
}

func (s schtasks) Stop() error {
	_, err := s.c.Run("schtasks", "/End", "/TN", Name)
	return err
}

func (s schtasks) Restart() error {
	s.Stop() // fails if it isn't running
	return s.Start()
}
