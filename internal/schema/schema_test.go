package schema

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kube-openapi/pkg/spec3"
)

func TestBuiltinDeployment(t *testing.T) {
	b := NewBuiltin()
	s, err := b.Lookup(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"})
	if err != nil {
		t.Fatal(err)
	}
	// TypeMeta and ObjectMeta fields are inlined or nested as in YAML.
	for _, name := range []string{"apiVersion", "kind", "metadata", "spec", "status"} {
		if s.Field(name) == nil {
			t.Errorf("missing field %s", name)
		}
	}
	spec := s.Field("spec").Schema
	sel := spec.Field("selector")
	if sel == nil || sel.Schema.Name != "LabelSelector" || !strings.Contains(sel.Description, "Label selector") {
		t.Fatalf("selector = %+v", sel)
	}
	ml := sel.Schema.Field("matchLabels").Schema
	if ml.String() != "map[string]string" {
		t.Errorf("matchLabels type = %s", ml)
	}
	containers := spec.Field("template").Schema.Field("spec").Schema.Field("containers")
	if containers.Schema.String() != "[]Container" || !containers.Schema.Container() {
		t.Errorf("containers type = %s", containers.Schema)
	}
	name := containers.Schema.Elem.Field("name")
	if !name.Required || name.Description == "" {
		t.Errorf("container name = %+v", name)
	}
	if r := spec.Field("replicas").Schema; r.String() != "int32" || r.Zero() != int64(0) {
		t.Errorf("replicas = %s %v", r, r.Zero())
	}
	if q := containers.Schema.Elem.Field("resources").Schema.Field("limits").Schema.Elem; q.String() != "quantity" {
		t.Errorf("limits values = %s", q)
	}
}

func TestBuiltinUnknownKind(t *testing.T) {
	_, err := NewBuiltin().Lookup(schema.GroupVersionKind{Group: "example.com", Version: "v1", Kind: "Widget"})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestOpenAPIConvert(t *testing.T) {
	const doc = `{
	  "openapi": "3.0.0",
	  "components": {"schemas": {
	    "com.example.v1.Widget": {
	      "type": "object",
	      "x-kubernetes-group-version-kind": [{"group": "example.com", "version": "v1", "kind": "Widget"}],
	      "properties": {
	        "spec": {"allOf": [{"$ref": "#/components/schemas/com.example.v1.WidgetSpec"}], "description": "Desired state."}
	      }
	    },
	    "com.example.v1.WidgetSpec": {
	      "type": "object",
	      "required": ["size"],
	      "properties": {
	        "size": {"type": "integer", "format": "int32", "description": "How big."},
	        "port": {"x-kubernetes-int-or-string": true},
	        "labels": {"type": "object", "additionalProperties": {"type": "string"}},
	        "parts": {"type": "array", "items": {"$ref": "#/components/schemas/com.example.v1.WidgetSpec"}},
	        "mode": {"type": "string", "enum": ["fast", "slow"]}
	      }
	    }
	  }}
	}`
	var s spec3.OpenAPI
	if err := json.Unmarshal([]byte(doc), &s); err != nil {
		t.Fatal(err)
	}
	d := &openAPIDoc{spec: &s, converted: map[string]*Schema{}}
	w := d.component("com.example.v1.Widget")
	spec := w.Field("spec")
	if spec.Description != "Desired state." || spec.Schema.Name != "WidgetSpec" {
		t.Fatalf("spec = %+v", spec)
	}
	ws := spec.Schema
	if f := ws.Field("size"); !f.Required || f.Schema.String() != "int32" {
		t.Errorf("size = %+v", f)
	}
	if ws.Field("port").Schema.Kind != IntOrString || ws.Field("labels").Schema.String() != "map[string]string" {
		t.Errorf("port/labels wrong")
	}
	if p := ws.Field("parts").Schema; p.Elem != d.component("com.example.v1.WidgetSpec") { // recursive reference is shared
		t.Errorf("parts elem = %+v", p.Elem)
	}
	if m := ws.Field("mode").Schema; m.Zero() != "fast" {
		t.Errorf("mode zero = %v", m.Zero())
	}
}
