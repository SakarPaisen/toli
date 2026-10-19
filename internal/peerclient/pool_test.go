package peerclient_test

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2acompat/a2av0"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/arjunvlama/toli/internal/peerclient"
)

// TestA2A03Peer checks toli can work with an agent on A2A 0.3: read its
// card, then send, poll and cancel over 0.3, with our token on every call.
func TestA2A03Peer(t *testing.T) {
	ctx := context.Background()
	var (
		mu   sync.Mutex
		auth []string // the Authorization header of each call the agent got
	)
	rpc := a2av0.NewJSONRPCHandler(a2asrv.NewHandler(heldTask{}))
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/agent-card.json" {
			// A 0.3 card: one url and a preferredTransport, no supportedInterfaces.
			fmt.Fprintf(w, `{"name": "old", "description": "An agent on A2A 0.3", "url": %q,
				"preferredTransport": "JSONRPC", "protocolVersion": "0.3.0", "version": "1",
				"capabilities": {}, "defaultInputModes": ["text/plain"],
				"defaultOutputModes": ["text/plain"], "skills": []}`, srv.URL+"/")
			return
		}
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		rpc.ServeHTTP(w, r)
	}))
	defer srv.Close()

	card, err := peerclient.FetchCard(ctx, srv.URL+"/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("FetchCard: %v", err)
	}
	if ifs := card.SupportedInterfaces; len(ifs) != 1 || ifs[0].URL != srv.URL+"/" || ifs[0].ProtocolVersion != "0.3.0" {
		t.Fatalf("card interfaces = %+v, want one: %s/, 0.3.0", ifs, srv.URL)
	}

	client, err := peerclient.NewClient(ctx, card, "secret")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	res, err := client.SendMessage(ctx, &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Long job")),
		Config:  &a2a.SendMessageConfig{ReturnImmediately: true},
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	task, ok := res.(*a2a.Task)
	if !ok {
		t.Fatalf("SendMessage = %T, want a task", res)
	}
	// The agent moves the task to working just after answering.
	for deadline := time.Now().Add(5 * time.Second); ; {
		got, err := client.GetTask(ctx, &a2a.GetTaskRequest{ID: task.ID})
		if err == nil && got.Status.State == a2a.TaskStateWorking {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GetTask = %v, %v; want working", got, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	canceled, err := client.CancelTask(ctx, &a2a.CancelTaskRequest{ID: task.ID})
	if err != nil || canceled.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("CancelTask = %v, %v; want canceled", canceled, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(auth) < 3 {
		t.Fatalf("the agent got %d calls, want 3", len(auth))
	}
	for i, a := range auth {
		if a != "Bearer secret" {
			t.Errorf("call %d: Authorization = %q, want our token", i+1, a)
		}
	}
}

// heldTask is an agent that starts a task and keeps it working until it's
// canceled.
type heldTask struct{}

func (heldTask) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
			yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateWorking, nil), nil)
		}
	}
}

func (heldTask) Cancel(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCanceled, nil), nil)
	}
}
