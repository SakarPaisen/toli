package wake

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/arjunvlama/toli/internal/store"
)

func TestBridgeNotes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "toli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.AddAgent(ctx, store.Agent{Name: "backend", Card: a2a.AgentCard{Name: "backend"}}, "hash"); err != nil {
		t.Fatal(err)
	}
	s := New(st)
	if err := s.Register(ctx, "backend", "opencode", "ses1", nil); err != nil {
		t.Fatal(err)
	}

	// Nothing queued: waits, then returns none.
	if notes, err := s.TakeNotes(ctx, "backend", "ses1", 50*time.Millisecond); err != nil || notes != nil {
		t.Fatalf("empty: %v, %v", notes, err)
	}

	// Queued notes come out once, in order.
	s.Notify(ctx, "backend", "one")
	s.Notify(ctx, "backend", "two")
	if notes, _ := s.TakeNotes(ctx, "backend", "ses1", time.Second); !slices.Equal(notes, []string{"one", "two"}) {
		t.Fatalf("notes = %q", notes)
	}

	// A waiting take gets a note as soon as it's delivered.
	got := make(chan []string, 1)
	go func() {
		notes, _ := s.TakeNotes(ctx, "backend", "ses1", 5*time.Second)
		got <- notes
	}()
	time.Sleep(50 * time.Millisecond)
	s.Notify(ctx, "backend", "three")
	select {
	case notes := <-got:
		if !slices.Equal(notes, []string{"three"}) {
			t.Fatalf("waiting take = %q", notes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting take never got the note")
	}

	// A replaced session is told so. The new one starts with no notes (it
	// gets the pending work when it registers), then gets new ones.
	s.Notify(ctx, "backend", "four")
	if err := s.Register(ctx, "backend", "opencode", "ses2", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakeNotes(ctx, "backend", "ses1", time.Millisecond); !errors.Is(err, ErrSessionReplaced) {
		t.Fatalf("old session: %v, want ErrSessionReplaced", err)
	}
	s.Notify(ctx, "backend", "five")
	if notes, _ := s.TakeNotes(ctx, "backend", "ses2", time.Second); !slices.Equal(notes, []string{"five"}) {
		t.Fatalf("new session's notes = %q", notes)
	}
}

// Agents' queues are independent: many at once, each gets exactly its own.
func TestQueuesConcurrent(t *testing.T) {
	var n notes
	var wg sync.WaitGroup
	for i := range 20 {
		agent := fmt.Sprintf("agent%d", i)
		wg.Go(func() {
			for j := range 50 {
				n.put(agent, fmt.Sprint(j))
			}
		})
	}
	wg.Wait()
	for i := range 20 {
		notes := n.take(context.Background(), fmt.Sprintf("agent%d", i), time.Second)
		if len(notes) != 50 || notes[0] != "0" || notes[49] != "49" {
			t.Fatalf("agent%d: %d notes, %q…%q", i, len(notes), notes[0], notes[len(notes)-1])
		}
	}
}

// A full queue keeps the newest notes.
func TestQueueFull(t *testing.T) {
	var n notes
	for i := range maxQueued + 10 {
		n.put("a", fmt.Sprint(i))
	}
	notes := n.take(context.Background(), "a", time.Second)
	if len(notes) != maxQueued || notes[0] != "10" || notes[len(notes)-1] != fmt.Sprint(maxQueued+9) {
		t.Fatalf("%d notes, %q…%q", len(notes), notes[0], notes[len(notes)-1])
	}
}
