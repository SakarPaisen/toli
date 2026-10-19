package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arjunvlama/toli/internal/a2aserver"
	"github.com/arjunvlama/toli/internal/backend"
	"github.com/arjunvlama/toli/internal/mcp"
	"github.com/arjunvlama/toli/internal/store"
)

// localPair is one daemon with two agents, "lead" and "worker", that are
// each other's peers over the daemon's own A2A listener. Returns an MCP
// session for each.
func localPair(t *testing.T) (lead, worker *sdkmcp.ClientSession) {
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

	var a2aHandler http.Handler
	a2aSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a2aHandler.ServeHTTP(w, r) }))
	a2aHandler = a2aserver.New(b, a2aSrv.URL)
	mux := http.NewServeMux()
	h := mcp.New(b)
	mux.Handle("/mcp/", h)
	mcpSrv := httptest.NewServer(mux)
	t.Cleanup(func() { mcpSrv.Close(); a2aSrv.Close(); cancel(); <-done; st.Close() })

	leadTok, err := b.AddAgent(ctx, "lead", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	workerTok, err := b.AddAgent(ctx, "worker", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, tokForLead, err := b.AddPeer(ctx, "worker", "lead", a2aserver.CardURL(a2aSrv.URL, "lead"), "unused")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.AddPeer(ctx, "lead", "worker", a2aserver.CardURL(a2aSrv.URL, "worker"), tokForLead); err != nil {
		t.Fatal(err)
	}

	e := &env{url: mcpSrv.URL}
	if lead, err = e.dial(t, leadTok); err != nil {
		t.Fatal(err)
	}
	if worker, err = e.dial(t, workerTok); err != nil {
		t.Fatal(err)
	}
	return lead, worker
}

// waitForMessage long-polls get_delegated_task_updates until the peer's
// messages include text.
func waitForMessage(t *testing.T, cs *sdkmcp.ClientSession, id, text string) update {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	args := map[string]any{"task_id": id}
	for time.Now().Before(deadline) {
		var u update
		if e := call(t, cs, "get_delegated_task_updates", args, &u); e != "" {
			t.Fatalf("get_delegated_task_updates: %s", e)
		}
		if slices.Contains(u.Messages, text) {
			return u
		}
		args = map[string]any{"task_id": id, "after_version": u.Version, "wait_seconds": 2}
	}
	t.Fatalf("task %s: never saw %q", id, text)
	return update{}
}

type inboundList struct {
	Tasks []struct {
		TaskID             string `json:"task_id"`
		Peer               string `json:"peer"`
		SessionID          string `json:"session_id"`
		Request            string `json:"request"`
		State              string `json:"state"`
		UnansweredMessages int    `json:"unanswered_messages"`
	} `json:"tasks"`
}

type fullTask struct {
	Direction string `json:"direction"`
	Peer      string `json:"peer"`
	State     string `json:"state"`
	Messages  []struct {
		From string `json:"from"`
		Text string `json:"text"`
	} `json:"messages"`
	Artifacts []struct {
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"artifacts"`
}

// Two agents on one daemon: lead delegates to worker, worker does the work.
func TestWorkingTools(t *testing.T) {
	lead, worker := localPair(t)

	var in inboundList
	call(t, worker, "list_available_tasks", nil, &in)
	if len(in.Tasks) != 0 {
		t.Fatalf("list_available_tasks before any work = %+v", in)
	}

	// Waiting for new work: delegated while worker waits.
	var s summary
	got := make(chan inboundList, 1)
	go func() {
		var in inboundList
		call(t, worker, "list_available_tasks", map[string]any{"wait_seconds": 10}, &in)
		got <- in
	}()
	time.Sleep(200 * time.Millisecond)
	call(t, lead, "delegate", map[string]any{"peer": "worker", "message": "Add a /users endpoint\nwith tests"}, &s)
	in = <-got
	if len(in.Tasks) != 1 {
		t.Fatalf("list_available_tasks = %+v", in)
	}
	if it := in.Tasks[0]; it.TaskID != s.TaskID || it.Peer != "lead" || it.State != "submitted" ||
		it.Request != "Add a /users endpoint\nwith tests" || it.UnansweredMessages != 0 || it.SessionID == "" {
		t.Fatalf("inbound task = %+v", it)
	}
	id := s.TaskID
	updateTask := func(args map[string]any) summary {
		t.Helper()
		args["task_id"] = id
		var out summary
		if e := call(t, worker, "update_task", args, &out); e != "" {
			t.Fatalf("update_task(%v): %s", args, e)
		}
		return out
	}

	// Accept it: once, and only then update it.
	if e := call(t, worker, "update_task", map[string]any{"task_id": id, "state": "working"}, nil); !strings.Contains(e, "accept it first") {
		t.Fatalf("update_task before accept_task: %q", e)
	}
	call(t, lead, "reply", map[string]any{"task_id": id, "message": "use cursor pagination"}, nil)
	var acc struct {
		State    string   `json:"state"`
		Request  string   `json:"request"`
		Messages []string `json:"messages"`
	}
	call(t, worker, "accept_task", map[string]any{"task_id": id}, &acc)
	if acc.State != "working" || acc.Request != "Add a /users endpoint\nwith tests" || !slices.Equal(acc.Messages, []string{"use cursor pagination"}) {
		t.Fatalf("accept_task = %+v", acc)
	}
	if e := call(t, worker, "accept_task", map[string]any{"task_id": id}, nil); !strings.Contains(e, "already accepted") {
		t.Fatalf("accept_task twice: %q", e)
	}
	call(t, worker, "list_available_tasks", nil, &in)
	if len(in.Tasks) != 0 {
		t.Fatalf("available after accepting = %+v", in)
	}

	// Progress, and a message from lead while worker works.
	updateTask(map[string]any{"state": "working", "message": "found an old /users handler"})
	u := waitForMessage(t, lead, id, "found an old /users handler")
	if !slices.Equal(u.Messages, []string{"found an old /users handler"}) {
		t.Fatalf("lead's update = %+v", u)
	}
	call(t, lead, "reply", map[string]any{"task_id": id, "message": "also add tests"}, nil)
	call(t, worker, "list_accepted_tasks", nil, &in)
	if len(in.Tasks) != 1 || in.Tasks[0].UnansweredMessages != 1 {
		t.Fatalf("after lead's message: %+v, want 1 unanswered", in)
	}
	updateTask(map[string]any{"state": "working", "message": "added tests"})
	call(t, worker, "list_accepted_tasks", nil, &in)
	if in.Tasks[0].UnansweredMessages != 0 {
		t.Fatalf("after worker's reply: %+v, want 0 unanswered", in)
	}

	// Lead sees only what worker said since lead's message.
	u = waitForMessage(t, lead, id, "added tests")
	if !slices.Equal(u.Messages, []string{"added tests"}) {
		t.Fatalf("lead's update after its message = %+v", u)
	}

	// Ask and wait: lead's reply comes back as the answer.
	asked := make(chan summary, 1)
	go func() {
		var out summary
		call(t, worker, "update_task", map[string]any{"task_id": id, "state": "input-required", "message": "Include deleted users?", "wait_seconds": 10}, &out)
		asked <- out
	}()
	waitFor(t, lead, id, "input-required")
	call(t, lead, "reply", map[string]any{"task_id": id, "message": "Only active ones"}, nil)
	if a := <-asked; a.State != "working" || a.Answer == nil || a.Answer.From != "lead" || a.Answer.Text != "Only active ones" {
		t.Fatalf("update_task with wait = %+v", a)
	}

	// Errors read plainly.
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"task_id": "nope", "state": "working"}, "task nope not found"},
		{map[string]any{"task_id": id, "state": "bogus"}, `unknown state "bogus"`},
		{map[string]any{"task_id": id, "state": "rejected"}, "rejected needs a message"},
	} {
		e := call(t, worker, "update_task", c.args, nil)
		if !strings.Contains(e, c.want) || strings.Contains(e, "invalid update") {
			t.Errorf("update_task(%v) error = %q, want %q", c.args, e, c.want)
		}
	}
	// Lead can't update a task it delegated.
	if e := call(t, lead, "update_task", map[string]any{"task_id": id, "state": "working"}, nil); !strings.Contains(e, "not found") {
		t.Errorf("lead updating its own delegated task: %q, want not found", e)
	}

	// Finish.
	updateTask(map[string]any{"state": "completed", "message": "Done", "artifacts": []map[string]any{{"name": "diff", "text": "+GET /users"}}})
	if u := waitFor(t, lead, id, "completed"); !slices.Equal(u.Messages, []string{"Done"}) {
		t.Fatalf("lead's final update = %+v", u)
	}

	// The whole conversation, from both sides, labeled by name.
	want := []string{"lead: Add a /users endpoint\nwith tests", "lead: use cursor pagination", "worker: found an old /users handler", "lead: also add tests",
		"worker: added tests", "worker: Include deleted users?", "lead: Only active ones", "worker: Done"}
	for _, side := range []struct {
		cs        *sdkmcp.ClientSession
		direction string
		peer      string
	}{{lead, "delegated", "worker"}, {worker, "assigned", "lead"}} {
		var full fullTask
		call(t, side.cs, "get_task_full", map[string]any{"task_id": id}, &full)
		var got []string
		for _, m := range full.Messages {
			got = append(got, m.From+": "+m.Text)
		}
		if full.Direction != side.direction || full.Peer != side.peer || full.State != "completed" ||
			!slices.Equal(got, want) || len(full.Artifacts) != 1 || full.Artifacts[0].Text != "+GET /users" {
			t.Errorf("%s's get_task_full = %+v\nmessages %q", side.direction, full, got)
		}
	}

	// Finished work is out of the default list.
	call(t, worker, "list_accepted_tasks", nil, &in)
	if len(in.Tasks) != 0 {
		t.Fatalf("default list after completing = %+v", in)
	}
	call(t, worker, "list_accepted_tasks", map[string]any{"state": "completed"}, &in)
	if len(in.Tasks) != 1 {
		t.Fatalf("list_accepted_tasks(completed) = %+v", in)
	}
}
