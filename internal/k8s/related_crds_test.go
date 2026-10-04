package k8s

import (
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

func TestDemoCustomRelations(t *testing.T) {
	s := NewStore(NewDemoProvider(nil), time.Second)
	s.Fetch(CRDKey("demo-dev"))
	reg := s.Registry("demo-dev")
	list := s.Lister("demo-dev", time.Minute)
	rel := func(obj *unstructured.Unstructured) []Relation {
		t.Helper()
		rels, err := Relations(list, reg, obj)
		if err != nil {
			t.Fatal(err)
		}
		return rels
	}
	custom := func(id, ns, name string) *unstructured.Unstructured {
		t.Helper()
		r, _ := reg.Lookup(id)
		return find(t, s.Fetch(Key{Context: "demo-dev", GVR: r.GVR(), Namespace: ns}).Items, name)
	}
	check := func(what string, rels []Relation, section string, want ...string) {
		t.Helper()
		if got := names(rels, section); !slices.Equal(got, want) {
			t.Errorf("%s %s: %v, want %v", what, section, got, want)
		}
	}

	pod := find(t, demoList(t, s, "pods", "shop"), "frontend-")
	rels := rel(pod)
	check("frontend pod", rels, "Cilium policies", "CiliumNetworkPolicy/frontend-ingress", "CiliumClusterwideNetworkPolicy/deny-metadata-api")
	check("frontend pod", rels, "Linkerd servers", "Server/frontend-http")
	cart := find(t, demoList(t, s, "deployments", "shop"), "cart")
	check("cart deployment", rel(cart), "Cilium policies", "CiliumNetworkPolicy/cart-from-frontend", "CiliumClusterwideNetworkPolicy/deny-metadata-api")

	cnp := custom(idCNP, "shop", "frontend-ingress")
	if got := names(rel(cnp), "Pods"); len(got) != 3 || !strings.HasPrefix(got[0], "Pod/frontend-") {
		t.Errorf("cilium policy pods: %v", got)
	}
	if got := names(rel(custom(idCCNP, "", "deny-metadata-api")), "Pods"); len(got) < 10 {
		t.Errorf("a clusterwide policy selecting every pod found %d", len(got))
	}
	if got := names(rel(custom(idServer, "shop", "cart-grpc")), "Pods"); len(got) != 2 {
		t.Errorf("server pods: %v", got)
	}

	cert := custom(idCertificate, "shop", "shop-tls")
	rels = rel(cert)
	check("certificate", rels, "Issuer", "ClusterIssuer/letsencrypt-prod")
	check("certificate", rels, "Secret", "Secret/shop-tls")
	check("cluster issuer", rel(custom(idClusterIssuer, "", "letsencrypt-prod")), "Certificates", "Certificate/shop-tls")
	// The certificate owns its secret: listed once, as the owner.
	rels = rel(find(t, demoList(t, s, "secrets", "shop"), "shop-tls"))
	check("secret", rels, "Owners", "Certificate/shop-tls")
	check("secret", rels, "Certificate")
	check("ingress", rel(find(t, demoList(t, s, "ingresses", "shop"), "shop")), "Certificates", "Certificate/shop-tls")

	check("cart service", rel(find(t, demoList(t, s, "services", "shop"), "cart")), "Service profiles", "ServiceProfile/cart.shop.svc.cluster.local")
	check("service profile", rel(custom(idServiceProfile, "shop", "cart.shop")), "Service", "Service/cart")

	gw := rel(custom(idGateway, "shop", "shop-gateway"))
	check("gateway", gw, "Uses", "Secret/shop-tls")
	check("gateway", gw, "Certificates", "Certificate/shop-tls")
	check("gateway", gw, "Routes", "HTTPRoute/shop")
	route := rel(custom(idHTTPRoute, "shop", "shop"))
	check("route", route, "Gateways", "Gateway/shop-gateway")
	check("route", route, "Services", "Service/frontend")

	// Without the CRDs nothing is looked for.
	if got := names(func() []Relation { r, _ := Relations(list, nil, pod); return r }(), "Cilium policies"); got != nil {
		t.Errorf("no registry: %v", got)
	}
}

func TestCiliumSelectors(t *testing.T) {
	policy := &unstructured.Unstructured{Object: map[string]any{"specs": []any{
		map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{
			"k8s:app": "cart", "k8s:io.kubernetes.pod.namespace": "shop"}}},
		map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"reserved:host": ""}}},
		map[string]any{"nodeSelector": map[string]any{}},
	}}}
	sels := ciliumSelectors(policy)
	if len(sels) != 1 {
		t.Fatalf("selectors: %v", sels)
	}
	pod := &unstructured.Unstructured{}
	pod.SetNamespace("shop")
	pod.SetLabels(map[string]string{"app": "cart"})
	if !sels[0].Matches(labels.Set(ciliumLabels(pod, nil))) {
		t.Error("k8s: prefixed labels and the namespace label should match")
	}
	pod.SetNamespace("other")
	if sels[0].Matches(labels.Set(ciliumLabels(pod, nil))) {
		t.Error("matched a pod in another namespace")
	}
}
