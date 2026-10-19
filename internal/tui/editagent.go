package tui

import (
	"context"
	"errors"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/arjunvlama/toli/internal/api"
)

// Editing an agent's card (description and skills) in two popups: the agent
// (description and its list of skills), and one skill at a time. Nothing is
// saved until the agent popup is saved.

// agentForm is the agent being edited.
type agentForm struct {
	agent    string
	desc     textarea.Model
	skills   []api.Skill
	cur      int  // selected skill
	onSkills bool // focus is on the skills list, not the description

	// popupEditSkill
	skill  int               // index of the skill being edited; -1 for a new one
	fields []textinput.Model // name, description, tags, examples
	field  int               // focused field
}

var skillLabels = []string{"Name", "Description", "Tags (comma-separated)", "Examples (separated by ;)"}

type agentUpdatedMsg struct{}

var errDescription = errors.New("description is required")

func updateAgent(c *api.Client, name string, req api.UpdateAgentRequest) tea.Cmd {
	return call(func(ctx context.Context) (tea.Msg, error) {
		return agentUpdatedMsg{}, c.UpdateAgent(ctx, name, req)
	})
}

func (m Model) openEditAgent(a api.AgentInfo) (tea.Model, tea.Cmd) {
	w := m.popupWidth() - 8
	f := agentForm{agent: a.Name, desc: newArea("what this agent works on", w, m.th), skills: append([]api.Skill(nil), a.Skills...)}
	f.desc.SetValue(a.Description)
	m.form, m.popup, m.err = f, popupEditAgent, nil
	cmd := m.form.desc.Focus()
	return m, cmd
}

func (m Model) openEditSkill(i int) (tea.Model, tea.Cmd) {
	w := m.popupWidth() - 8
	var s api.Skill
	if i >= 0 {
		s = m.form.skills[i]
	}
	values := []string{s.Name, s.Description, strings.Join(s.Tags, ", "), strings.Join(s.Examples, "; ")}
	placeholders := []string{"Write SQL migrations", "Postgres schema changes with rollback steps", "db, postgres", "Add an index on users.email"}
	m.form.fields = nil
	for j, v := range values {
		in := newInput(placeholders[j], w, m.th)
		in.SetValue(v)
		m.form.fields = append(m.form.fields, in)
	}
	m.form.skill, m.form.field = i, 0
	m.popup, m.err = popupEditSkill, nil
	cmd := m.form.fields[0].Focus()
	return m, cmd
}

func (m Model) updateEditAgent(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	f := &m.form
	switch key := msg.String(); key {
	case "esc":
		m.popup, m.err = popupNone, nil
		return m, nil
	case "ctrl+s":
		return m.saveAgent()
	case "tab", "shift+tab":
		f.onSkills = !f.onSkills
		if f.onSkills {
			f.desc.Blur()
			return m, nil
		}
		cmd := f.desc.Focus()
		return m, cmd
	}
	if !f.onSkills {
		if msg.String() == "enter" {
			return m.saveAgent()
		}
		var cmd tea.Cmd
		f.desc, cmd = f.desc.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "up", "k":
		f.cur = max(0, f.cur-1)
	case "down", "j":
		f.cur = max(0, min(len(f.skills)-1, f.cur+1))
	case "a":
		return m.openEditSkill(-1)
	case "enter":
		if f.cur < len(f.skills) {
			return m.openEditSkill(f.cur)
		}
	case "d":
		if f.cur < len(f.skills) {
			f.skills = append(f.skills[:f.cur:f.cur], f.skills[f.cur+1:]...)
			f.cur = max(0, min(f.cur, len(f.skills)-1))
		}
	}
	return m, nil
}

func (m Model) saveAgent() (tea.Model, tea.Cmd) {
	desc := strings.TrimSpace(m.form.desc.Value())
	if desc == "" {
		m.err = errDescription
		return m, nil
	}
	m.err = nil
	return m, updateAgent(m.client, m.form.agent, api.UpdateAgentRequest{Description: desc, Skills: m.form.skills})
}

func (m Model) updateEditSkill(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	f := &m.form
	switch key := msg.String(); key {
	case "esc":
		m.popup, m.err = popupEditAgent, nil
		return m, nil
	case "tab", "shift+tab":
		f.fields[f.field].Blur()
		step := 1
		if key == "shift+tab" {
			step = len(f.fields) - 1
		}
		f.field = (f.field + step) % len(f.fields)
		cmd := f.fields[f.field].Focus()
		return m, cmd
	case "enter":
		s := api.Skill{
			Name:        strings.TrimSpace(f.fields[0].Value()),
			Description: strings.TrimSpace(f.fields[1].Value()),
			Tags:        splitList(f.fields[2].Value(), ","),
			Examples:    splitList(f.fields[3].Value(), ";"),
		}
		if s.Name == "" || s.Description == "" {
			m.err = errors.New("name and description are required")
			return m, nil
		}
		if f.skill >= 0 {
			s.ID = f.skills[f.skill].ID
			f.skills[f.skill] = s
		} else {
			f.skills = append(f.skills, s)
			f.cur = len(f.skills) - 1
		}
		m.popup, m.err = popupEditAgent, nil
		return m, nil
	}
	var cmd tea.Cmd
	f.fields[f.field], cmd = f.fields[f.field].Update(msg)
	return m, cmd
}

// splitList splits s on sep, trimming each item and dropping empty ones.
func splitList(s, sep string) []string {
	var out []string
	for _, item := range strings.Split(s, sep) {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func (m Model) editAgentBox(w int) (title, body, keys string) {
	f := m.form
	title = "Edit " + f.agent
	label := func(s string, on bool) string {
		if on {
			return m.th.labelOn.Render(s)
		}
		return m.th.labelOff.Render(s)
	}
	lines := []string{label("Description", !f.onSkills), f.desc.View(), "", label("Skills", f.onSkills)}
	if len(f.skills) == 0 {
		lines = append(lines, m.th.muted.Render("(none)"))
	}
	for i, s := range f.skills {
		row := s.Name + "  " + m.th.muted.Render(s.Description)
		switch {
		case i == f.cur && f.onSkills:
			row = m.th.selBar.Render("▌") + m.th.selName.Render(s.Name) + "  " + m.th.muted.Render(s.Description)
		default:
			row = " " + row
		}
		lines = append(lines, ansi.Truncate(row, w-4, "…"))
	}
	body = strings.Join(lines, "\n")
	if f.onSkills {
		keys = m.th.help("↑↓", "select", "a", "add", "enter", "edit", "d", "remove", "tab", "description", "ctrl+s", "save", "esc", "cancel")
	} else {
		keys = m.th.help("tab", "skills", "enter", "save", "esc", "cancel")
	}
	return title, body, keys
}

func (m Model) editSkillBox() (title, body, keys string) {
	f := m.form
	title = "New skill"
	if f.skill >= 0 {
		title = "Edit skill"
	}
	var b strings.Builder
	for i, in := range f.fields {
		l := m.th.labelOff.Render(skillLabels[i])
		if i == f.field {
			l = m.th.labelOn.Render(skillLabels[i])
		}
		b.WriteString(l + "\n" + in.View())
		if i < len(f.fields)-1 {
			b.WriteString("\n\n")
		}
	}
	keys = m.th.help("tab", "next field", "enter", "done", "esc", "back")
	return title, b.String(), keys
}

// popupWidth is the width popups are drawn at.
func (m Model) popupWidth() int {
	return max(20, min(64, m.width-4))
}
