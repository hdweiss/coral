package k8s

import (
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// LogRequest selects a container log.
type LogRequest struct {
	Namespace, Pod, Container string
	Previous                  bool  // the log of the previous, terminated container
	Follow                    bool  // keep streaming new lines
	TailLines                 int64 // lines from the end; 0 = all
}

func (r LogRequest) options() *corev1.PodLogOptions {
	o := &corev1.PodLogOptions{Container: r.Container, Previous: r.Previous, Follow: r.Follow, Timestamps: true}
	if r.TailLines > 0 {
		o.TailLines = &r.TailLines
	}
	return o
}

// Containers lists a pod's containers in log-picking order: regular
// containers, then init and ephemeral ones. The first is the default, or the
// one named by the kubectl.kubernetes.io/default-container annotation.
func Containers(pod *unstructured.Unstructured) []string {
	var names []string
	for _, field := range []string{"containers", "initContainers", "ephemeralContainers"} {
		list, _, _ := unstructured.NestedSlice(pod.Object, "spec", field)
		for _, c := range list {
			if m, ok := c.(map[string]any); ok {
				if n, ok := m["name"].(string); ok {
					names = append(names, n)
				}
			}
		}
	}
	if def := pod.GetAnnotations()["kubectl.kubernetes.io/default-container"]; def != "" {
		for i, n := range names {
			if n == def {
				names = append([]string{n}, append(names[:i:i], names[i+1:]...)...)
				break
			}
		}
	}
	return names
}

// LogContainers lists the containers of a pod that have a log to show, in
// Containers order: those that run or ran (state or last state running or
// terminated). A pod none of whose containers started yet gets its regular
// containers, so that the view can say why there is no log.
func LogContainers(pod *unstructured.Unstructured) []string {
	started := map[string]bool{}
	for _, field := range []string{"containerStatuses", "initContainerStatuses", "ephemeralContainerStatuses"} {
		list, _, _ := unstructured.NestedSlice(pod.Object, "status", field)
		for _, c := range list {
			m, ok := c.(map[string]any)
			if !ok {
				continue
			}
			for _, st := range []string{"state", "lastState"} {
				s, _ := m[st].(map[string]any)
				if s["running"] != nil || s["terminated"] != nil {
					name, _ := m["name"].(string)
					started[name] = true
				}
			}
		}
	}
	var out []string
	for _, n := range Containers(pod) {
		if started[n] {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		list, _, _ := unstructured.NestedSlice(pod.Object, "spec", "containers")
		for _, c := range list {
			if m, ok := c.(map[string]any); ok {
				if n, ok := m["name"].(string); ok {
					out = append(out, n)
				}
			}
		}
	}
	return out
}

// ContainerRunning reports whether a container of the pod is running.
func ContainerRunning(pod *unstructured.Unstructured, name string) bool {
	for _, field := range []string{"containerStatuses", "initContainerStatuses", "ephemeralContainerStatuses"} {
		list, _, _ := unstructured.NestedSlice(pod.Object, "status", field)
		for _, c := range list {
			if m, ok := c.(map[string]any); ok && m["name"] == name {
				s, _ := m["state"].(map[string]any)
				return s["running"] != nil
			}
		}
	}
	return false
}

// LogWorkloads are the resources whose pods L shows together.
var LogWorkloads = []string{"deployments", "statefulsets", "daemonsets", "replicasets", "jobs"}

// PodsOf picks the pods a workload selects from pods, sorted by name.
func PodsOf(workload *unstructured.Unstructured, pods []unstructured.Unstructured) ([]*unstructured.Unstructured, error) {
	sel, err := labelSelector(workload, "spec", "selector")
	if err != nil {
		return nil, err
	}
	var out []*unstructured.Unstructured
	for i := range pods {
		if pods[i].GetNamespace() == workload.GetNamespace() && sel.Matches(labels.Set(pods[i].GetLabels())) {
			out = append(out, &pods[i])
		}
	}
	slices.SortFunc(out, func(a, b *unstructured.Unstructured) int { return strings.Compare(a.GetName(), b.GetName()) })
	return out, nil
}
