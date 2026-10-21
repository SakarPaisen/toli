package connect

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeRun records commands. claude succeeds unless noClaude; git commands
// run for real (in a temp repo) if git is installed.
type fakeRun struct {
	noClaude bool
	calls    []string
}

func (f *fakeRun) run(dir, name string, args ...string) error {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	switch name {
	case "claude", "codex", "openclaw":
		if f.noClaude {
			return &exec.Error{Name: name, Err: exec.ErrNotFound}
		}
		return nil
	default:
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		return cmd.Run()
	}
}

func readHooks(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestClaudeCodeProject(t *testing.T) {
	defer func(v bool) { onWindows = v }(onWindows)
	onWindows = false // the full-path form; TestWindowsHooks has Windows's
	dir := t.TempDir()
	settings := filepath.Join(dir, ".claude", "settings.local.json")
	os.MkdirAll(filepath.Dir(settings), 0o755)
	// Someone's existing settings: another setting, and another SessionStart hook.
	os.WriteFile(settings, []byte(`{"model": "opus", "hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "echo hi"}]}]}}`), 0o600)

	f := &fakeRun{}
	opts := Options{MCPURL: "http://127.0.0.1:7420/mcp/tok", Scope: Project, Dir: dir, Home: t.TempDir(), Toli: "/opt/my tools/toli", Run: f.run}
	for range 2 { // running it again replaces toli's entries
		if _, err := Connect("claude-code", opts); err != nil {
			t.Fatal(err)
		}
	}

	if !strings.Contains(strings.Join(f.calls, "\n"), "claude mcp add --transport http --scope local toli http://127.0.0.1:7420/mcp/tok") {
		t.Errorf("claude calls: %q", f.calls)
	}
	s := readHooks(t, settings)
	if s["model"] != "opus" {
		t.Errorf("other settings lost: %v", s)
	}
	if allow, _ := json.Marshal(s["permissions"]); string(allow) != `{"allow":["mcp__toli"]}` {
		t.Errorf("permissions = %s, want toli's tools allowed once", allow)
	}
	hooks := s["hooks"].(map[string]any)
	starts, _ := json.Marshal(hooks["SessionStart"])
	want := `'/opt/my tools/toli' hook claude-code session-start http://127.0.0.1:7420/mcp/tok`
	if strings.Count(string(starts), "session-start") != 1 || !strings.Contains(string(starts), want) || !strings.Contains(string(starts), "echo hi") {
		t.Errorf("SessionStart hooks = %s; want one toli hook (%s) and the existing one", starts, want)
	}
	if ends, _ := json.Marshal(hooks["SessionEnd"]); strings.Count(string(ends), "session-end") != 1 {
		t.Errorf("SessionEnd hooks = %s", ends)
	}
}

func TestClaudeCodeUserScopeWithoutClaude(t *testing.T) {
	home := t.TempDir()
	f := &fakeRun{noClaude: true}
	// Without claude, connect fails and writes nothing.
	_, err := Connect("claude-code", Options{MCPURL: "http://x/mcp/tok", Scope: User, Dir: t.TempDir(), Home: home, Toli: "/bin/toli", Run: f.run})
	if err == nil || !strings.Contains(err.Error(), "claude isn't on your PATH") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("settings written anyway: %v", err)
	}
}

func TestGitIgnore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	// Without the user's global git config (Claude Code adds
	// **/.claude/settings.local.json to the global ignore file).
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	f := &fakeRun{}
	opts := Options{MCPURL: "http://x/mcp/tok", Scope: Project, Dir: dir, Home: t.TempDir(), Toli: "/bin/toli", Run: f.run}
	if _, err := Connect("claude-code", opts); err != nil {
		t.Fatal(err)
	}
	if _, err := Connect("claude-code", opts); err != nil { // already ignored: not added twice
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if strings.Count(string(raw), ".claude/settings.local.json") != 1 {
		t.Errorf(".gitignore = %q", raw)
	}
}

func TestBadArgs(t *testing.T) {
	if _, err := Connect("nope", Options{Scope: Project}); err == nil {
		t.Error("unknown harness: nil error")
	}
	if _, err := Connect("claude-code", Options{Scope: "everywhere"}); err == nil {
		t.Error("unknown scope: nil error")
	}
}

func TestIsToliHook(t *testing.T) {
	for cmd, want := range map[string]bool{
		"/usr/local/bin/toli hook claude-code session-start http://x/mcp/t":      true,
		"'/opt/my tools/toli' hook claude-code session-end http://x/mcp/t":       true,
		"toli hook claude-code session-start http://x/mcp/t":                     true,
		"'C:/Program Files/toli/toli.exe' hook codex session-end http://x/mcp/t": true,  // Windows
		"~/src/toli/scripts/check.sh hook claude-code session-start":             false, // not a toli binary
		"/usr/local/bin/toli daemon":                                             false, // not a hook
		"echo hi":                                                                false,
		"/bin/notify hook claude-code session-start # from ~/src/toli/README":    false,
	} {
		if got := isToliHook(cmd); got != want {
			t.Errorf("isToliHook(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestCodexProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	exec.Command("git", "-C", dir, "init", "-q").Run()
	config := filepath.Join(dir, ".codex", "config.toml")
	os.MkdirAll(filepath.Dir(config), 0o755)
	// Someone's existing config, with an old toli entry in the middle.
	os.WriteFile(config, []byte("model = \"o4\"\n\n[mcp_servers.toli]\nurl = \"http://old/mcp/x\"\n\n[mcp_servers.other]\nurl = \"http://other\"\n"), 0o600)

	f := &fakeRun{}
	opts := Options{MCPURL: "http://127.0.0.1:7420/mcp/tok", Scope: Project, Dir: dir, Home: t.TempDir(), Toli: "/bin/toli", Run: f.run}
	for range 2 {
		if _, err := Connect("codex", opts); err != nil {
			t.Fatal(err)
		}
	}

	raw, _ := os.ReadFile(config)
	got := string(raw)
	if strings.Count(got, "[mcp_servers.toli]") != 1 || !strings.Contains(got, `url = "http://127.0.0.1:7420/mcp/tok"`) ||
		strings.Contains(got, "http://old") || !strings.Contains(got, `model = "o4"`) || !strings.Contains(got, "[mcp_servers.other]") ||
		!strings.Contains(got, `default_tools_approval_mode = "approve"`) {
		t.Errorf("config.toml =\n%s", got)
	}
	hooks, _ := os.ReadFile(filepath.Join(dir, ".codex", "hooks.json"))
	if strings.Count(string(hooks), "hook codex session-start http://127.0.0.1:7420/mcp/tok") != 1 {
		t.Errorf("hooks.json = %s", hooks)
	}
	ignore, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(ignore), ".codex/config.toml") || !strings.Contains(string(ignore), ".codex/hooks.json") {
		t.Errorf(".gitignore = %q", ignore)
	}

	// A config file the repo tracks: refused, so the token isn't committed.
	tracked := t.TempDir()
	exec.Command("git", "-C", tracked, "init", "-q").Run()
	os.MkdirAll(filepath.Join(tracked, ".codex"), 0o755)
	os.WriteFile(filepath.Join(tracked, ".codex", "config.toml"), []byte("model = \"o4\"\n"), 0o644)
	exec.Command("git", "-C", tracked, "add", ".codex/config.toml").Run()
	opts.Dir = tracked
	if _, err := Connect("codex", opts); err == nil || !strings.Contains(err.Error(), "--scope user") {
		t.Fatalf("tracked config: %v, want a refusal suggesting --scope user", err)
	}
}

func TestCodexUser(t *testing.T) {
	home := t.TempDir()
	f := &fakeRun{}
	if _, err := Connect("codex", Options{MCPURL: "http://x/mcp/tok", Scope: User, Dir: t.TempDir(), Home: home, Toli: "/bin/toli", Run: f.run}); err != nil {
		t.Fatal(err)
	}
	config, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if !strings.Contains(string(config), `url = "http://x/mcp/tok"`) || !strings.Contains(string(config), `default_tools_approval_mode = "approve"`) {
		t.Errorf("user config.toml = %s", config)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); err != nil {
		t.Errorf("user hooks not written: %v", err)
	}
}

func TestOpencode(t *testing.T) {
	dir := t.TempDir()
	// Someone's existing config.
	os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(`{"model": "anthropic/claude", "permission": {"bash": "ask"}}`), 0o600)
	f := &fakeRun{}
	opts := Options{MCPURL: "http://127.0.0.1:7420/mcp/tok", Scope: Project, Dir: dir, Home: t.TempDir(), Toli: "/bin/toli", Run: f.run}
	if _, err := Connect("opencode", opts); err != nil {
		t.Fatal(err)
	}

	plugin, err := os.ReadFile(filepath.Join(dir, ".opencode", "plugins", "toli.js"))
	if err != nil || !strings.Contains(string(plugin), `const TOLI = "http://127.0.0.1:7420/hook/tok"`) {
		t.Fatalf("plugin: %v\n%.300s", err, plugin)
	}
	var cfg map[string]any
	raw, _ := os.ReadFile(filepath.Join(dir, "opencode.json"))
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	mcp, _ := json.Marshal(cfg["mcp"])
	perm, _ := json.Marshal(cfg["permission"])
	if cfg["model"] != "anthropic/claude" || string(mcp) != `{"toli":{"enabled":true,"type":"remote","url":"http://127.0.0.1:7420/mcp/tok"}}` ||
		string(perm) != `{"bash":"ask","toli_*":"allow"}` {
		t.Errorf("opencode.json = %s", raw)
	}

	// An opencode.jsonc: left alone, with a clear error.
	jsonc := t.TempDir()
	os.WriteFile(filepath.Join(jsonc, "opencode.jsonc"), []byte("{}"), 0o600)
	opts.Dir = jsonc
	if _, err := Connect("opencode", opts); err == nil || !strings.Contains(err.Error(), "opencode.jsonc") {
		t.Errorf("with opencode.jsonc: %v", err)
	}
}

// TestDisconnect: disconnect takes out what connect wrote, and only that.
func TestDisconnect(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
	os.WriteFile(filepath.Join(dir, ".claude", "settings.local.json"), []byte(`{"model": "opus", "permissions": {"allow": ["Bash(ls)"]}, "hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "echo hi"}]}]}}`), 0o600)
	os.MkdirAll(filepath.Join(dir, ".codex"), 0o755)
	os.WriteFile(filepath.Join(dir, ".codex", "config.toml"), []byte("model = \"o4\"\n\n[mcp_servers.other]\nurl = \"http://other\"\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(`{"model": "anthropic/claude", "permission": {"bash": "ask"}}`), 0o600)

	var unregistered []string
	f := &fakeRun{}
	opts := Options{MCPURL: "http://127.0.0.1:7420/mcp/tok", Scope: Project, Dir: dir, Home: home, Toli: "/bin/toli", Run: f.run,
		Register:   func(string, string) error { return nil },
		Unregister: func(s string) error { unregistered = append(unregistered, s); return nil },
	}
	for _, h := range []string{"claude-code", "codex", "opencode", "openclaw", "pi"} {
		if _, err := Connect(h, opts); err != nil {
			t.Fatalf("connect %s: %v", h, err)
		}
		if _, err := Disconnect(h, opts); err != nil {
			t.Fatalf("disconnect %s: %v", h, err)
		}
	}

	settings, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.local.json"))
	if s := string(settings); strings.Contains(s, "toli") || !strings.Contains(s, "echo hi") || !strings.Contains(s, "Bash(ls)") || !strings.Contains(s, `"opus"`) {
		t.Errorf("claude settings = %s", s)
	}
	config, _ := os.ReadFile(filepath.Join(dir, ".codex", "config.toml"))
	if s := string(config); strings.Contains(s, "toli") || !strings.Contains(s, `model = "o4"`) || !strings.Contains(s, "[mcp_servers.other]") {
		t.Errorf("config.toml = %s", s)
	}
	hooks, _ := os.ReadFile(filepath.Join(dir, ".codex", "hooks.json"))
	if strings.Contains(string(hooks), "toli") {
		t.Errorf("hooks.json = %s", hooks)
	}
	if _, err := os.Stat(filepath.Join(dir, ".opencode", "plugins", "toli.js")); !os.IsNotExist(err) {
		t.Errorf("plugin still there: %v", err)
	}
	oc, _ := os.ReadFile(filepath.Join(dir, "opencode.json"))
	if s := string(oc); strings.Contains(s, "toli") || !strings.Contains(s, `"bash": "ask"`) || !strings.Contains(s, "anthropic/claude") {
		t.Errorf("opencode.json = %s", s)
	}
	if len(unregistered) != 1 || unregistered[0] != openclawMain {
		t.Errorf("unregistered %v, want [%s]", unregistered, openclawMain)
	}
	want := []string{"claude mcp remove --scope local toli", "openclaw mcp unset toli"}
	for _, w := range want {
		if !slices.Contains(f.calls, w) {
			t.Errorf("no %q in %v", w, f.calls)
		}
	}

	// Nothing connected: disconnect writes no files.
	empty := t.TempDir()
	opts.Dir = empty
	for _, h := range []string{"claude-code", "codex", "opencode", "pi"} {
		if _, err := Disconnect(h, opts); err != nil {
			t.Errorf("disconnect %s with nothing there: %v", h, err)
		}
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Errorf("disconnect wrote files: %v", entries)
	}
}

func TestPi(t *testing.T) {
	dir := t.TempDir()
	f := &fakeRun{}
	opts := Options{MCPURL: "http://127.0.0.1:7420/mcp/tok", Scope: Project, Dir: dir, Home: t.TempDir(), Toli: "/bin/toli", Run: f.run}
	if _, err := Connect("pi", opts); err != nil {
		t.Fatal(err)
	}
	ext, err := os.ReadFile(filepath.Join(dir, ".pi", "extensions", "toli.js"))
	if err != nil || !strings.Contains(string(ext), `const MCP = "http://127.0.0.1:7420/mcp/tok"`) ||
		!strings.Contains(string(ext), `const TOLI = "http://127.0.0.1:7420/hook/tok"`) {
		t.Fatalf("extension: %v\n%.400s", err, ext)
	}
	if _, err := Disconnect("pi", opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".pi", "extensions", "toli.js")); !os.IsNotExist(err) {
		t.Errorf("extension still there: %v", err)
	}
}

func TestClaudeCodeMCPDirs(t *testing.T) {
	defer func(v bool) { onWindows = v }(onWindows)
	onWindows = true
	for dir, want := range map[string][]string{
		`C:\Users\z\proj`: {`C:\Users\z\proj`, `c:\Users\z\proj`},
		`c:\Users\z\proj`: {`c:\Users\z\proj`, `C:\Users\z\proj`},
		`\\server\share`:  {`\\server\share`},
	} {
		if got := (claudeCode{}).mcpDirs(Options{Scope: Project, Dir: dir}); !slices.Equal(got, want) {
			t.Errorf("%s: %q, want %q", dir, got, want)
		}
	}
	if got := (claudeCode{}).mcpDirs(Options{Scope: User, Dir: `C:\x`}); len(got) != 1 {
		t.Errorf("user scope: %q, want one", got)
	}
}

func TestWindowsHooks(t *testing.T) {
	defer func(v bool, lp func(string) (string, error)) { onWindows, lookPath = v, lp }(onWindows, lookPath)
	onWindows = true
	o := Options{MCPURL: "http://127.0.0.1:7420/mcp/tok", Toli: `C:\Users\z\go\bin\toli.exe`}
	if got := hookCommand(o, "codex", "session-start"); got != "toli hook codex session-start http://127.0.0.1:7420/mcp/tok" {
		t.Errorf("hookCommand = %q", got)
	}
	if !isToliHook(hookCommand(o, "codex", "session-start")) {
		t.Error("disconnect wouldn't recognize the hook")
	}

	lookPath = func(string) (string, error) { return `c:\users\z\go\bin\toli.exe`, nil }
	if d := toliOnPath(o); d != nil {
		t.Errorf("toli on PATH is this one: %+v", d)
	}
	lookPath = func(string) (string, error) { return `C:\other\toli.exe`, nil }
	if d := toliOnPath(o); len(d) != 1 || !strings.Contains(d[0].What, `C:\Users\z\go\bin`) {
		t.Errorf("another toli first on PATH: %+v", d)
	}
}
