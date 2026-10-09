package ui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hdweiss/coral/internal/config"
	"github.com/hdweiss/coral/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newTestApp(t *testing.T, opts Options) *App {
	t.Helper()
	a, err := New(k8s.NewStore(k8s.NewDemoProvider(nil), time.Second), opts)
	if err != nil {
		t.Fatal(err)
	}
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	return a
}

func TestRefreshPausesWhenHidden(t *testing.T) {
	a := newTestApp(t, Options{Refresh: time.Nanosecond})
	a.Init()
	a.store.Fetch(a.cur)
	due := func() bool {
		clear(a.loading) // as if every fetch had finished
		return a.refreshDue() != nil
	}
	if !due() {
		t.Fatal("a stale visible list is not refreshed")
	}

	a.Update(tea.BlurMsg{})
	if due() {
		t.Error("refreshing while the terminal is unfocused")
	}
	a.Update(tea.FocusMsg{})

	if !due() {
		t.Error("no refresh when the focus returns")
	}
	a.logs = &logView{}
	if due() {
		t.Error("refreshing the list behind the log view")
	}
}

func TestDescribeShowsSubjectFirst(t *testing.T) {
	a := newTestApp(t, Options{})
	a.Init()
	a.store.Fetch(a.cur)
	e, _ := a.store.Get(a.cur)
	a.table.SetEntry(e)
	a.openDescribe(false)
	if a.desc == nil || len(a.desc.rows) == 0 {
		t.Fatal("no describe view")
	}
	top := a.desc.rows[0]
	if !top.self || top.rel.Obj != a.desc.subject || top.rel.Res.Name != "pods" {
		t.Errorf("top row: %+v", top)
	}
	a.desc.SetFilter("no-such-thing")
	if !a.desc.rows[0].self {
		t.Error("the filter hid the described object")
	}
	// d on the object itself goes back instead of describing it again.
	a.openDescribe(false)
	if a.desc != nil {
		t.Error("d on the top row opened another view")
	}
}

func TestLiveWatchesVisibleList(t *testing.T) {
	a := newTestApp(t, Options{Refresh: time.Nanosecond})
	a.Init()
	a.store.Fetch(a.cur)
	a.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	ek, _ := a.warningsKey()
	nk := k8s.ClusterWarningsKey(a.cur.Context)
	if len(a.watches) != 3 || a.watches[a.cur] == nil || a.watches[ek] == nil || a.watches[nk] == nil {
		t.Fatalf("watches %v, want the list, its warnings and the navigator's", a.watches)
	}
	clear(a.loading)
	if a.refreshDue() != nil {
		t.Error("polling a watched list")
	}

	// A change to the cluster reaches the table without a fetch. (A
	// creation, since the fake watch replays those if it happens between
	// the watch's list and its watch; a real API server replays deletions
	// too.)
	e, _ := a.store.Get(a.cur)
	pod := e.Items[0].DeepCopy()
	pod.SetName("new-pod")
	pod.SetUID("new-pod-uid")
	pod.SetResourceVersion("")
	ri, _ := a.store.Provider().Client(a.cur.Context)
	if _, err := ri.Resource(a.cur.GVR).Namespace(a.cur.Namespace).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		msg := make(chan tea.Msg)
		go func() { msg <- a.waitWatched()() }()
		select {
		case m := <-msg:
			a.Update(m)
		case <-deadline:
			t.Fatal("the new pod never reached the table")
		}
		if len(a.table.rows) == len(e.Items)+1 {
			break
		}
	}

	a.logs = &logView{}
	a.syncWatches()
	if len(a.watches) != 1 || a.watches[nk] == nil {
		t.Error("watching a list hidden by the log view")
	}
	a.logs = nil
	a.Update(tea.BlurMsg{})
	if len(a.watches) != 3 {
		t.Error("an unfocused terminal stopped the watches")
	}
	a.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	if len(a.watches) != 0 {
		t.Error("watches left running after live mode was switched off")
	}
}

func showPods(t *testing.T, a *App) {
	t.Helper()
	a.Init()
	e := a.store.Fetch(a.cur)
	a.Update(fetchedMsg{key: a.cur, entry: e})
	if a.table.Selected() == nil {
		t.Fatal("no pod selected")
	}
}

func TestReadOnlyRefusesWrites(t *testing.T) {
	a := newTestApp(t, Options{Settings: config.Settings{ReadOnly: []string{"demo-*"}}})
	showPods(t, a)
	a.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if a.editing != nil || !strings.Contains(a.flash, "read-only") {
		t.Errorf("edit in a read-only context: flash %q", a.flash)
	}
	for _, act := range a.actionsFor(a.table.res) {
		if act.write {
			t.Errorf("%s offered in a read-only context", act.label)
		}
	}
	if !strings.Contains(a.View().Content, "read-only") {
		t.Error("the header doesn't say read-only")
	}
}

func TestActionMenu(t *testing.T) {
	a := newTestApp(t, Options{})
	showPods(t, a)
	a.Update(tea.KeyPressMsg{Code: '.', Text: "."})
	if a.palette == nil {
		t.Fatal("no action menu")
	}
	var labels []string
	for _, it := range a.palette.items {
		labels = append(labels, it.cmd)
	}
	for _, want := range []string{"logs", "describe", "edit"} {
		if !slices.Contains(labels, want) {
			t.Errorf("menu %v lacks %s", labels, want)
		}
	}
	if slices.Contains(labels, "instances") {
		t.Error("a pod offers the CRD action")
	}
	for _, r := range "describe" {
		a.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.palette != nil || a.desc == nil {
		t.Error("picking describe from the menu didn't open it")
	}
}

func TestConfirm(t *testing.T) {
	ran := 0
	opt := &confirmOption{key: "p", label: "propagation", values: []string{"Background", "Foreground"}}
	c := newConfirm("Delete", "Delete", true, []string{"pod x"}, func(c *confirm) tea.Cmd {
		ran++
		return nil
	}, opt)
	c.View(100, 40)
	if done, _ := c.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); !done || ran != 0 {
		t.Error("enter on a destructive dialog should cancel")
	}
	c.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if opt.value() != "Foreground" {
		t.Error("p didn't cycle the option")
	}
	if done, _ := c.Update(tea.KeyPressMsg{Code: 'y', Text: "y"}); !done || ran != 1 {
		t.Error("y didn't confirm")
	}
}

func TestDeleteAsksFirst(t *testing.T) {
	a := newTestApp(t, Options{})
	showPods(t, a)
	victim := a.table.Selected()
	a.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if a.confirm == nil {
		t.Fatal("ctrl+d didn't ask")
	}
	a.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // focus is on Cancel
	if a.confirm != nil {
		t.Fatal("enter didn't close the dialog")
	}
	if _, err := a.store.GetObject(a.cur, victim.GetNamespace(), victim.GetName()); err != nil {
		t.Fatal("enter deleted the pod")
	}
	a.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	msg := cmd().(deletedMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	a.Update(msg)
	if _, err := a.store.GetObject(a.cur, victim.GetNamespace(), victim.GetName()); err == nil {
		t.Error("the pod is still there")
	}
}

func TestNavigatorWarningsInLiveMode(t *testing.T) {
	a := newTestApp(t, Options{})
	showPods(t, a)
	nsKey := k8s.Key{Context: "demo-dev", GVR: nsGVR}
	a.Update(fetchedMsg{key: nsKey, entry: a.store.Fetch(nsKey)})
	a.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	deadline := time.After(2 * time.Second)
	for a.nav.warnings["demo-dev"]["shop"] == 0 {
		msg := make(chan tea.Msg)
		go func() { msg <- a.waitWatched()() }()
		select {
		case m := <-msg:
			a.Update(m)
		case <-deadline:
			t.Fatalf("no warning count for shop: %v", a.nav.warnings)
		}
	}
	if !strings.Contains(a.nav.View(), "⚠") {
		t.Errorf("the navigator doesn't show the count:\n%s", a.nav.View())
	}
	a.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	if a.nav.warnings["demo-dev"] != nil {
		t.Error("counts stay after live mode is off")
	}
}
