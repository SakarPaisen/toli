// Package hook is the protocol between coding agents' hooks and the daemon,
// both sides: the daemon's /hook/ endpoints (server.go), and `toli hook`,
// which the hooks run (client.go). See ARCHITECTURE.md.
package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// harness is what toli hook needs to know about one coding agent.
type harness struct {
	// address is how to reach the session, from the hook's environment.
	address func(getenv func(string) string) map[string]string
	// context is what to print at session start so the agent sees text.
	context func(text string) any
}

// hookSpecific is the hook output Claude Code and Codex read context from.
func hookSpecific(event string) func(string) any {
	return func(text string) any {
		return map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": event, "additionalContext": text}}
	}
}

var harnesses = map[string]harness{
	"claude-code": {
		address: func(getenv func(string) string) map[string]string {
			return map[string]string{
				"socket":       getenv("CLAUDE_CODE_MESSAGING_SOCKET"),
				"socket_token": getenv("CLAUDE_CODE_MESSAGING_TOKEN"),
			}
		},
		context: hookSpecific("SessionStart"),
	},
	"codex": {
		context: hookSpecific("SessionStart"),
	},
}

// Events toli hook handles.
const (
	EventSessionStart = "session-start"
	EventSessionEnd   = "session-end"
)

// Call is one run of toli hook.
type Call struct {
	Harness string
	Event   string
	MCPURL  string // the agent's MCP URL: the daemon's address and the agent's token
	Stdin   io.Reader
	Stdout  io.Writer
	Getenv  func(string) string
}

// Run reports the hook's event to the daemon and prints what the harness
// should show the agent. Errors in the call itself (unknown harness or event,
// a bad URL) are returned; it's up to the caller not to fail the hook over a
// daemon that's down.
func Run(ctx context.Context, c Call) error {
	h, ok := harnesses[c.Harness]
	if !ok {
		return fmt.Errorf("unknown harness %q", c.Harness)
	}
	base, tok, err := parseMCPURL(c.MCPURL)
	if err != nil {
		return err
	}
	var in struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(c.Stdin).Decode(&in); err != nil {
		return fmt.Errorf("read the hook's input: %w", err)
	}
	endpoint := base + "/hook/" + url.PathEscape(tok) + "/" + c.Event

	switch c.Event {
	case EventSessionStart:
		req := SessionStart{Harness: c.Harness, SessionID: in.SessionID}
		if h.address != nil {
			req.Address = nonEmpty(h.address(c.Getenv))
		}
		var resp SessionStartResponse
		if err := post(ctx, endpoint, req, &resp); err != nil {
			return err
		}
		if resp.Context == "" {
			return nil
		}
		return json.NewEncoder(c.Stdout).Encode(h.context(resp.Context))
	case EventSessionEnd:
		return post(ctx, endpoint, SessionEnd{SessionID: in.SessionID}, nil)
	default:
		return fmt.Errorf("unknown event %q (want %s or %s)", c.Event, EventSessionStart, EventSessionEnd)
	}
}

// parseMCPURL splits http://host:port/mcp/<token> into the daemon's base URL
// and the token.
func parseMCPURL(raw string) (base, tok string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("MCP URL: %w", err)
	}
	tok, ok := strings.CutPrefix(u.Path, "/mcp/")
	if !ok || tok == "" {
		return "", "", fmt.Errorf("MCP URL %q: want http://host:port/mcp/<token>", raw)
	}
	return u.Scheme + "://" + u.Host, tok, nil
}

func post(ctx context.Context, endpoint string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return do(req, out)
}

// do sends req and decodes a 2xx response's JSON into out (if non-nil).
func do(req *http.Request, out any) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("reach the toli daemon: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e struct{ Error string }
		json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("toli daemon: %s: %s", resp.Status, e.Error)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func nonEmpty(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// Agent returns the name of the agent an MCP URL belongs to, which also checks
// the daemon can be reached with it.
func Agent(ctx context.Context, mcpURL string) (string, error) {
	base, tok, err := parseMCPURL(mcpURL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/hook/"+url.PathEscape(tok)+"/agent", nil)
	if err != nil {
		return "", err
	}
	var out AgentResponse
	if err := do(req, &out); err != nil {
		return "", err
	}
	return out.Agent, nil
}

// Register registers a session for the agent behind mcpURL directly, for
// harnesses whose sessions don't run hooks (e.g. OpenClaw's long-lived main
// session). Returns the agent's pending work, as at session start.
func Register(ctx context.Context, mcpURL, harness, sessionID string) (string, error) {
	base, tok, err := parseMCPURL(mcpURL)
	if err != nil {
		return "", err
	}
	var resp SessionStartResponse
	err = post(ctx, base+"/hook/"+url.PathEscape(tok)+"/"+EventSessionStart, SessionStart{Harness: harness, SessionID: sessionID}, &resp)
	return resp.Context, err
}

// Unregister drops a session registered with Register (if it's still the
// agent's session).
func Unregister(ctx context.Context, mcpURL, sessionID string) error {
	base, tok, err := parseMCPURL(mcpURL)
	if err != nil {
		return err
	}
	return post(ctx, base+"/hook/"+url.PathEscape(tok)+"/"+EventSessionEnd, SessionEnd{SessionID: sessionID}, nil)
}
