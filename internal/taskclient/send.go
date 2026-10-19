package taskclient

import (
	"context"
	"fmt"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	"github.com/arjunvlama/toli/internal/taskclient/taskstore"
)

// Send starts a task on a peer and records what the peer returns.
// Set msg.ContextID to group it (toli: a session); leave it empty for a
// standalone task. The task's deadline is now + opts.TimeLimit (or the default).
func (c *Client) Send(ctx context.Context, peerID int64, owner string, msg *a2a.Message, opts SendOptions) (*taskstore.Task, error) {
	client, err := c.peers(ctx, peerID)
	if err != nil {
		return nil, fmt.Errorf("resolve peer %d: %w", peerID, err)
	}

	result, err := client.SendMessage(ctx, &a2a.SendMessageRequest{
		Message: msg,
		Config:  &a2a.SendMessageConfig{ReturnImmediately: true}, // don't hold the call until the remote agent finishes
	})
	if err != nil {
		return nil, fmt.Errorf("send to peer %d: %w", peerID, err)
	}

	var t *taskstore.Task
	currTime := time.Now()
	switch r := result.(type) {
	case *a2a.Task:
		timeLimit := opts.TimeLimit
		if timeLimit == 0 {
			timeLimit = c.opts.DefaultTimeLimit
		}

		t = &taskstore.Task{A2A: r,
			PeerID:        peerID,
			Owner:         owner,
			Version:       1,
			SavedAt:       currTime,
			Deadline:      currTime.Add(timeLimit),
			ClientContext: msg.ContextID != "",
		}
	case *a2a.Message:
		t = &taskstore.Task{Direct: true, ClientContext: msg.ContextID != "", PeerID: peerID, Owner: owner, Version: 1, SavedAt: currTime, Deadline: currTime,
			A2A: &a2a.Task{ID: a2a.NewTaskID(),
				ContextID: msg.ContextID,
				Status:    a2a.TaskStatus{State: a2a.TaskStateCompleted, Message: r, Timestamp: &currTime},
				History:   []*a2a.Message{msg, r}}}
	}

	err = c.store.Create(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("store task %s: %w", t.A2A.ID, err)
	}

	return t, nil
}

// Reply sends a follow-up message on a task (e.g. answering input-required)
// and records the result.
func (c *Client) Reply(ctx context.Context, id a2a.TaskID, msg *a2a.Message) (*taskstore.Task, *a2a.Message, error) {

	t, err := c.store.Get(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("load task %s: %w", id, err)
	}

	if isFinal(t.A2A.Status.State) {
		return nil, nil, ErrFinal
	}

	client, err := c.peers(ctx, t.PeerID)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve peer %d: %w", t.PeerID, err)
	}

	msg.TaskID = id
	msg.ContextID = t.A2A.ContextID
	resp, err := client.SendMessage(ctx, &a2a.SendMessageRequest{
		Message: msg,
		Config:  &a2a.SendMessageConfig{ReturnImmediately: true},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("reply to task %s: %w", id, err)
	}

	switch r := resp.(type) {
	case *a2a.Task:
		updated, err := c.record(ctx, r, rejectHasMessage(msg.ID))
		if err != nil {
			return nil, nil, fmt.Errorf("record task %s: %w", id, err)
		}
		return updated, nil, nil
	case *a2a.Message:
		return t, r, nil
	}

	return nil, nil, fmt.Errorf("reply to task %s: unexpected result %T", id, resp)
}

// Cancel asks the peer to cancel a task and records the result.
func (c *Client) Cancel(ctx context.Context, id a2a.TaskID) (*taskstore.Task, error) {
	t, err := c.store.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load task %s: %w", id, err)
	}

	if isFinal(t.A2A.Status.State) {
		return nil, ErrFinal
	}

	client, err := c.peers(ctx, t.PeerID)
	if err != nil {
		return nil, fmt.Errorf("resolve peer %d: %w", t.PeerID, err)
	}

	updated, err := client.CancelTask(ctx, &a2a.CancelTaskRequest{ID: id})
	if err != nil {
		return nil, fmt.Errorf("cancel task %s: %w", id, err)
	}

	task, err := c.record(ctx, updated, rejectNone)
	if err != nil {
		return nil, fmt.Errorf("record task %s: %w", id, err)
	}

	return task, nil
}

// checkAndUpdateStatus fetches a task's current state from its peer and
// records it, unless it's older than what's stored (see rejectOlderThan).
func (c *Client) checkAndUpdateStatus(ctx context.Context, t *taskstore.Task) (*taskstore.Task, error) {
	id := t.A2A.ID

	client, err := c.peers(ctx, t.PeerID)
	if err != nil {
		return nil, fmt.Errorf("resolve peer %d: %w", t.PeerID, err)
	}

	reported, err := client.GetTask(ctx, &a2a.GetTaskRequest{ID: id})
	if err != nil {
		return nil, fmt.Errorf("get task %s: %w", id, err)
	}

	saved, err := c.record(ctx, reported, rejectOlderThan(reported))
	if err == nil && saved.Version != t.Version && c.opts.OnChange != nil {
		c.opts.OnChange(t, saved)
	}
	return saved, err
}

// Get returns the stored task. Never calls the peer.
func (c *Client) Get(ctx context.Context, id a2a.TaskID) (*taskstore.Task, error) {
	return c.store.Get(ctx, id)
}

// List returns stored tasks matching f. Never calls the peer.
func (c *Client) List(ctx context.Context, f taskstore.Filter) ([]*taskstore.Task, error) {
	return c.store.List(ctx, f)
}

// WaitForChange re-reads the stored task about once a second until its
// Version is greater than after or maxWait passes, then returns the latest
// either way.
func (c *Client) WaitForChange(ctx context.Context, id a2a.TaskID, after int64, maxWait time.Duration) (*taskstore.Task, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	timeout := time.After(maxWait)

	for {
		t, err := c.store.Get(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("load task %s: %w", id, err)
		}
		if t.Version > after {
			return t, nil
		}

		select {
		case <-timeout:
			return t, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// CancelUntracked asks a peer to cancel a task we no longer store (e.g. it was
// deleted with its peer or session). Nothing is recorded. It takes the client
// directly, since the peer may already be gone from the resolver.
func CancelUntracked(ctx context.Context, client *a2aclient.Client, id a2a.TaskID) error {
	if _, err := client.CancelTask(ctx, &a2a.CancelTaskRequest{ID: id}); err != nil {
		return fmt.Errorf("cancel untracked task %s: %w", id, err)
	}
	return nil
}
