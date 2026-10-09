package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// confirmOption is a setting of a confirm dialog that cycles through its
// values with its key or a click, such as the propagation of a delete.
type confirmOption struct {
	key    string // single letter
	label  string
	values []string
	cur    int
}

func (o *confirmOption) value() string { return o.values[o.cur] }

// confirm is a modal yes/no dialog: y or the action button runs it, n, esc
// or Cancel closes it, enter presses the focused button. Destructive
// dialogs focus Cancel, so that a stray enter does nothing.
type confirm struct {
	title   string
	body    []string
	options []*confirmOption
	yes     string // the action button's label, e.g. "Delete"
	danger  bool
	onYes   func(c *confirm) tea.Cmd
	focusOK bool // enter presses the action button rather than Cancel

	rect    rect
	targets []confirmTarget // clickable regions, absolute, set when rendered
}

type confirmTarget struct {
	x0, x1, y int
	hit       func() (done bool, cmd tea.Cmd)
}

func newConfirm(title, yes string, danger bool, body []string, onYes func(*confirm) tea.Cmd, options ...*confirmOption) *confirm {
	return &confirm{title: title, yes: yes, danger: danger, body: body, onYes: onYes, options: options, focusOK: !danger}
}

func (c *confirm) option(key string) *confirmOption {
	for _, o := range c.options {
		if o.key == key {
			return o
		}
	}
	return nil
}

// Update handles a key. done closes the dialog; cmd is what confirming runs.
func (c *confirm) Update(msg tea.KeyPressMsg) (done bool, cmd tea.Cmd) {
	switch k := msg.String(); k {
	case "y":
		return true, c.onYes(c)
	case "n", "esc", "q":
		return true, nil
	case "enter":
		if c.focusOK {
			return true, c.onYes(c)
		}
		return true, nil
	case "tab", "shift+tab", "left", "right", "h", "l":
		c.focusOK = !c.focusOK
	default:
		if o := c.option(k); o != nil {
			o.cur = (o.cur + 1) % len(o.values)
		}
	}
	return false, nil
}

// Click handles a click at absolute coordinates; one outside the dialog
// cancels it.
func (c *confirm) Click(x, y int) (done bool, cmd tea.Cmd) {
	if !c.rect.contains(x, y) {
		return true, nil
	}
	for _, t := range c.targets {
		if y == t.y && x >= t.x0 && x < t.x1 {
			return t.hit()
		}
	}
	return false, nil
}

func (c *confirm) View(screenW, screenH int) string {
	w := min(64, screenW-4)
	iw := w - 2
	var lines []string
	for _, b := range c.body {
		lines = append(lines, " "+fit(b, iw-2))
	}
	type pending struct {
		row, x0, x1 int
		hit         func() (bool, tea.Cmd)
	}
	var hits []pending
	if len(c.options) > 0 {
		lines = append(lines, "")
		for _, o := range c.options {
			o := o
			s := " " + stAccent.Bold(true).Render(o.key) + " " + stMuted.Render(o.label+":") + " " + o.value()
			hits = append(hits, pending{len(lines), 0, iw, func() (bool, tea.Cmd) {
				o.cur = (o.cur + 1) % len(o.values)
				return false, nil
			}})
			lines = append(lines, s)
		}
	}
	lines = append(lines, "")
	button := func(label string, focused, danger bool) string {
		st := lipgloss.NewStyle().Padding(0, 1).Foreground(colFg).Background(colSelBgLo)
		if focused {
			st = st.Bold(true).Background(colSelBg)
			if danger {
				st = st.Background(colRed).Foreground(colBarBg)
			}
		}
		return st.Render(label)
	}
	ok := button("y "+c.yes, c.focusOK, c.danger)
	cancel := button("n Cancel", !c.focusOK, false)
	row := len(lines)
	pad := iw - ansi.StringWidth(ok) - ansi.StringWidth(cancel) - 3
	lines = append(lines, strings.Repeat(" ", max(pad, 1))+ok+"  "+cancel)
	okX := max(pad, 1)
	hits = append(hits,
		pending{row, okX, okX + ansi.StringWidth(ok), func() (bool, tea.Cmd) { return true, c.onYes(c) }},
		pending{row, okX + ansi.StringWidth(ok) + 2, okX + ansi.StringWidth(ok) + 2 + ansi.StringWidth(cancel), func() (bool, tea.Cmd) { return true, nil }})

	box := frame(c.title, "", lines, w, len(lines)+2, true)
	c.rect = rect{(screenW - w) / 2, max((screenH-lipgloss.Height(box))/2, 0), w, lipgloss.Height(box)}
	c.targets = c.targets[:0]
	for _, h := range hits {
		c.targets = append(c.targets, confirmTarget{c.rect.x + 1 + h.x0, c.rect.x + 1 + h.x1, c.rect.y + 1 + h.row, h.hit})
	}
	return box
}
