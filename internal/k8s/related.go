package k8s

import (
	"errors"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

// Relation is one section of an object's related summary, e.g. its owners
// or the pods it selects.
type Relation struct {
	Title string
	Items []Related
}

// Related is an object related to the described one. Obj is nil when the
// object is referenced but does not exist; Kind and Name then say what was
// referenced.
type Related struct {
	Res        Resource
	Obj        *unstructured.Unstructured
	Kind, Name string
	Note       string // how it is related, e.g. "volume", "envFrom"
}

// Lister lists a resource in a namespace ("" = all namespaces or cluster
// scoped).
type Lister func(res Resource, ns string) ([]unstructured.Unstructured, error)

// Lister returns a Lister for a context that serves lists from the cache
// when they are younger than maxAge, and fetches them otherwise.
func (s *Store) Lister(ctx string, maxAge time.Duration) Lister {
	return func(res Resource, ns string) ([]unstructured.Unstructured, error) {
		if !res.Namespaced {
			ns = ""
		}
		k := Key{Context: ctx, GVR: res.GVR(), Namespace: ns}
		e, ok := s.Get(k)
		if !ok || time.Since(e.FetchedAt) > maxAge {
			e = s.Fetch(k)
		}
		return e.Items, e.Err
	}
}

// relator collects the sections of one object, remembering the first list
// error. Sections without items are left out.
type relator struct {
	list Lister
	obj  *unstructured.Unstructured
	ns   string
	out  []Relation
	err  error
}

func (r *relator) items(name, ns string) []unstructured.Unstructured {
	items, err := r.list(MustLookup(name), ns)
	if err != nil && r.err == nil {
		r.err = err
	}
	return items
}

func (r *relator) add(title string, items []Related) {
	if len(items) > 0 {
		r.out = append(r.out, Relation{Title: title, Items: items})
	}
}

func related(res Resource, obj *unstructured.Unstructured, note string) Related {
	return Related{Res: res, Obj: obj, Kind: obj.GetKind(), Name: obj.GetName(), Note: note}
}

// Relations computes the related summary of obj: its owners, what it owns or
// selects, what selects it, what it uses and what uses it. Lists that fail
// are skipped; the first error is returned with the sections found.
func Relations(list Lister, obj *unstructured.Unstructured) ([]Relation, error) {
	r := &relator{list: list, obj: obj, ns: obj.GetNamespace()}
	r.add("Owners", r.owners())
	switch obj.GetKind() {
	case "Pod":
		if node := str(obj, "spec", "nodeName"); node != "" {
			r.add("Node", r.byName("nodes", "", []string{node}, ""))
		}
		r.add("Services", r.selectingServices(obj.GetLabels()))
		r.add("Network policies", r.selectingPolicies(obj.GetLabels()))
		r.add("Uses", r.uses())
	case "Deployment":
		r.add("ReplicaSets", r.owned("replicasets"))
		r.addWorkload()
	case "StatefulSet", "DaemonSet", "ReplicaSet", "Job":
		r.addWorkload()
	case "CronJob":
		r.add("Jobs", r.owned("jobs"))
		r.add("Uses", r.uses())
	case "Service":
		if sel, ok, _ := unstructured.NestedStringMap(obj.Object, "spec", "selector"); ok && len(sel) > 0 {
			r.add("Pods", r.pods(labels.SelectorFromSet(sel), r.ns))
		}
		r.add("Ingresses", r.ingressesTo(obj.GetName()))
	case "Ingress":
		r.add("Services", r.byName("services", r.ns, ingressServices(obj), "backend"))
		r.add("Uses", r.byName("secrets", r.ns, ingressSecrets(obj), "tls"))
	case "NetworkPolicy":
		if sel, err := labelSelector(obj, "spec", "podSelector"); err == nil {
			r.add("Pods", r.pods(sel, r.ns))
		}
	case "ConfigMap", "Secret", "PersistentVolumeClaim", "ServiceAccount":
		r.add("Used by", r.usedBy())
		if obj.GetKind() == "PersistentVolumeClaim" {
			if v := str(obj, "spec", "volumeName"); v != "" {
				r.add("Volume", r.byName("persistentvolumes", "", []string{v}, ""))
			}
		}
	case "PersistentVolume":
		if ns := str(obj, "spec", "claimRef", "namespace"); ns != "" {
			r.add("Claim", r.byName("persistentvolumeclaims", ns, []string{str(obj, "spec", "claimRef", "name")}, ""))
		}
	case "Node":
		var out []Related
		items := r.items("pods", "")
		for i := range items {
			if str(&items[i], "spec", "nodeName") == obj.GetName() {
				out = append(out, related(MustLookup("pods"), &items[i], items[i].GetNamespace()+" · "+Summary(&items[i])))
			}
		}
		r.add("Pods", out)
	case "HorizontalPodAutoscaler":
		if res, ok := Lookup(str(obj, "spec", "scaleTargetRef", "kind")); ok {
			r.add("Target", r.byName(res.Name, r.ns, []string{str(obj, "spec", "scaleTargetRef", "name")}, ""))
		}
	case "RoleBinding", "ClusterRoleBinding":
		if res, ok := Lookup(str(obj, "roleRef", "kind")); ok {
			r.add("Role", r.byName(res.Name, r.ns, []string{str(obj, "roleRef", "name")}, ""))
		}
	case "Event":
		if res, ok := Lookup(str(obj, "involvedObject", "kind")); ok {
			r.add("Object", r.byName(res.Name, str(obj, "involvedObject", "namespace"), []string{str(obj, "involvedObject", "name")}, ""))
		}
	}
	return r.out, r.err
}

// addWorkload adds the sections of an object with a pod template.
func (r *relator) addWorkload() {
	if sel, err := labelSelector(r.obj, "spec", "selector"); err == nil {
		r.add("Pods", r.pods(sel, r.ns))
	}
	tmpl, _, _ := unstructured.NestedStringMap(r.obj.Object, "spec", "template", "metadata", "labels")
	r.add("Services", r.selectingServices(tmpl))
	r.add("Network policies", r.selectingPolicies(tmpl))
	r.add("Autoscalers", r.autoscalers())
	r.add("Uses", r.uses())
}

// owners follows the controller ownerReferences up: the direct owner first.
func (r *relator) owners() []Related {
	var out []Related
	cur := r.obj
	for range 5 { // owner chains are short; this also guards against cycles
		ref := controllerRef(cur)
		if ref == nil {
			break
		}
		res, ok := Lookup(ref.Kind)
		if !ok {
			out = append(out, Related{Kind: ref.Kind, Name: ref.Name, Note: "unknown kind"})
			break
		}
		var owner *unstructured.Unstructured
		items := r.items(res.Name, cur.GetNamespace())
		for i := range items {
			if items[i].GetUID() == ref.UID {
				owner = &items[i]
			}
		}
		if owner == nil {
			out = append(out, Related{Res: res, Kind: ref.Kind, Name: ref.Name, Note: "not found"})
			break
		}
		out = append(out, related(res, owner, ""))
		cur = owner
	}
	return out
}

func controllerRef(obj *unstructured.Unstructured) *metav1.OwnerReference {
	refs := obj.GetOwnerReferences()
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	if len(refs) > 0 {
		return &refs[0]
	}
	return nil
}

// owned lists the objects of a resource that obj controls.
func (r *relator) owned(name string) []Related {
	var out []Related
	items := r.items(name, r.ns)
	for i := range items {
		if ref := controllerRef(&items[i]); ref != nil && ref.UID == r.obj.GetUID() {
			out = append(out, related(MustLookup(name), &items[i], Summary(&items[i])))
		}
	}
	return out
}

func (r *relator) pods(sel labels.Selector, ns string) []Related {
	var out []Related
	items := r.items("pods", ns)
	for i := range items {
		if sel.Matches(labels.Set(items[i].GetLabels())) {
			out = append(out, related(MustLookup("pods"), &items[i], Summary(&items[i])))
		}
	}
	return out
}

// selectingServices lists the services whose selector matches lbls.
func (r *relator) selectingServices(lbls map[string]string) []Related {
	if len(lbls) == 0 {
		return nil
	}
	var out []Related
	items := r.items("services", r.ns)
	for i := range items {
		sel, ok, _ := unstructured.NestedStringMap(items[i].Object, "spec", "selector")
		if ok && len(sel) > 0 && labels.SelectorFromSet(sel).Matches(labels.Set(lbls)) {
			out = append(out, related(MustLookup("services"), &items[i], servicePorts(&items[i])))
		}
	}
	return out
}

// selectingPolicies lists the network policies whose podSelector matches
// lbls.
func (r *relator) selectingPolicies(lbls map[string]string) []Related {
	var out []Related
	items := r.items("networkpolicies", r.ns)
	for i := range items {
		sel, err := labelSelector(&items[i], "spec", "podSelector")
		if err == nil && sel.Matches(labels.Set(lbls)) {
			types, _, _ := unstructured.NestedStringSlice(items[i].Object, "spec", "policyTypes")
			out = append(out, related(MustLookup("networkpolicies"), &items[i], strings.Join(types, ",")))
		}
	}
	return out
}

func (r *relator) autoscalers() []Related {
	var out []Related
	items := r.items("horizontalpodautoscalers", r.ns)
	for i := range items {
		if str(&items[i], "spec", "scaleTargetRef", "kind") == r.obj.GetKind() &&
			str(&items[i], "spec", "scaleTargetRef", "name") == r.obj.GetName() {
			out = append(out, related(MustLookup("horizontalpodautoscalers"), &items[i], ""))
		}
	}
	return out
}

func (r *relator) ingressesTo(service string) []Related {
	var out []Related
	items := r.items("ingresses", r.ns)
	for i := range items {
		if slices.Contains(ingressServices(&items[i]), service) {
			out = append(out, related(MustLookup("ingresses"), &items[i], ""))
		}
	}
	return out
}

// byName looks up objects of a resource by name; missing ones are listed as
// not found.
func (r *relator) byName(name, ns string, names []string, note string) []Related {
	res := MustLookup(name)
	items := r.items(name, ns)
	var out []Related
	for _, n := range names {
		i := slices.IndexFunc(items, func(u unstructured.Unstructured) bool { return u.GetName() == n })
		if i < 0 {
			out = append(out, Related{Res: res, Kind: res.Kind, Name: n, Note: joinNote(note, "not found")})
			continue
		}
		out = append(out, related(res, &items[i], note))
	}
	return out
}

// uses lists the config maps, secrets, claims and service account that the
// object's pod spec refers to.
func (r *relator) uses() []Related {
	spec := PodSpec(r.obj)
	if spec == nil {
		return nil
	}
	var out []Related
	for _, ref := range podRefs(spec) {
		out = append(out, r.byName(ref.res, r.ns, []string{ref.name}, ref.via)...)
	}
	return out
}

// usedBy lists the pods and pod templates that refer to the object.
func (r *relator) usedBy() []Related {
	res, _ := Lookup(r.obj.GetKind())
	var out []Related
	for _, name := range []string{"pods", "deployments", "statefulsets", "daemonsets", "cronjobs"} {
		items := r.items(name, r.ns)
		for i := range items {
			if name == "pods" && controllerRef(&items[i]) != nil {
				continue // its owner is listed instead, or the owner's owner
			}
			spec := PodSpec(&items[i])
			if spec == nil {
				continue
			}
			var via []string
			for _, ref := range podRefs(spec) {
				if ref.res == res.Name && ref.name == r.obj.GetName() && !slices.Contains(via, ref.via) {
					via = append(via, ref.via)
				}
			}
			if len(via) > 0 {
				out = append(out, related(MustLookup(name), &items[i], strings.Join(via, ", ")))
			}
		}
	}
	// Pods owned by a ReplicaSet, Job, … are covered by their workload, but
	// a ReplicaSet without one, or a Job, would be missed: list such pods
	// when nothing else uses the object.
	if len(out) == 0 {
		items := r.items("pods", r.ns)
		for i := range items {
			for _, ref := range podRefs(PodSpec(&items[i])) {
				if ref.res == res.Name && ref.name == r.obj.GetName() {
					out = append(out, related(MustLookup("pods"), &items[i], ref.via))
					break
				}
			}
		}
	}
	if r.obj.GetKind() == "Secret" {
		items := r.items("ingresses", r.ns)
		for i := range items {
			if slices.Contains(ingressSecrets(&items[i]), r.obj.GetName()) {
				out = append(out, related(MustLookup("ingresses"), &items[i], "tls"))
			}
		}
	}
	return out
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + ", " + b
}

// PodSpec returns the pod spec of a pod, or the pod template spec of a
// workload, or nil.
func PodSpec(obj *unstructured.Unstructured) map[string]any {
	var path []string
	switch obj.GetKind() {
	case "Pod":
		path = []string{"spec"}
	case "CronJob":
		path = []string{"spec", "jobTemplate", "spec", "template", "spec"}
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job":
		path = []string{"spec", "template", "spec"}
	default:
		return nil
	}
	m, _, _ := unstructured.NestedMap(obj.Object, path...)
	return m
}

type podRef struct {
	res, name, via string
}

// podRefs lists what a pod spec refers to by name.
func podRefs(spec map[string]any) []podRef {
	if spec == nil {
		return nil
	}
	var out []podRef
	add := func(res string, name any, via string) {
		if s, ok := name.(string); ok && s != "" {
			out = append(out, podRef{res, s, via})
		}
	}
	sub := func(m any, key string) map[string]any {
		mm, _ := m.(map[string]any)
		v, _ := mm[key].(map[string]any)
		return v
	}
	list := func(m map[string]any, key string) []any {
		v, _ := m[key].([]any)
		return v
	}
	if sa, _ := spec["serviceAccountName"].(string); sa != "" {
		add("serviceaccounts", sa, "serviceAccount")
	}
	for _, s := range list(spec, "imagePullSecrets") {
		if m, ok := s.(map[string]any); ok {
			add("secrets", m["name"], "imagePullSecret")
		}
	}
	for _, v := range list(spec, "volumes") {
		add("configmaps", sub(v, "configMap")["name"], "volume")
		add("secrets", sub(v, "secret")["secretName"], "volume")
		add("persistentvolumeclaims", sub(v, "persistentVolumeClaim")["claimName"], "volume")
		if p := sub(v, "projected"); p != nil {
			for _, src := range list(p, "sources") {
				add("configmaps", sub(src, "configMap")["name"], "volume")
				add("secrets", sub(src, "secret")["name"], "volume")
			}
		}
	}
	for _, field := range []string{"initContainers", "containers", "ephemeralContainers"} {
		for _, c := range list(spec, field) {
			cm, _ := c.(map[string]any)
			for _, ef := range list(cm, "envFrom") {
				add("configmaps", sub(ef, "configMapRef")["name"], "envFrom")
				add("secrets", sub(ef, "secretRef")["name"], "envFrom")
			}
			for _, e := range list(cm, "env") {
				vf := sub(e, "valueFrom")
				add("configmaps", sub(vf, "configMapKeyRef")["name"], "env")
				add("secrets", sub(vf, "secretKeyRef")["name"], "env")
			}
		}
	}
	// One entry per object and way of use.
	slices.SortStableFunc(out, func(a, b podRef) int {
		return strings.Compare(a.res+"/"+a.name+"/"+a.via, b.res+"/"+b.name+"/"+b.via)
	})
	return slices.Compact(out)
}

func ingressServices(ing *unstructured.Unstructured) []string {
	var out []string
	if s := str(ing, "spec", "defaultBackend", "service", "name"); s != "" {
		out = append(out, s)
	}
	rules, _, _ := unstructured.NestedSlice(ing.Object, "spec", "rules")
	for _, rule := range rules {
		paths, _, _ := unstructured.NestedSlice(rule.(map[string]any), "http", "paths")
		for _, p := range paths {
			if s, _, _ := unstructured.NestedString(p.(map[string]any), "backend", "service", "name"); s != "" && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

func ingressSecrets(ing *unstructured.Unstructured) []string {
	var out []string
	tls, _, _ := unstructured.NestedSlice(ing.Object, "spec", "tls")
	for _, t := range tls {
		if s, _, _ := unstructured.NestedString(t.(map[string]any), "secretName"); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// labelSelector parses a metav1.LabelSelector at fields. A missing selector
// is an error; an empty one selects everything.
func labelSelector(obj *unstructured.Unstructured, fields ...string) (labels.Selector, error) {
	m, ok, err := unstructured.NestedMap(obj.Object, fields...)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("no selector")
	}
	var ls metav1.LabelSelector
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(m, &ls); err != nil {
		return nil, err
	}
	return metav1.LabelSelectorAsSelector(&ls)
}

// Summary is a short state of an object for lists of related objects: its
// STATUS or READY column.
func Summary(obj *unstructured.Unstructured) string {
	res, ok := Lookup(obj.GetKind())
	if !ok {
		return ""
	}
	for _, name := range []string{"STATUS", "READY"} {
		for _, c := range Columns(res) {
			if c.Name != name {
				continue
			}
			v := c.Value(obj)
			if name == "READY" && !strings.Contains(v, "/") {
				v = "ready " + v // a bare count, e.g. of a ReplicaSet
			}
			return v
		}
	}
	return ""
}
