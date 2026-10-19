package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/store"
)

func TestNormalizeSkills(t *testing.T) {
	got, err := normalizeSkills([]a2a.AgentSkill{
		{Name: "  Write SQL migrations "},
		{ID: "custom", Name: "Review PRs", Tags: []string{"git"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ID != "write-sql-migrations" || got[0].Name != "Write SQL migrations" || got[0].Tags == nil {
		t.Fatalf("skill 0 = %+v", got[0])
	}
	if got[1].ID != "custom" || len(got[1].Tags) != 1 {
		t.Fatalf("skill 1 = %+v", got[1])
	}

	for _, bad := range [][]a2a.AgentSkill{
		{{Name: ""}},
		{{Name: "Same"}, {Name: "same"}}, // both slug to "same"
	} {
		if _, err := normalizeSkills(bad); !errors.Is(err, ErrInvalidSkill) {
			t.Errorf("normalizeSkills(%+v) = %v, want ErrInvalidSkill", bad, err)
		}
	}
}

func TestDisableAndRotate(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "toli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b, err := New(ctx, st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := b.AddAgent(ctx, "backend", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := b.DisableAgent(ctx, "backend"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AgentByToken(ctx, tok); !errors.Is(err, ErrAgentDisabled) {
		t.Fatalf("disabled agent's token: %v, want ErrAgentDisabled", err)
	}
	if err := b.DisableAgent(ctx, "backend"); err != nil { // idempotent
		t.Fatal(err)
	}
	if err := b.EnableAgent(ctx, "backend"); err != nil {
		t.Fatal(err)
	}
	if a, err := b.AgentByToken(ctx, tok); err != nil || a.Name != "backend" {
		t.Fatalf("re-enabled agent's token: %v, %+v", err, a)
	}

	newTok, err := b.RotateAgentToken(ctx, "backend")
	if err != nil || newTok == tok {
		t.Fatalf("RotateAgentToken: %v (same token: %v)", err, newTok == tok)
	}
	if _, err := b.AgentByToken(ctx, tok); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old token after rotation: %v, want ErrNotFound", err)
	}
	if _, err := b.AgentByToken(ctx, newTok); err != nil {
		t.Fatalf("new token: %v", err)
	}

	for _, err := range []error{b.DisableAgent(ctx, "nope"), b.EnableAgent(ctx, "nope")} {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("unknown agent: %v, want ErrNotFound", err)
		}
	}
	if _, err := b.RotateAgentToken(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("rotate unknown agent: %v, want ErrNotFound", err)
	}
}

func TestCheckName(t *testing.T) {
	for _, ok := range []string{"backend", "zeb-testing", "agent_2", "A"} {
		if err := checkName(ok); err != nil {
			t.Errorf("checkName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "zeb's testing agent", "a b", "a/b", "é", string(make([]byte, 65))} {
		if err := checkName(bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("checkName(%q) = %v, want ErrInvalidName", bad, err)
		}
	}
}

func TestSetAgentNotify(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "toli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b, err := New(ctx, st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddAgent(ctx, "backend", "", nil); err != nil {
		t.Fatal(err)
	}
	stored := func() *store.Notify {
		t.Helper()
		a, err := b.GetAgent(ctx, "backend")
		if err != nil {
			t.Fatal(err)
		}
		return a.Notify
	}
	program := filepath.Join(t.TempDir(), "wake-backend")
	if err := os.WriteFile(program, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	secret, err := b.SetAgentNotify(ctx, "backend", "", "https://example.com/toli")
	if err != nil || secret == "" {
		t.Fatalf("https webhook: secret %q, %v", secret, err)
	}
	if n := stored(); n == nil || n.Webhook != "https://example.com/toli" || n.Secret != secret {
		t.Fatalf("stored = %+v", n)
	}
	for _, local := range []string{"http://localhost:8080/toli", "http://127.0.0.1/toli", "http://[::1]/toli"} {
		if _, err := b.SetAgentNotify(ctx, "backend", "", local); err != nil {
			t.Errorf("%s: %v", local, err)
		}
	}
	if secret, err := b.SetAgentNotify(ctx, "backend", program, ""); err != nil || secret != "" {
		t.Fatalf("command: secret %q, %v", secret, err)
	}
	if n := stored(); n == nil || n.Command != program || n.Webhook != "" {
		t.Fatalf("stored = %+v", n)
	}

	for _, bad := range []struct{ command, webhook string }{
		{"", "http://example.com/toli"},  // plain http off this machine
		{"", "example.com/toli"},         // not a URL
		{"wake-backend", ""},             // not absolute
		{program + "-missing", ""},       // no such file
		{filepath.Dir(program), ""},      // a directory
		{program, "https://example.com"}, // both
	} {
		if _, err := b.SetAgentNotify(ctx, "backend", bad.command, bad.webhook); !errors.Is(err, ErrInvalidNotify) {
			t.Errorf("SetAgentNotify(%q, %q) = %v, want ErrInvalidNotify", bad.command, bad.webhook, err)
		}
	}
	if n := stored(); n == nil || n.Command != program {
		t.Fatalf("a rejected target changed what's stored: %+v", n)
	}

	if _, err := b.SetAgentNotify(ctx, "backend", "", ""); err != nil {
		t.Fatal(err)
	}
	if n := stored(); n != nil {
		t.Fatalf("after clearing: %+v", n)
	}
	if _, err := b.SetAgentNotify(ctx, "nobody", program, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown agent: %v, want ErrNotFound", err)
	}
}
