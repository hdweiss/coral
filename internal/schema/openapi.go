package schema

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/openapi"
	"k8s.io/client-go/openapi3"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

// OpenAPI reads schemas from a cluster's OpenAPI v3 endpoint, which also
// covers custom resources. Documents are fetched once per group version.
type OpenAPI struct {
	root openapi3.Root

	mu   sync.Mutex
	docs map[schema.GroupVersion]*openAPIDoc
}

func NewOpenAPI(c openapi.Client) *OpenAPI {
	return &OpenAPI{root: openapi3.NewRoot(c), docs: map[schema.GroupVersion]*openAPIDoc{}}
}

type openAPIDoc struct {
	spec      *spec3.OpenAPI
	converted map[string]*Schema // by component name
}

func (o *OpenAPI) Lookup(gvk schema.GroupVersionKind) (*Schema, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	gv := gvk.GroupVersion()
	doc, ok := o.docs[gv]
	if !ok {
		s, err := o.root.GVSpec(gv)
		if err != nil {
			return nil, fmt.Errorf("openapi %s: %w", gv, err)
		}
		doc = &openAPIDoc{spec: s, converted: map[string]*Schema{}}
		o.docs[gv] = doc
	}
	if doc.spec.Components == nil {
		return nil, &NotFoundError{gvk}
	}
	// Find the component tagged with this group version kind.
	names := make([]string, 0, len(doc.spec.Components.Schemas))
	for name := range doc.spec.Components.Schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var gvks []struct{ Group, Version, Kind string }
		if err := doc.spec.Components.Schemas[name].Extensions.GetObject("x-kubernetes-group-version-kind", &gvks); err != nil {
			continue
		}
		for _, g := range gvks {
			if g.Group == gvk.Group && g.Version == gvk.Version && g.Kind == gvk.Kind {
				return doc.component(name), nil
			}
		}
	}
	return nil, &NotFoundError{gvk}
}

// component converts a named schema, caching it before its fields are
// filled in so recursive schemas terminate.
func (d *openAPIDoc) component(name string) *Schema {
	if s, ok := d.converted[name]; ok {
		return s
	}
	src, ok := d.spec.Components.Schemas[name]
	if !ok {
		return &Schema{Kind: Any}
	}
	s := &Schema{}
	d.converted[name] = s
	*s = *d.convert(src)
	if s.Kind == Object {
		s.Name = name[strings.LastIndex(name, ".")+1:]
	}
	return s
}

func (d *openAPIDoc) convert(src *spec.Schema) *Schema {
	if ref := src.Ref.String(); ref != "" {
		return d.component(strings.TrimPrefix(ref, "#/components/schemas/"))
	}
	// Kubernetes wraps references that carry their own description:
	// {allOf: [{$ref}], description}.
	if len(src.AllOf) == 1 && len(src.Properties) == 0 {
		inner := d.convert(&src.AllOf[0])
		if src.Description == "" || src.Description == inner.Description {
			return inner
		}
		s := *inner
		s.Description = src.Description
		return &s
	}

	s := &Schema{Description: src.Description, Format: src.Format}
	for _, e := range src.Enum {
		if v, ok := e.(string); ok {
			s.Enum = append(s.Enum, v)
		}
	}
	if b, _ := src.Extensions.GetBool("x-kubernetes-int-or-string"); b {
		s.Kind = IntOrString
		return s
	}
	typ := ""
	if len(src.Type) > 0 {
		typ = src.Type[0]
	}
	switch {
	case typ == "string":
		s.Kind = String
	case typ == "integer":
		s.Kind = Integer
	case typ == "number":
		s.Kind = Number
	case typ == "boolean":
		s.Kind = Boolean
	case typ == "array":
		s.Kind = Array
		s.Elem = &Schema{Kind: Any}
		if src.Items != nil && src.Items.Schema != nil {
			s.Elem = d.convert(src.Items.Schema)
		}
	case len(src.Properties) > 0:
		s.Kind = Object
		for name, p := range src.Properties {
			ps := d.convert(&p)
			desc := p.Description
			if desc == "" {
				desc = ps.Description
			}
			s.Fields = append(s.Fields, &Field{
				Name:        name,
				Description: desc,
				Required:    slices.Contains(src.Required, name),
				Schema:      ps,
			})
		}
		sort.Slice(s.Fields, func(i, j int) bool { return s.Fields[i].Name < s.Fields[j].Name })
	case src.AdditionalProperties != nil && src.AdditionalProperties.Schema != nil:
		s.Kind = Map
		s.Elem = d.convert(src.AdditionalProperties.Schema)
	case typ == "object":
		// An object without declared properties, e.g. preserve-unknown-fields.
		s.Kind = Map
		s.Elem = &Schema{Kind: Any}
	}
	return s
}
