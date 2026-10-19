// Package taskclient is a stateful A2A client: it sends tasks to peers and
// keeps the latest state each peer reports for them, in a pluggable
// taskstore.Store.
//
// It never works out a task's state itself. The peer's server is the source
// of truth; this package only delivers requests, records what comes back
// (responses and polls), and serves it from the store. v1 learns about
// changes by polling only; push and streaming can be added later as more
// sources of reports. The one rule it
// adds: a newer report replaces an older one, and a final state is never
// replaced.
//
// It knows nothing about toli's agents, sessions, or allowlists. Ownership
// checks happen in the caller before it calls in here.
package taskclient

import (
	"context"
	"errors"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	"github.com/arjunvlama/toli/internal/taskclient/taskstore"
)

// ErrFinal: the task is already in a final state, so it can't be replied to
// or canceled. (Storage errors live in taskstore.)
var ErrFinal = errors.New("task is already finished")

// activeStates are the A2A states a task can still leave: the ones the
// poller keeps checking.
var activeStates = []a2a.TaskState{
	a2a.TaskStateSubmitted,
	a2a.TaskStateWorking,
	a2a.TaskStateInputRequired,
	a2a.TaskStateAuthRequired,
}

// PeerResolver returns a ready A2A client for a peer (built from its Agent
// Card and our credentials for it), keeping cards, URLs, and auth out of here.
type PeerResolver func(ctx context.Context, peerID int64) (*a2aclient.Client, error)

// Options configure a Client.
type Options struct {
	// PollEvery is how often Run checks every active task with the peer.
	PollEvery time.Duration
	// DefaultTimeLimit is a task's deadline when Send isn't given one.
	DefaultTimeLimit time.Duration
	// OnChange, if set, is called when the poller saves a change to a task
	// (a newer report from its peer, or its cancel at the deadline), with the
	// task before and after.
	OnChange func(prev, next *taskstore.Task)
}

// SendOptions are per-task settings for Send.
type SendOptions struct {
	TimeLimit time.Duration // 0 = Options.DefaultTimeLimit
}

// Client is the stateful A2A client. Safe for concurrent use.
type Client struct {
	store taskstore.Store
	peers PeerResolver
	opts  Options
}

func New(store taskstore.Store, peers PeerResolver, opts Options) *Client {
	if opts.PollEvery == 0 {
		opts.PollEvery = 3 * time.Second
	}
	if opts.DefaultTimeLimit == 0 {
		opts.DefaultTimeLimit = time.Hour
	}
	return &Client{store: store, peers: peers, opts: opts}
}
