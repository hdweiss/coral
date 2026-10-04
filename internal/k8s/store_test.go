package k8s

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreFetchSharesRequestsInFlight(t *testing.T) {
	var log bytes.Buffer
	s := NewStore(NewDemoProvider(NewTracer(&log)), time.Second)
	key := Key{Context: "demo-dev", GVR: MustLookup("pods").GVR(), Namespace: "shop"}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if e := s.Fetch(key); e.Err != nil || len(e.Items) == 0 {
				t.Errorf("fetch: %d items, %v", len(e.Items), e.Err)
			}
		})
	}
	wg.Wait()
	// Racing goroutines may finish one request before the next starts; what
	// matters is that they share some.
	if n := strings.Count(log.String(), "LIST pods"); n == 0 || n >= 20 {
		t.Errorf("%d list requests for 20 concurrent fetches", n)
	}

	log.Reset()
	s.Cached(key, time.Minute)
	if log.Len() != 0 {
		t.Errorf("a fresh cached list was fetched again: %s", log.String())
	}
}

func TestWarningsKey(t *testing.T) {
	pods := Key{Context: "c", GVR: MustLookup("pods").GVR(), Namespace: "shop"}
	k, ok := WarningsKey(pods, MustLookup("pods"))
	if !ok || k.Namespace != "shop" || k.Fields != "type=Warning" {
		t.Errorf("pods: %+v %v", k, ok)
	}
	if k == (Key{Context: "c", GVR: EventsGVR, Namespace: "shop"}) {
		t.Error("the warnings share a cache entry with the events table")
	}
	nodes := Key{Context: "c", GVR: MustLookup("nodes").GVR()}
	if k, ok := WarningsKey(nodes, MustLookup("nodes")); !ok || k.Namespace != "" || !strings.Contains(k.Fields, "involvedObject.kind=Node") {
		t.Errorf("nodes: %+v %v", k, ok)
	}
	for _, name := range []string{"persistentvolumes", "customresourcedefinitions", "events"} {
		r := MustLookup(name)
		if _, ok := WarningsKey(Key{Context: "c", GVR: r.GVR()}, r); ok {
			t.Errorf("%s has warning markers", name)
		}
	}
}

func TestUpdateReplacesCachedObject(t *testing.T) {
	s := NewStore(NewDemoProvider(nil), time.Second)
	shop := Key{Context: "demo-dev", GVR: MustLookup("deployments").GVR(), Namespace: "shop"}
	all := Key{Context: "demo-dev", GVR: shop.GVR}
	before := s.Fetch(shop)
	s.Fetch(all)
	obj, err := s.GetObject(shop, "shop", "cart")
	if err != nil {
		t.Fatal(err)
	}
	obj.SetLabels(map[string]string{"edited": "yes"})
	if _, err := s.Update(shop, obj); err != nil {
		t.Fatal(err)
	}
	for _, k := range []Key{shop, all} {
		e, _ := s.Get(k)
		if got := find(t, e.Items, "cart").GetLabels()["edited"]; got != "yes" {
			t.Errorf("%+v: cached cart not updated", k)
		}
	}
	if find(t, before.Items, "cart").GetLabels()["edited"] != "" {
		t.Error("the update changed a list a view may still hold")
	}
}
