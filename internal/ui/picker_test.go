package ui

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/hdweiss/coral/internal/edit"
	"github.com/hdweiss/coral/internal/schema"
	"github.com/hdweiss/coral/internal/yamltree"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
)

func deploySpecPicker(t *testing.T) *picker {
	t.Helper()
	d, err := schema.NewBuiltin().Lookup(k8sschema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"})
	if err != nil {
		t.Fatal(err)
	}
	existing := map[string]any{"replicas": int64(2), "selector": map[string]any{"matchLabels": map[string]any{"app": "web"}}}
	return newPicker("Add", ".spec", d.Field("spec").Schema, existing)
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func typeText(p *picker, s string) {
	for _, r := range s {
		p.Update(key(string(r)))
	}
}

func seg(k string) yamltree.Seg { return yamltree.Seg{Key: k, Index: -1} }

func TestPickerDottedPathWithValue(t *testing.T) {
	p := deploySpecPicker(t)
	typeText(p, "selector.matchLabels.tier: db")
	if p.st.err != "" || p.st.partial != "tier" || p.st.prefix != "selector.matchLabels." {
		t.Fatalf("state = %+v", p.st)
	}
	if len(p.items) == 0 || p.items[0].name != "tier" || !p.items[0].newKey {
		t.Fatalf("items = %+v", p.items)
	}
	_, done, res := p.Update(key("enter"))
	want := &pickResult{segs: []yamltree.Seg{seg("selector"), seg("matchLabels"), seg("tier")}, value: "db", explicit: true}
	if !done || !reflect.DeepEqual(res, want) {
		t.Fatalf("result = %+v, done %v", res, done)
	}
}

func TestPickerDescendsIntoContainers(t *testing.T) {
	p := deploySpecPicker(t)
	typeText(p, "sel")
	if p.items[0].name != "selector" || !p.items[0].set {
		t.Fatalf("first item = %+v", p.items[0])
	}
	// Enter on a container continues below it instead of adding it.
	if _, done, _ := p.Update(key("enter")); done || p.input.Value() != "selector." {
		t.Fatalf("input = %q, done %v", p.input.Value(), done)
	}
	if p.items[0].kind != pickSelf {
		t.Fatalf("first item should add selector itself: %+v", p.items[0])
	}
	// Backspace after "." drops the whole segment.
	p.Update(key("backspace"))
	if p.input.Value() != "" {
		t.Fatalf("after backspace = %q", p.input.Value())
	}
}

func TestPickerNewListItem(t *testing.T) {
	p := deploySpecPicker(t)
	typeText(p, "template.spec.containers.ima")
	if p.st.err != "" || len(p.items) == 0 || p.items[0].name != "image" {
		t.Fatalf("state %+v items %+v", p.st, p.items)
	}
	_, done, res := p.Update(key("enter"))
	want := []yamltree.Seg{seg("template"), seg("spec"), seg("containers"), {Index: edit.Append}, seg("image")}
	if !done || !reflect.DeepEqual(res.segs, want) || res.value != "" || res.explicit {
		t.Fatalf("result = %+v", res)
	}
}

func TestPickerErrors(t *testing.T) {
	p := deploySpecPicker(t)
	typeText(p, "bogus.x")
	if p.st.err != "unknown field bogus" {
		t.Fatalf("err = %q", p.st.err)
	}
	p.setInput("replicas: many")
	if _, done, _ := p.Update(key("enter")); done || p.lastErr == "" {
		t.Fatalf("bad integer should be rejected, lastErr %q", p.lastErr)
	}
}

func TestPickerOnListTargets(t *testing.T) {
	d, _ := schema.NewBuiltin().Lookup(k8sschema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"})
	pod := d.Field("spec").Schema.Field("template").Schema.Field("spec").Schema
	containers := pod.Field("containers").Schema.Elem

	// On a list of objects, fields go into a new item.
	p := newPicker("Add", ".spec.template.spec.containers", pod.Field("containers").Schema, []any{})
	typeText(p, "name: sidecar")
	_, done, res := p.Update(key("enter"))
	if !done || !reflect.DeepEqual(res.segs, []yamltree.Seg{{Index: edit.Append}, seg("name")}) || res.value != "sidecar" {
		t.Fatalf("result = %+v", res)
	}

	// On a list of scalars, the input is a value to append.
	p = newPicker("Add", ".args", containers.Field("args").Schema, []any{"a"})
	typeText(p, "--verbose")
	_, done, res = p.Update(key("enter"))
	if !done || !reflect.DeepEqual(res.segs, []yamltree.Seg{{Index: edit.Append}}) || res.value != "--verbose" || !res.explicit {
		t.Fatalf("result = %+v", res)
	}
}
