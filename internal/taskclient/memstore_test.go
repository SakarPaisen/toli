package taskclient

import (
	"context"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/taskclient/taskstore"
)

// memStore is an in-memory Store for tests. It hands out copies so callers
// can't change stored state behind its back.
type memStore struct {
	mu    sync.Mutex
	tasks map[a2a.TaskID]*taskstore.Task
}

func newMemStore() *memStore {
	return &memStore{tasks: map[a2a.TaskID]*taskstore.Task{}}
}

func clone(t *taskstore.Task) *taskstore.Task {
	c := *t
	if t.A2A != nil {
		a := *t.A2A
		c.A2A = &a
	}
	return &c
}

func (s *memStore) Create(ctx context.Context, t *taskstore.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[t.A2A.ID]; ok {
		return taskstore.ErrExists
	}
	c := clone(t)
	c.Version = 1
	s.tasks[t.A2A.ID] = c
	return nil
}

func (s *memStore) UpdateA2A(ctx context.Context, id a2a.TaskID, reported *a2a.Task, prevVersion int64) (*taskstore.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.tasks[id]
	if !ok {
		return nil, taskstore.ErrNotFound
	}
	if stored.Version != prevVersion {
		return nil, taskstore.ErrConflict
	}
	stored.A2A = reported
	stored.SavedAt = time.Now()
	stored.Version = prevVersion + 1
	return clone(stored), nil
}

func (s *memStore) Get(ctx context.Context, id a2a.TaskID) (*taskstore.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, taskstore.ErrNotFound
	}
	return clone(t), nil
}

func (s *memStore) List(ctx context.Context, f taskstore.Filter) ([]*taskstore.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*taskstore.Task
	for _, t := range s.tasks {
		if f.Matches(t) {
			out = append(out, clone(t))
		}
	}
	return out, nil
}
