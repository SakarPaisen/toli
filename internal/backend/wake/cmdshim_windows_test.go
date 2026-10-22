package wake

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/arjunvlama/toli/internal/store"
)

// npmShim is npm's .cmd shim for a global command, running argv.js.
const npmShim = "@ECHO off\r\nGOTO start\r\n:find_dp0\r\nSET dp0=%~dp0\r\nEXIT /b\r\n:start\r\nSETLOCAL\r\nCALL :find_dp0\r\n\r\n" +
	"IF EXIST \"%dp0%\\node.exe\" (\r\n  SET \"_prog=%dp0%\\node.exe\"\r\n) ELSE (\r\n  SET \"_prog=node\"\r\n  SET PATHEXT=%PATHEXT:;.JS;=;%\r\n)\r\n\r\n" +
	"endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & \"%_prog%\"  \"%dp0%\\argv.js\" %*\r\n"

// TestCodexThroughCmd: a peer's attack on cmd.exe doesn't run.
func TestCodexThroughCmd(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("needs node, as npm's shim does")
	}
	dir := t.TempDir()
	argv := `require("fs").writeFileSync(require("path").join(__dirname, "args.json"), JSON.stringify(process.argv.slice(2)))`
	if err := os.WriteFile(filepath.Join(dir, "argv.js"), []byte(argv), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "codex.cmd"), []byte(npmShim), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	marker := filepath.Join(dir, "injected.txt")
	text := fmt.Sprintf("toli: new task from alice: %q. Call list_available_tasks.", `x" & echo INJECTED> `+marker+` & "`)
	sess := store.HarnessSession{Agent: "backend", Harness: "codex", SessionID: "019a2b3c-4d5e-7f00-8a9b-0c1d2e3f4a5b"}
	if err := (&Service{}).deliver(context.Background(), sess, text); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the peer's text ran as a command")
	}
	b, err := os.ReadFile(filepath.Join(dir, "args.json"))
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	json.Unmarshal(b, &args)
	want := []string{"queue", "--thread", sess.SessionID, "--message", cmdNote}
	if fmt.Sprint(args) != fmt.Sprint(want) {
		t.Errorf("codex got %q, want %q", args, want)
	}
}
