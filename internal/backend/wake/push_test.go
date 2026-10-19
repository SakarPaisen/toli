package wake

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/store"
)

// The test binary doubles as a notify program: with TOLI_TEST_PUSH_OUT set,
// it saves its stdin there and exits, before any test runs.
func init() {
	if out := os.Getenv("TOLI_TEST_PUSH_OUT"); out != "" {
		b, _ := io.ReadAll(os.Stdin)
		os.WriteFile(out, b, 0o600)
		os.Exit(0)
	}
}

// pushSetup is a service with one agent, "backend", and no session.
func pushSetup(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "toli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.AddAgent(ctx, store.Agent{Name: "backend", Card: a2a.AgentCard{Name: "backend"}}, "hash"); err != nil {
		t.Fatal(err)
	}
	retryAfter = 10 * time.Millisecond
	return New(st), st
}

// webhook records what it's sent, answering with the next of statuses (then
// 200s).
func webhook(t *testing.T, statuses ...int) (url string, got func() []http.Header, bodies func() []string) {
	t.Helper()
	var (
		mu      sync.Mutex
		headers []http.Header
		texts   []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		headers, texts = append(headers, r.Header.Clone()), append(texts, string(b))
		status := http.StatusOK
		if len(statuses) > 0 {
			status, statuses = statuses[0], statuses[1:]
		}
		mu.Unlock()
		if status == http.StatusFound {
			http.Redirect(w, r, "http://elsewhere.example/", status)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/hooks/s3cret-path",
		func() []http.Header { mu.Lock(); defer mu.Unlock(); return headers },
		func() []string { mu.Lock(); defer mu.Unlock(); return texts }
}

func TestPushWebhook(t *testing.T) {
	ctx := context.Background()
	s, st := pushSetup(t)

	// Nothing set, no session: nothing happens.
	if err := s.Notify(ctx, "backend", "hi"); err != nil {
		t.Fatalf("nowhere to push: %v", err)
	}

	url, headers, bodies := webhook(t)
	if err := st.SetAgentNotify(ctx, "backend", &store.Notify{Webhook: url, Secret: "s3cret"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Notify(ctx, "backend", "toli: new task from bob"); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if b := bodies(); len(b) != 1 || json.Unmarshal([]byte(b[0]), &got) != nil ||
		got["agent"] != "backend" || got["text"] != "toli: new task from bob" {
		t.Fatalf("webhook got %q", b)
	}
	if h := headers()[0]; h.Get("Authorization") != "Bearer s3cret" || h.Get("Content-Type") != "application/json" {
		t.Errorf("headers: %v", h)
	}
}

func TestPushWebhookRetryAndRedirect(t *testing.T) {
	ctx := context.Background()
	s, st := pushSetup(t)

	// A failure is retried once.
	url, _, bodies := webhook(t, http.StatusInternalServerError)
	st.SetAgentNotify(ctx, "backend", &store.Notify{Webhook: url, Secret: "s3cret"})
	if err := s.Notify(ctx, "backend", "hi"); err != nil || len(bodies()) != 2 {
		t.Fatalf("after a 500: %v, %d calls; want delivered on the retry", err, len(bodies()))
	}

	// A redirect isn't followed, and the error doesn't give the URL away.
	url, _, bodies = webhook(t, http.StatusFound, http.StatusFound)
	st.SetAgentNotify(ctx, "backend", &store.Notify{Webhook: url, Secret: "s3cret"})
	err := s.Notify(ctx, "backend", "hi")
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect: %v, want a failure", err)
	}
	if strings.Contains(err.Error(), "s3cret-path") {
		t.Errorf("the error has the webhook's URL: %v", err)
	}
}

func TestPushCommand(t *testing.T) {
	ctx := context.Background()
	s, st := pushSetup(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "stdin.json")
	t.Setenv("TOLI_TEST_PUSH_OUT", out) // the program (this binary) saves its stdin here
	st.SetAgentNotify(ctx, "backend", &store.Notify{Command: self})

	if err := s.Notify(ctx, "backend", "toli: bob replied"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	var got map[string]string
	if err != nil || json.Unmarshal(b, &got) != nil || got["agent"] != "backend" || got["text"] != "toli: bob replied" {
		t.Fatalf("the program got %q (%v)", b, err)
	}
}

// A session that's gone is dropped, and its notification pushed instead.
func TestPushAfterSessionGone(t *testing.T) {
	ctx := context.Background()
	s, st := pushSetup(t)
	url, _, bodies := webhook(t)
	st.SetAgentNotify(ctx, "backend", &store.Notify{Webhook: url, Secret: "s3cret"})
	gone := filepath.Join(t.TempDir(), "no.sock")
	if err := s.Register(ctx, "backend", "claude-code", "s1", map[string]string{"socket": gone}); err != nil {
		t.Fatal(err)
	}

	if err := s.Notify(ctx, "backend", "hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.HarnessSession(ctx, "backend"); err == nil {
		t.Error("the gone session is still registered")
	}
	if len(bodies()) != 1 {
		t.Fatalf("webhook got %d calls, want 1", len(bodies()))
	}
}
