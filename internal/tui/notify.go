package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/arjunvlama/toli/internal/api"
)

// Setting where an agent's notifications are pushed when it has no harness
// session (n on an agent): one field, a program's path or a webhook URL,
// empty for none. A new webhook's secret is shown once.

type notifySetMsg struct{ secret string }

func setNotify(c *api.Client, name, target string) tea.Cmd {
	req := api.SetNotifyRequest{Command: target}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		req = api.SetNotifyRequest{Webhook: target}
	}
	return call(func(ctx context.Context) (tea.Msg, error) {
		resp, err := c.SetAgentNotify(ctx, name, req)
		return notifySetMsg{secret: resp.Secret}, err
	})
}

// notifyLabel is where an agent's notifications are pushed, for the detail
// pane.
func notifyLabel(n *api.NotifyInfo) string {
	switch {
	case n == nil:
		return "none"
	case n.Command != "":
		return "command " + n.Command
	}
	return "webhook " + n.Webhook
}

func (m Model) openNotify(a api.AgentInfo) (tea.Model, tea.Cmd) {
	m.popup, m.err = popupNotify, nil
	m.notifyInput = newInput("/path/to/program or https://…", 50, m.th)
	if a.Notify != nil && a.Notify.Command != "" {
		m.notifyInput.SetValue(a.Notify.Command) // a webhook isn't shown in full, so it can't be edited
	}
	cmd := m.notifyInput.Focus()
	return m, cmd
}

func (m Model) updateNotify(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.popup, m.err = popupNone, nil
		return m, nil
	case "enter":
		a, _ := m.selAgent()
		m.err = nil
		return m, setNotify(m.client, a.Name, strings.TrimSpace(m.notifyInput.Value()))
	}
	var cmd tea.Cmd
	m.notifyInput, cmd = m.notifyInput.Update(msg)
	return m, cmd
}

func (m Model) notifyBox(w int) (title, body, keys string) {
	a, _ := m.selAgent()
	title = "Notifications for " + a.Name
	body = strings.Join(m.wrap(w-6, "When "+a.Name+" has no harness session, push its notifications to a program (run with each one on stdin) or a webhook (POSTed each one). Empty: none."), "\n") +
		"\n\n" + m.th.labelOn.Render("Program (absolute path) or webhook URL") + "\n" + m.notifyInput.View() + "\n\n" +
		m.th.muted.Render("now: "+notifyLabel(a.Notify))
	return title, body, m.th.help("enter", "save", "esc", "cancel")
}

func (m Model) notifySecretBox(w int) (title, body, keys string) {
	a, _ := m.selAgent()
	note := m.th.warn.Render("Shown once.")
	if m.copied {
		note = m.th.success.UnsetBold().Render("✓ Copied to clipboard")
	}
	body = strings.Join(m.wrap(w-6, "toli sends this with each notification as \"Authorization: Bearer <secret>\". Have your webhook check it."), "\n") +
		"\n\n" + m.th.bold.Render(m.notifySecret) + "\n" + note
	return "Webhook secret for " + a.Name, body, m.th.help("c", "copy secret", "enter", "close")
}
