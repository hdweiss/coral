package k8s

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"
)

// Column is one column of a resource table.
type Column struct {
	Name  string
	Value func(u *unstructured.Unstructured) string
	// Sort returns the sort key (int64 or string). Nil means sort by Value.
	Sort func(u *unstructured.Unstructured) any
	// Drop marks a column the table may hide when space runs out. Zero means
	// always shown; higher values are hidden first.
	Drop int
}

// dropFirst returns c marked as hideable with priority p.
func (c Column) dropFirst(p int) Column { c.Drop = p; return c }

var (
	colName = Column{Name: "NAME", Value: func(u *unstructured.Unstructured) string { return u.GetName() }}
	colAge  = Column{
		Name:  "AGE",
		Value: func(u *unstructured.Unstructured) string { return Age(u) },
		// Negate so that ascending order lists the youngest first, like the column reads.
		Sort: func(u *unstructured.Unstructured) any { return -u.GetCreationTimestamp().Unix() },
	}
	ColNamespace = Column{Name: "NAMESPACE", Value: func(u *unstructured.Unstructured) string { return u.GetNamespace() }}
)

// Columns returns the table columns for a resource.
func Columns(r Resource) []Column {
	switch r.Name {
	case "pods":
		return []Column{colName,
			col("READY", podReady),
			col("STATUS", PodStatus),
			intCol("RESTARTS", podRestarts),
			col("IP", field("status", "podIP")).dropFirst(2),
			col("NODE", field("spec", "nodeName")).dropFirst(1),
			colAge}
	case "deployments", "statefulsets":
		return []Column{colName,
			col("READY", func(u *unstructured.Unstructured) string {
				return fmt.Sprintf("%d/%d", num(u, "status", "readyReplicas"), num(u, "spec", "replicas"))
			}),
			intCol("UP-TO-DATE", func(u *unstructured.Unstructured) int64 { return num(u, "status", "updatedReplicas") }).dropFirst(1),
			intCol("AVAILABLE", func(u *unstructured.Unstructured) int64 { return num(u, "status", "availableReplicas") }),
			colAge}
	case "daemonsets":
		return []Column{colName,
			intCol("DESIRED", func(u *unstructured.Unstructured) int64 { return num(u, "status", "desiredNumberScheduled") }),
			intCol("CURRENT", func(u *unstructured.Unstructured) int64 { return num(u, "status", "currentNumberScheduled") }),
			intCol("READY", func(u *unstructured.Unstructured) int64 { return num(u, "status", "numberReady") }),
			colAge}
	case "replicasets":
		return []Column{colName,
			intCol("DESIRED", func(u *unstructured.Unstructured) int64 { return num(u, "spec", "replicas") }),
			intCol("CURRENT", func(u *unstructured.Unstructured) int64 { return num(u, "status", "replicas") }),
			intCol("READY", func(u *unstructured.Unstructured) int64 { return num(u, "status", "readyReplicas") }),
			colAge}
	case "jobs":
		return []Column{colName,
			col("COMPLETIONS", func(u *unstructured.Unstructured) string {
				c, ok, _ := unstructured.NestedInt64(u.Object, "spec", "completions")
				if !ok {
					c = 1
				}
				return fmt.Sprintf("%d/%d", num(u, "status", "succeeded"), c)
			}),
			col("STATUS", jobStatus),
			colAge}
	case "cronjobs":
		return []Column{colName,
			col("SCHEDULE", field("spec", "schedule")),
			col("SUSPEND", func(u *unstructured.Unstructured) string {
				b, _, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
				return strconv.FormatBool(b)
			}),
			intCol("ACTIVE", func(u *unstructured.Unstructured) int64 {
				a, _, _ := unstructured.NestedSlice(u.Object, "status", "active")
				return int64(len(a))
			}),
			col("LAST SCHEDULE", func(u *unstructured.Unstructured) string { return since(str(u, "status", "lastScheduleTime")) }),
			colAge}
	case "horizontalpodautoscalers":
		return []Column{colName,
			col("REFERENCE", func(u *unstructured.Unstructured) string {
				return str(u, "spec", "scaleTargetRef", "kind") + "/" + str(u, "spec", "scaleTargetRef", "name")
			}),
			intCol("MIN", func(u *unstructured.Unstructured) int64 { return num(u, "spec", "minReplicas") }),
			intCol("MAX", func(u *unstructured.Unstructured) int64 { return num(u, "spec", "maxReplicas") }),
			intCol("REPLICAS", func(u *unstructured.Unstructured) int64 { return num(u, "status", "currentReplicas") }),
			colAge}
	case "services":
		return []Column{colName,
			col("TYPE", field("spec", "type")),
			col("CLUSTER-IP", field("spec", "clusterIP")).dropFirst(1),
			col("PORTS", servicePorts),
			colAge}
	case "ingresses":
		return []Column{colName,
			col("CLASS", field("spec", "ingressClassName")),
			col("HOSTS", func(u *unstructured.Unstructured) string {
				rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
				var hosts []string
				for _, r := range rules {
					if m, ok := r.(map[string]any); ok {
						if h, ok := m["host"].(string); ok {
							hosts = append(hosts, h)
						}
					}
				}
				return strings.Join(hosts, ",")
			}),
			colAge}
	case "networkpolicies":
		return []Column{colName,
			col("POD-SELECTOR", func(u *unstructured.Unstructured) string {
				return selector(u, "spec", "podSelector", "matchLabels")
			}),
			colAge}
	case "configmaps":
		return []Column{colName,
			intCol("DATA", func(u *unstructured.Unstructured) int64 { return mapLen(u, "data") + mapLen(u, "binaryData") }),
			colAge}
	case "secrets":
		return []Column{colName,
			col("TYPE", field("type")),
			intCol("DATA", func(u *unstructured.Unstructured) int64 { return mapLen(u, "data") }),
			colAge}
	case "persistentvolumeclaims":
		return []Column{colName,
			col("STATUS", field("status", "phase")),
			col("VOLUME", field("spec", "volumeName")).dropFirst(2),
			col("CAPACITY", field("status", "capacity", "storage")),
			col("STORAGECLASS", field("spec", "storageClassName")).dropFirst(1),
			colAge}
	case "persistentvolumes":
		return []Column{colName,
			col("CAPACITY", field("spec", "capacity", "storage")),
			col("STATUS", field("status", "phase")),
			col("CLAIM", func(u *unstructured.Unstructured) string {
				ns := str(u, "spec", "claimRef", "namespace")
				if ns == "" {
					return ""
				}
				return ns + "/" + str(u, "spec", "claimRef", "name")
			}),
			col("STORAGECLASS", field("spec", "storageClassName")).dropFirst(1),
			colAge}
	case "nodes":
		return []Column{colName,
			col("STATUS", nodeStatus),
			col("ROLES", nodeRoles),
			col("VERSION", field("status", "nodeInfo", "kubeletVersion")),
			colAge}
	case "namespaces":
		return []Column{colName, col("STATUS", field("status", "phase")), colAge}
	}
	return []Column{colName, colAge}
}

func col(name string, f func(*unstructured.Unstructured) string) Column {
	return Column{Name: name, Value: f}
}

func intCol(name string, f func(*unstructured.Unstructured) int64) Column {
	return Column{
		Name:  name,
		Value: func(u *unstructured.Unstructured) string { return strconv.FormatInt(f(u), 10) },
		Sort:  func(u *unstructured.Unstructured) any { return f(u) },
	}
}

func field(fields ...string) func(*unstructured.Unstructured) string {
	return func(u *unstructured.Unstructured) string { return str(u, fields...) }
}

func str(u *unstructured.Unstructured, fields ...string) string {
	v, _, _ := unstructured.NestedFieldNoCopy(u.Object, fields...)
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func num(u *unstructured.Unstructured, fields ...string) int64 {
	v, _, _ := unstructured.NestedFieldNoCopy(u.Object, fields...)
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	case int:
		return int64(t)
	}
	return 0
}

func mapLen(u *unstructured.Unstructured, fields ...string) int64 {
	m, _, _ := unstructured.NestedMap(u.Object, fields...)
	return int64(len(m))
}

func selector(u *unstructured.Unstructured, fields ...string) string {
	m, _, _ := unstructured.NestedStringMap(u.Object, fields...)
	if len(m) == 0 {
		return "<all>"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		keys[i] = k + "=" + m[k]
	}
	return strings.Join(keys, ",")
}

// Age renders the object's age like kubectl does.
func Age(u *unstructured.Unstructured) string {
	ts := u.GetCreationTimestamp()
	if ts.IsZero() {
		return "<unknown>"
	}
	return duration.HumanDuration(time.Since(ts.Time))
}

func since(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return "<none>"
	}
	return duration.HumanDuration(time.Since(t))
}

func containerStatuses(u *unstructured.Unstructured) []map[string]any {
	s, _, _ := unstructured.NestedSlice(u.Object, "status", "containerStatuses")
	out := make([]map[string]any, 0, len(s))
	for _, c := range s {
		if m, ok := c.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func podReady(u *unstructured.Unstructured) string {
	containers, _, _ := unstructured.NestedSlice(u.Object, "spec", "containers")
	ready := 0
	for _, c := range containerStatuses(u) {
		if b, _ := c["ready"].(bool); b {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, len(containers))
}

func podRestarts(u *unstructured.Unstructured) int64 {
	var n int64
	for _, c := range containerStatuses(u) {
		switch v := c["restartCount"].(type) {
		case int64:
			n += v
		case float64:
			n += int64(v)
		}
	}
	return n
}

// PodStatus computes the STATUS column the way kubectl does (simplified).
func PodStatus(u *unstructured.Unstructured) string {
	if u.GetDeletionTimestamp() != nil {
		return "Terminating"
	}
	reason := str(u, "status", "phase")
	if r := str(u, "status", "reason"); r != "" {
		reason = r
	}
	for _, c := range containerStatuses(u) {
		state, _ := c["state"].(map[string]any)
		if w, ok := state["waiting"].(map[string]any); ok {
			if r, _ := w["reason"].(string); r != "" {
				reason = r
			}
		} else if t, ok := state["terminated"].(map[string]any); ok {
			if r, _ := t["reason"].(string); r != "" {
				reason = r
			}
		}
	}
	return reason
}

func jobStatus(u *unstructured.Unstructured) string {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["status"] == "True" {
			switch m["type"] {
			case "Complete":
				return "Complete"
			case "Failed":
				return "Failed"
			}
		}
	}
	return "Running"
}

func nodeStatus(u *unstructured.Unstructured) string {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	status := "Unknown"
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == "Ready" {
			if m["status"] == "True" {
				status = "Ready"
			} else {
				status = "NotReady"
			}
		}
	}
	if b, _, _ := unstructured.NestedBool(u.Object, "spec", "unschedulable"); b {
		status += ",SchedulingDisabled"
	}
	return status
}

func nodeRoles(u *unstructured.Unstructured) string {
	var roles []string
	for k := range u.GetLabels() {
		if r, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok {
			roles = append(roles, r)
		}
	}
	if len(roles) == 0 {
		return "<none>"
	}
	sort.Strings(roles)
	return strings.Join(roles, ",")
}

func servicePorts(u *unstructured.Unstructured) string {
	ports, _, _ := unstructured.NestedSlice(u.Object, "spec", "ports")
	var out []string
	for _, p := range ports {
		m, _ := p.(map[string]any)
		s := fmt.Sprint(m["port"])
		if np, ok := m["nodePort"]; ok {
			s += ":" + fmt.Sprint(np)
		}
		proto, _ := m["protocol"].(string)
		if proto == "" {
			proto = "TCP"
		}
		out = append(out, s+"/"+proto)
	}
	if len(out) == 0 {
		return "<none>"
	}
	return strings.Join(out, ",")
}
