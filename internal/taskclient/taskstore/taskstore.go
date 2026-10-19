// Package taskstore is the storage contract for taskclient: the Task it
// stores, the Store interface to implement, and the Filter every Store must
// honor. Implement Store to give taskclient persistence (toli uses SQLite).
package taskstore

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

var (
	// ErrNotFound: no stored task with that ID.
	ErrNotFound = errors.New("task not found")
	// ErrExists: Create was given a task ID that's already stored.
	ErrExists = errors.New("task already exists")
	// ErrConflict: UpdateA2A's prevVersion didn't match; someone else updated the task first.
	ErrConflict = errors.New("task was updated concurrently")
)

// Task is one outgoing task as last reported by its peer. It wraps the A2A
// task unmodified; the other fields are local bookkeeping, never sent anywhere.
type Task struct {
	A2A      *a2a.Task // latest task the peer reported: the source of truth
	PeerID   int64     // which peer runs it
	Owner    string    // local label for filtering; toli: the agent
	Version  int64     // +1 on every update; guards concurrent updates and lets callers detect change
	SavedAt  time.Time // when we last saved a report
	Deadline time.Time // when we give up: the poller cancels the task on the peer
	Direct   bool      // the peer answered directly; this is a local, already-completed record, not a task on the peer

	// ClientContext reports that the client supplied A2A.ContextID on the
	// first message, rather than the peer generating it. A peer must keep a
	// supplied contextId or reject the request (A2A §3.4.1), so it can't change.
	ClientContext bool
}

// Filter narrows List. Every field that's set must match (AND); a zero
// field matches anything, so a zero Filter matches every task. Matches is the
// reference definition; every Store must filter exactly the same way.
type Filter struct {
	Owner     string          // task's Owner equals this
	ContextID string          // task has ClientContext and its A2A.ContextID equals this (toli: a session)
	States    []a2a.TaskState // task's current A2A.Status.State is one of these
}

// Matches reports whether t passes the filter.
func (f Filter) Matches(t *Task) bool {
	if f.Owner != "" && t.Owner != f.Owner {
		return false
	}
	if f.ContextID != "" && (!t.ClientContext || t.A2A.ContextID != f.ContextID) {
		return false
	}
	if len(f.States) > 0 && !slices.Contains(f.States, t.A2A.Status.State) {
		return false
	}
	return true
}

// Store persists tasks.
//
// UpdateA2A is optimistic: it replaces only the A2A task (local fields are
// untouched) and only if the stored Version still equals prevVersion. In SQL:
//
//	UPDATE tasks SET a2a = ?, saved_at = ?, version = version + 1
//	WHERE id = ? AND version = ? RETURNING ...
//	-- 0 rows changed -> ErrConflict
type Store interface {
	Create(ctx context.Context, t *Task) error // new task (Version 1); ErrExists if the ID is taken
	// UpdateA2A sets A2A, SavedAt = now, Version = prevVersion+1, and returns
	// the updated task. ErrConflict if the stored Version != prevVersion,
	// ErrNotFound if there's no such task.
	UpdateA2A(ctx context.Context, id a2a.TaskID, reported *a2a.Task, prevVersion int64) (*Task, error)
	Get(ctx context.Context, id a2a.TaskID) (*Task, error) // ErrNotFound if missing
	List(ctx context.Context, f Filter) ([]*Task, error)   // tasks matching f, in any order
}
