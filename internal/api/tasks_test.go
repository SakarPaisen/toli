package api_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/a2atest"
	"github.com/arjunvlama/toli/internal/api"
	"github.com/arjunvlama/toli/internal/backend"
	"github.com/arjunvlama/toli/internal/store"
	"github.com/arjunvlama/toli/internal/token"
)

// setup runs a backend behind the admin API with agent "backend" and its fake
// peer "bob", and returns an admin client, the backend (to create tasks as the
// agent would), and bob.
func setup(t *testing.T) (*api.Client, *backend.Backend, *a2atest.FakePeer) {
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
	t.Cleanup(func() { cancel(); <-done; st.Close() })

	if _, err := b.AddAgent(ctx, "backend", "", nil); err != nil {
		t.Fatal(err)
	}
	bob := a2atest.NewFakePeer(t)
	if _, _, err := b.AddPeer(ctx, "backend", "bob", bob.URL, "bobs-token"); err != nil {
		t.Fatal(err)
	}

	admin, hash := token.New()
	srv := httptest.NewServer(api.New(b, hash, "http://toli.example"))
	t.Cleanup(srv.Close)
	return api.NewClient(srv.URL, admin), b, bob
}

func TestTasks(t *testing.T) {
	ctx := context.Background()
	c, b, bob := setup(t)

	sess, _ := b.StartSession(ctx, "backend", "users api")
	done, err := b.Delegate(ctx, "backend", "bob", "Add a /users endpoint\nwith tests", sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	bob.Next(a2a.TaskStateCompleted, "done: GET /users")
	running, err := b.Delegate(ctx, "backend", "bob", "fix the build", "", 0)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for the poller to record the first task's completion.
	deadline := time.Now().Add(5 * time.Second)
	for {
		d, err := c.GetTask(ctx, "backend", string(done.A2A.ID))
		if err != nil {
			t.Fatal(err)
		}
		if d.State == string(a2a.TaskStateCompleted) {
			if d.Peer != "bob" || d.SessionID != sess.ID || d.Summary != "Add a /users endpoint" {
				t.Fatalf("detail = %+v", d.TaskInfo)
			}
			last := d.Messages[len(d.Messages)-1]
			if d.Messages[0].FromPeer || !last.FromPeer || last.Text != "done: GET /users" {
				t.Fatalf("messages = %+v", d.Messages)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task never completed; state %s", d.State)
		}
		time.Sleep(20 * time.Millisecond)
	}

	tasks, err := c.ListTasks(ctx, "backend")
	if err != nil || len(tasks) != 2 {
		t.Fatalf("ListTasks = %v, %+v", err, tasks)
	}
	if tasks[0].ID != string(running.A2A.ID) || tasks[0].SessionID != "" { // newest first; standalone
		t.Fatalf("first row = %+v, want the newer, standalone task", tasks[0])
	}

	got, err := c.CancelTask(ctx, "backend", string(running.A2A.ID))
	if err != nil || got.State != string(a2a.TaskStateCanceled) {
		t.Fatalf("CancelTask = %v, %+v", err, got)
	}
	got, err = c.CancelTask(ctx, "backend", string(done.A2A.ID)) // already finished
	if err != nil || got.State != string(a2a.TaskStateCompleted) {
		t.Fatalf("CancelTask on a finished task = %v, %+v", err, got)
	}

	for name, err := range map[string]error{
		"get unknown":          func() error { _, err := c.GetTask(ctx, "backend", "nope"); return err }(),
		"cancel unknown":       func() error { _, err := c.CancelTask(ctx, "backend", "nope"); return err }(),
		"another agent's task": func() error { _, err := c.GetTask(ctx, "frontend", string(done.A2A.ID)); return err }(),
	} {
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("%s: %v, want not found", name, err)
		}
	}
	if _, err := c.ListTasks(ctx, ""); err == nil || !strings.Contains(err.Error(), "agent is required") {
		t.Errorf("ListTasks without agent: %v", err)
	}
}

// Tasks a peer sent the agent, and the agent's share URL.
func TestAssignedTasks(t *testing.T) {
	ctx := context.Background()
	c, b, _ := setup(t)
	peers, err := b.ListPeers(ctx, "backend")
	if err != nil {
		t.Fatal(err)
	}
	// bob sends the agent a task (as the A2A server would store it).
	asBob := b.IncomingTasks(func(context.Context) (store.Caller, error) {
		return store.Caller{Agent: "backend", PeerID: peers[0].ID}, nil
	})
	req := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Review the schema\nin db/"))
	task := a2a.NewSubmittedTask(req, req)
	task.History = []*a2a.Message{req}
	if _, err := asBob.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AcceptInboundTask(ctx, "backend", task.ID, "on it"); err != nil {
		t.Fatal(err)
	}

	list, err := c.ListAssignedTasks(ctx, "backend")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != string(task.ID) || list[0].Peer != "bob" || list[0].Summary != "Review the schema" ||
		list[0].State != string(a2a.TaskStateWorking) || list[0].Deadline != nil {
		t.Fatalf("assigned tasks = %+v", list)
	}
	d, err := c.GetAssignedTask(ctx, "backend", string(task.ID))
	if err != nil {
		t.Fatal(err)
	}
	// bob delegated it: the request is bob's, the reply ours.
	if len(d.Messages) != 2 || !d.Messages[0].FromPeer || d.Messages[1].FromPeer || d.Messages[1].Text != "on it" {
		t.Fatalf("messages = %+v", d.Messages)
	}
	if _, err := c.GetAssignedTask(ctx, "backend", "nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown task: %v", err)
	}

	agents, err := c.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := "http://toli.example/a2a/backend/.well-known/agent-card.json"; agents[0].ShareURL != want {
		t.Fatalf("share_url = %q, want %q", agents[0].ShareURL, want)
	}
	if agents[0].Harness != "" {
		t.Fatalf("harness before any session registered = %q", agents[0].Harness)
	}
	if _, err := b.RegisterHarnessSession(ctx, "backend", "claude-code", "s1", nil); err != nil {
		t.Fatal(err)
	}
	if agents, _ := c.ListAgents(ctx); agents[0].Harness != "claude-code" {
		t.Fatalf("harness = %q, want claude-code", agents[0].Harness)
	}
}

// A peer added without its token gets it later; the token we issued it stays.
func TestSetPeerToken(t *testing.T) {
	ctx := context.Background()
	c, _, _ := setup(t)
	bob2 := a2atest.NewFakePeer(t)
	added, err := c.AddPeer(ctx, api.AddPeerRequest{Agent: "backend", Name: "bob2", URL: bob2.URL})
	if err != nil {
		t.Fatal(err)
	}
	if added.Peer.HasToken || added.Token == "" {
		t.Fatalf("added without a token: %+v", added)
	}
	if err := c.SetPeerToken(ctx, added.Peer.ID, "bobs-token"); err != nil {
		t.Fatal(err)
	}
	peers, err := c.ListPeers(ctx, "backend")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range peers {
		if p.ID == added.Peer.ID && !p.HasToken {
			t.Fatalf("after setting its token: %+v", p)
		}
	}
	if err := c.SetPeerToken(ctx, 999, "x"); err == nil || !strings.Contains(err.Error(), "no peer") {
		t.Fatalf("unknown peer: %v", err)
	}
}
