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
	"github.com/hdweiss/coralctl/internal/k8s"
	"k8s.io/apimachinery/pkg/util/duration"
)

type Options struct {
	Context       string
	Namespace     string // "" = context default, "all" = all namespaces
	AllNamespaces bool
	Refresh       time.Duration // background refresh of the visible list; 0 disables
	Version       string
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
	focus  focusID

	ctx string // current context
	ns  string // current namespace, "" = all
	cur k8s.Key

	loading map[k8s.Key]bool

	navW, detailW int
	drag          int // 0 none, 1 nav|table divider, 2 table|detail divider
	zoom          bool

	palette     *palette
	filtering   bool
	filterInput textinput.Model
	help        bool

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

	fi := textinput.New()
	fi.Prompt = "/"
	st := textinput.DefaultDarkStyles()
	st.Focused.Prompt = stBarKey
	st.Focused.Text = stBar
	fi.SetStyles(st)

	a := &App{
		store:       store,
		opts:        opts,
		nav:         newNavView(store),
		table:       &tableView{},
		detail:      &detailView{},
		focus:       focusTable,
		ctx:         ctx,
		ns:          ns,
		loading:     map[k8s.Key]bool{},
		navW:        30,
		filterInput: fi,
	}
	a.nav.root(ctx).expanded = true
	a.nav.refresh()
	a.setFocus(focusTable)
	return a, nil
}

func (a *App) Init() tea.Cmd {
	pods := k8s.MustLookup("pods")
	return tea.Batch(
		a.fetch(k8s.Key{Context: a.ctx, GVR: nsGVR}),
		a.activate(k8s.Key{Context: a.ctx, GVR: pods.GVR(), Namespace: a.ns}, pods),
		tick(),
	)
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// fetch lists key in the background unless a fetch is already running.
func (a *App) fetch(key k8s.Key) tea.Cmd {
	if a.loading[key] {
		return nil
	}
	a.loading[key] = true
	if key == a.cur {
		a.table.loading = true
	}
	store := a.store
	return func() tea.Msg { return fetchedMsg{key: key, entry: store.Fetch(key)} }
}

// activate shows a resource list, from cache when possible.
func (a *App) activate(key k8s.Key, res k8s.Resource) tea.Cmd {
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
	if _, ok := a.store.Get(k8s.Key{Context: key.Context, GVR: nsGVR}); !ok {
		cmds = append(cmds, a.fetch(k8s.Key{Context: key.Context, GVR: nsGVR}))
	}
	if !ok || time.Since(e.FetchedAt) > freshFor {
		cmds = append(cmds, a.fetch(key))
	}
	return tea.Batch(cmds...)
}

func (a *App) syncDetail() { a.detail.SetObject(a.table.Selected()) }

func (a *App) setFocus(f focusID) {
	a.focus = f
	a.nav.focused = f == focusNav
	a.table.focused = f == focusTable
	a.detail.focused = f == focusDetail
	a.layout()
}

func (a *App) setFlash(msg string, isErr bool) {
	a.flash, a.flashErr, a.flashAt = msg, isErr, time.Now()
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		if a.detailW == 0 {
			a.detailW = (a.w - a.navW) * 45 / 100
		}
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
	case activateMsg:
		cmd = a.activate(msg.key, msg.res)
	case needNamespacesMsg:
		cmd = a.fetch(k8s.Key{Context: msg.context, GVR: nsGVR})
	case openDetailMsg:
		a.setFocus(focusDetail)
	case tickMsg:
		cmds := []tea.Cmd{tick()}
		if e, ok := a.store.Get(a.cur); ok && a.opts.Refresh > 0 && time.Since(e.FetchedAt) >= a.opts.Refresh {
			cmds = append(cmds, a.fetch(a.cur))
		}
		cmd = tea.Batch(cmds...)
	case tea.KeyPressMsg:
		cmd = a.handleKey(msg)
	case tea.MouseMsg:
		cmd = a.handleMouse(msg)
	default:
		// Cursor blink and similar internal messages of the text inputs.
		if a.palette != nil {
			cmd, _, _ = a.palette.Update(msg)
		} else if a.filtering {
			a.filterInput, cmd = a.filterInput.Update(msg)
		}
	}
	return a, cmd
}

func (a *App) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" {
		return tea.Quit
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
		a.table.SetFilter(a.filterInput.Value())
		a.syncDetail()
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
		a.setFocus((a.focus + 1) % 3)
		return nil
	case "shift+tab":
		a.setFocus((a.focus + 2) % 3)
		return nil
	case "1", "2", "3":
		a.setFocus(focusID(key[0] - '1'))
		return nil
	case "r":
		a.setFlash("refreshing…", false)
		return tea.Batch(a.fetch(a.cur), a.fetch(k8s.Key{Context: a.ctx, GVR: nsGVR}))
	case "z":
		a.zoom = !a.zoom
		a.layout()
		return nil
	case "esc":
		switch {
		case a.zoom:
			a.zoom = false
			a.layout()
		case a.table.filter != "":
			a.table.SetFilter("")
			a.syncDetail()
		case a.focus == focusDetail:
			a.setFocus(focusTable)
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
		if key == "enter" || key == "right" || key == "l" {
			a.setFocus(focusDetail)
			return nil
		}
		if key == "left" || key == "h" {
			a.setFocus(focusNav)
			return nil
		}
		cmd = a.table.Update(msg)
		a.syncDetail()
	case focusDetail:
		cmd = a.detail.Update(msg)
	}
	return cmd
}

func (a *App) startFilter() tea.Cmd {
	a.filtering = true
	a.filterInput.SetValue(a.table.filter)
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
		a.table.SetFilter("")
		a.syncDetail()
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

func (a *App) panels() []panelRef {
	return []panelRef{
		{focusNav, a.nav.rect, a.nav.Update},
		{focusTable, a.table.rect, a.table.Update},
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
	items = append(items, paletteItem{cmd: "ns all", desc: "all namespaces"})
	if e, ok := a.store.Get(k8s.Key{Context: a.ctx, GVR: nsGVR}); ok {
		for _, it := range e.Items {
			items = append(items, paletteItem{cmd: "ns " + it.GetName(), desc: "switch namespace"})
		}
	}
	for _, c := range a.store.Provider().Contexts() {
		items = append(items, paletteItem{cmd: "ctx " + c, desc: "switch context"})
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
	res, _ := k8s.Lookup(a.table.res.Name)
	switch f[0] {
	case "q", "quit", "q!":
		return tea.Quit
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
	r, ok := k8s.Lookup(f[0])
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
	v.WindowTitle = "coral"
	if a.w == 0 {
		return v
	}
	a.buttons = a.buttons[:0]

	var parts []string
	if a.nav.rect.w > 0 {
		parts = append(parts, a.nav.View())
	}
	if a.table.rect.w > 0 {
		parts = append(parts, a.table.View())
	}
	if a.detail.rect.w > 0 {
		parts = append(parts, a.detail.View())
	}
	content := a.renderHeader() + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, parts...) + "\n" + a.renderStatus()

	switch {
	case a.palette != nil:
		box := a.palette.View(a.w)
		content = overlay(content, box, a.palette.rect.x, a.palette.rect.y)
	case a.help:
		box := helpView()
		content = overlay(content, box, (a.w-lipgloss.Width(box))/2, max((a.h-lipgloss.Height(box))/2, 0))
	}
	v.SetContent(content)
	return v
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
	seg(sep, nil)
	seg(stBar.Foreground(colAccent).Bold(true).Render(a.table.res.Title), func() tea.Cmd { return a.openPalette("") })
	if o := a.table.Selected(); o != nil {
		seg(sep, nil)
		seg(stBar.Render(o.GetName()), nil)
	}

	right := ""
	if a.table.loading {
		right = "⟳ loading "
	} else if e, ok := a.store.Get(a.cur); ok {
		right = "updated " + duration.HumanDuration(time.Since(e.FetchedAt)) + " ago "
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
		{"tab", "focus", func() tea.Cmd { a.setFocus((a.focus + 1) % 3); return nil }},
		{"z", "zoom", func() tea.Cmd { a.zoom = !a.zoom; a.layout(); return nil }},
	}
	switch a.focus {
	case focusTable:
		hints = append(hints, hint{"s", "sort", func() tea.Cmd { return a.table.Update(tea.KeyPressMsg{Code: 's', Text: "s"}) }})
	case focusDetail:
		hints = append(hints,
			hint{"O", "expand all", func() tea.Cmd { return a.detail.Update(tea.KeyPressMsg{Code: 'O', Text: "O"}) }},
			hint{"C", "collapse all", func() tea.Cmd { return a.detail.Update(tea.KeyPressMsg{Code: 'C', Text: "C"}) }})
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

func helpView() string {
	sections := []struct {
		title string
		keys  [][2]string
	}{
		{"Global", [][2]string{
			{":", "command palette (po, deploy, ns <name>, ctx <name>)"},
			{"/", "filter the table"},
			{"tab / 1 2 3", "focus navigator / table / details"},
			{"z", "zoom the focused panel"},
			{"r", "refresh"},
			{"esc", "back / clear filter / unzoom"},
			{"q", "quit"},
		}},
		{"Lists and trees", [][2]string{
			{"↑↓ j k", "move"},
			{"pgup pgdn g G", "page / top / bottom"},
			{"← → h l", "collapse / expand, or move between panels"},
			{"enter space", "open / toggle"},
			{"s S", "table: next sort column / reverse"},
			{"O C", "details: expand all / collapse all"},
		}},
		{"Mouse", [][2]string{
			{"click", "select, focus a panel, toggle ▸ ▾"},
			{"double-click", "open row in details / toggle node"},
			{"click header", "sort by column (again to reverse)"},
			{"drag border", "resize panels"},
			{"wheel", "scroll the panel under the pointer"},
			{"click breadcrumb", "switch context / namespace / resource"},
		}},
	}
	var lines []string
	for i, s := range sections {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, " "+stAccent.Bold(true).Render(s.title))
		for _, k := range s.keys {
			lines = append(lines, "  "+stKey.Render(fit(k[0], 18))+" "+k[1])
		}
	}
	return frame("Help", "any key to close", lines, 74, len(lines)+2, true)
}
