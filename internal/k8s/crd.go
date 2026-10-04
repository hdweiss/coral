package k8s

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// CRDGVR is the CustomResourceDefinition resource. Its list (all of a
// context, no selector) is what a context's custom resources come from.
var CRDGVR = MustLookup("customresourcedefinitions").GVR()

// CRDKey is the list of a context's CRDs.
func CRDKey(ctx string) Key { return Key{Context: ctx, GVR: CRDGVR} }

// CRDMaxAge is how long a context's CRD list is kept before it is listed
// again, in memory and on disk. CRDs rarely change; r in the CRDs list
// relists them sooner.
const CRDMaxAge = 10 * time.Minute

// groupCategories puts the kinds of well-known groups into the builtin
// category they fit, instead of a group folder under Custom Resources. An
// entry also covers its subdomains: linkerd.io takes policy.linkerd.io.
var groupCategories = map[string]string{
	"gateway.networking.k8s.io": CatNetwork,
	"networking.istio.io":       CatNetwork,
	"traefik.io":                CatNetwork,
	"cilium.io":                 CatNetwork,
	"linkerd.io":                CatNetwork,
	"snapshot.storage.k8s.io":   CatStorage,
	"keda.sh":                   CatWorkloads,
	"external-secrets.io":       CatConfig,
}

// groupCategory looks up a group in groupCategories, then its parent
// domains.
func groupCategory(group string) (string, bool) {
	for g := group; g != ""; {
		if cat, ok := groupCategories[g]; ok {
			return cat, true
		}
		_, g, _ = strings.Cut(g, ".")
	}
	return "", false
}

// PrinterColumn is one of a CRD version's additionalPrinterColumns.
type PrinterColumn struct {
	Name, Type, Format, Description, JSONPath string
	Priority                                  int64
}

// CustomResources parses CRD objects into resources, sorted by group and
// title. CRDs without a served version are left out, and so are those whose
// Established condition is False.
func CustomResources(crds []unstructured.Unstructured) []Resource {
	var out []Resource
	for i := range crds {
		if r, ok := customResource(&crds[i]); ok {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Resource) int {
		return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Title, b.Title))
	})
	return out
}

func customResource(crd *unstructured.Unstructured) (Resource, bool) {
	conds, _, _ := unstructured.NestedSlice(crd.Object, "status", "conditions")
	for _, c := range conds {
		if m, _ := c.(map[string]any); m["type"] == "Established" && m["status"] == "False" {
			return Resource{}, false
		}
	}
	version, ok := servedVersion(crd)
	if !ok {
		return Resource{}, false
	}
	r := Resource{
		Name:       str(crd, "spec", "names", "plural"),
		Kind:       str(crd, "spec", "names", "kind"),
		Group:      str(crd, "spec", "group"),
		Version:    str(version, "name"),
		Namespaced: str(crd, "spec", "scope") != "Cluster",
		Custom:     true,
	}
	if r.Name == "" || r.Kind == "" || r.Version == "" {
		return Resource{}, false
	}
	r.Title = pluralTitle(r.Kind, r.Name)
	short, _, _ := unstructured.NestedStringSlice(crd.Object, "spec", "names", "shortNames")
	r.Aliases = append(r.Aliases, short...)
	if s := str(crd, "spec", "names", "singular"); s != "" {
		r.Aliases = append(r.Aliases, s)
	}
	if k := strings.ToLower(r.Kind); !slices.Contains(r.Aliases, k) {
		r.Aliases = append(r.Aliases, k)
	}
	r.Category = CatCustom
	if cat, ok := groupCategory(r.Group); ok {
		r.Category = cat
		if !r.Namespaced {
			r.Category = CatCluster
		}
	}
	cols, _, _ := unstructured.NestedSlice(version.Object, "additionalPrinterColumns")
	for _, c := range cols {
		m, _ := c.(map[string]any)
		u := &unstructured.Unstructured{Object: m}
		r.Printer = append(r.Printer, PrinterColumn{
			Name: str(u, "name"), Type: str(u, "type"), Format: str(u, "format"),
			Description: str(u, "description"), JSONPath: str(u, "jsonPath"), Priority: num(u, "priority"),
		})
	}
	return r, true
}

// servedVersion is the version coral lists: the storage version if it is
// served, else the first served one.
func servedVersion(crd *unstructured.Unstructured) (*unstructured.Unstructured, bool) {
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	var first *unstructured.Unstructured
	for _, v := range versions {
		m, _ := v.(map[string]any)
		u := &unstructured.Unstructured{Object: m}
		if served, _ := m["served"].(bool); !served {
			continue
		}
		if storage, _ := m["storage"].(bool); storage {
			return u, true
		}
		if first == nil {
			first = u
		}
	}
	return first, first != nil
}

// pluralTitle spells the plural with the kind's casing: Certificate +
// certificates → Certificates, NetworkPolicy + networkpolicies →
// NetworkPolicies.
func pluralTitle(kind, plural string) string {
	lk := strings.ToLower(kind)
	n := 0
	for n < len(lk) && n < len(plural) && lk[n] == plural[n] {
		n++
	}
	title := kind[:n] + plural[n:]
	if title != "" {
		title = strings.ToUpper(title[:1]) + title[1:]
	}
	return title
}

// CRDVersions lists a CRD's served versions, the storage version first.
func CRDVersions(crd *unstructured.Unstructured) []string {
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	var out []string
	for _, v := range versions {
		m, _ := v.(map[string]any)
		name, _ := m["name"].(string)
		if served, _ := m["served"].(bool); !served {
			continue
		}
		if storage, _ := m["storage"].(bool); storage {
			out = append([]string{name}, out...)
		} else {
			out = append(out, name)
		}
	}
	return out
}

// Registry is the resources of one context: the builtins and its custom
// resources. A nil Registry has only the builtins.
type Registry struct {
	custom []Resource
	names  map[string]Resource // custom resources by unambiguous name
	err    error               // listing the CRDs failed
}

// NewRegistry indexes custom resources for Lookup. A custom resource is
// found by plural.group, and by its plural, short names, singular and kind
// unless a builtin or another custom resource has the same name.
func NewRegistry(custom []Resource, err error) *Registry {
	r := &Registry{custom: custom, names: map[string]Resource{}, err: err}
	count := map[string]int{}
	names := func(c Resource) []string {
		return append([]string{c.Name}, c.Aliases...)
	}
	for _, c := range custom {
		for _, n := range uniq(names(c)) {
			count[strings.ToLower(n)]++
		}
	}
	for _, c := range custom {
		r.names[strings.ToLower(c.ID())] = c
		for _, n := range names(c) {
			n = strings.ToLower(n)
			if _, builtin := lookup[n]; !builtin && count[n] == 1 {
				r.names[n] = c
			}
		}
	}
	return r
}

func uniq(s []string) []string {
	s = slices.Clone(s)
	for i := range s {
		s[i] = strings.ToLower(s[i])
	}
	slices.Sort(s)
	return slices.Compact(s)
}

// Err is the error of listing the CRDs, if it failed.
func (r *Registry) Err() error {
	if r == nil {
		return nil
	}
	return r.err
}

// Custom returns the custom resources, sorted by group and title.
func (r *Registry) Custom() []Resource {
	if r == nil {
		return nil
	}
	return r.custom
}

// Lookup finds a resource by name, case-insensitively: builtins first (as
// Lookup), then custom resources by plural.group, plural, short name,
// singular or kind.
func (r *Registry) Lookup(name string) (Resource, bool) {
	if res, ok := Lookup(name); ok {
		return res, true
	}
	if r == nil {
		return Resource{}, false
	}
	res, ok := r.names[strings.ToLower(name)]
	return res, ok
}

// ForKind finds the resource of an object's kind, e.g. of an owner
// reference. apiVersion tells custom resources of the same kind apart.
func (r *Registry) ForKind(apiVersion, kind string) (Resource, bool) {
	group, _, found := strings.Cut(apiVersion, "/")
	if !found {
		group = ""
	}
	for _, b := range Builtins {
		if b.Kind == kind && (apiVersion == "" || b.Group == group) {
			return b, true
		}
	}
	for _, c := range r.Custom() {
		if c.Kind == kind && (apiVersion == "" || c.Group == group) {
			return c, true
		}
	}
	return Resource{}, false
}

// InCategory returns the resources of a builtin category: the builtins, then
// the custom resources mapped into it, by title.
func (r *Registry) InCategory(cat string) []Resource {
	out := InCategory(cat)
	var custom []Resource
	for _, c := range r.Custom() {
		if c.Category == cat {
			custom = append(custom, c)
		}
	}
	slices.SortStableFunc(custom, func(a, b Resource) int { return cmp.Compare(a.Title, b.Title) })
	return append(out, custom...)
}

// Group is the custom resources of one API group, for a folder under Custom
// Resources.
type Group struct {
	Name      string
	Resources []Resource // by title
}

// Groups returns the namespaced or the cluster-scoped custom resources of
// CatCustom, by group.
func (r *Registry) Groups(namespaced bool) []Group {
	var out []Group
	for _, c := range r.Custom() { // sorted by group, then title
		if c.Category != CatCustom || c.Namespaced != namespaced {
			continue
		}
		if len(out) == 0 || out[len(out)-1].Name != c.Group {
			out = append(out, Group{Name: c.Group})
		}
		g := &out[len(out)-1]
		g.Resources = append(g.Resources, c)
	}
	return out
}
