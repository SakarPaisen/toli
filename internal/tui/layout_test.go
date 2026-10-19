package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/arjunvlama/toli/internal/api"
)

// screen feeds msgs to a fresh model and returns what it draws, as plain text.
func screen(t *testing.T, w, h int, msgs ...tea.Msg) string {
	t.Helper()
	var m tea.Model = New(nil, "127.0.0.1:7420")
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	for _, msg := range msgs {
		m, _ = m.Update(msg)
	}
	return ansi.Strip(m.(Model).render())
}

func key(r rune) tea.KeyPressMsg { return tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}) }

func TestLayout(t *testing.T) {
	now := time.Now()
	deadline := now.Add(time.Hour)
	agents := agentsLoadedMsg{agents: []api.AgentInfo{
		{Name: "backend", Description: "Owns the API", Skills: []api.Skill{{Name: "Write SQL migrations"}},
			ShareURL: "http://toli.example/a2a/backend/.well-known/agent-card.json", Harness: "claude-code"},
		{Name: "frontend", Disabled: true},
	}}
	notConnected := false
	peers := peersLoadedMsg{agent: "backend", peers: []api.PeerInfo{{ID: 1, Name: "bob", CardName: "Bob's agent", LastSeen: &now, Connected: &notConnected,
		Skills: []api.Skill{{Name: "Review schemas"}}}}}
	tasks := tasksLoadedMsg{agent: "backend", tasks: []api.TaskInfo{
		{ID: "t1", Peer: "bob", State: "TASK_STATE_INPUT_REQUIRED", Summary: "Add a /users endpoint", UpdatedAt: now, Deadline: &deadline},
	}}
	assigned := tasksLoadedMsg{agent: "backend", tab: tabAssigned, tasks: []api.TaskInfo{
		{ID: "a1", Peer: "carol", State: "TASK_STATE_SUBMITTED", Summary: "Review the schema", UpdatedAt: now},
	}}
	assignedDetail := taskLoadedMsg{agent: "backend", tab: tabAssigned, task: api.TaskDetail{TaskInfo: assigned.tasks[0], Messages: []api.TaskMessage{
		{FromPeer: true, Text: "Review the schema"},
	}}}
	detail := taskLoadedMsg{agent: "backend", task: api.TaskDetail{TaskInfo: tasks.tasks[0], Messages: []api.TaskMessage{
		{Text: "Add a /users endpoint"}, {FromPeer: true, Text: "Paginated?"},
	}}}

	added := peerAddedMsg{resp: api.AddPeerResponse{Peer: api.PeerInfo{ID: 2, Name: "carol", CardName: "Carol's agent"}, Token: "tok-for-carol"}}
	enter := tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})

	for _, c := range []struct {
		name string
		w    int
		msgs []tea.Msg
		want []string
	}{
		{"empty", 110, nil, []string{"Agents", "NAME", "DISABLED", "No agents. Press a"}},
		{"agent detail", 110, []tea.Msg{agents, peers, tasks}, []string{
			"description", "Owns the API", "share_url", "agent-card.json", "HARNESS", "claude-code", "skills", "Write SQL migrations", "true", "Peers", "bob",
			"Delegated", "Assigned", "ID", "STATE", "INPUT_REQUIRED"}},
		{"assigned tab", 110, []tea.Msg{agents, peers, tasks, assigned, key('3'), key(']'), assignedDetail}, []string{
			"Assigned (1 new)", "carol", "SUBMITTED", "Review the schema", "[ ] tabs"}},
		{"peer detail", 110, []tea.Msg{agents, peers, tasks, key('2')}, []string{"card_name", "Bob's agent", "last_seen", "Review schemas",
			"1 not connected", "CONNECTED", "has_token", "No token for this peer: cannot delegate tasks to it.", "t token"}},
		{"add peer 1/3", 110, []tea.Msg{agents, peers, tasks, key('2'), key('a')}, []string{"Add peer to backend (1/3)", "Name", "Card URL"}},
		{"add peer 2/3", 110, []tea.Msg{agents, peers, tasks, key('2'), key('a'), added}, []string{
			"Add peer to backend (2/3)", "card_name", "Carol's agent", "Give this token to the peer for access", "tok-for-carol", "Shown once"}},
		{"add peer 3/3", 110, []tea.Msg{agents, peers, tasks, key('2'), key('a'), added, enter}, []string{
			"Add peer to backend (3/3)", "Enter the token the peer gave you for access", "esc later"}},
		{"paste", 110, []tea.Msg{agents, peers, tasks, key('2'), key('t'), tea.PasteMsg{Content: "pasted-token"}}, []string{"pasted-token"}},
		{"token popup", 110, []tea.Msg{agents, peers, tasks, key('2'), key('t')}, []string{"Token for bob", "Enter the token the peer gave you for access"}},
		{"task detail", 110, []tea.Msg{agents, peers, tasks, key('3'), detail}, []string{
			"TASK_STATE_INPUT_REQUIRED", "session_id", "messages", "backend", "Paginated?", "artifacts", "c cancel"}},
		{"cancel popup", 110, []tea.Msg{agents, peers, tasks, key('3'), key('c')}, []string{"Cancel this task on bob?", "y yes"}},
		{"narrow", 80, []tea.Msg{agents, peers, tasks, key('3'), detail}, []string{"Delegated", "messages", "more"}},
		{"peer new token", 110, []tea.Msg{agents, peers, tasks, key('2'), peerRotatedMsg{peer: peers.peers[0], token: "tok-new"}}, []string{
			"New token for bob", "Give this token to the peer for access", "tok-new", "enter close"}},
		{"peer keys", 110, []tea.Msg{agents, peers, tasks, key('2')}, []string{"n new token"}},
		{"agent created", 110, []tea.Msg{agents, agentAddedMsg{resp: api.AddAgentResponse{Name: "reviewer", Token: "tok123"}}}, []string{
			"Created reviewer", "http://127.0.0.1:7420/mcp/tok123", "toli connect <harness> <MCP URL>", "harness: claude-code, codex, openclaw, opencode, pi",
			"Other agents: MCP URL above, see docs"}},
	} {
		got := screen(t, c.w, 32, c.msgs...)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: screen lacks %q:\n%s", c.name, w, got)
			}
		}
	}
}

// Rendering must not panic at any size, popups included.
func TestTinySizes(t *testing.T) {
	agents := agentsLoadedMsg{agents: []api.AgentInfo{{Name: "a-very-long-agent-name-that-will-not-fit", Description: "x"}}}
	for _, sz := range [][2]int{{1, 1}, {20, 5}, {40, 10}, {89, 15}, {90, 15}, {200, 60}} {
		screen(t, sz[0], sz[1], agents)
		screen(t, sz[0], sz[1], agents, key('a'))
	}
}

// Page and top/bottom keys scroll a long conversation, from the task list.
func TestScrollDetail(t *testing.T) {
	agents := agentsLoadedMsg{agents: []api.AgentInfo{{Name: "backend"}}}
	tasks := tasksLoadedMsg{agent: "backend", tasks: []api.TaskInfo{{ID: "t1", Peer: "bob", State: "TASK_STATE_WORKING"}}}
	var msgs []api.TaskMessage
	for i := range 40 {
		msgs = append(msgs, api.TaskMessage{FromPeer: i%2 == 1, Text: fmt.Sprintf("message number %d", i)})
	}
	detail := taskLoadedMsg{agent: "backend", task: api.TaskDetail{TaskInfo: tasks.tasks[0], Messages: msgs}}

	base := []tea.Msg{agents, tasks, key('3'), detail}
	if s := screen(t, 110, 32, base...); strings.Contains(s, "message number 39") {
		t.Fatal("the last message is visible before scrolling")
	}
	if s := screen(t, 110, 32, append(base, key('G'))...); !strings.Contains(s, "message number 39") {
		t.Fatalf("G didn't scroll to the bottom:\n%s", s)
	}
	if s := screen(t, 110, 32, append(base, key('G'), key('g'))...); !strings.Contains(s, "message number 0") {
		t.Fatalf("g didn't scroll back to the top:\n%s", s)
	}
	pgdn := tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown})
	presses := append([]tea.Msg{}, base...)
	for range 20 {
		presses = append(presses, pgdn)
	}
	if s := screen(t, 110, 32, presses...); !strings.Contains(s, "message number 39") {
		t.Fatalf("pgdown didn't reach the bottom:\n%s", s)
	}
}
