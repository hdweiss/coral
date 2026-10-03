package ui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type paletteItem struct {
	cmd     string
	desc    string
	aliases []string
}

// palette is the ":" command palette: a text input with ranked suggestions.
type palette struct {
	input   textinput.Model
	items   []paletteItem
	matches []paletteItem
	cursor  int
	offset  int
	rect    rect // absolute position of the box, set when rendered
}

const paletteRows = 10

func newPalette(items []paletteItem, initial string) *palette {
	ti := textinput.New()
	ti.Prompt = ": "
	ti.Placeholder = "resource, ns <name>, ctx <name>, quit"
	st := textinput.DefaultDarkStyles()
	st.Focused.Prompt = stAccent.Bold(true)
	st.Focused.Placeholder = stMuted
	ti.SetStyles(st)
	ti.SetValue(initial)
	ti.CursorEnd()
	ti.Focus()
	p := &palette{input: ti, items: items}
	p.filter()
	return p
}

func (p *palette) filter() {
	q := strings.ToLower(strings.TrimSpace(p.input.Value()))
	type scored struct {
		item  paletteItem
		score int
	}
	var out []scored
	for _, it := range p.items {
		s := matchScore(q, it)
		if s >= 0 {
			out = append(out, scored{it, s})
		}
	}
	slices.SortStableFunc(out, func(a, b scored) int { return a.score - b.score })
	p.matches = p.matches[:0]
	for _, s := range out {
		p.matches = append(p.matches, s.item)
	}
	p.cursor, p.offset = 0, 0
}

// matchScore ranks how well q matches an item; lower is better, -1 is no match.
func matchScore(q string, it paletteItem) int {
	if q == "" {
		return 0
	}
	cmd := strings.ToLower(it.cmd)
	if cmd == q || slices.Contains(it.aliases, q) {
		return 0
	}
	if strings.HasPrefix(cmd, q) {
		return 1
	}
	for _, a := range it.aliases {
		if strings.HasPrefix(a, q) {
			return 2
		}
	}
	if strings.Contains(cmd+" "+strings.ToLower(it.desc), q) {
		return 3
	}
	return -1
}

// selected returns the command to run on enter.
func (p *palette) selected() string {
	if p.cursor < len(p.matches) {
		return p.matches[p.cursor].cmd
	}
	return strings.TrimSpace(p.input.Value())
}

// Update handles input; it returns done=true with the command to run, or
// done=true with an empty command when the palette was dismissed.
func (p *palette) Update(msg tea.Msg) (cmd tea.Cmd, done bool, run string) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			return nil, true, ""
		case "enter":
			return nil, true, p.selected()
		case "up", "ctrl+p":
			p.cursor = clamp(p.cursor-1, 0, len(p.matches)-1)
			p.offset = scrollTo(p.cursor, p.offset, paletteRows)
			return nil, false, ""
		case "down", "ctrl+n":
			p.cursor = clamp(p.cursor+1, 0, len(p.matches)-1)
			p.offset = scrollTo(p.cursor, p.offset, paletteRows)
			return nil, false, ""
		case "tab":
			if p.cursor < len(p.matches) {
				p.input.SetValue(p.matches[p.cursor].cmd + " ")
				p.input.CursorEnd()
				p.filter()
			}
			return nil, false, ""
		}
	}
	before := p.input.Value()
	var c tea.Cmd
	p.input, c = p.input.Update(msg)
	if p.input.Value() != before {
		p.filter()
	}
	return c, false, ""
}

// Click handles a click at absolute coordinates.
func (p *palette) Click(x, y int) (done bool, run string) {
	if !p.rect.contains(x, y) {
		return true, ""
	}
	row := y - p.rect.y - 3 // border, input, separator
	if i := p.offset + row; row >= 0 && i < len(p.matches) {
		return true, p.matches[i].cmd
	}
	return false, ""
}

func (p *palette) Wheel(delta int) {
	p.cursor = clamp(p.cursor+delta, 0, len(p.matches)-1)
	p.offset = scrollTo(p.cursor, p.offset, paletteRows)
}

func (p *palette) View(screenW int) string {
	w := min(72, screenW-4)
	iw := w - 2
	p.input.SetWidth(iw - 3)
	lines := []string{p.input.View(), stMuted.Render(strings.Repeat("─", iw))}
	for i := p.offset; i < len(p.matches) && i < p.offset+paletteRows; i++ {
		it := p.matches[i]
		cmdW := 28
		line := " " + fit(it.cmd, cmdW) + " " + it.desc
		if i == p.cursor {
			lines = append(lines, stSel.Render(fit(line, iw)))
		} else {
			lines = append(lines, " "+stAccent.Render(fit(it.cmd, cmdW))+" "+stMuted.Render(it.desc))
		}
	}
	if len(p.matches) == 0 {
		lines = append(lines, stMuted.Render(" no matches, enter runs it as typed"))
	}
	footer := ""
	if len(p.matches) > paletteRows {
		footer = "↑↓ more"
	}
	box := frame("Command", footer, lines, w, len(lines)+2, true)
	p.rect = rect{(screenW - w) / 2, 2, w, lipgloss.Height(box)}
	return box
}
