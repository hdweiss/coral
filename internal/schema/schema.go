// Package schema describes the fields of Kubernetes objects, for helping the
// user add fields. Schemas come from the cluster's OpenAPI v3 document, or
// from the Go types of the built-in API when there is no cluster to ask.
package schema

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Kind is the shape of a value.
type Kind int

const (
	Any Kind = iota // anything; arbitrary JSON
	Object
	Array
	Map
	String
	Integer
	Number
	Boolean
	IntOrString
)

// Schema describes a value. Schemas may be shared and cyclic.
type Schema struct {
	Kind        Kind
	Name        string // type name for display, e.g. "Container"; may be empty
	Description string
	Format      string   // e.g. int32, date-time, byte
	Enum        []string // allowed values of a string
	Fields      []*Field // Object, sorted by name
	Elem        *Schema  // Array items or Map values
}

// Field is one property of an object.
type Field struct {
	Name        string
	Description string // falls back to the type's description
	Required    bool
	Schema      *Schema
}

// Field returns the field with the given name, or nil.
func (s *Schema) Field(name string) *Field {
	if s == nil {
		return nil
	}
	i := slices.IndexFunc(s.Fields, func(f *Field) bool { return f.Name == name })
	if i < 0 {
		return nil
	}
	return s.Fields[i]
}

// Container reports whether the value holds named or indexed children that
// can be added one by one: an object, a map, or a list of objects.
func (s *Schema) Container() bool {
	if s == nil {
		return false
	}
	switch s.Kind {
	case Object, Map:
		return true
	case Array:
		return s.Elem != nil && s.Elem.Kind == Object
	}
	return false
}

// String returns the type for display, like "[]Container" or "map[string]string".
func (s *Schema) String() string {
	if s == nil {
		return "any"
	}
	switch s.Kind {
	case Object:
		if s.Name != "" {
			return s.Name
		}
		return "object"
	case Array:
		return "[]" + s.Elem.String()
	case Map:
		return "map[string]" + s.Elem.String()
	case String:
		if s.Format == "date-time" {
			return "time"
		}
		return "string"
	case Integer:
		if s.Format != "" {
			return s.Format
		}
		return "integer"
	case Number:
		return "number"
	case Boolean:
		return "boolean"
	case IntOrString:
		if s.Name == "Quantity" {
			return "quantity"
		}
		return "int-or-string"
	}
	return "any"
}

// Zero returns the placeholder value used when the user adds a field without
// typing a value.
func (s *Schema) Zero() any {
	if s == nil {
		return nil
	}
	switch s.Kind {
	case Object, Map:
		return map[string]any{}
	case Array:
		return []any{}
	case String, IntOrString:
		if len(s.Enum) > 0 {
			return s.Enum[0]
		}
		return ""
	case Integer, Number:
		return int64(0)
	case Boolean:
		return false
	}
	return nil
}

// Summary is the first sentence of a description, for one-line display.
func Summary(desc string) string {
	desc = strings.Join(strings.Fields(desc), " ")
	if i := strings.Index(desc, ". "); i >= 0 {
		return desc[:i+1]
	}
	return desc
}

// Source finds the schema of a kind.
type Source interface {
	Lookup(gvk schema.GroupVersionKind) (*Schema, error)
}

// Chain asks each source in turn and returns the first schema found.
type Chain []Source

func (c Chain) Lookup(gvk schema.GroupVersionKind) (*Schema, error) {
	var firstErr error
	for _, s := range c {
		sc, err := s.Lookup(gvk)
		if err == nil && sc != nil {
			return sc, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = &NotFoundError{gvk}
	}
	return nil, firstErr
}

type NotFoundError struct{ GVK schema.GroupVersionKind }

func (e *NotFoundError) Error() string { return "no schema for " + e.GVK.String() }
