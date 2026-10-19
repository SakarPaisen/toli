// Package tui is toli's terminal UI. It's a client of the daemon's /v1/ API,
// like any other: everything it does is an api.Client call.
//
// Layout (after lazydocker): a column of panels on the left (Agents, then the
// selected agent's Peers and Tasks) and a detail pane on the right showing
// whatever is selected in the focused panel. Forms, tokens, and confirmations
// open as popups over it.
package tui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/arjunvlama/toli/internal/api"
)

type panel int

const (
	panelAgents panel = iota
	panelPeers
	panelTasks
	panelDetail // the right pane, focused to scroll it
)

// taskTab is which of the agent's tasks the Tasks panel shows.
type taskTab int

const (
	tabDelegated taskTab = iota // the agent sent them to peers
	tabAssigned                 // peers sent them to the agent
)

type popup int

const (
	popupNone         popup = iota
	popupAddAgent           // form: name, description
	popupAddPeer            // add-peer step 1: name, card URL
	popupToken              // a new or rotated agent token, shown once
	popupPeerToken          // add-peer step 2: the token we issued the new peer, shown once
	popupConfirm            // y/n before something destructive
	popupSetToken           // add-peer step 3, or t on a peer: the token the peer issued us
	popupEditAgent          // the agent's description and skills
	popupEditSkill          // one skill of the agent being edited
	popupNotify             // n on an agent: where its notifications are pushed
	popupNotifySecret       // a new webhook's secret, shown once
)

// Model is the whole UI state. bubbletea calls Update with each message
// (key press, API result, ...) and View after every Update.
type Model struct {
	client  *api.Client
	mcpAddr string // for the `mcp add` line

	width, height int   // terminal size, from tea.WindowSizeMsg
	th            theme // styles; rebuilt once the terminal reports light/dark
	err           error // last error, shown above the help line; cleared on the next action

	focus    panel
	detailOf panel // which list the detail pane shows: the last focused one

	agents   []api.AgentInfo
	agentCur int
	peers    []api.PeerInfo // the selected agent's
	peerCur  int
	tab      taskTab
	tasks    []api.TaskInfo  // the selected agent's delegated tasks, newest first
	assigned []api.TaskInfo  // the selected agent's assigned tasks, newest first
	taskCur  int             // in the current tab's list
	task     *api.TaskDetail // the selected task in full, once loaded
	scroll   int             // detail pane scroll offset

	popup       popup
	confirmText string
	confirmCmd  tea.Cmd

	// popupAddAgent
	name        textinput.Model
	description textarea.Model
	// popupAddPeer
	peerInputs []textinput.Model
	peerFocus  int
	// popupSetToken
	theirToken textinput.Model
	tokenPeer  api.PeerInfo // whose token it is
	wizard     bool         // reached through add peer (step 3), not t
	// popupToken, popupPeerToken
	created   api.AddAgentResponse
	rotated   bool // created or addedPeer holds a rotated token, not a new agent or peer
	addedPeer api.AddPeerResponse
	copied    bool

	// popupEditAgent, popupEditSkill
	form agentForm
	// popupNotify, popupNotifySecret
	notifyInput  textinput.Model
	notifySecret string
}

func New(c *api.Client, mcpAddr string) Model {
	m := Model{client: c, mcpAddr: mcpAddr, th: newTheme(true)}
	m.resetAgentForm()
	m.resetPeerForm()
	m.theirToken = newInput("token", 50, m.th)
	return m
}

// Init loads the agents, starts the periodic refresh, and asks the terminal
// for its background color so the theme can match light or dark.
func (m Model) Init() tea.Cmd {
	return tea.Batch(loadAgents(m.client), tick(), tea.RequestBackgroundColor)
}

func (m *Model) resetAgentForm() {
	m.name = newInput("my_agent", 40, m.th)
	m.description = newArea("what this agent works on", 50, m.th)
}

func (m *Model) resetPeerForm() {
	m.peerInputs = nil
	for _, ph := range []string{"bob", "http://bob.example:7422/a2a/agent/.well-known/agent-card.json"} {
		m.peerInputs = append(m.peerInputs, newInput(ph, 50, m.th))
	}
	m.peerFocus = 0
}

// newArea is a text field for descriptions: it wraps, grows to 4 lines, and
// takes up to 300 characters. Enter isn't a newline: forms use it to submit.
func newArea(placeholder string, width int, th theme) textarea.Model {
	a := textarea.New()
	a.Placeholder = placeholder
	a.ShowLineNumbers = false
	a.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "› "
		}
		return "  "
	})
	a.DynamicHeight, a.MinHeight, a.MaxHeight = true, 1, 4
	a.CharLimit = 300
	a.KeyMap.InsertNewline.SetEnabled(false)
	a.SetStyles(th.area)
	a.SetWidth(width)
	return a
}

func newInput(placeholder string, width int, th theme) textinput.Model {
	in := textinput.New()
	in.Placeholder = placeholder
	in.Prompt = "› "
	in.SetWidth(width) // without a width, only the placeholder's first letter shows
	in.SetStyles(th.input)
	return in
}

func (m *Model) setTabTasks(tab taskTab, tasks []api.TaskInfo) {
	if tab == tabAssigned {
		m.assigned = tasks
	} else {
		m.tasks = tasks
	}
}

// The selected items, if any.

func (m Model) selAgent() (api.AgentInfo, bool) {
	if m.agentCur < len(m.agents) {
		return m.agents[m.agentCur], true
	}
	return api.AgentInfo{}, false
}

func (m Model) selPeer() (api.PeerInfo, bool) {
	if m.peerCur < len(m.peers) {
		return m.peers[m.peerCur], true
	}
	return api.PeerInfo{}, false
}

func (m Model) selTask() (api.TaskInfo, bool) {
	if tasks := m.tabTasks(); m.taskCur < len(tasks) {
		return tasks[m.taskCur], true
	}
	return api.TaskInfo{}, false
}

// tabTasks is the task list the Tasks panel shows.
func (m Model) tabTasks() []api.TaskInfo {
	if m.tab == tabAssigned {
		return m.assigned
	}
	return m.tasks
}

// refresh reloads everything on screen: agents, and the selected agent's
// peers, tasks, and selected task.
func (m Model) refresh() tea.Cmd {
	cmds := []tea.Cmd{loadAgents(m.client)}
	if a, ok := m.selAgent(); ok {
		cmds = append(cmds, loadPeers(m.client, a.Name), loadTasks(m.client, a.Name))
		if t, ok := m.selTask(); ok {
			cmds = append(cmds, loadTask(m.client, a.Name, m.tab, t.ID))
		}
	}
	return tea.Batch(cmds...)
}

// agentChanged clears what belonged to the previously selected agent and
// loads the new one's peers and tasks.
func (m Model) agentChanged() (Model, tea.Cmd) {
	m.peers, m.peerCur = nil, 0
	m.tasks, m.assigned, m.taskCur = nil, nil, 0
	m.task, m.scroll = nil, 0
	a, ok := m.selAgent()
	if !ok {
		return m, nil
	}
	return m, tea.Batch(loadPeers(m.client, a.Name), loadTasks(m.client, a.Name))
}

// taskChanged loads the newly selected task in full.
func (m Model) taskChanged() (Model, tea.Cmd) {
	m.task, m.scroll = nil, 0
	a, ok := m.selAgent()
	t, ok2 := m.selTask()
	if !ok || !ok2 {
		return m, nil
	}
	return m, loadTask(m.client, a.Name, m.tab, t.ID)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		m.th = newTheme(msg.IsDark())
		m.name.SetStyles(m.th.input) // restyle in place; keeps anything typed
		m.description.SetStyles(m.th.area)
		for i := range m.peerInputs {
			m.peerInputs[i].SetStyles(m.th.input)
		}
		m.theirToken.SetStyles(m.th.input)
	case tickMsg:
		return m, tea.Batch(m.refresh(), tick())

	case agentsLoadedMsg:
		prev, _ := m.selAgent()
		m.agents = msg.agents
		// Keep the same agent selected if it's still there.
		if i := slices.IndexFunc(m.agents, func(a api.AgentInfo) bool { return a.Name == prev.Name }); i >= 0 {
			m.agentCur = i
		} else {
			m.agentCur = min(m.agentCur, max(0, len(m.agents)-1))
		}
		if now, _ := m.selAgent(); now.Name != prev.Name {
			return m.agentChanged()
		}
	case peersLoadedMsg:
		if a, _ := m.selAgent(); msg.agent == a.Name {
			prev, _ := m.selPeer()
			m.peers = msg.peers
			if i := slices.IndexFunc(m.peers, func(p api.PeerInfo) bool { return p.ID == prev.ID }); i >= 0 {
				m.peerCur = i
			} else {
				m.peerCur = min(m.peerCur, max(0, len(m.peers)-1))
			}
		}
	case tasksLoadedMsg:
		if a, _ := m.selAgent(); msg.agent == a.Name {
			if msg.tab != m.tab {
				m.setTabTasks(msg.tab, msg.tasks)
				break
			}
			prev, _ := m.selTask()
			m.setTabTasks(msg.tab, msg.tasks)
			tasks := m.tabTasks()
			if i := slices.IndexFunc(tasks, func(t api.TaskInfo) bool { return t.ID == prev.ID }); i >= 0 {
				m.taskCur = i
			} else {
				m.taskCur = min(m.taskCur, max(0, len(tasks)-1))
			}
			if now, _ := m.selTask(); now.ID != prev.ID || m.task == nil {
				return m.taskChanged()
			}
		}
	case taskLoadedMsg:
		if t, ok := m.selTask(); ok && msg.tab == m.tab && msg.task.ID == t.ID {
			m.task = &msg.task
		}

	case agentAddedMsg:
		m.created, m.rotated, m.copied = msg.resp, false, false
		m.popup = popupToken
		m.err = nil
		m.resetAgentForm()
		return m, loadAgents(m.client)
	case tokenRotatedMsg:
		m.created = api.AddAgentResponse{Name: msg.resp.Name, Token: msg.resp.Token}
		m.rotated, m.copied = true, false
		m.popup = popupToken
		m.err = nil
	case peerRotatedMsg:
		m.addedPeer = api.AddPeerResponse{Peer: msg.peer, Token: msg.token}
		m.rotated, m.copied = true, false
		m.popup = popupPeerToken
		m.err = nil
	case peerAddedMsg:
		m.addedPeer, m.rotated, m.copied = msg.resp, false, false
		m.popup = popupPeerToken
		m.err = nil
		m.resetPeerForm()
		return m, m.refresh()
	case doneMsg:
		m.err = nil
		return m, m.refresh()

	case agentUpdatedMsg:
		m.popup, m.err = popupNone, nil
		return m, loadAgents(m.client)
	case notifySetMsg:
		m.popup, m.err = popupNone, nil
		if msg.secret != "" {
			m.popup, m.notifySecret, m.copied = popupNotifySecret, msg.secret, false
		}
		return m, loadAgents(m.client)

	case errMsg:
		m.err = msg.err

	case tea.PasteMsg:
		return m.paste(msg)
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.popup != popupNone {
			return m.updatePopup(msg)
		}
		return m.updateMain(msg)
	}
	return m, nil
}

// updateMain handles keys on the panels (no popup open).
func (m Model) updateMain(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "q":
		return m, tea.Quit
	case "tab", "shift+tab":
		order := []panel{panelAgents, panelPeers, panelTasks}
		i := slices.Index(order, m.detailOf)
		step := 1
		if key == "shift+tab" {
			step = len(order) - 1
		}
		return m.focusOn(order[(i+step)%len(order)]), nil
	case "1":
		return m.focusOn(panelAgents), nil
	case "2":
		return m.focusOn(panelPeers), nil
	case "3":
		return m.focusOn(panelTasks), nil
	case "enter", "right", "l":
		m.focus = panelDetail
		return m, nil
	case "esc", "left", "h":
		m.focus = m.detailOf
		m.err = nil
		return m, nil
	case "r":
		m.err = nil
		return m, m.refresh()
	case "up", "k", "down", "j":
		return m.move(key == "down" || key == "j")
	case "pgdown", "ctrl+d", "pgup", "ctrl+u", "g", "home", "G", "end":
		return m.scrollDetail(key), nil
	case "[", "]":
		if m.detailOf == panelTasks {
			m.tab = 1 - m.tab
			m.taskCur = 0
			return m.taskChanged()
		}
	}

	// Actions on the selected item of the panel in use (the focused list, or
	// the one the detail pane is showing).
	m.err = nil
	switch m.detailOf {
	case panelAgents:
		return m.agentAction(key)
	case panelPeers:
		return m.peerAction(key)
	case panelTasks:
		return m.taskAction(key)
	}
	return m, nil
}

func (m Model) focusOn(p panel) Model {
	m.focus, m.detailOf = p, p
	m.scroll = 0
	m.err = nil
	return m
}

// move goes up or down in the focused list, or scrolls the detail pane.
func (m Model) move(down bool) (tea.Model, tea.Cmd) {
	step := -1
	if down {
		step = 1
	}
	clamp := func(cur, n int) int { return max(0, min(n-1, cur+step)) }
	switch m.focus {
	case panelAgents:
		if next := clamp(m.agentCur, len(m.agents)); next != m.agentCur {
			m.agentCur = next
			return m.agentChanged()
		}
	case panelPeers:
		m.peerCur = clamp(m.peerCur, len(m.peers))
		m.scroll = 0
	case panelTasks:
		if next := clamp(m.taskCur, len(m.tabTasks())); next != m.taskCur {
			m.taskCur = next
			return m.taskChanged()
		}
	case panelDetail:
		m.scroll = max(0, min(m.scroll+step, m.maxScroll()))
	}
	return m, nil
}

// scrollDetail scrolls the detail pane, from any panel: half a page, or to
// the top or bottom.
func (m Model) scrollDetail(key string) Model {
	half := max(1, (m.dims().detailH-2)/2)
	switch key {
	case "pgdown", "ctrl+d":
		m.scroll += half
	case "pgup", "ctrl+u":
		m.scroll -= half
	case "g", "home":
		m.scroll = 0
	case "G", "end":
		m.scroll = m.maxScroll()
	}
	m.scroll = max(0, min(m.scroll, m.maxScroll()))
	return m
}

func (m Model) confirm(text string, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	m.popup, m.confirmText, m.confirmCmd = popupConfirm, text, cmd
	return m, nil
}

func (m Model) agentAction(key string) (tea.Model, tea.Cmd) {
	if key == "a" {
		m.popup = popupAddAgent
		m.description.Blur()
		cmd := m.name.Focus()
		return m, cmd
	}
	a, ok := m.selAgent()
	if !ok {
		return m, nil
	}
	switch key {
	case "e":
		return m.openEditAgent(a)
	case "x":
		return m, setAgentDisabled(m.client, a.Name, !a.Disabled)
	case "t":
		return m.confirm("Issue a new token for "+a.Name+"? Its current MCP URL stops working.", rotateToken(m.client, a.Name))
	case "n":
		return m.openNotify(a)
	case "d":
		return m.confirm("Remove "+a.Name+" with its peers, sessions, and tasks?", removeAgent(m.client, a.Name))
	}
	return m, nil
}

func (m Model) peerAction(key string) (tea.Model, tea.Cmd) {
	if _, ok := m.selAgent(); !ok {
		return m, nil
	}
	switch key {
	case "a":
		m.popup = popupAddPeer
		m.resetPeerForm()
		cmd := m.peerInputs[0].Focus()
		return m, cmd
	case "d":
		if p, ok := m.selPeer(); ok {
			return m.confirm("Remove peer "+p.Name+"? Its running tasks are canceled.", removePeer(m.client, p.ID))
		}
	case "t":
		if p, ok := m.selPeer(); ok {
			return m.askToken(p, false)
		}
	case "n":
		if p, ok := m.selPeer(); ok {
			return m.confirm("Issue a new token for "+p.Name+"? The token "+p.Name+" uses now stops working.", rotatePeerToken(m.client, p))
		}
	}
	return m, nil
}

// askToken opens the popup for the token peer p issued us.
func (m Model) askToken(p api.PeerInfo, wizard bool) (tea.Model, tea.Cmd) {
	m.popup, m.tokenPeer, m.wizard = popupSetToken, p, wizard
	m.theirToken = newInput("token", 50, m.th)
	cmd := m.theirToken.Focus()
	return m, cmd
}

func (m Model) taskAction(key string) (tea.Model, tea.Cmd) {
	a, _ := m.selAgent()
	t, ok := m.selTask()
	if ok && m.tab == tabDelegated && key == "c" && !finalState(t.State) {
		return m.confirm("Cancel this task on "+t.Peer+"?", cancelTask(m.client, a.Name, t.ID))
	}
	return m, nil
}

// paste puts pasted text into the focused field of the open popup, if any.
func (m Model) paste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.popup {
	case popupAddAgent:
		if m.name.Focused() {
			m.name, cmd = m.name.Update(msg)
		} else {
			m.description, cmd = m.description.Update(msg)
		}
	case popupAddPeer:
		m.peerInputs[m.peerFocus], cmd = m.peerInputs[m.peerFocus].Update(msg)
	case popupSetToken:
		m.theirToken, cmd = m.theirToken.Update(msg)
	case popupEditAgent:
		if !m.form.onSkills {
			m.form.desc, cmd = m.form.desc.Update(msg)
		}
	case popupEditSkill:
		m.form.fields[m.form.field], cmd = m.form.fields[m.form.field].Update(msg)
	case popupNotify:
		m.notifyInput, cmd = m.notifyInput.Update(msg)
	}
	return m, cmd
}

// updatePopup handles keys while a popup is open.
func (m Model) updatePopup(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch m.popup {
	case popupConfirm:
		m.popup = popupNone
		if key == "y" {
			return m, m.confirmCmd
		}
		return m, nil

	case popupToken:
		switch key {
		case "c":
			m.copied = true
			return m, tea.SetClipboard(mcpURL(m.mcpAddr, m.created.Token))
		case "enter", "esc":
			m.popup = popupNone
		}
		return m, nil

	case popupPeerToken:
		switch key {
		case "c":
			m.copied = true
			return m, tea.SetClipboard(m.addedPeer.Token)
		case "enter":
			if m.rotated {
				m.popup = popupNone
				return m, nil
			}
			return m.askToken(m.addedPeer.Peer, true)
		case "esc":
			m.popup = popupNone
		}
		return m, nil

	case popupAddAgent:
		switch key {
		case "esc":
			m.popup = popupNone
			m.err = nil
			m.resetAgentForm()
			return m, nil
		case "tab", "shift+tab":
			if m.name.Focused() {
				m.name.Blur()
				cmd := m.description.Focus()
				return m, cmd
			}
			m.description.Blur()
			cmd := m.name.Focus()
			return m, cmd
		case "enter":
			m.err = nil
			if strings.TrimSpace(m.description.Value()) == "" {
				m.err = errDescription
				return m, nil
			}
			return m, addAgent(m.client, api.AddAgentRequest{
				Name:        strings.TrimSpace(m.name.Value()),
				Description: strings.TrimSpace(m.description.Value()),
			})
		}
		var cmd tea.Cmd
		if m.name.Focused() {
			m.name, cmd = m.name.Update(msg)
		} else {
			m.description, cmd = m.description.Update(msg)
		}
		return m, cmd

	case popupEditAgent:
		return m.updateEditAgent(msg)
	case popupEditSkill:
		return m.updateEditSkill(msg)
	case popupNotify:
		return m.updateNotify(msg)
	case popupNotifySecret:
		switch key {
		case "c":
			m.copied = true
			return m, tea.SetClipboard(m.notifySecret)
		case "enter", "esc":
			m.popup, m.notifySecret = popupNone, ""
		}
		return m, nil

	case popupSetToken:
		switch key {
		case "esc":
			m.popup = popupNone
			m.err = nil
			return m, nil
		case "enter":
			tok := strings.TrimSpace(m.theirToken.Value())
			if tok == "" {
				return m, nil
			}
			m.popup = popupNone
			m.err = nil
			return m, setPeerToken(m.client, m.tokenPeer.ID, tok)
		}
		var cmd tea.Cmd
		m.theirToken, cmd = m.theirToken.Update(msg)
		return m, cmd

	case popupAddPeer:
		switch key {
		case "esc":
			m.popup = popupNone
			m.err = nil
			return m, nil
		case "tab", "shift+tab":
			m.peerInputs[m.peerFocus].Blur()
			step := 1
			if key == "shift+tab" {
				step = len(m.peerInputs) - 1
			}
			m.peerFocus = (m.peerFocus + step) % len(m.peerInputs)
			cmd := m.peerInputs[m.peerFocus].Focus()
			return m, cmd
		case "enter":
			a, _ := m.selAgent()
			m.err = nil
			return m, addPeer(m.client, api.AddPeerRequest{
				Agent: a.Name,
				Name:  strings.TrimSpace(m.peerInputs[0].Value()),
				URL:   strings.TrimSpace(m.peerInputs[1].Value()),
			})
		}
		var cmd tea.Cmd
		m.peerInputs[m.peerFocus], cmd = m.peerInputs[m.peerFocus].Update(msg)
		return m, cmd
	}
	return m, nil
}
