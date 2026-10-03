package k8s

import (
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Resource describes a Kubernetes resource type that coral knows how to list.
type Resource struct {
	Name       string // plural resource name, e.g. "pods"
	Kind       string
	Title      string // display name, e.g. "Pods"
	Group      string
	Version    string
	Namespaced bool
	Category   string
	Aliases    []string
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
)

// NamespacedCategories is the order categories appear under a namespace.
var NamespacedCategories = []string{CatWorkloads, CatNetwork, CatConfig, CatStorage, CatAccess}

var Builtins = []Resource{
	{"pods", "Pod", "Pods", "", "v1", true, CatWorkloads, []string{"po", "pod"}},
	{"deployments", "Deployment", "Deployments", "apps", "v1", true, CatWorkloads, []string{"deploy", "deployment", "dp"}},
	{"statefulsets", "StatefulSet", "StatefulSets", "apps", "v1", true, CatWorkloads, []string{"sts", "statefulset"}},
	{"daemonsets", "DaemonSet", "DaemonSets", "apps", "v1", true, CatWorkloads, []string{"ds", "daemonset"}},
	{"replicasets", "ReplicaSet", "ReplicaSets", "apps", "v1", true, CatWorkloads, []string{"rs", "replicaset"}},
	{"jobs", "Job", "Jobs", "batch", "v1", true, CatWorkloads, []string{"job"}},
	{"cronjobs", "CronJob", "CronJobs", "batch", "v1", true, CatWorkloads, []string{"cj", "cronjob"}},
	{"horizontalpodautoscalers", "HorizontalPodAutoscaler", "HPAs", "autoscaling", "v2", true, CatWorkloads, []string{"hpa"}},

	{"services", "Service", "Services", "", "v1", true, CatNetwork, []string{"svc", "service"}},
	{"ingresses", "Ingress", "Ingresses", "networking.k8s.io", "v1", true, CatNetwork, []string{"ing", "ingress"}},
	{"networkpolicies", "NetworkPolicy", "NetworkPolicies", "networking.k8s.io", "v1", true, CatNetwork, []string{"netpol", "np"}},

	{"configmaps", "ConfigMap", "ConfigMaps", "", "v1", true, CatConfig, []string{"cm", "configmap"}},
	{"secrets", "Secret", "Secrets", "", "v1", true, CatConfig, []string{"sec", "secret"}},

	{"persistentvolumeclaims", "PersistentVolumeClaim", "PVCs", "", "v1", true, CatStorage, []string{"pvc"}},

	{"serviceaccounts", "ServiceAccount", "ServiceAccounts", "", "v1", true, CatAccess, []string{"sa"}},
	{"roles", "Role", "Roles", "rbac.authorization.k8s.io", "v1", true, CatAccess, []string{"role"}},
	{"rolebindings", "RoleBinding", "RoleBindings", "rbac.authorization.k8s.io", "v1", true, CatAccess, []string{"rb"}},

	{"nodes", "Node", "Nodes", "", "v1", false, CatCluster, []string{"no", "node"}},
	{"namespaces", "Namespace", "Namespaces", "", "v1", false, CatCluster, []string{"ns", "namespace"}},
	{"persistentvolumes", "PersistentVolume", "PersistentVolumes", "", "v1", false, CatCluster, []string{"pv"}},
	{"storageclasses", "StorageClass", "StorageClasses", "storage.k8s.io", "v1", false, CatCluster, []string{"sc"}},
	{"clusterroles", "ClusterRole", "ClusterRoles", "rbac.authorization.k8s.io", "v1", false, CatCluster, []string{"cr"}},
	{"clusterrolebindings", "ClusterRoleBinding", "ClusterRoleBindings", "rbac.authorization.k8s.io", "v1", false, CatCluster, []string{"crb"}},
	{"customresourcedefinitions", "CustomResourceDefinition", "CRDs", "apiextensions.k8s.io", "v1", false, CatCluster, []string{"crd", "crds"}},
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
