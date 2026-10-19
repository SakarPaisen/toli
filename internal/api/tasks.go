package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/backend"
	"github.com/arjunvlama/toli/internal/store"
	"github.com/arjunvlama/toli/internal/taskclient/taskstore"
)

// TaskInfo is one of an agent's tasks, as a list row: delegated (the agent
// sent it to the peer) or assigned (the peer sent it to the agent).
type TaskInfo struct {
	ID        string     `json:"id"`
	Peer      string     `json:"peer"`                 // the peer's local name; "" if just removed
	SessionID string     `json:"session_id,omitempty"` // delegated: "" for a standalone task; assigned: the A2A contextId
	State     string     `json:"state"`                // A2A state, e.g. "TASK_STATE_WORKING"
	Summary   string     `json:"summary"`              // first line of the original request
	Direct    bool       `json:"direct,omitempty"`     // delegated: the peer answered directly; no task on its side
	UpdatedAt time.Time  `json:"updated_at"`           // delegated: when we last saved the peer's report; assigned: when the task last changed
	Deadline  *time.Time `json:"deadline,omitempty"`   // delegated: when the daemon cancels it if unfinished
}

// TaskMessage is one message of a task's conversation.
type TaskMessage struct {
	FromPeer bool   `json:"from_peer"` // false: sent by our agent
	Text     string `json:"text"`
}

// TaskArtifact is one of a task's outputs (text only for now).
type TaskArtifact struct {
	Name string `json:"name,omitempty"`
	Text string `json:"text"`
}

// TaskDetail is a whole task: the list row plus its conversation and outputs.
type TaskDetail struct {
	TaskInfo
	Messages  []TaskMessage  `json:"messages"`
	Artifacts []TaskArtifact `json:"artifacts,omitempty"`
}

type ListTasksResponse struct {
	Tasks []TaskInfo `json:"tasks"`
}

// GET /v1/tasks?agent=<name>[&session=<id>][&state=<A2A state>]
func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	agent := q.Get("agent")
	if agent == "" {
		writeError(w, http.StatusBadRequest, "agent is required")
		return
	}
	var states []a2a.TaskState
	if st := q.Get("state"); st != "" {
		states = []a2a.TaskState{a2a.TaskState(st)}
	}

	tasks, err := s.backend.ListTasks(r.Context(), agent, q.Get("session"), states)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list tasks: "+err.Error())
		return
	}

	resp := ListTasksResponse{Tasks: make([]TaskInfo, 0, len(tasks))}
	for _, t := range tasks {
		resp.Tasks = append(resp.Tasks, toTaskInfo(t))
	}
	writeJSON(w, http.StatusOK, resp)
}

// GET /v1/tasks/{id}?agent=<name>
func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	agent, id, ok := taskParams(w, r)
	if !ok {
		return
	}

	task, err := s.backend.GetTask(r.Context(), agent, id, -1, 0)
	if errors.Is(err, taskstore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task "+string(id)+" not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get task: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toTaskDetail(task))
}

// POST /v1/tasks/{id}/cancel?agent=<name>
// Returns the task as it stands after the peer's answer: canceled, or the
// final state it had already reached.
func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	agent, id, ok := taskParams(w, r)
	if !ok {
		return
	}

	task, err := s.backend.CancelTask(r.Context(), agent, id)
	if errors.Is(err, taskstore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task "+string(id)+" not found")
		return
	}
	if err != nil {
		// The peer refused or couldn't be reached; nothing changed here.
		writeError(w, http.StatusBadGateway, "cancel task: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toTaskInfo(task))
}

// taskParams reads the agent query parameter and the task id from the path,
// writing a 400 if the agent is missing.
func taskParams(w http.ResponseWriter, r *http.Request) (string, a2a.TaskID, bool) {
	agent := r.URL.Query().Get("agent")
	if agent == "" {
		writeError(w, http.StatusBadRequest, "agent is required")
		return "", "", false
	}
	return agent, a2a.TaskID(r.PathValue("id")), true
}

func toTaskInfo(t *backend.Task) TaskInfo {
	info := TaskInfo{
		ID:        string(t.A2A.ID),
		Peer:      t.Peer,
		State:     string(t.A2A.Status.State),
		Direct:    t.Direct,
		UpdatedAt: t.SavedAt,
		Deadline:  &t.Deadline,
	}
	if t.ClientContext { // only a contextId we supplied is a session
		info.SessionID = t.A2A.ContextID
	}
	for _, m := range t.A2A.History {
		if m != nil && m.Role == a2a.MessageRoleUser {
			info.Summary, _, _ = strings.Cut(partsText(m.Parts), "\n")
			break
		}
	}
	return info
}

func toTaskDetail(t *backend.Task) TaskDetail {
	d := TaskDetail{TaskInfo: toTaskInfo(t)}
	// We delegated it, so the peer's messages are the agent role's.
	d.Messages, d.Artifacts = conversation(t.A2A, a2a.MessageRoleAgent)
	return d
}

// conversation is a task's messages (the history, plus the current status
// message if it isn't in it yet) and artifacts. peerRole is the role of the
// peer's messages.
func conversation(t *a2a.Task, peerRole a2a.MessageRole) ([]TaskMessage, []TaskArtifact) {
	msgs := []TaskMessage{}
	seen := map[string]bool{}
	add := func(m *a2a.Message) {
		if m == nil || seen[m.ID] {
			return
		}
		seen[m.ID] = true
		msgs = append(msgs, TaskMessage{FromPeer: m.Role == peerRole, Text: partsText(m.Parts)})
	}
	for _, m := range t.History {
		add(m)
	}
	add(t.Status.Message)

	var arts []TaskArtifact
	for _, a := range t.Artifacts {
		if a != nil {
			arts = append(arts, TaskArtifact{Name: a.Name, Text: partsText(a.Parts)})
		}
	}
	return msgs, arts
}

// partsText joins the text parts of a message or artifact.
func partsText(parts a2a.ContentParts) string {
	var texts []string
	for _, p := range parts {
		if p != nil {
			if t := p.Text(); t != "" {
				texts = append(texts, t)
			}
		}
	}
	return strings.Join(texts, "\n")
}

// GET /v1/assigned-tasks?agent=<name>[&state=<A2A state>]
func (s *Server) listAssignedTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	agent := q.Get("agent")
	if agent == "" {
		writeError(w, http.StatusBadRequest, "agent is required")
		return
	}
	var states []a2a.TaskState
	if st := q.Get("state"); st != "" {
		states = []a2a.TaskState{a2a.TaskState(st)}
	}

	tasks, err := s.backend.ListInboundTasks(r.Context(), agent, states, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list assigned tasks: "+err.Error())
		return
	}
	resp := ListTasksResponse{Tasks: make([]TaskInfo, 0, len(tasks))}
	for _, t := range tasks {
		resp.Tasks = append(resp.Tasks, toAssignedInfo(t))
	}
	writeJSON(w, http.StatusOK, resp)
}

// GET /v1/assigned-tasks/{id}?agent=<name>
func (s *Server) getAssignedTask(w http.ResponseWriter, r *http.Request) {
	agent, id, ok := taskParams(w, r)
	if !ok {
		return
	}
	task, err := s.backend.GetInboundTask(r.Context(), agent, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task "+string(id)+" not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get assigned task: "+err.Error())
		return
	}

	d := TaskDetail{TaskInfo: toAssignedInfo(task)}
	// The peer delegated it, so its messages are the user role's.
	d.Messages, d.Artifacts = conversation(task.Task, a2a.MessageRoleUser)
	writeJSON(w, http.StatusOK, d)
}

func toAssignedInfo(t *backend.InboundTask) TaskInfo {
	info := TaskInfo{
		ID:        string(t.Task.ID),
		Peer:      t.Peer,
		SessionID: t.Task.ContextID,
		State:     string(t.Task.Status.State),
		UpdatedAt: t.UpdatedAt,
	}
	for _, m := range t.Task.History {
		if m != nil && m.Role == a2a.MessageRoleUser {
			info.Summary, _, _ = strings.Cut(partsText(m.Parts), "\n")
			break
		}
	}
	return info
}
