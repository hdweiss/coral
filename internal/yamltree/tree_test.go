package yamltree

import (
	"strings"
	"testing"
)

func TestBuildOrderAndFolding(t *testing.T) {
	obj := map[string]any{
		"status":     map[string]any{"phase": "Running"},
		"spec":       map[string]any{"containers": []any{map[string]any{"image": "nginx", "name": "web"}}},
		"kind":       "Pod",
		"apiVersion": "v1",
		"metadata": map[string]any{
			"managedFields": []any{map[string]any{"manager": "kubectl"}},
			"name":          "web",
		},
		"data": map[string]any{"conf": "a\nb\nc\n"},
	}
	root := Build(obj)
	var keys []string
	for _, c := range root.Children {
		keys = append(keys, c.Key)
	}
	want := []string{"apiVersion", "kind", "metadata", "spec", "data", "status"}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("order = %v, want %v", keys, want)
		}
	}

	mf := root.Find(".metadata.managedFields")
	if mf == nil || mf.Expanded {
		t.Fatalf("managedFields should exist and be collapsed: %+v", mf)
	}
	img := root.Find(".spec.containers[0].image")
	if img == nil || img.Value != "nginx" {
		t.Fatalf("image node = %+v", img)
	}
	if h := root.Find(".spec.containers[0]").Hint(); h != "web" {
		t.Fatalf("hint = %q", h)
	}
	conf := root.Find(".data.conf")
	if conf.Kind != Text || len(conf.Children) != 3 {
		t.Fatalf("conf = %+v", conf)
	}

	state := root.ExpansionState()
	state[".spec"] = false
	again := Build(obj)
	again.ApplyExpansionState(state)
	if again.Find(".spec").Expanded {
		t.Fatal("expansion state not applied")
	}
}

func TestArrange(t *testing.T) {
	obj := map[string]any{
		"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "a", "image": "x", "env": []any{"E"}},
				map[string]any{"name": "b", "image": "y", "env": []any{"F"}},
			},
			"dnsPolicy":     "ClusterFirst",
			"nodeName":      "n1",
			"tolerations":   []any{"t"},
			"schedulerName": "default",
		},
	}
	root := Build(obj)
	rules := map[string]string{
		".spec.nodeName":         "fav",
		".spec.containers[].env": "hide",
		".spec.tolerations":      "hide",
		".spec.schedulerName":    "hide",
	}
	is := func(rule string) func(*Node) bool {
		return func(n *Node) bool { return rules[n.Pattern()] == rule }
	}
	root.Arrange(is("fav"), is("hide"))

	spec := root.Find(".spec")
	var keys []string
	for _, c := range spec.Children {
		keys = append(keys, c.Key)
	}
	if got := strings.Join(keys, ","); got != "nodeName,containers,dnsPolicy," {
		t.Fatalf("spec children = %q", got)
	}
	group := spec.Children[3]
	if group.Kind != Hidden || len(group.Children) != 2 || group.Expanded {
		t.Fatalf("hidden group = %+v", group)
	}
	if spec.Fields() != 5 {
		t.Fatalf("Fields = %d", spec.Fields())
	}
	// The rule for containers[].env applies to every container.
	for _, c := range []string{".spec.containers[0]", ".spec.containers[1]"} {
		ch := root.Find(c).Children
		if last := ch[len(ch)-1]; last.Kind != Hidden || last.Children[0].Key != "env" {
			t.Fatalf("%s: env not hidden: %+v", c, last)
		}
	}
	if d := root.Find(".spec.tolerations[0]").HiddenDepth(); d != 1 {
		t.Fatalf("HiddenDepth = %d", d)
	}

	// Unhiding brings the field back in its natural position, and the
	// group's folding survives re-arranging.
	group.Expanded = true
	delete(rules, ".spec.tolerations")
	root.Arrange(is("fav"), is("hide"))
	keys = keys[:0]
	for _, c := range spec.Children {
		keys = append(keys, c.Key)
	}
	if got := strings.Join(keys, ","); got != "nodeName,containers,dnsPolicy,tolerations," {
		t.Fatalf("after unhide = %q", got)
	}
	if !spec.Children[4].Expanded {
		t.Fatal("hidden group lost its expanded state")
	}
	root.SetExpanded(false)
	if !spec.Children[4].Expanded {
		t.Fatal("collapse all should leave hidden groups alone")
	}
}

func TestLookupPattern(t *testing.T) {
	obj := map[string]any{
		"@timestamp": "t",
		"http":       map[string]any{"response": map[string]any{"status_code": int64(200)}},
		"spans":      []any{map[string]any{"x": 1}, map[string]any{"id": "b"}},
	}
	n := Build(obj).Find(`.http.response.status_code`)
	if n == nil {
		t.Fatal("node not found")
	}
	for pattern, want := range map[string]any{
		n.Pattern():      int64(200),
		`["@timestamp"]`: "t",
		`.spans[].id`:    "b",
		`.spans[0].x`:    1,
	} {
		got, ok := Lookup(obj, pattern)
		if !ok || got != want {
			t.Errorf("Lookup(%s) = %v, %v; want %v", pattern, got, ok, want)
		}
	}
	if _, ok := Lookup(obj, ".http.nope"); ok {
		t.Error("missing field found")
	}
	if got := PatternLabel(`.spans[].id`); got != "spans.id" {
		t.Errorf("PatternLabel = %s", got)
	}
}
