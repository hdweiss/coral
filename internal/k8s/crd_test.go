package k8s

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func crdObj(group, kind, plural, scope string, versions ...map[string]any) unstructured.Unstructured {
	var vs []any
	for _, v := range versions {
		vs = append(vs, v)
	}
	return unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": map[string]any{"name": plural + "." + group},
		"spec": map[string]any{
			"group": group, "scope": scope, "versions": vs,
			"names": map[string]any{"kind": kind, "plural": plural, "singular": strings.ToLower(kind)},
		},
	}}
}

func TestCustomResources(t *testing.T) {
	cert := crdObj("cert-manager.io", "Certificate", "certificates", "Namespaced",
		map[string]any{"name": "v1alpha2", "served": false, "storage": false},
		map[string]any{"name": "v1beta1", "served": true, "storage": false},
		map[string]any{"name": "v1", "served": true, "storage": true, "additionalPrinterColumns": []any{
			map[string]any{"name": "Ready", "type": "string", "jsonPath": `.status.conditions[?(@.type=="Ready")].status`},
			map[string]any{"name": "Issuer", "type": "string", "jsonPath": ".spec.issuerRef.name", "priority": int64(1)},
		}})
	unstructured.SetNestedStringSlice(cert.Object, []string{"cert", "certs"}, "spec", "names", "shortNames")
	// The storage version isn't served: the first served one is listed.
	prom := crdObj("monitoring.coreos.com", "Prometheus", "prometheuses", "Namespaced",
		map[string]any{"name": "v1", "served": true, "storage": false},
		map[string]any{"name": "v2", "served": false, "storage": true})
	gc := crdObj("gateway.networking.k8s.io", "GatewayClass", "gatewayclasses", "Cluster",
		map[string]any{"name": "v1", "served": true, "storage": true})
	gw := crdObj("gateway.networking.k8s.io", "Gateway", "gateways", "Namespaced",
		map[string]any{"name": "v1", "served": true, "storage": true})
	none := crdObj("example.com", "Nothing", "nothings", "Namespaced",
		map[string]any{"name": "v1", "served": false, "storage": true})
	broken := crdObj("example.com", "Broken", "brokens", "Namespaced",
		map[string]any{"name": "v1", "served": true, "storage": true})
	unstructured.SetNestedSlice(broken.Object, []any{map[string]any{"type": "Established", "status": "False"}}, "status", "conditions")

	got := CustomResources([]unstructured.Unstructured{cert, prom, gc, gw, none, broken})
	byKind := map[string]Resource{}
	for _, r := range got {
		byKind[r.Kind] = r
	}
	if len(got) != 4 {
		t.Fatalf("got %d resources, want 4: %+v", len(got), got)
	}
	c := byKind["Certificate"]
	if c.Version != "v1" || !c.Namespaced || c.Category != CatCustom || c.Title != "Certificates" || c.ID() != "certificates.cert-manager.io" {
		t.Errorf("certificate: %+v", c)
	}
	if !slices.Equal(c.Aliases, []string{"cert", "certs", "certificate"}) {
		t.Errorf("certificate aliases: %v", c.Aliases)
	}
	if len(c.Printer) != 2 || c.Printer[1].Priority != 1 {
		t.Errorf("certificate printer columns: %+v", c.Printer)
	}
	if p := byKind["Prometheus"]; p.Version != "v1" || p.Title != "Prometheuses" {
		t.Errorf("prometheus: %+v", p)
	}
	if g := byKind["GatewayClass"]; g.Namespaced || g.Category != CatCluster {
		t.Errorf("gatewayclass, a cluster-scoped kind of a mapped group: %+v", g)
	}
	if g := byKind["Gateway"]; g.Category != CatNetwork {
		t.Errorf("gateway: %+v", g)
	}
}

func TestGroupCategory(t *testing.T) {
	for group, want := range map[string]string{
		"cilium.io":               CatNetwork,
		"linkerd.io":              CatNetwork,
		"policy.linkerd.io":       CatNetwork,
		"multicluster.linkerd.io": CatNetwork,
		"keda.sh":                 CatWorkloads,
		"notlinkerd.io":           "",
		"cert-manager.io":         "",
	} {
		if got, _ := groupCategory(group); got != want {
			t.Errorf("groupCategory(%s) = %q, want %q", group, got, want)
		}
	}
}

func TestPluralTitle(t *testing.T) {
	for _, tt := range [][3]string{
		{"Certificate", "certificates", "Certificates"},
		{"NetworkPolicy", "networkpolicies", "NetworkPolicies"},
		{"Prometheus", "prometheuses", "Prometheuses"},
		{"Foo", "bars", "Bars"},
	} {
		if got := pluralTitle(tt[0], tt[1]); got != tt[2] {
			t.Errorf("pluralTitle(%s, %s) = %s, want %s", tt[0], tt[1], got, tt[2])
		}
	}
}

func TestPrinterColumns(t *testing.T) {
	now := time.Now()
	cols := printerColumns([]PrinterColumn{
		{Name: "Ready", Type: "string", JSONPath: `.status.conditions[?(@.type=="Ready")].status`},
		{Name: "Replicas", Type: "integer", JSONPath: ".spec.replicas"},
		{Name: "Renewal", Type: "date", JSONPath: ".status.renewalTime", Priority: 1},
		{Name: "Hosts", Type: "string", JSONPath: ".spec.hosts"},
	})
	var names []string
	for _, c := range cols {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, []string{"NAME", "READY", "REPLICAS", "RENEWAL", "HOSTS", "AGE"}) {
		t.Fatalf("columns: %v", names)
	}
	if cols[3].Drop != 1 || cols[1].Drop != 0 {
		t.Errorf("priority should make a column hideable: %+v", cols[3])
	}
	obj := func(replicas int64, renewal time.Time, ready string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"replicas": replicas, "hosts": []any{"a.example.com", "b.example.com"}},
			"status": map[string]any{
				"renewalTime": renewal.UTC().Format(time.RFC3339),
				"conditions": []any{
					map[string]any{"type": "Issuing", "status": "False"},
					map[string]any{"type": "Ready", "status": ready},
				},
			},
		}}
		return u
	}
	a := obj(10, now.Add(-3*24*time.Hour), "True")
	b := obj(9, now.Add(-time.Hour), "False")
	if got := cols[1].Value(a); got != "True" {
		t.Errorf("filter JSONPath: %q", got)
	}
	if got := cols[2].Value(a); got != "10" {
		t.Errorf("integer: %q", got)
	}
	if ka, kb := cols[2].Sort(a).(float64), cols[2].Sort(b).(float64); !(kb < ka) {
		t.Errorf("integers sort as numbers: 9 → %v, 10 → %v", kb, ka)
	}
	if got := cols[3].Value(a); got != "3d" {
		t.Errorf("date as age: %q", got)
	}
	if ka, kb := cols[3].Sort(a).(float64), cols[3].Sort(b).(float64); !(kb < ka) {
		t.Errorf("dates sort youngest first like AGE")
	}
	if got := cols[4].Value(a); got != `["a.example.com","b.example.com"]` {
		t.Errorf("list value: %q", got)
	}
	if got := cols[1].Value(&unstructured.Unstructured{Object: map[string]any{}}); got != "" {
		t.Errorf("missing value: %q", got)
	}

	withAge := printerColumns([]PrinterColumn{{Name: "Age", Type: "date", JSONPath: ".metadata.creationTimestamp"}})
	if len(withAge) != 2 || withAge[1].Name != "AGE" {
		t.Errorf("a printer column of the creation time replaces AGE: %v", withAge)
	}
	if got := printerColumns(nil); len(got) != 2 || got[0].Name != "NAME" || got[1].Name != "AGE" {
		t.Errorf("no printer columns: %v", got)
	}
}

func TestRegistryLookup(t *testing.T) {
	custom := []Resource{
		{Name: "certificates", Kind: "Certificate", Group: "cert-manager.io", Custom: true, Aliases: []string{"cert", "certificate"}},
		{Name: "certificates", Kind: "Certificate", Group: "example.com", Custom: true, Aliases: []string{"certificate"}},
		{Name: "services", Kind: "Service", Group: "serving.knative.dev", Custom: true, Aliases: []string{"ksvc", "service"}},
	}
	reg := NewRegistry(custom, nil)
	for _, tt := range []struct{ name, want string }{
		{"services", "services"}, // the builtin wins
		{"svc", "services"},
		{"ksvc", "services.serving.knative.dev"},
		{"services.serving.knative.dev", "services.serving.knative.dev"},
		{"cert", "certificates.cert-manager.io"},
		{"Certificates.Cert-Manager.io", "certificates.cert-manager.io"},
		{"certificates.example.com", "certificates.example.com"},
		{"certificates", ""}, // two groups have it
		{"certificate", ""},
	} {
		r, ok := reg.Lookup(tt.name)
		if got := map[bool]string{true: r.ID(), false: ""}[ok]; got != tt.want {
			t.Errorf("Lookup(%s) = %q, want %q", tt.name, got, tt.want)
		}
	}
	var none *Registry
	if _, ok := none.Lookup("pods"); !ok {
		t.Error("a nil registry still has the builtins")
	}
	if r, ok := reg.ForKind("cert-manager.io/v1", "Certificate"); !ok || r.Group != "cert-manager.io" {
		t.Errorf("ForKind: %+v", r)
	}
	if r, ok := reg.ForKind("v1", "Service"); !ok || r.Custom {
		t.Errorf("ForKind core service: %+v", r)
	}
}

func TestDemoCustomResources(t *testing.T) {
	s := NewStore(NewDemoProvider(nil), time.Second)
	if e := s.Fetch(CRDKey("demo-dev")); e.Err != nil {
		t.Fatal(e.Err)
	}
	reg := s.Registry("demo-dev")
	if len(reg.Custom()) == 0 {
		t.Fatal("no custom resources in demo-dev")
	}
	for _, r := range reg.Custom() {
		e := s.Fetch(Key{Context: "demo-dev", GVR: r.GVR()})
		if e.Err != nil || len(e.Items) == 0 {
			t.Errorf("%s: %d items, %v", r.ID(), len(e.Items), e.Err)
		}
	}
	if s.Registry("demo-dev") != reg {
		t.Error("the registry is rebuilt without a new CRD list")
	}
	var groups []string
	for _, g := range reg.Groups(true) {
		groups = append(groups, g.Name)
	}
	if !slices.Equal(groups, []string{"cert-manager.io", "monitoring.coreos.com"}) {
		t.Errorf("namespaced groups: %v", groups)
	}
	var network []string
	for _, r := range reg.InCategory(CatNetwork) {
		network = append(network, r.Title)
	}
	if !slices.Equal(network, []string{"Services", "Ingresses", "NetworkPolicies", "CiliumNetworkPolicies", "Gateways", "HTTPRoutes", "Servers", "ServiceProfiles"}) {
		t.Errorf("network: %v", network)
	}

	// The secret cert-manager issued is owned by its certificate.
	sec := find(t, demoList(t, s, "secrets", "shop"), "shop-tls")
	rels, err := Relations(s.Lister("demo-dev", time.Minute), reg, sec)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(rels, "Owners"); !slices.Equal(got, []string{"Certificate/shop-tls"}) {
		t.Errorf("owners of shop-tls: %v", got)
	}
}

func TestCRDListOnDisk(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(NewDemoProvider(nil), time.Second)
	s.SetCacheDir(dir)
	if e := s.Cached(CRDKey("demo-dev"), CRDMaxAge); e.Err != nil {
		t.Fatal(e.Err)
	}
	path := filepath.Join(CacheDir(dir, "demo-dev"), "crds.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatal("not saved:", err)
	}

	// A new store reads it instead of listing.
	var log strings.Builder
	s2 := NewStore(NewDemoProvider(NewTracer(&log)), time.Second)
	s2.SetCacheDir(dir)
	e := s2.Cached(CRDKey("demo-dev"), CRDMaxAge)
	if len(e.Items) == 0 || log.Len() != 0 {
		t.Errorf("from disk: %d items, requests: %s", len(e.Items), log.String())
	}
	if len(s2.Registry("demo-dev").Custom()) == 0 {
		t.Error("no custom resources from the disk cache")
	}

	// Too old: listed again.
	old := time.Now().Add(-2 * CRDMaxAge)
	os.Chtimes(path, old, old)
	s3 := NewStore(NewDemoProvider(NewTracer(&log)), time.Second)
	s3.SetCacheDir(dir)
	s3.Cached(CRDKey("demo-dev"), CRDMaxAge)
	if !strings.Contains(log.String(), "LIST customresourcedefinitions") {
		t.Errorf("a stale disk cache was used: %s", log.String())
	}
}

func TestCacheDir(t *testing.T) {
	a := CacheDir("/c", "arn:aws:eks:eu-west-1:1234:cluster/prod")
	b := CacheDir("/c", "arn_aws_eks_eu-west-1_1234_cluster_prod")
	if filepath.Dir(a) != "/c" || a == b {
		t.Errorf("CacheDir: %s, %s", a, b)
	}
	if got := CacheDir("/c", "kind-dev"); got != "/c/kind-dev" {
		t.Errorf("plain name: %s", got)
	}
}
