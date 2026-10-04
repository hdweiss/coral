package ui

import (
	"slices"
	"testing"

	"github.com/hdweiss/coral/internal/config"
	"github.com/hdweiss/coral/internal/k8s"
)

func navChild(n *navNode, label string) *navNode {
	for _, c := range n.children {
		if c.label == label {
			return c
		}
	}
	return nil
}

func labels(n *navNode) []string {
	var out []string
	for _, c := range n.children {
		out = append(out, c.label)
	}
	return out
}

func TestNavCustomResources(t *testing.T) {
	v := newTestNav(t)
	root := v.root("demo-dev")
	if navChild(navChild(root, "Cluster"), k8s.CatCustom) != nil {
		t.Fatal("a Custom Resources folder before the CRDs are listed")
	}
	v.SetNamespaces("demo-dev", []string{"shop"}, nil)
	v.store.Fetch(k8s.CRDKey("demo-dev"))
	v.SetCustom("demo-dev")

	shop := navChild(root, "shop")
	want := []string{"Workloads", "Network", "Config", "Storage", "Access Control", k8s.CatCustom, "Events"}
	if got := labels(shop); !slices.Equal(got, want) {
		t.Errorf("shop: %v, want %v", got, want)
	}
	if got := labels(navChild(shop, "Network")); !slices.Equal(got, []string{"Services", "Ingresses", "NetworkPolicies", "CiliumNetworkPolicies", "Gateways", "HTTPRoutes", "Servers", "ServiceProfiles"}) {
		t.Errorf("network: %v", got)
	}
	custom := navChild(shop, k8s.CatCustom)
	if got := labels(custom); !slices.Equal(got, []string{"cert-manager.io", "monitoring.coreos.com"}) {
		t.Errorf("groups: %v", got)
	}
	if got := labels(navChild(custom, "cert-manager.io")); !slices.Equal(got, []string{"Certificates", "Issuers"}) {
		t.Errorf("cert-manager kinds: %v", got)
	}
	cluster := navChild(root, "Cluster")
	if navChild(cluster, "GatewayClasses") == nil {
		t.Errorf("cluster: %v", labels(cluster))
	}
	if got := labels(navChild(navChild(cluster, k8s.CatCustom), "cert-manager.io")); !slices.Equal(got, []string{"ClusterIssuers"}) {
		t.Errorf("cluster cert-manager kinds: %v", got)
	}

	// Fold state and the cursor survive a refresh of the CRDs.
	root.expanded, shop.expanded, custom.expanded = true, true, true
	navChild(custom, "cert-manager.io").expanded = true
	v.refresh()
	certs := navChild(navChild(custom, "cert-manager.io"), "Certificates")
	v.cursor = slices.Index(v.lines, certs)
	v.store.Fetch(k8s.CRDKey("demo-dev"))
	v.SetCustom("demo-dev")
	cur := v.lines[v.cursor]
	if cur == certs || cur.label != "Certificates" || cur.parent.label != "cert-manager.io" {
		t.Errorf("cursor on %q after the rebuild", cur.label)
	}
	if !navChild(shop, k8s.CatCustom).expanded || !navChild(navChild(shop, k8s.CatCustom), "cert-manager.io").expanded {
		t.Error("fold state lost")
	}
}

func TestNavCustomResourcePinsWaitForCRDs(t *testing.T) {
	v := newTestNav(t)
	certs := config.Pin{Context: "demo-dev", Namespace: "shop", Resource: "certificates.cert-manager.io"}
	bogus := config.Pin{Context: "demo-dev", Resource: "nothings.example.com"}
	v.SetPins([]config.Pin{certs, bogus})
	if len(v.pinned.children) != 0 {
		t.Fatalf("pins shown before the CRDs are listed: %d", len(v.pinned.children))
	}
	v.store.Fetch(k8s.CRDKey("demo-dev"))
	v.SetCustom("demo-dev")
	n := pinNode(v, certs)
	if n == nil || n.kind != nkResource || n.res.Kind != "Certificate" {
		t.Fatalf("certificates pin: %+v", n)
	}
	if got := pinFor(n); got != certs {
		t.Errorf("pinFor = %+v", got)
	}
	if b := pinNode(v, bogus); b == nil || !b.err {
		t.Errorf("unknown custom resource pin: %+v", b)
	}
}

func TestCustomPaletteItems(t *testing.T) {
	reg := k8s.NewRegistry([]k8s.Resource{
		{Name: "certificates", Kind: "Certificate", Title: "Certificates", Group: "cert-manager.io", Custom: true, Aliases: []string{"cert", "certificate"}},
		{Name: "services", Kind: "Service", Title: "Services", Group: "serving.knative.dev", Custom: true, Aliases: []string{"ksvc", "service"}},
	}, nil)
	items := customPaletteItems(reg)
	if len(items) != 2 {
		t.Fatalf("items: %+v", items)
	}
	if it := items[0]; it.cmd != "certificates" || !slices.Contains(it.aliases, "cert") || !slices.Contains(it.aliases, "certificates.cert-manager.io") ||
		it.desc != "Certificates · cert-manager.io · cert" {
		t.Errorf("certificates: %+v", it)
	}
	// services is the builtin's: the custom one goes by plural.group.
	if it := items[1]; it.cmd != "services.serving.knative.dev" || slices.Contains(it.aliases, "service") || !slices.Contains(it.aliases, "ksvc") {
		t.Errorf("knative services: %+v", it)
	}
}
