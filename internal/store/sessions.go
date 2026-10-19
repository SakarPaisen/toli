package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	sqlite3 "modernc.org/sqlite/lib"
)

// Session groups one agent's related outgoing tasks. Its ID is the A2A
// contextId sent with each of them.
type Session struct {
	ID        string
	Agent     string
	Title     string
	CreatedAt time.Time
}

// CreateSession starts a session for agent. Returns ErrNotFound if the agent
// doesn't exist.
func (s *Store) CreateSession(ctx context.Context, agent, title string) (Session, error) {
	sess := Session{ID: a2a.NewContextID(), Agent: agent, Title: title, CreatedAt: time.Now()}
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO sessions (id, agent, title, created_at) VALUES (?, ?, ?, ?)",
		sess.ID, sess.Agent, sess.Title, sess.CreatedAt.Unix())
	if isConstraint(err, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	return sess, nil
}

// GetSession looks a session up by id.
func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	var (
		sess    Session
		created int64
	)
	err := s.db.QueryRowContext(ctx,
		"SELECT id, agent, title, created_at FROM sessions WHERE id = ?", id,
	).Scan(&sess.ID, &sess.Agent, &sess.Title, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	sess.CreatedAt = time.Unix(created, 0)
	return sess, nil
}

// DeleteSession deletes one of agent's sessions; its outgoing tasks are
// deleted with it (cascade). It returns the tasks that were still active,
// read in the same transaction, so the caller can cancel them on their peers.
// Returns ErrNotFound if agent has no such session.
func (s *Store) DeleteSession(ctx context.Context, agent, id string) ([]ActiveTask, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	active, err := activeTasks(ctx, tx, "context_id = ? AND owner = ?", id, agent)
	if err != nil {
		return nil, fmt.Errorf("list tasks of session %q: %w", id, err)
	}
	if err := oneRow(tx.ExecContext(ctx, "DELETE FROM sessions WHERE id = ? AND agent = ?", id, agent)); err != nil {
		return nil, err
	}
	return active, tx.Commit()
}
