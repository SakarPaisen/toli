package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

// Design rules (after Crush and OpenClaw):
//   - Body text uses the terminal's own foreground, so it reads on any theme.
//   - One accent color (primary), used sparingly: logo, selection, focus, keys.
//   - Other colors only carry meaning: success, error, warning.
//   - Hierarchy comes from bold vs. muted, not from more hues.
//   - Light/dark variants are picked from the terminal's actual background.
//
// Colors are from Charm's CharmTone palette (the one Crush uses).

// theme holds every style the views use. It's rebuilt when the terminal
// reports its background color, so light and dark terminals both look right.
type theme struct {
	logo, status lipgloss.Style
	rule         lipgloss.Style // horizontal separators
	section      lipgloss.Style // "AGENTS" heading
	count        lipgloss.Style
	muted        lipgloss.Style // secondary text
	bold         lipgloss.Style // primary text, default foreground

	selBar  lipgloss.Style // ▌ next to the selected row
	selName lipgloss.Style

	labelOn, labelOff lipgloss.Style // form labels, focused / not

	success, warn, errText lipgloss.Style
	urlBox                 lipgloss.Style

	key lipgloss.Style // key names in the help line

	input textinput.Styles
	area  textarea.Styles
}

func newTheme(isDark bool) theme {
	ld := lipgloss.LightDark(isDark)
	var (
		primary = ld(lipgloss.Color("#6B50FF"), lipgloss.Color("#8B75FF")) // Charple / Hazy
		success = ld(lipgloss.Color("#00A475"), lipgloss.Color("#12C78F")) // Pickle / Guac
		danger  = ld(lipgloss.Color("#EB4268"), lipgloss.Color("#FF577D")) // Sriracha / Coral
		warning = lipgloss.Color("#FF985A")                                // Tang
		muted   = lipgloss.Color("#858392")                                // Squid
		subtle  = ld(lipgloss.Color("#BFBCC8"), lipgloss.Color("#4D4C57")) // Smoke / Iron
	)

	in := textinput.DefaultStyles(isDark)
	in.Focused.Prompt = lipgloss.NewStyle().Foreground(primary)
	in.Blurred.Prompt = lipgloss.NewStyle().Foreground(subtle)
	in.Focused.Placeholder = lipgloss.NewStyle().Foreground(muted)
	in.Blurred.Placeholder = lipgloss.NewStyle().Foreground(subtle)
	in.Focused.Text = lipgloss.NewStyle()
	in.Blurred.Text = lipgloss.NewStyle().Foreground(muted)

	area := textarea.DefaultStyles(isDark)
	for _, st := range []*textarea.StyleState{&area.Focused, &area.Blurred} {
		st.Base, st.CursorLine, st.EndOfBuffer = lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle()
	}
	area.Focused.Prompt, area.Blurred.Prompt = in.Focused.Prompt, in.Blurred.Prompt
	area.Focused.Placeholder, area.Blurred.Placeholder = in.Focused.Placeholder, in.Blurred.Placeholder
	area.Focused.Text, area.Blurred.Text = in.Focused.Text, in.Blurred.Text

	return theme{
		logo:    lipgloss.NewStyle().Bold(true).Foreground(primary),
		status:  lipgloss.NewStyle().Foreground(muted),
		rule:    lipgloss.NewStyle().Foreground(subtle),
		section: lipgloss.NewStyle().Bold(true),
		count:   lipgloss.NewStyle().Foreground(muted),
		muted:   lipgloss.NewStyle().Foreground(muted),
		bold:    lipgloss.NewStyle().Bold(true),

		selBar:  lipgloss.NewStyle().Foreground(primary),
		selName: lipgloss.NewStyle().Bold(true),

		labelOn:  lipgloss.NewStyle().Bold(true).Foreground(primary),
		labelOff: lipgloss.NewStyle().Foreground(muted),

		success: lipgloss.NewStyle().Bold(true).Foreground(success),
		warn:    lipgloss.NewStyle().Foreground(warning),
		errText: lipgloss.NewStyle().Foreground(danger),
		urlBox:  lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(subtle).Padding(0, 1),

		key: lipgloss.NewStyle().Bold(true),

		input: in,
		area:  area,
	}
}

// help renders key hints as "key desc  key desc".
func (t theme) help(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, t.key.Render(pairs[i])+" "+t.muted.Render(pairs[i+1]))
	}
	return strings.Join(parts, "   ")
}
