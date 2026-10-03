package k8s

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
