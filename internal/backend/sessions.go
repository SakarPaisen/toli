package backend

import (
	"context"
	"errors"
	"fmt"

	"github.com/arjunvlama/toli/internal/store"
)

// StartSession creates a session to group the agent's related tasks.
func (b *Backend) StartSession(ctx context.Context, agent, title string) (store.Session, error) {
	return b.store.CreateSession(ctx, agent, title)
}

// DeleteSession deletes one of the agent's sessions and its tasks, then asks
// the peers to cancel the ones still active, in the background (best effort).
func (b *Backend) DeleteSession(ctx context.Context, agent, sessionID string) error {
	tasks, err := b.store.DeleteSession(ctx, agent, sessionID)
	if err != nil {
		return err
	}
	b.peers.Cancel(tasks)
	return nil
}

// ownSession loads a session and checks it belongs to agent.
func (b *Backend) ownSession(ctx context.Context, agent string, sessionID string) (*store.Session, error) {
	sess, err := b.store.GetSession(ctx, sessionID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("load session %q: %w", sessionID, err)
	}
	if err != nil || sess.Agent != agent {
		return nil, fmt.Errorf("session %q: %w", sessionID, store.ErrNotFound)
	}
	return &sess, nil
}
