package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/arjunvlama/toli/internal/api"
	"github.com/arjunvlama/toli/internal/connect"
)

// What's shown is the /v1/ API's data as it comes: lists have the API's
// fields as columns, and the detail pane labels each value with its API field
// name. Only times are reformatted (local time).

// dims is where everything goes for the current terminal size.
type dims struct {
	wide                    bool // detail pane beside the panels (else below)
	leftW, rightW           int
	agentsH, peersH, tasksH int // panel heights, borders included
	detailH                 int
}

// minWide is the terminal width below which the detail pane moves under the
// panels.
const minWide = 90

func (m Model) dims() dims {
	d := dims{wide: m.width >= minWide}
	body := max(12, m.height-2-len(m.helpLines())) // header, status line, help
	left := body
	if d.wide {
		d.leftW = max(34, min(52, m.width*2/5))
		d.rightW = m.width - d.leftW
	} else {
		d.leftW, d.rightW = m.width, m.width
		left = body * 11 / 20
	}
	fit := func(n, cap int) int { return max(4, min(n+3, cap)) } // borders + column header
	d.agentsH = fit(len(m.agents), left/3)
	d.peersH = fit(len(m.peers), left/4)
	d.tasksH = max(4, left-d.agentsH-d.peersH)
	d.detailH = body
	if !d.wide {
		d.detailH = max(4, body-left)
	}
	return d
}

func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true // take over the terminal like vim/less; restored on quit
	v.WindowTitle = "toli"
	return v
}

func (m Model) render() string {
	if m.width == 0 {
		return "" // size not known yet
	}
	d := m.dims()

	agentsHead, agentRows := m.agentTable()
	peersHead, peerRows := m.peerTable()
	tasksHead, taskRows := m.taskTable()
	left := lipgloss.JoinVertical(lipgloss.Left,
		m.list(m.title("Agents", m.focus == panelAgents), agentsHead, agentRows, m.agentCur, d.leftW, d.agentsH, m.focus == panelAgents),
		m.list(m.peersTitle(), peersHead, peerRows, m.peerCur, d.leftW, d.peersH, m.focus == panelPeers),
		m.list(m.taskTabs(), tasksHead, taskRows, m.taskCur, d.leftW, d.tasksH, m.focus == panelTasks),
	)
	title, lines := m.detail(d.rightW - 4)
	scroll := min(m.scroll, max(0, len(lines)-(d.detailH-2)))
	right := m.boxAt(m.title(title, m.focus == panelDetail), lines, scroll, d.rightW, d.detailH, m.focus == panelDetail)

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	if !d.wide {
		body = lipgloss.JoinVertical(lipgloss.Left, left, right)
	}

	status := ""
	if m.err != nil {
		status = m.th.errText.Render(ansi.Truncate(" ✗ "+m.err.Error(), m.width-1, "…"))
	}
	header := spread(" "+m.th.logo.Render("toli"), m.th.status.Render("mcp "+m.mcpAddr)+" ", m.width)
	page := lipgloss.JoinVertical(lipgloss.Left, append([]string{header, body, status}, m.helpLines()...)...)

	if m.popup == popupNone {
		return page
	}
	pop := m.popupBox()
	x := max(0, (m.width-lipgloss.Width(pop))/2)
	y := max(0, (m.height-lipgloss.Height(pop))/2)
	return lipgloss.NewCanvas(m.width, m.height).Compose(lipgloss.NewCompositor(
		lipgloss.NewLayer(page),
		lipgloss.NewLayer(pop).X(x).Y(y).Z(1),
	)).Render()
}

// list draws a panel holding a table: a column-header row, then one row per
// item, keeping the cursor row in view and marking it.
func (m Model) list(title, head string, rows []string, cursor, w, h int, focused bool) string {
	visible := h - 3 // borders and the header row
	start := 0
	if cursor >= visible {
		start = cursor - visible + 1
	}
	lines := []string{" " + m.th.bold.Render(head)}
	for i := start; i < len(rows); i++ {
		switch {
		case i == cursor && focused:
			lines = append(lines, m.th.selBar.Render("▌")+m.th.selName.Render(rows[i]))
		case i == cursor:
			lines = append(lines, m.th.muted.Render("▏")+rows[i])
		default:
			lines = append(lines, " "+rows[i])
		}
	}
	return m.boxAt(title, lines, 0, w, h, focused)
}

// title is a panel title, styled for whether the panel is focused.
func (m Model) title(s string, focused bool) string {
	if focused {
		return m.th.labelOn.Render(" " + s + " ")
	}
	return m.th.bold.Render(" " + s + " ")
}

// peersTitle is the Peers panel's title, with how many peers aren't
// connected (unreachable, or no working token: calls to them fail).
func (m Model) peersTitle() string {
	t := m.title("Peers", m.focus == panelPeers)
	if n := len(slices.DeleteFunc(slices.Clone(m.peers), func(p api.PeerInfo) bool { return p.Connected == nil || *p.Connected })); n > 0 {
		t += m.th.errText.Render(fmt.Sprintf("%d not connected ", n))
	}
	return t
}

// taskTabs is the Tasks panel's title: its two tabs, the current one
// highlighted, with how many assigned tasks are new (submitted).
func (m Model) taskTabs() string {
	assigned := "Assigned"
	if n := len(slices.DeleteFunc(slices.Clone(m.assigned), func(t api.TaskInfo) bool { return t.State != "TASK_STATE_SUBMITTED" })); n > 0 {
		assigned += fmt.Sprintf(" (%d new)", n)
	}
	tabs := []string{"Delegated", assigned}
	focused := m.focus == panelTasks
	var out []string
	for i, name := range tabs {
		if taskTab(i) == m.tab {
			out = append(out, m.title(name, focused))
		} else {
			out = append(out, m.th.muted.Render(" "+name+" "))
		}
	}
	return strings.Join(out, m.th.muted.Render("─"))
}

// boxAt draws a bordered panel of lines under a (styled) title, starting at
// line start.
func (m Model) boxAt(title string, lines []string, start, w, h int, focused bool) string {
	border := m.th.rule
	if focused {
		border = m.th.selBar
	}
	inner := max(1, w-2)
	t := ansi.Truncate(title, max(0, inner-2), "…")
	top := border.Render("╭─") + t + border.Render(strings.Repeat("─", max(0, inner-1-lipgloss.Width(t)))+"╮")

	var b strings.Builder
	b.WriteString(top + "\n")
	for i := range max(0, h-2) {
		line := ""
		if j := start + i; j >= 0 && j < len(lines) {
			line = lines[j]
		}
		line = ansi.Truncate(line, max(0, inner-2), "…")
		pad := strings.Repeat(" ", max(0, inner-2-lipgloss.Width(line)))
		b.WriteString(border.Render("│") + " " + line + pad + " " + border.Render("│") + "\n")
	}
	if more := len(lines) - start - (h - 2); more > 0 {
		tag := m.th.muted.Render(fmt.Sprintf(" ↓ %d more ", more))
		b.WriteString(border.Render("╰"+strings.Repeat("─", max(0, inner-lipgloss.Width(tag)-1))) + tag + border.Render("─╯"))
	} else {
		b.WriteString(border.Render("╰" + strings.Repeat("─", inner) + "╯"))
	}
	return b.String()
}

// table lays out rows of cells in aligned columns; it returns the header
// line and one line per row.
func table(header []string, rows [][]string) (string, []string) {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len(h)
	}
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], lipgloss.Width(c))
		}
	}
	line := func(cells []string) string {
		var b strings.Builder
		for i, c := range cells {
			b.WriteString(c)
			if i < len(cells)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-lipgloss.Width(c)+2))
			}
		}
		return b.String()
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = line(r)
	}
	return line(header), out
}

func (m Model) agentTable() (string, []string) {
	var rows [][]string
	for _, a := range m.agents {
		rows = append(rows, []string{a.Name, a.Harness, fmt.Sprint(a.Disabled)})
	}
	return table([]string{"NAME", "HARNESS", "DISABLED"}, rows)
}

func (m Model) peerTable() (string, []string) {
	var rows [][]string
	for _, p := range m.peers {
		rows = append(rows, []string{p.Name, m.connected(p), p.URL})
	}
	return table([]string{"NAME", "CONNECTED", "URL"}, rows)
}

// connected and hasToken are a peer's flags, colored so a problem stands out.
func (m Model) connected(p api.PeerInfo) string {
	if p.Connected == nil {
		return "" // not checked yet
	}
	if *p.Connected {
		return m.th.success.UnsetBold().Render("true")
	}
	return m.th.errText.Render("false")
}

func (m Model) hasToken(p api.PeerInfo) string {
	if p.HasToken {
		return "true"
	}
	return m.th.warn.Render("false")
}

func (m Model) taskTable() (string, []string) {
	var rows [][]string
	for _, t := range m.tabTasks() {
		rows = append(rows, []string{shortID(t.ID), strings.TrimPrefix(t.State, "TASK_STATE_"), t.Peer})
	}
	return table([]string{"ID", "STATE", "PEER"}, rows)
}

// shortID is a task ID's last 8 characters. Task IDs are UUIDv7, which start
// with a timestamp, so tasks created together share a prefix; the end is the
// random part.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

// detail is the right pane's title and lines (each fitting w) for what's
// selected in the panel in use.
func (m Model) detail(w int) (string, []string) {
	switch m.detailOf {
	case panelPeers:
		if p, ok := m.selPeer(); ok {
			return "Peer", m.peerDetail(p, w)
		}
		if _, ok := m.selAgent(); ok {
			return "Peer", m.wrap(w, "No peers. Press a to add one.")
		}
	case panelTasks:
		if t, ok := m.selTask(); ok {
			return "Task", m.taskDetail(t, w)
		}
		if _, ok := m.selAgent(); ok && m.tab == tabAssigned {
			return "Task", m.wrap(w, "No assigned tasks. They appear here when a peer delegates work to this agent.")
		}
		if _, ok := m.selAgent(); ok {
			return "Task", m.wrap(w, "No delegated tasks. They appear here when this agent delegates work to a peer.")
		}
	default:
		if a, ok := m.selAgent(); ok {
			return "Agent", m.agentDetail(a, w)
		}
		return "Agent", m.wrap(w, "No agents. Press a to register one.")
	}
	return "", nil
}

// fields renders label/value pairs, wrapping long values under the value
// column.
func (m Model) fields(w int, kv ...string) []string {
	const labelW = 13
	var lines []string
	for i := 0; i+1 < len(kv); i += 2 {
		vals := m.wrap(w-labelW, kv[i+1])
		if len(vals) == 0 || kv[i+1] == "" {
			vals = []string{""}
		}
		for j, v := range vals {
			label := strings.Repeat(" ", labelW)
			if j == 0 {
				label = m.th.bold.Render(fmt.Sprintf("%-*s", labelW, kv[i]))
			}
			lines = append(lines, label+v)
		}
	}
	return lines
}

func (m Model) section(name string) []string {
	return []string{"", m.th.labelOn.Render(name)}
}

func (m Model) agentDetail(a api.AgentInfo, w int) []string {
	lines := m.fields(w, "name", a.Name, "description", a.Description, "disabled", fmt.Sprint(a.Disabled), "share_url", a.ShareURL, "harness", a.Harness, "notify", notifyLabel(a.Notify))
	return append(lines, m.skills(a.Skills, w)...)
}

// skills is the skills section of an agent's or peer's detail.
func (m Model) skills(skills []api.Skill, w int) []string {
	lines := m.section("skills")
	if len(skills) == 0 {
		lines = append(lines, m.th.muted.Render("(none)"))
	}
	for i, s := range skills {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, m.fields(w, "id", s.ID, "name", s.Name, "description", s.Description,
			"tags", strings.Join(s.Tags, ", "), "examples", strings.Join(s.Examples, "; "))...)
	}
	return lines
}

func (m Model) peerDetail(p api.PeerInfo, w int) []string {
	lines := m.fields(w,
		"id", fmt.Sprint(p.ID),
		"name", p.Name,
		"url", p.URL,
		"card_name", p.CardName,
		"description", p.Description,
		"last_seen", timeOrEmpty(p.LastSeen),
		"last_error", p.LastError,
		"connected", m.connected(p),
		"has_token", m.hasToken(p),
	)
	if !p.HasToken {
		lines = append(lines, "")
		for _, l := range m.wrap(w, "No token for this peer: cannot delegate tasks to it. Press t to add it.") {
			lines = append(lines, m.th.warn.Render(l))
		}
	}
	return append(lines, m.skills(p.Skills, w)...)
}

func (m Model) taskDetail(t api.TaskInfo, w int) []string {
	kv := []string{
		"id", t.ID,
		"state", t.State,
		"peer", t.Peer,
		"session_id", t.SessionID,
		"summary", t.Summary,
		"updated_at", fmtTime(t.UpdatedAt),
	}
	if m.tab == tabDelegated {
		kv = append(kv, "direct", fmt.Sprint(t.Direct), "deadline", timeOrEmpty(t.Deadline))
	}
	lines := m.fields(w, kv...)
	lines = append(lines, m.section("messages")...)
	if m.task == nil || m.task.ID != t.ID {
		return append(lines, m.th.muted.Render("loading…"))
	}
	a, _ := m.selAgent()
	if len(m.task.Messages) == 0 {
		lines = append(lines, m.th.muted.Render("(none)"))
	}
	for _, msg := range m.task.Messages {
		lines = append(lines, m.bubble(msg, a.Name, t.Peer, w)...)
	}
	lines = append(lines, m.section("artifacts")...)
	if len(m.task.Artifacts) == 0 {
		lines = append(lines, m.th.muted.Render("(none)"))
	}
	for _, art := range m.task.Artifacts {
		name := art.Name
		if name == "" {
			name = "artifact"
		}
		lines = append(lines, strings.Split(m.titledBox(name, art.Text, w, m.th.rule), "\n")...)
	}
	return lines
}

// bubble is one chat message: the agent's on the left in the accent color,
// the peer's on the right, each under its sender's name.
func (m Model) bubble(msg api.TaskMessage, agent, peer string, w int) []string {
	bw := max(12, w*7/10)
	who, border, indent := agent, m.th.selBar, 0
	if msg.FromPeer {
		who, border = peer, m.th.rule
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border.GetForeground()).
		Padding(0, 1).MaxWidth(bw).Width(min(bw, lipgloss.Width(msg.Text)+4)).Render(msg.Text)
	if msg.FromPeer {
		indent = max(0, w-lipgloss.Width(box))
	}
	pad := strings.Repeat(" ", indent)
	name := m.th.bold.Render(who)
	if msg.FromPeer {
		name = strings.Repeat(" ", max(0, w-lipgloss.Width(name))) + name
	}
	lines := []string{"", name}
	for _, l := range strings.Split(box, "\n") {
		lines = append(lines, pad+l)
	}
	return lines
}

// titledBox is text in a rounded box with title in its top border.
func (m Model) titledBox(title, text string, w int, border lipgloss.Style) string {
	inner := max(4, w-2)
	t := " " + m.th.bold.Render(title) + " "
	top := border.Render("╭─") + t + border.Render(strings.Repeat("─", max(0, inner-1-lipgloss.Width(t)))+"╮")
	var b strings.Builder
	b.WriteString(top + "\n")
	for _, l := range m.wrap(inner-2, text) {
		b.WriteString(border.Render("│") + " " + l + strings.Repeat(" ", max(0, inner-2-lipgloss.Width(l))) + " " + border.Render("│") + "\n")
	}
	b.WriteString(border.Render("╰" + strings.Repeat("─", inner) + "╯"))
	return b.String()
}

// maxScroll is how far the detail pane can scroll.
func (m Model) maxScroll() int {
	d := m.dims()
	_, lines := m.detail(d.rightW - 4)
	return max(0, len(lines)-(d.detailH-2))
}

// helpLines is the key help: what works on the selection, then what works
// everywhere, on two lines if the terminal is narrow.
func (m Model) helpLines() []string {
	var keys []string
	switch m.detailOf {
	case panelAgents:
		toggle := "disable"
		if a, ok := m.selAgent(); ok && a.Disabled {
			toggle = "enable"
		}
		keys = []string{"a", "add", "e", "edit", "x", toggle, "t", "new token", "n", "notify", "d", "remove"}
	case panelPeers:
		keys = []string{"a", "add", "t", "token", "n", "new token", "d", "remove"}
	case panelTasks:
		keys = []string{"[ ]", "tabs"}
		if t, ok := m.selTask(); ok && m.tab == tabDelegated && !finalState(t.State) {
			keys = append(keys, "c", "cancel")
		}
	}
	nav := []string{"enter", "scroll", "tab", "next panel", "r", "refresh", "q", "quit"}
	if m.focus == panelDetail {
		nav = []string{"↑↓", "scroll", "pgup/pgdn", "page", "g/G", "top/bottom", "esc", "back", "r", "refresh", "q", "quit"}
	}
	one := m.th.help(append(keys, nav...)...)
	if lipgloss.Width(one) <= m.width-2 || len(keys) == 0 {
		return []string{" " + ansi.Truncate(one, max(0, m.width-2), "…")}
	}
	return []string{
		" " + ansi.Truncate(m.th.help(keys...), max(0, m.width-2), "…"),
		" " + ansi.Truncate(m.th.help(nav...), max(0, m.width-2), "…"),
	}
}

// The popup over the layout.

func (m Model) popupBox() string {
	w := m.popupWidth()
	var title, body, keys string
	switch m.popup {
	case popupEditAgent:
		title, body, keys = m.editAgentBox(w)
	case popupEditSkill:
		title, body, keys = m.editSkillBox()
	case popupNotify:
		title, body, keys = m.notifyBox(w)
	case popupNotifySecret:
		title, body, keys = m.notifySecretBox(w)
	case popupConfirm:
		title = "Confirm"
		body = strings.Join(m.wrap(w-4, m.confirmText), "\n")
		keys = m.th.help("y", "yes", "n", "no")
	case popupAddAgent:
		title = "Register an agent"
		label := func(s string, on bool) string {
			if on {
				return m.th.labelOn.Render(s)
			}
			return m.th.labelOff.Render(s)
		}
		body = label("Name", m.name.Focused()) + "\n" + m.name.View() + "\n\n" +
			label("Description", m.description.Focused()) + "\n" + m.description.View() + "\n\n" +
			m.th.muted.Render("Peers see the description when deciding what to send this agent.")
		keys = m.th.help("tab", "next field", "enter", "create", "esc", "cancel")
	case popupAddPeer:
		a, _ := m.selAgent()
		title = "Add peer to " + a.Name + " (1/3)"
		labels := []string{"Name", "Card URL"}
		var b strings.Builder
		for i, in := range m.peerInputs {
			l := m.th.labelOff.Render(labels[i])
			if i == m.peerFocus {
				l = m.th.labelOn.Render(labels[i])
			}
			b.WriteString(l + "\n" + in.View() + "\n\n")
		}
		b.WriteString(m.th.muted.Render("Enter fetches the peer's card and adds the peer."))
		body = b.String()
		keys = m.th.help("tab", "next field", "enter", "next", "esc", "cancel")
	case popupToken:
		url := mcpURL(m.mcpAddr, m.created.Token)
		title = "Created " + m.created.Name
		intro := "Connect a coding agent to it with this MCP URL:"
		if m.rotated {
			title = "New token for " + m.created.Name
			intro = "The old MCP URL no longer works. Reconnect the coding agent with this one:"
		}
		note := m.th.warn.Render("It contains the agent's token and won't be shown again.")
		if m.copied {
			note = m.th.success.UnsetBold().Render("✓ Copied to clipboard")
		}
		body = strings.Join(m.wrap(w-4, intro), "\n") + "\n\n" + m.th.bold.Render(url) + "\n" + note + "\n\n" +
			m.th.muted.Render("Set up a coding agent, in its project directory:") + "\n" +
			m.th.muted.Render("toli connect <harness> <MCP URL>") + "\n" +
			m.th.muted.Render("harness: "+strings.Join(connect.Harnesses(), ", ")) + "\n\n" +
			m.th.muted.Render(strings.Join(m.wrap(w-4, "Other agents: MCP URL above, see docs for how to get notifications"), "\n"))
		keys = m.th.help("c", "copy URL", "enter", "close")
	case popupSetToken:
		a, _ := m.selAgent()
		p := m.tokenPeer
		title = "Token for " + p.Name
		keys = m.th.help("enter", "save", "esc", "cancel")
		if m.wizard {
			title = "Add peer to " + a.Name + " (3/3)"
			keys = m.th.help("enter", "save", "esc", "later (t on the peer)")
		}
		body = m.th.labelOn.Render("Enter the token the peer gave you for access") + "\n" + m.theirToken.View()
	case popupPeerToken:
		a, _ := m.selAgent()
		p := m.addedPeer.Peer
		title = "Add peer to " + a.Name + " (2/3)"
		note := m.th.warn.Render("Shown once.")
		if m.copied {
			note = m.th.success.UnsetBold().Render("✓ Copied to clipboard")
		}
		body = strings.Join(m.fields(w-4, "name", p.Name, "card_name", p.CardName, "description", p.Description), "\n") +
			"\n\n" + m.th.labelOn.Render("Give this token to the peer for access") + "\n" + m.th.bold.Render(m.addedPeer.Token) + "\n" + note
		keys = m.th.help("c", "copy token", "enter", "next", "esc", "done")
		if m.rotated {
			title = "New token for " + p.Name
			keys = m.th.help("c", "copy token", "enter", "close")
		}
	}
	if m.err != nil {
		body += "\n\n" + strings.Join(m.wrap(w-4, "✗ "+m.err.Error()), "\n")
	}
	content := m.th.section.Render(title) + "\n\n" + body + "\n\n" + keys
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.th.selBar.GetForeground()).
		Padding(1, 2).Width(w).Render(content)
}

// Small pieces.

// spread puts left and right at the two ends of width w (or one space apart
// if w is too small).
func spread(left, right string, w int) string {
	if right == "" {
		return left
	}
	gap := max(1, w-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", gap) + right
}

// wrap word-wraps paragraphs to width w; "" makes a blank line.
func (m Model) wrap(w int, paras ...string) []string {
	var lines []string
	for _, p := range paras {
		if p == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, strings.Split(lipgloss.NewStyle().Width(max(10, w)).Render(p), "\n")...)
	}
	return lines
}

func finalState(state string) bool {
	switch state {
	case "TASK_STATE_COMPLETED", "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED":
		return true
	}
	return false
}

// fmtTime shows a time in local time, to the second.
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func timeOrEmpty(t *time.Time) string {
	if t == nil {
		return ""
	}
	return fmtTime(*t)
}

// mcpURL is the URL that links a coding-agent session to an agent: the token
// is in the path, so any MCP client that takes a URL works (no headers needed).
func mcpURL(mcpAddr, tok string) string {
	return fmt.Sprintf("http://%s/mcp/%s", mcpAddr, tok)
}
