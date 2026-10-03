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
	widths []int  // last rendered column widths; 0 means hidden
	colX   []int  // start x of each visible column, for header clicks
	colIdx []int  // column index of each entry in colX
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
	t.widths = nil
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
			t.sortCol, t.sortDesc = t.nextVisibleCol(t.sortCol), false
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
					t.sortBy(t.colIdx[i])
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

// nextVisibleCol returns the column after c, skipping hidden columns.
func (t *tableView) nextVisibleCol(c int) int {
	n := max(len(t.cols), 1)
	for range n {
		c = (c + 1) % n
		if c >= len(t.widths) || t.widths[c] > 0 {
			return c
		}
	}
	return c
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

const (
	colGap      = 2
	nameSoftMin = 24 // NAME shrinks to this before optional columns are hidden
	nameHardMin = 12
)

func (t *tableView) columnWidths(avail int) []int {
	natural := make([]int, len(t.cols))
	for i, c := range t.cols {
		natural[i] = len(c.Name) + 1 // room for the sort arrow
	}
	for _, r := range t.rows {
		for i, c := range r.cells {
			natural[i] = max(natural[i], ansi.StringWidth(c))
		}
	}
	for i := range natural {
		natural[i] = min(natural[i], 60)
	}
	t.widths = layoutColumns(t.cols, natural, avail, t.sortCol)
	return t.widths
}

// layoutColumns fits columns of the given natural widths into avail cells.
// It shrinks NAME to a comfortable width first, then hides optional columns
// (highest Drop first), then shrinks NAME down to its hard minimum, and
// finally hides columns from the right. A width
// of 0 means the column is hidden. The keep column is never hidden.
func layoutColumns(cols []k8s.Column, natural []int, avail, keep int) []int {
	w := slices.Clone(natural)
	over := func() int {
		total, n := 1, 0 // leading space
		for _, x := range w {
			if x > 0 {
				total += x
				n++
			}
		}
		return total + max(n-1, 0)*colGap - avail
	}
	nameCol := slices.IndexFunc(cols, func(c k8s.Column) bool { return c.Name == "NAME" })
	shrinkName := func(floor int) {
		if o := over(); o > 0 && nameCol >= 0 {
			w[nameCol] = max(w[nameCol]-o, min(natural[nameCol], floor))
		}
	}

	shrinkName(nameSoftMin)
	var optional []int
	for i, c := range cols {
		if c.Drop > 0 && i != keep {
			optional = append(optional, i)
		}
	}
	slices.SortStableFunc(optional, func(a, b int) int {
		return cmp.Or(cmp.Compare(cols[b].Drop, cols[a].Drop), cmp.Compare(b, a))
	})
	for _, i := range optional {
		if over() <= 0 {
			break
		}
		w[i] = 0
	}
	shrinkName(nameHardMin)
	// Last resort: hide columns from the right rather than cutting the row.
	for i := len(w) - 1; i >= 0 && over() > 0; i-- {
		if i != nameCol && i != keep {
			w[i] = 0
		}
	}
	return w
}

func (t *tableView) renderHeader(widths []int) string {
	t.colX, t.colIdx = t.colX[:0], t.colIdx[:0]
	var cells []string
	x := 1
	for i, c := range t.cols {
		if widths[i] == 0 {
			continue
		}
		t.colX = append(t.colX, x)
		t.colIdx = append(t.colIdx, i)
		x += widths[i] + colGap
		if i == t.sortCol {
			arrow := "↑"
			if t.sortDesc {
				arrow = "↓"
			}
			cells = append(cells, stAccent.Bold(true).Render(fit(c.Name+arrow, widths[i])))
		} else {
			cells = append(cells, stHeader.Render(fit(c.Name, widths[i])))
		}
	}
	return joinCells(cells)
}

// joinCells lays out rendered cells with the leading space and column gaps.
func joinCells(cells []string) string {
	return " " + strings.Join(cells, strings.Repeat(" ", colGap))
}

func (t *tableView) renderRow(r tableRow, widths []int, selected bool, iw int) string {
	var cells []string
	for i, c := range r.cells {
		if widths[i] == 0 {
			continue
		}
		cell := fit(c, widths[i])
		if !selected { // the selection is drawn with one uniform highlight
			cell = cellStyle(t.cols[i].Name, c, cell)
		}
		cells = append(cells, cell)
	}
	if selected {
		st := stSelLo
		if t.focused {
			st = stSel
		}
		return st.Render(fit(joinCells(cells), iw))
	}
	return joinCells(cells)
}

// cellStyle colors a padded cell by the meaning of its raw value.
func cellStyle(col, raw, cell string) string {
	switch col {
	case "STATUS":
		return statusStyle(raw).Render(cell)
	case "NAMESPACE", "AGE":
		return stMuted.Render(cell)
	case "RESTARTS":
		if raw != "0" {
			return stWarn.Render(cell)
		}
	case "READY":
		if a, b, ok := strings.Cut(raw, "/"); ok && a != b {
			return stWarn.Render(cell)
		}
	}
	return cell
}
