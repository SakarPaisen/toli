package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	sqlite3 "modernc.org/sqlite/lib"
)

// Caller is who's using the incoming task store: a peer calling one of our
// agents (Agent and PeerID), or our agent itself (PeerID 0: all of its tasks).
type Caller struct {
	Agent  string
	PeerID int64
}

// scope is the WHERE condition limiting a query to c's tasks.
func (c Caller) scope() (string, []any) {
	return "agent = ? AND (? = 0 OR peer_id = ?)", []any{c.Agent, c.PeerID, c.PeerID}
}

// IncomingTasks is a2asrv's taskstore.Store on the incoming_tasks table. It
// behaves like a2asrv's own in-memory store: every task belongs to the peer
// that created it, and is invisible (ErrTaskNotFound) to everyone else, except
// the agent it was sent to.
type IncomingTasks struct {
	db     *sql.DB
	caller func(context.Context) (Caller, error)
}

var _ taskstore.Store = (*IncomingTasks)(nil)

// IncomingTasks returns the incoming task store. caller identifies who a
// request is from (set by the A2A server's authentication).
func (s *Store) IncomingTasks(caller func(context.Context) (Caller, error)) *IncomingTasks {
	return &IncomingTasks{db: s.db, caller: caller}
}

// user is how a caller appears in taskstore.StoredTask.User.
func user(peerID int64) string {
	return strconv.FormatInt(peerID, 10)
}

func (s *IncomingTasks) Create(ctx context.Context, task *a2a.Task) (taskstore.TaskVersion, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("identify caller: %w", err)
	}
	jsonA2A, err := json.Marshal(task)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("encode task %s: %w", task.ID, err)
	}

	now := time.Now().UnixNano()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO incoming_tasks (id, agent, peer_id, context_id, state, a2a, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		task.ID, c.Agent, c.PeerID, task.ContextID, string(task.Status.State), string(jsonA2A), now, now)
	switch {
	case isConstraint(err, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY):
		return taskstore.TaskVersionMissing, taskstore.ErrTaskAlreadyExists
	case isConstraint(err, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY):
		return taskstore.TaskVersionMissing, fmt.Errorf("unknown agent %q or peer %d: %w", c.Agent, c.PeerID, err)
	case err != nil:
		return taskstore.TaskVersionMissing, err
	}
	return 1, nil
}

func (s *IncomingTasks) Update(ctx context.Context, req *taskstore.UpdateRequest) (taskstore.TaskVersion, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("identify caller: %w", err)
	}
	task := req.Task
	jsonA2A, err := json.Marshal(task)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("encode task %s: %w", task.ID, err)
	}

	// Only the caller's own task, and (unless PrevVersion is missing) only if
	// nobody updated it since PrevVersion.
	scope, scopeArgs := c.scope()
	var version int64
	err = s.db.QueryRowContext(ctx,
		`UPDATE incoming_tasks SET a2a = ?, state = ?, version = version + 1, updated_at = ?
		  WHERE id = ? AND `+scope+` AND (? = 0 OR version = ?)
		RETURNING version`,
		append(append([]any{string(jsonA2A), string(task.Status.State), time.Now().UnixNano(), task.ID},
			scopeArgs...), int64(req.PrevVersion), int64(req.PrevVersion))...,
	).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		// Not found, someone else's (masked as not found), or a stale version.
		var one int
		err := s.db.QueryRowContext(ctx, "SELECT 1 FROM incoming_tasks WHERE id = ? AND "+scope,
			append([]any{task.ID}, scopeArgs...)...).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return taskstore.TaskVersionMissing, a2a.ErrTaskNotFound
		}
		if err != nil {
			return taskstore.TaskVersionMissing, err
		}
		return taskstore.TaskVersionMissing, taskstore.ErrConcurrentModification
	}
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	return taskstore.TaskVersion(version), nil
}

func (s *IncomingTasks) Get(ctx context.Context, id a2a.TaskID) (*taskstore.StoredTask, error) {
	c, err := s.caller(ctx)
	if err != nil {
		return nil, fmt.Errorf("identify caller: %w", err)
	}
	scope, scopeArgs := c.scope()
	t, err := scanIncoming(s.db.QueryRowContext(ctx,
		"SELECT "+incomingColumns+" FROM incoming_tasks WHERE id = ? AND "+scope, append([]any{id}, scopeArgs...)...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, a2a.ErrTaskNotFound // someone else's tasks look like no task
	}
	if err != nil {
		return nil, err
	}
	return &taskstore.StoredTask{Task: t.task, Version: t.version, User: user(t.peerID)}, nil
}

// List returns the caller's tasks, most recently updated first, with the
// request's filters and paging, as a2asrv's in-memory store does.
func (s *IncomingTasks) List(ctx context.Context, req *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	const (
		defaultPageSize   = 50
		defaultMaxHistory = 100
	)
	c, err := s.caller(ctx)
	if err != nil {
		return nil, a2a.ErrUnauthenticated
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = defaultPageSize
	} else if pageSize < 1 || pageSize > 100 {
		return nil, fmt.Errorf("page size must be between 1 and 100 inclusive, got %d: %w", pageSize, a2a.ErrInvalidRequest)
	}

	scope, args := c.scope()
	conds := []string{scope}
	if req.ContextID != "" {
		conds, args = append(conds, "context_id = ?"), append(args, req.ContextID)
	}
	if req.Status != a2a.TaskStateUnspecified {
		conds, args = append(conds, "state = ?"), append(args, string(req.Status))
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+incomingColumns+" FROM incoming_tasks WHERE "+strings.Join(conds, " AND ")+
			" ORDER BY updated_at DESC, id DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var matched []incomingRow
	for rows.Next() {
		t, err := scanIncoming(rows)
		if err != nil {
			return nil, err
		}
		if ts := t.task.Status.Timestamp; req.StatusTimestampAfter != nil && ts != nil && ts.Before(*req.StatusTimestampAfter) {
			continue
		}
		matched = append(matched, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Page: skip up to and including the token's task, then take pageSize.
	page := matched
	if req.PageToken != "" {
		at, id, err := decodePageToken(req.PageToken)
		if err != nil {
			return nil, err
		}
		i := 0
		for i < len(page) && (page[i].updatedAt > at || page[i].updatedAt == at && page[i].task.ID >= id) {
			i++
		}
		page = page[i:]
	}
	next := ""
	if len(page) > pageSize {
		last := page[pageSize-1]
		next = encodePageToken(last.updatedAt, last.task.ID)
		page = page[:pageSize]
	}

	history := defaultMaxHistory
	if req.HistoryLength != nil {
		history = *req.HistoryLength
	}
	tasks := make([]*a2a.Task, 0, len(page))
	for _, r := range page {
		t := r.task
		switch {
		case history <= 0:
			t.History = []*a2a.Message{}
		case len(t.History) > history:
			t.History = t.History[len(t.History)-history:]
		}
		if !req.IncludeArtifacts {
			t.Artifacts = nil
		}
		tasks = append(tasks, t)
	}
	return &a2a.ListTasksResponse{Tasks: tasks, TotalSize: len(matched), PageSize: pageSize, NextPageToken: next}, nil
}

// incomingColumns is the column list scanIncoming expects, in order.
const incomingColumns = "a2a, version, peer_id, updated_at"

type incomingRow struct {
	task      *a2a.Task
	version   taskstore.TaskVersion
	peerID    int64
	updatedAt int64 // unix nanoseconds
}

func scanIncoming(sc scanner) (incomingRow, error) {
	var (
		r       incomingRow
		jsonA2A string
	)
	if err := sc.Scan(&jsonA2A, &r.version, &r.peerID, &r.updatedAt); err != nil {
		return incomingRow{}, err
	}
	r.task = new(a2a.Task)
	if err := json.Unmarshal([]byte(jsonA2A), r.task); err != nil {
		return incomingRow{}, fmt.Errorf("decode incoming task: %w", err)
	}
	return r, nil
}

// A page token is "<updated_at>:<task id>" of the last task on the previous
// page, base64url-encoded.
func encodePageToken(updatedAt int64, id a2a.TaskID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(updatedAt, 10) + ":" + string(id)))
}

func decodePageToken(tok string) (int64, a2a.TaskID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err == nil {
		if at, id, ok := strings.Cut(string(raw), ":"); ok {
			if n, err := strconv.ParseInt(at, 10, 64); err == nil {
				return n, a2a.TaskID(id), nil
			}
		}
	}
	return 0, "", fmt.Errorf("invalid page token: %w", a2a.ErrInvalidRequest)
}

// IncomingTask is a task a peer sent one of our agents, as our agent sees it.
type IncomingTask struct {
	Task      *a2a.Task
	Version   int64
	PeerID    int64
	UpdatedAt time.Time
}

// GetIncoming returns one of agent's incoming tasks. Returns ErrNotFound if
// there's no such task or it isn't agent's.
func (s *Store) GetIncoming(ctx context.Context, agent string, id a2a.TaskID) (*IncomingTask, error) {
	r, err := scanIncoming(s.db.QueryRowContext(ctx,
		"SELECT "+incomingColumns+" FROM incoming_tasks WHERE id = ? AND agent = ?", id, agent))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.incomingTask(), nil
}

// ListIncoming returns agent's incoming tasks in any of states (all if none),
// most recently updated first.
func (s *Store) ListIncoming(ctx context.Context, agent string, states []a2a.TaskState) ([]*IncomingTask, error) {
	q, args := "SELECT "+incomingColumns+" FROM incoming_tasks WHERE agent = ?", []any{agent}
	if len(states) > 0 {
		q += " AND state IN (" + strings.Repeat("?, ", len(states)-1) + "?)"
		for _, st := range states {
			args = append(args, string(st))
		}
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY updated_at DESC, id DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*IncomingTask
	for rows.Next() {
		r, err := scanIncoming(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r.incomingTask())
	}
	return out, rows.Err()
}

func (r incomingRow) incomingTask() *IncomingTask {
	return &IncomingTask{Task: r.task, Version: int64(r.version), PeerID: r.peerID, UpdatedAt: time.Unix(0, r.updatedAt)}
}
