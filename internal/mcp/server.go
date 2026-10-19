package mcp

import (
	"net/http"

	"github.com/arjunvlama/toli/internal/backend"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server serves every local agent; which one a call acts as comes from its
// token (see auth.go).
type Server struct {
	backend *backend.Backend
}

const instructions = `toli connects you with other people's coding agents (peers).
Delegating: call list_peers to see who you can delegate to, delegate to start a task on a peer, then
get_delegated_task_updates to follow it (if this session gets toli's notifications, you'll be told when it changes).
Group related tasks with start_session.
Working: at the start of a session, call list_accepted_tasks to resume your work and list_available_tasks
for new work. Take new work on with accept_task, or decline it with update_task(..., "rejected").
Then use update_task: "working" for progress, "input-required" to ask, "completed" to finish.`

// New builds the MCP server, registers every tool, and returns it as an
// http.Handler. Tools follow docs/mcp-api.md.
func New(b *backend.Backend) http.Handler {
	s := &Server{backend: b}
	srv := mcp.NewServer(&mcp.Implementation{Name: "toli", Version: "v0.0.1"}, &mcp.ServerOptions{Instructions: instructions})

	// Delegating.
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_peers",
		Description: "The peers you may delegate work to, with what each does and whether it's reachable.",
	}, s.listPeers)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "start_session",
		Description: "Start a session to group related tasks (e.g. \"users api\"). Pass its session_id to delegate.",
	}, s.startSession)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_session",
		Description: "Done with a session: removes it and its tasks, and cancels any still running on their peers.",
	}, s.deleteSession)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delegate",
		Description: "Start a task on a peer (see list_peers). Returns its task_id, state, and version; if the peer answers right away, the answer is included. Use get_delegated_task_updates to follow it.",
	}, s.delegate)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_delegated_task_updates",
		Description: "Where a task you delegated stands: its state, everything the peer said since your last message to it, and its outputs so far. With after_version (the version from the last result you saw), waits up to wait_seconds for something new; no need to if this session gets toli's notifications. For the whole conversation, use get_task_full.",
	}, s.getDelegatedTaskUpdates)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "reply",
		Description: "Message the peer on a task you delegated: answer its question (input-required) or tell it something while it works.",
	}, s.reply)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "cancel_task",
		Description: "Stop a task you delegated. Returns canceled, or the final state it had already reached.",
	}, s.cancelTask)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_delegated_tasks",
		Description: "Find the tasks you delegated again, newest first, optionally only a session's or only one state (e.g. input-required).",
	}, s.listDelegatedTasks)

	// Working.
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_available_tasks",
		Description: "New work peers sent you that you haven't accepted yet, newest first. With wait_seconds, waits for some if there's none.",
	}, s.listAvailableTasks)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "accept_task",
		Description: "Take on new work (see list_available_tasks). Returns the request and anything else the peer said. To decline instead, use update_task with rejected.",
	}, s.acceptTask)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_accepted_tasks",
		Description: "Work you've accepted, newest first: by default what's still in progress, with how many of the peer's messages you haven't responded to.",
	}, s.listAcceptedTasks)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_task",
		Description: "Everything you tell the peer about work you accepted: post progress (working), ask (input-required: all questions in one message; one open question at a time; with wait_seconds, waits for the answer), finish (completed, with artifacts), or give up (failed). Also declines new work (rejected).",
	}, s.updateTask)

	// Both.
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_task_full",
		Description: "Everything about a task, delegated or assigned to you: the whole conversation and all outputs. Use it to catch up, not to wait.",
	}, s.getTaskFull)

	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	return tokenFromPath(auth.RequireBearerToken(s.verifyToken, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(h))
}
