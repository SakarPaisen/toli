package backend

import (
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func TestDelegatedNote(t *testing.T) {
	req := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Add /users"))
	task := func(state a2a.TaskState, history []*a2a.Message, status *a2a.Message) *a2a.Task {
		return &a2a.Task{ID: "t1", History: history, Status: a2a.TaskStatus{State: state, Message: status}}
	}
	agent := func(text string) *a2a.Message { return a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(text)) }
	onIt, q, done := agent("on it"), agent("Include deleted users?\nAlso: paging?"), agent("Done")
	submitted := task(a2a.TaskStateSubmitted, []*a2a.Message{req}, nil)
	working := task(a2a.TaskStateWorking, []*a2a.Message{req}, onIt)

	for _, c := range []struct {
		name       string
		prev, next *a2a.Task
		want       string
	}{
		{"accepted with a message", submitted, working, `toli: bob posted on task t1: "on it".`},
		{"accepted silently", submitted, task(a2a.TaskStateWorking, []*a2a.Message{req}, nil), ""},
		{"nothing new", working, working, ""},
		{"asked", working, task(a2a.TaskStateInputRequired, []*a2a.Message{req, onIt}, q),
			`toli: bob asked on task t1: "Include deleted users?". If you haven't already, answer with reply.`},
		{"completed after two messages", submitted, task(a2a.TaskStateCompleted, []*a2a.Message{req, onIt}, done),
			`toli: bob completed task t1: "Done". See get_delegated_task_updates. (2 new messages: get_delegated_task_updates)`},
		{"canceled", working, task(a2a.TaskStateCanceled, []*a2a.Message{req, onIt}, nil), `toli: task t1 on bob was canceled.`},
		{"rejected", submitted, task(a2a.TaskStateRejected, []*a2a.Message{req}, agent("Not my area")),
			`toli: bob rejected task t1: "Not my area".`},
	} {
		if got := delegatedNote("bob", c.prev, c.next); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}

	long := agent(strings.Repeat("x", 200))
	if got := delegatedNote("bob", submitted, task(a2a.TaskStateWorking, []*a2a.Message{req}, long)); !strings.Contains(got, strings.Repeat("x", 120)+"…") {
		t.Errorf("long message not shortened: %q", got)
	}
}
