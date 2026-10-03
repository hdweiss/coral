package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
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
}

func newNavView(store *k8s.Store) *navView {
	v := &navView{store: store, loaded: map[string]bool{}}
	for _, ctx := range store.Provider().Contexts() {
		v.roots = append(v.roots, newContextNode(ctx))
	}
	v.refresh()
	return v
}

func newContextNode(ctx string) *navNode {
	n := &navNode{kind: nkContext, label: ctx, context: ctx}
	cluster := n.add(&navNode{kind: nkCategory, label: k8s.CatCluster})
	for _, r := range k8s.InCategory(k8s.CatCluster) {
		cluster.add(&navNode{kind: nkResource, label: r.Title, res: r})
	}
	addCategories(n.add(&navNode{kind: nkAllNS, label: "All namespaces"}))
	n.add(&navNode{kind: nkInfo, label: "loading namespaces…"})
	return n
}

func addCategories(n *navNode) {
	for _, cat := range k8s.NamespacedCategories {
		c := n.add(&navNode{kind: nkCategory, label: cat, ns: n.ns})
		for _, r := range k8s.InCategory(cat) {
			c.add(&navNode{kind: nkResource, label: r.Title, res: r, ns: n.ns})
		}
		c.expanded = cat == k8s.CatWorkloads
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
	v.loaded[ctx] = true
	if p := v.pending; p != nil && p.key.Context == ctx {
		v.pending = nil
		v.Reveal(p.key, p.res)
	}
	v.refresh()
}

// Reveal expands the path to the node for key and moves the cursor there.
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
		if n.kind == nkResource && n.res.Name == res.Name && (n.ns == key.Namespace || !res.Namespaced) {
			target = n
			return
		}
		for _, c := range n.children {
			find(c)
		}
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

func (v *navView) activate(n *navNode) tea.Cmd {
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
			return v.activate(n)
		case "right", "l":
			if n.foldable() && !n.expanded {
				return v.toggle(n)
			}
			if n.expanded && len(n.children) > 0 {
				v.cursor++
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
	label, suffix := n.label, ""
	isActive := false
	switch n.kind {
	case nkContext:
		label = "⎈ " + label
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

	if selected {
		st := stSelLo
		if v.focused {
			st = stSel
		}
		return st.Render(fit(indent+glyph+label+suffix, w))
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
	case n.kind == nkCategory:
		ls = stMuted.Render(label)
	default:
		ls = label
	}
	return indent + stMuted.Render(glyph) + ls + stMuted.Render(suffix)
}
