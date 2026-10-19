package a2aserver

import (
	"context"
	"errors"
	"iter"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// executor is the a2asrv.AgentExecutor. Messages never reach it (handler
// records them itself), so only Cancel is used: a2asrv's CancelTask runs it
// and saves the result, retrying if the task changes meanwhile.
type executor struct{}

var _ a2asrv.AgentExecutor = executor{}

// Execute isn't used: handler.SendMessage replaces a2asrv's message handling.
func (executor) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(nil, errors.New("bug: messages are handled by a2aserver.handler, not the executor"))
	}
}

// Cancel handles a peer canceling its task.
func (executor) Cancel(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCanceled, nil), nil)
	}
}
