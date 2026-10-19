// Package a2atest provides an in-process A2A agent for tests, the way
// net/http/httptest provides an HTTP server.
package a2atest

import (
	"context"
	"iter"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// FakePeer is a remote A2A agent: an httptest server serving an Agent Card at
// /.well-known/agent-card.json and the JSON-RPC endpoint at /.
//
// A new task (or a reply on one) moves to working, then waits for the test to
// say what happens next via Next. A message with text "ping" is answered
// directly with "pong", with no task.
type FakePeer struct {
	URL string // base URL: where the card is fetched from

	next chan Step

	mu       sync.Mutex
	received []*a2a.Message
	canceled []a2a.TaskID
}

// Step is what a waiting task does next: move to State, with Text as the
// agent's status message (if non-empty).
type Step struct {
	State a2a.TaskState
	Text  string
}

// NewFakePeer starts a fake peer, stopped when the test ends.
func NewFakePeer(t testing.TB) *FakePeer {
	p := &FakePeer{next: make(chan Step, 16)}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	p.URL = srv.URL

	card := &a2a.AgentCard{
		Name:                "Fake peer",
		Description:         "a2atest.FakePeer",
		Version:             "1.0",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(srv.URL+"/", a2a.TransportProtocolJSONRPC)},
		DefaultInputModes:   []string{"text/plain"},
		DefaultOutputModes:  []string{"text/plain"},
		Skills:              []a2a.AgentSkill{},
	}
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(p)))

	// Close unblocks any task still waiting on Next before stopping the server.
	t.Cleanup(func() { close(p.next); srv.Close() })
	return p
}

// Next tells one waiting task what to do. Tasks take steps in the order
// they're queued.
func (p *FakePeer) Next(state a2a.TaskState, text string) {
	p.next <- Step{State: state, Text: text}
}

// Received returns every message the peer has been sent, in order.
func (p *FakePeer) Received() []*a2a.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*a2a.Message(nil), p.received...)
}

// Canceled returns the IDs of tasks the peer was asked to cancel, in order.
func (p *FakePeer) Canceled() []a2a.TaskID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]a2a.TaskID(nil), p.canceled...)
}

// Text returns a message's first text part, or "".
func Text(m *a2a.Message) string {
	if m == nil || len(m.Parts) == 0 {
		return ""
	}
	return m.Parts[0].Text()
}

// Execute implements a2asrv.AgentExecutor.
func (p *FakePeer) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		p.mu.Lock()
		p.received = append(p.received, ec.Message)
		p.mu.Unlock()

		if ec.StoredTask == nil && Text(ec.Message) == "ping" {
			yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("pong")), nil)
			return
		}
		if ec.StoredTask == nil {
			if !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
				return
			}
		}
		if !yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateWorking, nil), nil) {
			return
		}
		select {
		case <-ctx.Done():
		case s, ok := <-p.next:
			if !ok {
				return
			}
			var msg *a2a.Message
			if s.Text != "" {
				msg = a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(s.Text))
			}
			yield(a2a.NewStatusUpdateEvent(ec, s.State, msg), nil)
		}
	}
}

// Cancel implements a2asrv.AgentExecutor.
func (p *FakePeer) Cancel(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		p.mu.Lock()
		p.canceled = append(p.canceled, ec.TaskID)
		p.mu.Unlock()
		yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCanceled, nil), nil)
	}
}
