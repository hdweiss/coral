package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hdweiss/coralctl/internal/config"
	"github.com/hdweiss/coralctl/internal/k8s"
)

type navKind int

const (
	nkContext navKind = iota
	nkAllNS
	nkNamespace
	nkCategory
	nkResource
	nkInfo // non-interactive message, e.g. "loading…"
	nkPins // the "Pinned" section header
)

type navNode struct {
	kind     navKind
	label    string
	context  string
	ns       string // namespace scope of this node; "" = all / cluster
	res      k8s.Resource
	err      bool
	children []*navNode
	parent   *navNode
	expanded bool
	depth    int
	pin      *config.Pin // set on the top node of a pin
}

func (n *navNode) add(c *navNode) *navNode {
	c.parent = n
	c.depth = n.depth + 1
	c.context = n.context
	n.children = append(n.children, c)
	return c
}

func (n *navNode) foldable() bool { return n.kind != nkResource && n.kind != nkInfo }

// Messages emitted by the nav panel.
type (
	activateMsg struct {
		key k8s.Key
		res k8s.Resource
	}
	needNamespacesMsg struct{ context string }
)

type navView struct {
	rect    rect
	focused bool
	store   *k8s.Store
	roots   []*navNode
	lines   []*navNode
	cursor  int
	offset  int
	active  k8s.Key
	loaded  map[string]bool // contexts whose namespaces arrived
	pending *activateMsg    // reveal once namespaces load

	contexts []string // kubeconfig order; pinned clusters move to the front
	pins     []config.Pin
	pinned   *navNode              // the "Pinned" section, shown above the contexts
	known    func(ctx string) bool // whether a context exists in the kubeconfig
}

func newNavView(store *k8s.Store) *navView {
	v := &navView{
		store:    store,
		loaded:   map[string]bool{},
		contexts: store.Provider().Contexts(),
		pinned:   &navNode{kind: nkPins, label: "Pinned", expanded: true},
	}
	for _, ctx := range v.contexts {
		v.roots = append(v.roots, newContextNode(ctx))
	}
	v.refresh()
	return v
}

func newContextNode(ctx string) *navNode {
	n := &navNode{kind: nkContext, label: ctx, context: ctx}
	fillContext(n)
	return n
}

// fillContext adds the children of a context node.
func fillContext(n *navNode) {
	cluster := n.add(&navNode{kind: nkCategory, label: k8s.CatCluster})
	for _, r := range k8s.InCategory(k8s.CatCluster) {
		cluster.add(&navNode{kind: nkResource, label: r.Title, res: r})
	}
	addCategories(n.add(&navNode{kind: nkAllNS, label: "All namespaces"}))
	n.add(&navNode{kind: nkInfo, label: "loading namespaces…"})
}

func addCategories(n *navNode) {
	for _, cat := range k8s.NamespacedCategories {
		c := n.add(&navNode{kind: nkCategory, label: cat, ns: n.ns})
		for _, r := range k8s.InCategory(cat) {
			c.add(&navNode{kind: nkResource, label: r.Title, res: r, ns: n.ns})
		}
		c.expanded = cat == k8s.CatWorkloads
	}
	for _, r := range k8s.InCategory(k8s.CatTop) {
		n.add(&navNode{kind: nkResource, label: r.Title, res: r, ns: n.ns})
	}
}

func (v *navView) root(ctx string) *navNode {
	for _, r := range v.roots {
		if r.context == ctx {
			return r
		}
	}
	return nil
}

// SetNamespaces replaces a context's namespace nodes, keeping fold state.
func (v *navView) SetNamespaces(ctx string, names []string, err error) {
	root := v.root(ctx)
	if root == nil {
		return
	}
	setNamespaces(root, names, err)
	v.loaded[ctx] = true
	if p := v.pending; p != nil && p.key.Context == ctx {
		v.pending = nil
		v.Reveal(p.key, p.res)
	}
	v.refresh()
}

func setNamespaces(root *navNode, names []string, err error) {
	old := map[string]*navNode{}
	var kept []*navNode
	for _, c := range root.children {
		switch c.kind {
		case nkNamespace:
			old[c.ns] = c
		case nkInfo:
		default:
			kept = append(kept, c)
		}
	}
	root.children = kept
	if err != nil {
		root.add(&navNode{kind: nkInfo, label: "error: " + err.Error(), err: true})
	}
	for _, ns := range names {
		if n, ok := old[ns]; ok {
			root.children = append(root.children, n)
			continue
		}
		n := root.add(&navNode{kind: nkNamespace, label: ns, ns: ns})
		addCategories(n)
	}
}

// Reveal expands the path to the node for key and moves the cursor there. It
// looks at the cursor and the pin the cursor is in first, so that opening
// something inside a pin stays there, then in namespace and resource pins,
// then in the context's tree.
func (v *navView) Reveal(key k8s.Key, res k8s.Resource) {
	v.active = key
	root := v.root(key.Context)
	if root == nil {
		return
	}
	var target *navNode
	var find func(n *navNode)
	find = func(n *navNode) {
		if target != nil {
			return
		}
		if n.kind == nkResource && n.context == key.Context && n.res.Name == res.Name && (n.ns == key.Namespace || !res.Namespaced) {
			target = n
			return
		}
		for _, c := range n.children {
			find(c)
		}
	}
	if v.cursor < len(v.lines) {
		c := v.lines[v.cursor]
		if c.kind == nkResource {
			find(c)
		}
		for ; c != nil; c = c.parent {
			if c.pin != nil {
				find(c)
			}
		}
	}
	for _, p := range v.pinned.children {
		find(p)
	}
	find(root)
	if target == nil {
		if !v.loaded[key.Context] {
			v.pending = &activateMsg{key: key, res: res}
		}
		root.expanded = true
		v.refresh()
		return
	}
	for p := target.parent; p != nil; p = p.parent {
		p.expanded = true
	}
	v.refresh()
	for i, n := range v.lines {
		if n == target {
			v.cursor = i
		}
	}
	v.offset = scrollTo(v.cursor, v.offset, v.rect.h-2)
}

func (v *navView) refresh() {
	v.lines = v.lines[:0]
	var walk func(ns []*navNode)
	walk = func(ns []*navNode) {
		for _, n := range ns {
			v.lines = append(v.lines, n)
			if n.expanded {
				walk(n.children)
			}
		}
	}
	if len(v.pinned.children) > 0 {
		walk([]*navNode{v.pinned})
	}
	walk(v.roots)
	v.cursor = clamp(v.cursor, 0, len(v.lines)-1)
}

func (v *navView) height() int { return v.rect.h - 2 }

// toggle expands or collapses n, asking for namespaces when a context opens.
func (v *navView) toggle(n *navNode) tea.Cmd {
	if !n.foldable() {
		return nil
	}
	n.expanded = !n.expanded
	v.refresh()
	if n.kind == nkContext && n.expanded && !v.loaded[n.context] {
		return emit(needNamespacesMsg{context: n.context})
	}
	return nil
}

// isPinFolder reports whether n is a namespace pin, which opens like the old
// location (keeping the resource type) besides folding.
func isPinFolder(n *navNode) bool {
	return n.pin != nil && n.kind == nkNamespace
}

func (v *navView) activate(n *navNode) tea.Cmd {
	if isPinFolder(n) {
		return emit(openPinMsg{*n.pin})
	}
	if n.kind != nkResource {
		return v.toggle(n)
	}
	ns := n.ns
	if !n.res.Namespaced {
		ns = ""
	}
	return emit(activateMsg{key: k8s.Key{Context: n.context, GVR: n.res.GVR(), Namespace: ns}, res: n.res})
}

func (v *navView) Update(msg tea.Msg) tea.Cmd {
	if len(v.lines) == 0 {
		return nil
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		n := v.lines[v.cursor]
		switch msg.String() {
		case "enter", "space":
			if n.kind == nkResource {
				// Moving onto it already shows the list; enter goes there.
				return tea.Sequence(v.activate(n), emit(focusMsg{focusTable}))
			}
			return v.activate(n)
		case "ctrl+p":
			if pin := pinFor(n); pin.Context != "" {
				return emit(togglePinMsg{pin})
			}
		case "delete", "backspace":
			if n.pin != nil {
				return emit(togglePinMsg{*n.pin})
			}
		case "right", "l":
			if n.foldable() && !n.expanded {
				return v.toggle(n)
			}
			if n.expanded && len(n.children) > 0 {
				v.cursor++
			} else if n.kind == nkResource {
				return emit(focusMsg{focusTable}) // a leaf: move on to its list
			}
		case "left", "h":
			if n.foldable() && n.expanded {
				return v.toggle(n)
			}
			if n.parent != nil {
				for i, l := range v.lines {
					if l == n.parent {
						v.cursor = i
					}
				}
			}
		default:
			if c, ok := moveCursor(msg.String(), v.cursor, len(v.lines), v.height()); ok {
				v.cursor = c
				// Moving onto a resource previews it, like a file browser.
				if v.lines[c].kind == nkResource {
					v.offset = scrollTo(v.cursor, v.offset, v.height())
					return v.activate(v.lines[c])
				}
			}
		}
	case clickMsg:
		i := v.offset + msg.y
		if i < 0 || i >= len(v.lines) {
			return nil
		}
		n := v.lines[i]
		onGlyph := msg.x >= n.depth*2 && msg.x <= n.depth*2+1
		was := v.cursor
		v.cursor = i
		switch {
		case n.pin != nil && msg.x >= v.rect.w-2-ansi.StringWidth(unpinGlyph):
			return emit(togglePinMsg{*n.pin})
		case isPinFolder(n) && !onGlyph && !msg.double && was != i:
			return v.activate(n)
		case n.kind == nkResource && msg.double:
			return tea.Sequence(v.activate(n), emit(focusMsg{focusTable}))
		case n.kind == nkResource:
			return v.activate(n)
		case onGlyph || msg.double || was == i:
			return v.toggle(n)
		}
	case wheelMsg:
		v.offset = clamp(v.offset+msg.delta, 0, max(len(v.lines)-v.height(), 0))
		return nil
	}
	v.offset = scrollTo(v.cursor, v.offset, v.height())
	return nil
}

func (v *navView) View() string {
	lines := make([]string, 0, v.height())
	iw := v.rect.w - 2
	for i := v.offset; i < len(v.lines) && len(lines) < v.height(); i++ {
		lines = append(lines, v.renderLine(v.lines[i], i == v.cursor, iw))
	}
	return frame("Navigator", "", lines, v.rect.w, v.rect.h, v.focused)
}

func (v *navView) renderLine(n *navNode, selected bool, w int) string {
	indent := strings.Repeat("  ", n.depth)
	glyph := "  "
	if n.foldable() {
		glyph = "▸ "
		if n.expanded {
			glyph = "▾ "
		}
	}
	label, loc, suffix, star := n.label, "", "", ""
	isActive := false
	if n.pin != nil {
		// Pins name their location after the label, since they sit outside
		// its tree and the label matters most when the line is cut.
		switch {
		case n.kind == nkResource && n.res.Namespaced && n.ns == "":
			loc = " · " + n.context + " › all"
		case n.kind == nkResource && n.ns != "":
			loc = " · " + n.context + " › " + n.ns
		case n.kind == nkResource, n.kind == nkNamespace:
			loc = " · " + n.context
		}
		isActive = v.pinActive(n)
	} else if (n.kind == nkContext || n.kind == nkNamespace || n.kind == nkResource) && v.hasPin(pinFor(n)) {
		star = " ★"
		if n.kind == nkContext { // pinned clusters show their number key
			star += v.pinNumber(pinFor(n))
		}
	}
	switch n.kind {
	case nkContext:
		label = "⎈ " + label
	case nkPins:
		label = "★ " + label
	case nkResource:
		key := k8s.Key{Context: n.context, GVR: n.res.GVR(), Namespace: n.ns}
		if !n.res.Namespaced {
			key.Namespace = ""
		}
		if e, ok := v.store.Get(key); ok && e.Err == nil {
			suffix = fmt.Sprintf(" %d", len(e.Items))
		}
		isActive = key == v.active
	}
	// Pins end in a × that unpins them.
	// Pins end in their number key (1-9) and a × that unpins them.
	unpin := ""
	if n.pin != nil {
		unpin = v.pinNumber(*n.pin) + unpinGlyph
		w -= ansi.StringWidth(unpin)
	}

	if selected {
		st := stSelLo
		if v.focused {
			st = stSel
		}
		return st.Render(fit(indent+glyph+label+star+suffix+loc, w) + unpin)
	}
	var ls string
	switch {
	case n.err:
		ls = stErr.Render(label)
	case n.kind == nkInfo:
		ls = stMuted.Italic(true).Render(label)
	case isActive:
		ls = stAccent.Bold(true).Render(label)
	case n.kind == nkContext:
		ls = stBold.Render(label)
	case n.kind == nkContext, n.kind == nkPins:
		ls = stBold.Render(label)
	case n.kind == nkCategory:
		ls = stMuted.Render(label)
	default:
		ls = label
	}
	line := indent + stMuted.Render(glyph) + ls + stAccent.Render(star) + stMuted.Render(suffix) + stMuted.Render(loc)
	if unpin != "" {
		return fit(line, w) + stMuted.Render(unpin)
	}
	return line
}

const unpinGlyph = " × "
