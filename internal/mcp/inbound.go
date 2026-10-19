package mcp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arjunvlama/toli/internal/backend"
)

// Working tools: tasks peers sent this agent.

type listAvailableIn struct {
	WaitSeconds int `json:"wait_seconds,omitempty" jsonschema:"if there's no new work, wait up to this long for some (max 120)"`
}

type listAcceptedIn struct {
	State string `json:"state,omitempty" jsonschema:"only tasks in this state, e.g. completed; default: working and input-required"`
}

type acceptTaskIn struct {
	TaskID  string `json:"task_id"`
	Message string `json:"message,omitempty" jsonschema:"optional note to the peer, e.g. your plan"`
}

type acceptTaskOut struct {
	TaskID   string   `json:"task_id"`
	State    string   `json:"state"`
	Version  int64    `json:"version"`
	Request  string   `json:"request" jsonschema:"what the peer asked for"`
	Messages []string `json:"messages" jsonschema:"anything else the peer said since, oldest first"`
}

type inboundItem struct {
	TaskID             string    `json:"task_id"`
	Peer               string    `json:"peer" jsonschema:"who sent it"`
	SessionID          string    `json:"session_id" jsonschema:"tasks with the same session_id belong together"`
	Request            string    `json:"request" jsonschema:"what the peer asked for"`
	State              string    `json:"state"`
	UnansweredMessages int       `json:"unanswered_messages" jsonschema:"the peer's messages since your last message on it"`
	Version            int64     `json:"version"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type listInboundOut struct {
	Tasks []inboundItem `json:"tasks" jsonschema:"newest first"`
}

type updateTaskIn struct {
	TaskID      string     `json:"task_id"`
	State       string     `json:"state" jsonschema:"working (post progress, carry on), input-required (ask), completed, failed, or rejected (decline new work)"`
	Message     string     `json:"message,omitempty" jsonschema:"to the peer: progress, your question (all of it in one message), your summary, or why it failed or you decline; required for input-required and rejected"`
	Artifacts   []artifact `json:"artifacts,omitempty" jsonschema:"outputs, each with a name and text; a name already sent replaces that output"`
	WaitSeconds int        `json:"wait_seconds,omitempty" jsonschema:"with input-required: wait up to this long (max 120) for the answer"`
}

// inProgress is list_accepted_tasks' default filter.
var inProgress = []a2a.TaskState{a2a.TaskStateWorking, a2a.TaskStateInputRequired}

func (s *Server) listAvailableTasks(ctx context.Context, req *sdkmcp.CallToolRequest, in listAvailableIn) (*sdkmcp.CallToolResult, listInboundOut, error) {
	wait := time.Duration(min(max(in.WaitSeconds, 0), maxWait)) * time.Second
	return s.listInbound(ctx, agentName(req), []a2a.TaskState{a2a.TaskStateSubmitted}, wait)
}

func (s *Server) listAcceptedTasks(ctx context.Context, req *sdkmcp.CallToolRequest, in listAcceptedIn) (*sdkmcp.CallToolResult, listInboundOut, error) {
	states := inProgress
	if in.State != "" {
		st, err := parseState(in.State)
		if err != nil {
			return nil, listInboundOut{}, err
		}
		states = []a2a.TaskState{st}
	}
	return s.listInbound(ctx, agentName(req), states, 0)
}

func (s *Server) listInbound(ctx context.Context, agent string, states []a2a.TaskState, wait time.Duration) (*sdkmcp.CallToolResult, listInboundOut, error) {
	tasks, err := s.backend.ListInboundTasks(ctx, agent, states, wait)
	if err != nil {
		return nil, listInboundOut{}, err
	}
	out := listInboundOut{Tasks: make([]inboundItem, 0, len(tasks))}
	for _, t := range tasks {
		out.Tasks = append(out.Tasks, inboundItem{
			TaskID:             string(t.Task.ID),
			Peer:               t.Peer,
			SessionID:          t.Task.ContextID,
			Request:            request(t.Task),
			State:              backend.StateName(t.Task.Status.State),
			UnansweredMessages: unanswered(t.Task),
			Version:            t.Version,
			UpdatedAt:          t.UpdatedAt,
		})
	}
	return nil, out, nil
}

func (s *Server) acceptTask(ctx context.Context, req *sdkmcp.CallToolRequest, in acceptTaskIn) (*sdkmcp.CallToolResult, acceptTaskOut, error) {
	task, err := s.backend.AcceptInboundTask(ctx, agentName(req), a2a.TaskID(in.TaskID), in.Message)
	if err != nil {
		return nil, acceptTaskOut{}, updateErr(in.TaskID, err)
	}
	out := acceptTaskOut{
		TaskID:   string(task.Task.ID),
		State:    backend.StateName(task.Task.Status.State),
		Version:  task.Version,
		Request:  request(task.Task),
		Messages: []string{},
	}
	// The peer's messages other than the request.
	seenRequest := false
	for _, m := range conversation(task.Task) {
		if m.Role != a2a.MessageRoleUser {
			continue
		}
		if !seenRequest {
			seenRequest = true
			continue
		}
		out.Messages = append(out.Messages, partsText(m.Parts))
	}
	return nil, out, nil
}

func (s *Server) updateTask(ctx context.Context, req *sdkmcp.CallToolRequest, in updateTaskIn) (*sdkmcp.CallToolResult, taskSummary, error) {
	st, err := parseState(in.State)
	if err != nil {
		return nil, taskSummary{}, err
	}
	u := backend.InboundUpdate{State: st, Message: in.Message}
	for _, a := range in.Artifacts {
		u.Artifacts = append(u.Artifacts, backend.Artifact{Name: a.Name, Text: a.Text})
	}
	wait := time.Duration(min(max(in.WaitSeconds, 0), maxWait)) * time.Second

	task, err := s.backend.UpdateInboundTask(ctx, agentName(req), a2a.TaskID(in.TaskID), u, wait)
	if err != nil {
		return nil, taskSummary{}, updateErr(in.TaskID, err)
	}

	out := taskSummary{TaskID: string(task.Task.ID), State: backend.StateName(task.Task.Status.State), Version: task.Version}
	if wait > 0 && st == a2a.TaskStateInputRequired {
		// The peer's messages after our question are its answer.
		var texts []string
		for _, m := range afterLast(conversation(task.Task), a2a.MessageRoleAgent) {
			texts = append(texts, partsText(m.Parts))
		}
		if len(texts) > 0 {
			out.Answer = &message{From: task.Peer, Text: strings.Join(texts, "\n\n")}
		}
	}
	return nil, out, nil
}

// updateErr is an accept or update error as the agent should read it.
func updateErr(id string, err error) error {
	if errors.Is(err, backend.ErrInvalidUpdate) {
		return errors.New(strings.TrimSuffix(err.Error(), ": "+backend.ErrInvalidUpdate.Error()))
	}
	return taskErr(id, err)
}

// unanswered counts the delegator's messages after the worker's last one,
// not counting the original request.
func unanswered(t *a2a.Task) int {
	conv := conversation(t)
	rest := afterLast(conv, a2a.MessageRoleAgent)
	if len(rest) == len(conv) && len(rest) > 0 {
		rest = rest[1:] // no worker message yet: the first is the request
	}
	return len(rest)
}
