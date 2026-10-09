package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hdweiss/coral/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type tableRow struct {
	cells    []string
	obj      *unstructured.Unstructured
	warnings int // recent Warning events about obj
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

	// warnings marks rows with recent Warning events (⚠ in the NAME
	// column). Not used for the events list itself.
	warnings *k8s.WarningIndex

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
	if i := slices.IndexFunc(t.cols, func(c k8s.Column) bool { return c.DefaultSort }); i >= 0 {
		t.sortCol = i
	}
	t.warnings = nil
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

// Select moves the cursor to the object with uid, now or once it is listed.
func (t *tableView) Select(uid string) {
	t.selUID = uid
	t.rebuild()
}

// SetWarnings marks the rows with recent Warning events.
func (t *tableView) SetWarnings(ix k8s.WarningIndex) {
	t.warnings = &ix
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
		row := tableRow{cells: cells, obj: obj}
		if t.warnings != nil {
			row.warnings = t.warnings.Count(obj)
		}
		t.rows = append(t.rows, row)
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
		case float64:
			c = cmp.Compare(ka, key(b).(float64))
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
		if col, ok := sortKeys(t.cols)[msg.String()]; ok {
			t.sortBy(col)
			break
		}
		if c, ok := moveCursor(msg.String(), t.cursor, len(t.rows), t.height()); ok {
			t.cursor = c
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
			if t.onMarker(t.rows[i], msg.x) {
				t.remember()
				return emit(openEventsMsg{})
			}
			if msg.double {
				t.remember()
				if t.res.ID() == "customresourcedefinitions" {
					return emit(openInstancesMsg{})
				}
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

// k9sSortKeys are k9s's shift+letter sort keys for its common columns.
var k9sSortKeys = map[string]string{
	"NAME": "N", "AGE": "A", "NAMESPACE": "P", "STATUS": "S", "RESTARTS": "T",
	"IP": "I", "NODE": "O", "CPU": "C", "MEM": "M",
}

// Shift letters that are taken in the table (live mode, logs, events, end),
// so no column sorts with them. k9s sorts READY with R, which is live mode
// here.
var reservedSortKeys = "RLEG"

// sortKeys maps a shift+letter key ("N") to the column it sorts by: k9s's
// letter for its columns, else the first free letter of the column name.
// The same key again reverses the order.
func sortKeys(cols []k8s.Column) map[string]int {
	keys := map[string]int{}
	free := func(k string) bool {
		_, taken := keys[k]
		return k != "" && !taken && !strings.Contains(reservedSortKeys, k)
	}
	for i, c := range cols {
		if k := k9sSortKeys[c.Name]; free(k) {
			keys[k] = i
		}
	}
	for i, c := range cols {
		if _, ok := k9sSortKeys[c.Name]; ok {
			continue
		}
		for _, r := range strings.ToUpper(c.Name) {
			if k := string(r); r >= 'A' && r <= 'Z' && free(k) {
				keys[k] = i
				break
			}
		}
	}
	return keys
}

// sortKeyOf returns the key that sorts by column i, or "".
func sortKeyOf(cols []k8s.Column, i int) string {
	for k, c := range sortKeys(cols) {
		if c == i {
			return k
		}
	}
	return ""
}

type (
	openDetailMsg struct{}
	// openEventsMsg asks to show the events of the selected object.
	openEventsMsg struct{}
)

// warningMark is the ⚠ marker of a row with n warnings, drawn at the right
// end of the NAME column.
func warningMark(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" ⚠%d", n)
}

// nameCol returns the index of the column that carries the warning marker.
func (t *tableView) nameCol() int {
	return slices.IndexFunc(t.cols, func(c k8s.Column) bool { return c.Name == "NAME" })
}

// onMarker reports whether x (panel-local) is on the row's warning marker.
func (t *tableView) onMarker(r tableRow, x int) bool {
	nc := t.nameCol()
	if r.warnings == 0 || nc < 0 || nc >= len(t.widths) || t.widths[nc] == 0 {
		return false
	}
	for i, ci := range t.colIdx {
		if ci == nc {
			end := t.colX[i] + t.widths[nc]
			return x >= end-ansi.StringWidth(warningMark(r.warnings)) && x < end
		}
	}
	return false
}

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
	nc := t.nameCol()
	for _, r := range t.rows {
		for i, c := range r.cells {
			w := ansi.StringWidth(c)
			if i == nc {
				w += ansi.StringWidth(warningMark(r.warnings))
			}
			natural[i] = max(natural[i], w)
		}
	}
	for i, c := range t.cols {
		if !c.Flex {
			natural[i] = min(natural[i], 60)
		}
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
	if nameCol < 0 {
		nameCol = slices.IndexFunc(cols, func(c k8s.Column) bool { return c.Flex })
	}
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
	// A flex column takes back the room that hiding columns left over.
	if nameCol >= 0 && cols[nameCol].Flex {
		if o := over(); o < 0 {
			w[nameCol] = min(w[nameCol]-o, natural[nameCol])
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
		st, name := stHeader, c.Name
		if i == t.sortCol {
			st = stAccent.Bold(true)
			name += map[bool]string{false: "↑", true: "↓"}[t.sortDesc]
		}
		cells = append(cells, underlineKey(fit(name, widths[i]), sortKeyOf(t.cols, i), st))
	}
	return joinCells(cells)
}

// underlineKey renders s in st with the first occurrence of the shift+letter
// key k underlined, like a menu accelerator.
func underlineKey(s, k string, st lipgloss.Style) string {
	i := strings.Index(s, k)
	if k == "" || i < 0 {
		return st.Render(s)
	}
	return st.Render(s[:i]) + st.Underline(true).Render(s[i:i+1]) + st.Render(s[i+1:])
}

// joinCells lays out rendered cells with the leading space and column gaps.
func joinCells(cells []string) string {
	return " " + strings.Join(cells, strings.Repeat(" ", colGap))
}

func (t *tableView) renderRow(r tableRow, widths []int, selected bool, iw int) string {
	var cells []string
	warn := t.res.ID() == "events" && k8s.IsWarning(r.obj)
	nc := t.nameCol()
	for i, c := range r.cells {
		if widths[i] == 0 {
			continue
		}
		if mark := warningMark(r.warnings); i == nc && mark != "" {
			mw := ansi.StringWidth(mark)
			if selected {
				cells = append(cells, fit(c, widths[i]-mw)+mark)
			} else {
				cells = append(cells, fit(c, widths[i]-mw)+stErr.Render(mark))
			}
			continue
		}
		cell := fit(c, widths[i])
		if !selected { // the selection is drawn with one uniform highlight
			cell = cellStyle(t.cols[i].Name, c, cell)
			if warn && (t.cols[i].Name == "TYPE" || t.cols[i].Name == "REASON") {
				cell = stErr.Render(cell)
			}
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
	case "NAMESPACE", "AGE", "LAST SEEN":
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
