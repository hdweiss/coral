package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/hdweiss/coralctl/internal/yamltree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// detailView shows the selected object as a foldable YAML tree.
type detailView struct {
	rect    rect
	focused bool

	obj    *unstructured.Unstructured
	root   *yamltree.Node
	lines  []*yamltree.Node
	cursor int
	offset int
}

// SetObject shows obj. When obj is the same object as before (e.g. after a
// refresh), folding and cursor position are kept.
func (d *detailView) SetObject(obj *unstructured.Unstructured) {
	if obj == d.obj {
		return
	}
	if obj == nil {
		d.obj, d.root, d.lines = nil, nil, nil
		return
	}
	same := d.obj != nil && d.obj.GetUID() == obj.GetUID()
	if same && d.obj.GetResourceVersion() == obj.GetResourceVersion() {
		d.obj = obj
		return
	}
	var state map[string]bool
	cursorPath := ""
	if same {
		state = d.root.ExpansionState()
		if c := d.current(); c != nil {
			cursorPath = c.Path
		}
	}
	d.obj = obj
	d.root = yamltree.Build(obj.Object)
	if same {
		d.root.ApplyExpansionState(state)
	} else {
		d.cursor, d.offset = 0, 0
	}
	d.relayout()
	if cursorPath != "" {
		d.moveTo(d.root.Find(cursorPath))
	}
}

func (d *detailView) relayout() {
	if d.root == nil {
		d.lines = nil
		return
	}
	d.lines = d.root.Visible()
	d.cursor = clamp(d.cursor, 0, len(d.lines)-1)
}

func (d *detailView) current() *yamltree.Node {
	if d.cursor >= 0 && d.cursor < len(d.lines) {
		return d.lines[d.cursor]
	}
	return nil
}

func (d *detailView) moveTo(n *yamltree.Node) {
	for i, l := range d.lines {
		if l == n {
			d.cursor = i
			return
		}
	}
}

func (d *detailView) height() int { return d.rect.h - 2 }

func (d *detailView) toggle(n *yamltree.Node) {
	if n == nil || !n.HasChildren() {
		return
	}
	n.Expanded = !n.Expanded
	d.relayout()
}

func (d *detailView) Update(msg tea.Msg) tea.Cmd {
	if d.root == nil {
		return nil
	}
	n := d.current()
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "enter", "space", "o":
			d.toggle(n)
		case "right", "l":
			if n != nil && n.HasChildren() && !n.Expanded {
				d.toggle(n)
			} else if n != nil && n.Expanded {
				d.cursor = clamp(d.cursor+1, 0, len(d.lines)-1)
			}
		case "left", "h":
			if n != nil && n.HasChildren() && n.Expanded {
				d.toggle(n)
			} else if n != nil && n.Parent != nil && n.Parent != d.root {
				d.moveTo(n.Parent)
			}
		case "O":
			d.root.SetExpanded(true)
			d.relayout()
			d.moveTo(n)
		case "C":
			d.root.SetExpanded(false)
			d.relayout()
			for n != nil && n.Parent != d.root {
				n = n.Parent
			}
			d.moveTo(n)
		default:
			if c, ok := moveCursor(msg.String(), d.cursor, len(d.lines), d.height()); ok {
				d.cursor = c
			}
		}
	case clickMsg:
		i := d.offset + msg.y
		if i < 0 || i >= len(d.lines) {
			return nil
		}
		n := d.lines[i]
		onGlyph := msg.x >= n.Depth*2 && msg.x <= n.Depth*2+1
		if onGlyph || msg.double || i == d.cursor {
			d.toggle(n)
		}
		d.moveTo(n)
	case wheelMsg:
		d.offset = clamp(d.offset+msg.delta, 0, max(len(d.lines)-d.height(), 0))
		return nil
	}
	d.offset = scrollTo(d.cursor, d.offset, d.height())
	return nil
}

func (d *detailView) View() string {
	if d.obj == nil {
		return frame("Details", "", []string{stMuted.Render(" nothing selected")}, d.rect.w, d.rect.h, d.focused)
	}
	iw := d.rect.w - 2
	lines := make([]string, 0, d.height())
	for i := d.offset; i < len(d.lines) && len(lines) < d.height(); i++ {
		lines = append(lines, d.renderLine(d.lines[i], i == d.cursor, iw))
	}
	footer := ""
	if c := d.current(); c != nil {
		footer = c.Path
	}
	title := d.obj.GetKind() + " " + d.obj.GetName()
	return frame(title, footer, lines, d.rect.w, d.rect.h, d.focused)
}

func (d *detailView) renderLine(n *yamltree.Node, selected bool, w int) string {
	indent := strings.Repeat("  ", n.Depth)
	glyph := "  "
	if n.HasChildren() {
		glyph = "▸ "
		if n.Expanded {
			glyph = "▾ "
		}
	}

	// Each part is kept as (plain, styled) so the selected line can be
	// rendered with one uniform highlight.
	var plain, styled strings.Builder
	part := func(s string, st func(...string) string) {
		plain.WriteString(s)
		styled.WriteString(st(s))
	}
	none := func(s ...string) string { return strings.Join(s, "") }

	part(indent, none)
	part(glyph, stMuted.Render)
	switch {
	case n.Kind == yamltree.Line:
		part(n.Value.(string), stString.Render)
	case n.Index >= 0:
		if n.HasChildren() || n.Kind == yamltree.Map || n.Kind == yamltree.List {
			part(fmt.Sprintf("[%d]", n.Index), stMuted.Render)
			if h := n.Hint(); h != "" {
				part(" "+h, stBold.Render)
			}
		} else {
			part("- ", stMuted.Render)
		}
	default:
		part(n.Key, stKey.Render)
		part(":", stMuted.Render)
	}

	switch n.Kind {
	case yamltree.Scalar:
		if n.Index < 0 {
			part(" ", none)
		}
		s, st := formatScalar(n.Value)
		part(s, st)
	case yamltree.Map, yamltree.List:
		if !n.Expanded {
			open, close := "{", "}"
			if n.Kind == yamltree.List {
				open, close = "[", "]"
			}
			part(fmt.Sprintf(" %s…%d%s", open, len(n.Children), close), stMuted.Render)
		} else if len(n.Children) == 0 {
			if n.Kind == yamltree.List {
				part(" []", stMuted.Render)
			} else {
				part(" {}", stMuted.Render)
			}
		}
	case yamltree.Text:
		part(" |", stMuted.Render)
		if !n.Expanded {
			part(fmt.Sprintf(" %s… (%d lines)", n.Children[0].Value, len(n.Children)), stMuted.Render)
		}
	}

	if selected {
		st := stSelLo
		if d.focused {
			st = stSel
		}
		return st.Render(fit(plain.String(), w))
	}
	return styled.String()
}

func formatScalar(v any) (string, func(...string) string) {
	switch t := v.(type) {
	case nil:
		return "null", stMuted.Render
	case string:
		if t == "" {
			return `""`, stString.Render
		}
		return t, stString.Render
	case bool:
		return fmt.Sprint(t), stBool.Render
	case int64, float64, int:
		return fmt.Sprint(t), stNumber.Render
	}
	return fmt.Sprint(v), stString.Render
}
