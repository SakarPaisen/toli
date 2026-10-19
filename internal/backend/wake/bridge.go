package wake

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/arjunvlama/toli/internal/store"
)

// Bridge harnesses (e.g. OpenCode) have no way in from outside, so a plugin
// inside the harness collects notes from the daemon (TakeNotes) and hands
// them to its session. Until then, an agent's notes wait in a channel, in
// memory: lost if the daemon restarts.

// maxQueued is how many notes can wait per agent; past that, the oldest go.
const maxQueued = 50

// ErrSessionReplaced: the session asking for notes isn't the agent's
// registered one any more.
var ErrSessionReplaced = errors.New("this session isn't the agent's registered session")

// notes maps each agent to the channel its notes wait in.
type notes struct {
	agents sync.Map // agent -> chan string
}

// put adds a note for agent, dropping the oldest if maxQueued are waiting.
func (n *notes) put(agent, note string) {
	v, _ := n.agents.LoadOrStore(agent, make(chan string, maxQueued))
	ch := v.(chan string)
	for {
		select {
		case ch <- note:
			return
		default:
			select {
			case <-ch:
			default:
			}
		}
	}
}

// clear drops agent's waiting notes.
func (n *notes) clear(agent string) {
	n.agents.Delete(agent)
}

// take waits up to wait for agent's notes, and returns all that are there.
func (n *notes) take(ctx context.Context, agent string, wait time.Duration) []string {
	v, _ := n.agents.LoadOrStore(agent, make(chan string, maxQueued))
	ch := v.(chan string)
	var got []string
	select {
	case note := <-ch:
		got = append(got, note)
	case <-time.After(wait):
		return nil
	case <-ctx.Done():
		return nil
	}
	for {
		select {
		case note := <-ch:
			got = append(got, note)
		default:
			return got
		}
	}
}

// TakeNotes returns the notes waiting for agent, if session is its
// registered session, waiting up to wait for some.
func (s *Service) TakeNotes(ctx context.Context, agent, session string, wait time.Duration) ([]string, error) {
	sess, err := s.store.HarnessSession(ctx, agent)
	if errors.Is(err, store.ErrNotFound) || err == nil && sess.SessionID != session {
		return nil, ErrSessionReplaced
	}
	if err != nil {
		return nil, fmt.Errorf("load session of %s: %w", agent, err)
	}
	return s.notes.take(ctx, agent, wait), ctx.Err()
}
