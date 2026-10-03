package schema

import (
	"reflect"
	"sort"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
)

// Builtin derives schemas of the built-in kinds from their Go types, with
// descriptions from the generated SwaggerDoc methods. It needs no cluster.
type Builtin struct {
	mu    sync.Mutex
	types map[reflect.Type]*Schema
}

func NewBuiltin() *Builtin { return &Builtin{types: map[reflect.Type]*Schema{}} }

func (b *Builtin) Lookup(gvk schema.GroupVersionKind) (*Schema, error) {
	obj, err := scheme.Scheme.New(gvk)
	if err != nil {
		return nil, &NotFoundError{gvk}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.of(reflect.TypeOf(obj).Elem()), nil
}

var (
	timeType      = reflect.TypeFor[metav1.Time]()
	microTimeType = reflect.TypeFor[metav1.MicroTime]()
	durationType  = reflect.TypeFor[metav1.Duration]()
	quantityType  = reflect.TypeFor[resource.Quantity]()
	intOrStrType  = reflect.TypeFor[intstr.IntOrString]()
	rawExtType    = reflect.TypeFor[runtime.RawExtension]()
	bytesType     = reflect.TypeFor[[]byte]()
)

type swaggerDoc interface{ SwaggerDoc() map[string]string }

// of returns the schema of t. Struct schemas are cached before their fields
// are filled in, so recursive types terminate.
func (b *Builtin) of(t reflect.Type) *Schema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if s, ok := b.types[t]; ok {
		return s
	}
	switch t {
	case timeType, microTimeType:
		return &Schema{Kind: String, Format: "date-time"}
	case durationType:
		return &Schema{Kind: String, Name: "Duration"}
	case quantityType:
		return &Schema{Kind: IntOrString, Name: "Quantity"}
	case intOrStrType:
		return &Schema{Kind: IntOrString}
	case rawExtType:
		return &Schema{Kind: Any}
	case bytesType:
		return &Schema{Kind: String, Format: "byte"}
	}
	switch t.Kind() {
	case reflect.String:
		return &Schema{Kind: String}
	case reflect.Bool:
		return &Schema{Kind: Boolean}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		format := ""
		switch t.Kind() {
		case reflect.Int32:
			format = "int32"
		case reflect.Int64:
			format = "int64"
		}
		return &Schema{Kind: Integer, Format: format}
	case reflect.Float32, reflect.Float64:
		return &Schema{Kind: Number}
	case reflect.Slice, reflect.Array:
		return &Schema{Kind: Array, Elem: b.of(t.Elem())}
	case reflect.Map:
		return &Schema{Kind: Map, Elem: b.of(t.Elem())}
	case reflect.Struct:
		s := &Schema{Kind: Object, Name: t.Name()}
		b.types[t] = s
		docs := map[string]string{}
		if d, ok := reflect.New(t).Elem().Interface().(swaggerDoc); ok {
			docs = d.SwaggerDoc()
		}
		s.Description = docs[""]
		b.addFields(s, t, docs)
		sort.Slice(s.Fields, func(i, j int) bool { return s.Fields[i].Name < s.Fields[j].Name })
		return s
	}
	return &Schema{Kind: Any}
}

func (b *Builtin) addFields(s *Schema, t reflect.Type, docs map[string]string) {
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" || (!f.IsExported() && !f.Anonymous) {
			continue
		}
		if f.Anonymous && (name == "" || strings.Contains(opts, "inline")) {
			// Embedded structs such as TypeMeta are inlined into the parent.
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				inner := map[string]string{}
				if d, ok := reflect.New(ft).Elem().Interface().(swaggerDoc); ok {
					inner = d.SwaggerDoc()
				}
				b.addFields(s, ft, inner)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		optional := strings.Contains(opts, "omitempty") || strings.Contains(opts, "omitzero") ||
			f.Type.Kind() == reflect.Pointer
		fs := b.of(f.Type)
		desc := docs[name]
		if desc == "" {
			desc = fs.Description
		}
		s.Fields = append(s.Fields, &Field{Name: name, Description: desc, Required: !optional, Schema: fs})
	}
}
