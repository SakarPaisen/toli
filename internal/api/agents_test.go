package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/arjunvlama/toli/internal/api"
)

func TestSetAgentNotify(t *testing.T) {
	ctx := context.Background()
	c, _, _ := setup(t)
	notify := func() *api.NotifyInfo {
		t.Helper()
		agents, err := c.ListAgents(ctx)
		if err != nil || len(agents) != 1 {
			t.Fatalf("ListAgents: %v, %d", err, len(agents))
		}
		return agents[0].Notify
	}

	set, err := c.SetAgentNotify(ctx, "backend", api.SetNotifyRequest{Webhook: "https://example.com/toli"})
	if err != nil || set.Secret == "" {
		t.Fatalf("set a webhook: %+v, %v", set, err)
	}
	if n := notify(); n == nil || n.Webhook != "https://example.com/…" || n.Command != "" {
		t.Fatalf("listed notify = %+v", n)
	}
	// A URL that's a credential itself is listed by its host only.
	if _, err := c.SetAgentNotify(ctx, "backend", api.SetNotifyRequest{Webhook: "https://hooks.slack.com/services/T0/B0/s3cret"}); err != nil {
		t.Fatal(err)
	}
	if n := notify(); n == nil || n.Webhook != "https://hooks.slack.com/…" {
		t.Fatalf("listed notify = %+v", n)
	}

	if _, err := c.SetAgentNotify(ctx, "backend", api.SetNotifyRequest{Webhook: "http://example.com/toli"}); err == nil || !strings.Contains(err.Error(), "invalid notify target") {
		t.Errorf("plain http off this machine: %v", err)
	}
	if _, err := c.SetAgentNotify(ctx, "nobody", api.SetNotifyRequest{}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown agent: %v", err)
	}

	if set, err := c.SetAgentNotify(ctx, "backend", api.SetNotifyRequest{}); err != nil || set.Secret != "" {
		t.Fatalf("clear: %+v, %v", set, err)
	}
	if n := notify(); n != nil {
		t.Fatalf("after clearing: %+v", n)
	}
}
