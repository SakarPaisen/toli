package taskclient

import (
	"context"
	"errors"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/taskclient/taskstore"
)

// A2A gives polled reports no ordering (no sequence number until spec v1.1's
// Task.generation), and our own calls on a task can race the poller: a poll
// answered before the peer saw our reply can arrive after the reply's
// response. So each kind of save says which stored states make its report
// stale (a reject rule), and record saves only if the rule passes against
// the latest stored copy.

// maxRecordTries bounds record's retries when other saves keep landing
// between its read and its write.
const maxRecordTries = 5

// record saves the peer's report unless reject says the stored task makes it
// stale (or the stored task is final). It returns the task as it ends up.
// The decision is made on the latest stored copy: if another save lands
// between reading and writing, it reads again and decides again.
func (c *Client) record(ctx context.Context, report *a2a.Task, reject func(stored *a2a.Task) bool) (*taskstore.Task, error) {
	for range maxRecordTries {
		stored, err := c.store.Get(ctx, report.ID)
		if err != nil {
			return nil, err
		}
		if isFinal(stored.A2A.Status.State) || reject(stored.A2A) {
			return stored, nil
		}
		t, err := c.store.UpdateA2A(ctx, report.ID, report, stored.Version)
		if errors.Is(err, taskstore.ErrConflict) {
			continue
		}
		return t, err
	}
	return nil, fmt.Errorf("record task %s: kept conflicting with other saves", report.ID)
}

// Reject rules, one per kind of save.

// rejectNone: only the final-state rule applies (a cancel's result).
func rejectNone(*a2a.Task) bool { return false }

// rejectOlderThan rejects a polled report that lacks the stored task's newest
// message: it was produced before something we've already seen. History only
// grows, and a peer that trims it still keeps the newest messages.
func rejectOlderThan(report *a2a.Task) func(stored *a2a.Task) bool {
	return func(stored *a2a.Task) bool {
		newest := newestMessage(stored)
		return newest != nil && !hasMessage(report, newest.ID)
	}
}

// rejectHasMessage rejects a reply's result if the stored task already has
// that reply: a later save (e.g. a poll after the peer saw it) got there first
// and is at least as new.
func rejectHasMessage(id string) func(stored *a2a.Task) bool {
	return func(stored *a2a.Task) bool { return hasMessage(stored, id) }
}

// newestMessage is the last thing said on the task: the current status
// message if there is one (a peer's question sits there until its next status
// change moves it into history), else the last history entry.
func newestMessage(t *a2a.Task) *a2a.Message {
	if t.Status.Message != nil {
		return t.Status.Message
	}
	for i := len(t.History) - 1; i >= 0; i-- {
		if t.History[i] != nil {
			return t.History[i]
		}
	}
	return nil
}

// hasMessage reports whether a message is in the task's history or is its
// current status message.
func hasMessage(t *a2a.Task, id string) bool {
	if t.Status.Message != nil && t.Status.Message.ID == id {
		return true
	}
	for _, m := range t.History {
		if m != nil && m.ID == id {
			return true
		}
	}
	return false
}

// isFinal reports whether a state can never change again.
func isFinal(s a2a.TaskState) bool {
	return s.Terminal()
}
