package taskclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	"github.com/arjunvlama/toli/internal/a2atest"
	"github.com/arjunvlama/toli/internal/taskclient/taskstore"
)

// setup starts a fake peer (peer ID 1) and a Client talking to it.
func setup(t *testing.T, opts Options) (*Client, *a2atest.FakePeer) {
	t.Helper()
	peer := a2atest.NewFakePeer(t)
	resolve := func(ctx context.Context, peerID int64) (*a2aclient.Client, error) {
		return a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{
			a2a.NewAgentInterface(peer.URL+"/", a2a.TransportProtocolJSONRPC),
		})
	}
	return New(newMemStore(), resolve, opts), peer
}

func userMsg(s string) *a2a.Message {
	return a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(s))
}

// runPoller starts Run in the background for the rest of the test.
func runPoller(t *testing.T, m *Client) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

// waitForState waits (via WaitForChange) until the task reaches state.
func waitForState(t *testing.T, m *Client, id a2a.TaskID, state a2a.TaskState) *taskstore.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var after int64
	for time.Now().Before(deadline) {
		task, err := m.WaitForChange(context.Background(), id, after, time.Second)
		if err != nil {
			t.Fatalf("WaitForChange: %v", err)
		}
		if task.A2A.Status.State == state {
			return task
		}
		after = task.Version
	}
	t.Fatalf("task %s never reached %s", id, state)
	return nil
}

func TestSendThenPollToCompletion(t *testing.T) {
	m, agent := setup(t, Options{PollEvery: 20 * time.Millisecond})
	ctx := context.Background()

	task, err := m.Send(ctx, 1, "me", userMsg("add a /users endpoint"), SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if s := task.A2A.Status.State; s != a2a.TaskStateSubmitted && s != a2a.TaskStateWorking {
		t.Fatalf("Send returned state %s, want submitted or working", s)
	}
	if task.Version != 1 || task.Owner != "me" || task.PeerID != 1 || task.Direct {
		t.Fatalf("unexpected local fields: %+v", task)
	}

	runPoller(t, m)
	agent.Next(a2a.TaskStateCompleted, "done: GET /users")

	got := waitForState(t, m, task.A2A.ID, a2a.TaskStateCompleted)
	if a2atest.Text(got.A2A.Status.Message) != "done: GET /users" {
		t.Fatalf("final status message = %q", a2atest.Text(got.A2A.Status.Message))
	}
	if got.Owner != "me" || got.PeerID != 1 {
		t.Fatalf("local fields changed by updates: %+v", got)
	}

	active, _ := m.List(ctx, taskstore.Filter{States: activeStates})
	if len(active) != 0 {
		t.Fatalf("completed task still listed as active")
	}
}

func TestReplyToInputRequired(t *testing.T) {
	m, agent := setup(t, Options{PollEvery: 20 * time.Millisecond})
	ctx := context.Background()
	runPoller(t, m)

	task, err := m.Send(ctx, 1, "me", userMsg("add pagination"), SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	agent.Next(a2a.TaskStateInputRequired, "cursor or offset?")
	waitForState(t, m, task.A2A.ID, a2a.TaskStateInputRequired)

	replied, answer, err := m.Reply(ctx, task.A2A.ID, userMsg("cursor"))
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if answer != nil {
		t.Fatalf("unexpected direct answer to a reply")
	}
	if replied.A2A.ID != task.A2A.ID {
		t.Fatalf("Reply started a new task %s instead of continuing %s", replied.A2A.ID, task.A2A.ID)
	}

	agent.Next(a2a.TaskStateCompleted, "used cursor pagination")
	got := waitForState(t, m, task.A2A.ID, a2a.TaskStateCompleted)

	var sawReply bool
	for _, msg := range got.A2A.History {
		if a2atest.Text(msg) == "cursor" {
			sawReply = true
		}
	}
	if !sawReply {
		t.Fatalf("our reply isn't in the task history the peer reported")
	}
}

func TestCancel(t *testing.T) {
	m, _ := setup(t, Options{})
	ctx := context.Background()

	task, err := m.Send(ctx, 1, "me", userMsg("long job"), SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	canceled, err := m.Cancel(ctx, task.A2A.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if canceled.A2A.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("state after Cancel = %s", canceled.A2A.Status.State)
	}
	if _, err := m.Cancel(ctx, task.A2A.ID); !errors.Is(err, ErrFinal) {
		t.Fatalf("second Cancel: got %v, want ErrFinal", err)
	}
	if _, _, err := m.Reply(ctx, task.A2A.ID, userMsg("hello?")); !errors.Is(err, ErrFinal) {
		t.Fatalf("Reply on canceled task: got %v, want ErrFinal", err)
	}
}

func TestDirectAnswer(t *testing.T) {
	m, _ := setup(t, Options{})
	ctx := context.Background()

	task, err := m.Send(ctx, 1, "me", userMsg("ping"), SendOptions{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !task.Direct || task.A2A.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("direct answer not recorded as a completed Direct task: %+v", task)
	}
	if a2atest.Text(task.A2A.Status.Message) != "pong" || len(task.A2A.History) != 2 {
		t.Fatalf("direct record = %q, history %d", a2atest.Text(task.A2A.Status.Message), len(task.A2A.History))
	}
	stored, err := m.Get(ctx, task.A2A.ID)
	if err != nil || !stored.Direct {
		t.Fatalf("direct record not stored: %v", err)
	}
}

func TestDeadlineCancelsOnPeer(t *testing.T) {
	m, _ := setup(t, Options{PollEvery: 20 * time.Millisecond})
	ctx := context.Background()

	task, err := m.Send(ctx, 1, "me", userMsg("slow job"), SendOptions{TimeLimit: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Past its deadline, the poller cancels it on the peer.
	runPoller(t, m)
	time.Sleep(300 * time.Millisecond)

	got, _ := m.Get(ctx, task.A2A.ID)
	if got.A2A.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("after deadline: state %s, want canceled", got.A2A.Status.State)
	}
}

func msg(id string, role a2a.MessageRole) *a2a.Message {
	m := a2a.NewMessage(role, a2a.NewTextPart(id))
	m.ID = id
	return m
}

// storeTask puts a task in the client's store and returns its id.
func storeTask(t *testing.T, m *Client, status a2a.TaskStatus, history ...*a2a.Message) a2a.TaskID {
	t.Helper()
	id := a2a.NewTaskID()
	task := &a2a.Task{ID: id, Status: status, History: history}
	if err := m.store.Create(context.Background(), &taskstore.Task{A2A: task, Deadline: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRecordRejectsStalePoll(t *testing.T) {
	m, _ := setup(t, Options{})
	ctx := context.Background()
	req, question, answer := msg("req", a2a.MessageRoleUser), msg("q", a2a.MessageRoleAgent), msg("a", a2a.MessageRoleUser)

	// Stored: the peer is asking a question (in the status message, not yet in history).
	id := storeTask(t, m, a2a.TaskStatus{State: a2a.TaskStateInputRequired, Message: question}, req)

	// A poll answered before the question: lacks it, so it's older. Rejected.
	stale := &a2a.Task{ID: id, Status: a2a.TaskStatus{State: a2a.TaskStateWorking}, History: []*a2a.Message{req}}
	got, err := m.record(ctx, stale, rejectOlderThan(stale))
	if err != nil || got.A2A.Status.State != a2a.TaskStateInputRequired || got.Version != 1 {
		t.Fatalf("stale poll applied: %v, state %s, version %d", err, got.A2A.Status.State, got.Version)
	}

	// A poll after our answer: has the question (moved into history). Saved.
	newer := &a2a.Task{ID: id, Status: a2a.TaskStatus{State: a2a.TaskStateWorking}, History: []*a2a.Message{req, question, answer}}
	got, err = m.record(ctx, newer, rejectOlderThan(newer))
	if err != nil || got.A2A.Status.State != a2a.TaskStateWorking || got.Version != 2 {
		t.Fatalf("newer poll: %v, state %s, version %d", err, got.A2A.Status.State, got.Version)
	}

	// Then a stale poll from before our answer: lacks it now. Rejected.
	got, _ = m.record(ctx, stale, rejectOlderThan(stale))
	if got.Version != 2 {
		t.Fatalf("poll from before our answer applied: version %d", got.Version)
	}

	// A status-only change (no new message) still has the newest message. Saved.
	done := &a2a.Task{ID: id, Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}, History: []*a2a.Message{req, question, answer}}
	got, _ = m.record(ctx, done, rejectOlderThan(done))
	if got.A2A.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("status-only change rejected: state %s", got.A2A.Status.State)
	}
}

func TestRecordReplyAlreadyStored(t *testing.T) {
	m, _ := setup(t, Options{})
	ctx := context.Background()
	req, question, answer := msg("req", a2a.MessageRoleUser), msg("q", a2a.MessageRoleAgent), msg("a", a2a.MessageRoleUser)

	// A poll already brought in a copy with our answer and more progress ...
	id := storeTask(t, m, a2a.TaskStatus{State: a2a.TaskStateWorking, Message: msg("progress", a2a.MessageRoleAgent)}, req, question, answer)
	// ... so the answer's own (older) response is rejected.
	resp := &a2a.Task{ID: id, Status: a2a.TaskStatus{State: a2a.TaskStateWorking}, History: []*a2a.Message{req, question, answer}}
	got, _ := m.record(ctx, resp, rejectHasMessage("a"))
	if got.A2A.Status.Message == nil || got.A2A.Status.Message.ID != "progress" {
		t.Fatalf("reply response overwrote newer progress: %+v", got.A2A.Status)
	}

	// Without the answer stored (a stale poll landed first), the response is saved.
	id = storeTask(t, m, a2a.TaskStatus{State: a2a.TaskStateInputRequired, Message: question}, req)
	resp = &a2a.Task{ID: id, Status: a2a.TaskStatus{State: a2a.TaskStateWorking}, History: []*a2a.Message{req, question, answer}}
	got, _ = m.record(ctx, resp, rejectHasMessage("a"))
	if got.A2A.Status.State != a2a.TaskStateWorking {
		t.Fatalf("reply response rejected: state %s", got.A2A.Status.State)
	}
}

func TestRecordKeepsFinalState(t *testing.T) {
	m, _ := setup(t, Options{})
	ctx := context.Background()
	id := storeTask(t, m, a2a.TaskStatus{State: a2a.TaskStateCanceled})

	working := &a2a.Task{ID: id, Status: a2a.TaskStatus{State: a2a.TaskStateWorking}}
	for name, reject := range map[string]func(*a2a.Task) bool{
		"poll": rejectOlderThan(working), "reply": rejectHasMessage("x"), "cancel": rejectNone,
	} {
		got, err := m.record(ctx, working, reject)
		if err != nil || got.A2A.Status.State != a2a.TaskStateCanceled {
			t.Fatalf("%s overwrote a final state: %v, %s", name, err, got.A2A.Status.State)
		}
	}
}

// racingStore lands another save between record's read and its first write,
// as a concurrent poll or reply would.
type racingStore struct {
	*memStore
	other *a2a.Task
	done  bool
}

func (s *racingStore) UpdateA2A(ctx context.Context, id a2a.TaskID, reported *a2a.Task, prevVersion int64) (*taskstore.Task, error) {
	if !s.done {
		s.done = true
		if _, err := s.memStore.UpdateA2A(ctx, id, s.other, prevVersion); err != nil {
			return nil, err
		}
	}
	return s.memStore.UpdateA2A(ctx, id, reported, prevVersion)
}

func TestRecordRedecidesAfterConflict(t *testing.T) {
	m, _ := setup(t, Options{})
	ctx := context.Background()
	req, question := msg("req", a2a.MessageRoleUser), msg("q", a2a.MessageRoleAgent)
	id := storeTask(t, m, a2a.TaskStatus{State: a2a.TaskStateWorking}, req)

	// While a stale poll is deciding, the peer's question gets saved.
	rs := &racingStore{memStore: m.store.(*memStore),
		other: &a2a.Task{ID: id, Status: a2a.TaskStatus{State: a2a.TaskStateInputRequired, Message: question}, History: []*a2a.Message{req}}}
	m.store = rs
	stale := &a2a.Task{ID: id, Status: a2a.TaskStatus{State: a2a.TaskStateWorking}, History: []*a2a.Message{req}}

	// Its write conflicts; it re-reads, sees the question it lacks, and backs off.
	got, err := m.record(ctx, stale, rejectOlderThan(stale))
	if err != nil || got.A2A.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("after conflict: %v, state %s; want the question kept", err, got.A2A.Status.State)
	}
}

func TestWaitForChange(t *testing.T) {
	m, _ := setup(t, Options{})
	ctx := context.Background()
	s := m.store

	id := a2a.NewTaskID()
	if err := s.Create(ctx, &taskstore.Task{A2A: &a2a.Task{ID: id, Status: a2a.TaskStatus{State: a2a.TaskStateWorking}}}); err != nil {
		t.Fatal(err)
	}

	// Already newer than what the caller saw: returns at once.
	start := time.Now()
	if got, err := m.WaitForChange(ctx, id, 0, time.Minute); err != nil || got.Version != 1 {
		t.Fatalf("immediate: %v, %+v", err, got)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatalf("immediate case waited %v", time.Since(start))
	}

	// Nothing new: returns the current task after maxWait, without error.
	got, err := m.WaitForChange(ctx, id, 1, 50*time.Millisecond)
	if err != nil || got.Version != 1 {
		t.Fatalf("timeout: %v, %+v", err, got)
	}

	// A change arrives while waiting: returns it.
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.UpdateA2A(ctx, id, &a2a.Task{ID: id, Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}, 1)
	}()
	got, err = m.WaitForChange(ctx, id, 1, 5*time.Second)
	if err != nil || got.Version != 2 || got.A2A.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("change: %v, %+v", err, got)
	}
}
