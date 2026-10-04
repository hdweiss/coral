package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hdweiss/coral/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type descRowKind int

const (
	descHeader descRowKind = iota // section title, not selectable
	descInfo                      // a message such as "no events", not selectable
	descObject
	descEvent
)

type descRow struct {
	kind descRowKind
	text string      // header title or info message
	rel  k8s.Related // descObject
	self bool        // the described object itself, on top
	ev   *unstructured.Unstructured
}

func (r descRow) selectable() bool { return r.kind == descObject || r.kind == descEvent }

// obj is the object the row shows in the details: the related object or the
// event.
func (r descRow) obj() *unstructured.Unstructured {
	if r.kind == descEvent {
		return r.ev
	}
	return r.rel.Obj
}

// describeView replaces the table, like the log view. With eventsOnly (E) it
// lists the events of one object; otherwise (d) it describes the object: its
// related objects by section, then its events. The detail panel shows the
// selected row's object, and enter jumps to a related object's list.
type describeView struct {
	rect    rect
	focused bool

	ctx        string
	subject    *unstructured.Unstructured
	res        k8s.Resource // subject's resource
	eventsOnly bool
	prev       *describeView // the view this one was opened from; esc returns to it

	rels    []k8s.Relation
	events  []*unstructured.Unstructured
	loaded  bool
	loading bool
	err     error
	at      time.Time // when the last load started
	gen     int

	rows   []descRow
	cursor int
	offset int
	filter string
	selUID string // keeps the selection across reloads
}

type describeMsg struct {
	view     *describeView
	gen      int
	rels     []k8s.Relation
	keepRels bool // only the events were loaded
	events   []*unstructured.Unstructured
	err      error
}

func newDescribeView(ctx string, subject *unstructured.Unstructured, res k8s.Resource, eventsOnly bool) *describeView {
	v := &describeView{ctx: ctx, subject: subject, res: res, eventsOnly: eventsOnly}
	v.rebuild() // the object shows while the rest loads
	return v
}

// load computes the view's content in the background. Lists younger than
// maxAge come from the cache. Without withRels only the events are loaded
// and the related objects stay as they are, since they rarely change.
func (v *describeView) load(store *k8s.Store, maxAge time.Duration, withRels bool) tea.Cmd {
	v.gen++
	v.loading, v.at = true, time.Now()
	gen, obj, ctx := v.gen, v.subject, v.ctx
	withRels = withRels && !v.eventsOnly
	rel, reg := store.Lister(ctx, maxAge), store.Registry(ctx)
	return func() tea.Msg {
		msg := describeMsg{view: v, gen: gen, keepRels: !withRels}
		var relErr error
		if withRels {
			msg.rels, relErr = k8s.Relations(rel, reg, obj)
		}
		e := store.Cached(k8s.EventsKey(ctx, obj), maxAge)
		msg.events = k8s.EventsAbout(e.Items, obj)
		msg.err = errors.Join(relErr, e.Err)
		return msg
	}
}

func (v *describeView) onLoaded(msg describeMsg) {
	if msg.gen != v.gen {
		return
	}
	v.loading, v.loaded = false, true
	if !msg.keepRels {
		v.rels = msg.rels
	}
	v.events, v.err = msg.events, msg.err
	v.rebuild()
}

func (v *describeView) matches(text string) bool {
	hay := strings.ToLower(text)
	for _, t := range strings.Fields(strings.ToLower(v.filter)) {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

func (v *describeView) rebuild() {
	v.rows = v.rows[:0]
	if !v.eventsOnly {
		// The described object heads the view, whatever the filter.
		v.rows = append(v.rows, descRow{kind: descObject, self: true, rel: k8s.Related{
			Res: v.res, Obj: v.subject, Kind: v.subject.GetKind(), Name: v.subject.GetName(),
			Note: k8s.SummaryOf(v.res, v.subject),
		}})
		if !v.loaded {
			v.rows = append(v.rows, descRow{kind: descInfo, text: "loading…"})
		}
		for _, rel := range v.rels {
			var items []descRow
			for _, it := range rel.Items {
				if v.matches(it.Kind + " " + it.Name + " " + it.Note) {
					items = append(items, descRow{kind: descObject, rel: it})
				}
			}
			if len(items) > 0 {
				v.rows = append(v.rows, descRow{kind: descHeader, text: fmt.Sprintf("%s (%d)", rel.Title, len(rel.Items))})
				v.rows = append(v.rows, items...)
			}
		}
		if len(v.rels) == 0 && v.loaded && v.filter == "" {
			v.rows = append(v.rows, descRow{kind: descHeader, text: "Related"},
				descRow{kind: descInfo, text: "no related objects found"})
		}
	}
	var events []descRow
	warnings := 0
	for _, e := range v.events {
		if k8s.IsWarning(e) {
			warnings++
		}
		if v.matches(eventText(e)) {
			events = append(events, descRow{kind: descEvent, ev: e})
		}
	}
	if !v.eventsOnly {
		title := fmt.Sprintf("Events (%d)", len(v.events))
		if warnings > 0 {
			title = fmt.Sprintf("Events (%d, %d warning", len(v.events), warnings)
			if warnings > 1 {
				title += "s"
			}
			title += ")"
		}
		if len(events) > 0 || v.filter == "" {
			v.rows = append(v.rows, descRow{kind: descHeader, text: title})
		}
	}
	v.rows = append(v.rows, events...)
	if len(v.events) == 0 && v.loaded {
		v.rows = append(v.rows, descRow{kind: descInfo, text: "no events (clusters keep them for about an hour)"})
	}

	v.cursor = clamp(v.cursor, 0, len(v.rows)-1)
	if v.selUID != "" {
		for i, r := range v.rows {
			if o := r.obj(); o != nil && string(o.GetUID()) == v.selUID {
				v.cursor = i
			}
		}
	}
	v.settle(1)
	v.remember()
	v.scroll()
}

func eventText(e *unstructured.Unstructured) string {
	return strings.Join([]string{k8s.LastSeen(e), k8s.EventObject(e), stringField(e, "type"), stringField(e, "reason"), eventCount(e), k8s.EventMessage(e)}, " ")
}

func stringField(u *unstructured.Unstructured, fields ...string) string {
	s, _, _ := unstructured.NestedString(u.Object, fields...)
	return s
}

// settle moves the cursor off headers and info rows: in direction dir
// first, then the other way.
func (v *describeView) settle(dir int) {
	for _, d := range []int{dir, -dir} {
		for c := v.cursor; c >= 0 && c < len(v.rows); c += d {
			if v.rows[c].selectable() {
				v.cursor = c
				return
			}
		}
	}
}

func (v *describeView) remember() {
	if o := v.SelectedObject(); o != nil {
		v.selUID = string(o.GetUID())
	}
}

// Selected returns the selected row, if it is an object or event.
func (v *describeView) Selected() (descRow, bool) {
	if v.cursor >= 0 && v.cursor < len(v.rows) && v.rows[v.cursor].selectable() {
		return v.rows[v.cursor], true
	}
	return descRow{}, false
}

// SelectedObject is the object the details show: the selected row's, or the
// described object when nothing is selected.
func (v *describeView) SelectedObject() *unstructured.Unstructured {
	if r, ok := v.Selected(); ok && r.obj() != nil {
		return r.obj()
	}
	return nil
}

// selectionKey is the list the selected row's object belongs to, for editing.
func (v *describeView) selectionKey() (k8s.Key, *unstructured.Unstructured, bool) {
	r, ok := v.Selected()
	if !ok || r.obj() == nil {
		return k8s.Key{}, nil, false
	}
	res := k8s.MustLookup("events")
	if r.kind == descObject {
		res = r.rel.Res
	}
	ns := r.obj().GetNamespace()
	if !res.Namespaced {
		ns = ""
	}
	return k8s.Key{Context: v.ctx, GVR: res.GVR(), Namespace: ns}, r.obj(), true
}

func (v *describeView) SetFilter(f string) {
	v.filter = f
	v.rebuild()
}

func (v *describeView) scroll() {
	v.offset = clamp(v.offset, 0, max(len(v.rows)-v.height(), 0))
	v.offset = scrollTo(v.cursor, v.offset, v.height())
}

func (v *describeView) height() int { return v.rect.h - 2 }

// jumpMsg asks the app to show a related object in its list.
type jumpMsg struct{ rel k8s.Related }

// activate opens the selected row: a related object's list, or an event's
// details.
func (v *describeView) activate() tea.Cmd {
	r, ok := v.Selected()
	switch {
	case !ok, r.self && r.rel.Res.Name == "": // a kind coral can't list
		return nil
	case r.kind == descObject:
		return emit(jumpMsg{r.rel})
	default:
		return emit(openDetailMsg{})
	}
}

func (v *describeView) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == "enter" {
			return v.activate()
		}
		if c, ok := moveCursor(msg.String(), v.cursor, len(v.rows), v.height()); ok {
			dir := 1
			if c < v.cursor {
				dir = -1
			}
			v.cursor = c
			v.settle(dir)
		}
	case clickMsg:
		i := v.offset + msg.y
		if i < 0 || i >= len(v.rows) || !v.rows[i].selectable() {
			return nil
		}
		v.cursor = i
		v.remember()
		if msg.double {
			return v.activate()
		}
	case wheelMsg:
		v.offset = clamp(v.offset+msg.delta, 0, max(len(v.rows)-v.height(), 0))
		v.cursor = clamp(v.cursor, v.offset, v.offset+v.height()-1)
		v.cursor = clamp(v.cursor, 0, len(v.rows)-1)
		dir := 1
		if msg.delta < 0 {
			dir = -1
		}
		v.settle(dir)
		v.remember()
		return nil
	}
	v.remember()
	v.scroll()
	return nil
}

// name is how the view refers to its object: pod/catalog-6b9.
func (v *describeView) name() string {
	return strings.ToLower(v.subject.GetKind()) + "/" + v.subject.GetName()
}

func (v *describeView) label() string {
	if v.eventsOnly {
		return "Events"
	}
	return "Describe"
}

func (v *describeView) title() string {
	t := v.label() + " " + v.name()
	if v.eventsOnly && v.loaded {
		t += fmt.Sprintf(" [%d]", len(v.events))
	}
	return t
}

func (v *describeView) View() string {
	iw := v.rect.w - 2
	var lines []string
	if !v.loaded && len(v.rows) == 0 {
		lines = append(lines, stMuted.Render(" loading…"))
	}
	reasonW := 0
	for _, r := range v.rows {
		if r.kind == descEvent {
			reasonW = max(reasonW, ansi.StringWidth(stringField(r.ev, "reason")))
		}
	}
	reasonW = min(reasonW, 24)
	countW := 0
	for _, r := range v.rows {
		if r.kind == descEvent {
			countW = max(countW, ansi.StringWidth(eventCount(r.ev)))
		}
	}
	kindW := 0
	for _, r := range v.rows {
		if r.kind == descObject {
			kindW = max(kindW, ansi.StringWidth(r.rel.Kind))
		}
	}
	kindW = min(kindW, 24) // CiliumClusterwideNetworkPolicy
	for i := v.offset; i < len(v.rows) && len(lines) < v.height(); i++ {
		lines = append(lines, v.renderRow(v.rows[i], i == v.cursor, iw, kindW, reasonW, countW))
	}

	var footer []string
	if v.filter != "" {
		footer = append(footer, "/"+v.filter)
	}
	switch {
	case v.err != nil:
		footer = append(footer, "error: "+v.err.Error())
	case v.loading && v.loaded:
		footer = append(footer, "⟳")
	}
	if r, ok := v.Selected(); ok && r.kind == descObject && r.rel.Obj != nil {
		footer = append(footer, "enter: go to")
	}
	return frame(v.title(), strings.Join(footer, "  "), lines, v.rect.w, v.rect.h, v.focused)
}

// eventCount renders how often an event was seen, when more than once.
func eventCount(e *unstructured.Unstructured) string {
	if n := k8s.EventCount(e); n > 1 {
		return fmt.Sprintf("×%d", n)
	}
	return ""
}

func (v *describeView) renderRow(r descRow, selected bool, w, kindW, reasonW, countW int) string {
	switch r.kind {
	case descHeader:
		return " " + stAccent.Bold(true).Render(fit(r.text, w-1))
	case descInfo:
		indent := " "
		if !v.eventsOnly {
			indent = "   "
		}
		return stMuted.Italic(true).Render(fit(indent+r.text, w))
	}

	var plain, styled strings.Builder
	part := func(s string, st lipgloss.Style) {
		plain.WriteString(s)
		styled.WriteString(st.Render(s))
	}
	none := lipgloss.NewStyle()
	switch {
	case v.eventsOnly:
		part(" ", none)
	case r.self:
		part(" "+selfMarker+" ", stAccent.Bold(true))
	default:
		part("   ", none)
	}
	switch r.kind {
	case descObject:
		part(fit(r.rel.Kind, kindW)+"  ", stMuted)
		switch {
		case r.self:
			part(r.rel.Name, stAccent.Bold(true))
		case r.rel.Obj == nil:
			part(r.rel.Name, stErr)
		default:
			part(r.rel.Name, none)
		}
		if r.rel.Note != "" {
			st := statusStyle(r.rel.Note)
			if r.rel.Obj == nil {
				st = stErr
			} else if _, unset := st.GetForeground().(lipgloss.NoColor); unset {
				st = stMuted
			}
			part("  "+r.rel.Note, st)
		}
	case descEvent:
		e := r.ev
		typ := stringField(e, "type")
		warn := lipgloss.NewStyle()
		if k8s.IsWarning(e) {
			warn = stErr
		}
		part(fit(k8s.LastSeen(e), 7)+" ", stMuted)
		part(fit(typ, 7)+" ", warn)
		part(fit(stringField(e, "reason"), reasonW)+"  ", warn)
		if countW > 0 {
			part(fmt.Sprintf("%*s  ", countW, eventCount(e)), stMuted)
		}
		part(k8s.EventMessage(e), none)
	}

	if selected {
		st := stSelLo
		if v.focused {
			st = stSel
		}
		return st.Render(fit(plain.String(), w))
	}
	return fit(styled.String(), w)
}

// selfMarker marks the described object, the top row of describe.
const selfMarker = "▶"
