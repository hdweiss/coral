package ui

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/hdweiss/coral/internal/config"
	"github.com/hdweiss/coral/internal/k8s"
	"github.com/hdweiss/coral/internal/logs"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestPinLabels(t *testing.T) {
	got := pinLabels([]string{".http.response.status_code", ".service.name", ".host.name", ".trace.id", `["@timestamp"]`})
	want := []string{"status_code", "service.name", "host.name", "trace.id", "@timestamp"}
	if !slices.Equal(got, want) {
		t.Errorf("pinLabels = %v, want %v", got, want)
	}
	got = pinLabels([]string{".a.x.level", ".b.x.level"})
	if want := []string{"a.x.level", "b.x.level"}; !slices.Equal(got, want) {
		t.Errorf("pinLabels = %v, want %v", got, want)
	}
}

func TestLogViewStreamsDemoLogs(t *testing.T) {
	p := k8s.NewDemoProvider(nil)
	c, _ := p.Client("demo-dev")
	list, err := c.Resource(k8s.MustLookup("pods").GVR()).Namespace("shop").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var pod *unstructured.Unstructured
	for i := range list.Items {
		if list.Items[i].GetLabels()["app"] == "cart" {
			pod = &list.Items[i]
		}
	}
	v := newLogView("demo-dev", pod, &config.Fields{})
	v.rect = rect{0, 0, 100, 20}
	cmd := v.start(p)
	defer v.stop()
	for len(v.entries) < 300 {
		msg, ok := cmd().(logLinesMsg)
		if !ok || msg.err != nil {
			t.Fatalf("unexpected message %#v", msg)
		}
		if cmd = v.onLines(msg); cmd == nil {
			break
		}
	}
	if len(v.entries) < 300 {
		t.Fatalf("got %d lines", len(v.entries))
	}
	if e := v.Selected(); e == nil || e.Format != logs.ECS || e != &v.entries[len(v.entries)-1] {
		t.Errorf("following should select the newest ECS entry, got %+v", e)
	}
	v.SetFilter("zzz-no-match")
	if len(v.rows) != 0 || v.Selected() != nil {
		t.Errorf("filter left %d rows", len(v.rows))
	}
}

func TestLogViewTrims(t *testing.T) {
	feed := func(v *logView, n int, line string) {
		for range n / 1000 {
			batch := make([]logs.Entry, 1000)
			for i := range batch {
				batch[i] = logs.Parse(line)
			}
			v.onLines(logLinesMsg{gen: v.gen, entries: batch})
		}
	}
	pod := &unstructured.Unstructured{Object: map[string]any{}}

	v := newLogView("c", pod, &config.Fields{})
	v.rect = rect{0, 0, 100, 20}
	feed(v, 22000, "short line")
	if len(v.entries) != logMaxLines || len(v.rows) != logMaxLines || v.cursor != logMaxLines-1 {
		t.Errorf("by lines: %d entries, %d rows, cursor %d", len(v.entries), len(v.rows), v.cursor)
	}

	v = newLogView("c", pod, &config.Fields{})
	v.rect = rect{0, 0, 100, 20}
	long := strings.Repeat("x", 64<<10)
	feed(v, 1000, long)
	want := logMaxBytes / len(long)
	if len(v.entries) != want || v.bytes != want*len(long) || v.cursor != want-1 {
		t.Errorf("by bytes: %d entries (want %d), %d bytes, cursor %d", len(v.entries), want, v.bytes, v.cursor)
	}
}

// A pod whose first container is a sidecar shows every container's log,
// merged by time and labelled, and c cycles through them.
func TestLogViewMergesContainers(t *testing.T) {
	p := k8s.NewDemoProvider(nil)
	c, _ := p.Client("demo-dev")
	list, err := c.Resource(k8s.MustLookup("pods").GVR()).Namespace("payments").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var pod *unstructured.Unstructured
	for i := range list.Items {
		if list.Items[i].GetLabels()["app"] == "payments-api" && k8s.PodStatus(&list.Items[i]) == "Running" {
			pod = &list.Items[i]
		}
	}
	if pod == nil || k8s.Containers(pod)[0] != "linkerd-proxy" {
		t.Fatal("no demo pod with a proxy sidecar first")
	}
	v := newLogView("demo-dev", pod, &config.Fields{})
	v.rect = rect{0, 0, 140, 20}
	if v.container != "" {
		t.Fatalf("opened on container %q, want all", v.container)
	}
	read := func() {
		t.Helper()
		cmd := v.start(p)
		for sources := map[string]bool{}; len(sources) < 2 || len(v.entries) < 300; {
			msg := cmd().(logLinesMsg)
			if cmd = v.onLines(msg); cmd == nil {
				t.Fatalf("stream ended with %d lines from %v", len(v.entries), sources)
			}
			for _, e := range v.entries {
				sources[e.Source] = true
			}
		}
		v.stop()
	}
	read()
	if !slices.IsSortedFunc(v.entries, func(a, b logs.Entry) int { return a.KubeTime.Compare(b.KubeTime) }) {
		t.Error("merged lines are not in time order")
	}
	if !strings.Contains(v.View(), "payments-api ") || !strings.Contains(v.title(), "all containers") {
		t.Errorf("lines are not labelled:\n%s", v.View())
	}

	v.nextContainer()
	if v.container != "linkerd-proxy" {
		t.Errorf("c went to %q", v.container)
	}
	cmd := v.start(p)
	defer v.stop()
	for len(v.entries) < 40 {
		if cmd = v.onLines(cmd().(logLinesMsg)); cmd == nil {
			break
		}
	}
	if v.labelW != 0 || v.entries[0].Source != "" {
		t.Error("a single container's lines are labelled")
	}
}

func TestLogViewMergeKeepsSelection(t *testing.T) {
	v := newLogView("c", &unstructured.Unstructured{Object: map[string]any{}}, &config.Fields{})
	v.rect = rect{0, 0, 100, 20}
	v.labelW = 3
	at := func(sec int, src string) logs.Entry {
		e := logs.Parse(fmt.Sprintf("2026-10-09T10:00:%02dZ line %d", sec, sec))
		e.Source = src
		return e
	}
	v.onLines(logLinesMsg{gen: v.gen, entries: []logs.Entry{at(1, "a"), at(3, "a"), at(5, "a")}})
	v.cursor, v.follow = 1, false // on 3
	v.onLines(logLinesMsg{gen: v.gen, entries: []logs.Entry{at(2, "b"), at(4, "b")}})
	var got []string
	for _, e := range v.entries {
		got = append(got, e.Raw)
	}
	if want := []string{"line 1", "line 2", "line 3", "line 4", "line 5"}; !slices.Equal(got, want) {
		t.Errorf("order %v", got)
	}
	if e := v.Selected(); e == nil || e.Raw != "line 3" {
		t.Errorf("selection moved to %+v", e)
	}
}

func TestWorkloadLogs(t *testing.T) {
	a := newTestApp(t, Options{})
	a.Init()
	deploy := k8s.MustLookup("deployments")
	key := k8s.Key{Context: "demo-dev", GVR: deploy.GVR(), Namespace: "shop"}
	a.activate(key, deploy)
	a.Update(fetchedMsg{key: key, entry: a.store.Fetch(key)})
	for a.table.Selected().GetName() != "frontend" {
		a.table.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	_, cmd := a.Update(tea.KeyPressMsg{Code: 'L', Text: "L"})
	msg, ok := cmd().(workloadPodsMsg)
	if !ok || msg.err != nil {
		t.Fatalf("got %#v", msg)
	}
	a.Update(msg)
	if a.logs == nil || len(a.logs.pods) != 3 {
		t.Fatalf("log view of %d pods", len(a.logs.pods))
	}
	defer a.logs.stop()
	srcs := a.logs.sources()
	if len(srcs) != 3 || !strings.HasPrefix(srcs[0].label, "frontend-") || strings.Contains(srcs[0].label, "/") {
		t.Errorf("sources %+v", srcs)
	}
	if !strings.Contains(a.logs.title(), "deployment frontend (3 pods)") {
		t.Errorf("title %q", a.logs.title())
	}
}

func TestLayoutSegments(t *testing.T) {
	plain := lipgloss.NewStyle()
	segs := []segment{{"abcdef", plain}, {"ghij", plain}, {"\n", plain}, {"kl", plain}}
	got := layoutSegments(segs, 4, true, plain)
	if want := []string{"abcd", "efgh", "ij  ", "kl  "}; !slices.Equal(got, want) {
		t.Errorf("wrap: %q, want %q", got, want)
	}
	got = layoutSegments(segs[:2], 6, false, plain)
	if len(got) != 1 || got[0] != "abcde…" {
		t.Errorf("cut: %q", got)
	}
}

func TestLogViewWrapAndSearch(t *testing.T) {
	v := newLogView("c", &unstructured.Unstructured{Object: map[string]any{}}, &config.Fields{})
	v.rect = rect{0, 0, 40, 8} // 6 lines inside
	var batch []logs.Entry
	for i := range 10 {
		batch = append(batch, logs.Parse(fmt.Sprintf("2026-10-09T10:00:%02dZ line %d %s", i, i, strings.Repeat("x", 60))))
	}
	batch[3] = logs.Parse("2026-10-09T10:00:03Z needle here")
	v.onLines(logLinesMsg{gen: v.gen, entries: batch})
	v.wrap = true
	v.scroll()
	if h := v.entryHeight(0); h < 2 {
		t.Fatalf("a long entry takes %d lines wrapped", h)
	}
	if v.cursor != 9 || v.linesBetween(v.offset, v.cursor) > v.height() {
		t.Errorf("the followed last entry is not on screen: offset %d", v.offset)
	}
	if got := strings.Count(v.View(), "\n"); got != v.rect.h-1 {
		t.Errorf("view is %d lines high", got+1)
	}
	v.search = "NEEDLE"
	if !v.jump(1) || v.cursor != 3 || v.follow {
		t.Errorf("search jumped to %d", v.cursor)
	}
	if v.rowAt(0) != v.offset {
		t.Error("the first line is not the offset row")
	}
}

func TestLogRanges(t *testing.T) {
	v := newLogView("c", &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "p"}}}, &config.Fields{})
	if s := v.sources(); len(s) != 0 {
		t.Fatalf("a pod without containers streams %v", s)
	}
	v.pods[0].Object["spec"] = map[string]any{"containers": []any{map[string]any{"name": "c"}}}
	v.containers, v.container = []string{"c"}, "c"
	for key, want := range map[string]string{"0": "tail 500", "1": "all", "5": "since 30m", "6": "since 1h"} {
		v.tail, v.since = logRanges[key].tail, logRanges[key].since
		if got := v.rangeLabel(); got != want {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
	r := logRanges["3"]
	v.tail, v.since = r.tail, r.since
	req := v.sources()[0].req
	if req.SinceSeconds != 300 || req.TailLines != 0 || v.rangeLabel() != "since 5m" {
		t.Errorf("5m: %+v %q", req, v.rangeLabel())
	}
}

func TestLogSave(t *testing.T) {
	t.Chdir(t.TempDir())
	v := newLogView("c", &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "web-1"}}}, &config.Fields{})
	v.rect = rect{0, 0, 80, 10}
	v.onLines(logLinesMsg{gen: v.gen, entries: []logs.Entry{logs.Parse("2026-10-09T10:00:00Z hello"), logs.Parse("2026-10-09T10:00:01Z skip")}})
	v.SetFilter("hello")
	name, err := v.save(time.Date(2026, 10, 9, 12, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(name)
	if name != "web-1-all-20261009-120000.log" || string(b) != "2026-10-09T10:00:00Z hello\n" {
		t.Errorf("%s: %q", name, b)
	}
}

func TestLogPinsPerApp(t *testing.T) {
	fields := &config.Fields{Kinds: map[string]*config.KindFields{logFieldsKind: {Favorites: []string{".old"}}}}
	pod := func(app string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "p", "labels": map[string]any{"app": app}}}}
	}
	cart, web := newLogView("c", pod("cart"), fields), newLogView("c", pod("web"), fields)
	cart.togglePin(".user")
	if !slices.Equal(cart.pins(), []string{".old", ".user"}) || !slices.Equal(web.pins(), []string{".old"}) {
		t.Errorf("cart %v, web %v", cart.pins(), web.pins())
	}
	web.togglePin(".old") // unpinning a global pin removes it everywhere
	if !slices.Equal(cart.pins(), []string{".user"}) {
		t.Errorf("after unpinning .old: %v", cart.pins())
	}
	nodes := leafPatterns(map[string]any{"a": map[string]any{"b": "x"}, "c": int64(1)})
	var got []string
	for _, n := range nodes {
		got = append(got, n.Pattern())
	}
	if !slices.Equal(got, []string{".a.b", ".c"}) {
		t.Errorf("leaves %v", got)
	}
}
