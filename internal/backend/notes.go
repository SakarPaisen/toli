package backend

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/taskclient/taskstore"
)

// Summary is a message's first line of text, shortened for a note.
func Summary(msg *a2a.Message) string {
	var text string
	for _, p := range msg.Parts {
		if p != nil && p.Text() != "" {
			text = p.Text()
			break
		}
	}
	line, _, _ := strings.Cut(text, "\n")
	if r := []rune(line); len(r) > 120 {
		line = string(r[:120]) + "…"
	}
	return line
}

// delegatedChanged tells the agent that delegated a task what changed on it
// (taskclient's OnChange: the poller saved a newer report).
func (b *Backend) delegatedChanged(prev, next *taskstore.Task) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peer, err := b.GetPeer(ctx, next.PeerID)
	if err != nil {
		slog.Warn("notify: load peer", "peer", next.PeerID, "err", err)
		return
	}
	if text := delegatedNote(peer.Name, prev.A2A, next.A2A); text != "" {
		slog.Info("task updated", "agent", next.Owner, "peer", peer.Name, "task", next.A2A.ID, "state", StateName(next.A2A.Status.State))
		b.NotifyAgent(next.Owner, text)
	}
}

// delegatedNote is the note for a delegated task that went from prev to next
// on peer: what the worker said since, and where the task is now. "" if
// nothing worth telling changed.
func delegatedNote(peer string, prev, next *a2a.Task) string {
	seen := map[string]bool{}
	for _, m := range conversation(prev) {
		seen[m.ID] = true
	}
	var said []*a2a.Message
	for _, m := range conversation(next) {
		if m.Role == a2a.MessageRoleAgent && !seen[m.ID] {
			said = append(said, m)
		}
	}
	if len(said) == 0 && prev.Status.State == next.Status.State {
		return ""
	}

	latest, more := "", ""
	if len(said) > 0 {
		latest = fmt.Sprintf(": %q", Summary(said[len(said)-1]))
	}
	if len(said) > 1 {
		more = fmt.Sprintf(" (%d new messages: get_delegated_task_updates)", len(said))
	}
	id := next.ID
	switch next.Status.State {
	case a2a.TaskStateInputRequired:
		return fmt.Sprintf("toli: %s asked on task %s%s. If you haven't already, answer with reply.%s", peer, id, latest, more)
	case a2a.TaskStateCompleted:
		return fmt.Sprintf("toli: %s completed task %s%s. See get_delegated_task_updates.%s", peer, id, latest, more)
	case a2a.TaskStateFailed:
		return fmt.Sprintf("toli: %s failed task %s%s.%s", peer, id, latest, more)
	case a2a.TaskStateRejected:
		return fmt.Sprintf("toli: %s rejected task %s%s.%s", peer, id, latest, more)
	case a2a.TaskStateCanceled:
		return fmt.Sprintf("toli: task %s on %s was canceled%s.%s", id, peer, latest, more)
	default:
		if len(said) == 0 {
			return "" // e.g. submitted -> working with nothing said
		}
		return fmt.Sprintf("toli: %s posted on task %s%s.%s", peer, id, latest, more)
	}
}

// conversation is a task's history plus its status message, if that isn't
// in the history yet.
func conversation(t *a2a.Task) []*a2a.Message {
	var msgs []*a2a.Message
	seen := map[string]bool{}
	for _, m := range t.History {
		if m != nil && !seen[m.ID] {
			msgs = append(msgs, m)
			seen[m.ID] = true
		}
	}
	if m := t.Status.Message; m != nil && !seen[m.ID] {
		msgs = append(msgs, m)
	}
	return msgs
}
