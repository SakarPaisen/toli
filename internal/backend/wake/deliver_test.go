package wake

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/store"
)

// fakeSession is a Claude Code inbox socket: it records each line written.
func fakeSession(t *testing.T) (path string, lines <-chan map[string]any) {
	t.Helper()
	dir, err := os.MkdirTemp("", "toli") // short: Unix socket paths are limited to ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path = filepath.Join(dir, "inbox.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan map[string]any, 16)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			sc := bufio.NewScanner(conn)
			for sc.Scan() {
				var line map[string]any
				json.Unmarshal(sc.Bytes(), &line)
				ch <- line
			}
			conn.Close()
		}
	}()
	return path, ch
}

func TestNotify(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "toli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.AddAgent(ctx, store.Agent{Name: "backend", Card: a2a.AgentCard{Name: "backend"}}, "hash"); err != nil {
		t.Fatal(err)
	}
	s := New(st)
	retryAfter = 10 * time.Millisecond

	// No session: nothing to do.
	if err := s.Notify(ctx, "backend", "hi"); err != nil {
		t.Fatalf("no session: %v", err)
	}

	// A Claude Code session gets the auth line, then the message.
	path, lines := fakeSession(t)
	if err := s.Register(ctx, "backend", "claude-code", "s1", map[string]string{"socket": path, "socket_token": "tok"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Notify(ctx, "backend", "toli: new task from bob"); err != nil {
		t.Fatal(err)
	}
	auth, msg := <-lines, <-lines
	if auth["type"] != "auth" || auth["token"] != "tok" {
		t.Fatalf("first line = %v, want the auth line", auth)
	}
	m, _ := msg["message"].(map[string]any)
	if msg["type"] != "user" || m["role"] != "user" || m["content"] != "toli: new task from bob" {
		t.Fatalf("second line = %v, want the message", msg)
	}

	// The session is gone: its registration is dropped.
	if err := s.Register(ctx, "backend", "claude-code", "s2", map[string]string{"socket": path + ".gone"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Notify(ctx, "backend", "hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, "backend"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after delivering to a gone session: %v, want it unregistered", err)
	}

	// Any other harness: the note waits to be collected (GET /hook/notifications).
	if err := s.Register(ctx, "backend", "my-agent", "s3", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Notify(ctx, "backend", "hi"); err != nil {
		t.Fatal(err)
	}
	if notes, err := s.TakeNotes(ctx, "backend", "s3", time.Second); err != nil || len(notes) != 1 || notes[0] != "hi" {
		t.Fatalf("my-agent's notes: %q, %v", notes, err)
	}
}

func TestNotifyCodex(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "toli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.AddAgent(ctx, store.Agent{Name: "backend", Card: a2a.AgentCard{Name: "backend"}}, "hash"); err != nil {
		t.Fatal(err)
	}
	s := New(st)

	if runtime.GOOS == "windows" {
		t.Skip("the fake codex is a shell script")
	}
	// A fake codex that records its arguments.
	bin, got := t.TempDir(), filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + got + "\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := s.Register(ctx, "backend", "codex", "thread-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Notify(ctx, "backend", "toli: new task from bob"); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(got)
	if string(args) != "queue\n--thread\nthread-1\n--message\ntoli: new task from bob\n" {
		t.Fatalf("codex called with %q", args)
	}
}

// A thread codex doesn't have: the registration is dropped.
func TestNotifyCodexGone(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "toli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.AddAgent(ctx, store.Agent{Name: "backend", Card: a2a.AgentCard{Name: "backend"}}, "hash"); err != nil {
		t.Fatal(err)
	}
	s := New(st)
	if runtime.GOOS == "windows" {
		t.Skip("the fake codex is a shell script")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\necho 'Error: failed to queue session message: thread/queue/add failed: failed to read thread: no rollout found for thread id x' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := s.Register(ctx, "backend", "codex", "thread-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Notify(ctx, "backend", "hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, "backend"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after a gone thread: %v, want it unregistered", err)
	}
}
