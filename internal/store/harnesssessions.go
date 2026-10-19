package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sqlite3 "modernc.org/sqlite/lib"
)

// HarnessSession is the coding-agent session an agent is running now.
type HarnessSession struct {
	Agent        string
	Harness      string
	SessionID    string
	Address      map[string]string // how to reach the session; depends on the harness
	RegisteredAt time.Time
}

// SetHarnessSession records agent's session, replacing any earlier one.
func (s *Store) SetHarnessSession(ctx context.Context, as HarnessSession) error {
	addr, err := json.Marshal(as.Address)
	if err != nil {
		return fmt.Errorf("encode address: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO harness_sessions (agent, harness, session_id, address, registered_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (agent) DO UPDATE SET harness = excluded.harness, session_id = excluded.session_id,
			address = excluded.address, registered_at = excluded.registered_at`,
		as.Agent, as.Harness, as.SessionID, string(addr), as.RegisteredAt.Unix())
	if isConstraint(err, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY) {
		return fmt.Errorf("agent %q: %w", as.Agent, ErrNotFound)
	}
	return err
}

// HarnessSession returns agent's session. Returns ErrNotFound if it has none.
func (s *Store) HarnessSession(ctx context.Context, agent string) (HarnessSession, error) {
	as := HarnessSession{Agent: agent}
	var addr string
	var at int64
	err := s.db.QueryRowContext(ctx,
		"SELECT harness, session_id, address, registered_at FROM harness_sessions WHERE agent = ?", agent,
	).Scan(&as.Harness, &as.SessionID, &addr, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return HarnessSession{}, ErrNotFound
	}
	if err != nil {
		return HarnessSession{}, err
	}
	if err := json.Unmarshal([]byte(addr), &as.Address); err != nil {
		return HarnessSession{}, fmt.Errorf("decode address: %w", err)
	}
	as.RegisteredAt = time.Unix(at, 0)
	return as, nil
}

// RemoveHarnessSession forgets agent's session if it's still sessionID (a
// newer session may have replaced it). Returns ErrNotFound otherwise.
func (s *Store) RemoveHarnessSession(ctx context.Context, agent, sessionID string) error {
	return oneRow(s.db.ExecContext(ctx, "DELETE FROM harness_sessions WHERE agent = ? AND session_id = ?", agent, sessionID))
}
