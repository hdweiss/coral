package k8s

import (
	"maps"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

// Relations of well-known custom resources: cert-manager, Cilium, Linkerd
// and the Gateway API. Each is only looked for when the context has its CRD,
// so clusters without them list nothing more.
const (
	idCertificate    = "certificates.cert-manager.io"
	idIssuer         = "issuers.cert-manager.io"
	idClusterIssuer  = "clusterissuers.cert-manager.io"
	idCNP            = "ciliumnetworkpolicies.cilium.io"
	idCCNP           = "ciliumclusterwidenetworkpolicies.cilium.io"
	idServer         = "servers.policy.linkerd.io"
	idServiceProfile = "serviceprofiles.linkerd.io"
	idGateway        = "gateways.gateway.networking.k8s.io"
	idHTTPRoute      = "httproutes.gateway.networking.k8s.io"
)

// custom returns the context's custom resource id, if it has the CRD.
func (r *relator) custom(id string) (Resource, bool) {
	res, ok := r.reg.Lookup(id)
	return res, ok && res.Custom
}

// customItems lists custom resource id in ns; nothing without its CRD.
func (r *relator) customItems(id, ns string) (Resource, []unstructured.Unstructured) {
	res, ok := r.custom(id)
	if !ok {
		return Resource{}, nil
	}
	return res, r.itemsOf(res, ns)
}

// listed reports whether an earlier section has obj already, so that e.g. a
// certificate that owns its secret isn't listed again as its certificate.
func (r *relator) listed(obj *unstructured.Unstructured) bool {
	for _, rel := range r.out {
		for _, it := range rel.Items {
			if it.Obj != nil && it.Obj.GetUID() == obj.GetUID() {
				return true
			}
		}
	}
	return false
}

// customRelations adds the sections of a custom object.
func (r *relator) customRelations(res Resource) {
	obj := r.obj
	switch res.ID() {
	case idCertificate:
		r.add("Issuer", r.certIssuer())
		if s := str(obj, "spec", "secretName"); s != "" {
			r.add("Secret", r.byName("secrets", r.ns, []string{s}, ""))
		}
	case idIssuer, idClusterIssuer:
		r.add("Certificates", r.certificates(func(c *unstructured.Unstructured) bool {
			kind := str(c, "spec", "issuerRef", "kind")
			if kind == "" {
				kind = "Issuer"
			}
			return kind == obj.GetKind() && str(c, "spec", "issuerRef", "name") == obj.GetName() &&
				(kind == "ClusterIssuer" || c.GetNamespace() == obj.GetNamespace())
		}))
	case idCNP, idCCNP:
		var out []Related
		pods := r.items("pods", r.ns) // all namespaces for the clusterwide kind
		for _, sel := range ciliumSelectors(obj) {
			for i := range pods {
				if sel.Matches(labels.Set(ciliumLabels(&pods[i], PodSpec(&pods[i])))) && !slices.ContainsFunc(out, sameObj(&pods[i])) {
					out = append(out, related(MustLookup("pods"), &pods[i], podNote(&pods[i], r.ns == "")))
				}
			}
		}
		r.add("Pods", out)
	case idServer:
		if sel, err := labelSelector(obj, "spec", "podSelector"); err == nil {
			r.add("Pods", r.pods(sel, r.ns))
		}
	case idServiceProfile:
		if svc, ns, ok := profileService(obj.GetName()); ok {
			r.add("Service", r.byName("services", ns, []string{svc}, ""))
		}
	case idGateway:
		secrets := gatewaySecrets(obj)
		r.add("Uses", r.byName("secrets", r.ns, secrets, "tls"))
		r.add("Certificates", r.certificates(func(c *unstructured.Unstructured) bool {
			return c.GetNamespace() == r.ns && slices.Contains(secrets, str(c, "spec", "secretName"))
		}))
		if route, ok := r.custom(idHTTPRoute); ok {
			var out []Related
			items := r.itemsOf(route, "")
			for i := range items {
				if slices.Contains(routeParents(&items[i], r.ns), obj.GetName()) {
					note := "" // routes from other namespaces say where
					if items[i].GetNamespace() != r.ns {
						note = items[i].GetNamespace()
					}
					out = append(out, related(route, &items[i], note))
				}
			}
			r.add("Routes", out)
		}
	case idHTTPRoute:
		if gw, ok := r.custom(idGateway); ok {
			byNS := groupByNamespace(routeParentRefs(obj))
			var out []Related
			for _, ns := range slices.Sorted(maps.Keys(byNS)) {
				out = append(out, r.byRes(gw, ns, byNS[ns], "")...)
			}
			r.add("Gateways", out)
		}
		r.add("Services", r.byName("services", r.ns, routeBackends(obj), "backend"))
	}
}

func sameObj(obj *unstructured.Unstructured) func(Related) bool {
	return func(rel Related) bool { return rel.Obj != nil && rel.Obj.GetUID() == obj.GetUID() }
}

func podNote(pod *unstructured.Unstructured, withNamespace bool) string {
	if withNamespace {
		return pod.GetNamespace() + " · " + Summary(pod)
	}
	return Summary(pod)
}

// customReverse adds the sections of custom objects that refer to a builtin
// object: the certificate of a secret, an ingress's certificates and a
// service's profile. Pods and workloads get theirs (Cilium policies, Linkerd
// servers) from addPodSelectors, next to the network policies.
func (r *relator) customReverse() {
	obj := r.obj
	switch obj.GetKind() {
	case "Secret":
		r.add("Certificate", r.certificates(func(c *unstructured.Unstructured) bool {
			return c.GetNamespace() == r.ns && str(c, "spec", "secretName") == obj.GetName()
		}))
	case "Ingress":
		secrets := ingressSecrets(obj)
		r.add("Certificates", r.certificates(func(c *unstructured.Unstructured) bool {
			return c.GetNamespace() == r.ns && slices.Contains(secrets, str(c, "spec", "secretName"))
		}))
	case "Service":
		res, items := r.customItems(idServiceProfile, r.ns)
		var out []Related
		for i := range items {
			if svc, ns, ok := profileService(items[i].GetName()); ok && svc == obj.GetName() && ns == r.ns {
				out = append(out, related(res, &items[i], ""))
			}
		}
		r.add("Service profiles", out)
	}
}

// addPodSelectors adds the Cilium policies and Linkerd servers that select
// pods with these labels and pod spec in r's namespace.
func (r *relator) addPodSelectors(lbls map[string]string, spec map[string]any) {
	pod := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"namespace": r.ns}}}
	cl := labels.Set(ciliumLabels(pod, spec))
	for k, v := range lbls {
		cl[k] = v
	}
	var policies []Related
	for _, id := range []string{idCNP, idCCNP} {
		ns := r.ns
		if id == idCCNP {
			ns = ""
		}
		res, items := r.customItems(id, ns)
		for i := range items {
			for _, sel := range ciliumSelectors(&items[i]) {
				if sel.Matches(cl) {
					policies = append(policies, related(res, &items[i], ciliumDirections(&items[i])))
					break
				}
			}
		}
	}
	r.add("Cilium policies", policies)

	res, items := r.customItems(idServer, r.ns)
	var servers []Related
	for i := range items {
		if sel, err := labelSelector(&items[i], "spec", "podSelector"); err == nil && sel.Matches(labels.Set(lbls)) {
			note := "port " + str(&items[i], "spec", "port")
			if p := str(&items[i], "spec", "proxyProtocol"); p != "" {
				note += " · " + p
			}
			servers = append(servers, related(res, &items[i], note))
		}
	}
	r.add("Linkerd servers", servers)
}

// certificates lists the cert-manager certificates that match, in r's
// namespace, leaving out those listed already.
func (r *relator) certificates(match func(*unstructured.Unstructured) bool) []Related {
	ns := r.ns
	if r.obj.GetKind() == "ClusterIssuer" {
		ns = ""
	}
	res, items := r.customItems(idCertificate, ns)
	var out []Related
	for i := range items {
		if match(&items[i]) && !r.listed(&items[i]) {
			out = append(out, related(res, &items[i], certReady(&items[i])))
		}
	}
	return out
}

func certReady(c *unstructured.Unstructured) string {
	conds, _, _ := unstructured.NestedSlice(c.Object, "status", "conditions")
	for _, cond := range conds {
		if m, _ := cond.(map[string]any); m["type"] == "Ready" {
			if m["status"] == "True" {
				return "Ready"
			}
			return "Not ready"
		}
	}
	return ""
}

// certIssuer is the Issuer or ClusterIssuer a certificate names.
func (r *relator) certIssuer() []Related {
	name := str(r.obj, "spec", "issuerRef", "name")
	id, ns := idIssuer, r.ns
	if str(r.obj, "spec", "issuerRef", "kind") == "ClusterIssuer" {
		id, ns = idClusterIssuer, ""
	}
	if g := str(r.obj, "spec", "issuerRef", "group"); name == "" || g != "" && g != "cert-manager.io" {
		return nil // an external issuer
	}
	res, ok := r.custom(id)
	if !ok {
		return nil
	}
	return r.byRes(res, ns, []string{name}, "")
}

// ciliumSelectors are the endpoint selectors of a Cilium policy, from spec
// and specs, translated to plain label selectors. Selectors on reserved
// identities (host, world, …) match no pod and are left out.
func ciliumSelectors(policy *unstructured.Unstructured) []labels.Selector {
	var specs []map[string]any
	if m, ok, _ := unstructured.NestedMap(policy.Object, "spec"); ok {
		specs = append(specs, m)
	}
	list, _, _ := unstructured.NestedSlice(policy.Object, "specs")
	for _, s := range list {
		if m, ok := s.(map[string]any); ok {
			specs = append(specs, m)
		}
	}
	var out []labels.Selector
	for _, spec := range specs {
		m, ok := spec["endpointSelector"].(map[string]any)
		if !ok {
			continue // a node selector: host policies
		}
		var ls metav1.LabelSelector
		if runtime.DefaultUnstructuredConverter.FromUnstructured(m, &ls) != nil {
			continue
		}
		reserved := false
		strip := func(key string) string {
			source, rest, found := strings.Cut(key, ":")
			switch {
			case !found:
				return key
			case source == "reserved":
				reserved = true
			}
			return rest
		}
		lbls := map[string]string{}
		for k, v := range ls.MatchLabels {
			lbls[strip(k)] = v
		}
		ls.MatchLabels = lbls
		for i := range ls.MatchExpressions {
			ls.MatchExpressions[i].Key = strip(ls.MatchExpressions[i].Key)
		}
		if sel, err := metav1.LabelSelectorAsSelector(&ls); err == nil && !reserved {
			out = append(out, sel)
		}
	}
	return out
}

// ciliumLabels are the labels Cilium gives a pod's endpoint besides the
// pod's own: its namespace and service account.
func ciliumLabels(pod *unstructured.Unstructured, spec map[string]any) map[string]string {
	sa, _ := spec["serviceAccountName"].(string)
	if sa == "" {
		sa = "default"
	}
	out := map[string]string{
		"io.kubernetes.pod.namespace":         pod.GetNamespace(),
		"io.cilium.k8s.policy.serviceaccount": sa,
	}
	for k, v := range pod.GetLabels() {
		out[k] = v
	}
	return out
}

// ciliumDirections names the directions a Cilium policy has rules for.
func ciliumDirections(policy *unstructured.Unstructured) string {
	var dirs []string
	for _, d := range []string{"ingress", "egress"} {
		if _, ok, _ := unstructured.NestedSlice(policy.Object, "spec", d); ok {
			dirs = append(dirs, d)
		} else if _, ok, _ := unstructured.NestedSlice(policy.Object, "spec", d+"Deny"); ok {
			dirs = append(dirs, d+" deny")
		}
	}
	return strings.Join(dirs, ",")
}

// profileService splits a Linkerd ServiceProfile's name, the service's
// FQDN, into the service and its namespace.
func profileService(name string) (svc, ns string, ok bool) {
	parts := strings.SplitN(name, ".", 4)
	if len(parts) < 3 || parts[2] != "svc" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// gatewaySecrets are the secrets a Gateway's listeners use for TLS.
func gatewaySecrets(gw *unstructured.Unstructured) []string {
	listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	var out []string
	for _, l := range listeners {
		m, _ := l.(map[string]any)
		refs, _, _ := unstructured.NestedSlice(m, "tls", "certificateRefs")
		for _, ref := range refs {
			rm, _ := ref.(map[string]any)
			if kind, _ := rm["kind"].(string); kind != "" && kind != "Secret" {
				continue
			}
			if name, _ := rm["name"].(string); name != "" && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

type nsName struct{ ns, name string }

// routeParentRefs are the Gateways a route attaches to.
func routeParentRefs(route *unstructured.Unstructured) []nsName {
	refs, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	var out []nsName
	for _, ref := range refs {
		m, _ := ref.(map[string]any)
		if kind, _ := m["kind"].(string); kind != "" && kind != "Gateway" {
			continue
		}
		ns, _ := m["namespace"].(string)
		if ns == "" {
			ns = route.GetNamespace()
		}
		if name, _ := m["name"].(string); name != "" {
			out = append(out, nsName{ns, name})
		}
	}
	return out
}

// routeParents are the names of the Gateways in ns a route attaches to.
func routeParents(route *unstructured.Unstructured, ns string) []string {
	var out []string
	for _, p := range routeParentRefs(route) {
		if p.ns == ns {
			out = append(out, p.name)
		}
	}
	return out
}

func groupByNamespace(refs []nsName) map[string][]string {
	out := map[string][]string{}
	for _, r := range refs {
		out[r.ns] = append(out[r.ns], r.name)
	}
	return out
}

// routeBackends are the services a route sends traffic to in its namespace.
func routeBackends(route *unstructured.Unstructured) []string {
	rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "rules")
	var out []string
	for _, rule := range rules {
		m, _ := rule.(map[string]any)
		refs, _, _ := unstructured.NestedSlice(m, "backendRefs")
		for _, ref := range refs {
			rm, _ := ref.(map[string]any)
			kind, _ := rm["kind"].(string)
			ns, _ := rm["namespace"].(string)
			if (kind != "" && kind != "Service") || (ns != "" && ns != route.GetNamespace()) {
				continue
			}
			if name, _ := rm["name"].(string); name != "" && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}
