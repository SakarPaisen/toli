package hook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

func TestSessionHooks(t *testing.T) {
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

	post := func(tok, event string, body any) (*http.Response, map[string]string) {
		t.Helper()
		raw, _ := json.Marshal(body)
		resp, err := http.Post(srv.URL+"/hook/"+tok+"/"+event, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]string
		json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}
	start := hook.SessionStart{Harness: "claude-code", SessionID: "s1", Address: map[string]string{"socket": "/tmp/s1.sock"}}

	// Registering: the session is recorded; it's told it gets notified.
	resp, out := post(tok, "session-start", start)
	if resp.StatusCode != http.StatusOK || !strings.Contains(out["context"], "this session is notified") {
		t.Fatalf("session-start: %d %v", resp.StatusCode, out)
	}
	as, err := b.HarnessSession(ctx, "backend")
	if err != nil || as.Harness != "claude-code" || as.SessionID != "s1" || as.Address["socket"] != "/tmp/s1.sock" {
		t.Fatalf("registered session = %+v, %v", as, err)
	}

	// A session ID with characters a shell would act on is refused.
	bad := start
	bad.SessionID = `x" & calc & "`
	if resp, _ := post(tok, "session-start", bad); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unsafe session_id: %d, want 400", resp.StatusCode)
	}

	// With a task from a peer waiting, the context says so.
	bob := a2atest.NewFakePeer(t)
	peer, _, err := b.AddPeer(ctx, "backend", "bob", bob.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	asBob := b.IncomingTasks(func(context.Context) (store.Caller, error) {
		return store.Caller{Agent: "backend", PeerID: peer.ID}, nil
	})
	req := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Add a /users endpoint"))
	if _, err := asBob.Create(ctx, a2a.NewSubmittedTask(req, req)); err != nil {
		t.Fatal(err)
	}
	if _, out := post(tok, "session-start", start); !strings.Contains(out["context"], "1 new task(s)") {
		t.Fatalf("context with a task waiting: %q", out["context"])
	}

	// Ending a replaced session leaves the newer one; ending the current one removes it.
	post(tok, "session-start", hook.SessionStart{Harness: "claude-code", SessionID: "s2"})
	if resp, _ := post(tok, "session-end", hook.SessionEnd{SessionID: "s1"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("session-end of a replaced session: %d", resp.StatusCode)
	}
	if as, _ := b.HarnessSession(ctx, "backend"); as.SessionID != "s2" {
		t.Fatalf("after the replaced session ended: %+v", as)
	}
	post(tok, "session-end", hook.SessionEnd{SessionID: "s2"})
	if _, err := b.HarnessSession(ctx, "backend"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after its session ended: %v, want ErrNotFound", err)
	}

	// Any agent (here one toli has no built-in support for) collects its
	// session's notes, with the token in the Authorization header; another
	// session is told it's been replaced.
	post(tok, "session-start", hook.SessionStart{Harness: "my-agent", SessionID: "ses1"})
	b.NotifyAgent("backend", "toli: hello")
	get := func(session string) (*http.Response, hook.NotesResponse) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/hook/notifications?session="+session, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out hook.NotesResponse
		json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}
	if resp, out := get("ses1"); resp.StatusCode != http.StatusOK || len(out.Notes) != 1 || out.Notes[0] != "toli: hello" {
		t.Fatalf("notes: %d %+v", resp.StatusCode, out)
	}
	if resp, _ := get("other"); resp.StatusCode != http.StatusGone {
		t.Fatalf("another session's notes: %d, want 410", resp.StatusCode)
	}

	// Rejected: unknown token, disabled agent, missing fields.
	if resp, _ := post("nope", "session-start", start); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown token: %d, want 401", resp.StatusCode)
	}
	if resp, _ := post(tok, "session-start", hook.SessionStart{Harness: "claude-code"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no session_id: %d, want 400", resp.StatusCode)
	}
	if err := b.DisableAgent(ctx, "backend"); err != nil {
		t.Fatal(err)
	}
	if resp, _ := post(tok, "session-start", start); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("disabled agent: %d, want 401", resp.StatusCode)
	}
}
