// Package edit edits Kubernetes objects as YAML in the user's editor, and
// inserts new fields into objects.
package edit

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/hdweiss/coral/internal/schema"
	"github.com/hdweiss/coral/internal/yamltree"
	"go.yaml.in/yaml/v3"
)

// Append as a Seg.Index means "a new item at the end of the list".
const Append = -2

// ToYAML renders obj with keys in the same order as the tree view
// (apiVersion, kind, metadata, spec, …), without managedFields.
func ToYAML(obj map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	n, err := toNode("", withoutManagedFields(obj))
	if err != nil {
		return nil, err
	}
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

// withoutManagedFields returns obj without .metadata.managedFields, which
// nobody edits by hand. The server keeps them when an update omits them.
func withoutManagedFields(obj map[string]any) map[string]any {
	md, ok := obj["metadata"].(map[string]any)
	if !ok {
		return obj
	}
	if _, ok := md["managedFields"]; !ok {
		return obj
	}
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		out[k] = v
	}
	mdc := make(map[string]any, len(md))
	for k, v := range md {
		if k != "managedFields" {
			mdc[k] = v
		}
	}
	out["metadata"] = mdc
	return out
}

func toNode(path string, v any) (*yaml.Node, error) {
	switch t := v.(type) {
	case map[string]any:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if len(t) == 0 {
			n.Style = yaml.FlowStyle
		}
		for _, k := range yamltree.OrderedKeys(path, t) {
			child, err := toNode(path+"."+k, t[k])
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, child)
		}
		return n, nil
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		if len(t) == 0 {
			n.Style = yaml.FlowStyle
		}
		for _, item := range t {
			child, err := toNode(path+"[]", item)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, child)
		}
		return n, nil
	}
	n := &yaml.Node{}
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	if s, ok := v.(string); ok && strings.Contains(strings.TrimRight(s, "\n"), "\n") {
		n.Style = yaml.LiteralStyle
	}
	return n, nil
}

// LineOf returns the 1-based line of the field at segs in the YAML document
// doc, or 0 when it is not there. Seg indices must be concrete (not Append).
func LineOf(doc []byte, segs []yamltree.Seg) int {
	var root yaml.Node
	if err := yaml.Unmarshal(doc, &root); err != nil || len(root.Content) == 0 {
		return 0
	}
	n := root.Content[0]
	line := n.Line
	for _, sg := range segs {
		switch {
		case sg.Index < 0 && n.Kind == yaml.MappingNode:
			found := false
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == sg.Key {
					line, n, found = n.Content[i].Line, n.Content[i+1], true
					break
				}
			}
			if !found {
				return 0
			}
		case sg.Index >= 0 && n.Kind == yaml.SequenceNode && sg.Index < len(n.Content):
			n = n.Content[sg.Index]
			line = n.Line
		default:
			return 0
		}
	}
	return line
}

// Insert sets the value at segs in obj, creating maps and lists on the way.
// A Seg with Index Append adds a new list item. An existing value is only
// replaced when explicit is set; otherwise it is kept, so that adding a field
// that is already there just points at it. It returns segs with Append
// resolved to the index of the new item.
func Insert(obj map[string]any, segs []yamltree.Seg, value any, explicit bool) ([]yamltree.Seg, error) {
	_, out, err := insert(obj, segs, value, explicit, "")
	return out, err
}

func insert(cur any, segs []yamltree.Seg, value any, explicit bool, at string) (any, []yamltree.Seg, error) {
	if len(segs) == 0 {
		if cur == nil || explicit {
			return value, nil, nil
		}
		return cur, nil, nil
	}
	sg := segs[0]
	switch {
	case sg.Index < 0 && sg.Index != Append:
		m, ok := cur.(map[string]any)
		if cur == nil {
			m, ok = map[string]any{}, true
		}
		if !ok {
			return nil, nil, fmt.Errorf("%s is not an object", pathString(at))
		}
		child, rest, err := insert(m[sg.Key], segs[1:], value, explicit, at+"."+sg.Key)
		if err != nil {
			return nil, nil, err
		}
		m[sg.Key] = child
		return m, append([]yamltree.Seg{sg}, rest...), nil
	default:
		l, ok := cur.([]any)
		if cur == nil {
			ok = true
		}
		if !ok {
			return nil, nil, fmt.Errorf("%s is not a list", pathString(at))
		}
		i := sg.Index
		if i == Append {
			l = append(l, nil)
			i = len(l) - 1
		}
		if i >= len(l) {
			return nil, nil, fmt.Errorf("%s has no item %d", pathString(at), i)
		}
		child, rest, err := insert(l[i], segs[1:], value, explicit, at+"["+strconv.Itoa(i)+"]")
		if err != nil {
			return nil, nil, err
		}
		l[i] = child
		return l, append([]yamltree.Seg{{Index: i}}, rest...), nil
	}
}

func pathString(p string) string {
	if p == "" {
		return "the object"
	}
	return p
}

// ParseValue converts text typed by the user into a value of schema s.
func ParseValue(text string, s *schema.Schema) (any, error) {
	text = strings.TrimSpace(text)
	if s == nil {
		s = &schema.Schema{}
	}
	switch s.Kind {
	case schema.String:
		if u, err := strconv.Unquote(text); err == nil && strings.HasPrefix(text, `"`) {
			return u, nil
		}
		return text, nil
	case schema.Integer:
		v, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", text)
		}
		return v, nil
	case schema.Number:
		v, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", text)
		}
		return v, nil
	case schema.Boolean:
		v, err := strconv.ParseBool(text)
		if err != nil {
			return nil, fmt.Errorf("%q is not true or false", text)
		}
		return v, nil
	case schema.IntOrString:
		if v, err := strconv.ParseInt(text, 10, 64); err == nil {
			return v, nil
		}
		return text, nil
	}
	// Anything else is YAML, e.g. "[a, b]" or "{x: 1}".
	var v any
	if err := yaml.Unmarshal([]byte(text), &v); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if s.Kind == schema.Array {
		if _, ok := v.([]any); !ok {
			v = []any{v}
		}
	}
	return normalize(v), nil
}

// normalize converts YAML-decoded values to JSON types (int → int64).
func normalize(v any) any {
	switch t := v.(type) {
	case int:
		return int64(t)
	case map[string]any:
		for k, x := range t {
			t[k] = normalize(x)
		}
	case []any:
		for i, x := range t {
			t[i] = normalize(x)
		}
	}
	return v
}
