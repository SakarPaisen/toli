package service

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// config returns a Config whose commands are recorded, not run.
func config(t *testing.T) (Config, *[]string) {
	var calls []string
	home := t.TempDir()
	return Config{
		Exe:     "/opt/my tools/toli",
		Args:    []string{"daemon", "--public-url", "http://100.64.0.2:7422/a&b"},
		Env:     map[string]string{"PATH": "/usr/bin:/opt/bin", "TOLI_HOME": "/data/50%"},
		Home:    home,
		LogDir:  filepath.Join(home, "logs"),
		UID:     501,
		User:    `PC\alice`,
		TempDir: t.TempDir(),
		Run: func(name string, args ...string) (string, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return "", nil
		},
	}, &calls
}

func TestLaunchd(t *testing.T) {
	c, calls := config(t)
	l := launchd{c}
	if err := l.Install(); err != nil {
		t.Fatal(err)
	}
	plist, err := os.ReadFile(l.plistPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<string>/opt/my tools/toli</string>", "<string>http://100.64.0.2:7422/a&amp;b</string>",
		"<key>PATH</key>", "<key>KeepAlive</key>", "<string>" + l.Logs() + "</string>",
	} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("plist lacks %q:\n%s", want, plist)
		}
	}
	if _, err := exec.LookPath("plutil"); err == nil {
		if out, err := exec.Command("plutil", "-lint", l.plistPath()).CombinedOutput(); err != nil {
			t.Errorf("plutil: %s", out)
		}
	}
	want := []string{
		"launchctl bootout gui/501/" + launchdLabel,
		"launchctl enable gui/501/" + launchdLabel,
		"launchctl bootstrap gui/501 " + l.plistPath(),
	}
	if !slices.Equal(*calls, want) {
		t.Errorf("calls = %q", *calls)
	}
	if ok, _ := l.Installed(); !ok {
		t.Error("not installed after Install")
	}
	if err := l.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := l.Installed(); ok {
		t.Error("installed after Uninstall")
	}
}

func TestSystemd(t *testing.T) {
	c, calls := config(t)
	s := systemd{c}
	if err := s.Install(); err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(s.unitPath())
	for _, want := range []string{
		`ExecStart="/opt/my tools/toli" "daemon" "--public-url" "http://100.64.0.2:7422/a&b"`,
		`Environment="TOLI_HOME=/data/50%%"`, "Restart=on-failure", "WantedBy=default.target",
	} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable toli", "systemctl --user restart toli"}
	if !slices.Equal(*calls, want) {
		t.Errorf("calls = %q", *calls)
	}
}

func TestSchtasks(t *testing.T) {
	c, calls := config(t)
	c.Env = map[string]string{"PATH": `C:\Program Files\nodejs;C:\Users\alice\AppData\Roaming\npm`, "TOLI_HOME": "x"}
	s := schtasks{c}
	task := s.task()
	var parsed struct {
		Exec struct {
			Command   string `xml:"Command"`
			Arguments string `xml:"Arguments"`
		} `xml:"Actions>Exec"`
		User string `xml:"Principals>Principal>UserId"`
	}
	// encoding/xml won't read a document that declares UTF-16; the file is
	// UTF-16, this string isn't.
	if err := xml.Unmarshal([]byte(strings.Replace(task, `encoding="UTF-16"`, "", 1)), &parsed); err != nil {
		t.Fatalf("task XML: %v\n%s", err, task)
	}
	wantArgs := `daemon --public-url http://100.64.0.2:7422/a&b --log-file ` + windowsQuote(s.Logs()) +
		` --path "C:\Program Files\nodejs;C:\Users\alice\AppData\Roaming\npm"`
	if parsed.Exec.Command != "/opt/my tools/toli" || parsed.Exec.Arguments != wantArgs || parsed.User != `PC\alice` {
		t.Errorf("task = %+v", parsed)
	}
	if b := utf16File("a"); !slices.Equal(b, []byte{0xFF, 0xFE, 'a', 0}) {
		t.Errorf("utf16File = %v", b)
	}
	if err := s.Install(); err != nil {
		t.Fatal(err)
	}
	want := []string{"schtasks /End /TN toli", "schtasks /Create /TN toli /XML " + filepath.Join(c.TempDir, "toli-task.xml") + " /F", "schtasks /Run /TN toli"}
	if !slices.Equal(*calls, want) {
		t.Errorf("calls = %q", *calls)
	}
}

func TestFor(t *testing.T) {
	c, _ := config(t)
	for _, goos := range []string{"darwin", "linux", "windows"} {
		if _, err := For(goos, c); err != nil {
			t.Errorf("%s: %v", goos, err)
		}
	}
	if _, err := For("plan9", c); err == nil {
		t.Error("plan9: no error")
	}
}
