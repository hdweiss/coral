package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

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
