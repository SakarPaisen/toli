package backend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/store"
	"github.com/arjunvlama/toli/internal/taskclient"
	"github.com/arjunvlama/toli/internal/taskclient/taskstore"
)

// Task operations for a local agent's outgoing tasks. Every method takes the
// calling agent and only touches that agent's tasks, peers, and sessions;
// anything else looks like it doesn't exist (store.ErrNotFound / taskstore.ErrNotFound).

// Task is a stored task plus what the frontends need to show it, filled in by
// the same call that loads it: the peer's name.
type Task struct {
	*taskstore.Task
	Peer string // the peer's local name, e.g. "bob"; "" if it was just removed
}

// withPeer fills in what a stored task is shown with. names caches peer
// names across calls (nil for a single task). A peer removed in the meantime
// leaves Peer empty rather than failing.
func (b *Backend) withPeer(ctx context.Context, t *taskstore.Task, names map[int64]string) (*Task, error) {
	name, ok := names[t.PeerID]
	if !ok {
		p, err := b.GetPeer(ctx, t.PeerID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("load peer %d: %w", t.PeerID, err)
		}
		name = p.Name
		if names != nil {
			names[t.PeerID] = name
		}
	}
	return &Task{Task: t, Peer: name}, nil
}

// sendTimeout bounds a single call to a peer made on an agent's behalf.
const sendTimeout = 30 * time.Second

// Delegate starts a task on one of the agent's peers. sessionID groups it
// ("" for standalone); timeLimit 0 means the default.
func (b *Backend) Delegate(ctx context.Context, agent, peerName, text, sessionID string, timeLimit time.Duration) (*Task, error) {
	peers, err := b.peers.List(ctx, agent)
	if err != nil {
		return nil, fmt.Errorf("list peers of %q: %w", agent, err)
	}

	var peerID int64
	for _, peer := range peers {
		if peer.Name == peerName {
			peerID = peer.ID
			break
		}
	}
	if peerID == 0 {
		return nil, fmt.Errorf("peer %q: %w", peerName, store.ErrNotFound)
	}

	if sessionID != "" {
		_, err := b.ownSession(ctx, agent, sessionID)
		if err != nil {
			return nil, err
		}
	}

	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text))
	msg.ContextID = sessionID

	// A peer that accepts the connection but never answers shouldn't hang the agent's tool call.
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	task, err := b.tasks.Send(ctx, peerID, agent, msg, taskclient.SendOptions{TimeLimit: timeLimit})
	if err != nil {
		return nil, err
	}
	slog.Info("task delegated", "agent", agent, "peer", peerName, "task", task.A2A.ID)
	return &Task{Task: task, Peer: peerName}, nil
}

// GetTask returns one of the agent's tasks. If after >= 0, it first waits
// (up to wait) for a version newer than after, returning at once if there
// already is one; after < 0 returns immediately.
func (b *Backend) GetTask(ctx context.Context, agent string, id a2a.TaskID, after int64, wait time.Duration) (*Task, error) {
	task, err := b.ownTask(ctx, agent, id)
	if err != nil {
		return nil, err
	}

	if task.Version <= after {
		task, err = b.tasks.WaitForChange(ctx, id, after, wait)
		if err != nil {
			return nil, err
		}
	}
	return b.withPeer(ctx, task, nil)
}

// Reply answers the peer on one of the agent's tasks. answer is set if the
// peer replied directly with a message instead of updating the task.
// Everything that can fail locally happens before the message is sent, so an
// error before that point means nothing was sent and retrying is safe.
func (b *Backend) Reply(ctx context.Context, agent string, id a2a.TaskID, text string) (*Task, *a2a.Message, error) {
	own, err := b.ownTask(ctx, agent, id)
	if err != nil {
		return nil, nil, err
	}
	peer, err := b.GetPeer(ctx, own.PeerID)
	if err != nil {
		return nil, nil, fmt.Errorf("load peer %d: %w", own.PeerID, err)
	}

	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text))
	task, answer, err := b.tasks.Reply(ctx, id, msg)
	if err != nil {
		return nil, nil, err
	}
	return &Task{Task: task, Peer: peer.Name}, answer, nil
}

// CancelTask stops one of the agent's tasks on its peer. Like Reply, local
// lookups happen before the peer is called.
func (b *Backend) CancelTask(ctx context.Context, agent string, id a2a.TaskID) (*Task, error) {
	own, err := b.ownTask(ctx, agent, id)
	if err != nil {
		return nil, err
	}
	peer, err := b.GetPeer(ctx, own.PeerID)
	if err != nil {
		return nil, fmt.Errorf("load peer %d: %w", own.PeerID, err)
	}

	task, err := b.tasks.Cancel(ctx, id)
	if errors.Is(err, taskclient.ErrFinal) {
		return &Task{Task: own, Peer: peer.Name}, nil // already finished: report the state it ended in
	}
	if err != nil {
		return nil, err
	}
	return &Task{Task: task, Peer: peer.Name}, nil
}

// ListTasks lists the agent's tasks, optionally only a session's and/or only
// some states.
func (b *Backend) ListTasks(ctx context.Context, agent, sessionID string, states []a2a.TaskState) ([]*Task, error) {
	tasks, err := b.tasks.List(ctx, taskstore.Filter{Owner: agent, ContextID: sessionID, States: states})
	if err != nil {
		return nil, err
	}
	out := make([]*Task, 0, len(tasks))
	names := map[int64]string{}
	for _, t := range tasks {
		shown, err := b.withPeer(ctx, t, names)
		if err != nil {
			return nil, err
		}
		out = append(out, shown)
	}
	return out, nil
}

// ownTask loads a task and checks it belongs to agent.
func (b *Backend) ownTask(ctx context.Context, agent string, id a2a.TaskID) (*taskstore.Task, error) {
	task, err := b.tasks.Get(ctx, id)
	if err != nil {
		return task, err
	}
	if task.Owner != agent {
		return nil, taskstore.ErrNotFound
	}
	return task, nil
}

// StateName is an A2A state as toli shows it to agents and humans:
// TASK_STATE_INPUT_REQUIRED -> input-required.
func StateName(s a2a.TaskState) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(string(s), "TASK_STATE_")), "_", "-")
}
