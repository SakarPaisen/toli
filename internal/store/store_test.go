package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "toli.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addAgent(t *testing.T, s *Store, name string) {
	t.Helper()
	if err := s.AddAgent(context.Background(), Agent{Name: name, Card: a2a.AgentCard{Name: name}}, "hash-"+name); err != nil {
		t.Fatalf("AddAgent %s: %v", name, err)
	}
}

func TestOpenTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toli.db")
	for i := 0; i < 2; i++ {
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		var v int
		s.db.QueryRow("PRAGMA user_version").Scan(&v)
		s.Close()
		if v != len(migrations) {
			t.Fatalf("Open #%d: user_version = %d, want %d", i+1, v, len(migrations))
		}
	}
}

func TestPeers(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	addAgent(t, s, "backend")

	id, err := s.AddPeer(ctx, Peer{Agent: "backend", Name: "bob", URL: "https://bob", OutboundToken: "from-bob"}, "hash-for-bob")
	if err != nil || id == 0 {
		t.Fatalf("AddPeer: %v, id %d", err, id)
	}
	// No inbound token yet: NULL, so several of these don't collide on UNIQUE.
	if _, err := s.AddPeer(ctx, Peer{Agent: "backend", Name: "carol", URL: "https://carol"}, ""); err != nil {
		t.Fatalf("AddPeer carol: %v", err)
	}
	if _, err := s.AddPeer(ctx, Peer{Agent: "backend", Name: "dave", URL: "https://dave"}, ""); err != nil {
		t.Fatalf("AddPeer dave: %v", err)
	}

	if _, err := s.AddPeer(ctx, Peer{Agent: "backend", Name: "bob", URL: "x"}, ""); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate name: got %v, want ErrExists", err)
	}
	if _, err := s.AddPeer(ctx, Peer{Agent: "ghost", Name: "bob", URL: "x"}, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: got %v, want ErrNotFound", err)
	}

	got, err := s.GetPeer(ctx, id)
	if err != nil || got.Name != "bob" || got.OutboundToken != "from-bob" {
		t.Fatalf("GetPeer: %v, %+v", err, got)
	}
	byToken, err := s.PeerByInboundTokenHash(ctx, "hash-for-bob")
	if err != nil || byToken.ID != id {
		t.Fatalf("PeerByInboundTokenHash: %v, %+v", err, byToken)
	}
	if _, err := s.PeerByInboundTokenHash(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token: got %v, want ErrNotFound", err)
	}
	if _, err := s.GetPeer(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: got %v, want ErrNotFound", err)
	}

	addAgent(t, s, "frontend")
	if _, err := s.AddPeer(ctx, Peer{Agent: "frontend", Name: "bob", URL: "https://bob"}, ""); err != nil {
		t.Fatalf("AddPeer frontend/bob: %v", err)
	}
	if mine, _ := s.ListPeers(ctx, "backend"); len(mine) != 3 {
		t.Fatalf("ListPeers(backend) = %d peers, want 3", len(mine))
	}
	if all, err := s.ListAllPeers(ctx); err != nil || len(all) != 4 {
		t.Fatalf("ListAllPeers = %d peers, %v; want 4", len(all), err)
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	addAgent(t, s, "backend")

	sess, err := s.CreateSession(ctx, "backend", "users api")
	if err != nil || sess.ID == "" {
		t.Fatalf("CreateSession: %v, %+v", err, sess)
	}
	if _, err := s.CreateSession(ctx, "ghost", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: got %v, want ErrNotFound", err)
	}

	got, err := s.GetSession(ctx, sess.ID)
	if err != nil || got.Title != "users api" || got.Agent != "backend" {
		t.Fatalf("GetSession: %v, %+v", err, got)
	}

	if _, err := s.DeleteSession(ctx, "backend", sess.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := s.GetSession(ctx, sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session still there: %v", err)
	}
}

func TestRemove(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	addAgent(t, s, "backend")
	addAgent(t, s, "frontend")
	bob, _ := s.AddPeer(ctx, Peer{Agent: "backend", Name: "bob", URL: "https://bob"}, "")
	carol, _ := s.AddPeer(ctx, Peer{Agent: "backend", Name: "carol", URL: "https://carol"}, "")
	if _, err := s.AddPeer(ctx, Peer{Agent: "frontend", Name: "dave", URL: "https://dave"}, ""); err != nil {
		t.Fatal(err)
	}
	sess, _ := s.CreateSession(ctx, "backend", "x")

	// UpdatePeerCard
	if err := s.UpdatePeerCard(ctx, bob, &a2a.AgentCard{Name: "Bob's agent"}); err != nil {
		t.Fatalf("UpdatePeerCard: %v", err)
	}
	if p, _ := s.GetPeer(ctx, bob); p.Card == nil || p.Card.Name != "Bob's agent" {
		t.Fatalf("card not updated: %+v", p.Card)
	}
	if err := s.UpdatePeerCard(ctx, 9999, &a2a.AgentCard{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdatePeerCard unknown: %v, want ErrNotFound", err)
	}

	// RemovePeer
	if _, err := s.RemovePeer(ctx, carol); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}
	if _, err := s.GetPeer(ctx, carol); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed peer still there: %v", err)
	}
	if _, err := s.RemovePeer(ctx, carol); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RemovePeer twice: %v, want ErrNotFound", err)
	}

	// RemoveAgent cascades to its peers and sessions, and leaves other agents alone.
	if _, err := s.RemoveAgent(ctx, "backend"); err != nil {
		t.Fatalf("RemoveAgent: %v", err)
	}
	if _, err := s.GetPeer(ctx, bob); !errors.Is(err, ErrNotFound) {
		t.Fatalf("agent's peer survived: %v", err)
	}
	if _, err := s.GetSession(ctx, sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("agent's session survived: %v", err)
	}
	if all, _ := s.ListAllPeers(ctx); len(all) != 1 || all[0].Name != "dave" {
		t.Fatalf("other agent's peers affected: %+v", all)
	}
	if _, err := s.RemoveAgent(ctx, "backend"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RemoveAgent twice: %v, want ErrNotFound", err)
	}
}

// Peer ids are never reused, so an id held by in-flight work can't come to
// mean a different peer.
func TestPeerIDsNotReused(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	addAgent(t, s, "backend")
	first, _ := s.AddPeer(ctx, Peer{Agent: "backend", Name: "bob", URL: "x"}, "")
	if _, err := s.RemovePeer(ctx, first); err != nil {
		t.Fatal(err)
	}
	second, err := s.AddPeer(ctx, Peer{Agent: "backend", Name: "bob", URL: "x"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatalf("peer id %d was reused", first)
	}
}

func TestAgentNotify(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	addAgent(t, s, "backend")
	get := func() *Notify {
		t.Helper()
		a, err := s.GetAgent(ctx, "backend")
		if err != nil {
			t.Fatal(err)
		}
		return a.Notify
	}

	if n := get(); n != nil {
		t.Fatalf("new agent: notify = %+v, want none", n)
	}
	hook := &Notify{Webhook: "https://example.com/toli", Secret: "s3cret"}
	if err := s.SetAgentNotify(ctx, "backend", hook); err != nil {
		t.Fatal(err)
	}
	if n := get(); n == nil || *n != *hook {
		t.Fatalf("after setting a webhook: notify = %+v", n)
	}
	cmd := &Notify{Command: "/usr/local/bin/wake-backend"}
	if err := s.SetAgentNotify(ctx, "backend", cmd); err != nil {
		t.Fatal(err)
	}
	if n := get(); n == nil || *n != *cmd {
		t.Fatalf("after setting a command: notify = %+v", n)
	}
	if err := s.SetAgentNotify(ctx, "backend", nil); err != nil {
		t.Fatal(err)
	}
	if n := get(); n != nil {
		t.Fatalf("after clearing: notify = %+v", n)
	}
	if err := s.SetAgentNotify(ctx, "nobody", cmd); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: err = %v, want ErrNotFound", err)
	}
}
