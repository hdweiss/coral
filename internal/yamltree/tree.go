// Package yamltree turns an unstructured Kubernetes object into a foldable
// tree of nodes, in the order a human expects to read it.
package yamltree

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type Kind int

const (
	Scalar Kind = iota
	Map
	List
	Text // multi-line string; children are its lines
	Line // one line of a Text node
)

type Node struct {
	Key      string // map key, "" for list items and lines
	Index    int    // list index, -1 when not a list item
	Kind     Kind
	Value    any // scalar value, or the full string of a Text node
	Children []*Node
	Parent   *Node
	Depth    int
	Expanded bool
	Path     string // e.g. .spec.containers[0].image
}

// Build creates the tree for obj. The returned root is not itself displayed.
func Build(obj map[string]any) *Node {
	root := &Node{Kind: Map, Index: -1, Depth: -1, Expanded: true}
	for _, k := range orderedKeys("", obj) {
		root.Children = append(root.Children, newNode(root, k, -1, obj[k]))
	}
	return root
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func newNode(parent *Node, key string, index int, v any) *Node {
	n := &Node{Key: key, Index: index, Parent: parent, Depth: parent.Depth + 1, Value: v}
	switch {
	case index >= 0:
		n.Path = parent.Path + "[" + strconv.Itoa(index) + "]"
	case identRe.MatchString(key):
		n.Path = parent.Path + "." + key
	default:
		n.Path = parent.Path + "[" + strconv.Quote(key) + "]"
	}

	switch t := v.(type) {
	case map[string]any:
		n.Kind = Map
		n.Value = nil
		for _, k := range orderedKeys(n.Path, t) {
			n.Children = append(n.Children, newNode(n, k, -1, t[k]))
		}
	case []any:
		n.Kind = List
		n.Value = nil
		for i, item := range t {
			n.Children = append(n.Children, newNode(n, "", i, item))
		}
	case string:
		if strings.Contains(strings.TrimRight(t, "\n"), "\n") {
			n.Kind = Text
			for _, line := range strings.Split(strings.TrimRight(t, "\n"), "\n") {
				n.Children = append(n.Children, &Node{Kind: Line, Index: -1, Value: line, Parent: n, Depth: n.Depth + 1, Path: n.Path})
			}
		}
	}
	n.Expanded = defaultExpanded(n)
	return n
}

// Fields that are noise most of the time start out collapsed.
var collapsedByDefault = map[string]bool{
	".metadata.managedFields": true,
	`.metadata.annotations["kubectl.kubernetes.io/last-applied-configuration"]`: true,
}

func defaultExpanded(n *Node) bool {
	if collapsedByDefault[n.Path] {
		return false
	}
	return n.Kind != Text || len(n.Children) <= 20
}

// Preferred key order per path; other keys follow alphabetically.
var keyOrder = map[string][]string{
	"":          {"apiVersion", "kind", "metadata", "spec", "type", "data", "stringData", "binaryData", "status"},
	".metadata": {"name", "generateName", "namespace", "uid", "resourceVersion", "generation", "creationTimestamp", "deletionTimestamp", "labels", "annotations", "ownerReferences", "finalizers", "managedFields"},
}

func orderedKeys(path string, obj map[string]any) []string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	pref, ok := keyOrder[path]
	if !ok {
		// Generic rule: identity fields first.
		pref = []string{"name", "type", "kind", "apiVersion"}
	}
	rank := func(k string) int {
		if i := slices.Index(pref, k); i >= 0 {
			return i
		}
		return len(pref)
	}
	sort.Slice(keys, func(i, j int) bool {
		ri, rj := rank(keys[i]), rank(keys[j])
		if ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	return keys
}

// HasChildren reports whether the node can be folded.
func (n *Node) HasChildren() bool { return len(n.Children) > 0 }

// Visible returns the descendants of n that are currently shown, in order.
func (n *Node) Visible() []*Node {
	var out []*Node
	var walk func(*Node)
	walk = func(p *Node) {
		for _, c := range p.Children {
			out = append(out, c)
			if c.Expanded {
				walk(c)
			}
		}
	}
	walk(n)
	return out
}

// SetExpanded sets the expanded state of n and all of its descendants.
func (n *Node) SetExpanded(expanded bool) {
	if n.Parent != nil { // keep root open
		n.Expanded = expanded
	}
	for _, c := range n.Children {
		c.SetExpanded(expanded)
	}
}

// Walk calls f for n and every descendant.
func (n *Node) Walk(f func(*Node)) {
	f(n)
	for _, c := range n.Children {
		c.Walk(f)
	}
}

// ExpansionState records which paths are expanded, so that a rebuilt tree of
// the same object keeps the user's folding.
func (n *Node) ExpansionState() map[string]bool {
	s := map[string]bool{}
	n.Walk(func(c *Node) {
		if c.HasChildren() && c.Kind != Line {
			s[c.Path] = c.Expanded
		}
	})
	return s
}

func (n *Node) ApplyExpansionState(s map[string]bool) {
	n.Walk(func(c *Node) {
		if v, ok := s[c.Path]; ok && c.Parent != nil {
			c.Expanded = v
		}
	})
}

// Find returns the node with the given path, or nil.
func (n *Node) Find(path string) *Node {
	var found *Node
	n.Walk(func(c *Node) {
		if found == nil && c.Kind != Line && c.Path == path {
			found = c
		}
	})
	return found
}

// Hint is a short description of an item, used for list elements: the value of
// its "name" field if any.
func (n *Node) Hint() string {
	if n.Kind != Map {
		return ""
	}
	for _, c := range n.Children {
		if c.Key == "name" || c.Key == "type" {
			if s, ok := c.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}
