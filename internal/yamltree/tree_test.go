package yamltree

import "testing"

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
