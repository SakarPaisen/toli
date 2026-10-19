package a2aserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	"github.com/arjunvlama/toli/internal/a2aserver"
	"github.com/arjunvlama/toli/internal/backend"
	"github.com/arjunvlama/toli/internal/peerclient"
	"github.com/arjunvlama/toli/internal/store"
)

// daemon is one toli daemon's worth of backend: its store, its backend
// (running, with fast polling), and its A2A server.
type daemon struct {
	b   *backend.Backend
	st  *store.Store
	url string // the A2A server's URL (and public URL)
}

func newDaemon(t *testing.T) *daemon {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "toli.db"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := backend.New(ctx, st, backend.Options{PollEvery: 20 * time.Millisecond, CheckEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { b.Run(runCtx); close(done) }()

	var h http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	h = a2aserver.New(b, srv.URL)
	t.Cleanup(func() { srv.Close(); cancel(); <-done; st.Close() })
	return &daemon{b: b, st: st, url: srv.URL}
}

// pair sets up two daemons the way two people would: A's agent "lead" and
// B's agent "worker" add each other as peers, exchanging the tokens each
// side issues. Returns A, B, and B's peer id for A.
func pair(t *testing.T) (a, b *daemon, aOnB int64) {
	t.Helper()
	ctx := context.Background()
	a, b = newDaemon(t), newDaemon(t)
	if _, err := a.b.AddAgent(ctx, "lead", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.b.AddAgent(ctx, "worker", "", nil); err != nil {
		t.Fatal(err)
	}
	// B's human adds A's agent (no token from A yet) and sends A's human the
	// token B issued...
	peerA, tokForA, err := b.b.AddPeer(ctx, "worker", "lead", a2aserver.CardURL(a.url, "lead"), "")
	if err != nil {
		t.Fatal(err)
	}
	// ... which A's human enters when adding B's agent, and sends back the
	// token A issued, which B's human sets on A's agent.
	_, tokForB, err := a.b.AddPeer(ctx, "lead", "worker", a2aserver.CardURL(b.url, "worker"), tokForA)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.b.SetPeerToken(ctx, peerA.ID, tokForB); err != nil {
		t.Fatal(err)
	}
	return a, b, peerA.ID
}

// onB reads a task from B's incoming store as the peer that sent it.
func onB(t *testing.T, b *daemon, peer int64, id a2a.TaskID) *a2a.Task {
	t.Helper()
	caller := func(context.Context) (store.Caller, error) { return store.Caller{Agent: "worker", PeerID: peer}, nil }
	st, err := b.b.IncomingTasks(caller).Get(context.Background(), id)
	if err != nil {
		t.Fatalf("B doesn't have task %s: %v", id, err)
	}
	return st.Task
}

// waitOnA waits for A's copy of a task (kept up to date by A's poller) to
// reach state.
func waitOnA(t *testing.T, a *daemon, id a2a.TaskID, state a2a.TaskState) *backend.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		task, err := a.b.GetTask(context.Background(), "lead", id, -1, 0)
		if err != nil {
			t.Fatal(err)
		}
		if task.A2A.Status.State == state {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("A's copy of %s is %s, never %s", id, task.A2A.Status.State, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTolitoToli(t *testing.T) {
	ctx := context.Background()
	a, b, aOnB := pair(t)

	// A's agent delegates to B's agent, over A2A.
	task, err := a.b.Delegate(ctx, "lead", "worker", "Add a /users endpoint", "", 0)
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if task.A2A.Status.State != a2a.TaskStateSubmitted {
		t.Fatalf("delegated task is %s, want submitted", task.A2A.Status.State)
	}
	got := onB(t, b, aOnB, task.A2A.ID)
	if got.Status.State != a2a.TaskStateSubmitted || len(got.History) != 1 || got.History[0].Parts[0].Text() != "Add a /users endpoint" {
		t.Fatalf("B's copy: %s, history %+v", got.Status.State, got.History)
	}

	// A message from A on the submitted task: B records it, state unchanged.
	if _, _, err := a.b.Reply(ctx, "lead", task.A2A.ID, "also add tests"); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	got = onB(t, b, aOnB, task.A2A.ID)
	if got.Status.State != a2a.TaskStateSubmitted || len(got.History) != 2 {
		t.Fatalf("after a message: %s, %d messages", got.Status.State, len(got.History))
	}

	// A cancels: B marks it canceled; A's poller sees it.
	if _, err := a.b.CancelTask(ctx, "lead", task.A2A.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if got := onB(t, b, aOnB, task.A2A.ID); got.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("B's copy after cancel: %s", got.Status.State)
	}
	waitOnA(t, a, task.A2A.ID, a2a.TaskStateCanceled)
}

// Another peer of B can't see or touch A's tasks on B.
func TestOtherPeerSeesNothing(t *testing.T) {
	ctx := context.Background()
	a, b, aOnB := pair(t)
	task, err := a.b.Delegate(ctx, "lead", "worker", "secret work", "", 0)
	if err != nil {
		t.Fatal(err)
	}

	other := func(context.Context) (store.Caller, error) {
		return store.Caller{Agent: "worker", PeerID: aOnB + 1}, nil
	}
	if _, err := b.b.IncomingTasks(other).Get(ctx, task.A2A.ID); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("another peer reading A's task: %v, want ErrTaskNotFound", err)
	}
}

// ask has B's agent ask a question on a task, accepting it first if it's new.
func ask(t *testing.T, b *daemon, id a2a.TaskID, question string) {
	t.Helper()
	if _, err := b.b.AcceptInboundTask(context.Background(), "worker", id, ""); err != nil && !strings.Contains(err.Error(), "already accepted") {
		t.Fatal(err)
	}
	u := backend.InboundUpdate{State: a2a.TaskStateInputRequired, Message: question}
	if _, err := b.b.UpdateInboundTask(context.Background(), "worker", id, u, 0); err != nil {
		t.Fatal(err)
	}
}

// rawClient talks to B as A does, but lets a test send exact messages.
func rawClient(t *testing.T, a, b *daemon) *a2aclient.Client {
	t.Helper()
	ctx := context.Background()
	peers, err := a.b.ListPeers(ctx, "lead")
	if err != nil || len(peers) != 1 {
		t.Fatalf("A's peers: %v, %d", err, len(peers))
	}
	c, err := peerclient.NewClient(ctx, peers[0].Card, peers[0].OutboundToken)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMessages(t *testing.T) {
	ctx := context.Background()
	a, b, aOnB := pair(t)
	c := rawClient(t, a, b)
	send := func(m *a2a.Message) (*a2a.Task, error) {
		res, err := c.SendMessage(ctx, &a2a.SendMessageRequest{Message: m, Config: &a2a.SendMessageConfig{ReturnImmediately: true}})
		if err != nil {
			return nil, err
		}
		return res.(*a2a.Task), nil
	}
	text := func(m *a2a.Message) string { return m.Parts[0].Text() }

	task, err := send(a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Add a /users endpoint")))
	if err != nil {
		t.Fatal(err)
	}

	ask(t, b, task.ID, "Include deleted users?")

	// A's answer resumes it; the question goes into history before the answer.
	answer := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Only active ones"))
	answer.TaskID = task.ID
	got, err := send(answer)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.State != a2a.TaskStateWorking {
		t.Fatalf("after the answer: %s, want working", got.Status.State)
	}
	h := onB(t, b, aOnB, task.ID).History
	if len(h) != 3 || text(h[1]) != "Include deleted users?" || text(h[2]) != "Only active ones" {
		var texts []string
		for _, m := range h {
			texts = append(texts, text(m))
		}
		t.Fatalf("history = %q, want [request, question, answer]", texts)
	}

	// Sending the same message again (a retry) doesn't record it twice.
	if _, err := send(answer); err != nil {
		t.Fatal(err)
	}
	if n := len(onB(t, b, aOnB, task.ID).History); n != 3 {
		t.Fatalf("retried message recorded twice: %d messages", n)
	}

	// A message whose context isn't the task's is refused.
	wrong := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))
	wrong.TaskID, wrong.ContextID = task.ID, "some-other-context"
	if _, err := send(wrong); err == nil {
		t.Fatal("message with the wrong context: nil error")
	}

	// A finished task takes no more messages. (This task was sent with the raw
	// client, so it's canceled the same way; A's backend doesn't track it.)
	if canceled, err := c.CancelTask(ctx, &a2a.CancelTaskRequest{ID: task.ID}); err != nil || canceled.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("CancelTask: %v", err)
	}
	late := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("one more thing"))
	late.TaskID = task.ID
	if _, err := send(late); err == nil {
		t.Fatal("message on a canceled task: nil error")
	}

	// No streaming: our card says so (a2a-go's client then falls back to a
	// plain send), and the server refuses raw streaming requests.
	peers, _ := a.b.ListPeers(ctx, "lead") // A's token for B
	for _, method := range []string{"SendStreamingMessage", "SubscribeToTask"} {
		body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{"id":"` + string(task.ID) +
			`","message":{"messageId":"m","role":"ROLE_USER","parts":[{"text":"x"}]}}}`
		req, _ := http.NewRequest("POST", a2aserver.AgentURL(b.url, "worker"), strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+peers[0].OutboundToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(out), "UNSUPPORTED_OPERATION") {
			t.Errorf("%s: %s, want an unsupported-operation error", method, out)
		}
	}
}

// A send without ReturnImmediately is held until the task pauses or ends.
func TestBlockingSend(t *testing.T) {
	ctx := context.Background()
	a, b, aOnB := pair(t)
	c := rawClient(t, a, b)

	res, err := c.SendMessage(ctx, &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Add a /users endpoint")),
		Config:  &a2a.SendMessageConfig{ReturnImmediately: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := res.(*a2a.Task)

	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("also add tests"))
	msg.TaskID = task.ID
	type result struct {
		task *a2a.Task
		err  error
	}
	done := make(chan result, 1)
	go func() {
		res, err := c.SendMessage(ctx, &a2a.SendMessageRequest{Message: msg})
		got, _ := res.(*a2a.Task)
		done <- result{got, err}
	}()

	select {
	case r := <-done:
		t.Fatalf("blocking send returned while the task was %s: %v", task.Status.State, r.err)
	case <-time.After(time.Second):
	}

	// B's agent asks a question: the send returns with it.
	ask(t, b, task.ID, "Include deleted users?")
	select {
	case r := <-done:
		if r.err != nil || r.task.Status.State != a2a.TaskStateInputRequired {
			t.Fatalf("blocking send: %v, want the input-required task", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocking send still held after the task became input-required")
	}

	// A caller that gives up doesn't affect the task.
	giveUp, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	another := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Only active ones"))
	another.TaskID = task.ID
	if _, err := c.SendMessage(giveUp, &a2a.SendMessageRequest{Message: another}); err == nil {
		t.Fatal("send outlived its context")
	}
	if got := onB(t, b, aOnB, task.ID); got.Status.State != a2a.TaskStateWorking || len(got.History) != 4 {
		t.Fatalf("after the caller gave up: %s, %d messages; want working, 4", got.Status.State, len(got.History))
	}
}

// B's agent works a task A delegated; A's poller sees each step.
func TestUpdateInboundTask(t *testing.T) {
	ctx := context.Background()
	a, b, _ := pair(t)
	if _, err := b.b.AddAgent(ctx, "other", "", nil); err != nil {
		t.Fatal(err)
	}
	task, err := a.b.Delegate(ctx, "lead", "worker", "Add a /users endpoint", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	id := task.A2A.ID
	update := func(agent string, state a2a.TaskState, msg string, artifacts ...backend.Artifact) error {
		_, err := b.b.UpdateInboundTask(ctx, agent, id, backend.InboundUpdate{State: state, Message: msg, Artifacts: artifacts}, 0)
		return err
	}

	// Only the agent it was sent to can update it.
	if err := update("other", a2a.TaskStateWorking, ""); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("another agent's update: %v, want ErrTaskNotFound", err)
	}
	// Bad arguments.
	for _, u := range []backend.InboundUpdate{
		{State: a2a.TaskStateCanceled},
		{State: a2a.TaskStateInputRequired},
		{State: a2a.TaskStateRejected},
		{State: a2a.TaskStateWorking, Artifacts: []backend.Artifact{{Name: "diff"}}},
	} {
		if _, err := b.b.UpdateInboundTask(ctx, "worker", id, u, 0); !errors.Is(err, backend.ErrInvalidUpdate) {
			t.Errorf("update %+v: %v, want ErrInvalidUpdate", u, err)
		}
	}

	// Updates wait until it's accepted; it's accepted once.
	if err := update("worker", a2a.TaskStateWorking, "progress"); !errors.Is(err, backend.ErrInvalidUpdate) {
		t.Fatalf("update before accepting: %v, want ErrInvalidUpdate", err)
	}
	if _, err := b.b.AcceptInboundTask(ctx, "other", id, ""); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("another agent accepting: %v, want ErrTaskNotFound", err)
	}
	if _, err := b.b.AcceptInboundTask(ctx, "worker", id, "on it"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.b.AcceptInboundTask(ctx, "worker", id, ""); !errors.Is(err, backend.ErrInvalidUpdate) {
		t.Fatalf("accepting twice: %v, want ErrInvalidUpdate", err)
	}
	waitOnA(t, a, id, a2a.TaskStateWorking)

	// Ask, and wait for the answer: A's reply resumes the task.
	type result struct {
		task *backend.InboundTask
		err  error
	}
	asked := make(chan result, 1)
	go func() {
		u := backend.InboundUpdate{State: a2a.TaskStateInputRequired, Message: "Include deleted users?"}
		got, err := b.b.UpdateInboundTask(ctx, "worker", id, u, 10*time.Second)
		asked <- result{got, err}
	}()
	waitOnA(t, a, id, a2a.TaskStateInputRequired)
	if err := update("worker", a2a.TaskStateInputRequired, "And paging?"); !errors.Is(err, backend.ErrInvalidUpdate) {
		t.Fatalf("second open question: %v, want ErrInvalidUpdate", err)
	}
	if _, _, err := a.b.Reply(ctx, "lead", id, "Only active ones"); err != nil {
		t.Fatal(err)
	}
	r := <-asked
	if r.err != nil || r.task.Task.Status.State != a2a.TaskStateWorking {
		t.Fatalf("waiting for the answer: %v, want the task working again", r.err)
	}
	if h := r.task.Task.History; h[len(h)-1].Parts[0].Text() != "Only active ones" {
		t.Fatalf("last message %q, want the answer", h[len(h)-1].Parts[0].Text())
	}

	// Finish, with an artifact sent twice: the second replaces the first.
	if err := update("worker", a2a.TaskStateWorking, "", backend.Artifact{Name: "diff", Text: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := update("worker", a2a.TaskStateCompleted, "Done", backend.Artifact{Name: "diff", Text: "v2"}); err != nil {
		t.Fatal(err)
	}
	done := waitOnA(t, a, id, a2a.TaskStateCompleted)
	if arts := done.A2A.Artifacts; len(arts) != 1 || arts[0].Name != "diff" || arts[0].Parts[0].Text() != "v2" {
		t.Fatalf("A's artifacts: %+v, want one diff, v2", arts)
	}
	var texts []string
	for _, m := range done.A2A.History {
		texts = append(texts, m.Parts[0].Text())
	}
	texts = append(texts, done.A2A.Status.Message.Parts[0].Text())
	want := []string{"Add a /users endpoint", "on it", "Include deleted users?", "Only active ones", "Done"}
	if !slices.Equal(texts, want) {
		t.Fatalf("A sees %q, want %q", texts, want)
	}

	// A final task takes no more updates.
	if err := update("worker", a2a.TaskStateWorking, "one more thing"); !errors.Is(err, backend.ErrInvalidUpdate) {
		t.Fatalf("update after completed: %v, want ErrInvalidUpdate", err)
	}
}

// B's agent's updates and A's messages race on one task: every one of them is
// recorded (each write re-reads and retries on a version conflict).
func TestUpdateRaces(t *testing.T) {
	ctx := context.Background()
	a, b, aOnB := pair(t)
	task, err := a.b.Delegate(ctx, "lead", "worker", "Add a /users endpoint", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	id := task.A2A.ID
	if _, err := b.b.AcceptInboundTask(ctx, "worker", id, ""); err != nil {
		t.Fatal(err)
	}

	const n = 4
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := range n {
		wg.Go(func() {
			u := backend.InboundUpdate{State: a2a.TaskStateWorking, Message: fmt.Sprintf("note %d", i)}
			_, err := b.b.UpdateInboundTask(ctx, "worker", id, u, 0)
			errs <- err
		})
		wg.Go(func() {
			_, _, err := a.b.Reply(ctx, "lead", id, fmt.Sprintf("message %d", i))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	got := onB(t, b, aOnB, id)
	recorded := len(got.History)
	if got.Status.Message != nil {
		recorded++
	}
	if recorded != 1+2*n {
		t.Fatalf("%d messages recorded, want %d", recorded, 1+2*n)
	}
}

// A delegator's message while the worker is working lands after the worker's
// latest message, not before it.
func TestMessageOrder(t *testing.T) {
	ctx := context.Background()
	a, b, aOnB := pair(t)
	task, err := a.b.Delegate(ctx, "lead", "worker", "Add a /users endpoint", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	id := task.A2A.ID
	post := func(msg string) {
		t.Helper()
		u := backend.InboundUpdate{State: a2a.TaskStateWorking, Message: msg}
		if _, err := b.b.UpdateInboundTask(ctx, "worker", id, u, 0); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := b.b.AcceptInboundTask(ctx, "worker", id, ""); err != nil {
		t.Fatal(err)
	}
	post("found an old /users handler")
	if _, _, err := a.b.Reply(ctx, "lead", id, "also add tests"); err != nil {
		t.Fatal(err)
	}
	got := onB(t, b, aOnB, id)
	if got.Status.State != a2a.TaskStateWorking || got.Status.Message != nil {
		t.Fatalf("after the delegator's message: %s, status message %v; want working, none", got.Status.State, got.Status.Message)
	}
	post("added tests")

	got = onB(t, b, aOnB, id)
	var texts []string
	for _, m := range append(got.History, got.Status.Message) {
		texts = append(texts, m.Parts[0].Text())
	}
	want := []string{"Add a /users endpoint", "found an old /users handler", "also add tests", "added tests"}
	if !slices.Equal(texts, want) {
		t.Fatalf("conversation %q, want %q", texts, want)
	}
}

// Once paired, either side can delegate to the other.
func TestBothDirections(t *testing.T) {
	ctx := context.Background()
	a, b, _ := pair(t)
	if _, err := a.b.Delegate(ctx, "lead", "worker", "A to B", "", 0); err != nil {
		t.Fatalf("A delegating to B: %v", err)
	}
	if _, err := b.b.Delegate(ctx, "worker", "lead", "B to A", "", 0); err != nil {
		t.Fatalf("B delegating to A: %v", err)
	}
}

// A peer is connected once we can get its extended card with our token; that
// card (with skills) is what we keep.
func TestPeerStatus(t *testing.T) {
	ctx := context.Background()
	a, b := newDaemon(t), newDaemon(t)
	if _, err := a.b.AddAgent(ctx, "lead", "", nil); err != nil {
		t.Fatal(err)
	}
	skills := []a2a.AgentSkill{{Name: "Write SQL migrations", Description: "Postgres"}}
	if _, err := b.b.AddAgent(ctx, "worker", "Owns the API", skills); err != nil {
		t.Fatal(err)
	}
	// status waits for the peer's next check (adding it or setting its token
	// asks for one), then returns the peer and the check's error.
	status := func(d *daemon, agent string) (store.Peer, error) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			peers, err := d.b.ListPeers(ctx, agent)
			if err != nil || len(peers) != 1 {
				t.Fatalf("%s's peers: %v, %d", agent, err, len(peers))
			}
			st, err := d.b.PeerStatus(peers[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if !st.LastSeen.IsZero() || st.LastError != nil {
				return peers[0], st.LastError
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s's peer was never checked", agent)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// B adds A without a token: not connected.
	peerA, tokForA, err := b.b.AddPeer(ctx, "worker", "lead", a2aserver.CardURL(a.url, "lead"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := status(b, "worker"); !errors.Is(err, peerclient.ErrNoToken) {
		t.Fatalf("B's status for A without a token: %v, want ErrNoToken", err)
	}

	// A adds B with B's token: connected, with B's skills from its extended card.
	_, tokForB, err := a.b.AddPeer(ctx, "lead", "worker", a2aserver.CardURL(b.url, "worker"), tokForA)
	if err != nil {
		t.Fatal(err)
	}
	p, err := status(a, "lead")
	if err != nil {
		t.Fatalf("A's status for B: %v, want connected", err)
	}
	if len(p.Card.Skills) != 1 || p.Card.Skills[0].Name != "Write SQL migrations" {
		t.Fatalf("A's card for B: skills %+v, want B's (from the extended card)", p.Card.Skills)
	}

	// A wrong token: not connected. The right one: connected.
	if err := b.b.SetPeerToken(ctx, peerA.ID, "wrong"); err != nil {
		t.Fatal(err)
	}
	if _, err := status(b, "worker"); err == nil {
		t.Fatal("B's status for A with a wrong token: connected")
	}
	if err := b.b.SetPeerToken(ctx, peerA.ID, tokForB); err != nil {
		t.Fatal(err)
	}
	if _, err := status(b, "worker"); err != nil {
		t.Fatalf("B's status for A with its token: %v", err)
	}
}

// inbox registers a fake Claude Code session for agent on d and returns the
// text of each note delivered to it.
func inbox(t *testing.T, d *daemon, agent string) <-chan string {
	t.Helper()
	dir, err := os.MkdirTemp("", "toli") // short: Unix socket paths are limited to ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "inbox.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	notes := make(chan string, 16)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			dec := json.NewDecoder(conn)
			for {
				var line struct {
					Type    string
					Message struct{ Content string }
				}
				if dec.Decode(&line) != nil {
					break
				}
				if line.Type == "user" {
					notes <- line.Message.Content
				}
			}
			conn.Close()
		}
	}()
	if _, err := d.b.RegisterHarnessSession(context.Background(), agent, "claude-code", "s1", map[string]string{"socket": sock}); err != nil {
		t.Fatal(err)
	}
	return notes
}

// next returns the next note, failing if none comes.
func next(t *testing.T, notes <-chan string) string {
	t.Helper()
	select {
	case n := <-notes:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("no note")
		return ""
	}
}

// A new task wakes the worker's registered session.
func TestNewTaskWakesWorker(t *testing.T) {
	ctx := context.Background()
	a, b, _ := pair(t)
	notes := inbox(t, b, "worker")
	if _, err := a.b.Delegate(ctx, "lead", "worker", "Add a /users endpoint\nwith tests", "", 0); err != nil {
		t.Fatal(err)
	}
	if n := next(t, notes); n != `toli: new task from lead: "Add a /users endpoint". Call list_available_tasks.` {
		t.Fatalf("note = %q", n)
	}
}

// The delegator's messages on a task wake the worker, worded by where the task is.
func TestMessagesWakeWorker(t *testing.T) {
	ctx := context.Background()
	a, b, _ := pair(t)
	notes := inbox(t, b, "worker")
	task, err := a.b.Delegate(ctx, "lead", "worker", "Add a /users endpoint", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	id := task.A2A.ID
	next(t, notes) // the new task

	reply := func(text string) {
		t.Helper()
		if _, _, err := a.b.Reply(ctx, "lead", id, text); err != nil {
			t.Fatal(err)
		}
	}
	want := func(prefix string) {
		t.Helper()
		if n := next(t, notes); !strings.HasPrefix(n, prefix) {
			t.Fatalf("note = %q, want it to start with %q", n, prefix)
		}
	}

	reply("use cursor pagination")
	want(`toli: lead added to task ` + string(id) + `: "use cursor pagination". It's in list_available_tasks.`)

	if _, err := b.b.AcceptInboundTask(ctx, "worker", id, ""); err != nil {
		t.Fatal(err)
	}
	reply("also add tests")
	want(`toli: lead sent a message on task ` + string(id) + `: "also add tests". If you haven't already, respond with update_task.`)

	ask(t, b, id, "Include deleted users?")
	reply("Only active ones")
	want(`toli: lead answered your question on task ` + string(id) + `: "Include deleted users?" → "Only active ones". If there's more to do, respond with update_task.`)

	select {
	case n := <-notes:
		t.Fatalf("unexpected note: %q", n)
	case <-time.After(200 * time.Millisecond):
	}
}

// The worker's progress, question, and answer wake the delegating agent.
func TestChangesWakeDelegator(t *testing.T) {
	ctx := context.Background()
	a, b, _ := pair(t)
	notes := inbox(t, a, "lead")
	task, err := a.b.Delegate(ctx, "lead", "worker", "Add a /users endpoint", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	id := string(task.A2A.ID)
	want := func(note string) {
		t.Helper()
		if n := next(t, notes); n != note {
			t.Fatalf("note = %q\nwant %q", n, note)
		}
	}

	if _, err := b.b.AcceptInboundTask(ctx, "worker", task.A2A.ID, "on it"); err != nil {
		t.Fatal(err)
	}
	want(`toli: worker posted on task ` + id + `: "on it".`)

	ask(t, b, task.A2A.ID, "Include deleted users?")
	want(`toli: worker asked on task ` + id + `: "Include deleted users?". If you haven't already, answer with reply.`)

	if _, _, err := a.b.Reply(ctx, "lead", task.A2A.ID, "Only active ones"); err != nil {
		t.Fatal(err)
	}
	u := backend.InboundUpdate{State: a2a.TaskStateCompleted, Message: "Done: GET /users"}
	if _, err := b.b.UpdateInboundTask(ctx, "worker", task.A2A.ID, u, 0); err != nil {
		t.Fatal(err)
	}
	want(`toli: worker completed task ` + id + `: "Done: GET /users". See get_delegated_task_updates.`)
}
