package backend_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/a2atest"
	"github.com/arjunvlama/toli/internal/backend"
	"github.com/arjunvlama/toli/internal/store"
	"github.com/arjunvlama/toli/internal/taskclient/taskstore"
)

// setup opens a fresh store and runs a backend on it (fast polling) for the
// rest of the test.
func setup(t *testing.T) *backend.Backend {
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
	return b
}

func addAgent(t *testing.T, b *backend.Backend, name string) {
	t.Helper()
	if _, err := b.AddAgent(context.Background(), name, "", nil); err != nil {
		t.Fatal(err)
	}
}

// addPeer starts a fake peer and puts it on agent's allowlist as name.
func addPeer(t *testing.T, b *backend.Backend, agent, name string) (int64, *a2atest.FakePeer) {
	t.Helper()
	fp := a2atest.NewFakePeer(t)
	p, _, err := b.AddPeer(context.Background(), agent, name, fp.URL, "token-for-"+name)
	if err != nil {
		t.Fatal(err)
	}
	return p.ID, fp
}

func delegate(t *testing.T, b *backend.Backend, agent, peer, text, session string) *backend.Task {
	t.Helper()
	task, err := b.Delegate(context.Background(), agent, peer, text, session, 0)
	if err != nil {
		t.Fatalf("Delegate(%s -> %s): %v", agent, peer, err)
	}
	return task
}

// waitState waits (via GetTask's long poll) until the task reaches state.
func waitState(t *testing.T, b *backend.Backend, agent string, id a2a.TaskID, state a2a.TaskState) *backend.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var after int64
	for time.Now().Before(deadline) {
		task, err := b.GetTask(context.Background(), agent, id, after, time.Second)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if task.A2A.Status.State == state {
			return task
		}
		after = task.Version
	}
	t.Fatalf("task %s never reached %s", id, state)
	return nil
}

// waitCanceled waits until the peer has been asked to cancel exactly ids
// (in any order). The cancels are sent in the background, so they lag.
func waitCanceled(t *testing.T, fp *a2atest.FakePeer, ids ...a2a.TaskID) {
	t.Helper()
	want := slices.Sorted(slices.Values(ids))
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := slices.Sorted(slices.Values(fp.Canceled()))
		if slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("peer got cancels for %v, want %v", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func taskIDs(tasks []*backend.Task) []a2a.TaskID {
	var ids []a2a.TaskID
	for _, t := range tasks {
		ids = append(ids, t.A2A.ID)
	}
	return slices.Sorted(slices.Values(ids))
}

func TestDelegateToCompletion(t *testing.T) {
	b := setup(t)
	addAgent(t, b, "backend")
	_, bob := addPeer(t, b, "backend", "bob")

	task := delegate(t, b, "backend", "bob", "add /users", "")
	if task.A2A.Status.State.Terminal() {
		t.Fatalf("new task already %s", task.A2A.Status.State)
	}
	bob.Next(a2a.TaskStateCompleted, "done: GET /users")

	if task.Peer != "bob" {
		t.Fatalf("Delegate returned peer %q, want bob", task.Peer)
	}
	got := waitState(t, b, "backend", task.A2A.ID, a2a.TaskStateCompleted)
	if got.Peer != "bob" {
		t.Fatalf("GetTask returned peer %q, want bob", got.Peer)
	}
	if a2atest.Text(got.A2A.Status.Message) != "done: GET /users" {
		t.Fatalf("result = %q", a2atest.Text(got.A2A.Status.Message))
	}
	if msgs := bob.Received(); len(msgs) != 1 || msgs[0].ContextID != "" || a2atest.Text(msgs[0]) != "add /users" {
		t.Fatalf("peer received %+v, want one standalone message", msgs)
	}
}

func TestDelegateInSession(t *testing.T) {
	ctx := context.Background()
	b := setup(t)
	addAgent(t, b, "backend")
	_, bob := addPeer(t, b, "backend", "bob")
	sess, err := b.StartSession(ctx, "backend", "users api")
	if err != nil {
		t.Fatal(err)
	}

	inSession := delegate(t, b, "backend", "bob", "add /users", sess.ID)
	standalone := delegate(t, b, "backend", "bob", "fix the build", "")

	if msgs := bob.Received(); len(msgs) != 2 || msgs[0].ContextID != sess.ID {
		t.Fatalf("peer received %+v, want the session id as contextId", msgs)
	}
	got, _ := b.ListTasks(ctx, "backend", sess.ID, nil)
	if !slices.Equal(taskIDs(got), []a2a.TaskID{inSession.A2A.ID}) || got[0].Peer != "bob" {
		t.Fatalf("session tasks = %v (peer %q)", taskIDs(got), got[0].Peer)
	}
	all, _ := b.ListTasks(ctx, "backend", "", nil)
	if want := taskIDs([]*backend.Task{inSession, standalone}); !slices.Equal(taskIDs(all), want) {
		t.Fatalf("all tasks = %v, want %v", taskIDs(all), want)
	}
}

func TestDelegateNotFound(t *testing.T) {
	ctx := context.Background()
	b := setup(t)
	addAgent(t, b, "backend")
	addAgent(t, b, "frontend")
	addPeer(t, b, "backend", "bob")
	addPeer(t, b, "frontend", "carol")
	theirs, _ := b.StartSession(ctx, "frontend", "theirs")
	deleted, _ := b.StartSession(ctx, "backend", "gone")
	if err := b.DeleteSession(ctx, "backend", deleted.ID); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ name, peer, session string }{
		{"unknown peer", "nobody", ""},
		{"another agent's peer", "carol", ""},
		{"another agent's session", "bob", theirs.ID},
		{"deleted session", "bob", deleted.ID},
		{"unknown session", "bob", "nope"},
	} {
		if _, err := b.Delegate(ctx, "backend", c.peer, "hi", c.session, 0); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", c.name, err)
		}
	}
}

func TestDirectAnswer(t *testing.T) {
	b := setup(t)
	addAgent(t, b, "backend")
	addPeer(t, b, "backend", "bob")

	task := delegate(t, b, "backend", "bob", "ping", "")
	if !task.Direct || task.A2A.Status.State != a2a.TaskStateCompleted || a2atest.Text(task.A2A.Status.Message) != "pong" {
		t.Fatalf("direct answer stored as %+v", task)
	}
	got, err := b.GetTask(context.Background(), "backend", task.A2A.ID, -1, 0)
	if err != nil || !got.Direct {
		t.Fatalf("GetTask: %v, %+v", err, got)
	}
}

func TestOtherAgentsTasksAreHidden(t *testing.T) {
	ctx := context.Background()
	b := setup(t)
	addAgent(t, b, "backend")
	addAgent(t, b, "frontend")
	addPeer(t, b, "backend", "bob")
	task := delegate(t, b, "backend", "bob", "add /users", "")
	id := task.A2A.ID

	if _, err := b.GetTask(ctx, "frontend", id, -1, 0); !errors.Is(err, taskstore.ErrNotFound) {
		t.Errorf("GetTask: %v, want ErrNotFound", err)
	}
	if _, _, err := b.Reply(ctx, "frontend", id, "hi"); !errors.Is(err, taskstore.ErrNotFound) {
		t.Errorf("Reply: %v, want ErrNotFound", err)
	}
	if _, err := b.CancelTask(ctx, "frontend", id); !errors.Is(err, taskstore.ErrNotFound) {
		t.Errorf("CancelTask: %v, want ErrNotFound", err)
	}
	if tasks, _ := b.ListTasks(ctx, "frontend", "", nil); len(tasks) != 0 {
		t.Errorf("ListTasks shows %d of another agent's tasks", len(tasks))
	}
}

func TestGetTaskWaitsForChange(t *testing.T) {
	ctx := context.Background()
	b := setup(t)
	addAgent(t, b, "backend")
	_, bob := addPeer(t, b, "backend", "bob")
	task := delegate(t, b, "backend", "bob", "add /users", "")

	// after < 0 returns at once, whatever the version.
	now, err := b.GetTask(ctx, "backend", task.A2A.ID, -1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// With after = the current version, it waits for the poller to record the
	// peer's next report.
	go func() {
		time.Sleep(200 * time.Millisecond)
		bob.Next(a2a.TaskStateCompleted, "done")
	}()
	start := time.Now()
	for {
		got, err := b.GetTask(ctx, "backend", task.A2A.ID, now.Version, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if got.Version <= now.Version {
			t.Fatalf("returned version %d, not newer than %d", got.Version, now.Version)
		}
		if got.A2A.Status.State == a2a.TaskStateCompleted {
			break
		}
		now = got // an intermediate report (e.g. working); keep waiting
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatalf("returned after %v, before the peer changed anything", time.Since(start))
	}
}

func TestReplyToInputRequired(t *testing.T) {
	b := setup(t)
	addAgent(t, b, "backend")
	_, bob := addPeer(t, b, "backend", "bob")
	task := delegate(t, b, "backend", "bob", "add /users", "")

	bob.Next(a2a.TaskStateInputRequired, "paginated?")
	waitState(t, b, "backend", task.A2A.ID, a2a.TaskStateInputRequired)

	if _, _, err := b.Reply(context.Background(), "backend", task.A2A.ID, "yes, cursor"); err != nil {
		t.Fatal(err)
	}
	bob.Next(a2a.TaskStateCompleted, "used cursor pagination")
	waitState(t, b, "backend", task.A2A.ID, a2a.TaskStateCompleted)

	msgs := bob.Received()
	if len(msgs) != 2 || a2atest.Text(msgs[1]) != "yes, cursor" || msgs[1].TaskID != task.A2A.ID {
		t.Fatalf("peer received %+v, want the reply on the same task", msgs)
	}
}

func TestCancelTask(t *testing.T) {
	ctx := context.Background()
	b := setup(t)
	addAgent(t, b, "backend")
	_, bob := addPeer(t, b, "backend", "bob")
	task := delegate(t, b, "backend", "bob", "add /users", "")

	got, err := b.CancelTask(ctx, "backend", task.A2A.ID)
	if err != nil || got.A2A.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("CancelTask: %v, %+v", err, got)
	}
	waitCanceled(t, bob, task.A2A.ID)

	// Canceling a finished task reports the state it ended in.
	again, err := b.CancelTask(ctx, "backend", task.A2A.ID)
	if err != nil || again.A2A.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("CancelTask on a finished task: %v, %+v", err, again)
	}
}

func TestDeleteSessionCancelsItsTasks(t *testing.T) {
	ctx := context.Background()
	b := setup(t)
	addAgent(t, b, "backend")
	_, bob := addPeer(t, b, "backend", "bob")
	_, carol := addPeer(t, b, "backend", "carol")
	sess, _ := b.StartSession(ctx, "backend", "users api")

	// A finished task in the session isn't canceled; active ones on both peers are.
	done := delegate(t, b, "backend", "bob", "first", sess.ID)
	bob.Next(a2a.TaskStateCompleted, "ok")
	waitState(t, b, "backend", done.A2A.ID, a2a.TaskStateCompleted)
	onBob := delegate(t, b, "backend", "bob", "second", sess.ID)
	onCarol := delegate(t, b, "backend", "carol", "third", sess.ID)
	standalone := delegate(t, b, "backend", "bob", "unrelated", "")

	if err := b.DeleteSession(ctx, "backend", sess.ID); err != nil {
		t.Fatal(err)
	}
	waitCanceled(t, bob, onBob.A2A.ID)
	waitCanceled(t, carol, onCarol.A2A.ID)

	left, _ := b.ListTasks(ctx, "backend", "", nil)
	if !slices.Equal(taskIDs(left), []a2a.TaskID{standalone.A2A.ID}) {
		t.Fatalf("tasks left = %v, want only the standalone one", taskIDs(left))
	}
	if err := b.DeleteSession(ctx, "backend", sess.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second DeleteSession: %v, want ErrNotFound", err)
	}
}

func TestRemovePeerCancelsItsTasks(t *testing.T) {
	ctx := context.Background()
	b := setup(t)
	addAgent(t, b, "backend")
	bobID, bob := addPeer(t, b, "backend", "bob")
	_, carol := addPeer(t, b, "backend", "carol")

	first := delegate(t, b, "backend", "bob", "one", "")
	second := delegate(t, b, "backend", "bob", "two", "")
	other := delegate(t, b, "backend", "carol", "three", "")

	if err := b.RemovePeer(ctx, bobID); err != nil {
		t.Fatal(err)
	}
	waitCanceled(t, bob, first.A2A.ID, second.A2A.ID)

	left, _ := b.ListTasks(ctx, "backend", "", nil)
	if !slices.Equal(taskIDs(left), []a2a.TaskID{other.A2A.ID}) {
		t.Fatalf("tasks left = %v, want only carol's", taskIDs(left))
	}
	if len(carol.Canceled()) != 0 {
		t.Fatalf("carol got cancels: %v", carol.Canceled())
	}
	if _, err := b.Delegate(ctx, "backend", "bob", "hi", "", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delegate to removed peer: %v, want ErrNotFound", err)
	}
}

func TestRemoveAgentCancelsItsTasks(t *testing.T) {
	ctx := context.Background()
	b := setup(t)
	addAgent(t, b, "backend")
	addAgent(t, b, "frontend")
	_, bob := addPeer(t, b, "backend", "bob")
	_, carol := addPeer(t, b, "backend", "carol")
	_, dave := addPeer(t, b, "frontend", "dave")
	sess, _ := b.StartSession(ctx, "backend", "users api")

	onBob := delegate(t, b, "backend", "bob", "one", sess.ID)
	onCarol := delegate(t, b, "backend", "carol", "two", "")
	theirs := delegate(t, b, "frontend", "dave", "three", "")

	if err := b.RemoveAgent(ctx, "backend"); err != nil {
		t.Fatal(err)
	}
	waitCanceled(t, bob, onBob.A2A.ID)
	waitCanceled(t, carol, onCarol.A2A.ID)

	if len(dave.Canceled()) != 0 {
		t.Fatalf("another agent's peer got cancels: %v", dave.Canceled())
	}
	if left, _ := b.ListTasks(ctx, "frontend", "", nil); !slices.Equal(taskIDs(left), []a2a.TaskID{theirs.A2A.ID}) {
		t.Fatalf("frontend's tasks = %v, want its own task untouched", taskIDs(left))
	}
	if err := b.RemoveAgent(ctx, "backend"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second RemoveAgent: %v, want ErrNotFound", err)
	}
}

// A peer's new token works at once and its old one doesn't; names are
// checked when peers are added.
func TestRotatePeerToken(t *testing.T) {
	b := setup(t)
	ctx := context.Background()
	addAgent(t, b, "frontend")
	fp := a2atest.NewFakePeer(t)
	p, oldTok, err := b.AddPeer(ctx, "frontend", "bob", fp.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	name, newTok, err := b.RotatePeerToken(ctx, p.ID)
	if err != nil || name != "bob" || newTok == oldTok {
		t.Fatalf("RotatePeerToken = %q, %v (same token: %v)", name, err, newTok == oldTok)
	}
	if _, err := b.PeerByToken(ctx, oldTok); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("old token: %v, want ErrNotFound", err)
	}
	if got, err := b.PeerByToken(ctx, newTok); err != nil || got.ID != p.ID {
		t.Errorf("new token: %+v, %v", got, err)
	}
	if _, _, err := b.RotatePeerToken(ctx, p.ID+100); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown peer: %v, want ErrNotFound", err)
	}
	if _, _, err := b.AddPeer(ctx, "frontend", "bob smith", fp.URL, ""); !errors.Is(err, backend.ErrInvalidName) {
		t.Errorf("peer name with a space: %v, want ErrInvalidName", err)
	}
}
