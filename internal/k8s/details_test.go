package k8s

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func detailField(secs []Section, title, key string) (Field, bool) {
	for _, s := range secs {
		if s.Title == title {
			for _, f := range s.Fields {
				if f.Key == key {
					return f, true
				}
			}
		}
	}
	return Field{}, false
}

func TestDetails(t *testing.T) {
	s := NewStore(NewDemoProvider(nil), time.Second)
	pods := s.Fetch(Key{Context: "demo-dev", GVR: MustLookup("pods").GVR(), Namespace: "shop"}).Items
	var crash *Section
	for i := range pods {
		if PodStatus(&pods[i]) != "CrashLoopBackOff" {
			continue
		}
		secs := Details(&pods[i])
		if f, ok := detailField(secs, "Pod", "Status"); !ok || !f.Warn {
			t.Errorf("pod status %+v", f)
		}
		for i := range secs {
			if strings.HasPrefix(secs[i].Title, "Container ") {
				crash = &secs[i]
			}
		}
		if f, _ := detailField(secs, crash.Title, "State"); !f.Warn || !strings.Contains(f.Value, "CrashLoopBackOff") {
			t.Errorf("state %+v", f)
		}
		if f, _ := detailField(secs, crash.Title, "Readiness"); f.Value != "http-get :http/healthz delay=5s period=10s" {
			t.Errorf("probe %q", f.Value)
		}
		if f, _ := detailField(secs, crash.Title, "Last state"); !strings.Contains(f.Value, "Error (exit 1)") {
			t.Errorf("last state %q", f.Value)
		}
		if f, ok := detailField(secs, "Conditions", "Ready"); !ok || !f.Warn {
			t.Errorf("ready condition %+v", f)
		}
	}
	if crash == nil {
		t.Fatal("no crash-looping demo pod")
	}
	deps := s.Fetch(Key{Context: "demo-dev", GVR: MustLookup("deployments").GVR(), Namespace: "shop"}).Items
	secs := Details(find(t, deps, "catalog"))
	if f, _ := detailField(secs, "Deployment", "Replicas"); !f.Warn || !strings.HasPrefix(f.Value, "2 desired") {
		t.Errorf("replicas %+v", f)
	}
}

func TestGenericCustomRelations(t *testing.T) {
	s := NewStore(NewDemoProvider(nil), time.Second)
	reg := NewRegistry(CustomResources(s.Fetch(CRDKey("demo-dev")).Items), nil)
	res, ok := reg.Lookup("servicemonitors")
	if !ok {
		t.Fatal("no ServiceMonitor CRD in the demo")
	}
	list := s.Lister("demo-dev", time.Minute)
	mons, err := list(res, "")
	if err != nil || len(mons) == 0 {
		t.Fatal("no service monitors", err)
	}
	// One in shop selecting its own namespace's service, one in monitoring
	// selecting shop's through its namespaceSelector.
	for _, name := range []string{"cart", "frontend"} {
		mon := find(t, mons, name)
		rels, err := Relations(list, reg, mon)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range rels {
			for _, it := range r.Items {
				got = append(got, r.Title+":"+it.Name)
			}
		}
		if !slices.Contains(got, "Selected services:"+name) {
			t.Errorf("%s/%s relations: %v", mon.GetNamespace(), name, got)
		}
	}
}
