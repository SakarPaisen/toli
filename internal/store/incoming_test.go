package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

type callerKey struct{}

// as returns a context that the store will treat as coming from c.
func as(c Caller) context.Context {
	return context.WithValue(context.Background(), callerKey{}, c)
}

func testCaller(ctx context.Context) (Caller, error) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	if !ok {
		return Caller{}, errors.New("no caller")
	}
	return c, nil
}

func incomingTask(id, contextID string, state a2a.TaskState) *a2a.Task {
	return &a2a.Task{ID: a2a.TaskID(id), ContextID: contextID, Status: a2a.TaskStatus{State: state}}
}

func TestIncomingTasks(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	addAgent(t, s, "backend")
	bobID, _ := s.AddPeer(ctx, Peer{Agent: "backend", Name: "bob", URL: "x"}, "bob-hash")
	carolID, _ := s.AddPeer(ctx, Peer{Agent: "backend", Name: "carol", URL: "y"}, "carol-hash")
	bob, carol := as(Caller{Agent: "backend", PeerID: bobID}), as(Caller{Agent: "backend", PeerID: carolID})
	it := s.IncomingTasks(testCaller)

	// Create, and duplicates.
	if v, err := it.Create(bob, incomingTask("t1", "c1", a2a.TaskStateSubmitted)); err != nil || v != 1 {
		t.Fatalf("Create: %v, version %d", err, v)
	}
	if _, err := it.Create(bob, incomingTask("t1", "c1", a2a.TaskStateSubmitted)); !errors.Is(err, taskstore.ErrTaskAlreadyExists) {
		t.Fatalf("duplicate Create: %v, want ErrTaskAlreadyExists", err)
	}

	// Get: the owner sees it; another peer doesn't.
	got, err := it.Get(bob, "t1")
	if err != nil || got.Version != 1 || got.Task.ContextID != "c1" || got.User == "" {
		t.Fatalf("Get: %v, %+v", err, got)
	}
	if _, err := it.Get(carol, "t1"); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("another peer's Get: %v, want ErrTaskNotFound", err)
	}

	// Update: versioned, owner only.
	working := incomingTask("t1", "c1", a2a.TaskStateWorking)
	if v, err := it.Update(bob, &taskstore.UpdateRequest{Task: working, PrevVersion: 1}); err != nil || v != 2 {
		t.Fatalf("Update: %v, version %d", err, v)
	}
	if _, err := it.Update(bob, &taskstore.UpdateRequest{Task: working, PrevVersion: 1}); !errors.Is(err, taskstore.ErrConcurrentModification) {
		t.Fatalf("stale Update: %v, want ErrConcurrentModification", err)
	}
	if v, err := it.Update(bob, &taskstore.UpdateRequest{Task: working}); err != nil || v != 3 {
		t.Fatalf("Update without a version: %v, version %d", err, v)
	}
	if _, err := it.Update(carol, &taskstore.UpdateRequest{Task: working, PrevVersion: 3}); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("another peer's Update: %v, want ErrTaskNotFound", err)
	}
	if _, err := it.Update(bob, &taskstore.UpdateRequest{Task: incomingTask("nope", "c1", a2a.TaskStateWorking)}); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("Update unknown: %v, want ErrTaskNotFound", err)
	}
	if got, _ := it.Get(bob, "t1"); got.Task.Status.State != a2a.TaskStateWorking || got.Version != 3 {
		t.Fatalf("after updates: %s, version %d", got.Task.Status.State, got.Version)
	}

	// The agent itself (no peer) sees and updates its peers' tasks; another
	// agent doesn't.
	addAgent(t, s, "frontend")
	agent, other := as(Caller{Agent: "backend"}), as(Caller{Agent: "frontend"})
	if got, err := it.Get(agent, "t1"); err != nil || got.User != strconv.FormatInt(bobID, 10) {
		t.Fatalf("the agent's Get: %v, %+v", err, got)
	}
	if v, err := it.Update(agent, &taskstore.UpdateRequest{Task: working, PrevVersion: 3}); err != nil || v != 4 {
		t.Fatalf("the agent's Update: %v, version %d", err, v)
	}
	if _, err := it.Get(other, "t1"); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("another agent's Get: %v, want ErrTaskNotFound", err)
	}
	if _, err := it.Update(other, &taskstore.UpdateRequest{Task: working}); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("another agent's Update: %v, want ErrTaskNotFound", err)
	}

	// Removing the peer removes its tasks.
	if _, err := s.RemovePeer(ctx, bobID); err != nil {
		t.Fatal(err)
	}
	if _, err := it.Get(bob, "t1"); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("task survived its peer: %v", err)
	}
}

func TestIncomingTasksList(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	addAgent(t, s, "backend")
	bobID, _ := s.AddPeer(ctx, Peer{Agent: "backend", Name: "bob", URL: "x"}, "bob-hash")
	carolID, _ := s.AddPeer(ctx, Peer{Agent: "backend", Name: "carol", URL: "y"}, "carol-hash")
	bob, carol := as(Caller{Agent: "backend", PeerID: bobID}), as(Caller{Agent: "backend", PeerID: carolID})
	it := s.IncomingTasks(testCaller)

	// Created oldest first: t1 (c1), t2 (c1, working), t3 (c2). t1 gets
	// history and an artifact; carol has one task of her own.
	t1 := incomingTask("t1", "c1", a2a.TaskStateSubmitted)
	for i := range 3 {
		t1.History = append(t1.History, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(string(rune('a'+i)))))
	}
	t1.Artifacts = []*a2a.Artifact{{ID: "art", Parts: a2a.ContentParts{a2a.NewTextPart("x")}}}
	for _, task := range []*a2a.Task{t1, incomingTask("t2", "c1", a2a.TaskStateWorking), incomingTask("t3", "c2", a2a.TaskStateSubmitted)} {
		if _, err := it.Create(bob, task); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond) // distinct updated_at
	}
	if _, err := it.Create(carol, incomingTask("t4", "c9", a2a.TaskStateSubmitted)); err != nil {
		t.Fatal(err)
	}

	ids := func(resp *a2a.ListTasksResponse) []string {
		var out []string
		for _, task := range resp.Tasks {
			out = append(out, string(task.ID))
		}
		return out
	}
	list := func(ctx context.Context, req *a2a.ListTasksRequest) *a2a.ListTasksResponse {
		t.Helper()
		resp, err := it.List(ctx, req)
		if err != nil {
			t.Fatalf("List(%+v): %v", req, err)
		}
		return resp
	}

	// Newest first, only the caller's.
	if got := ids(list(bob, &a2a.ListTasksRequest{})); !slices.Equal(got, []string{"t3", "t2", "t1"}) {
		t.Errorf("bob's tasks = %v, want [t3 t2 t1]", got)
	}
	if got := ids(list(carol, &a2a.ListTasksRequest{})); !slices.Equal(got, []string{"t4"}) {
		t.Errorf("carol's tasks = %v, want [t4]", got)
	}

	// Filters.
	if got := ids(list(bob, &a2a.ListTasksRequest{ContextID: "c1"})); !slices.Equal(got, []string{"t2", "t1"}) {
		t.Errorf("context c1 = %v", got)
	}
	if got := ids(list(bob, &a2a.ListTasksRequest{Status: a2a.TaskStateWorking})); !slices.Equal(got, []string{"t2"}) {
		t.Errorf("working = %v", got)
	}

	// Paging.
	p1 := list(bob, &a2a.ListTasksRequest{PageSize: 2})
	if got := ids(p1); !slices.Equal(got, []string{"t3", "t2"}) || p1.NextPageToken == "" || p1.TotalSize != 3 {
		t.Fatalf("page 1 = %v, next %q, total %d", got, p1.NextPageToken, p1.TotalSize)
	}
	p2 := list(bob, &a2a.ListTasksRequest{PageSize: 2, PageToken: p1.NextPageToken})
	if got := ids(p2); !slices.Equal(got, []string{"t1"}) || p2.NextPageToken != "" {
		t.Errorf("page 2 = %v, next %q", got, p2.NextPageToken)
	}
	if _, err := it.List(bob, &a2a.ListTasksRequest{PageSize: 101}); !errors.Is(err, a2a.ErrInvalidRequest) {
		t.Errorf("page size 101: %v, want ErrInvalidRequest", err)
	}
	if _, err := it.List(bob, &a2a.ListTasksRequest{PageToken: "bogus"}); !errors.Is(err, a2a.ErrInvalidRequest) {
		t.Errorf("bad page token: %v, want ErrInvalidRequest", err)
	}

	// History is trimmed to the most recent N; artifacts only on request.
	two := 2
	got := list(bob, &a2a.ListTasksRequest{ContextID: "c1", Status: a2a.TaskStateSubmitted, HistoryLength: &two})
	if h := got.Tasks[0].History; len(h) != 2 || h[1].Parts[0].Text() != "c" || got.Tasks[0].Artifacts != nil {
		t.Errorf("trimmed task: %d messages, artifacts %v", len(h), got.Tasks[0].Artifacts)
	}
	got = list(bob, &a2a.ListTasksRequest{ContextID: "c1", Status: a2a.TaskStateSubmitted, IncludeArtifacts: true})
	if len(got.Tasks[0].Artifacts) != 1 || len(got.Tasks[0].History) != 3 {
		t.Errorf("with artifacts: %d artifacts, %d messages", len(got.Tasks[0].Artifacts), len(got.Tasks[0].History))
	}

	// Without a known caller, List refuses.
	if _, err := it.List(context.Background(), &a2a.ListTasksRequest{}); !errors.Is(err, a2a.ErrUnauthenticated) {
		t.Errorf("List without a caller: %v, want ErrUnauthenticated", err)
	}
}
