package k8s

import (
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Resource describes a Kubernetes resource type that coral knows how to list:
// a builtin, or a custom resource of one context.
type Resource struct {
	Name       string // plural resource name, e.g. "pods"
	Kind       string
	Title      string // display name, e.g. "Pods"
	Group      string
	Version    string
	Namespaced bool
	Category   string
	Aliases    []string // for builtins all names; for custom resources the short names, singular and kind

	Custom  bool
	Printer []PrinterColumn // a custom resource's additionalPrinterColumns
}

func builtin(name, kind, title, group, version string, namespaced bool, cat string, aliases []string) Resource {
	return Resource{Name: name, Kind: kind, Title: title, Group: group, Version: version,
		Namespaced: namespaced, Category: cat, Aliases: aliases}
}

// ID names the resource unambiguously within a context: the plural for
// builtins, plural.group for custom resources. Pins store it.
func (r Resource) ID() string {
	if r.Custom {
		return r.Name + "." + r.Group
	}
	return r.Name
}

func (r Resource) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Name}
}

func (r Resource) APIVersion() string {
	if r.Group == "" {
		return r.Version
	}
	return r.Group + "/" + r.Version
}

const (
	CatWorkloads = "Workloads"
	CatNetwork   = "Network"
	CatConfig    = "Config"
	CatStorage   = "Storage"
	CatAccess    = "Access Control"
	CatCluster   = "Cluster"
	// CatCustom holds the custom resources of groups that fit no builtin
	// category, in a folder per group.
	CatCustom = "Custom Resources"
	// CatTop resources sit directly under a namespace in the navigator,
	// after the categories.
	CatTop = ""
)

// NamespacedCategories is the order categories appear under a namespace.
var NamespacedCategories = []string{CatWorkloads, CatNetwork, CatConfig, CatStorage, CatAccess}

var Builtins = []Resource{
	builtin("pods", "Pod", "Pods", "", "v1", true, CatWorkloads, []string{"po", "pod"}),
	builtin("deployments", "Deployment", "Deployments", "apps", "v1", true, CatWorkloads, []string{"deploy", "deployment", "dp"}),
	builtin("statefulsets", "StatefulSet", "StatefulSets", "apps", "v1", true, CatWorkloads, []string{"sts", "statefulset"}),
	builtin("daemonsets", "DaemonSet", "DaemonSets", "apps", "v1", true, CatWorkloads, []string{"ds", "daemonset"}),
	builtin("replicasets", "ReplicaSet", "ReplicaSets", "apps", "v1", true, CatWorkloads, []string{"rs", "replicaset"}),
	builtin("jobs", "Job", "Jobs", "batch", "v1", true, CatWorkloads, []string{"job"}),
	builtin("cronjobs", "CronJob", "CronJobs", "batch", "v1", true, CatWorkloads, []string{"cj", "cronjob"}),
	builtin("horizontalpodautoscalers", "HorizontalPodAutoscaler", "HPAs", "autoscaling", "v2", true, CatWorkloads, []string{"hpa"}),

	builtin("services", "Service", "Services", "", "v1", true, CatNetwork, []string{"svc", "service"}),
	builtin("ingresses", "Ingress", "Ingresses", "networking.k8s.io", "v1", true, CatNetwork, []string{"ing", "ingress"}),
	builtin("networkpolicies", "NetworkPolicy", "NetworkPolicies", "networking.k8s.io", "v1", true, CatNetwork, []string{"netpol", "np"}),

	builtin("configmaps", "ConfigMap", "ConfigMaps", "", "v1", true, CatConfig, []string{"cm", "configmap"}),
	builtin("secrets", "Secret", "Secrets", "", "v1", true, CatConfig, []string{"sec", "secret"}),

	builtin("persistentvolumeclaims", "PersistentVolumeClaim", "PVCs", "", "v1", true, CatStorage, []string{"pvc"}),

	builtin("events", "Event", "Events", "", "v1", true, CatTop, []string{"ev", "event"}),

	builtin("serviceaccounts", "ServiceAccount", "ServiceAccounts", "", "v1", true, CatAccess, []string{"sa"}),
	builtin("roles", "Role", "Roles", "rbac.authorization.k8s.io", "v1", true, CatAccess, []string{"role"}),
	builtin("rolebindings", "RoleBinding", "RoleBindings", "rbac.authorization.k8s.io", "v1", true, CatAccess, []string{"rb"}),

	builtin("nodes", "Node", "Nodes", "", "v1", false, CatCluster, []string{"no", "node"}),
	builtin("namespaces", "Namespace", "Namespaces", "", "v1", false, CatCluster, []string{"ns", "namespace"}),
	builtin("persistentvolumes", "PersistentVolume", "PersistentVolumes", "", "v1", false, CatCluster, []string{"pv"}),
	builtin("storageclasses", "StorageClass", "StorageClasses", "storage.k8s.io", "v1", false, CatCluster, []string{"sc"}),
	builtin("clusterroles", "ClusterRole", "ClusterRoles", "rbac.authorization.k8s.io", "v1", false, CatCluster, []string{"cr"}),
	builtin("clusterrolebindings", "ClusterRoleBinding", "ClusterRoleBindings", "rbac.authorization.k8s.io", "v1", false, CatCluster, []string{"crb"}),
	builtin("customresourcedefinitions", "CustomResourceDefinition", "CRDs", "apiextensions.k8s.io", "v1", false, CatCluster, []string{"crd", "crds"}),
}

var lookup = func() map[string]Resource {
	m := map[string]Resource{}
	for _, r := range Builtins {
		m[r.Name] = r
		m[strings.ToLower(r.Kind)] = r
		m[strings.ToLower(r.Title)] = r
		for _, a := range r.Aliases {
			m[a] = r
		}
	}
	return m
}()

// Lookup finds a resource by plural name, kind, title or alias (case-insensitive).
func Lookup(name string) (Resource, bool) {
	r, ok := lookup[strings.ToLower(name)]
	return r, ok
}

// MustLookup is Lookup for names known at compile time.
func MustLookup(name string) Resource {
	r, ok := Lookup(name)
	if !ok {
		panic("unknown resource " + name)
	}
	return r
}

// InCategory returns the builtin resources in a category, in registry order.
func InCategory(cat string) []Resource {
	var out []Resource
	for _, r := range Builtins {
		if r.Category == cat {
			out = append(out, r)
		}
	}
	return out
}
