package k8s

import (
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func demoList(t *testing.T, s *Store, res, ns string) []unstructured.Unstructured {
	t.Helper()
	e := s.Fetch(Key{Context: "demo-dev", GVR: MustLookup(res).GVR(), Namespace: ns})
	if e.Err != nil {
		t.Fatal(e.Err)
	}
	return e.Items
}

func find(t *testing.T, items []unstructured.Unstructured, prefix string) *unstructured.Unstructured {
	t.Helper()
	for i := range items {
		if strings.HasPrefix(items[i].GetName(), prefix) {
			return &items[i]
		}
	}
	t.Fatalf("no object named %s*", prefix)
	return nil
}

func TestDemoEventsAndWarnings(t *testing.T) {
	s := NewStore(NewDemoProvider(nil), time.Second)
	events := demoList(t, s, "events", "shop")
	ix := IndexWarnings(events, time.Now().Add(-WarningWindow))

	var crashing, healthy *unstructured.Unstructured
	pods := demoList(t, s, "pods", "shop")
	for i := range pods {
		switch PodStatus(&pods[i]) {
		case "CrashLoopBackOff":
			crashing = &pods[i]
		case "Running":
			healthy = &pods[i]
		}
	}
	if n := ix.Count(crashing); n != 2 { // Unhealthy, BackOff
		t.Errorf("crashing pod: %d warnings, want 2", n)
	}
	if n := ix.Count(healthy); n != 0 {
		t.Errorf("healthy pod: %d warnings, want 0", n)
	}
	about := EventsAbout(events, crashing)
	if len(about) != 5 || about[0].Object["reason"] != "BackOff" {
		t.Errorf("events about the crashing pod, newest first: got %d, first %v", len(about), about[0].Object["reason"])
	}

	// Node events carry the node's name as uid and live in default.
	node := find(t, demoList(t, s, "nodes", ""), "dev-node-3")
	nodeEvents := demoList(t, s, "events", "")
	if n := IndexWarnings(nodeEvents, time.Now().Add(-WarningWindow)).Count(node); n != 1 {
		t.Errorf("NotReady node: %d warnings, want 1", n)
	}
	if got := len(EventsAbout(nodeEvents, node)); got != 2 {
		t.Errorf("node events: %d, want 2", got)
	}
}

func TestEventAboutRecreatedObject(t *testing.T) {
	pod := &unstructured.Unstructured{}
	pod.SetKind("Pod")
	pod.SetNamespace("shop")
	pod.SetName("redis-0")
	pod.SetUID("new")
	ev := func(uid string) unstructured.Unstructured {
		return unstructured.Unstructured{Object: map[string]any{
			"type":           "Warning",
			"lastTimestamp":  time.Now().UTC().Format(time.RFC3339),
			"involvedObject": map[string]any{"kind": "Pod", "namespace": "shop", "name": "redis-0", "uid": uid},
		}}
	}
	events := []unstructured.Unstructured{ev("old"), ev("new"), ev("")}
	if n := IndexWarnings(events, time.Now().Add(-time.Hour)).Count(pod); n != 2 {
		t.Errorf("got %d warnings, want 2 (the old pod's is not counted)", n)
	}
	if n := len(EventsAbout(events, pod)); n != 2 {
		t.Errorf("got %d events, want 2", n)
	}
}

// names renders a relation as "Kind/name" entries.
func names(rels []Relation, title string) []string {
	for _, r := range rels {
		if r.Title == title {
			var out []string
			for _, it := range r.Items {
				out = append(out, it.Kind+"/"+it.Name)
			}
			return out
		}
	}
	return nil
}

func TestDemoRelations(t *testing.T) {
	s := NewStore(NewDemoProvider(nil), time.Second)
	list := s.Lister("demo-dev", time.Minute)
	rel := func(obj *unstructured.Unstructured) []Relation {
		t.Helper()
		rels, err := Relations(list, nil, obj)
		if err != nil {
			t.Fatal(err)
		}
		return rels
	}

	pod := find(t, demoList(t, s, "pods", "shop"), "cart-")
	rels := rel(pod)
	owners := names(rels, "Owners")
	if len(owners) != 2 || !strings.HasPrefix(owners[0], "ReplicaSet/cart-") || owners[1] != "Deployment/cart" {
		t.Errorf("owners: %v", owners)
	}
	if got := names(rels, "Services"); !slices.Equal(got, []string{"Service/cart"}) {
		t.Errorf("services: %v", got)
	}
	// default-deny selects every pod, allow-frontend-to-cart the cart pods.
	if got := names(rels, "Network policies"); len(got) != 2 {
		t.Errorf("network policies: %v", got)
	}
	if got := names(rels, "Uses"); !slices.Equal(got, []string{"Secret/cart-db", "ServiceAccount/default"}) {
		t.Errorf("uses: %v", got)
	}

	dep := find(t, demoList(t, s, "deployments", "shop"), "frontend")
	rels = rel(dep)
	if got := names(rels, "Pods"); len(got) != 3 {
		t.Errorf("frontend pods: %v", got)
	}
	if got := names(rels, "Autoscalers"); !slices.Equal(got, []string{"HorizontalPodAutoscaler/frontend"}) {
		t.Errorf("autoscalers: %v", got)
	}

	cm := find(t, demoList(t, s, "configmaps", "shop"), "frontend-config")
	if got := names(rel(cm), "Used by"); !slices.Equal(got, []string{"Deployment/frontend"}) {
		t.Errorf("configmap used by: %v", got)
	}
	tls := find(t, demoList(t, s, "secrets", "shop"), "shop-tls")
	if got := names(rel(tls), "Used by"); !slices.Equal(got, []string{"Ingress/shop"}) {
		t.Errorf("tls secret used by: %v", got)
	}
	np := find(t, demoList(t, s, "networkpolicies", "shop"), "allow-frontend-to-cart")
	if got := names(rel(np), "Pods"); len(got) != 2 {
		t.Errorf("policy pods: %v", got)
	}
	svc := find(t, demoList(t, s, "services", "shop"), "frontend")
	if got := names(rel(svc), "Ingresses"); !slices.Equal(got, []string{"Ingress/shop"}) {
		t.Errorf("service ingresses: %v", got)
	}
}

func TestOwnedTimeline(t *testing.T) {
	s := NewStore(NewDemoProvider(nil), time.Second)
	list := s.Lister("demo-dev", time.Minute)
	deps, _ := list(MustLookup("deployments"), "shop")
	dep := find(t, deps, "catalog")
	owned, err := Owned(list, dep)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, o := range owned {
		kinds[o.GetKind()]++
	}
	if kinds["ReplicaSet"] != 1 || kinds["Pod"] != 2 {
		t.Fatalf("catalog owns %v", kinds)
	}
	evs, _ := list(MustLookup("events"), "shop")
	got := EventsAboutAny(evs, append(owned, dep))
	var pods int
	for _, e := range got {
		if strings.HasPrefix(EventObject(e), "pod/catalog-") {
			pods++
		}
	}
	if pods == 0 {
		t.Error("the timeline has no events of the crash-looping pod")
	}
}
