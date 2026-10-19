package a2aserver

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aevent"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"

	"github.com/arjunvlama/toli/internal/backend"
	"github.com/arjunvlama/toli/internal/store"
)

// handler is a2asrv's request handler with message handling replaced.
//
// a2asrv runs an execution per message and keeps it open until the executor
// reaches a final or input-required state; it assumes the agent runs inside
// the executor. Ours doesn't: it's a separate coding agent that picks the
// work up later over MCP. So messages are recorded straight into the task
// store (and our agent's updates are written there too), and nothing stays
// open. GetTask, ListTasks, CancelTask, and the JSON-RPC layer are a2asrv's.
type handler struct {
	a2asrv.RequestHandler
	tasks        *store.IncomingTasks
	extendedCard func(context.Context) (*a2a.AgentCard, error) // the calling peer's view of the agent it calls
	notify       func(agent, text string)                      // tells an agent's running session about work
}

// maxSaveTries bounds retries when another write lands between reading a
// task and saving it.
const maxSaveTries = 10

// SendMessage records a peer's message: a new task, or a message on one of
// its existing tasks.
func (h *handler) SendMessage(ctx context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	msg := req.Message
	if msg == nil {
		return nil, fmt.Errorf("message is required: %w", a2a.ErrInvalidParams)
	}
	var task *a2a.Task
	var err error
	if msg.TaskID == "" {
		task, err = h.newTask(ctx, msg)
	} else {
		task, err = h.addMessage(ctx, msg)
	}
	if err != nil || (req.Config != nil && req.Config.ReturnImmediately) {
		return task, err
	}
	return h.waitForPause(ctx, task)
}

// blockingPollEvery is how often a blocking send re-reads its task.
const blockingPollEvery = 500 * time.Millisecond

// waitForPause holds a blocking send (no ReturnImmediately) until the task is
// final or interrupted (input-required, auth-required), as the spec requires
// and a2asrv does, then returns it. If the caller goes away first, the task
// carries on.
func (h *handler) waitForPause(ctx context.Context, task *a2a.Task) (*a2a.Task, error) {
	ticker := time.NewTicker(blockingPollEvery)
	defer ticker.Stop()
	for {
		switch s := task.Status.State; {
		case s.Terminal(), s == a2a.TaskStateInputRequired, s == a2a.TaskStateAuthRequired:
			return task, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
		stored, err := h.tasks.Get(ctx, task.ID)
		if err != nil {
			return nil, err
		}
		task = stored.Task
	}
}

// newTask stores a new task, submitted, with the message as its request.
func (h *handler) newTask(ctx context.Context, msg *a2a.Message) (*a2a.Task, error) {
	now := time.Now()
	task := &a2a.Task{
		ID:        a2a.NewTaskID(),
		ContextID: msg.ContextID,
		Status:    a2a.TaskStatus{State: a2a.TaskStateSubmitted, Timestamp: &now},
	}
	if task.ContextID == "" {
		task.ContextID = a2a.NewContextID()
	}
	task.History = []*a2a.Message{onTask(msg, task)}
	if _, err := h.tasks.Create(ctx, task); err != nil {
		return nil, fmt.Errorf("store task: %w", err)
	}
	if p, ok := callerFrom(ctx); ok {
		slog.Info("task received", "agent", p.Agent, "peer", p.Name, "task", task.ID)
		h.notify(p.Agent, fmt.Sprintf("toli: new task from %s: %q. Call list_available_tasks.", p.Name, backend.Summary(msg)))
	}
	return task, nil
}

// addMessage records a message on an existing task, after the worker's
// latest message. If the task was waiting for input, the message is the
// answer and the task resumes (working). Otherwise its state is unchanged. A
// message already recorded (a retried send) isn't added twice.
func (h *handler) addMessage(ctx context.Context, msg *a2a.Message) (*a2a.Task, error) {
	for range maxSaveTries {
		stored, err := h.tasks.Get(ctx, msg.TaskID) // only the caller's own tasks
		if err != nil {
			return nil, err
		}
		task := stored.Task
		if task.Status.State.Terminal() {
			return nil, fmt.Errorf("task is %s: %w", task.Status.State, a2a.ErrUnsupportedOperation)
		}
		if msg.ContextID != "" && msg.ContextID != task.ContextID {
			return nil, fmt.Errorf("message context %q doesn't match the task's: %w", msg.ContextID, a2a.ErrInvalidParams)
		}
		if slices.ContainsFunc(task.History, func(m *a2a.Message) bool { return m.ID == msg.ID }) {
			return task, nil
		}

		// The worker's latest message (the status message) only moves into
		// history with its next status, so a message arriving now would land
		// before it. Move it first, so history keeps the order things were
		// said. A message on an input-required task is the answer: the task
		// resumes.
		if state := task.Status.State; task.Status.Message != nil || state == a2a.TaskStateInputRequired {
			if state == a2a.TaskStateInputRequired {
				state = a2a.TaskStateWorking
			}
			task, err = a2aevent.ApplyStatusUpdate(task, a2a.NewStatusUpdateEvent(task, state, nil))
			if err != nil {
				return nil, err
			}
		}
		task.History = append(task.History, onTask(msg, task))

		_, err = h.tasks.Update(ctx, &taskstore.UpdateRequest{Task: task, Event: msg, PrevTask: stored.Task, PrevVersion: stored.Version})
		if errors.Is(err, taskstore.ErrConcurrentModification) {
			continue // someone else wrote it in between: decide again on the latest copy
		}
		if err != nil {
			return nil, err
		}
		if p, ok := callerFrom(ctx); ok {
			h.notify(p.Agent, messageNote(p.Name, stored.Task, msg))
		}
		return task, nil
	}
	return nil, fmt.Errorf("task %s kept changing while recording a message", msg.TaskID)
}

// messageNote tells our agent that its delegator sent msg on task (as it
// was before the message).
func messageNote(peer string, task *a2a.Task, msg *a2a.Message) string {
	switch task.Status.State {
	case a2a.TaskStateInputRequired:
		question := ""
		if q := task.Status.Message; q != nil {
			question = fmt.Sprintf(" %q →", backend.Summary(q))
		}
		return fmt.Sprintf("toli: %s answered your question on task %s:%s %q. If there's more to do, respond with update_task.",
			peer, task.ID, question, backend.Summary(msg))
	case a2a.TaskStateSubmitted:
		return fmt.Sprintf("toli: %s added to task %s: %q. It's in list_available_tasks.", peer, task.ID, backend.Summary(msg))
	default:
		return fmt.Sprintf("toli: %s sent a message on task %s: %q. If you haven't already, respond with update_task.", peer, task.ID, backend.Summary(msg))
	}
}

// onTask is msg as stored in task's history: tied to the task and its context.
func onTask(msg *a2a.Message, task *a2a.Task) *a2a.Message {
	m := *msg
	m.TaskID, m.ContextID = task.ID, task.ContextID
	return &m
}

// GetExtendedAgentCard is the agent's full card (skills included), for the
// authenticated peer calling it. It's also how peers check their token works.
func (h *handler) GetExtendedAgentCard(ctx context.Context, req *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error) {
	return h.extendedCard(ctx)
}

// Streaming isn't offered (our cards say so): updates are written to the
// store directly, so they never reach a2asrv's event streams.

func (h *handler) SendStreamingMessage(ctx context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) { yield(nil, a2a.ErrUnsupportedOperation) }
}

func (h *handler) SubscribeToTask(ctx context.Context, req *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) { yield(nil, a2a.ErrUnsupportedOperation) }
}
