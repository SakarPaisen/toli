// Package wake keeps track of the coding-agent session each agent is
// running (registered by its harness's hooks) and delivers notes to it. See
// ARCHITECTURE.md.
package wake

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/arjunvlama/toli/internal/store"
)

type Service struct {
	store *store.Store
	notes notes // waiting for bridge harnesses' plugins
}

func New(st *store.Store) *Service {
	return &Service{store: st}
}

// Register records the session agent is running now, replacing any earlier
// one. Notes still waiting for the earlier one are dropped: the new session
// gets the agent's pending work when it registers.
func (s *Service) Register(ctx context.Context, agent, harness, sessionID string, address map[string]string) error {
	err := s.store.SetHarnessSession(ctx, store.HarnessSession{
		Agent: agent, Harness: harness, SessionID: sessionID, Address: address, RegisteredAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("register session of %s: %w", agent, err)
	}
	s.notes.clear(agent)
	slog.Info("session registered", "agent", agent, "harness", harness)
	return nil
}

// Unregister forgets agent's session, if it's still sessionID.
func (s *Service) Unregister(ctx context.Context, agent, sessionID string) error {
	err := s.store.RemoveHarnessSession(ctx, agent, sessionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil // already replaced by a newer session, or never registered
	}
	return err
}

// Session returns agent's registered session. Returns store.ErrNotFound if
// it has none.
func (s *Service) Session(ctx context.Context, agent string) (store.HarnessSession, error) {
	return s.store.HarnessSession(ctx, agent)
}
