package ui

import (
	"testing"
	"time"

	"github.com/hdweiss/coral/internal/config"
	"github.com/hdweiss/coral/internal/k8s"
)

func newTestNav(t *testing.T) *navView {
	t.Helper()
	v := newNavView(k8s.NewStore(k8s.NewDemoProvider(), time.Second))
	v.rect = rect{0, 0, 30, 40}
	v.known = func(ctx string) bool { return ctx == "demo-dev" || ctx == "demo-prod" }
	return v
}

func pinNode(v *navView, pin config.Pin) *navNode {
	for _, n := range v.pinned.children {
		if *n.pin == pin {
			return n
		}
	}
	return nil
}

func TestPinForResourceAndNamespace(t *testing.T) {
	v := newTestNav(t)
	v.SetNamespaces("demo-dev", []string{"shop"}, nil)
	v.Reveal(k8s.Key{Context: "demo-dev", GVR: k8s.MustLookup("pods").GVR(), Namespace: "shop"}, k8s.MustLookup("pods"))
	n := v.lines[v.cursor]
	if got, want := pinFor(n), (config.Pin{Context: "demo-dev", Namespace: "shop", Resource: "pods"}); got != want {
		t.Errorf("pinFor(pods) = %+v, want %+v", got, want)
	}
	if got, want := pinFor(n.parent), (config.Pin{Context: "demo-dev", Namespace: "shop"}); got != want {
		t.Errorf("pinFor(category) = %+v, want %+v", got, want)
	}
}

func TestPinsFoldOutAndKeepState(t *testing.T) {
	v := newTestNav(t)
	ns := config.Pin{Context: "demo-dev", Namespace: "shop"}
	cluster := config.Pin{Context: "demo-prod"}
	missing := config.Pin{Context: "gone"}
	v.SetPins([]config.Pin{ns, cluster, missing})

	n := pinNode(v, ns)
	if n.kind != nkNamespace || len(n.children) == 0 {
		t.Fatalf("namespace pin has no categories: %+v", n)
	}
	if m := pinNode(v, missing); m.kind != nkInfo || !m.err {
		t.Errorf("missing context pin = %+v", m)
	}

	// A cluster pin moves the context first instead of adding a node.
	if pinNode(v, cluster) != nil {
		t.Error("cluster pin has a node in the pinned section")
	}
	if v.roots[0].context != "demo-prod" {
		t.Errorf("first context = %s, want demo-prod", v.roots[0].context)
	}
	v.togglePin(cluster)
	if v.roots[0].context != "demo-dev" {
		t.Errorf("unpinned cluster stays first")
	}

	n.expanded = true
	v.togglePin(config.Pin{Context: "demo-dev", Namespace: "shop", Resource: "pods"})
	if pinNode(v, ns) != n || !n.expanded {
		t.Error("adding a pin lost the fold state of another")
	}
	if !v.hasPin(config.Pin{Context: "demo-dev", Namespace: "shop", Resource: "pods"}) {
		t.Error("resource pin not added")
	}
	v.togglePin(ns)
	if pinNode(v, ns) != nil || v.hasPin(ns) {
		t.Error("namespace pin not removed")
	}
}

func TestRevealPrefersPins(t *testing.T) {
	v := newTestNav(t)
	v.SetNamespaces("demo-dev", []string{"default", "shop"}, nil)
	ns := config.Pin{Context: "demo-dev", Namespace: "shop"}
	v.SetPins([]config.Pin{ns})
	pods := k8s.MustLookup("pods")
	v.Reveal(k8s.Key{Context: "demo-dev", GVR: pods.GVR(), Namespace: "shop"}, pods)
	n := v.lines[v.cursor]
	if n.kind != nkResource || n.parent.parent != pinNode(v, ns) {
		t.Errorf("revealed %q at depth %d, want pods inside the pin", n.label, n.depth)
	}

	// Moving onto the same list in the main tree keeps the cursor there.
	shop := v.root("demo-dev").children[len(v.root("demo-dev").children)-1]
	shop.expanded = true
	v.refresh()
	for i, l := range v.lines {
		if l.parent != nil && l.parent.parent == shop && l.res.Name == "pods" {
			v.cursor = i
		}
	}
	main := v.lines[v.cursor]
	v.Reveal(k8s.Key{Context: "demo-dev", GVR: pods.GVR(), Namespace: "shop"}, pods)
	if v.lines[v.cursor] != main {
		t.Error("reveal moved the cursor away from a matching node")
	}
}
