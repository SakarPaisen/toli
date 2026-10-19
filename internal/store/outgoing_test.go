package store

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/taskclient/taskstore"
)

func TestOutgoingTasks(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	addAgent(t, s, "backend")
	addAgent(t, s, "frontend")
	pid, err := s.AddPeer(ctx, Peer{Agent: "backend", Name: "bob", URL: "x"}, "")
	if err != nil {
		t.Fatal(err)
	}
	sb, _ := s.CreateSession(ctx, "backend", "b")
	sf, _ := s.CreateSession(ctx, "frontend", "f")
	o := s.OutgoingTasks()
	now := time.Now()

	create := func(owner, contextID string, state a2a.TaskState) a2a.TaskID {
		t.Helper()
		id := a2a.NewTaskID()
		task := &taskstore.Task{
			A2A:      &a2a.Task{ID: id, ContextID: contextID, Status: a2a.TaskStatus{State: state}},
			PeerID:   pid,
			Owner:    owner,
			SavedAt:  now,
			Deadline: now.Add(time.Hour),

			ClientContext: contextID != "",
		}
		if err := o.Create(ctx, task); err != nil {
			t.Fatalf("Create: %v", err)
		}
		return id
	}
	working := create("backend", sb.ID, a2a.TaskStateWorking)
	create("backend", "", a2a.TaskStateCompleted)
	create("frontend", sf.ID, a2a.TaskStateSubmitted)

	// Create / Get round trip, including local fields and millisecond times.
	got, err := o.Get(ctx, working)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Version != 1 || got.Owner != "backend" || got.PeerID != pid || got.Direct ||
		got.A2A.ContextID != sb.ID || got.Deadline.UnixMilli() != now.Add(time.Hour).UnixMilli() {
		t.Fatalf("Get = %+v", got)
	}
	if _, err := o.Get(ctx, "nope"); !errors.Is(err, taskstore.ErrNotFound) {
		t.Fatalf("Get unknown: %v, want ErrNotFound", err)
	}
	if err := o.Create(ctx, got); !errors.Is(err, taskstore.ErrExists) {
		t.Fatalf("Create duplicate: %v, want ErrExists", err)
	}

	// UpdateA2A: applies at the right version, conflicts at a stale one.
	done := &a2a.Task{ID: working, ContextID: sb.ID, Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}
	updated, err := o.UpdateA2A(ctx, working, done, 1)
	if err != nil || updated.Version != 2 || updated.A2A.Status.State != a2a.TaskStateCompleted || updated.Owner != "backend" {
		t.Fatalf("UpdateA2A: %v, %+v", err, updated)
	}
	if _, err := o.UpdateA2A(ctx, working, done, 1); !errors.Is(err, taskstore.ErrConflict) {
		t.Fatalf("stale UpdateA2A: %v, want ErrConflict", err)
	}
	if _, err := o.UpdateA2A(ctx, "nope", done, 1); !errors.Is(err, taskstore.ErrNotFound) {
		t.Fatalf("UpdateA2A unknown: %v, want ErrNotFound", err)
	}

	// List filters, alone and combined.
	count := func(f taskstore.Filter) int {
		t.Helper()
		tasks, err := o.List(ctx, f)
		if err != nil {
			t.Fatalf("List(%+v): %v", f, err)
		}
		return len(tasks)
	}
	active := []a2a.TaskState{a2a.TaskStateSubmitted, a2a.TaskStateWorking, a2a.TaskStateInputRequired}
	for _, c := range []struct {
		f    taskstore.Filter
		want int
	}{
		{taskstore.Filter{}, 3},
		{taskstore.Filter{Owner: "backend"}, 2},
		{taskstore.Filter{ContextID: sb.ID}, 1},
		{taskstore.Filter{States: active}, 1}, // only frontend's submitted one; "working" was completed above
		{taskstore.Filter{Owner: "backend", States: active}, 0},
		{taskstore.Filter{Owner: "frontend", ContextID: sf.ID, States: active}, 1},
	} {
		if n := count(c.f); n != c.want {
			t.Errorf("List(%+v) = %d tasks, want %d", c.f, n, c.want)
		}
		// The SQL filter must agree with taskstore's reference definition (Filter.Matches).
		all, _ := o.List(ctx, taskstore.Filter{})
		listed, _ := o.List(ctx, c.f)
		want := map[a2a.TaskID]bool{}
		for _, task := range all {
			if c.f.Matches(task) {
				want[task.A2A.ID] = true
			}
		}
		got := map[a2a.TaskID]bool{}
		for _, task := range listed {
			got[task.A2A.ID] = true
		}
		if !maps.Equal(got, want) {
			t.Errorf("List(%+v) disagrees with Filter.Matches: got %v, want %v", c.f, got, want)
		}
	}
}

func TestDeleteReturnsActive(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	addAgent(t, s, "backend")
	addAgent(t, s, "frontend")
	bob, _ := s.AddPeer(ctx, Peer{Agent: "backend", Name: "bob", URL: "x"}, "")
	carol, _ := s.AddPeer(ctx, Peer{Agent: "backend", Name: "carol", URL: "y"}, "")
	sess, _ := s.CreateSession(ctx, "backend", "users api")
	o := s.OutgoingTasks()
	now := time.Now()

	create := func(peer int64, session string, state a2a.TaskState) ActiveTask {
		t.Helper()
		id := a2a.NewTaskID()
		task := &taskstore.Task{A2A: &a2a.Task{ID: id, ContextID: session, Status: a2a.TaskStatus{State: state}},
			PeerID: peer, Owner: "backend", SavedAt: now, Deadline: now.Add(time.Hour), ClientContext: session != ""}
		if err := o.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
		return ActiveTask{ID: id, PeerID: peer}
	}
	ids := func(ts []ActiveTask) []a2a.TaskID {
		var out []a2a.TaskID
		for _, at := range ts {
			out = append(out, at.ID)
		}
		slices.Sort(out)
		return out
	}

	// A session spanning two peers, plus a standalone task.
	sessBob := create(bob, sess.ID, a2a.TaskStateWorking)
	sessCarol := create(carol, sess.ID, a2a.TaskStateInputRequired)
	create(bob, sess.ID, a2a.TaskStateCompleted)
	standalone := create(bob, "", a2a.TaskStateWorking)

	if err := o.Create(ctx, &taskstore.Task{A2A: &a2a.Task{ID: a2a.NewTaskID(), ContextID: "nope"},
		PeerID: bob, Owner: "backend", SavedAt: now, Deadline: now, ClientContext: true}); err == nil {
		t.Fatal("Create in unknown session: nil error")
	}

	// DeleteSession: only the owner can, it returns the active tasks with
	// their peers, and the session's tasks go with it.
	if _, err := s.DeleteSession(ctx, "frontend", sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteSession by another agent: %v, want ErrNotFound", err)
	}
	active, err := s.DeleteSession(ctx, "backend", sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(active), ids([]ActiveTask{sessBob, sessCarol}); !slices.Equal(got, want) {
		t.Fatalf("DeleteSession active = %v, want %v", got, want)
	}
	for _, at := range active {
		if at != sessBob && at != sessCarol {
			t.Fatalf("wrong peer in %+v", at)
		}
	}
	if left, _ := o.List(ctx, taskstore.Filter{Owner: "backend"}); len(left) != 1 || left[0].A2A.ID != standalone.ID {
		t.Fatalf("after DeleteSession, tasks left = %d, want only the standalone one", len(left))
	}
	if _, err := s.DeleteSession(ctx, "backend", sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteSession: %v, want ErrNotFound", err)
	}

	// RemovePeer returns its remaining active task.
	active, err = s.RemovePeer(ctx, bob)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0] != standalone {
		t.Fatalf("RemovePeer active = %+v, want [%+v]", active, standalone)
	}

	// RemoveAgent returns active tasks across all its peers and sessions.
	sess2, _ := s.CreateSession(ctx, "backend", "second")
	viaSession := create(carol, sess2.ID, a2a.TaskStateWorking)
	direct := create(carol, "", a2a.TaskStateSubmitted)
	create(carol, "", a2a.TaskStateFailed)
	active, err = s.RemoveAgent(ctx, "backend")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(active), ids([]ActiveTask{viaSession, direct}); !slices.Equal(got, want) {
		t.Fatalf("RemoveAgent active = %v, want %v", got, want)
	}
	if left, _ := o.List(ctx, taskstore.Filter{}); len(left) != 0 {
		t.Fatalf("%d tasks left after removing their agent, want 0", len(left))
	}
}

// A standalone task's contextId is the peer's own (A2A requires one), not a
// session of ours: it's stored, but isn't a session and doesn't filter as one.
func TestPeerGeneratedContext(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	addAgent(t, s, "backend")
	pid, _ := s.AddPeer(ctx, Peer{Agent: "backend", Name: "bob", URL: "x"}, "")
	o := s.OutgoingTasks()
	now := time.Now()

	id := a2a.NewTaskID()
	task := &taskstore.Task{A2A: &a2a.Task{ID: id, ContextID: "made-up-by-peer", Status: a2a.TaskStatus{State: a2a.TaskStateWorking}},
		PeerID: pid, Owner: "backend", SavedAt: now, Deadline: now.Add(time.Hour)}
	if err := o.Create(ctx, task); err != nil {
		t.Fatalf("Create standalone task with a peer-generated contextId: %v", err)
	}
	got, err := o.Get(ctx, id)
	if err != nil || got.ClientContext || got.A2A.ContextID != "made-up-by-peer" {
		t.Fatalf("Get = %v, %+v", err, got)
	}
	f := taskstore.Filter{ContextID: "made-up-by-peer"}
	if listed, _ := o.List(ctx, f); len(listed) != 0 || f.Matches(got) {
		t.Fatalf("a peer-generated contextId matched as a session")
	}
}
