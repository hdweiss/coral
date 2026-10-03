package ui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hdweiss/coralctl/internal/edit"
	"github.com/hdweiss/coralctl/internal/schema"
	"github.com/hdweiss/coralctl/internal/yamltree"
)

// picker is the "add field" dialog. The user types a dotted path below the
// target, like "selector.matchLabels.app: web", helped by suggestions from
// the schema. Containers are entered by picking them; leaves are added.
type picker struct {
	input  textinput.Model
	title  string
	base   *schema.Schema // schema of the target
	target any            // current value of the target, for "set" marks

	st      pickState
	items   []pickItem
	cursor  int
	offset  int
	rect    rect
	lastErr string
}

// pickResult is what the user chose: a path relative to the target (Append
// for new list items) and the value to put there.
type pickResult struct {
	segs     []yamltree.Seg
	value    any
	explicit bool // the user typed the value
}

// pickState is the parsed input.
type pickState struct {
	segs     []yamltree.Seg
	cur      *schema.Schema // where the partial segment is looked up
	existing any            // current value at segs
	prefix   string         // the input up to the partial segment
	partial  string
	value    string
	hasValue bool
	err      string
}

type pickKind int

const (
	pickField  pickKind = iota // a field of an object
	pickKey                    // a key of a map
	pickSelf                   // add the container typed so far
	pickAppend                 // append a value to a list of scalars
)

type pickItem struct {
	kind     pickKind
	name     string
	field    *schema.Field  // pickField
	schema   *schema.Schema // the value's schema
	set      bool           // already present in the object
	newKey   bool           // pickKey that does not exist yet
	required bool
}

const pickerRows = 10

func newPicker(title, basePath string, base *schema.Schema, target any) *picker {
	ti := textinput.New()
	ti.Prompt = basePath + " "
	if basePath == "" {
		ti.Prompt = "› "
	}
	ti.SetStyles(inputStyles(stMuted))
	ti.Placeholder = "field, or a path like selector.matchLabels.app: web"
	if base.Kind == schema.Array && !base.Container() {
		ti.Placeholder = "value to append"
	}
	ti.Focus()
	p := &picker{input: ti, title: title, base: base, target: target}
	p.refresh()
	return p
}

// parse reads the input relative to the target.
func (p *picker) parse() pickState {
	text := p.input.Value()
	pathPart, value, hasValue := strings.Cut(text, ":")
	st := pickState{cur: p.base, existing: p.target, value: strings.TrimSpace(value), hasValue: hasValue}
	if p.base.Kind == schema.Array && p.base.Container() {
		st.segs = []yamltree.Seg{{Index: edit.Append}}
		st.cur, st.existing = p.base.Elem, nil
	}
	parts := strings.Split(pathPart, ".")
	for i, part := range parts {
		if st.cur.Kind == schema.Map || i == len(parts)-1 {
			// The rest is the partial segment. Map keys may contain dots,
			// e.g. app.kubernetes.io/name.
			st.partial = strings.TrimSpace(strings.Join(parts[i:], "."))
			if i > 0 {
				st.prefix = strings.Join(parts[:i], ".") + "."
			}
			break
		}
		f := st.cur.Field(part)
		if st.cur.Kind != schema.Object || f == nil {
			st.err = "unknown field " + strings.TrimSpace(part)
			if st.cur.Kind != schema.Object {
				st.err = strings.Join(parts[:i], ".") + " has no fields"
			}
			return st
		}
		st.segs = append(st.segs, yamltree.Seg{Key: part, Index: -1})
		st.cur, st.existing = f.Schema, child(st.existing, part)
		if st.cur.Kind == schema.Array && st.cur.Container() {
			st.segs = append(st.segs, yamltree.Seg{Index: edit.Append})
			st.cur, st.existing = st.cur.Elem, nil
		}
	}
	return st
}

func child(v any, key string) any {
	if m, ok := v.(map[string]any); ok {
		return m[key]
	}
	return nil
}

// refresh re-parses the input and rebuilds the suggestions.
func (p *picker) refresh() {
	p.st = p.parse()
	p.items = p.items[:0]
	st := p.st
	switch {
	case st.err != "":
	case p.base.Kind == schema.Array && !p.base.Container():
		p.items = append(p.items, pickItem{kind: pickAppend, name: "append to the list", schema: p.base.Elem})
	case st.cur.Kind == schema.Object:
		if st.partial == "" && len(st.segs) > 0 && !st.hasValue {
			p.items = append(p.items, pickItem{kind: pickSelf, schema: st.cur})
		}
		p.items = append(p.items, p.fieldItems(st)...)
	case st.cur.Kind == schema.Map:
		if st.partial == "" && len(st.segs) > 0 && !st.hasValue {
			p.items = append(p.items, pickItem{kind: pickSelf, schema: st.cur})
		}
		existing, _ := st.existing.(map[string]any)
		if st.partial != "" && existing[st.partial] == nil {
			p.items = append(p.items, pickItem{kind: pickKey, name: st.partial, schema: st.cur.Elem, newKey: true})
		}
		for _, k := range yamltree.OrderedKeys("", existing) {
			if strings.Contains(strings.ToLower(k), strings.ToLower(st.partial)) {
				p.items = append(p.items, pickItem{kind: pickKey, name: k, schema: st.cur.Elem, set: true})
			}
		}
	}
	p.cursor = clamp(p.cursor, 0, len(p.items)-1)
	p.offset = scrollTo(p.cursor, p.offset, pickerRows)
}

// fieldItems ranks the fields of st.cur against the partial input: name
// prefix, then name substring, then description; required fields first.
func (p *picker) fieldItems(st pickState) []pickItem {
	q := strings.ToLower(st.partial)
	existing, _ := st.existing.(map[string]any)
	type ranked struct {
		item pickItem
		rank int
	}
	var out []ranked
	for _, f := range st.cur.Fields {
		name := strings.ToLower(f.Name)
		rank := -1
		switch {
		case strings.HasPrefix(name, q):
			rank = 0
		case strings.Contains(name, q):
			rank = 1
		case len(q) >= 3 && strings.Contains(strings.ToLower(f.Description), q):
			rank = 2
		}
		if rank < 0 {
			continue
		}
		_, set := existing[f.Name]
		out = append(out, ranked{pickItem{kind: pickField, name: f.Name, field: f, schema: f.Schema, set: set, required: f.Required}, rank})
	}
	slices.SortStableFunc(out, func(a, b ranked) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		if a.item.required != b.item.required {
			if a.item.required {
				return -1
			}
			return 1
		}
		return strings.Compare(a.item.name, b.item.name)
	})
	items := make([]pickItem, len(out))
	for i, r := range out {
		items[i] = r.item
	}
	return items
}

// Update handles a key. It returns done with a result when the user picked
// a leaf, or done with nil when the dialog was dismissed.
func (p *picker) Update(msg tea.Msg) (cmd tea.Cmd, done bool, res *pickResult) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			return nil, true, nil
		case "enter":
			res, done := p.pick(p.cursor)
			return nil, done, res
		case "tab":
			p.complete(p.cursor)
			return nil, false, nil
		case "up", "ctrl+p":
			p.cursor = clamp(p.cursor-1, 0, len(p.items)-1)
			p.offset = scrollTo(p.cursor, p.offset, pickerRows)
			return nil, false, nil
		case "down", "ctrl+n":
			p.cursor = clamp(p.cursor+1, 0, len(p.items)-1)
			p.offset = scrollTo(p.cursor, p.offset, pickerRows)
			return nil, false, nil
		case "backspace":
			// Backspace right after a "." removes the whole segment.
			if v := p.input.Value(); strings.HasSuffix(v, ".") && p.input.Position() == len(v) {
				v = strings.TrimSuffix(v, ".")
				if i := strings.LastIndex(v, "."); i >= 0 {
					v = v[:i+1]
				} else {
					v = ""
				}
				p.setInput(v)
				return nil, false, nil
			}
		}
	}
	before := p.input.Value()
	var c tea.Cmd
	p.input, c = p.input.Update(msg)
	if p.input.Value() != before {
		p.cursor, p.lastErr = 0, ""
		p.refresh()
	}
	return c, false, nil
}

func (p *picker) setInput(v string) {
	p.input.SetValue(v)
	p.input.CursorEnd()
	p.cursor, p.lastErr = 0, ""
	p.refresh()
}

// complete fills in item i: containers get a "." to continue below them,
// leaves a ": " for the value.
func (p *picker) complete(i int) {
	if i < 0 || i >= len(p.items) {
		return
	}
	it := p.items[i]
	switch it.kind {
	case pickField, pickKey:
		suffix := ": "
		if it.kind == pickField && it.schema.Container() {
			suffix = "."
		}
		v := p.st.prefix + it.name + suffix
		if p.st.hasValue && suffix == ": " {
			v = p.st.prefix + it.name + ": " + p.st.value
		}
		p.setInput(v)
	}
}

// pick chooses item i: containers are entered, leaves are added.
func (p *picker) pick(i int) (*pickResult, bool) {
	if p.st.err != "" {
		return nil, false
	}
	if i < 0 || i >= len(p.items) {
		return nil, false
	}
	it := p.items[i]
	switch it.kind {
	case pickSelf:
		return &pickResult{segs: p.st.segs, value: it.schema.Zero()}, true
	case pickAppend:
		if !p.st.hasValue && strings.TrimSpace(p.input.Value()) == "" {
			return &pickResult{segs: []yamltree.Seg{{Index: edit.Append}}, value: it.schema.Zero()}, true
		}
		v, err := edit.ParseValue(p.input.Value(), it.schema)
		if err != nil {
			p.lastErr = err.Error()
			return nil, false
		}
		return &pickResult{segs: []yamltree.Seg{{Index: edit.Append}}, value: v, explicit: true}, true
	}
	if it.kind == pickField && it.schema.Container() && !p.st.hasValue {
		p.complete(i)
		return nil, false
	}
	segs := append(slices.Clone(p.st.segs), yamltree.Seg{Key: it.name, Index: -1})
	if !p.st.hasValue || p.st.value == "" {
		return &pickResult{segs: segs, value: it.schema.Zero()}, true
	}
	v, err := edit.ParseValue(p.st.value, it.schema)
	if err != nil {
		p.lastErr = err.Error()
		return nil, false
	}
	return &pickResult{segs: segs, value: v, explicit: true}, true
}

// Click handles a click at absolute coordinates.
func (p *picker) Click(x, y int) (done bool, res *pickResult) {
	if !p.rect.contains(x, y) {
		return true, nil
	}
	row := y - p.rect.y - 3 // border, input, separator
	if i := p.offset + row; row >= 0 && row < pickerRows && i < len(p.items) {
		p.cursor = i
		res, done := p.pick(i)
		return done, res
	}
	return false, nil
}

func (p *picker) Wheel(delta int) {
	p.cursor = clamp(p.cursor+delta, 0, len(p.items)-1)
	p.offset = scrollTo(p.cursor, p.offset, pickerRows)
}

func (p *picker) View(screenW, screenH int) string {
	w := min(96, screenW-4)
	iw := w - 2
	p.input.SetWidth(iw - ansi.StringWidth(p.input.Prompt) - 1)
	lines := []string{p.input.View(), stMuted.Render(strings.Repeat("─", iw))}

	nameW, typeW := 26, 22
	for i := p.offset; i < len(p.items) && i < p.offset+pickerRows; i++ {
		lines = append(lines, p.renderItem(p.items[i], i == p.cursor, iw, nameW, typeW))
	}
	for len(lines) < pickerRows+2 {
		if len(lines) == 2 && len(p.items) == 0 {
			msg := " no matching fields"
			switch {
			case p.st.err != "":
				msg = " " + p.st.err
			case p.st.cur != nil && p.st.cur.Kind == schema.Map:
				msg = " type a key"
			}
			lines = append(lines, stMuted.Render(msg))
			continue
		}
		lines = append(lines, "")
	}

	// Description of the highlighted item.
	lines = append(lines, stMuted.Render(strings.Repeat("─", iw)))
	desc := ""
	if p.cursor < len(p.items) {
		it := p.items[p.cursor]
		switch {
		case it.field != nil:
			desc = it.field.Description
		case it.schema != nil:
			desc = it.schema.Description
		}
	}
	var descLines []string
	if p.lastErr != "" {
		descLines = []string{stErr.Render(" " + p.lastErr)}
	} else {
		for _, l := range strings.Split(lipgloss.Wrap(strings.Join(strings.Fields(desc), " "), iw-2, ""), "\n") {
			descLines = append(descLines, " "+stMuted.Render(l))
		}
	}
	for i := 0; i < 4; i++ {
		if i < len(descLines) {
			lines = append(lines, descLines[i])
		} else {
			lines = append(lines, "")
		}
	}

	box := frame(p.title, "tab complete · enter pick · esc close", lines, w, len(lines)+2, true)
	p.rect = rect{(screenW - w) / 2, max((screenH-lipgloss.Height(box))/3, 1), w, lipgloss.Height(box)}
	return box
}

func (p *picker) renderItem(it pickItem, selected bool, iw, nameW, typeW int) string {
	name, typ, note := it.name, it.schema.String(), ""
	switch it.kind {
	case pickSelf:
		path := strings.TrimSuffix(p.st.prefix, ".")
		name, note = "✓ add "+path, "as it is"
		typ = it.schema.String()
	case pickAppend:
		name = "✓ append"
	case pickKey:
		if it.newKey {
			note = "new key"
		}
	}
	if it.set {
		note = "set"
	}
	if it.required {
		name += "*"
	}
	summary := ""
	if it.field != nil {
		summary = schema.Summary(it.field.Description)
	}
	if selected {
		line := " " + fit(name, nameW) + " " + fit(typ, typeW) + " " + fit(note, 7) + " " + summary
		return stSel.Render(fit(line, iw))
	}
	ns := stKey
	switch {
	case it.kind == pickSelf || it.kind == pickAppend:
		ns = stAccent.Bold(true)
	case it.set:
		ns = stMuted
	}
	return " " + ns.Render(fit(name, nameW)) + " " + stMuted.Render(fit(typ, typeW)) + " " +
		stAccent.Render(fit(note, 7)) + " " + stMuted.Render(summary)
}
