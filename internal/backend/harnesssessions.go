package backend

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/store"
)

// RegisterHarnessSession records the coding-agent session the agent is running now
// (replacing any earlier one), so the daemon can wake it. Returns a summary of
// the agent's pending work for the session's context ("" if there's none).
func (b *Backend) RegisterHarnessSession(ctx context.Context, agent, harness, sessionID string, address map[string]string) (string, error) {
	if err := b.wake.Register(ctx, agent, harness, sessionID, address); err != nil {
		return "", err
	}
	pending, err := b.pendingWork(ctx, agent)
	if err != nil {
		return "", err
	}
	notified := "toli: this session is notified when tasks you delegated change and when peers send you work, " +
		"so you don't need to wait on get_delegated_task_updates."
	if pending == "" {
		return notified, nil
	}
	return pending + "\n" + notified, nil
}

// UnregisterHarnessSession forgets the agent's session, if it's still
// sessionID.
func (b *Backend) UnregisterHarnessSession(ctx context.Context, agent, sessionID string) error {
	return b.wake.Unregister(ctx, agent, sessionID)
}

// HarnessSession returns the agent's registered session. Returns
// store.ErrNotFound if it has none.
func (b *Backend) HarnessSession(ctx context.Context, agent string) (store.HarnessSession, error) {
	return b.wake.Session(ctx, agent)
}

// pendingWork summarizes the agent's assigned tasks that need it.
func (b *Backend) pendingWork(ctx context.Context, agent string) (string, error) {
	tasks, err := b.store.ListIncoming(ctx, agent, []a2a.TaskState{
		a2a.TaskStateSubmitted, a2a.TaskStateWorking, a2a.TaskStateInputRequired,
	})
	if err != nil {
		return "", fmt.Errorf("list assigned tasks of %s: %w", agent, err)
	}
	var available, accepted int
	for _, t := range tasks {
		if t.Task.Status.State == a2a.TaskStateSubmitted {
			available++
		} else {
			accepted++
		}
	}
	var parts []string
	if available > 0 {
		parts = append(parts, fmt.Sprintf("%d new task(s) from peers (list_available_tasks)", available))
	}
	if accepted > 0 {
		parts = append(parts, fmt.Sprintf("%d accepted task(s) in progress (list_accepted_tasks)", accepted))
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "toli: " + strings.Join(parts, "; ") + ".", nil
}

// notifyTimeout bounds one NotifyAgent, retry included.
const notifyTimeout = 30 * time.Second

// NotifyAgent tells the agent's running session about work (see
// ARCHITECTURE.md), in the background: callers never wait on it. Best effort.
func (b *Backend) NotifyAgent(agent, text string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		if err := b.wake.Notify(ctx, agent, text); err != nil {
			slog.Warn("notify agent", "agent", agent, "err", err)
		}
	}()
}

// TakeNotes returns the notes waiting for the agent's session (a bridge
// harness's plugin collects them), waiting up to wait for some.
func (b *Backend) TakeNotes(ctx context.Context, agent, sessionID string, wait time.Duration) ([]string, error) {
	return b.wake.TakeNotes(ctx, agent, sessionID, wait)
}
