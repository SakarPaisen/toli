package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/arjunvlama/toli/internal/api"
)

// API calls never run inside Update. They run as commands (tea.Cmd): bubbletea
// runs the func in the background and feeds the returned message back into
// Update. List results carry the agent they're for, so a late answer for an
// agent that's no longer selected is ignored.

type (
	agentsLoadedMsg struct{ agents []api.AgentInfo }
	peersLoadedMsg  struct {
		agent string
		peers []api.PeerInfo
	}
	tasksLoadedMsg struct {
		agent string
		tab   taskTab
		tasks []api.TaskInfo
	}
	taskLoadedMsg struct {
		agent string
		tab   taskTab
		task  api.TaskDetail
	}

	agentAddedMsg   struct{ resp api.AddAgentResponse }
	peerAddedMsg    struct{ resp api.AddPeerResponse }
	tokenRotatedMsg struct{ resp api.RotateTokenResponse }
	peerRotatedMsg  struct {
		peer  api.PeerInfo
		token string
	}

	// doneMsg reports that a change (remove, disable, cancel, ...) went
	// through; everything on screen is reloaded.
	doneMsg struct{}

	errMsg  struct{ err error }
	tickMsg struct{}
)

// refreshEvery is how often the screen reloads on its own: tasks change as
// peers report, and nothing pushes those changes to the TUI.
const refreshEvery = 3 * time.Second

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

func call(f func(context.Context) (tea.Msg, error)) tea.Cmd {
	return func() tea.Msg {
		msg, err := f(context.Background())
		if err != nil {
			return errMsg{err: err}
		}
		return msg
	}
}

func loadAgents(c *api.Client) tea.Cmd {
	return call(func(ctx context.Context) (tea.Msg, error) {
		agents, err := c.ListAgents(ctx)
		return agentsLoadedMsg{agents: agents}, err
	})
}

func loadPeers(c *api.Client, agent string) tea.Cmd {
	return call(func(ctx context.Context) (tea.Msg, error) {
		peers, err := c.ListPeers(ctx, agent)
		return peersLoadedMsg{agent: agent, peers: peers}, err
	})
}

// loadTasks loads both of the agent's task lists.
func loadTasks(c *api.Client, agent string) tea.Cmd {
	return tea.Batch(
		call(func(ctx context.Context) (tea.Msg, error) {
			tasks, err := c.ListTasks(ctx, agent)
			return tasksLoadedMsg{agent: agent, tab: tabDelegated, tasks: tasks}, err
		}),
		call(func(ctx context.Context) (tea.Msg, error) {
			tasks, err := c.ListAssignedTasks(ctx, agent)
			return tasksLoadedMsg{agent: agent, tab: tabAssigned, tasks: tasks}, err
		}),
	)
}

func loadTask(c *api.Client, agent string, tab taskTab, id string) tea.Cmd {
	return call(func(ctx context.Context) (tea.Msg, error) {
		get := c.GetTask
		if tab == tabAssigned {
			get = c.GetAssignedTask
		}
		t, err := get(ctx, agent, id)
		return taskLoadedMsg{agent: agent, tab: tab, task: t}, err
	})
}

func addAgent(c *api.Client, req api.AddAgentRequest) tea.Cmd {
	return call(func(ctx context.Context) (tea.Msg, error) {
		resp, err := c.AddAgent(ctx, req)
		return agentAddedMsg{resp: resp}, err
	})
}

func addPeer(c *api.Client, req api.AddPeerRequest) tea.Cmd {
	return call(func(ctx context.Context) (tea.Msg, error) {
		resp, err := c.AddPeer(ctx, req)
		return peerAddedMsg{resp: resp}, err
	})
}

func rotateToken(c *api.Client, name string) tea.Cmd {
	return call(func(ctx context.Context) (tea.Msg, error) {
		resp, err := c.RotateAgentToken(ctx, name)
		return tokenRotatedMsg{resp: resp}, err
	})
}

func rotatePeerToken(c *api.Client, p api.PeerInfo) tea.Cmd {
	return call(func(ctx context.Context) (tea.Msg, error) {
		resp, err := c.RotatePeerToken(ctx, p.ID)
		return peerRotatedMsg{peer: p, token: resp.Token}, err
	})
}

// change runs an API call whose only result is success or an error.
func change(f func(context.Context) error) tea.Cmd {
	return call(func(ctx context.Context) (tea.Msg, error) {
		return doneMsg{}, f(ctx)
	})
}

func removeAgent(c *api.Client, name string) tea.Cmd {
	return change(func(ctx context.Context) error { return c.RemoveAgent(ctx, name) })
}

func setAgentDisabled(c *api.Client, name string, disabled bool) tea.Cmd {
	return change(func(ctx context.Context) error {
		if disabled {
			return c.DisableAgent(ctx, name)
		}
		return c.EnableAgent(ctx, name)
	})
}

func removePeer(c *api.Client, id int64) tea.Cmd {
	return change(func(ctx context.Context) error { return c.RemovePeer(ctx, id) })
}

func setPeerToken(c *api.Client, id int64, token string) tea.Cmd {
	return change(func(ctx context.Context) error { return c.SetPeerToken(ctx, id, token) })
}

func cancelTask(c *api.Client, agent, id string) tea.Cmd {
	return change(func(ctx context.Context) error {
		_, err := c.CancelTask(ctx, agent, id)
		return err
	})
}
