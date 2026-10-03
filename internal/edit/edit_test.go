package edit

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hdweiss/coralctl/internal/schema"
	"github.com/hdweiss/coralctl/internal/yamltree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func deployment() map[string]any {
	return map[string]any{
		"spec": map[string]any{
			"replicas": int64(2),
			"template": map[string]any{"spec": map[string]any{
				"containers": []any{map[string]any{"name": "web", "image": "nginx"}},
			}},
		},
		"kind":       "Deployment",
		"apiVersion": "apps/v1",
		"metadata": map[string]any{
			"name": "web", "namespace": "shop", "resourceVersion": "7",
			"managedFields": []any{map[string]any{"manager": "kubectl"}},
		},
		"status": map[string]any{"replicas": int64(2)},
	}
}

func TestToYAMLOrderAndManagedFields(t *testing.T) {
	doc, err := ToYAML(deployment())
	if err != nil {
		t.Fatal(err)
	}
	s := string(doc)
	if strings.Contains(s, "managedFields") {
		t.Error("managedFields should be dropped")
	}
	order := []string{"apiVersion:", "kind:", "metadata:", "  name: web", "spec:", "status:"}
	last := -1
	for _, o := range order {
		i := strings.Index(s, o)
		if i <= last {
			t.Fatalf("%q out of order in:\n%s", o, s)
		}
		last = i
	}
}

func TestInsertAndLineOf(t *testing.T) {
	obj := deployment()
	segs := []yamltree.Seg{{Key: "spec", Index: -1}, {Key: "selector", Index: -1}, {Key: "matchLabels", Index: -1}, {Key: "app", Index: -1}}
	got, err := Insert(obj, segs, "web", true)
	if err != nil || !reflect.DeepEqual(got, segs) {
		t.Fatalf("Insert = %v, %v", got, err)
	}
	// A new container: Append resolves to index 1.
	csegs := []yamltree.Seg{{Key: "spec", Index: -1}, {Key: "template", Index: -1}, {Key: "spec", Index: -1},
		{Key: "containers", Index: -1}, {Index: Append}, {Key: "name", Index: -1}}
	got, err = Insert(obj, csegs, "", false)
	if err != nil || got[4].Index != 1 {
		t.Fatalf("Insert append = %v, %v", got, err)
	}
	// Adding an existing field without a value keeps it.
	if _, err := Insert(obj, []yamltree.Seg{{Key: "spec", Index: -1}, {Key: "replicas", Index: -1}}, int64(0), false); err != nil {
		t.Fatal(err)
	}
	if r := obj["spec"].(map[string]any)["replicas"]; r != int64(2) {
		t.Fatalf("replicas = %v", r)
	}
	if _, err := Insert(obj, []yamltree.Seg{{Key: "kind", Index: -1}, {Key: "x", Index: -1}}, 1, true); err == nil {
		t.Fatal("inserting below a scalar should fail")
	}

	doc, _ := ToYAML(obj)
	lines := strings.Split(string(doc), "\n")
	if l := LineOf(doc, segs); l == 0 || strings.TrimSpace(lines[l-1]) != "app: web" {
		t.Fatalf("LineOf(app) = %d in\n%s", l, doc)
	}
	if l := LineOf(doc, got); l == 0 || !strings.Contains(lines[l-1], `name: ""`) {
		t.Fatalf("LineOf(new container name) = %d in\n%s", l, doc)
	}
}

func TestParseValue(t *testing.T) {
	cases := []struct {
		text string
		s    *schema.Schema
		want any
	}{
		{"3", &schema.Schema{Kind: schema.Integer}, int64(3)},
		{"true", &schema.Schema{Kind: schema.String}, "true"},
		{`"x y"`, &schema.Schema{Kind: schema.String}, "x y"},
		{"80", &schema.Schema{Kind: schema.IntOrString}, int64(80)},
		{"http", &schema.Schema{Kind: schema.IntOrString}, "http"},
		{"[a, b]", &schema.Schema{Kind: schema.Array}, []any{"a", "b"}},
		{"a", &schema.Schema{Kind: schema.Array}, []any{"a"}},
	}
	for _, c := range cases {
		got, err := ParseValue(c.text, c.s)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseValue(%q) = %#v, %v; want %#v", c.text, got, err, c.want)
		}
	}
	if _, err := ParseValue("x", &schema.Schema{Kind: schema.Integer}); err == nil {
		t.Error("expected an error for a non-integer")
	}
}

func TestSessionRounds(t *testing.T) {
	orig := &unstructured.Unstructured{Object: deployment()}
	s, doc, err := Start(orig, orig.Object, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, out, err := s.Read(); err != nil || out != NotSaved {
		t.Fatalf("untouched file: %v, %v", out, err)
	}
	raw, _ := os.ReadFile(s.path)
	os.WriteFile(s.path, raw, 0o600)
	future := time.Now().Add(time.Second)
	os.Chtimes(s.path, future, future)
	if _, out, err := s.Read(); err != nil || out != Unchanged {
		t.Fatalf("saved without changes: %v, %v", out, err)
	}

	edited := strings.Replace(string(doc), "replicas: 2", "replicas: 5", 1)
	os.WriteFile(s.path, []byte("# my comment\n"+edited), 0o600)
	u, out, err := s.Read()
	if err != nil || out != Changed {
		t.Fatalf("edited: %v, %v", out, err)
	}
	if r, _, _ := unstructured.NestedFloat64(u.Object, "spec", "replicas"); r != 5 {
		if ri, _, _ := unstructured.NestedInt64(u.Object, "spec", "replicas"); ri != 5 {
			t.Fatalf("replicas = %v", u.Object["spec"])
		}
	}

	// The server rejects it: the error goes on top, and quitting without
	// further changes abandons the edit.
	if err := s.Reject(errorString("replicas: too many")); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(s.path)
	if !strings.Contains(string(raw), "# error: replicas: too many") || !strings.Contains(string(raw), "replicas: 5") {
		t.Fatalf("after reject:\n%s", raw)
	}
	if _, out, _ := s.Read(); out != NotSaved {
		t.Fatalf("quit after reject: %v", out)
	}
	os.Chtimes(s.path, future.Add(time.Second), future.Add(time.Second))
	if _, out, _ := s.Read(); out != Abandoned {
		t.Fatalf("unchanged after reject: %v", out)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

func TestEditorCommand(t *testing.T) {
	t.Setenv("KUBE_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "code --wait")
	c := editorCommand("/tmp/x.yaml", 12)
	if got := strings.Join(c.Args, " "); got != "code --wait --goto /tmp/x.yaml:12" {
		t.Errorf("args = %q", got)
	}
	t.Setenv("EDITOR", "nvim")
	if got := strings.Join(editorCommand("/tmp/x.yaml", 3).Args, " "); got != "nvim +3 /tmp/x.yaml" {
		t.Errorf("args = %q", got)
	}
}
