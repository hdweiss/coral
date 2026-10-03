package ui

import (
	"strings"
	"testing"

	"github.com/hdweiss/coral/internal/schema"
	"github.com/hdweiss/coral/internal/yamltree"
	k8sschema "k8s.io/apimachinery/pkg/runtime/schema"
)

func TestInfoAt(t *testing.T) {
	d, err := schema.NewBuiltin().Lookup(k8sschema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"})
	if err != nil {
		t.Fatal(err)
	}
	fi, ok := infoAt(d, []yamltree.Seg{seg("spec"), seg("replicas")})
	if !ok || fi.typ != "int32" || !strings.HasPrefix(fi.desc, "Number of desired pods") {
		t.Fatalf("replicas = %+v", fi)
	}
	// A list item: the container type, described as an entry of its list.
	fi, ok = infoAt(d, []yamltree.Seg{seg("spec"), seg("template"), seg("spec"), seg("containers"), {Index: 0}})
	if !ok || fi.typ != "Container" || !strings.HasPrefix(fi.desc, "An entry of containers.") {
		t.Fatalf("container = %+v", fi)
	}
	fi, ok = infoAt(d, []yamltree.Seg{seg("spec"), seg("template"), seg("spec"), seg("containers"), {Index: 0}, seg("name")})
	if !ok || !fi.required {
		t.Fatalf("container name = %+v", fi)
	}
	// A map entry.
	fi, ok = infoAt(d, []yamltree.Seg{seg("metadata"), seg("labels"), seg("app")})
	if !ok || fi.typ != "string" || !strings.HasPrefix(fi.desc, "An entry of labels.") {
		t.Fatalf("label = %+v", fi)
	}
	if _, ok := infoAt(d, []yamltree.Seg{seg("spec"), seg("bogus")}); ok {
		t.Fatal("unknown field should not be found")
	}
}

func TestExtractLinks(t *testing.T) {
	text, urls := extractLinks("Labels. More info: http://kubernetes.io/docs/user-guide/labels. See also (https://example.com/a) and http://kubernetes.io/docs/user-guide/labels")
	want := "Labels. More info: [1]. See also ([2]) and [1]"
	if text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if len(urls) != 2 || urls[0] != "http://kubernetes.io/docs/user-guide/labels" || urls[1] != "https://example.com/a" {
		t.Errorf("urls = %q", urls)
	}
}
