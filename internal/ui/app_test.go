package ui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/hdweiss/coral/internal/k8s"
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
