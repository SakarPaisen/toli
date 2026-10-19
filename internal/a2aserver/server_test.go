package a2aserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/a2aserver"
	"github.com/arjunvlama/toli/internal/backend"
	"github.com/arjunvlama/toli/internal/peerclient"
	"github.com/arjunvlama/toli/internal/store"
)

// setup runs the A2A handler over HTTP for a fresh backend; it returns the
// backend and the server's URL (also used as the public URL).
func setup(t *testing.T) (*backend.Backend, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "toli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b, err := backend.New(ctx, st, backend.Options{})
	if err != nil {
		t.Fatal(err)
	}

	var h http.Handler // the public URL is only known once the server is up
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)
	h = a2aserver.New(b, srv.URL)
	return b, srv.URL
}

func TestAgentCard(t *testing.T) {
	ctx := context.Background()
	b, url := setup(t)
	skills := []a2a.AgentSkill{{Name: "Write SQL migrations", Tags: []string{"db"}}}
	if _, err := b.AddAgent(ctx, "backend", "Owns the API", skills); err != nil {
		t.Fatal(err)
	}

	// Fetched the way AddPeer fetches a peer's card.
	card, err := peerclient.FetchCard(ctx, a2aserver.CardURL(url, "backend"))
	if err != nil {
		t.Fatalf("FetchCard: %v", err)
	}
	if card.Name != "backend" || card.Description != "Owns the API" || card.Version == "" {
		t.Errorf("identity: %q %q %q", card.Name, card.Description, card.Version)
	}
	// Skills are only on the extended card (for authenticated peers).
	if card.Skills == nil || len(card.Skills) != 0 || !card.Capabilities.ExtendedAgentCard {
		t.Errorf("public card: skills %+v, extended card advertised %v", card.Skills, card.Capabilities.ExtendedAgentCard)
	}
	// One endpoint for A2A 1.0 peers, one for 0.3 ones.
	ifs := card.SupportedInterfaces
	if len(ifs) != 2 ||
		ifs[0].URL != url+"/a2a/backend/" || ifs[0].ProtocolVersion != "1.0" ||
		ifs[1].URL != url+"/a2a/backend/v0.3/" || ifs[1].ProtocolVersion != "0.3" ||
		ifs[0].ProtocolBinding != a2a.TransportProtocolJSONRPC || ifs[1].ProtocolBinding != a2a.TransportProtocolJSONRPC {
		t.Errorf("interfaces: %+v %+v", ifs[0], ifs[len(ifs)-1])
	}
	if card.Capabilities.Streaming || card.Capabilities.PushNotifications {
		t.Errorf("capabilities: %+v (want no streaming, no push)", card.Capabilities)
	}
	if _, ok := card.SecuritySchemes["bearer"]; !ok || len(card.SecurityRequirements) != 1 {
		t.Errorf("security: schemes %+v, requirements %+v", card.SecuritySchemes, card.SecurityRequirements)
	}

	// An agent with no skills still gets a skills list (A2A requires one).
	if _, err := b.AddAgent(ctx, "frontend", "", nil); err != nil {
		t.Fatal(err)
	}
	if card, err := peerclient.FetchCard(ctx, a2aserver.CardURL(url, "frontend")); err != nil || card.Skills == nil {
		t.Errorf("agent without skills: %v, skills %v", err, card.Skills)
	}

	// Unknown agents are 404.
	resp, err := http.Get(url + "/a2a/nope/.well-known/agent-card.json")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown agent: %d, want 404", resp.StatusCode)
	}
}

func TestAuth(t *testing.T) {
	ctx := context.Background()
	b, url := setup(t)
	for _, name := range []string{"backend", "frontend"} {
		if _, err := b.AddAgent(ctx, name, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	// Peers get the tokens we issue when they're added. Our own agents stand in
	// as each other's peers (adding one fetches its card from this server).
	_, bobTok, err := b.AddPeer(ctx, "backend", "bob", a2aserver.CardURL(url, "frontend"), "x")
	if err != nil {
		t.Fatal(err)
	}
	_, carolTok, err := b.AddPeer(ctx, "frontend", "carol", a2aserver.CardURL(url, "backend"), "y")
	if err != nil {
		t.Fatal(err)
	}

	post := func(agent, tok string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", a2aserver.AgentURL(url, agent), nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	if resp := post("backend", ""); resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Errorf("no token: %d %q, want 401 with WWW-Authenticate", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	if resp := post("backend", "bogus"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown token: %d, want 401", resp.StatusCode)
	}
	if resp := post("backend", carolTok); resp.StatusCode != http.StatusForbidden {
		t.Errorf("another agent's peer's token: %d, want 403", resp.StatusCode)
	}
	if resp := post("backend", bobTok); !reachedA2A(resp) {
		t.Errorf("right token: %d, want it through to the A2A handler", resp.StatusCode)
	}

	if err := b.DisableAgent(ctx, "backend"); err != nil {
		t.Fatal(err)
	}
	if resp := post("backend", bobTok); resp.StatusCode != http.StatusForbidden {
		t.Errorf("disabled agent: %d, want 403", resp.StatusCode)
	}
	if err := b.EnableAgent(ctx, "backend"); err != nil {
		t.Fatal(err)
	}
	if resp := post("backend", bobTok); !reachedA2A(resp) {
		t.Errorf("re-enabled agent: %d, want it through to the A2A handler", resp.StatusCode)
	}

	// Cards stay public: no token needed.
	if _, err := peerclient.FetchCard(ctx, a2aserver.CardURL(url, "backend")); err != nil {
		t.Errorf("card without a token: %v", err)
	}
}

// reachedA2A reports whether a request got past auth to the JSON-RPC handler
// (which answers an empty body with a JSON-RPC error, not 401/403).
func reachedA2A(resp *http.Response) bool {
	return resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden &&
		resp.Header.Get("Content-Type") == "application/json"
}
