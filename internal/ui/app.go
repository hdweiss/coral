// Package ui implements the coral terminal UI.
package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/hdweiss/coral/internal/config"
	"github.com/hdweiss/coral/internal/k8s"
	"github.com/hdweiss/coral/internal/schema"
	"github.com/hdweiss/coral/internal/theme"
	"github.com/hdweiss/coral/internal/yamltree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"
)

type Options struct {
	Context       string
	Namespace     string // "" = context default, "all" = all namespaces
	AllNamespaces bool
	Refresh       time.Duration // background refresh of the visible list; 0 disables
	Version       string
	PinsPath      string        // where pins are saved; "" keeps them in memory only
	FieldsPath    string        // where favorite and hidden fields are saved; "" keeps them in memory only
	Theme         *theme.Source // colors, reloaded when they change; nil keeps the built-in palette
}

type focusID int

const (
	focusNav focusID = iota
	focusTable
	focusDetail
)

type (
	fetchedMsg struct {
		key   k8s.Key
		entry k8s.Entry
	}
	tickMsg time.Time
	// openInstancesMsg opens the instances of the CRD selected in the CRDs
	// list.
	openInstancesMsg struct{}
)

// button is a clickable region of the header or status bar.
type button struct {
	x0, x1, y int
	run       func() tea.Cmd
}

const (
	doubleClick = 400 * time.Millisecond
	// A cached list younger than this is shown without refetching.
	freshFor = 5 * time.Second
)

var nsGVR = k8s.MustLookup("namespaces").GVR()

type App struct {
	store *k8s.Store
	opts  Options

	w, h   int
	nav    *navView
	table  *tableView
	detail *detailView
	logs   *logView      // shown in place of the table while open
	desc   *describeView // events (E) or describe (d), in place of the table
	focus  focusID

	ctx string // current context
	ns  string // current namespace, "" = all
	cur k8s.Key

	loading map[k8s.Key]bool
	blurred bool // the terminal lost focus; refreshes pause

	navW, detailW int
	drag          int // 0 none, 1 nav|table divider, 2 table|detail divider
	zoom          bool

	palette       *palette
	picker        *picker
	pickTarget    addTarget
	editing       *editState
	schemas       map[string]schema.Source
	schemaLoading map[string]bool // detail schema lookups in flight
	builtin       *schema.Builtin
	filtering     bool
	filterInput   textinput.Model
	help          bool

	flash    string
	flashErr bool
	flashAt  time.Time

	lastClick struct {
		t    time.Time
		x, y int
	}
	buttons []button
}

func New(store *k8s.Store, opts Options) (*App, error) {
	p := store.Provider()
	ctx := opts.Context
	if ctx == "" {
		ctx = p.Current()
	}
	if !slices.Contains(p.Contexts(), ctx) {
		return nil, fmt.Errorf("context %q not found in kubeconfig", ctx)
	}
	ns := opts.Namespace
	if opts.AllNamespaces {
		ns = "all"
	}
	switch ns {
	case "":
		ns = p.DefaultNamespace(ctx)
	case "all":
		ns = ""
	}

	if opts.Theme != nil {
		t, err := opts.Theme.Load()
		if err != nil {
			return nil, fmt.Errorf("loading theme: %w", err)
		}
		applyTheme(t)
	}

	fi := textinput.New()
	fi.Prompt = "/"

	a := &App{
		store:         store,
		opts:          opts,
		nav:           newNavView(store),
		table:         &tableView{},
		detail:        &detailView{},
		focus:         focusTable,
		ctx:           ctx,
		ns:            ns,
		loading:       map[k8s.Key]bool{},
		navW:          30,
		filterInput:   fi,
		schemas:       map[string]schema.Source{},
		schemaLoading: map[string]bool{},
		builtin:       schema.NewBuiltin(),
	}
	contexts := p.Contexts()
	a.nav.known = func(c string) bool { return slices.Contains(contexts, c) }
	if pins, err := config.LoadPins(opts.PinsPath); err != nil {
		a.setFlash("loading pins: "+err.Error(), true)
	} else {
		a.nav.SetPins(pins)
	}
	fields, err := config.LoadFields(opts.FieldsPath)
	if err != nil {
		a.setFlash("loading fields: "+err.Error(), true)
	}
	a.detail.fields = fields
	a.nav.root(ctx).expanded = true
	a.nav.refresh()
	a.setFocus(focusTable)
	return a, nil
}

func (a *App) Init() tea.Cmd {
	pods := k8s.MustLookup("pods")
	cmds := []tea.Cmd{
		a.fetch(k8s.Key{Context: a.ctx, GVR: nsGVR}),
		a.fetchCRDs(a.ctx),
		a.activate(k8s.Key{Context: a.ctx, GVR: pods.GVR(), Namespace: a.ns}, pods),
		tick(),
	}
	// Pins of custom resources show once their context's CRDs are known.
	for _, pin := range a.nav.pins {
		if _, builtin := k8s.Lookup(pin.Resource); pin.Resource != "" && !builtin && a.nav.known(pin.Context) {
			cmds = append(cmds, a.fetchCRDs(pin.Context))
		}
	}
	return tea.Batch(cmds...)
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// fetch lists key in the background unless a fetch is already running.
func (a *App) fetch(key k8s.Key) tea.Cmd {
	store := a.store
	return a.fetchWith(key, func() k8s.Entry { return store.Fetch(key) })
}

func (a *App) fetchWith(key k8s.Key, list func() k8s.Entry) tea.Cmd {
	if a.loading[key] {
		return nil
	}
	a.loading[key] = true
	if key == a.cur {
		a.table.loading = true
	}
	return func() tea.Msg { return fetchedMsg{key: key, entry: list()} }
}

// fetchCRDs lists a context's CRDs, for its custom resources, unless they
// are younger than CRDMaxAge in memory or on disk. They are not refreshed
// on the tick, only by r in the CRDs list.
func (a *App) fetchCRDs(ctx string) tea.Cmd {
	key := k8s.CRDKey(ctx)
	if e, ok := a.store.Get(key); ok && time.Since(e.FetchedAt) <= k8s.CRDMaxAge {
		return nil
	}
	store := a.store
	return a.fetchWith(key, func() k8s.Entry { return store.Cached(key, k8s.CRDMaxAge) })
}

// registry has a context's resources, custom ones included once its CRDs
// are listed.
func (a *App) registry(ctx string) *k8s.Registry { return a.store.Registry(ctx) }

// resourceIn returns res as context ctx has it: builtins everywhere, a
// custom resource only where its CRD is.
func (a *App) resourceIn(ctx string, res k8s.Resource) (k8s.Resource, bool) {
	if res.Name == "" {
		return res, false
	}
	if !res.Custom {
		return res, true
	}
	return a.registry(ctx).Lookup(res.ID())
}

// activate shows a resource list, from cache when possible.
func (a *App) activate(key k8s.Key, res k8s.Resource) tea.Cmd {
	a.closeLogs()
	a.closeDesc()
	a.cur, a.ctx = key, key.Context
	if res.Namespaced {
		a.ns = key.Namespace
	}
	a.table.SetResource(key, res)
	e, ok := a.store.Get(key)
	if ok {
		a.table.SetEntry(e)
	}
	a.table.loading = a.loading[key]
	a.nav.Reveal(key, res)
	a.syncDetail()
	var cmds []tea.Cmd
	// A context folded in the navigator (say, a pin's) needs no namespaces
	// or CRDs until it is expanded.
	if a.nav.expanded(key.Context) {
		if _, ok := a.store.Get(k8s.Key{Context: key.Context, GVR: nsGVR}); !ok {
			cmds = append(cmds, a.fetch(k8s.Key{Context: key.Context, GVR: nsGVR}))
		}
		if key != k8s.CRDKey(key.Context) {
			cmds = append(cmds, a.fetchCRDs(key.Context))
		}
	}
	maxAge := freshFor
	if key == k8s.CRDKey(key.Context) {
		maxAge = k8s.CRDMaxAge
	}
	if !ok || time.Since(e.FetchedAt) > maxAge {
		cmds = append(cmds, a.fetch(key))
	}
	if ek, ok := a.warningsKey(); ok {
		ee, cached := a.store.Get(ek)
		if cached {
			a.setWarnings(ee)
		}
		if !cached || time.Since(ee.FetchedAt) > freshFor {
			cmds = append(cmds, a.fetch(ek))
		}
	}
	return tea.Batch(cmds...)
}

// refreshDue refreshes what is on screen once the refresh interval has
// passed: the list and its warnings, or the events of the describe view
// (what an object relates to rarely changes; r reloads it). Nothing refreshes
// while the terminal is unfocused, nor a list hidden by the log or describe
// view; when they come back, the next tick catches up.
func (a *App) refreshDue() tea.Cmd {
	if a.opts.Refresh <= 0 || a.blurred {
		return nil
	}
	stale := func(k k8s.Key) bool {
		e, ok := a.store.Get(k)
		return ok && time.Since(e.FetchedAt) >= a.opts.Refresh
	}
	switch {
	case a.desc != nil:
		if !a.desc.loading && time.Since(a.desc.at) >= a.opts.Refresh {
			return a.desc.load(a.store, 0, false)
		}
		return nil
	case a.logs != nil:
		return nil
	}
	var cmds []tea.Cmd
	if stale(a.cur) && a.cur != k8s.CRDKey(a.ctx) { // CRDs: only on r
		cmds = append(cmds, a.fetch(a.cur))
	}
	if ek, ok := a.warningsKey(); ok && stale(ek) {
		cmds = append(cmds, a.fetch(ek))
	}
	return tea.Batch(cmds...)
}

// warningsKey is the event list behind the table's warning markers.
func (a *App) warningsKey() (k8s.Key, bool) {
	return k8s.WarningsKey(a.cur, a.table.res)
}

func (a *App) setWarnings(e k8s.Entry) {
	if e.Err == nil {
		a.table.SetWarnings(k8s.IndexWarnings(e.Items, time.Now().Add(-k8s.WarningWindow)))
	}
}

func (a *App) syncDetail() {
	if a.desc != nil {
		obj := a.desc.SelectedObject()
		if obj == nil {
			obj = a.desc.subject
		}
		a.detail.SetObject(obj)
		return
	}
	if a.logs != nil {
		a.detail.SetLog(a.logs.Selected())
		return
	}
	a.detail.SetObject(a.table.Selected())
}

// logPod is the pod whose log L and p open: the table's selected pod, or the
// pod selected or described in the events or describe view.
func (a *App) logPod() *unstructured.Unstructured {
	if a.desc != nil {
		pod := a.desc.SelectedObject()
		if pod == nil || pod.GetKind() != "Pod" {
			pod = a.desc.subject
		}
		if pod.GetKind() == "Pod" {
			return pod
		}
		return nil
	}
	if a.table.res.ID() != "pods" {
		return nil
	}
	return a.table.Selected()
}

// openLogs shows the log of the selected pod in place of the table.
func (a *App) openLogs() tea.Cmd {
	pod := a.logPod()
	if pod == nil {
		a.setFlash("logs: select a pod", true)
		return nil
	}
	if a.filtering {
		a.stopFilter(false)
	}
	a.closeDesc()
	a.logs = newLogView(a.cur.Context, pod, a.detail.fields)
	a.setFocus(focusTable)
	cmd := a.logs.start(a.store.Provider())
	a.syncDetail()
	return cmd
}

// openDescribe shows the events (E) or the description (d) of the selected
// object in place of the table. From such a view it opens the selected
// related object's, on top of the current view; on anything else the same
// key goes back.
func (a *App) openDescribe(eventsOnly bool) tea.Cmd {
	var subject *unstructured.Unstructured
	switch {
	case a.desc != nil:
		r, ok := a.desc.Selected()
		switch {
		case ok && r.kind == descObject && r.rel.Obj != nil && !r.self:
			subject = r.rel.Obj
		case a.desc.eventsOnly == eventsOnly:
			return a.popDesc()
		default:
			subject = a.desc.subject
		}
	case a.logs != nil:
		subject = a.logs.pod
	default:
		subject = a.table.Selected()
	}
	if subject == nil {
		a.setFlash("select an object first", true)
		return nil
	}
	if a.filtering {
		a.stopFilter(false)
	}
	res, ok := a.registry(a.cur.Context).ForKind(subject.GetAPIVersion(), subject.GetKind())
	if !ok {
		res = k8s.Resource{Kind: subject.GetKind()}
	}
	v := newDescribeView(a.cur.Context, subject, res, eventsOnly)
	v.prev = a.desc
	a.closeLogs()
	a.desc = v
	a.setFocus(focusTable)
	cmd := v.load(a.store, freshFor, true)
	a.syncDetail()
	return cmd
}

// popDesc goes back to the view the current one was opened from, or to the
// table.
func (a *App) popDesc() tea.Cmd {
	if a.desc == nil {
		return nil
	}
	if a.filtering {
		a.stopFilter(false)
	}
	a.desc = a.desc.prev
	var cmd tea.Cmd
	if a.desc != nil && time.Since(a.desc.at) > freshFor {
		cmd = a.desc.load(a.store, freshFor, true)
	}
	a.setFocus(focusTable)
	a.syncDetail()
	return cmd
}

func (a *App) closeDesc() {
	if a.desc == nil {
		return
	}
	if a.filtering {
		a.stopFilter(false)
	}
	a.desc = nil
	a.syncDetail()
	a.layout()
}

// jump shows a related object in its list.
func (a *App) jump(rel k8s.Related) tea.Cmd {
	if rel.Obj == nil {
		a.setFlash(rel.Kind+" "+rel.Name+" not found", true)
		return nil
	}
	ns := rel.Obj.GetNamespace()
	if a.ns == "" {
		ns = "" // stay in all namespaces
	}
	cmd := a.activate(a.keyFor(a.ctx, ns, rel.Res), rel.Res)
	a.table.Select(string(rel.Obj.GetUID()))
	a.syncDetail()
	return cmd
}

// openInstances lists the instances of the CRD selected in the CRDs list:
// in the current namespace (or all namespaces) for namespaced ones,
// cluster-wide otherwise.
func (a *App) openInstances() tea.Cmd {
	crd := a.table.Selected()
	if crd == nil || a.table.res.ID() != "customresourcedefinitions" {
		return nil
	}
	parsed := k8s.CustomResources([]unstructured.Unstructured{*crd})
	if len(parsed) == 0 {
		a.setFlash(crd.GetName()+" serves no version", true)
		return nil
	}
	res := parsed[0]
	if r, ok := a.registry(a.ctx).Lookup(res.ID()); ok {
		res = r // the same node in the navigator
	}
	a.setFocus(focusTable)
	return a.activate(a.keyFor(a.ctx, a.ns, res), res)
}

// subview reports whether a log, events or describe view replaces the table.
func (a *App) subview() bool { return a.logs != nil || a.desc != nil }

func (a *App) closeLogs() {
	if a.logs == nil {
		return
	}
	if a.filtering {
		a.stopFilter(false)
	}
	a.logs.stop()
	a.logs = nil
	a.syncDetail()
	a.layout()
}

func (a *App) setFocus(f focusID) {
	a.focus = f
	a.nav.focused = f == focusNav
	a.table.focused = f == focusTable
	if a.logs != nil {
		a.logs.focused = f == focusTable
		if f == focusDetail {
			a.logs.follow = false // keep the inspected entry
		}
	}
	if a.desc != nil {
		a.desc.focused = f == focusTable
	}
	a.detail.focused = f == focusDetail
	a.layout()
}

// cycleFocus moves focus by step through the panels in screen order.
func (a *App) cycleFocus(step int) {
	order := []focusID{focusNav, focusTable, focusDetail}
	i := max(slices.Index(order, a.focus), 0)
	a.setFocus(order[(i+step+len(order))%len(order)])
}

// currentPin is what pinning "here" means: the current namespace, or the
// cluster when looking at all namespaces or a cluster-scoped resource.
func (a *App) currentPin() config.Pin {
	if a.table.res.Namespaced {
		return config.Pin{Context: a.ctx, Namespace: a.ns}
	}
	return config.Pin{Context: a.ctx}
}

func (a *App) togglePin(pin config.Pin) {
	added := a.nav.togglePin(pin)
	label := pinLabel(pin, a.registry(pin.Context))
	if added {
		a.setFlash("pinned "+label, false)
	} else {
		a.setFlash("unpinned "+label, false)
	}
	if err := config.SavePins(a.opts.PinsPath, a.nav.pins); err != nil {
		a.setFlash("saving pins: "+err.Error(), true)
	}
	a.clampScroll()
}

// openPin jumps to a pinned namespace, keeping the resource type when it is
// namespaced.
func (a *App) openPin(pin config.Pin) tea.Cmd {
	res, ok := a.resourceIn(pin.Context, a.table.res)
	if !ok || !res.Namespaced {
		res = k8s.MustLookup("pods")
	}
	return a.activate(a.keyFor(pin.Context, pin.Namespace, res), res)
}

func (a *App) setFlash(msg string, isErr bool) {
	a.flash, a.flashErr, a.flashAt = msg, isErr, time.Now()
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if a.detailW == 0 {
			a.detailW = (msg.Width - a.navW) * 45 / 100
		} else if a.w > a.navW {
			// Keep the detail panel's share of the space right of the navigator.
			a.detailW = a.detailW * (msg.Width - a.navW) / (a.w - a.navW)
		}
		a.w, a.h = msg.Width, msg.Height
		a.layout()
	case fetchedMsg:
		delete(a.loading, msg.key)
		if msg.key.GVR == nsGVR && msg.key.Namespace == "" {
			names := make([]string, 0, len(msg.entry.Items))
			for _, it := range msg.entry.Items {
				names = append(names, it.GetName())
			}
			slices.Sort(names)
			a.nav.SetNamespaces(msg.key.Context, names, msg.entry.Err)
		}
		if msg.key == a.cur {
			a.table.loading = false
			a.table.SetEntry(msg.entry)
			a.syncDetail()
			if msg.entry.Err != nil {
				a.setFlash(msg.entry.Err.Error(), true)
			}
		}
		if msg.key == k8s.CRDKey(msg.key.Context) {
			a.nav.SetCustom(msg.key.Context)
		}
		if k, ok := a.warningsKey(); ok && msg.key == k {
			a.setWarnings(msg.entry)
		}
	case describeMsg:
		msg.view.onLoaded(msg)
		if msg.view == a.desc {
			a.syncDetail()
			if msg.err != nil {
				a.setFlash(msg.err.Error(), true)
			}
		}
	case jumpMsg:
		cmd = a.jump(msg.rel)
	case openInstancesMsg:
		cmd = a.openInstances()
	case openEventsMsg:
		cmd = a.openDescribe(true)
	case activateMsg:
		cmd = a.activate(msg.key, msg.res)
	case logLinesMsg:
		if a.logs != nil {
			cmd = a.logs.onLines(msg)
			a.syncDetail()
		}
	case needNamespacesMsg:
		cmd = tea.Batch(a.fetch(k8s.Key{Context: msg.context, GVR: nsGVR}), a.fetchCRDs(msg.context))
	case openDetailMsg:
		a.setFocus(focusDetail)
	case focusMsg:
		a.setFocus(msg.f)
	case editReadyMsg:
		cmd = a.onEditReady(msg)
	case editorExitMsg:
		cmd = a.onEditorExit(msg)
	case updatedMsg:
		cmd = a.onUpdated(msg)
	case schemaMsg:
		cmd = a.onSchema(msg)
	case needSchemaMsg:
		cmd = a.ensureDetailSchema()
	case detailSchemaMsg:
		delete(a.schemaLoading, msg.key)
		if msg.key == a.detailSchemaKey() {
			a.detail.info.schemaKey, a.detail.info.schema, a.detail.info.err = msg.key, msg.schema, msg.err
		}
	case fieldsChangedMsg:
		if err := config.SaveFields(a.opts.FieldsPath, a.detail.fields); err != nil {
			a.setFlash("saving fields: "+err.Error(), true)
		}
	case togglePinMsg:
		a.togglePin(msg.pin)
	case openPinMsg:
		cmd = a.openPin(msg.pin)
	case tickMsg:
		if t, ok := a.opts.Theme.Changed(); ok {
			applyTheme(t)
		}
		cmd = tea.Batch(tick(), a.refreshDue())
	case tea.FocusMsg:
		a.blurred = false
		cmd = a.refreshDue()
	case tea.BlurMsg:
		a.blurred = true
	case tea.KeyPressMsg:
		cmd = a.handleKey(msg)
	case tea.MouseMsg:
		cmd = a.handleMouse(msg)
	default:
		// Cursor blink and similar internal messages of the text inputs.
		if a.picker != nil {
			cmd, _, _ = a.picker.Update(msg)
		} else if a.palette != nil {
			cmd, _, _ = a.palette.Update(msg)
		} else if a.filtering {
			a.filterInput, cmd = a.filterInput.Update(msg)
		}
	}
	// The info popup follows the selection, which may now be another kind.
	if a.detail.info.on {
		cmd = tea.Batch(cmd, a.ensureDetailSchema())
	}
	return a, cmd
}

// detailSchemaKey identifies the schema the detail view needs.
func (a *App) detailSchemaKey() string {
	if a.detail.obj == nil {
		return ""
	}
	return a.cur.Context + "|" + a.detail.obj.GroupVersionKind().String()
}

// ensureDetailSchema loads the schema for the info popup if it is missing.
func (a *App) ensureDetailSchema() tea.Cmd {
	key := a.detailSchemaKey()
	if key == "" || key == a.detail.info.schemaKey || a.schemaLoading[key] {
		return nil
	}
	a.detail.info.schemaKey, a.detail.info.schema, a.detail.info.err = "", nil, nil
	a.schemaLoading[key] = true
	src, gvk := a.schemaSource(a.cur.Context), a.detail.obj.GroupVersionKind()
	return func() tea.Msg {
		s, err := src.Lookup(gvk)
		return detailSchemaMsg{key: key, schema: s, err: err}
	}
}

func (a *App) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" {
		return tea.Quit
	}

	if a.picker != nil {
		cmd, done, res := a.picker.Update(msg)
		if done {
			a.picker = nil
			if res != nil {
				return a.onPicked(res)
			}
		}
		return cmd
	}

	if a.palette != nil {
		cmd, done, run := a.palette.Update(msg)
		if done {
			a.palette = nil
			if run != "" {
				return a.runCommand(run)
			}
		}
		return cmd
	}

	if a.filtering {
		switch key {
		case "esc":
			a.stopFilter(true)
			return nil
		case "enter":
			a.stopFilter(false)
			return nil
		}
		var cmd tea.Cmd
		a.filterInput, cmd = a.filterInput.Update(msg)
		a.setFilter(a.filterInput.Value())
		return cmd
	}

	if a.help {
		a.help = false
		return nil
	}

	switch key {
	case "q":
		return tea.Quit
	case ":":
		return a.openPalette("")
	case "/":
		return a.startFilter()
	case "?":
		a.help = true
		return nil
	case "tab":
		a.cycleFocus(1)
		return nil
	case "shift+tab":
		a.cycleFocus(-1)
		return nil
	case "0":
		return a.allNamespaces()
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		return a.openPinNumber(int(key[0] - '0'))
	case "ctrl+p":
		if a.focus == focusTable && !a.subview() {
			a.togglePin(a.currentPin())
			return nil
		}
	case "p":
		if a.focus != focusNav {
			return a.previousLogs()
		}
	case "y":
		if a.focus == focusTable {
			a.setFocus(focusDetail)
			return nil
		}
	case "L":
		if a.logs != nil {
			a.closeLogs()
			return nil
		}
		return a.openLogs()
	case "E":
		if a.focus != focusNav {
			return a.openDescribe(true)
		}
	case "d":
		if a.focus != focusNav {
			return a.openDescribe(false)
		}
	case "e":
		if a.logs != nil {
			return nil // log entries are not objects
		}
		switch a.focus {
		case focusTable:
			return a.startEdit(nil)
		case focusDetail:
			var focus []yamltree.Seg
			if n := a.detail.current(); n != nil {
				focus = n.Segments()
			}
			return a.startEdit(focus)
		}
	case "ctrl+n":
		if a.logs == nil && (a.focus == focusTable || a.focus == focusDetail) {
			return a.startAdd()
		}
	case "r", "ctrl+r":
		if a.logs != nil {
			return a.logs.start(a.store.Provider())
		}
		if a.desc != nil {
			a.setFlash("refreshing…", false)
			return a.desc.load(a.store, 0, true)
		}
		a.setFlash("refreshing…", false)
		cmds := []tea.Cmd{a.fetch(a.cur)}
		if ek, ok := a.warningsKey(); ok {
			cmds = append(cmds, a.fetch(ek))
		}
		if a.nav.expanded(a.ctx) {
			cmds = append(cmds, a.fetch(k8s.Key{Context: a.ctx, GVR: nsGVR}))
		}
		return tea.Batch(cmds...)
	case "z":
		a.zoom = !a.zoom
		a.layout()
		return nil
	case "o":
		if a.focus == focusTable && !a.subview() && a.table.res.ID() == "customresourcedefinitions" {
			return a.openInstances()
		}
	case "esc":
		switch {
		case a.focus == focusDetail && a.detail.info.on:
			a.detail.info.on = false
		case a.zoom:
			a.zoom = false
			a.layout()
		case a.logs != nil && a.logs.filter != "":
			a.logs.SetFilter("")
			a.syncDetail()
		case a.desc != nil && a.desc.filter != "":
			a.desc.SetFilter("")
			a.syncDetail()
		case !a.subview() && a.table.filter != "":
			a.table.SetFilter("")
			a.syncDetail()
		case a.focus == focusDetail:
			a.setFocus(focusTable)
		case a.logs != nil:
			a.closeLogs()
		case a.desc != nil:
			return a.popDesc()
		case a.focus == focusTable:
			a.setFocus(focusNav)
		}
		return nil
	}

	var cmd tea.Cmd
	switch a.focus {
	case focusNav:
		cmd = a.nav.Update(msg)
	case focusTable:
		if a.desc != nil && key == "enter" {
			return a.desc.activate()
		}
		if key == "enter" || key == "right" || key == "l" {
			a.setFocus(focusDetail)
			return nil
		}
		if key == "left" || key == "h" {
			a.setFocus(focusNav)
			return nil
		}
		if a.logs != nil {
			return a.logKey(msg)
		}
		if a.desc != nil {
			cmd = a.desc.Update(msg)
			a.syncDetail()
			return cmd
		}
		cmd = a.table.Update(msg)
		a.syncDetail()
	case focusDetail:
		cmd = a.detail.Update(msg)
	}
	return cmd
}

// previousLogs shows the log of the previous container: from the log view it
// toggles, from a pod it opens the log that way.
func (a *App) previousLogs() tea.Cmd {
	if a.logs != nil {
		a.logs.previous = !a.logs.previous
		return a.logs.start(a.store.Provider())
	}
	if a.logPod() == nil {
		return nil
	}
	cmd := a.openLogs()
	if a.logs == nil {
		return cmd
	}
	a.logs.previous = true
	return a.logs.start(a.store.Provider())
}

// allNamespaces shows the current resource across all namespaces, or pods
// when the resource is cluster-scoped (k9s's 0).
func (a *App) allNamespaces() tea.Cmd {
	res := a.table.res
	if !res.Namespaced {
		res = k8s.MustLookup("pods")
	}
	return a.activate(a.keyFor(a.ctx, "", res), res)
}

// openPinNumber opens the n-th pin (1-based), in the order pins were added.
func (a *App) openPinNumber(n int) tea.Cmd {
	if n > len(a.nav.pins) {
		a.setFlash(fmt.Sprintf("no pin %d", n), true)
		return nil
	}
	pin := a.nav.pins[n-1]
	p := a.store.Provider()
	if !slices.Contains(p.Contexts(), pin.Context) {
		a.setFlash("context "+pin.Context+" is not in the kubeconfig", true)
		return nil
	}
	switch {
	case pin.Resource != "":
		reg := a.registry(pin.Context)
		res, ok := reg.Lookup(pin.Resource)
		switch {
		case !ok && reg == nil:
			a.setFlash("loading the custom resources of "+pin.Context+"…", false)
			return a.fetchCRDs(pin.Context)
		case !ok:
			a.setFlash("unknown resource "+pin.Resource, true)
			return nil
		}
		return a.activate(a.keyFor(pin.Context, pin.Namespace, res), res)
	case pin.Namespace != "":
		return a.openPin(pin)
	}
	res, ok := a.resourceIn(pin.Context, a.table.res)
	if !ok {
		res = k8s.MustLookup("pods")
	}
	return a.activate(a.keyFor(pin.Context, p.DefaultNamespace(pin.Context), res), res)
}

// logKey handles a key in the log view.
func (a *App) logKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "c":
		if !a.logs.nextContainer() {
			a.setFlash("the pod has one container", false)
			return nil
		}
		return a.logs.start(a.store.Provider())
	case "s": // autoscroll, as in k9s
		a.logs.follow = !a.logs.follow
		if a.logs.follow {
			a.logs.cursor = max(len(a.logs.rows)-1, 0)
			a.logs.scroll()
			a.syncDetail()
		}
		return nil
	case "f": // fullscreen, as in k9s
		a.zoom = !a.zoom
		a.layout()
		return nil
	}
	cmd := a.logs.Update(msg)
	a.syncDetail()
	return cmd
}

// setFilter filters the log view while it is open, else the table.
func (a *App) setFilter(f string) {
	if a.desc != nil {
		a.desc.SetFilter(f)
	} else if a.logs != nil {
		a.logs.SetFilter(f)
	} else {
		a.table.SetFilter(f)
	}
	a.syncDetail()
}

func (a *App) startFilter() tea.Cmd {
	a.filtering = true
	if a.desc != nil {
		a.filterInput.SetValue(a.desc.filter)
	} else if a.logs != nil {
		a.filterInput.SetValue(a.logs.filter)
	} else {
		a.filterInput.SetValue(a.table.filter)
	}
	a.filterInput.CursorEnd()
	if a.focus == focusNav {
		a.setFocus(focusTable)
	}
	return a.filterInput.Focus()
}

func (a *App) stopFilter(clear bool) {
	a.filtering = false
	a.filterInput.Blur()
	if clear {
		a.setFilter("")
	}
}

func (a *App) handleMouse(msg tea.MouseMsg) tea.Cmd {
	m := msg.Mouse()
	switch msg.(type) {
	case tea.MouseClickMsg:
		if m.Button != tea.MouseLeft {
			return nil
		}
		double := time.Since(a.lastClick.t) < doubleClick && a.lastClick.x == m.X && a.lastClick.y == m.Y
		a.lastClick.t, a.lastClick.x, a.lastClick.y = time.Now(), m.X, m.Y
		if double {
			a.lastClick.t = time.Time{} // a third click starts over
		}

		if a.picker != nil {
			done, res := a.picker.Click(m.X, m.Y)
			if done {
				a.picker = nil
				if res != nil {
					return a.onPicked(res)
				}
			}
			return nil
		}
		if a.palette != nil {
			done, run := a.palette.Click(m.X, m.Y)
			if done {
				a.palette = nil
				if run != "" {
					return a.runCommand(run)
				}
			}
			return nil
		}
		if a.help {
			a.help = false
			return nil
		}
		for _, b := range a.buttons {
			if m.Y == b.y && m.X >= b.x0 && m.X < b.x1 {
				return b.run()
			}
		}
		if d := a.dividerAt(m.X, m.Y); d != 0 {
			a.drag = d
			return nil
		}
		for _, p := range a.panels() {
			if !p.rect.contains(m.X, m.Y) {
				continue
			}
			if a.filtering {
				a.stopFilter(false)
			}
			a.setFocus(p.id)
			in := p.rect.inner()
			if !in.contains(m.X, m.Y) {
				return nil
			}
			cmd := p.update(clickMsg{x: m.X - in.x, y: m.Y - in.y, double: double})
			if p.id == focusTable {
				a.syncDetail()
			}
			return cmd
		}
	case tea.MouseMotionMsg:
		switch a.drag {
		case 1:
			a.navW = m.X + 1
			a.layout()
		case 2:
			a.detailW = a.w - m.X
			a.layout()
		}
	case tea.MouseReleaseMsg:
		a.drag = 0
	case tea.MouseWheelMsg:
		delta := 0
		switch m.Button {
		case tea.MouseWheelUp:
			delta = -3
		case tea.MouseWheelDown:
			delta = 3
		}
		if a.picker != nil {
			a.picker.Wheel(delta / 3)
			return nil
		}
		if a.palette != nil {
			a.palette.Wheel(delta / 3)
			return nil
		}
		for _, p := range a.panels() {
			if p.rect.contains(m.X, m.Y) {
				cmd := p.update(wheelMsg{delta: delta})
				if p.id == focusTable {
					a.syncDetail()
				}
				return cmd
			}
		}
	}
	return nil
}

type panelRef struct {
	id     focusID
	rect   rect
	update func(tea.Msg) tea.Cmd
}

// mainUpdate sends a panel message to the log view when it is open, else
// to the table.
func (a *App) mainUpdate(msg tea.Msg) tea.Cmd {
	if a.desc != nil {
		return a.desc.Update(msg)
	}
	if a.logs != nil {
		return a.logs.Update(msg)
	}
	return a.table.Update(msg)
}

func (a *App) panels() []panelRef {
	return []panelRef{
		{focusNav, a.nav.rect, a.nav.Update},
		{focusTable, a.table.rect, a.mainUpdate},
		{focusDetail, a.detail.rect, a.detail.Update},
	}
}

// dividerAt reports which panel divider (the two adjacent border columns)
// is under (x, y).
func (a *App) dividerAt(x, y int) int {
	t := a.table.rect
	if t.w == 0 || y < t.y || y >= t.y+t.h {
		return 0
	}
	if a.nav.rect.w > 0 && (x == t.x-1 || x == t.x) {
		return 1
	}
	if a.detail.rect.w > 0 && (x == t.x+t.w-1 || x == t.x+t.w) {
		return 2
	}
	return 0
}

// Below this width the detail panel replaces the table instead of sitting
// next to it.
const narrowWidth = 100

func (a *App) layout() {
	if a.w == 0 {
		return
	}
	bodyY, bodyH := 1, max(a.h-2, 3)
	full := rect{0, bodyY, a.w, bodyH}
	a.nav.rect, a.table.rect, a.detail.rect = rect{}, rect{}, rect{}

	narrow := a.w < narrowWidth
	if a.zoom || (narrow && a.focus == focusDetail) {
		switch a.focus {
		case focusNav:
			a.nav.rect = full
		case focusTable:
			a.table.rect = full
		case focusDetail:
			a.detail.rect = full
		}
		a.clampScroll()
		return
	}

	a.navW = clamp(a.navW, 16, a.w/2)
	if narrow {
		a.nav.rect = rect{0, bodyY, a.navW, bodyH}
		a.table.rect = rect{a.navW, bodyY, a.w - a.navW, bodyH}
		a.clampScroll()
		return
	}
	a.detailW = clamp(a.detailW, 24, a.w-a.navW-30)
	tableW := a.w - a.navW - a.detailW
	a.nav.rect = rect{0, bodyY, a.navW, bodyH}
	a.table.rect = rect{a.navW, bodyY, tableW, bodyH}
	a.detail.rect = rect{a.navW + tableW, bodyY, a.detailW, bodyH}
	a.clampScroll()
}

func (a *App) clampScroll() {
	if a.desc != nil {
		a.desc.rect = a.table.rect
		a.desc.scroll()
	}
	if a.logs != nil {
		a.logs.rect = a.table.rect
		a.logs.offset = scrollTo(a.logs.cursor, a.logs.offset, a.logs.height())
	}
	a.nav.offset = scrollTo(a.nav.cursor, a.nav.offset, a.nav.height())
	a.table.offset = scrollTo(a.table.cursor, a.table.offset, a.table.height())
	a.detail.offset = scrollTo(a.detail.cursor, a.detail.offset, a.detail.height())
}

// --- commands ---

func (a *App) openPalette(initial string) tea.Cmd {
	var items []paletteItem
	for _, r := range k8s.Builtins {
		items = append(items, paletteItem{cmd: r.Name, desc: r.Title + " · " + strings.Join(r.Aliases, ", "), aliases: r.Aliases})
	}
	items = append(items, customPaletteItems(a.registry(a.ctx))...)
	items = append(items, paletteItem{cmd: "ns all", desc: "all namespaces"})
	if e, ok := a.store.Get(k8s.Key{Context: a.ctx, GVR: nsGVR}); ok {
		for _, it := range e.Items {
			items = append(items, paletteItem{cmd: "ns " + it.GetName(), desc: "switch namespace"})
		}
	}
	for _, c := range a.store.Provider().Contexts() {
		items = append(items, paletteItem{cmd: "ctx " + c, desc: "switch context"})
	}
	pinDesc := "pin this namespace"
	if pin := a.currentPin(); a.nav.hasPin(pin) {
		pinDesc = "unpin this namespace"
	}
	if !a.table.res.Namespaced {
		pinDesc = strings.Replace(pinDesc, "namespace", "cluster", 1)
	}
	items = append(items, paletteItem{cmd: "pin", desc: pinDesc, aliases: []string{"unpin"}})
	if a.table.res.ID() == "pods" {
		items = append(items, paletteItem{cmd: "logs", desc: "show the selected pod's log", aliases: []string{"log"}})
	}
	items = append(items, paletteItem{cmd: "quit", desc: "exit coral", aliases: []string{"q"}})
	a.palette = newPalette(items, initial)
	return a.palette.input.Focus()
}

func (a *App) runCommand(text string) tea.Cmd {
	f := strings.Fields(text)
	if len(f) == 0 {
		return nil
	}
	res := a.table.res
	switch f[0] {
	case "q", "quit", "q!":
		return tea.Quit
	case "pin", "unpin":
		a.togglePin(a.currentPin())
		return nil
	case "logs", "log":
		return a.openLogs()
	case "ctx", "context":
		if len(f) < 2 {
			return a.openPalette("ctx ")
		}
		if !slices.Contains(a.store.Provider().Contexts(), f[1]) {
			a.setFlash("unknown context "+f[1], true)
			return nil
		}
		a.nav.root(f[1]).expanded = true
		a.ns = a.store.Provider().DefaultNamespace(f[1])
		if r, ok := a.resourceIn(f[1], res); ok {
			res = r
		} else {
			res = k8s.MustLookup("pods")
		}
		return a.activate(a.keyFor(f[1], a.ns, res), res)
	case "ns", "namespace":
		if len(f) < 2 {
			break // bare "ns" lists namespaces
		}
		ns := f[1]
		if ns == "all" || ns == "-A" {
			ns = ""
		}
		if !res.Namespaced {
			res = k8s.MustLookup("pods")
		}
		return a.activate(a.keyFor(a.ctx, ns, res), res)
	}
	r, ok := a.registry(a.ctx).Lookup(f[0])
	if !ok {
		a.setFlash("unknown command: "+text, true)
		return nil
	}
	ns := a.ns
	if len(f) > 1 {
		ns = f[1]
		if ns == "all" || ns == "-A" {
			ns = ""
		}
	}
	a.setFocus(focusTable)
	return a.activate(a.keyFor(a.ctx, ns, r), r)
}

func (a *App) keyFor(ctx, ns string, r k8s.Resource) k8s.Key {
	if !r.Namespaced {
		ns = ""
	}
	return k8s.Key{Context: ctx, GVR: r.GVR(), Namespace: ns}
}

// --- rendering ---

func (a *App) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true
	v.WindowTitle = "coral"
	if a.w == 0 {
		return v
	}
	a.buttons = a.buttons[:0]

	var parts []string
	if a.nav.rect.w > 0 {
		parts = append(parts, a.nav.View())
	}
	switch {
	case a.table.rect.w > 0 && a.desc != nil:
		parts = append(parts, a.desc.View())
	case a.table.rect.w > 0 && a.logs != nil:
		parts = append(parts, a.logs.View())
	case a.table.rect.w > 0:
		parts = append(parts, a.table.View())
	}
	if a.detail.rect.w > 0 {
		parts = append(parts, a.detail.View())
	}
	content := a.renderHeader() + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, parts...) + "\n" + a.renderStatus()

	if a.detail.info.on && a.focus == focusDetail && a.detail.rect.w > 0 && a.detail.obj != nil {
		content = a.overlayInfo(content)
	}
	switch {
	case a.picker != nil:
		box := a.picker.View(a.w, a.h)
		content = overlay(content, box, a.picker.rect.x, a.picker.rect.y)
	case a.palette != nil:
		box := a.palette.View(a.w)
		content = overlay(content, box, a.palette.rect.x, a.palette.rect.y)
	case a.help:
		box := helpView(a.w, a.h)
		content = overlay(content, box, (a.w-lipgloss.Width(box))/2, max((a.h-lipgloss.Height(box))/2, 0))
	}
	v.SetContent(content)
	return v
}

// overlayInfo draws the info popup like a tooltip: below the selected line
// of the details, or above it when there is no room below.
func (a *App) overlayInfo(content string) string {
	r := a.detail.rect
	w := max(min(r.w-4, 72), min(40, a.w-2))
	x := min(r.x+2, a.w-w)
	row := r.y + 1 + a.detail.cursor - a.detail.offset
	below := r.y + r.h - 1 - (row + 1)
	above := row - r.y - 1
	box, links := a.detail.infoView(w, max(below, above, 5))
	if box == "" {
		return content
	}
	h := lipgloss.Height(box)
	y := row + 1
	if h > below && above > below {
		y = row - h
	}
	y = max(y, 1)
	for _, l := range links {
		url := l.url
		a.buttons = append(a.buttons, button{x0: x + 1, x1: x + w - 1, y: y + l.row, run: func() tea.Cmd {
			if err := openURL(url); err != nil {
				a.setFlash("opening link: "+err.Error(), true)
			} else {
				a.setFlash("opened "+url, false)
			}
			return nil
		}})
	}
	// Other clicks on the popup should not reach the panel underneath.
	for i := range h {
		a.buttons = append(a.buttons, button{x0: x, x1: x + w, y: y + i, run: func() tea.Cmd { return nil }})
	}
	return overlay(content, box, x, y)
}

func overlay(base, top string, x, y int) string {
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(top).X(x).Y(y).Z(1),
	).Render()
}

func (a *App) renderHeader() string {
	var sb strings.Builder
	x := 0
	seg := func(s string, run func() tea.Cmd) {
		if run != nil {
			a.buttons = append(a.buttons, button{x0: x, x1: x + ansi.StringWidth(s), y: 0, run: run})
		}
		sb.WriteString(s)
		x += ansi.StringWidth(s)
	}
	sep := stBarText.Render(" › ")

	seg(stLogo.Render(" coral "), nil)
	seg(stBar.Render(" "), nil)
	seg(stBar.Bold(true).Render("⎈ "+a.ctx), func() tea.Cmd { return a.openPalette("ctx ") })
	if a.table.res.Namespaced {
		ns := a.ns
		if ns == "" {
			ns = "all namespaces"
		}
		seg(sep, nil)
		seg(stBar.Render(ns), func() tea.Cmd { return a.openPalette("ns ") })
	}
	star := stBarText.Render(" ☆")
	if a.nav.hasPin(a.currentPin()) {
		star = stBarKey.Render(" ★")
	}
	seg(star, func() tea.Cmd { a.togglePin(a.currentPin()); return nil })
	seg(sep, nil)
	seg(stBar.Foreground(colAccent).Bold(true).Render(a.table.res.Title), func() tea.Cmd { return a.openPalette("") })
	if a.desc != nil {
		seg(sep, nil)
		seg(stBar.Render(a.desc.name()), nil)
		seg(sep, nil)
		seg(stBar.Foreground(colAccent).Bold(true).Render(a.desc.label()), func() tea.Cmd { return a.popDesc() })
	} else if a.logs != nil {
		seg(sep, nil)
		seg(stBar.Render(a.logs.pod.GetName()), nil)
		seg(sep, nil)
		seg(stBar.Foreground(colAccent).Bold(true).Render("Logs"), func() tea.Cmd { a.closeLogs(); return nil })
	} else if o := a.table.Selected(); o != nil {
		seg(sep, nil)
		seg(stBar.Render(o.GetName()), nil)
	}

	right := ""
	switch {
	case a.subview():
	case a.table.loading:
		right = "⟳ loading "
	default:
		if e, ok := a.store.Get(a.cur); ok {
			right = "updated " + duration.HumanDuration(time.Since(e.FetchedAt)) + " ago "
		}
	}
	right = stBarText.Render(right)
	help := stBarKey.Render(" ? ") + stBarText.Render("help ")
	pad := a.w - x - ansi.StringWidth(right) - ansi.StringWidth(help)
	if pad > 0 {
		sb.WriteString(stBar.Render(strings.Repeat(" ", pad)))
	}
	x = a.w - ansi.StringWidth(help)
	a.buttons = append(a.buttons, button{x0: x, x1: a.w, y: 0, run: func() tea.Cmd { a.help = true; return nil }})
	return ansi.Truncate(sb.String()+right+help, a.w, "")
}

func (a *App) renderStatus() string {
	y := a.h - 1
	if a.filtering {
		a.filterInput.SetWidth(a.w - 4)
		// Styled here rather than once so that it follows theme changes.
		st := inputStyles(stBarKey)
		st.Focused.Text = stBar
		a.filterInput.SetStyles(st)
		return stBar.Render(fit(a.filterInput.View(), a.w))
	}

	type hint struct {
		key, label string
		run        func() tea.Cmd
	}
	hints := []hint{
		{":", "command", func() tea.Cmd { return a.openPalette("") }},
		{"/", "filter", a.startFilter},
		{"r", "refresh", func() tea.Cmd { return a.handleKey(tea.KeyPressMsg{Code: 'r', Text: "r"}) }},
		{"tab", "focus", func() tea.Cmd { a.cycleFocus(1); return nil }},
		{"z", "zoom", func() tea.Cmd { a.zoom = !a.zoom; a.layout(); return nil }},
	}
	press := func(update func(tea.Msg) tea.Cmd, k tea.KeyPressMsg) func() tea.Cmd {
		return func() tea.Cmd { return update(k) }
	}
	switch a.focus {
	case focusNav:
		hints = append(hints, hint{"^p", "pin", press(a.nav.Update, ctrlKey('p'))})
	case focusTable:
		if a.logs != nil {
			logKey := func(m tea.Msg) tea.Cmd { return a.logKey(m.(tea.KeyPressMsg)) }
			hints = append(hints, hint{"esc", "back", func() tea.Cmd { a.closeLogs(); return nil }},
				hint{"s", "autoscroll", press(logKey, plainKey('s'))}, hint{"p", "previous", a.previousLogs})
			if len(a.logs.containers) > 1 {
				hints = append(hints, hint{"c", "container", press(logKey, plainKey('c'))})
			}
			break
		}
		describe := hint{"d", "describe", func() tea.Cmd { return a.openDescribe(false) }}
		events := hint{"E", "events", func() tea.Cmd { return a.openDescribe(true) }}
		if a.desc != nil {
			hints = append(hints, hint{"esc", "back", a.popDesc})
			switch r, ok := a.desc.Selected(); {
			case ok && r.kind == descObject && !r.self:
				hints = append(hints, hint{"enter", "go to", a.desc.activate}, describe, events)
			case a.desc.eventsOnly:
				hints = append(hints, describe)
			default:
				hints = append(hints, events)
			}
			if a.logPod() != nil {
				hints = append(hints, hint{"L", "logs", a.openLogs})
			}
			hints = append(hints, hint{"e", "edit", func() tea.Cmd { return a.startEdit(nil) }})
			break
		}
		if a.table.res.ID() == "pods" {
			hints = append(hints, hint{"L", "logs", a.openLogs})
		}
		if a.table.res.ID() == "customresourcedefinitions" {
			hints = append(hints, hint{"o", "instances", a.openInstances})
		}
		hints = append(hints, describe, events,
			hint{"e", "edit", func() tea.Cmd { return a.startEdit(nil) }},
			hint{"^n", "add", a.startAdd},
			hint{"s", "sort", press(a.table.Update, plainKey('s'))})
	case focusDetail:
		if a.logs != nil {
			hints = append(hints, hint{"^p", "pin to lines", press(a.detail.Update, ctrlKey('p'))},
				hint{"^x", "hide", press(a.detail.Update, ctrlKey('x'))},
				hint{"O", "expand all", press(a.detail.Update, plainKey('O'))},
				hint{"C", "collapse all", press(a.detail.Update, plainKey('C'))})
			break
		}
		hints = append(hints,
			hint{"e", "edit", func() tea.Cmd { return a.handleKey(plainKey('e')) }},
			hint{"^n", "add", a.startAdd},
			hint{"i", "info", press(a.detail.Update, plainKey('i'))},
			hint{"^p", "favorite", press(a.detail.Update, ctrlKey('p'))},
			hint{"^x", "hide", press(a.detail.Update, ctrlKey('x'))},
			hint{"O", "expand all", press(a.detail.Update, plainKey('O'))},
			hint{"C", "collapse all", press(a.detail.Update, plainKey('C'))})
	}
	hints = append(hints, hint{"q", "quit", func() tea.Cmd { return tea.Quit }})

	var sb strings.Builder
	x := 0
	for _, h := range hints {
		s := stBarKey.Render(" "+h.key) + stBarText.Render(" "+h.label+" ")
		w := ansi.StringWidth(s)
		a.buttons = append(a.buttons, button{x0: x, x1: x + w, y: y, run: h.run})
		sb.WriteString(s)
		x += w
	}

	right := ""
	if a.flash != "" && time.Since(a.flashAt) < 5*time.Second {
		st := stBarText
		if a.flashErr {
			st = stBar.Foreground(colRed)
		}
		right = st.Render(" " + a.flash + " ")
	}
	left := sb.String()
	pad := a.w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if pad < 0 {
		return ansi.Truncate(left+right, a.w, "")
	}
	return left + stBar.Render(strings.Repeat(" ", pad)) + right
}

// helpView renders the key help, in two columns when one column would be
// taller than h and the screen (w) is wide enough.
func helpView(w, h int) string {
	sections := []helpSection{
		{"Global", [][2]string{
			{":", "command palette (po, deploy, ev, ns <name>, ctx <name>, pin, logs)"},
			{"/", "filter the table or log"},
			{"0", "all namespaces"},
			{"1-9", "open pin 1-9 (numbered in the navigator)"},
			{"tab h l", "move between panels"},
			{"ctrl+p", "pin / unpin: navigator node, or here (namespace or cluster) from the table"},
			{"z", "zoom the focused panel"},
			{"r ctrl+r", "refresh"},
			{"esc", "back / clear filter / unzoom"},
			{"q", "quit"},
		}},
		{"Lists and trees", [][2]string{
			{"↑↓ j k", "move"},
			{"pgup pgdn g G", "page / top / bottom (also ctrl+b ctrl+f)"},
			{"← → h l", "collapse / expand, or move between panels"},
			{"enter space", "open / toggle; on a resource in the navigator, open its list (also l or a click)"},
			{"y", "table: go to the YAML details"},
			{"s S", "table: next sort column / reverse"},
			{"o", "CRDs list: open the selected CRD's instances (also double-click)"},
			{"e", "edit the object in $EDITOR (at the selected field)"},
			{"ctrl+n", "add a field below the selected one, from the API schema"},
			{"ctrl+p ctrl+x", "details: favorite (to the top) / hide (to the bottom)"},
			{"O C", "details: expand all / collapse all"},
			{"i", "details: toggle help for the selected field"},
		}},
		{"Logs", [][2]string{
			{"L", "show the selected pod's log (again or esc to close)"},
			{"p", "previous container's log (on a pod, or toggle in the log)"},
			{"enter", "show the selected line as a tree (JSON / ECS fields)"},
			{"ctrl+p", "in the entry: pin the field to every log line"},
			{"s G", "toggle autoscroll / jump to the end and follow"},
			{"f", "fullscreen"},
			{"c", "next container"},
		}},
		{"Events and describe", [][2]string{
			{"E", "events of the selected object, newest first (again or esc to close)"},
			{"d", "describe: owners, pods, services, uses / used by, events"},
			{"enter d E", "on a related object: go to its list / describe it / its events"},
			{"⚠N", "table: N warning events in the last hour (click it for the events)"},
		}},
		{"Mouse", [][2]string{
			{"click", "select, focus a panel, toggle ▸ ▾"},
			{"double-click", "open row in details / toggle node"},
			{"click header", "sort by column (again to reverse)"},
			{"drag border", "resize panels"},
			{"wheel", "scroll the panel under the pointer"},
			{"click breadcrumb", "switch context / namespace / resource"},
			{"click ☆ / ×", "pin where you are / unpin"},
		}},
	}
	render := func(secs []helpSection) []string {
		var lines []string
		for i, s := range secs {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, " "+stAccent.Bold(true).Render(s.title))
			for _, k := range s.keys {
				lines = append(lines, "  "+stKey.Render(fit(k[0], 18))+" "+k[1])
			}
		}
		return lines
	}
	const colW = 80
	lines := render(sections)
	if len(lines)+2 <= h || w < 2*colW+2 {
		return frame("Help", "any key to close", lines, 76, len(lines)+2, true)
	}
	// Two columns when one does not fit the screen.
	left, right := render(sections[:2]), render(sections[2:])
	lines = lines[:0]
	for i := range max(len(left), len(right)) {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		lines = append(lines, fit(l, colW)+r)
	}
	return frame("Help", "any key to close", lines, 2*colW+2, len(lines)+2, true)
}

type helpSection struct {
	title string
	keys  [][2]string
}
