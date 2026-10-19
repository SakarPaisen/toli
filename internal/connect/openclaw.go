package connect

import (
	"errors"
	"fmt"
	"os/exec"
)

// openclaw is OpenClaw.
type openclaw struct{}

// openclawMain is the session toli connects in OpenClaw: the default
// agent's main session, which keeps its key across resets.
const openclawMain = "agent:main:main"

// connect adds the MCP server with OpenClaw's own CLI and registers
// the agent's main session with the daemon: it's long-lived (the Gateway
// runs it), so there's no session start to hook. Notes are delivered through
// the Gateway (chat.send). OpenClaw's config is per user, so there's no
// project scope.
func (openclaw) connect(o Options) ([]Done, error) {
	var done []Done
	add := []string{"mcp", "add", "toli", "--url", o.MCPURL, "--transport", "streamable-http"}
	switch err := o.Run(o.Dir, "openclaw", add...); {
	case errors.Is(err, exec.ErrNotFound):
		return nil, errors.New("openclaw isn't on your PATH: install OpenClaw (or open a new terminal), then retry")
	case err != nil:
		return nil, fmt.Errorf("openclaw mcp add: %w", err)
	default:
		done = append(done, Done{What: "MCP server toli in ~/.openclaw/openclaw.json"})
	}
	if err := o.Register("openclaw", openclawMain); err != nil {
		return nil, fmt.Errorf("register %s: %w", openclawMain, err)
	}
	return append(done,
		Done{What: "registered OpenClaw's main session (" + openclawMain + ")"},
		Done{What: "with several OpenClaw agents: toli's tools reach all of them (same toli agent); notifications go to main"},
	), nil
}

// disconnect removes the MCP server and unregisters the main
// session, which never ends on its own.
func (openclaw) disconnect(o Options) ([]Done, error) {
	var done []Done
	remove := []string{"mcp", "unset", "toli"}
	switch err := o.Run(o.Dir, "openclaw", remove...); {
	case errors.Is(err, exec.ErrNotFound):
		done = append(done, Done{What: "MCP server: openclaw isn't on your PATH", Manual: "openclaw " + shellJoin(remove)})
	case err != nil:
		done = append(done, Done{What: fmt.Sprintf("MCP server toli: %v", err)})
	default:
		done = append(done, Done{What: "removed MCP server toli from ~/.openclaw/openclaw.json"})
	}
	if err := o.Unregister(openclawMain); err != nil {
		done = append(done, Done{What: fmt.Sprintf("unregister %s: %v", openclawMain, err)})
	} else {
		done = append(done, Done{What: "unregistered OpenClaw's main session (" + openclawMain + ")"})
	}
	return done, nil
}
