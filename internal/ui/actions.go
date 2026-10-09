package ui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/hdweiss/coral/internal/k8s"
	"github.com/hdweiss/coral/internal/yamltree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// action is something to do with the selected object. Its key works in the
// table, the describe view and the details; the action menu (. or a right
// click), the status bar and the help list the actions that apply, so a new
// action only needs an entry in objectActions.
type action struct {
	key   string // as tea names it
	label string // short, for the status bar and the menu
	desc  string // for the menu and the help
	write bool   // changes the cluster: refused in read-only contexts
	// on reports whether the action applies to res; nil is every resource.
	on  func(res k8s.Resource) bool
	run func(a *App, obj *unstructured.Unstructured) tea.Cmd
}

// kinds applies an action to the resources with the given IDs.
func kinds(ids ...string) func(k8s.Resource) bool {
	return func(r k8s.Resource) bool { return slices.Contains(ids, r.ID()) }
}

// objectActions lists every action, in menu and status bar order. It is a
// function rather than a variable because the actions refer back to App
// methods that read it.
func objectActions() []action {
	return []action{
		{key: "L", label: "logs", desc: "show the pod's log", on: kinds("pods"),
			run: func(a *App, _ *unstructured.Unstructured) tea.Cmd { return a.openLogs() }},
		{key: "p", label: "previous logs", desc: "show the previous container's log", on: kinds("pods"),
			run: func(a *App, _ *unstructured.Unstructured) tea.Cmd { return a.previousLogs() }},
		{key: "d", label: "describe", desc: "related objects and events",
			run: func(a *App, _ *unstructured.Unstructured) tea.Cmd { return a.openDescribe(false) }},
		{key: "E", label: "events", desc: "the object's events",
			run: func(a *App, _ *unstructured.Unstructured) tea.Cmd { return a.openDescribe(true) }},
		{key: "o", label: "instances", desc: "list the CRD's instances", on: kinds("customresourcedefinitions"),
			run: func(a *App, _ *unstructured.Unstructured) tea.Cmd { return a.openInstances() }},
		{key: "e", label: "edit", desc: "edit in $EDITOR", write: true,
			run: func(a *App, _ *unstructured.Unstructured) tea.Cmd {
				var focus []yamltree.Seg
				if a.focus == focusDetail {
					if n := a.detail.current(); n != nil {
						focus = n.Segments()
					}
				}
				return a.startEdit(focus)
			}},
		{key: "ctrl+n", label: "add", desc: "add a field from the API schema", write: true,
			run: func(a *App, _ *unstructured.Unstructured) tea.Cmd { return a.startAdd() }},
	}
}

// selected returns the object the actions work on and its resource: the
// selected row of the describe view or the table.
func (a *App) selected() (*unstructured.Unstructured, k8s.Resource, bool) {
	if a.logs != nil {
		return nil, k8s.Resource{}, false
	}
	_, obj := a.selection()
	if obj == nil {
		return nil, k8s.Resource{}, false
	}
	if a.desc == nil {
		return obj, a.table.res, true
	}
	res, ok := a.registry(a.cur.Context).ForKind(obj.GetAPIVersion(), obj.GetKind())
	if !ok {
		res = k8s.Resource{Kind: obj.GetKind()}
	}
	return obj, res, true
}

// actionsFor lists the actions that apply to res, without those that write
// in a read-only context.
func (a *App) actionsFor(res k8s.Resource) []action {
	var out []action
	for _, act := range objectActions() {
		if (act.on == nil || act.on(res)) && !(act.write && a.readOnly()) {
			out = append(out, act)
		}
	}
	return out
}

// actionKey runs the action bound to key for the selected object, if there
// is one.
func (a *App) actionKey(key string) (tea.Cmd, bool) {
	obj, res, ok := a.selected()
	if !ok {
		return nil, false
	}
	for _, act := range objectActions() {
		if act.key == key && (act.on == nil || act.on(res)) {
			return a.runAction(act, obj), true
		}
	}
	return nil, false
}

func (a *App) runAction(act action, obj *unstructured.Unstructured) tea.Cmd {
	if act.write && a.refuseWrite() {
		return nil
	}
	return act.run(a, obj)
}

// readOnly reports whether the current context may not be changed: --readonly
// or a readonly pattern of config.yaml.
func (a *App) readOnly() bool {
	return a.opts.ReadOnly || a.opts.Settings.IsReadOnly(a.cur.Context)
}

// refuseWrite says so and reports true in a read-only context.
func (a *App) refuseWrite() bool {
	if a.readOnly() {
		a.setFlash("read-only: "+a.cur.Context+" can't be changed", true)
		return true
	}
	return false
}

// openActionMenu lists the actions for the selected object in the palette.
func (a *App) openActionMenu() tea.Cmd {
	obj, res, ok := a.selected()
	if !ok {
		a.setFlash("select an object first", true)
		return nil
	}
	var items []paletteItem
	for _, act := range a.actionsFor(res) {
		act := act
		items = append(items, paletteItem{cmd: act.label, desc: keyLabel(act.key) + " · " + act.desc,
			run: func() tea.Cmd { return a.runAction(act, obj) }})
	}
	if len(items) == 0 {
		a.setFlash("no actions for "+obj.GetKind(), false)
		return nil
	}
	a.palette = newPalette(items, "")
	a.palette.title = obj.GetKind() + " " + obj.GetName()
	a.palette.input.Placeholder = "action"
	return a.palette.input.Focus()
}

// keyLabel shortens a key name for the status bar: ctrl+n → ^n.
func keyLabel(k string) string {
	if len(k) > 5 && k[:5] == "ctrl+" {
		return "^" + k[5:]
	}
	return k
}
