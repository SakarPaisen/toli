package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arjunvlama/toli/internal/backend"
	"github.com/arjunvlama/toli/internal/store"
	"github.com/arjunvlama/toli/internal/taskclient"
	"github.com/arjunvlama/toli/internal/taskclient/taskstore"
)

// Task tools: thin wrappers over the backend's task methods, which do the
// ownership checks and return each task with its peer's name (backend.Task).
// Each handler fills its small output struct (see docs/mcp-api.md, "Task
// views"). A returned error becomes an isError tool result.

type delegateIn struct {
	Peer             string `json:"peer" jsonschema:"the peer's alias from list_peers"`
	Message          string `json:"message" jsonschema:"what you want the peer to do"`
	SessionID        string `json:"session_id,omitempty" jsonschema:"from start_session; omit for a standalone task"`
	TimeLimitMinutes int    `json:"time_limit_minutes,omitempty" jsonschema:"cancel the task if it isn't done by then (default 60)"`
}

type delegatedUpdatesIn struct {
	TaskID       string `json:"task_id"`
	AfterVersion *int64 `json:"after_version,omitempty" jsonschema:"the version from the last update you saw: wait until the task is newer"`
	WaitSeconds  int    `json:"wait_seconds,omitempty" jsonschema:"with after_version, how long to wait (default 60, max 120)"`
}

type replyIn struct {
	TaskID  string `json:"task_id"`
	Message string `json:"message" jsonschema:"your answer, e.g. to a question the peer asked"`
}

type taskIDIn struct {
	TaskID string `json:"task_id"`
}

type listDelegatedIn struct {
	SessionID string `json:"session_id,omitempty" jsonschema:"only this session's tasks"`
	State     string `json:"state,omitempty" jsonschema:"only tasks in this state, e.g. input-required"`
}

type message struct {
	From string `json:"from" jsonschema:"who said it: your name or the peer's"`
	Text string `json:"text"`
}

type artifact struct {
	Name string `json:"name,omitempty"`
	Text string `json:"text"`
}

// taskSummary is what actions (delegate, reply, cancel_task) return.
type taskSummary struct {
	TaskID  string   `json:"task_id"`
	State   string   `json:"state"`
	Version int64    `json:"version" jsonschema:"pass as after_version to get_delegated_task_updates to wait for the next change"`
	Answer  *message `json:"answer,omitempty" jsonschema:"set when the peer answered directly instead of working on a task"`
}

// taskUpdate is get_delegated_task_updates' result: what's new, small enough
// to poll.
type taskUpdate struct {
	TaskID    string     `json:"task_id"`
	Peer      string     `json:"peer"`
	SessionID string     `json:"session_id,omitempty"`
	State     string     `json:"state"`
	Messages  []string   `json:"messages" jsonschema:"everything the peer said since your last message to it, oldest first: progress notes, its open question, its final answer"`
	Artifacts []artifact `json:"artifacts,omitempty" jsonschema:"the task's outputs so far"`
	Version   int64      `json:"version" jsonschema:"pass as after_version to wait for the next change"`
	UpdatedAt time.Time  `json:"updated_at"`
	Deadline  time.Time  `json:"deadline"`
}

// taskFull is get_task_full's result: everything, for a task in either
// direction.
type taskFull struct {
	TaskID    string     `json:"task_id"`
	Direction string     `json:"direction" jsonschema:"delegated (you sent it to the peer) or assigned (the peer sent it to you)"`
	Peer      string     `json:"peer"`
	SessionID string     `json:"session_id,omitempty"`
	State     string     `json:"state"`
	Messages  []message  `json:"messages"`
	Artifacts []artifact `json:"artifacts,omitempty"`
	Direct    bool       `json:"direct,omitempty" jsonschema:"the peer answered directly; there's no task on its side"`
	Version   int64      `json:"version"`
	UpdatedAt time.Time  `json:"updated_at"`
	Deadline  *time.Time `json:"deadline,omitempty" jsonschema:"delegated tasks: when the daemon cancels it if unfinished"`
}

// taskListItem is one row of list_delegated_tasks.
type taskListItem struct {
	TaskID    string    `json:"task_id"`
	Peer      string    `json:"peer"`
	SessionID string    `json:"session_id,omitempty"`
	State     string    `json:"state"`
	Summary   string    `json:"summary" jsonschema:"first line of the original request"`
	UpdatedAt time.Time `json:"updated_at"`
}

type listDelegatedOut struct {
	Tasks []taskListItem `json:"tasks" jsonschema:"newest first"`
}

const (
	defaultWait = 60 // seconds
	maxWait     = 120
)

// delegate: a2a SendMessage to the peer.
func (s *Server) delegate(ctx context.Context, req *sdkmcp.CallToolRequest, in delegateIn) (*sdkmcp.CallToolResult, taskSummary, error) {
	if in.TimeLimitMinutes < 0 {
		return nil, taskSummary{}, errors.New("time_limit_minutes must be positive")
	}
	task, err := s.backend.Delegate(ctx, agentName(req), in.Peer, in.Message, in.SessionID,
		time.Duration(in.TimeLimitMinutes)*time.Minute)
	if err != nil {
		return nil, taskSummary{}, err
	}
	return nil, toSummary(task, nil), nil
}

// getDelegatedTaskUpdates: no after_version returns at once (backend after = -1); with
// it, waits up to wait_seconds (default 60, clamped to 120).
func (s *Server) getDelegatedTaskUpdates(ctx context.Context, req *sdkmcp.CallToolRequest, in delegatedUpdatesIn) (*sdkmcp.CallToolResult, taskUpdate, error) {
	after := int64(-1)
	if in.AfterVersion != nil {
		after = *in.AfterVersion
	}
	wait := in.WaitSeconds
	if wait <= 0 {
		wait = defaultWait
	}
	wait = min(wait, maxWait)

	task, err := s.backend.GetTask(ctx, agentName(req), a2a.TaskID(in.TaskID), after, time.Duration(wait)*time.Second)
	if err != nil {
		return nil, taskUpdate{}, taskErr(in.TaskID, err)
	}

	out := taskUpdate{
		TaskID:    string(task.A2A.ID),
		Peer:      task.Peer,
		SessionID: sessionID(task),
		State:     backend.StateName(task.A2A.Status.State),
		Version:   task.Version,
		UpdatedAt: task.SavedAt,
		Deadline:  task.Deadline,
	}
	out.Messages = []string{}
	for _, m := range afterLast(conversation(task.A2A), a2a.MessageRoleUser) {
		if m.Role == a2a.MessageRoleAgent {
			out.Messages = append(out.Messages, partsText(m.Parts))
		}
	}
	out.Artifacts = artifacts(task.A2A)
	return nil, out, nil
}

// getTaskFull: everything, at once. The id is looked up among the agent's
// delegated tasks, then its inbound ones.
func (s *Server) getTaskFull(ctx context.Context, req *sdkmcp.CallToolRequest, in taskIDIn) (*sdkmcp.CallToolResult, taskFull, error) {
	agent := agentName(req)
	task, err := s.backend.GetTask(ctx, agent, a2a.TaskID(in.TaskID), -1, 0)
	if err == nil {
		out := fullView(task.A2A, "delegated", task.Peer, agent, task.Version, task.SavedAt)
		out.SessionID = sessionID(task)
		out.Direct = task.Direct
		out.Deadline = &task.Deadline
		return nil, out, nil
	}
	if !errors.Is(err, taskstore.ErrNotFound) {
		return nil, taskFull{}, err
	}

	inbound, err := s.backend.GetInboundTask(ctx, agent, a2a.TaskID(in.TaskID))
	if err != nil {
		return nil, taskFull{}, taskErr(in.TaskID, err)
	}
	out := fullView(inbound.Task, "assigned", inbound.Peer, agent, inbound.Version, inbound.UpdatedAt)
	out.SessionID = inbound.Task.ContextID
	return nil, out, nil
}

// fullView is the part of get_task_full that's the same in both directions.
// Messages are labeled with their sender's name: the user role is whoever
// delegated the task, the agent role whoever works on it.
func fullView(t *a2a.Task, direction, peer, agent string, version int64, updated time.Time) taskFull {
	delegator, worker := agent, peer
	if direction == "assigned" {
		delegator, worker = peer, agent
	}
	out := taskFull{
		TaskID:    string(t.ID),
		Direction: direction,
		Peer:      peer,
		State:     backend.StateName(t.Status.State),
		Messages:  []message{},
		Artifacts: artifacts(t),
		Version:   version,
		UpdatedAt: updated,
	}
	for _, m := range conversation(t) {
		from := delegator
		if m.Role == a2a.MessageRoleAgent {
			from = worker
		}
		out.Messages = append(out.Messages, message{From: from, Text: partsText(m.Parts)})
	}
	return out
}

// reply: a2a SendMessage on the task (e.g. answering input-required).
func (s *Server) reply(ctx context.Context, req *sdkmcp.CallToolRequest, in replyIn) (*sdkmcp.CallToolResult, taskSummary, error) {
	task, answer, err := s.backend.Reply(ctx, agentName(req), a2a.TaskID(in.TaskID), in.Message)
	if errors.Is(err, taskclient.ErrFinal) {
		return nil, taskSummary{}, fmt.Errorf("task %s is already finished; use get_delegated_task_updates for its result", in.TaskID)
	}
	if err != nil {
		return nil, taskSummary{}, taskErr(in.TaskID, err)
	}
	return nil, toSummary(task, answer), nil
}

// cancelTask: a2a CancelTask. A finished task comes back in its final state,
// not as an error.
func (s *Server) cancelTask(ctx context.Context, req *sdkmcp.CallToolRequest, in taskIDIn) (*sdkmcp.CallToolResult, taskSummary, error) {
	task, err := s.backend.CancelTask(ctx, agentName(req), a2a.TaskID(in.TaskID))
	if err != nil {
		return nil, taskSummary{}, taskErr(in.TaskID, err)
	}
	return nil, toSummary(task, nil), nil
}

// listDelegatedTasks: the agent's delegated tasks, optionally one session's and/or one state's.
func (s *Server) listDelegatedTasks(ctx context.Context, req *sdkmcp.CallToolRequest, in listDelegatedIn) (*sdkmcp.CallToolResult, listDelegatedOut, error) {
	var states []a2a.TaskState
	if in.State != "" {
		st, err := parseState(in.State)
		if err != nil {
			return nil, listDelegatedOut{}, err
		}
		states = []a2a.TaskState{st}
	}

	tasks, err := s.backend.ListTasks(ctx, agentName(req), in.SessionID, states)
	if err != nil {
		return nil, listDelegatedOut{}, err
	}

	out := listDelegatedOut{Tasks: make([]taskListItem, 0, len(tasks))}
	for _, t := range tasks {
		out.Tasks = append(out.Tasks, taskListItem{
			TaskID:    string(t.A2A.ID),
			Peer:      t.Peer,
			SessionID: sessionID(t),
			State:     backend.StateName(t.A2A.Status.State),
			Summary:   firstRequestLine(t.A2A),
			UpdatedAt: t.SavedAt,
		})
	}
	return nil, out, nil
}

// taskErr turns a task lookup's not-found into a message the agent can act on.
func taskErr(id string, err error) error {
	if errors.Is(err, taskstore.ErrNotFound) || errors.Is(err, store.ErrNotFound) || errors.Is(err, a2a.ErrTaskNotFound) {
		return fmt.Errorf("task %s not found", id)
	}
	return err
}

// toSummary is an action's result. answer is the peer's direct reply, if any;
// a task that is itself a direct answer carries it as its status message.
func toSummary(t *backend.Task, answer *a2a.Message) taskSummary {
	out := taskSummary{TaskID: string(t.A2A.ID), State: backend.StateName(t.A2A.Status.State), Version: t.Version}
	if answer == nil && t.Direct {
		answer = t.A2A.Status.Message
	}
	if answer != nil {
		out.Answer = &message{From: t.Peer, Text: partsText(answer.Parts)}
	}
	return out
}

// sessionID is the task's session, or "" for a standalone task: only a
// contextId we supplied (ClientContext) is a session.
func sessionID(t *backend.Task) string {
	if t.ClientContext {
		return t.A2A.ContextID
	}
	return ""
}

// conversation is the task's history plus its current status message, if the
// history doesn't already include it.
func conversation(t *a2a.Task) []*a2a.Message {
	var msgs []*a2a.Message
	seen := map[string]bool{}
	for _, m := range t.History {
		if m != nil {
			msgs = append(msgs, m)
			seen[m.ID] = true
		}
	}
	if m := t.Status.Message; m != nil && !seen[m.ID] {
		msgs = append(msgs, m)
	}
	return msgs
}

// afterLast is the part of msgs after the last message with role (all of them
// if there's none).
func afterLast(msgs []*a2a.Message, role a2a.MessageRole) []*a2a.Message {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == role {
			return msgs[i+1:]
		}
	}
	return msgs
}

func artifacts(t *a2a.Task) []artifact {
	var out []artifact
	for _, a := range t.Artifacts {
		if a != nil {
			out = append(out, artifact{Name: a.Name, Text: partsText(a.Parts)})
		}
	}
	return out
}

// request is the text of the task's first message from its delegator.
func request(t *a2a.Task) string {
	for _, m := range t.History {
		if m != nil && m.Role == a2a.MessageRoleUser {
			return partsText(m.Parts)
		}
	}
	return ""
}

// firstRequestLine is the first line of request.
func firstRequestLine(t *a2a.Task) string {
	line, _, _ := strings.Cut(request(t), "\n")
	return line
}

var knownStates = []a2a.TaskState{
	a2a.TaskStateSubmitted, a2a.TaskStateWorking, a2a.TaskStateInputRequired,
	a2a.TaskStateCompleted, a2a.TaskStateFailed, a2a.TaskStateCanceled, a2a.TaskStateRejected,
}

// parseState is stateName's inverse, for the list tools' state filters.
func parseState(name string) (a2a.TaskState, error) {
	var names []string
	for _, s := range knownStates {
		if backend.StateName(s) == name {
			return s, nil
		}
		names = append(names, backend.StateName(s))
	}
	return "", fmt.Errorf("unknown state %q; use one of: %s", name, strings.Join(names, ", "))
}

// partsText joins a message's or artifact's text parts (v1 is text only;
// other parts are dropped).
func partsText(parts a2a.ContentParts) string {
	var texts []string
	for _, p := range parts {
		if p == nil {
			continue
		}
		if t := p.Text(); t != "" {
			texts = append(texts, t)
		}
	}
	return strings.Join(texts, "\n")
}
