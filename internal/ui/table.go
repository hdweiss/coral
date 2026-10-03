package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hdweiss/coralctl/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type tableRow struct {
	cells []string
	obj   *unstructured.Unstructured
}

type tableView struct {
	rect    rect
	focused bool

	key     k8s.Key
	res     k8s.Resource
	cols    []k8s.Column
	entry   k8s.Entry
	hasData bool
	loading bool

	rows     []tableRow
	cursor   int
	offset   int
	sortCol  int
	sortDesc bool
	filter   string

	selUID string // keeps the selection on the same object across refreshes
	colX   []int  // start x of each column, for header clicks
}

func (t *tableView) SetResource(key k8s.Key, res k8s.Resource) {
	if t.key == key {
		return
	}
	t.key, t.res = key, res
	t.cols = k8s.Columns(res)
	if res.Namespaced && key.Namespace == "" {
		t.cols = append([]k8s.Column{k8s.ColNamespace}, t.cols...)
	}
	t.sortCol, t.sortDesc = 0, false
	t.cursor, t.offset, t.selUID = 0, 0, ""
	t.filter = ""
	t.hasData = false
	t.entry = k8s.Entry{}
	t.rows = nil
}

func (t *tableView) SetEntry(e k8s.Entry) {
	t.entry, t.hasData = e, true
	t.rebuild()
}

func (t *tableView) SetFilter(f string) {
	t.filter = f
	t.rebuild()
}

func (t *tableView) Selected() *unstructured.Unstructured {
	if t.cursor >= 0 && t.cursor < len(t.rows) {
		return t.rows[t.cursor].obj
	}
	return nil
}

func (t *tableView) rebuild() {
	t.rows = t.rows[:0]
	terms := strings.Fields(strings.ToLower(t.filter))
	for i := range t.entry.Items {
		obj := &t.entry.Items[i]
		cells := make([]string, len(t.cols))
		for c, col := range t.cols {
			cells[c] = col.Value(obj)
		}
		if len(terms) > 0 {
			hay := strings.ToLower(strings.Join(cells, " "))
			ok := true
			for _, term := range terms {
				if !strings.Contains(hay, term) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
		}
		t.rows = append(t.rows, tableRow{cells: cells, obj: obj})
	}
	t.sortRows()
	t.cursor = clamp(t.cursor, 0, len(t.rows)-1)
	if t.selUID != "" {
		for i, r := range t.rows {
			if string(r.obj.GetUID()) == t.selUID {
				t.cursor = i
			}
		}
	}
	t.remember()
}

func (t *tableView) sortRows() {
	if t.sortCol >= len(t.cols) {
		return
	}
	col := t.cols[t.sortCol]
	key := func(r tableRow) any {
		if col.Sort != nil {
			return col.Sort(r.obj)
		}
		return r.cells[t.sortCol]
	}
	slices.SortStableFunc(t.rows, func(a, b tableRow) int {
		var c int
		switch ka := key(a).(type) {
		case int64:
			c = cmp.Compare(ka, key(b).(int64))
		case string:
			c = cmp.Compare(ka, key(b).(string))
		}
		if c == 0 { // tie-break on namespace/name for a stable order
			c = cmp.Compare(a.obj.GetNamespace()+"/"+a.obj.GetName(), b.obj.GetNamespace()+"/"+b.obj.GetName())
		}
		if t.sortDesc {
			return -c
		}
		return c
	})
}

func (t *tableView) remember() {
	if o := t.Selected(); o != nil {
		t.selUID = string(o.GetUID())
	}
}

func (t *tableView) sortBy(col int) {
	if col == t.sortCol {
		t.sortDesc = !t.sortDesc
	} else {
		t.sortCol, t.sortDesc = col, false
	}
	t.rebuild()
}

func (t *tableView) height() int { return t.rect.h - 3 } // border + header

func (t *tableView) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "s":
			t.sortCol, t.sortDesc = (t.sortCol+1)%max(len(t.cols), 1), false
			t.rebuild()
		case "S":
			t.sortBy(t.sortCol)
		default:
			if c, ok := moveCursor(msg.String(), t.cursor, len(t.rows), t.height()); ok {
				t.cursor = c
			}
		}
	case clickMsg:
		if msg.y == 0 {
			for i := len(t.colX) - 1; i >= 0; i-- {
				if msg.x >= t.colX[i] {
					t.sortBy(i)
					break
				}
			}
			return nil
		}
		i := t.offset + msg.y - 1
		if i >= 0 && i < len(t.rows) {
			t.cursor = i
			if msg.double {
				t.remember()
				return emit(openDetailMsg{})
			}
		}
	case wheelMsg:
		t.offset = clamp(t.offset+msg.delta, 0, max(len(t.rows)-t.height(), 0))
		// Keep the cursor on screen so the detail pane follows the scroll.
		t.cursor = clamp(t.cursor, t.offset, t.offset+t.height()-1)
		t.cursor = clamp(t.cursor, 0, len(t.rows)-1)
		t.remember()
		return nil
	}
	t.remember()
	t.offset = scrollTo(t.cursor, t.offset, t.height())
	return nil
}

type openDetailMsg struct{}

func (t *tableView) title() string {
	scope := t.key.Namespace
	if !t.res.Namespaced {
		scope = ""
	} else if scope == "" {
		scope = "all"
	}
	title := t.res.Title
	if scope != "" {
		title += "(" + scope + ")"
	}
	if t.hasData {
		if t.filter != "" {
			title += fmt.Sprintf(" [%d/%d]", len(t.rows), len(t.entry.Items))
		} else {
			title += fmt.Sprintf(" [%d]", len(t.rows))
		}
	}
	return title
}

func (t *tableView) View() string {
	iw := t.rect.w - 2
	widths := t.columnWidths(iw)
	lines := []string{t.renderHeader(widths)}

	switch {
	case !t.hasData && t.loading:
		lines = append(lines, stMuted.Render(" loading…"))
	case t.entry.Err != nil && len(t.rows) == 0:
		lines = append(lines, stErr.Render(" "+t.entry.Err.Error()))
	case t.hasData && len(t.rows) == 0:
		msg := " no " + strings.ToLower(t.res.Title)
		if t.filter != "" {
			msg += " match “" + t.filter + "”"
		}
		lines = append(lines, stMuted.Render(msg))
	}
	for i := t.offset; i < len(t.rows) && len(lines) < t.height()+1; i++ {
		lines = append(lines, t.renderRow(t.rows[i], widths, i == t.cursor, iw))
	}

	footer := ""
	if len(t.rows) > 0 {
		footer = fmt.Sprintf("%d/%d", t.cursor+1, len(t.rows))
	}
	if t.filter != "" {
		footer = "/" + t.filter + "  " + footer
	}
	return frame(t.title(), footer, lines, t.rect.w, t.rect.h, t.focused)
}

const colGap = 2

func (t *tableView) columnWidths(avail int) []int {
	w := make([]int, len(t.cols))
	for i, c := range t.cols {
		w[i] = len(c.Name) + 2 // room for the sort arrow
	}
	for _, r := range t.rows {
		for i, c := range r.cells {
			w[i] = max(w[i], ansi.StringWidth(c))
		}
	}
	for i := range w {
		w[i] = min(w[i], 60)
	}
	total := (len(w) - 1) * colGap
	for _, x := range w {
		total += x
	}
	// Shrink the name column first, it is the one most worth truncating.
	nameCol := slices.IndexFunc(t.cols, func(c k8s.Column) bool { return c.Name == "NAME" })
	if over := total - avail + 1; over > 0 && nameCol >= 0 {
		w[nameCol] = max(w[nameCol]-over, 12)
	}
	return w
}

func (t *tableView) renderHeader(widths []int) string {
	t.colX = t.colX[:0]
	var sb strings.Builder
	x := 1
	sb.WriteString(" ")
	for i, c := range t.cols {
		t.colX = append(t.colX, x)
		name := c.Name
		if i == t.sortCol {
			arrow := "↑"
			if t.sortDesc {
				arrow = "↓"
			}
			sb.WriteString(stAccent.Bold(true).Render(fit(name+arrow, widths[i])))
		} else {
			sb.WriteString(stHeader.Render(fit(name, widths[i])))
		}
		sb.WriteString(strings.Repeat(" ", colGap))
		x += widths[i] + colGap
	}
	return sb.String()
}

func (t *tableView) renderRow(r tableRow, widths []int, selected bool, iw int) string {
	if selected {
		var sb strings.Builder
		sb.WriteString(" ")
		for i, c := range r.cells {
			sb.WriteString(fit(c, widths[i]) + strings.Repeat(" ", colGap))
		}
		st := stSelLo
		if t.focused {
			st = stSel
		}
		return st.Render(fit(sb.String(), iw))
	}
	var sb strings.Builder
	sb.WriteString(" ")
	for i, c := range r.cells {
		cell := fit(c, widths[i])
		switch t.cols[i].Name {
		case "STATUS":
			cell = statusStyle(c).Render(cell)
		case "NAMESPACE", "AGE":
			cell = stMuted.Render(cell)
		case "RESTARTS":
			if c != "0" {
				cell = stWarn.Render(cell)
			}
		case "READY":
			if a, b, ok := strings.Cut(c, "/"); ok && a != b {
				cell = stWarn.Render(cell)
			}
		}
		sb.WriteString(cell + strings.Repeat(" ", colGap))
	}
	return sb.String()
}
