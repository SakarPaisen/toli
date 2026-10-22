// Temporary: a real Codex wake on Windows. toli's daemon runs as the
// installed scheduled task; Codex runs as its app-server daemon with a
// thread started through `codex app-server proxy`; a peer delegates; the
// daemon wakes the idle thread with `codex queue`; the model completes it.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/net/websocket"

	"github.com/arjunvlama/toli/internal/api"
)

func main() {
	go func() {
		time.Sleep(15 * time.Minute)
		fmt.Println("FAIL: still running after 15 minutes")
		os.Exit(1)
	}()
	if err := run(); err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS")
}

func step(format string, args ...any) { fmt.Printf("\n=== "+format+"\n", args...) }

func run() error {
	ctx := context.Background()
	home, _ := os.UserHomeDir()
	tok, err := os.ReadFile(filepath.Join(home, ".toli", "admin-token"))
	if err != nil {
		return err
	}
	c := api.NewClient("http://127.0.0.1:7421", strings.TrimSpace(string(tok)))

	step("agents and pairing")
	lead, err := c.AddAgent(ctx, api.AddAgentRequest{Name: "lead"})
	if err != nil {
		return err
	}
	worker, err := c.AddAgent(ctx, api.AddAgentRequest{Name: "worker", Description: "Codex on Windows"})
	if err != nil {
		return err
	}
	card := func(a string) string { return "http://127.0.0.1:7422/a2a/" + a + "/.well-known/agent-card.json" }
	onLead, err := c.AddPeer(ctx, api.AddPeerRequest{Agent: "lead", Name: "worker", URL: card("worker")})
	if err != nil {
		return err
	}
	onWorker, err := c.AddPeer(ctx, api.AddPeerRequest{Agent: "worker", Name: "lead", URL: card("lead"), Token: onLead.Token})
	if err != nil {
		return err
	}
	if err := c.SetPeerToken(ctx, onLead.Peer.ID, onWorker.Token); err != nil {
		return err
	}
	workerURL := "http://127.0.0.1:7420/mcp/" + worker.Token

	step("toli connect codex worker --scope user")
	out, err := exec.Command("toli", "connect", "codex", "--mcp-url", workerURL, "--scope", "user").CombinedOutput()
	fmt.Print(redact(string(out), worker.Token))
	if err != nil {
		return err
	}
	hooks, _ := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	fmt.Println(redact(string(hooks), worker.Token))

	step("codex app-server daemon start")
	// Output to a file: the daemon it leaves running would hold a pipe open.
	logf, _ := os.CreateTemp("", "codex-daemon-*.txt")
	start := exec.Command("codex", "app-server", "daemon", "start")
	start.Stdout, start.Stderr = logf, logf
	err = start.Run()
	logf.Close()
	b, _ := os.ReadFile(logf.Name())
	fmt.Print(string(b))
	if err != nil {
		return err
	}
	var socketPath string
	for _, line := range strings.Split(string(b), "\n") {
		var st struct{ SocketPath string }
		if json.Unmarshal([]byte(line), &st) == nil && st.SocketPath != "" {
			socketPath = st.SocketPath
		}
	}
	if socketPath == "" {
		return fmt.Errorf("no socketPath in daemon start's output")
	}

	step("thread via the app-server daemon's socket (WebSocket over a Unix socket)")
	p, err := dialDaemon(socketPath)
	if err != nil {
		return err
	}
	if _, err := p.call("initialize", map[string]any{"clientInfo": map[string]any{"name": "toli-e2e", "version": "0"}}); err != nil {
		return err
	}
	p.notify("initialized", nil)
	work, _ := os.MkdirTemp("", "work")
	res, err := p.call("thread/start", map[string]any{
		"cwd": work, "approvalPolicy": "never", "sandbox": "workspace-write",
		"config": map[string]any{"bypass_hook_trust": true},
	})
	if err != nil {
		return err
	}
	var ts struct {
		Thread struct{ ID string } `json:"thread"`
	}
	json.Unmarshal(res, &ts)
	fmt.Println("thread", ts.Thread.ID)
	harness(ctx, c)

	step("first turn (Codex runs SessionStart hooks around here)")
	if _, err := p.call("turn/start", map[string]any{"threadId": ts.Thread.ID, "input": []any{map[string]any{"type": "text", "text": "Reply with the single word: ready"}}}); err != nil {
		return err
	}
	p.waitFor("turn/completed", 3*time.Minute)
	if h := harness(ctx, c); h != "codex" {
		return fmt.Errorf("worker's session didn't register (harness %q)", h)
	}

	step("lead delegates; the daemon should wake the idle thread with codex queue")
	ls, err := mcp.NewClient(&mcp.Implementation{Name: "lead", Version: "0"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: "http://127.0.0.1:7420/mcp/" + lead.Token}, nil)
	if err != nil {
		return err
	}
	r, err := ls.CallTool(ctx, &mcp.CallToolParams{Name: "delegate", Arguments: map[string]any{"peer": "worker",
		"message": "Automated wake test. Accept this task (accept_task), then complete it with update_task, state completed, message 'ok'. Nothing else."}})
	if err != nil {
		return err
	}
	var d struct {
		TaskID string `json:"task_id"`
	}
	raw, _ := json.Marshal(r.StructuredContent)
	json.Unmarshal(raw, &d)
	fmt.Println("task", d.TaskID)

	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		r, err := ls.CallTool(ctx, &mcp.CallToolParams{Name: "get_delegated_task_updates", Arguments: map[string]any{"task_id": d.TaskID}})
		if err == nil {
			raw, _ := json.Marshal(r.StructuredContent)
			var u struct{ State string }
			json.Unmarshal(raw, &u)
			if u.State == "completed" {
				fmt.Println(string(raw))
				return nil
			}
			fmt.Println("state:", u.State)
		}
		time.Sleep(10 * time.Second)
	}
	return fmt.Errorf("task not completed within 5 minutes")
}

func harness(ctx context.Context, c *api.Client) string {
	agents, _ := c.ListAgents(ctx)
	for _, a := range agents {
		if a.Name == "worker" {
			fmt.Printf("worker harness: %q\n", a.Harness)
			return a.Harness
		}
	}
	return ""
}

func redact(s, tok string) string { return strings.ReplaceAll(s, tok, "<token>") }

// proxy speaks JSON-RPC to Codex's app-server daemon: WebSocket messages
// over its Unix socket, as Codex's own client does.
type proxy struct {
	ws    *websocket.Conn
	lines chan map[string]any
	id    int
}

func dialDaemon(socketPath string) (*proxy, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, err
	}
	cfg, err := websocket.NewConfig("ws://localhost/rpc", "http://localhost/")
	if err != nil {
		return nil, err
	}
	ws, err := websocket.NewClient(cfg, conn)
	if err != nil {
		return nil, fmt.Errorf("websocket handshake: %w", err)
	}
	ws.MaxPayloadBytes = 128 << 20
	p := &proxy{ws: ws, lines: make(chan map[string]any, 1000)}
	go func() {
		for {
			var msg string
			if err := websocket.Message.Receive(ws, &msg); err != nil {
				fmt.Println("  (socket closed:", err, ")")
				close(p.lines)
				return
			}
			var m map[string]any
			if json.Unmarshal([]byte(msg), &m) == nil {
				if meth, _ := m["method"].(string); meth != "" && !strings.Contains(meth, "delta") {
					fmt.Println("  <-", meth)
					if meth == "error" || meth == "warning" || meth == "configWarning" || meth == "turn/completed" {
						b, _ := json.Marshal(m["params"])
						fmt.Println("     ", truncate(string(b)))
					}
				}
				p.lines <- m
			}
		}
	}()
	return p, nil
}

func (p *proxy) send(m map[string]any) {
	b, _ := json.Marshal(m)
	websocket.Message.Send(p.ws, string(b))
}

func (p *proxy) notify(method string, params any) {
	m := map[string]any{"method": method}
	if params != nil {
		m["params"] = params
	}
	p.send(m)
}

func (p *proxy) call(method string, params any) (json.RawMessage, error) {
	p.id++
	id := p.id
	p.send(map[string]any{"id": id, "method": method, "params": params})
	timeout := time.After(2 * time.Minute)
	for {
		select {
		case m, ok := <-p.lines:
			if !ok {
				return nil, fmt.Errorf("%s: proxy closed", method)
			}
			if fmt.Sprint(m["id"]) != fmt.Sprint(id) || m["method"] != nil {
				continue
			}
			if e, ok := m["error"]; ok {
				return nil, fmt.Errorf("%s: %v", method, e)
			}
			b, _ := json.Marshal(m["result"])
			fmt.Printf("  %s -> %s\n", method, truncate(string(b)))
			return b, nil
		case <-timeout:
			return nil, fmt.Errorf("%s: no answer", method)
		}
	}
}

func (p *proxy) waitFor(method string, d time.Duration) {
	timeout := time.After(d)
	for {
		select {
		case m, ok := <-p.lines:
			if !ok || m["method"] == method {
				return
			}
		case <-timeout:
			fmt.Println("  (timed out waiting for", method+")")
			return
		}
	}
}

func truncate(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
