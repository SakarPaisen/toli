package backend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aevent"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"

	"github.com/arjunvlama/toli/internal/store"
)

// ErrInvalidUpdate is returned for an update_task the task can't take: bad
// arguments, a final task, or a second open question.
var ErrInvalidUpdate = errors.New("invalid update")

// Artifact is a named text output of a task.
type Artifact struct {
	Name string
	Text string
}

// InboundUpdate is what our agent tells the delegator of a task it was sent.
type InboundUpdate struct {
	State     a2a.TaskState
	Message   string
	Artifacts []Artifact // a name already on the task replaces that artifact
}

// maxUpdateTries bounds retries when another write (e.g. the delegator's
// message) lands between reading a task and saving it.
const maxUpdateTries = 10

// InboundTask is a task a peer sent the agent, with the agent's name for
// that peer.
type InboundTask struct {
	*store.IncomingTask
	Peer string
}

// GetInboundTask returns one of the tasks peers sent the agent. Returns
// store.ErrNotFound if there's no such task or it isn't the agent's.
func (b *Backend) GetInboundTask(ctx context.Context, agent string, id a2a.TaskID) (*InboundTask, error) {
	t, err := b.store.GetIncoming(ctx, agent, id)
	if err != nil {
		return nil, fmt.Errorf("task %s: %w", id, err)
	}
	return b.inboundWithPeer(ctx, t, nil)
}

// ListInboundTasks returns the tasks peers sent the agent in any of states
// (all if none), most recently updated first. With wait > 0, if there are
// none, it waits up to wait for one.
func (b *Backend) ListInboundTasks(ctx context.Context, agent string, states []a2a.TaskState, wait time.Duration) ([]*InboundTask, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	timeout := time.After(wait)
	for {
		tasks, err := b.store.ListIncoming(ctx, agent, states)
		if err != nil {
			return nil, fmt.Errorf("list incoming tasks of %q: %w", agent, err)
		}
		done := wait <= 0 || len(tasks) > 0
		if !done {
			select {
			case <-timeout:
				done = true
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ticker.C:
				continue
			}
		}
		out := make([]*InboundTask, 0, len(tasks))
		names := map[int64]string{}
		for _, t := range tasks {
			shown, err := b.inboundWithPeer(ctx, t, names)
			if err != nil {
				return nil, err
			}
			out = append(out, shown)
		}
		return out, nil
	}
}

// inboundWithPeer adds the peer's name, caching lookups in names if non-nil.
func (b *Backend) inboundWithPeer(ctx context.Context, t *store.IncomingTask, names map[int64]string) (*InboundTask, error) {
	name, ok := names[t.PeerID]
	if !ok {
		p, err := b.GetPeer(ctx, t.PeerID)
		if err != nil {
			return nil, fmt.Errorf("load peer %d: %w", t.PeerID, err)
		}
		name = p.Name
		if names != nil {
			names[t.PeerID] = name
		}
	}
	return &InboundTask{IncomingTask: t, Peer: name}, nil
}

// inbound is the incoming task store as the agent itself: all its tasks,
// from any peer.
func (b *Backend) inbound(agent string) *store.IncomingTasks {
	return b.store.IncomingTasks(func(context.Context) (store.Caller, error) {
		return store.Caller{Agent: agent}, nil
	})
}

// AcceptInboundTask takes on new work (submitted -> working), with an
// optional message to the delegator.
func (b *Backend) AcceptInboundTask(ctx context.Context, agent string, id a2a.TaskID, message string) (*InboundTask, error) {
	u := InboundUpdate{State: a2a.TaskStateWorking, Message: message}
	if _, err := applyInbound(ctx, b.inbound(agent), id, u, checkAccept); err != nil {
		return nil, err
	}
	slog.Info("task accepted", "agent", agent, "task", id)
	return b.GetInboundTask(ctx, agent, id)
}

// UpdateInboundTask applies the agent's update to a task a peer sent it: one
// it accepted, or a new one it declines (rejected). The delegator sees it on
// its next poll. With wait > 0 on input-required, it then waits (up to wait)
// for the task to change, e.g. the delegator's answer, and returns the task
// as it is then.
func (b *Backend) UpdateInboundTask(ctx context.Context, agent string, id a2a.TaskID, u InboundUpdate, wait time.Duration) (*InboundTask, error) {
	if err := checkUpdate(u); err != nil {
		return nil, err
	}
	saved, err := applyInbound(ctx, b.inbound(agent), id, u, checkUpdateOn)
	if err != nil {
		return nil, err
	}
	slog.Info("task updated", "agent", agent, "task", id, "state", StateName(u.State))
	if wait > 0 && u.State == a2a.TaskStateInputRequired {
		if err := b.waitInbound(ctx, agent, id, saved, wait); err != nil {
			return nil, err
		}
	}
	return b.GetInboundTask(ctx, agent, id)
}

func checkUpdate(u InboundUpdate) error {
	switch u.State {
	case a2a.TaskStateWorking, a2a.TaskStateCompleted, a2a.TaskStateFailed:
	case a2a.TaskStateInputRequired, a2a.TaskStateRejected:
		if u.Message == "" {
			return fmt.Errorf("%s needs a message: %w", StateName(u.State), ErrInvalidUpdate)
		}
	default:
		return fmt.Errorf("state %q: want working, input-required, completed, failed, or rejected: %w", StateName(u.State), ErrInvalidUpdate)
	}
	for _, a := range u.Artifacts {
		if a.Name == "" || a.Text == "" {
			return fmt.Errorf("an artifact needs a name and text: %w", ErrInvalidUpdate)
		}
	}
	return nil
}

// checkAccept: only new work can be accepted.
func checkAccept(id a2a.TaskID, s a2a.TaskState, _ InboundUpdate) error {
	if err := checkOpen(id, s); err != nil {
		return err
	}
	if s != a2a.TaskStateSubmitted {
		return fmt.Errorf("task %s is already accepted: %w", id, ErrInvalidUpdate)
	}
	return nil
}

// checkUpdateOn: updates go to accepted work, except declining new work; one
// open question at a time.
func checkUpdateOn(id a2a.TaskID, s a2a.TaskState, u InboundUpdate) error {
	if err := checkOpen(id, s); err != nil {
		return err
	}
	switch {
	case s == a2a.TaskStateSubmitted && u.State != a2a.TaskStateRejected:
		return fmt.Errorf("task %s hasn't been accepted yet: accept it first, or decline it (rejected): %w", id, ErrInvalidUpdate)
	case s == a2a.TaskStateInputRequired && u.State == a2a.TaskStateInputRequired:
		return fmt.Errorf("task %s already has an open question: %w", id, ErrInvalidUpdate)
	}
	return nil
}

// checkOpen: a final task can't change.
func checkOpen(id a2a.TaskID, s a2a.TaskState) error {
	switch {
	case s == a2a.TaskStateCanceled:
		return fmt.Errorf("task %s was canceled by its delegator; stop working on it: %w", id, ErrInvalidUpdate)
	case s.Terminal():
		return fmt.Errorf("task %s is already %s: %w", id, StateName(s), ErrInvalidUpdate)
	}
	return nil
}

// applyInbound reads the task, checks the update against it, and saves the
// result if nobody wrote the task in between; otherwise it decides again on
// the latest copy. Returns the saved version.
func applyInbound(ctx context.Context, tasks *store.IncomingTasks, id a2a.TaskID, u InboundUpdate,
	check func(a2a.TaskID, a2a.TaskState, InboundUpdate) error) (int64, error) {
	for range maxUpdateTries {
		stored, err := tasks.Get(ctx, id)
		if err != nil {
			return 0, fmt.Errorf("task %s: %w", id, err)
		}
		task := stored.Task
		if err := check(id, task.Status.State, u); err != nil {
			return 0, err
		}

		next, err := applyUpdate(task, u)
		if err != nil {
			return 0, err
		}
		v, err := tasks.Update(ctx, &taskstore.UpdateRequest{Task: next, PrevTask: task, PrevVersion: stored.Version})
		if errors.Is(err, taskstore.ErrConcurrentModification) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("save task %s: %w", id, err)
		}
		return int64(v), nil
	}
	return 0, fmt.Errorf("task %s kept changing while saving an update", id)
}

// applyUpdate returns task with u applied as A2A events: the artifacts, then
// the status. The status event moves the previous status message (e.g. an
// earlier question) into history.
func applyUpdate(task *a2a.Task, u InboundUpdate) (*a2a.Task, error) {
	next := task
	for _, a := range u.Artifacts {
		ev := a2a.NewArtifactEvent(next, a2a.NewTextPart(a.Text))
		ev.Artifact.Name = a.Name
		if i := slices.IndexFunc(next.Artifacts, func(x *a2a.Artifact) bool { return x.Name == a.Name }); i >= 0 {
			ev.Artifact.ID = next.Artifacts[i].ID
		}
		var err error
		if next, err = a2aevent.ApplyUpdate(next, ev); err != nil {
			return nil, fmt.Errorf("apply artifact %q: %w", a.Name, err)
		}
	}

	var msg *a2a.Message
	if u.Message != "" {
		msg = a2a.NewMessageForTask(a2a.MessageRoleAgent, next, a2a.NewTextPart(u.Message))
	}
	next, err := a2aevent.ApplyUpdate(next, a2a.NewStatusUpdateEvent(next, u.State, msg))
	if err != nil {
		return nil, fmt.Errorf("apply status: %w", err)
	}
	return next, nil
}

// waitInbound re-reads the task about once a second until its version is
// newer than saved or wait passes.
func (b *Backend) waitInbound(ctx context.Context, agent string, id a2a.TaskID, saved int64, wait time.Duration) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	timeout := time.After(wait)
	for {
		select {
		case <-timeout:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		t, err := b.store.GetIncoming(ctx, agent, id)
		if err != nil {
			return fmt.Errorf("task %s: %w", id, err)
		}
		if t.Version > saved {
			return nil
		}
	}
}
