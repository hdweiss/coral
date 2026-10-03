package ui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hdweiss/coralctl/internal/config"
)

// Messages for pinning, emitted by the nav and pins panels.
type (
	togglePinMsg struct{ pin config.Pin }
	openPinMsg   struct{ pin config.Pin }
)

// pinsView is the box of pinned clusters and namespaces above the navigator.
type pinsView struct {
	rect    rect
	focused bool

	pins     []config.Pin
	known    func(ctx string) bool // whether the context exists in the kubeconfig
	cursor   int
	offset   int
	ctx, ns  string // the current location, highlighted when pinned
	nsScoped bool   // whether the current view is namespaced
}

func (p *pinsView) has(pin config.Pin) bool {
	for _, q := range p.pins {
		if q == pin {
			return true
		}
	}
	return false
}

// toggle adds pin at the end, or removes it if it is already pinned. It
// reports whether the pin is now present.
func (p *pinsView) toggle(pin config.Pin) bool {
	for i, q := range p.pins {
		if q == pin {
			p.pins = append(p.pins[:i:i], p.pins[i+1:]...)
			p.cursor = clamp(p.cursor, 0, len(p.pins)-1)
			return false
		}
	}
	p.pins = append(p.pins, pin)
	return true
}

func (p *pinsView) height() int { return p.rect.h - 2 }

// wantHeight is the box height that shows every pin.
func (p *pinsView) wantHeight() int {
	if len(p.pins) == 0 {
		return 0
	}
	return len(p.pins) + 2
}

func (p *pinsView) Update(msg tea.Msg) tea.Cmd {
	if len(p.pins) == 0 {
		return nil
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		pin := p.pins[p.cursor]
		switch msg.String() {
		case "enter", "space", "right", "l":
			return emit(openPinMsg{pin})
		case "p", "d", "x", "delete", "backspace":
			return emit(togglePinMsg{pin})
		default:
			if c, ok := moveCursor(msg.String(), p.cursor, len(p.pins), p.height()); ok {
				p.cursor = c
			}
		}
	case clickMsg:
		i := p.offset + msg.y
		if i < 0 || i >= len(p.pins) {
			return nil
		}
		p.cursor = i
		if msg.x >= p.rect.w-2-len(" x ") { // the × at the right edge
			return emit(togglePinMsg{p.pins[i]})
		}
		return emit(openPinMsg{p.pins[i]})
	case wheelMsg:
		p.offset = clamp(p.offset+msg.delta, 0, max(len(p.pins)-p.height(), 0))
		return nil
	}
	p.offset = scrollTo(p.cursor, p.offset, p.height())
	return nil
}

// active reports whether pin is where the user currently is. A cluster pin
// is active anywhere in its cluster unless a namespace pin matches better.
func (p *pinsView) active(pin config.Pin) bool {
	if pin.Context != p.ctx {
		return false
	}
	if pin.Namespace != "" {
		return p.nsScoped && pin.Namespace == p.ns
	}
	return !(p.nsScoped && p.has(config.Pin{Context: p.ctx, Namespace: p.ns}))
}

func (p *pinsView) View() string {
	iw := p.rect.w - 2
	lines := make([]string, 0, p.height())
	for i := p.offset; i < len(p.pins) && len(lines) < p.height(); i++ {
		lines = append(lines, p.renderLine(p.pins[i], i == p.cursor, iw))
	}
	return frame("Pinned", "", lines, p.rect.w, p.rect.h, p.focused)
}

func (p *pinsView) renderLine(pin config.Pin, selected bool, w int) string {
	const unpin = " × "
	lw := w - ansi.StringWidth(unpin)
	missing := p.known != nil && !p.known(pin.Context)

	if selected {
		label := " ⎈ " + pin.Context
		if pin.Namespace != "" {
			label = " " + pin.Context + " › " + pin.Namespace
		}
		st := stSelLo
		if p.focused {
			st = stSel
		}
		return st.Render(fit(label, lw) + unpin)
	}

	var label string
	switch {
	case missing:
		label = stErr.Render(" " + pin.Context + " (missing)")
	case pin.Namespace == "":
		st := stBold
		if p.active(pin) {
			st = stAccent.Bold(true)
		}
		label = " " + st.Render("⎈ "+pin.Context)
	default:
		st := stMuted
		ns := lipgloss.NewStyle()
		if p.active(pin) {
			ns = stAccent.Bold(true)
		}
		label = " " + st.Render(pin.Context+" › ") + ns.Render(pin.Namespace)
	}
	return fit(label, lw) + stMuted.Render(unpin)
}
