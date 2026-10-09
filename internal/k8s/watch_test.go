package k8s

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
)

func TestWatchFollowsChanges(t *testing.T) {
	var log syncBuffer
	s := NewStore(NewDemoProvider(NewTracer(&log)), time.Second)
	key := Key{Context: "demo-dev", GVR: MustLookup("pods").GVR(), Namespace: "shop"}
	notified := make(chan struct{}, 100)
	stop := s.Watch(key, func() { notified <- struct{}{} })
	defer stop()
	waitFor := func(what string, ok func(Entry) bool) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			if e, _ := s.Get(key); ok(e) {
				return
			}
			select {
			case <-notified:
			case <-deadline:
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	waitFor("the list", func(e Entry) bool { return len(e.Items) > 0 && e.ResourceVersion != "" })
	before, _ := s.Get(key)

	ri, _ := s.resource(key, "shop")
	ctx := context.Background()
	victim := before.Items[0].GetName()
	if err := ri.Delete(ctx, victim, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor("the deletion", func(e Entry) bool { return len(e.Items) == len(before.Items)-1 })

	added := before.Items[1].DeepCopy()
	added.SetName("added")
	added.SetUID("added-uid")
	added.SetResourceVersion("")
	if _, err := ri.Create(ctx, added, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor("the addition", func(e Entry) bool { _, ok := findOK(e.Items, "added"); return ok })

	mod, err := ri.Get(ctx, "added", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mod.SetLabels(map[string]string{"changed": "yes"})
	if _, err := ri.Update(ctx, mod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor("the update", func(e Entry) bool { u, ok := findOK(e.Items, "added"); return ok && u.GetLabels()["changed"] == "yes" })

	if _, ok := findOK(before.Items, "added"); ok || len(before.Items) == 0 {
		t.Error("the watch changed a list a view may still hold")
	}
	if n := strings.Count(log.String(), "LIST pods"); n != 1 {
		t.Errorf("%d lists, want 1:\n%s", n, log.String())
	}
	if !strings.Contains(log.String(), "WATCH pods ns=shop rv=") {
		t.Errorf("watch not traced:\n%s", log.String())
	}
}

func TestWatchFollowsAfterRelist(t *testing.T) {
	var log syncBuffer
	s := NewStore(NewDemoProvider(NewTracer(&log)), time.Second)
	key := Key{Context: "demo-dev", GVR: MustLookup("pods").GVR(), Namespace: "shop"}
	notified := make(chan struct{}, 100)
	stop := s.Watch(key, func() { notified <- struct{}{} })
	defer stop()
	for deadline := time.Now().Add(2 * time.Second); !strings.Contains(log.String(), "WATCH"); {
		if time.Now().After(deadline) {
			t.Fatal("no watch")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.Fetch(key) // r while live: the running watch must not apply to this list
	ri, _ := s.resource(key, "shop")
	ctx := context.Background()
	e, _ := s.Get(key)
	obj, err := ri.Get(ctx, e.Items[0].GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obj.SetLabels(map[string]string{"changed": "yes"})
	if _, err := ri.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		if cur, _ := s.Get(key); cur.Items[0].GetLabels()["changed"] == "yes" {
			break
		}
		select {
		case <-notified:
		case <-deadline:
			t.Fatal("the watch stopped following after a relist")
		}
	}
}

func TestApplyAfterRelist(t *testing.T) {
	s := NewStore(NewDemoProvider(nil), time.Second)
	key := Key{Context: "demo-dev", GVR: MustLookup("pods").GVR(), Namespace: "shop"}
	s.entries[key] = Entry{ResourceVersion: "7"}
	pod := &unstructured.Unstructured{}
	pod.SetUID("u")
	pod.SetResourceVersion("6")
	rv := "5" // the watch started from an older list
	if _, err := s.apply(key, &rv, []watch.Event{{Type: watch.Added, Object: pod}}); !errors.Is(err, errRelisted) {
		t.Fatalf("got %v, want errRelisted", err)
	}
	if e, _ := s.Get(key); len(e.Items) != 0 {
		t.Error("an older change was applied to a newer list")
	}
	rv = "7"
	pod.SetResourceVersion("8")
	if changed, err := s.apply(key, &rv, []watch.Event{{Type: watch.Added, Object: pod}}); !changed || err != nil || rv != "8" {
		t.Fatalf("changed %v, err %v, rv %s", changed, err, rv)
	}
}

// syncBuffer is a bytes.Buffer safe to read while the tracer writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func findOK(items []unstructured.Unstructured, name string) (*unstructured.Unstructured, bool) {
	for i := range items {
		if items[i].GetName() == name {
			return &items[i], true
		}
	}
	return nil, false
}
