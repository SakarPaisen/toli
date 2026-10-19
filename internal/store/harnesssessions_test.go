package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHarnessSessions(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	addAgent(t, s, "backend")

	if _, err := s.HarnessSession(ctx, "backend"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no session yet: %v, want ErrNotFound", err)
	}
	first := HarnessSession{Agent: "backend", Harness: "claude-code", SessionID: "s1",
		Address: map[string]string{"socket": "/tmp/s1.sock"}, RegisteredAt: time.Now()}
	if err := s.SetHarnessSession(ctx, first); err != nil {
		t.Fatal(err)
	}
	got, err := s.HarnessSession(ctx, "backend")
	if err != nil || got.SessionID != "s1" || got.Address["socket"] != "/tmp/s1.sock" {
		t.Fatalf("HarnessSession = %+v, %v", got, err)
	}

	// A newer session replaces it; the old one ending doesn't remove the new one.
	second := first
	second.SessionID, second.Address = "s2", map[string]string{"socket": "/tmp/s2.sock"}
	if err := s.SetHarnessSession(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveHarnessSession(ctx, "backend", "s1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing the replaced session: %v, want ErrNotFound", err)
	}
	if got, _ := s.HarnessSession(ctx, "backend"); got.SessionID != "s2" {
		t.Fatalf("after the old session ended: %+v", got)
	}
	if err := s.RemoveHarnessSession(ctx, "backend", "s2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HarnessSession(ctx, "backend"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after its session ended: %v, want ErrNotFound", err)
	}

	if err := s.SetHarnessSession(ctx, HarnessSession{Agent: "nope", Harness: "x", SessionID: "x", RegisteredAt: time.Now()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: %v, want ErrNotFound", err)
	}
}
