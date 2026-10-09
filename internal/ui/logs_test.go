package ui

import (
	"context"
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
