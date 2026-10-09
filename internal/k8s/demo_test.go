package k8s

import (
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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

func TestDemoChurn(t *testing.T) {
	p := NewDemoProvider(nil)
	s := NewStore(p, time.Second)
	key := Key{Context: "demo-dev", GVR: MustLookup("pods").GVR(), Namespace: "shop"}
	before := s.Fetch(key)
	c := &churner{tracker: p.(*demoProvider).clients["demo-dev"].(*dynamicfake.FakeDynamicClient).Tracker()}
	statuses := map[string]bool{}
	for round := 1; round <= 5; round++ {
		c.step(round)
		for _, pod := range s.Fetch(key).Items {
			statuses[PodStatus(&pod)] = true
		}
	}
	for _, want := range []string{"ContainerCreating", "CrashLoopBackOff", "Running"} {
		if !statuses[want] {
			t.Errorf("no %s pod during churn, saw %v", want, statuses)
		}
	}
	c.step(6)
	after := s.Fetch(key)
	if len(after.Items) != len(before.Items) {
		t.Errorf("%d pods after churn, %d before", len(after.Items), len(before.Items))
	}
	for _, pod := range after.Items {
		if PodStatus(&pod) == "ContainerCreating" {
			t.Errorf("%s never started", pod.GetName())
		}
	}
}
