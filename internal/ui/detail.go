package ui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hdweiss/coral/internal/config"
	"github.com/hdweiss/coral/internal/logs"
	"github.com/hdweiss/coral/internal/yamltree"
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

	fields *config.Fields // favorite and hidden fields per kind
	info   infoState      // the "i" help popup

	// log is the log entry shown instead of an object while the log view is
	// open; obj then wraps its fields. Favorites are the fields pinned to
	// the log lines, kept per app (logKind) besides the old global ones.
	log     *logs.Entry
	logKind string
}

// fieldsChangedMsg asks the app to save the field preferences.
type fieldsChangedMsg struct{}

// kind is the key of the shown object's field preferences.
func (d *detailView) kind() string {
	if d.log != nil {
		return d.logKind
	}
	return d.obj.GroupVersionKind().GroupKind().String()
}

// arrange applies the favorite and hidden fields of the object's kind.
func (d *detailView) arrange() {
	if d.fields == nil {
		return
	}
	kind := d.kind()
	is := func(f func(kind, pattern string) bool, n *yamltree.Node) bool {
		return f(kind, n.Pattern()) || d.log != nil && f(logFieldsKind, n.Pattern())
	}
	d.root.Arrange(
		func(n *yamltree.Node) bool { return is(d.fields.IsFavorite, n) },
		func(n *yamltree.Node) bool { return is(d.fields.IsHidden, n) },
	)
}

func (d *detailView) toggleFavorite(n *yamltree.Node) tea.Cmd {
	if n == nil || !n.Arrangeable() || d.fields == nil {
		return nil
	}
	d.fields.SetFavorite(d.kind(), n.Pattern(), !n.Favorite)
	if d.log != nil && n.Favorite {
		d.fields.SetFavorite(logFieldsKind, n.Pattern(), false)
	}
	return d.rearranged(n)
}

func (d *detailView) toggleHidden(n *yamltree.Node) tea.Cmd {
	if n == nil || !n.Arrangeable() || d.fields == nil {
		return nil
	}
	d.fields.SetHidden(d.kind(), n.Pattern(), !n.IsHidden)
	if d.log != nil && n.IsHidden {
		d.fields.SetHidden(logFieldsKind, n.Pattern(), false)
	}
	return d.rearranged(n)
}

// rearranged re-applies the rules after a change, following n if it is still
// on screen and otherwise keeping the cursor on the same row.
func (d *detailView) rearranged(n *yamltree.Node) tea.Cmd {
	d.arrange()
	d.relayout()
	d.moveTo(n)
	return emit(fieldsChangedMsg{})
}

// rowAction is a clickable button drawn at the right end of a row.
type rowAction struct {
	label string
	run   func() tea.Cmd
}

// actions returns the buttons of a row: unhide on hidden fields, and
// favorite and hide on the selected row.
func (d *detailView) actions(n *yamltree.Node, selected bool) []rowAction {
	if d.fields == nil || !n.Arrangeable() {
		return nil
	}
	switch {
	case n.IsHidden:
		return []rowAction{{"unhide", func() tea.Cmd { return d.toggleHidden(n) }}}
	case selected && n.HiddenDepth() == 0:
		star := "☆"
		if n.Favorite {
			star = "★"
		}
		if d.log != nil {
			star += " pin" // a favorite log field shows on every line
		}
		return []rowAction{
			{star, func() tea.Cmd { return d.toggleFavorite(n) }},
			{"hide", func() tea.Cmd { return d.toggleHidden(n) }},
		}
	}
	return nil
}

func actionsWidth(acts []rowAction) int {
	w := 0
	for _, a := range acts {
		w += ansi.StringWidth(a.label) + 2
	}
	return w
}

// SetObject shows obj. When obj is the same object as before (e.g. after a
// refresh), folding and cursor position are kept.
func (d *detailView) SetObject(obj *unstructured.Unstructured) {
	if d.log != nil {
		d.log, d.obj = nil, nil
		d.cursor, d.offset = 0, 0
	}
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
	d.arrange()
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

// SetLog shows a log entry. Folding and the cursor's field carry over from the
// previous entry, so that moving through the log keeps the same field in view.
func (d *detailView) SetLog(e *logs.Entry) {
	if e == d.log && d.root != nil {
		return
	}
	var state map[string]bool
	cursorPath := ""
	if d.log != nil && d.root != nil {
		state = d.root.ExpansionState()
		if c := d.current(); c != nil {
			cursorPath = c.Path
		}
	} else {
		d.cursor, d.offset = 0, 0
	}
	d.log = e
	if e == nil {
		d.obj, d.root, d.lines = nil, nil, nil
		return
	}
	d.obj = &unstructured.Unstructured{Object: e.Fields}
	d.root = yamltree.BuildFirst(e.Fields, "@timestamp", "timestamp", "time", "ts", "log", "level", "message", "msg")
	d.arrange()
	d.root.ApplyExpansionState(state)
	d.relayout()
	if n := d.root.Find(cursorPath); n != nil {
		d.moveTo(n)
	}
	d.offset = scrollTo(d.cursor, d.offset, d.height())
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
	if n.Kind == yamltree.Hidden && n.Expanded {
		d.revealChildren(n)
	}
}

// revealChildren scrolls so that as many of n's visible descendants as fit
// are on screen, keeping n itself in view.
func (d *detailView) revealChildren(n *yamltree.Node) {
	i := slices.Index(d.lines, n)
	if i < 0 {
		return
	}
	last := i
	for last+1 < len(d.lines) && isBelow(d.lines[last+1], n) {
		last++
	}
	d.offset = clamp(max(d.offset, last-d.height()+1), 0, i)
}

// isBelow reports whether n is a descendant of group, through the group's
// hidden children.
func isBelow(n, group *yamltree.Node) bool {
	for _, c := range group.Children {
		for p := n; p != nil; p = p.Parent {
			if p == c {
				return true
			}
		}
	}
	return false
}

func (d *detailView) Update(msg tea.Msg) tea.Cmd {
	if d.root == nil {
		if k, ok := msg.(tea.KeyPressMsg); ok && (k.String() == "h" || k.String() == "left") {
			return emit(focusMsg{focusTable})
		}
		return nil
	}
	n := d.current()
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "i":
			if d.log != nil {
				return nil
			}
			d.info.on = !d.info.on
			if d.info.on {
				return emit(needSchemaMsg{})
			}
			return nil
		case "enter", "space", "o":
			d.toggle(n)
		case "ctrl+p": // favorite; for a log entry, pin the field to the lines
			return d.toggleFavorite(n)
		case "ctrl+x":
			return d.toggleHidden(n)
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
			} else {
				return emit(focusMsg{focusTable}) // nothing left to fold: back to the list
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
		if acts := d.actions(n, i == d.cursor); len(acts) > 0 {
			x := d.rect.w - 2 - actionsWidth(acts)
			for _, a := range acts {
				w := ansi.StringWidth(a.label) + 2
				if msg.x >= x && msg.x < x+w {
					return a.run()
				}
				x += w
			}
		}
		indent := (n.Depth + n.HiddenDepth()) * 2
		onGlyph := msg.x >= indent && msg.x <= indent+1
		if onGlyph || msg.double || i == d.cursor || n.Kind == yamltree.Hidden {
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
		if c.Kind == yamltree.Hidden {
			footer = strings.TrimSpace(c.Parent.Path + " hidden fields")
		}
	}
	title := d.obj.GetKind() + " " + d.obj.GetName()
	if d.log != nil {
		title = "Log entry"
		if !d.log.Time.IsZero() {
			title += " " + d.log.Time.Local().Format("15:04:05.000")
		}
		if d.log.Format != logs.Plain {
			title += " (" + d.log.Format.String() + ")"
		}
	}
	return frame(title, footer, lines, d.rect.w, d.rect.h, d.focused)
}

func (d *detailView) renderLine(n *yamltree.Node, selected bool, w int) string {
	acts := d.actions(n, selected)
	w -= actionsWidth(acts)
	hidden := n.HiddenDepth() > 0
	indent := strings.Repeat("  ", n.Depth+n.HiddenDepth())
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
	case n.Kind == yamltree.Hidden:
		label := fmt.Sprintf("⋯ %d hidden", len(n.Children))
		if len(n.Children) == 1 {
			label = "⋯ 1 hidden: " + n.Children[0].Key
		}
		part(label, stMuted.Italic(true).Render)
	case n.Kind == yamltree.Line:
		part(untab(n.Value.(string)), stString.Render)
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
	if n.Favorite {
		part(" ★", stAccent.Render)
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
			part(fmt.Sprintf(" %s…%d%s", open, n.Fields(), close), stMuted.Render)
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
		return st.Render(fit(plain.String(), w)) + renderActions(acts, st.Foreground(colAccent))
	}
	if hidden {
		return stMuted.Render(fit(plain.String(), w)) + renderActions(acts, stMuted)
	}
	return fit(styled.String(), w) + renderActions(acts, stAccent)
}

func renderActions(acts []rowAction, st lipgloss.Style) string {
	var sb strings.Builder
	for _, a := range acts {
		sb.WriteString(st.Render(" " + a.label + " "))
	}
	return sb.String()
}

func formatScalar(v any) (string, func(...string) string) {
	switch t := v.(type) {
	case nil:
		return "null", stMuted.Render
	case string:
		if t == "" {
			return `""`, stString.Render
		}
		return untab(t), stString.Render
	case bool:
		return fmt.Sprint(t), stBool.Render
	case int64, float64, int:
		return fmt.Sprint(t), stNumber.Render
	}
	return fmt.Sprint(v), stString.Render
}

// untab expands tabs, which would otherwise move the terminal cursor past
// the cells that widths are computed for.
func untab(s string) string { return strings.ReplaceAll(s, "\t", "    ") }
