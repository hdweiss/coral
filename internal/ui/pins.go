package ui

import (
	"github.com/hdweiss/coral/internal/config"
	"github.com/hdweiss/coral/internal/k8s"
)

// Messages for pinning, emitted by the navigator.
type (
	togglePinMsg struct{ pin config.Pin }
	openPinMsg   struct{ pin config.Pin }
)

// Pinned clusters move to the front of the context list. Namespace and
// resource pins are nodes of the "Pinned" section at the top of the
// navigator: a namespace pin folds out like the namespace, and a resource pin
// is a leaf that opens the list.

func (v *navView) hasPin(pin config.Pin) bool {
	for _, q := range v.pins {
		if q == pin {
			return true
		}
	}
	return false
}

// togglePin adds pin at the end, or removes it if it is already pinned. It
// reports whether the pin is now present.
func (v *navView) togglePin(pin config.Pin) bool {
	pins := v.pins
	added := true
	for i, q := range pins {
		if q == pin {
			pins = append(pins[:i:i], pins[i+1:]...)
			added = false
			break
		}
	}
	if added {
		pins = append(pins, pin)
	}
	v.SetPins(pins)
	return added
}

// SetPins replaces the pinned section and reorders the contexts, keeping the
// fold state of pins that stay.
func (v *navView) SetPins(pins []config.Pin) {
	var at *navNode
	if v.cursor < len(v.lines) {
		at = v.lines[v.cursor]
	}
	v.pins = pins
	old := map[config.Pin]*navNode{}
	for _, c := range v.pinned.children {
		old[*c.pin] = c
	}
	v.pinned.children = nil
	v.sortRoots()
	for _, p := range pins {
		if v.isClusterPin(p) {
			continue
		}
		if n, ok := old[p]; ok {
			v.pinned.children = append(v.pinned.children, n)
			continue
		}
		v.addPin(p)
	}
	v.refresh()
	for i, n := range v.lines {
		if n == at {
			v.cursor = i
		}
	}
}

func (v *navView) addPin(p config.Pin) {
	pin := p
	n := v.pinned.add(&navNode{pin: &pin, ns: p.Namespace})
	n.context = p.Context
	switch {
	case v.known != nil && !v.known(p.Context):
		n.kind, n.err, n.label = nkInfo, true, p.Context+" (missing)"
	case p.Resource != "":
		res, ok := k8s.Lookup(p.Resource)
		if !ok {
			n.kind, n.err, n.label = nkInfo, true, p.Resource+" (unknown)"
			return
		}
		n.kind, n.res, n.label = nkResource, res, res.Title
	case p.Namespace != "":
		n.kind, n.label = nkNamespace, p.Namespace
		addCategories(n)
	}
}

// isClusterPin reports whether p only reorders the contexts. A pinned
// cluster missing from the kubeconfig stays in the section, to be removed.
func (v *navView) isClusterPin(p config.Pin) bool {
	return p.Namespace == "" && p.Resource == "" && (v.known == nil || v.known(p.Context))
}

// sortRoots puts pinned clusters first, in pin order, then the rest in
// kubeconfig order.
func (v *navView) sortRoots() {
	byCtx := map[string]*navNode{}
	for _, r := range v.roots {
		byCtx[r.context] = r
	}
	v.roots = v.roots[:0]
	for _, p := range v.pins {
		if r := byCtx[p.Context]; r != nil && v.isClusterPin(p) {
			v.roots = append(v.roots, r)
			delete(byCtx, p.Context)
		}
	}
	for _, ctx := range v.contexts {
		if r := byCtx[ctx]; r != nil {
			v.roots = append(v.roots, r)
		}
	}
}

// pinNumber is the key that opens pin, "1" to "9", or "" past nine.
func (v *navView) pinNumber(pin config.Pin) string {
	for i, q := range v.pins {
		if q == pin && i < 9 {
			return string(rune('1' + i))
		}
	}
	return ""
}

// pinFor returns what pressing p on n means: the pin itself, the resource
// list, or else the enclosing namespace or cluster.
func pinFor(n *navNode) config.Pin {
	if n.pin != nil {
		return *n.pin
	}
	if n.kind == nkResource {
		ns := n.ns
		if !n.res.Namespaced {
			ns = ""
		}
		return config.Pin{Context: n.context, Namespace: ns, Resource: n.res.Name}
	}
	for ; n != nil; n = n.parent {
		switch n.kind {
		case nkNamespace:
			return config.Pin{Context: n.context, Namespace: n.ns}
		case nkContext:
			return config.Pin{Context: n.context}
		}
	}
	return config.Pin{}
}

// pinLabel describes pin for messages.
func pinLabel(pin config.Pin) string {
	s := "⎈ " + pin.Context
	if pin.Namespace != "" {
		s = pin.Context + " › " + pin.Namespace
	}
	if pin.Resource != "" {
		title := pin.Resource
		if r, ok := k8s.Lookup(pin.Resource); ok {
			title = r.Title
			if r.Namespaced && pin.Namespace == "" {
				s = pin.Context + " › all"
			}
		}
		s += " › " + title
	}
	return s
}

// pinActive reports whether the namespace pin n is where the user currently
// is.
func (v *navView) pinActive(n *navNode) bool {
	return n.kind == nkNamespace && n.context == v.active.Context && n.ns == v.active.Namespace
}
