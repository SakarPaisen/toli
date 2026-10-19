package hook_test

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/a2atest"
	"github.com/arjunvlama/toli/internal/backend"
	"github.com/arjunvlama/toli/internal/hook"
	"github.com/arjunvlama/toli/internal/store"
)

func TestHook(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "toli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b, err := backend.New(ctx, st, backend.Options{CheckEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := b.AddAgent(ctx, "backend", "Owns the API", nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(hook.Handler(b))
	t.Cleanup(srv.Close)
	mcpURL := srv.URL + "/mcp/" + tok

	env := map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/tmp/cc.sock", "CLAUDE_CODE_MESSAGING_TOKEN": "cc-tok"}
	run := func(harness, event, input string) (string, error) {
		var out bytes.Buffer
		err := hook.Run(ctx, hook.Call{Harness: harness, Event: event, MCPURL: mcpURL,
			Stdin: strings.NewReader(input), Stdout: &out, Getenv: func(k string) string { return env[k] }})
		return out.String(), err
	}

	// Claude Code: registers with its socket, and is told it gets notified.
	out, err := run("claude-code", hook.EventSessionStart, `{"session_id": "s1", "hook_event_name": "SessionStart", "source": "startup"}`)
	if err != nil || !strings.Contains(out, "this session is notified") {
		t.Fatalf("session-start: %q, %v", out, err)
	}
	hs, err := b.HarnessSession(ctx, "backend")
	if err != nil || hs.Harness != "claude-code" || hs.SessionID != "s1" ||
		hs.Address["socket"] != "/tmp/cc.sock" || hs.Address["socket_token"] != "cc-tok" {
		t.Fatalf("registered = %+v, %v", hs, err)
	}

	// With a task waiting, the agent gets it as context.
	bob := a2atest.NewFakePeer(t)
	peer, _, err := b.AddPeer(ctx, "backend", "bob", bob.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	req := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Add a /users endpoint"))
	asBob := b.IncomingTasks(func(context.Context) (store.Caller, error) {
		return store.Caller{Agent: "backend", PeerID: peer.ID}, nil
	})
	if _, err := asBob.Create(ctx, a2a.NewSubmittedTask(req, req)); err != nil {
		t.Fatal(err)
	}
	out, err = run("codex", hook.EventSessionStart, `{"session_id": "thread-1"}`)
	if err != nil || !strings.Contains(out, `"hookEventName":"SessionStart"`) || !strings.Contains(out, "1 new task(s)") {
		t.Fatalf("session-start with work waiting: %q, %v", out, err)
	}
	if hs, _ := b.HarnessSession(ctx, "backend"); hs.Harness != "codex" || hs.SessionID != "thread-1" || len(hs.Address) != 0 {
		t.Fatalf("after codex registered: %+v", hs)
	}

	if _, err := run("codex", hook.EventSessionEnd, `{"session_id": "thread-1"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.HarnessSession(ctx, "backend"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after session-end: %v, want ErrNotFound", err)
	}

	// Errors: unknown harness or event, a bad URL, a bad token.
	if _, err := run("nope", hook.EventSessionStart, `{}`); err == nil {
		t.Error("unknown harness: nil error")
	}
	if _, err := run("codex", "lunch", `{}`); err == nil {
		t.Error("unknown event: nil error")
	}
	mcpURL = srv.URL + "/v1/whatever"
	if _, err := run("codex", hook.EventSessionStart, `{}`); err == nil {
		t.Error("bad MCP URL: nil error")
	}
	mcpURL = srv.URL + "/mcp/wrong-token"
	if _, err := run("codex", hook.EventSessionStart, `{"session_id": "x"}`); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong token: %v, want a 401", err)
	}
}
