package k8s

import (
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func TestDemoStoreListsEveryBuiltin(t *testing.T) {
	s := NewStore(NewDemoProvider(nil), time.Second)
	for _, r := range Builtins {
		e := s.Fetch(Key{Context: "demo-dev", GVR: r.GVR()})
		if e.Err != nil {
			t.Errorf("%s: %v", r.Name, e.Err)
		}
	}
	pods := s.Fetch(Key{Context: "demo-dev", GVR: MustLookup("po").GVR(), Namespace: "shop"})
	if len(pods.Items) == 0 {
		t.Fatal("no pods in shop")
	}
	statuses := map[string]bool{}
	for i := range pods.Items {
		statuses[PodStatus(&pods.Items[i])] = true
	}
	for _, want := range []string{"Running", "CrashLoopBackOff", "Completed"} {
		if !statuses[want] {
			t.Errorf("missing pod status %s, got %v", want, statuses)
		}
	}
}

func TestDemoUpdateVersions(t *testing.T) {
	s := NewStore(NewDemoProvider(nil), time.Second)
	key := Key{Context: "demo-dev", GVR: MustLookup("deployments").GVR()}
	obj, err := s.GetObject(key, "shop", "cart")
	if err != nil {
		t.Fatal(err)
	}
	stale := obj.DeepCopy()
	obj.SetLabels(map[string]string{"edited": "yes"})
	upd, err := s.Update(key, obj)
	if err != nil {
		t.Fatal(err)
	}
	if upd.GetResourceVersion() == stale.GetResourceVersion() {
		t.Fatal("update should bump the resourceVersion")
	}
	if _, err := s.Update(key, stale); !apierrors.IsConflict(err) {
		t.Fatalf("stale update: got %v, want a conflict", err)
	}
}
