package k8s

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"
)

// Field is one line of a details section: "Image  nginx:1.27".
type Field struct {
	Key, Value string
	Warn       bool // something is wrong: a waiting container, a False condition
}

// Section is a titled group of fields, like a block of kubectl describe.
type Section struct {
	Title  string
	Fields []Field
}

// Details computes what kubectl describe shows beyond the YAML, from the
// object alone: a pod's containers with their state, restarts, probes and
// resources; a workload's replicas and strategy; a node's capacity; and the
// conditions of anything that has them. It needs no requests, so it works
// the same on the demo clusters.
func Details(obj *unstructured.Unstructured) []Section {
	var out []Section
	switch obj.GetKind() {
	case "Pod":
		out = append(out, podStatusSection(obj))
		out = append(out, containerSections(obj)...)
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
		out = append(out, workloadSection(obj))
	case "Node":
		out = append(out, nodeSection(obj))
	case "Job":
		out = append(out, jobSection(obj))
	}
	if s, ok := conditionsSection(obj); ok {
		out = append(out, s)
	}
	return out
}

func ago(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	return duration.HumanDuration(time.Since(t)) + " ago"
}

func addIf(fs []Field, key, value string) []Field {
	if value != "" {
		fs = append(fs, Field{Key: key, Value: value})
	}
	return fs
}

func podStatusSection(pod *unstructured.Unstructured) Section {
	status := PodStatus(pod)
	fs := []Field{{Key: "Status", Value: status, Warn: !podHealthy(status)}}
	fs = addIf(fs, "Node", str(pod, "spec", "nodeName"))
	fs = addIf(fs, "IP", str(pod, "status", "podIP"))
	fs = addIf(fs, "QoS", str(pod, "status", "qosClass"))
	fs = addIf(fs, "Service account", str(pod, "spec", "serviceAccountName"))
	if t := str(pod, "status", "startTime"); t != "" {
		fs = append(fs, Field{Key: "Started", Value: ago(t)})
	}
	return Section{Title: "Pod", Fields: fs}
}

func podHealthy(status string) bool {
	switch status {
	case "Running", "Completed", "Succeeded":
		return true
	}
	return false
}

// containerSections describes each container (init containers first, as
// they ran first) with its status.
func containerSections(pod *unstructured.Unstructured) []Section {
	statuses := map[string]map[string]any{}
	for _, field := range []string{"initContainerStatuses", "containerStatuses"} {
		list, _, _ := unstructured.NestedSlice(pod.Object, "status", field)
		for _, c := range list {
			if m, ok := c.(map[string]any); ok {
				name, _ := m["name"].(string)
				statuses[name] = m
			}
		}
	}
	var out []Section
	for _, field := range []string{"initContainers", "containers"} {
		list, _, _ := unstructured.NestedSlice(pod.Object, "spec", field)
		for _, c := range list {
			spec, ok := c.(map[string]any)
			if !ok {
				continue
			}
			name, _ := spec["name"].(string)
			title := "Container " + name
			if field == "initContainers" {
				title = "Init container " + name
			}
			out = append(out, Section{Title: title, Fields: containerFields(spec, statuses[name])})
		}
	}
	return out
}

func containerFields(spec, st map[string]any) []Field {
	s := func(m map[string]any, path ...string) string {
		v, _, _ := unstructured.NestedFieldNoCopy(m, path...)
		switch t := v.(type) {
		case string:
			return t
		case int64:
			return fmt.Sprint(t)
		case float64:
			return fmt.Sprint(int64(t))
		case bool:
			return fmt.Sprint(t)
		}
		return ""
	}
	fs := []Field{{Key: "Image", Value: s(spec, "image")}}
	if st != nil {
		state, _ := st["state"].(map[string]any)
		desc, warn := containerState(state)
		fs = append(fs, Field{Key: "State", Value: desc, Warn: warn})
		if last, _ := st["lastState"].(map[string]any); len(last) > 0 {
			desc, _ := containerState(last)
			fs = append(fs, Field{Key: "Last state", Value: desc})
		}
		ready := s(st, "ready")
		fs = append(fs, Field{Key: "Ready", Value: ready, Warn: ready == "false" && desc != "" && !strings.HasPrefix(desc, "Terminated: Completed")})
		if r := s(st, "restartCount"); r != "" && r != "0" {
			fs = append(fs, Field{Key: "Restarts", Value: r, Warn: true})
		}
	}
	var ports []string
	if list, ok := spec["ports"].([]any); ok {
		for _, p := range list {
			if m, ok := p.(map[string]any); ok {
				port := s(m, "containerPort") + "/" + strings.ToLower(cmpOr(s(m, "protocol"), "TCP"))
				if n := s(m, "name"); n != "" {
					port = n + " " + port
				}
				ports = append(ports, port)
			}
		}
	}
	fs = addIf(fs, "Ports", strings.Join(ports, ", "))
	fs = addIf(fs, "Requests", resourceList(spec, "requests"))
	fs = addIf(fs, "Limits", resourceList(spec, "limits"))
	for _, p := range []struct{ key, field string }{{"Liveness", "livenessProbe"}, {"Readiness", "readinessProbe"}, {"Startup", "startupProbe"}} {
		if m, ok := spec[p.field].(map[string]any); ok {
			fs = append(fs, Field{Key: p.key, Value: probe(m)})
		}
	}
	if mounts, ok := spec["volumeMounts"].([]any); ok {
		var ms []string
		for _, v := range mounts {
			if m, ok := v.(map[string]any); ok {
				mount := s(m, "mountPath") + " ← " + s(m, "name")
				if s(m, "readOnly") == "true" {
					mount += " (ro)"
				}
				ms = append(ms, mount)
			}
		}
		fs = addIf(fs, "Mounts", strings.Join(ms, ", "))
	}
	return fs
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// containerState describes a state ({running: …} and so on) like kubectl:
// "Running since 3h ago", "Waiting: CrashLoopBackOff", "Terminated: Error
// (exit 1) 3m ago". warn is set for a waiting or failed container.
func containerState(state map[string]any) (desc string, warn bool) {
	if r, ok := state["running"].(map[string]any); ok {
		if t, _ := r["startedAt"].(string); t != "" {
			return "Running, started " + ago(t), false
		}
		return "Running", false
	}
	if w, ok := state["waiting"].(map[string]any); ok {
		desc = "Waiting"
		if r, _ := w["reason"].(string); r != "" {
			desc += ": " + r
		}
		if m, _ := w["message"].(string); m != "" {
			desc += " (" + m + ")"
		}
		return desc, true
	}
	if t, ok := state["terminated"].(map[string]any); ok {
		reason, _ := t["reason"].(string)
		code, _ := t["exitCode"].(int64)
		desc = fmt.Sprintf("Terminated: %s (exit %d)", cmpOr(reason, "?"), code)
		if f, _ := t["finishedAt"].(string); f != "" {
			desc += " " + ago(f)
		}
		return desc, code != 0
	}
	return "", false
}

func resourceList(spec map[string]any, which string) string {
	m, _, _ := unstructured.NestedMap(spec, "resources", which)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %v", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// probe describes a probe like kubectl: "http-get :http/healthz delay=5s
// period=10s".
func probe(p map[string]any) string {
	var what string
	switch {
	case p["httpGet"] != nil:
		h := p["httpGet"].(map[string]any)
		what = fmt.Sprintf("http-get :%v%v", h["port"], h["path"])
	case p["tcpSocket"] != nil:
		what = fmt.Sprintf("tcp :%v", p["tcpSocket"].(map[string]any)["port"])
	case p["grpc"] != nil:
		what = fmt.Sprintf("grpc :%v", p["grpc"].(map[string]any)["port"])
	case p["exec"] != nil:
		cmd, _ := p["exec"].(map[string]any)["command"].([]any)
		var parts []string
		for _, c := range cmd {
			parts = append(parts, fmt.Sprint(c))
		}
		what = "exec " + strings.Join(parts, " ")
	}
	for _, f := range []struct{ key, label, unit string }{
		{"initialDelaySeconds", "delay", "s"}, {"timeoutSeconds", "timeout", "s"}, {"periodSeconds", "period", "s"}, {"failureThreshold", "failures", ""},
	} {
		if v, ok := p[f.key]; ok {
			what += fmt.Sprintf(" %s=%v%s", f.label, v, f.unit)
		}
	}
	return what
}

func workloadSection(obj *unstructured.Unstructured) Section {
	n := func(path ...string) int64 { return num(obj, path...) }
	var fs []Field
	switch obj.GetKind() {
	case "DaemonSet":
		want, ready := n("status", "desiredNumberScheduled"), n("status", "numberReady")
		fs = append(fs, Field{Key: "Pods", Value: fmt.Sprintf("%d desired, %d scheduled, %d ready, %d up to date, %d available",
			want, n("status", "currentNumberScheduled"), ready, n("status", "updatedNumberScheduled"), n("status", "numberAvailable")), Warn: ready < want})
	default:
		want, ready := n("spec", "replicas"), n("status", "readyReplicas")
		fs = append(fs, Field{Key: "Replicas", Value: fmt.Sprintf("%d desired, %d updated, %d ready, %d available",
			want, n("status", "updatedReplicas"), ready, n("status", "availableReplicas")), Warn: ready < want})
	}
	if t := str(obj, "spec", "strategy", "type"); t != "" {
		if t == "RollingUpdate" {
			t += fmt.Sprintf(" (max surge %v, max unavailable %v)",
				val(obj, "spec", "strategy", "rollingUpdate", "maxSurge"), val(obj, "spec", "strategy", "rollingUpdate", "maxUnavailable"))
		}
		fs = append(fs, Field{Key: "Strategy", Value: t})
	}
	fs = addIf(fs, "Update strategy", str(obj, "spec", "updateStrategy", "type"))
	if sel, ok, _ := unstructured.NestedStringMap(obj.Object, "spec", "selector", "matchLabels"); ok {
		fs = addIf(fs, "Selector", labelString(sel))
	}
	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	var images []string
	for _, c := range containers {
		if m, ok := c.(map[string]any); ok {
			images = append(images, fmt.Sprintf("%v %v", m["name"], m["image"]))
		}
	}
	fs = addIf(fs, "Images", strings.Join(images, ", "))
	return Section{Title: obj.GetKind(), Fields: fs}
}

func jobSection(obj *unstructured.Unstructured) Section {
	fs := []Field{{Key: "Completions", Value: fmt.Sprintf("%d/%d succeeded, %d active, %d failed",
		num(obj, "status", "succeeded"), max(num(obj, "spec", "completions"), 1), num(obj, "status", "active"), num(obj, "status", "failed")),
		Warn: num(obj, "status", "failed") > 0}}
	if t := str(obj, "status", "startTime"); t != "" {
		fs = append(fs, Field{Key: "Started", Value: ago(t)})
	}
	if t := str(obj, "status", "completionTime"); t != "" {
		fs = append(fs, Field{Key: "Completed", Value: ago(t)})
	}
	return Section{Title: "Job", Fields: fs}
}

func nodeSection(node *unstructured.Unstructured) Section {
	var fs []Field
	for _, k := range []string{"cpu", "memory", "pods"} {
		fs = addIf(fs, "Allocatable "+k, fmt.Sprintf("%v of %v", val(node, "status", "allocatable", k), val(node, "status", "capacity", k)))
	}
	fs = addIf(fs, "Kubelet", str(node, "status", "nodeInfo", "kubeletVersion"))
	fs = addIf(fs, "OS", str(node, "status", "nodeInfo", "osImage"))
	fs = addIf(fs, "Runtime", str(node, "status", "nodeInfo", "containerRuntimeVersion"))
	if b, _, _ := unstructured.NestedBool(node.Object, "spec", "unschedulable"); b {
		fs = append(fs, Field{Key: "Scheduling", Value: "disabled (cordoned)", Warn: true})
	}
	return Section{Title: "Node", Fields: fs}
}

// conditionsSection lists status.conditions: type, status, reason, message
// and when it last changed. A condition is a warning when it is False,
// except for the "pressure" kind where False is good.
func conditionsSection(obj *unstructured.Unstructured) (Section, bool) {
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	var fs []Field
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		status, _ := m["status"].(string)
		v := status
		if r, _ := m["reason"].(string); r != "" {
			v += " · " + r
		}
		if msg, _ := m["message"].(string); msg != "" {
			v += " · " + oneLine(msg)
		}
		if t, _ := m["lastTransitionTime"].(string); t != "" {
			v += " · " + ago(t)
		}
		bad := status == "False"
		if strings.HasSuffix(typ, "Pressure") || typ == "NetworkUnavailable" || typ == "ReplicaFailure" || typ == "Failed" {
			bad = status == "True"
		}
		fs = append(fs, Field{Key: typ, Value: v, Warn: bad})
	}
	return Section{Title: "Conditions", Fields: fs}, len(fs) > 0
}

func val(u *unstructured.Unstructured, path ...string) any {
	v, _, _ := unstructured.NestedFieldNoCopy(u.Object, path...)
	if v == nil {
		return "?"
	}
	return v
}

func labelString(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return strings.Join(parts, ",")
}
