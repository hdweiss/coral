package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/hdweiss/coral/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// deletedMsg reports the outcome of a delete or kill.
type deletedMsg struct {
	key  k8s.Key
	what string // "deleted pod x"
	err  error
}

// Values of the delete dialog's options.
var (
	propagations = []string{"Background", "Foreground", "Orphan"}
	gracePeriods = []string{"default", "0s", "5s", "30s"}
)

// confirmDelete asks before deleting obj, with the propagation policy and
// grace period as options. kill is a pod delete with grace period 0, like
// k9s's kill: the kubelet stops the containers at once.
func (a *App) confirmDelete(obj *unstructured.Unstructured, kill bool) tea.Cmd {
	key, _ := a.selection()
	kind, name := obj.GetKind(), obj.GetName()
	where := ""
	if ns := obj.GetNamespace(); ns != "" {
		where = " in " + ns
	}
	if kill {
		a.confirm = newConfirm("Kill "+kind, "Kill", true,
			[]string{"Kill " + strings.ToLower(kind) + " " + name + where + "?", "Its containers are stopped without a grace period."},
			func(*confirm) tea.Cmd {
				zero := int64(0)
				return a.deleteObject(key, obj, metav1.DeleteOptions{GracePeriodSeconds: &zero}, "killed")
			})
		return nil
	}
	prop := &confirmOption{key: "p", label: "propagation", values: propagations}
	grace := &confirmOption{key: "g", label: "grace period", values: gracePeriods}
	a.confirm = newConfirm("Delete "+kind, "Delete", true,
		[]string{"Delete " + strings.ToLower(kind) + " " + name + where + "?"},
		func(c *confirm) tea.Cmd {
			p := metav1.DeletionPropagation(prop.value())
			opts := metav1.DeleteOptions{PropagationPolicy: &p}
			switch g := grace.value(); g {
			case "default":
			default:
				secs := map[string]int64{"0s": 0, "5s": 5, "30s": 30}[g]
				opts.GracePeriodSeconds = &secs
			}
			return a.deleteObject(key, obj, opts, "deleted")
		}, prop, grace)
	return nil
}

func (a *App) deleteObject(key k8s.Key, obj *unstructured.Unstructured, opts metav1.DeleteOptions, verb string) tea.Cmd {
	if a.refuseWrite() {
		return nil
	}
	store := a.store
	what := verb + " " + strings.ToLower(obj.GetKind()) + " " + obj.GetName()
	a.setFlash(strings.TrimSuffix(verb, "ed")+"ing "+obj.GetName()+"…", false)
	return func() tea.Msg {
		return deletedMsg{key: key, what: what, err: store.Delete(key, obj, opts)}
	}
}

func (a *App) onDeleted(msg deletedMsg) tea.Cmd {
	if msg.err != nil {
		a.setFlash(msg.err.Error(), true)
		return nil
	}
	a.setFlash(msg.what, false)
	if a.desc != nil {
		return a.desc.load(a.store, 0, false)
	}
	if a.watches[a.cur] != nil {
		return nil // the watch shows it
	}
	return a.fetch(a.cur)
}
