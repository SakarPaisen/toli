package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/arjunvlama/toli/internal/taskclient/taskstore"
)

// OutgoingTasks is the SQLite taskstore.Store, on the outgoing_tasks table.
// Times are stored as unix milliseconds; state is a copy of
// A2A.Status.State, kept in its own column for List's filter.
type OutgoingTasks struct {
	db *sql.DB
}

var _ taskstore.Store = (*OutgoingTasks)(nil)

// OutgoingTasks returns the outgoing task store backed by this database.
func (s *Store) OutgoingTasks() *OutgoingTasks {
	return &OutgoingTasks{db: s.db}
}

func (o *OutgoingTasks) Create(ctx context.Context, t *taskstore.Task) error {
	jsonA2A, err := json.Marshal(t.A2A)
	if err != nil {
		return fmt.Errorf("encode task %s: %w", t.A2A.ID, err)
	}

	_, err = o.db.ExecContext(ctx,
		`INSERT INTO outgoing_tasks
		(id, peer_id, owner, context_id, state, a2a, version, saved_at, deadline, direct, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
		t.A2A.ID,
		t.PeerID,
		t.Owner,
		clientContext(t),
		string(t.A2A.Status.State),
		string(jsonA2A), // string, so SQLite stores TEXT
		t.SavedAt.UnixMilli(),
		t.Deadline.UnixMilli(),
		t.Direct, // the driver stores bool as 1/0
		time.Now().UnixMilli(),
	)
	switch {
	case isConstraint(err, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY):
		return taskstore.ErrExists
	case isConstraint(err, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY):
		return fmt.Errorf("unknown peer %d, owner %q, or session %q: %w", t.PeerID, t.Owner, clientContext(t).String, err)
	}
	return err
}

func (o *OutgoingTasks) UpdateA2A(ctx context.Context, id a2a.TaskID, reported *a2a.Task, prevVersion int64) (*taskstore.Task, error) {

	jsonA2A, err := json.Marshal(reported)
	if err != nil {
		return nil, fmt.Errorf("encode task %s: %w", id, err)
	}

	// Only replaces the A2A part, and only if nobody updated the task since
	// prevVersion. RETURNING hands back the updated row in the same statement.
	row := o.db.QueryRowContext(ctx,
		`UPDATE outgoing_tasks
		    SET a2a = ?, state = ?, saved_at = ?, version = version + 1
		  WHERE id = ? AND version = ?
		RETURNING `+taskColumns,
		string(jsonA2A),
		string(reported.Status.State),
		time.Now().UnixMilli(),
		id,
		prevVersion,
	)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		// No row matched: either the task doesn't exist or its version moved on.
		var exists int
		err := o.db.QueryRowContext(ctx, "SELECT 1 FROM outgoing_tasks WHERE id = ?", id).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, taskstore.ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		return nil, taskstore.ErrConflict
	}
	return t, err
}

func (o *OutgoingTasks) Get(ctx context.Context, id a2a.TaskID) (*taskstore.Task, error) {
	t, err := scanTask(o.db.QueryRowContext(ctx, "SELECT "+taskColumns+" FROM outgoing_tasks WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, taskstore.ErrNotFound
	}
	return t, err
}

func (o *OutgoingTasks) List(ctx context.Context, f taskstore.Filter) ([]*taskstore.Task, error) {
	where, args := filterSQL(f)
	rows, err := o.db.QueryContext(ctx,
		"SELECT "+taskColumns+" FROM outgoing_tasks"+where+" ORDER BY created_at DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*taskstore.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// ActiveTask is a task that hadn't reached a final state when it was
// deleted: the caller should cancel it on its peer.
type ActiveTask struct {
	ID     a2a.TaskID
	PeerID int64
}

// activeTasks lists the non-final outgoing tasks matching where, inside tx.
func activeTasks(ctx context.Context, tx *sql.Tx, where string, args ...any) ([]ActiveTask, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id, peer_id, state FROM outgoing_tasks WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var active []ActiveTask
	for rows.Next() {
		var (
			t     ActiveTask
			state string
		)
		if err := rows.Scan(&t.ID, &t.PeerID, &state); err != nil {
			return nil, err
		}
		if !a2a.TaskState(state).Terminal() {
			active = append(active, t)
		}
	}
	return active, rows.Err()
}

// filterSQL turns a taskstore.Filter into a WHERE clause (with a leading
// space, or "" for no filter) and its arguments. It must match exactly what
// taskstore.Filter.Matches does.
func filterSQL(f taskstore.Filter) (string, []any) {
	var (
		conds []string
		args  []any
	)
	if f.Owner != "" {
		conds = append(conds, "owner = ?")
		args = append(args, f.Owner)
	}
	if f.ContextID != "" {
		conds = append(conds, "context_id = ?")
		args = append(args, f.ContextID)
	}
	if len(f.States) > 0 {
		conds = append(conds, "state IN (?"+strings.Repeat(", ?", len(f.States)-1)+")")
		for _, st := range f.States {
			args = append(args, string(st))
		}
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// clientContext is what goes in the context_id column: the task's contextId
// if the client supplied it (toli: the session), else NULL. A contextId the
// peer generated for a standalone task is no session of ours.
func clientContext(t *taskstore.Task) sql.NullString {
	if !t.ClientContext {
		return sql.NullString{}
	}
	return nullIfEmpty(t.A2A.ContextID)
}

// taskColumns is the column list scanTask expects, in order.
const taskColumns = "id, peer_id, owner, a2a, version, saved_at, deadline, direct, context_id"

// scanTask reads one outgoing_tasks row into a taskstore.Task. created_at
// isn't part of it.
func scanTask(sc scanner) (*taskstore.Task, error) {
	var (
		t        taskstore.Task
		id       string
		jsonA2A  string
		saved    int64
		deadline int64
		context  sql.NullString
	)
	if err := sc.Scan(&id, &t.PeerID, &t.Owner, &jsonA2A, &t.Version, &saved, &deadline, &t.Direct, &context); err != nil {
		return nil, err
	}
	t.A2A = new(a2a.Task)
	if err := json.Unmarshal([]byte(jsonA2A), t.A2A); err != nil {
		return nil, fmt.Errorf("decode task %s: %w", id, err)
	}
	t.SavedAt = time.UnixMilli(saved)
	t.Deadline = time.UnixMilli(deadline)
	t.ClientContext = context.Valid
	return &t, nil
}
