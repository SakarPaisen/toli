package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/arjunvlama/toli/internal/api"
)

func TestEditAgent(t *testing.T) {
	agents := agentsLoadedMsg{agents: []api.AgentInfo{{Name: "backend", Description: "Owns the API",
		Skills: []api.Skill{{ID: "write-sql", Name: "Write SQL", Description: "Postgres"}}}}}
	enter := tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	tab := tea.KeyPressMsg(tea.Key{Code: tea.KeyTab})
	esc := tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})

	var m tea.Model = New(nil, "127.0.0.1:7420")
	step := func(msgs ...tea.Msg) string {
		t.Helper()
		for _, msg := range msgs {
			m, _ = m.Update(msg)
		}
		return ansi.Strip(m.(Model).render())
	}
	want := func(screen string, texts ...string) {
		t.Helper()
		for _, s := range texts {
			if !strings.Contains(screen, s) {
				t.Errorf("screen lacks %q:\n%s", s, screen)
			}
		}
	}

	want(step(tea.WindowSizeMsg{Width: 110, Height: 40}, agents, key('e')),
		"Edit backend", "Description", "Owns the API", "Skills", "Write SQL", "Postgres")

	// A long description wraps onto more lines instead of scrolling sideways.
	long := " It owns the Go API and the Postgres schema for billing; ask it for endpoints and migrations."
	if s := step(tea.PasteMsg{Content: long}); !strings.Contains(s, "billing;") || !strings.Contains(s, "migrations.") {
		t.Errorf("long description not all visible:\n%s", s)
	}

	// Edit the existing skill: its fields are filled in; esc leaves it as is.
	want(step(tab, enter), "Edit skill", "Write SQL", "Postgres")
	step(esc)

	// Add one: name and description are required.
	want(step(key('a'), enter), "New skill", "name and description are required")
	step(tea.PasteMsg{Content: "Review schemas"}, tab, tea.PasteMsg{Content: "Checks migrations"},
		tab, tea.PasteMsg{Content: "db, review"}, tab, tea.PasteMsg{Content: "Review 0042; Check indexes"}, enter)
	f := m.(Model).form
	if len(f.skills) != 2 {
		t.Fatalf("skills = %+v", f.skills)
	}
	got := f.skills[1]
	if got.Name != "Review schemas" || got.Description != "Checks migrations" ||
		!slices.Equal(got.Tags, []string{"db", "review"}) || !slices.Equal(got.Examples, []string{"Review 0042", "Check indexes"}) {
		t.Fatalf("new skill = %+v", got)
	}
	if f.skills[0].ID != "write-sql" {
		t.Fatalf("existing skill lost its id: %+v", f.skills[0])
	}

	// Remove the first.
	step(key('k'), key('d'))
	if f := m.(Model).form; len(f.skills) != 1 || f.skills[0].Name != "Review schemas" {
		t.Fatalf("after remove: %+v", f.skills)
	}

	// esc cancels the whole edit.
	if s := step(esc); strings.Contains(s, "Edit backend") {
		t.Fatalf("esc didn't close the form:\n%s", s)
	}
}

// Adding an agent needs a description.
func TestAddAgentNeedsDescription(t *testing.T) {
	s := screen(t, 110, 32, key('a'), tea.PasteMsg{Content: "backend"}, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !strings.Contains(s, "description is required") {
		t.Fatalf("screen lacks the error:\n%s", s)
	}
}
