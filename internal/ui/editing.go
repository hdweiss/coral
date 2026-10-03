package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/hdweiss/coralctl/internal/edit"
	"github.com/hdweiss/coralctl/internal/k8s"
	"github.com/hdweiss/coralctl/internal/schema"
	"github.com/hdweiss/coralctl/internal/yamltree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Editing an object goes: fetch it fresh → write it to a temp file → run
// $EDITOR → read it back → update on the server. When the server or the YAML
// parser rejects it, the editor opens again with the error on top, like
// kubectl edit. Adding a field ("a") runs the same flow with the field
// inserted and the cursor on it.

type (
	// editReadyMsg carries a fresh object and what to show in the editor.
	editReadyMsg struct {
		key   k8s.Key
		orig  *unstructured.Unstructured
		body  map[string]any
		focus []yamltree.Seg // where to put the cursor
		note  string         // header note, e.g. what was added
		err   error
	}
	editorExitMsg struct{ err error }
	updatedMsg    struct {
		obj *unstructured.Unstructured
		err error
	}
	schemaMsg struct {
		target addTarget
		schema *schema.Schema // of the whole kind
		err    error
	}
)

// editState is the editing in progress; there is at most one.
type editState struct {
	session *edit.Session
	key     k8s.Key
	desc    string
}

// addTarget is where the "add field" dialog adds: an object and a path in it.
type addTarget struct {
	key  k8s.Key
	obj  *unstructured.Unstructured
	segs []yamltree.Seg
	path string // for display, e.g. .spec.template
}

// startEdit opens the selected object in the editor, with the cursor on
// focus when it is given.
func (a *App) startEdit(focus []yamltree.Seg) tea.Cmd {
	obj := a.table.Selected()
	if obj == nil {
		a.setFlash("nothing selected to edit", true)
		return nil
	}
	if a.editing != nil {
		return nil
	}
	return a.fetchForEdit(a.cur, obj, "", func(fresh *unstructured.Unstructured) (map[string]any, []yamltree.Seg, error) {
		return fresh.DeepCopy().Object, focus, nil
	})
}

// fetchForEdit reads obj fresh from the cluster, so the edit starts from the
// latest version, and prepares the editor content with prepare.
func (a *App) fetchForEdit(key k8s.Key, obj *unstructured.Unstructured,
	note string, prepare func(fresh *unstructured.Unstructured) (map[string]any, []yamltree.Seg, error)) tea.Cmd {
	store := a.store
	ns, name := obj.GetNamespace(), obj.GetName()
	a.setFlash("loading "+obj.GetKind()+" "+name+"…", false)
	return func() tea.Msg {
		fresh, err := store.GetObject(key, ns, name)
		if err != nil {
			return editReadyMsg{err: err}
		}
		body, focus, err := prepare(fresh)
		return editReadyMsg{key: key, orig: fresh, body: body, focus: focus, note: note, err: err}
	}
}

func (a *App) onEditReady(msg editReadyMsg) tea.Cmd {
	if msg.err != nil {
		a.setFlash(msg.err.Error(), true)
		return nil
	}
	s, doc, err := edit.Start(msg.orig, msg.body, msg.note)
	if err != nil {
		a.setFlash("edit: "+err.Error(), true)
		return nil
	}
	a.editing = &editState{session: s, key: msg.key, desc: msg.orig.GetKind() + " " + msg.orig.GetName()}
	a.setFlash("", false)
	return a.runEditor(s.Line(edit.LineOf(doc, msg.focus), nil))
}

func (a *App) runEditor(line int) tea.Cmd {
	return tea.ExecProcess(a.editing.session.Command(line), func(err error) tea.Msg { return editorExitMsg{err} })
}

func (a *App) stopEditing(flash string, isErr bool) {
	if a.editing != nil {
		a.editing.session.Close()
		a.editing = nil
	}
	a.setFlash(flash, isErr)
}

func (a *App) onEditorExit(msg editorExitMsg) tea.Cmd {
	e := a.editing
	if e == nil {
		return nil
	}
	if msg.err != nil {
		a.stopEditing(fmt.Sprintf("editor %q failed: %v", edit.Editor()[0], msg.err), true)
		return nil
	}
	obj, outcome, err := e.session.Read()
	switch {
	case err != nil:
		return a.retryEdit(err)
	case outcome == edit.NotSaved:
		a.stopEditing("edit cancelled", false)
		return nil
	case outcome == edit.Unchanged:
		a.stopEditing("edit cancelled, no changes", false)
		return nil
	case outcome == edit.Abandoned:
		a.stopEditing("edit cancelled", false)
		return nil
	}
	store, key := a.store, e.key
	a.setFlash("applying "+e.desc+"…", false)
	return func() tea.Msg {
		u, err := store.Update(key, obj)
		return updatedMsg{obj: u, err: err}
	}
}

// retryEdit reopens the editor with problem shown at the top of the file.
func (a *App) retryEdit(problem error) tea.Cmd {
	if err := a.editing.session.Reject(problem); err != nil {
		a.stopEditing("edit: "+err.Error(), true)
		return nil
	}
	return a.runEditor(0)
}

func (a *App) onUpdated(msg updatedMsg) tea.Cmd {
	if a.editing == nil {
		return nil
	}
	if msg.err != nil {
		return a.retryEdit(msg.err)
	}
	key := a.editing.key
	a.stopEditing("updated "+a.editing.desc, false)
	return a.fetch(key)
}

// --- adding fields ---

// schemaSource returns the schema source of a context: the cluster's
// OpenAPI document, falling back to the built-in types.
func (a *App) schemaSource(ctx string) schema.Source {
	if s, ok := a.schemas[ctx]; ok {
		return s
	}
	var chain schema.Chain
	if c, err := a.store.Provider().OpenAPI(ctx); err == nil {
		chain = append(chain, schema.NewOpenAPI(c))
	}
	chain = append(chain, a.builtin)
	a.schemas[ctx] = chain
	return chain
}

// startAdd opens the "add field" dialog below the selected node of the
// detail view, or at the top level of the object from the table.
func (a *App) startAdd() tea.Cmd {
	obj := a.table.Selected()
	if obj == nil {
		a.setFlash("nothing selected", true)
		return nil
	}
	t := addTarget{key: a.cur, obj: obj}
	if a.focus == focusDetail {
		if n := addParent(a.detail.current()); n != nil {
			t.segs, t.path = n.Segments(), n.Path
		}
	}
	src := a.schemaSource(a.cur.Context)
	gvk := obj.GroupVersionKind()
	a.setFlash("loading schema…", false)
	return func() tea.Msg {
		s, err := src.Lookup(gvk)
		return schemaMsg{target: t, schema: s, err: err}
	}
}

// addParent is the node that new fields go below: n itself when it is a map
// or list, otherwise the map that contains it. Nil means the object itself.
func addParent(n *yamltree.Node) *yamltree.Node {
	for n != nil && n.Parent != nil {
		if n.Kind == yamltree.Map || n.Kind == yamltree.List {
			return n
		}
		n = n.Parent
	}
	return nil
}

func (a *App) onSchema(msg schemaMsg) tea.Cmd {
	if msg.err != nil {
		a.setFlash(msg.err.Error(), true)
		return nil
	}
	a.setFlash("", false)
	s := schemaAt(msg.schema, msg.target.segs)
	if s == nil || (!s.Container() && s.Kind != schema.Array) {
		a.setFlash("the schema does not describe "+msg.target.path, true)
		return nil
	}
	existing := valueAt(msg.target.obj.Object, msg.target.segs)
	title := "Add to " + msg.target.obj.GetKind() + " " + msg.target.obj.GetName()
	a.picker = newPicker(title, msg.target.path, s, existing)
	a.pickTarget = msg.target
	return a.picker.input.Focus()
}

// schemaAt follows segs from s.
func schemaAt(s *schema.Schema, segs []yamltree.Seg) *schema.Schema {
	for _, sg := range segs {
		if s == nil {
			return nil
		}
		switch {
		case sg.Index >= 0 && s.Kind == schema.Array:
			s = s.Elem
		case sg.Index < 0 && s.Kind == schema.Object:
			f := s.Field(sg.Key)
			if f == nil {
				return nil
			}
			s = f.Schema
		case sg.Index < 0 && s.Kind == schema.Map:
			s = s.Elem
		default:
			return nil
		}
	}
	return s
}

func valueAt(v any, segs []yamltree.Seg) any {
	for _, sg := range segs {
		switch t := v.(type) {
		case map[string]any:
			v = t[sg.Key]
		case []any:
			if sg.Index < 0 || sg.Index >= len(t) {
				return nil
			}
			v = t[sg.Index]
		default:
			return nil
		}
	}
	return v
}

// onPicked inserts the chosen field into a fresh copy of the object and opens
// it in the editor with the cursor on the new field.
func (a *App) onPicked(res *pickResult) tea.Cmd {
	t := a.pickTarget
	segs := append(append([]yamltree.Seg{}, t.segs...), res.segs...)
	note := "Added " + pathOf(segs) + "."
	return a.fetchForEdit(t.key, t.obj, note, func(fresh *unstructured.Unstructured) (map[string]any, []yamltree.Seg, error) {
		body := fresh.DeepCopy().Object
		at, err := edit.Insert(body, segs, res.value, res.explicit)
		if err != nil {
			return nil, nil, err
		}
		return body, at, nil
	})
}

// pathOf renders segs like a tree path; new list items show as [+].
func pathOf(segs []yamltree.Seg) string {
	var b strings.Builder
	for _, sg := range segs {
		switch {
		case sg.Index == edit.Append:
			b.WriteString("[+]")
		case sg.Index >= 0:
			b.WriteString("[" + strconv.Itoa(sg.Index) + "]")
		default:
			b.WriteString("." + sg.Key)
		}
	}
	return b.String()
}
