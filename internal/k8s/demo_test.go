package k8s

import (
	"testing"
	"time"
)

func TestDemoStoreListsEveryBuiltin(t *testing.T) {
	s := NewStore(NewDemoProvider(), time.Second)
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
