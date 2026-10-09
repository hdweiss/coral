package k8s

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// Churn makes the demo clusters change every interval until ctx ends, so
// that refreshes and live mode have something to show: the crash-looping
// pod restarts and backs off again (with its BackOff event counting up),
// and every few rounds the oldest frontend pod is replaced by a new one that
// starts up. It writes to the fake trackers directly, so the changes are not
// traced as requests. p must come from NewDemoProvider.
func Churn(ctx context.Context, p Provider, interval time.Duration) {
	dp := p.(*demoProvider)
	var churners []*churner
	for _, name := range dp.Contexts() {
		churners = append(churners, &churner{tracker: dp.clients[name].(*dynamicfake.FakeDynamicClient).Tracker()})
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for round := 1; ; round++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, c := range churners {
			c.step(round)
		}
	}
}

type churner struct {
	tracker  k8stesting.ObjectTracker
	starting string // the frontend pod that is starting up
}

const churnNS = "shop"

var podsGVR = MustLookup("pods").GVR()

func (c *churner) step(round int) {
	if c.starting != "" {
		c.update(c.starting, func(p *unstructured.Unstructured) { setPodState(p, running, 0) })
		c.starting = ""
	}
	if round%2 == 0 {
		c.crashLoop(round%4 == 2)
	}
	if round%5 == 0 {
		c.replaceFrontend()
	}
}

func (c *churner) pods() []unstructured.Unstructured {
	o, err := c.tracker.List(podsGVR, podsGVR.GroupVersion().WithKind("Pod"), churnNS)
	if err != nil {
		return nil
	}
	return o.(*unstructured.UnstructuredList).Items
}

// update changes a pod and bumps its resourceVersion, so that an edit
// started before is a conflict.
func (c *churner) update(name string, change func(*unstructured.Unstructured)) {
	o, err := c.tracker.Get(podsGVR, churnNS, name)
	if err != nil {
		return
	}
	p := o.(*unstructured.Unstructured).DeepCopy()
	change(p)
	bumpVersion(p)
	c.tracker.Update(podsGVR, p, churnNS)
}

func bumpVersion(u *unstructured.Unstructured) {
	n, _ := strconv.Atoi(u.GetResourceVersion())
	u.SetResourceVersion(strconv.Itoa(n + 1))
}

// crashLoop restarts the crash-looping pod (restarted) or lets it back off
// again.
func (c *churner) crashLoop(restarted bool) {
	var pod *unstructured.Unstructured
	for _, p := range c.pods() {
		if podRestarts(&p) > 0 {
			pod = &p
			break
		}
	}
	if pod == nil {
		return
	}
	c.update(pod.GetName(), func(p *unstructured.Unstructured) {
		restarts := podRestarts(p)
		if restarted {
			setPodState(p, "Restarted", restarts+1)
		} else {
			setPodState(p, crash, restarts)
		}
	})
	if restarted {
		return
	}
	evs, err := c.tracker.List(EventsGVR, EventsGVR.GroupVersion().WithKind("Event"), churnNS)
	if err != nil {
		return
	}
	for _, ev := range evs.(*unstructured.UnstructuredList).Items {
		if str(&ev, "involvedObject", "name") == pod.GetName() && str(&ev, "reason") == "BackOff" {
			n, _, _ := unstructured.NestedInt64(ev.Object, "count")
			ev.Object["count"] = n + 1
			ev.Object["lastTimestamp"] = time.Now().UTC().Format(time.RFC3339)
			bumpVersion(&ev)
			c.tracker.Update(EventsGVR, &ev, churnNS)
		}
	}
}

// replaceFrontend deletes the oldest frontend pod and creates its
// replacement, which starts up on the next step.
func (c *churner) replaceFrontend() {
	var frontend []unstructured.Unstructured
	for _, p := range c.pods() {
		if p.GetLabels()["app"] == "frontend" {
			frontend = append(frontend, p)
		}
	}
	if len(frontend) == 0 {
		return
	}
	oldest := slices.MinFunc(frontend, func(a, b unstructured.Unstructured) int {
		return a.GetCreationTimestamp().Compare(b.GetCreationTimestamp().Time)
	})
	if err := c.tracker.Delete(podsGVR, churnNS, oldest.GetName()); err != nil {
		return
	}
	p := oldest.DeepCopy()
	now := time.Now()
	owner := p.GetOwnerReferences()
	prefix := p.GetName()
	if len(owner) > 0 {
		prefix = owner[0].Name
	}
	p.SetName(prefix + "-" + hash(now.String(), 5))
	p.SetUID(types.UID(fmt.Sprintf("churn-%d", now.UnixNano())))
	p.SetCreationTimestamp(metav1.NewTime(now.Truncate(time.Second)))
	p.SetResourceVersion("1")
	setPodState(p, "ContainerCreating", 0)
	if c.tracker.Create(podsGVR, p, churnNS) == nil {
		c.starting = p.GetName()
	}
}

// setPodState puts the pod's only container into state st: running, crash
// (CrashLoopBackOff), "Restarted" (running again but not ready yet) or
// "ContainerCreating".
func setPodState(p *unstructured.Unstructured, st podState, restarts int64) {
	cs := containerStatuses(p)
	if len(cs) == 0 {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	c := cs[0]
	c["restartCount"] = restarts
	ready := st == running
	c["ready"], c["started"] = ready, ready || st == "Restarted"
	phase := "Running"
	switch st {
	case running, "Restarted":
		c["state"] = map[string]any{"running": map[string]any{"startedAt": now}}
	case crash:
		c["state"] = map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff", "message": "back-off 5m0s restarting failed container"}}
		c["lastState"] = map[string]any{"terminated": map[string]any{"exitCode": int64(1), "reason": "Error", "finishedAt": now}}
	case "ContainerCreating":
		c["state"] = map[string]any{"waiting": map[string]any{"reason": "ContainerCreating"}}
		delete(c, "lastState")
		phase = "Pending"
	}
	list := make([]any, len(cs))
	for i := range cs {
		list[i] = cs[i]
	}
	unstructured.SetNestedSlice(p.Object, list, "status", "containerStatuses")
	unstructured.SetNestedField(p.Object, phase, "status", "phase")
	conds, _, _ := unstructured.NestedSlice(p.Object, "status", "conditions")
	for _, cond := range conds {
		if m, ok := cond.(map[string]any); ok && (m["type"] == "Ready" || m["type"] == "ContainersReady") {
			m["status"] = map[bool]string{true: "True", false: "False"}[ready]
			m["lastTransitionTime"] = now
		}
	}
	unstructured.SetNestedSlice(p.Object, conds, "status", "conditions")
}
