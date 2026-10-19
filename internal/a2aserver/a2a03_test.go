package a2aserver_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2acompat/a2av0"

	"github.com/arjunvlama/toli/internal/a2aserver"
	"github.com/arjunvlama/toli/internal/peerclient"
)

// TestA2A03Client checks a peer on A2A 0.3 can work with a toli agent: it
// finds the 0.3 endpoint from the card's 0.3 fields, then sends, polls and
// cancels over 0.3, and needs its token like anyone else.
func TestA2A03Client(t *testing.T) {
	ctx := context.Background()
	a, b, aOnB := pair(t)

	// Read the worker's card as a 0.3 client does: without supportedInterfaces.
	resp, err := http.Get(a2aserver.CardURL(b.url, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "supportedInterfaces")
	raw, _ = json.Marshal(fields)
	card, err := a2av0.NewAgentCardParser()(raw)
	if err != nil {
		t.Fatal(err)
	}
	if ifs := card.SupportedInterfaces; len(ifs) != 1 || ifs[0].URL != b.url+"/a2a/worker/v0.3/" {
		t.Fatalf("0.3 interfaces = %+v, want %s/a2a/worker/v0.3/", ifs, b.url)
	}

	peers, err := a.b.ListPeers(ctx, "lead")
	if err != nil || len(peers) != 1 {
		t.Fatalf("A's peers: %v, %d", err, len(peers))
	}
	c, err := peerclient.NewClient(ctx, card, peers[0].OutboundToken)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.SendMessage(ctx, &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Add a /users endpoint")),
		Config:  &a2a.SendMessageConfig{ReturnImmediately: true},
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	id := res.(*a2a.Task).ID
	if got := onB(t, b, aOnB, id); got.History[0].Parts[0].Text() != "Add a /users endpoint" {
		t.Fatalf("B's task: %+v", got)
	}

	ask(t, b, id, "Which database?")
	got, err := c.GetTask(ctx, &a2a.GetTaskRequest{ID: id})
	if err != nil || got.Status.State != a2a.TaskStateInputRequired || got.Status.Message.Parts[0].Text() != "Which database?" {
		t.Fatalf("GetTask = %+v, %v; want input-required, asking which database", got, err)
	}
	canceled, err := c.CancelTask(ctx, &a2a.CancelTaskRequest{ID: id})
	if err != nil || canceled.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("CancelTask = %+v, %v; want canceled", canceled, err)
	}

	stranger, err := peerclient.NewClient(ctx, card, "not-a-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stranger.GetTask(ctx, &a2a.GetTaskRequest{ID: id}); err == nil {
		t.Error("a wrong token reached the agent over 0.3")
	}
}
