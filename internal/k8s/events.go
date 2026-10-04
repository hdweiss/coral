package k8s

import (
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"
)

// EventsGVR is the core v1 Event resource. Its involvedObject is easier to
// match than the regarding field of events.k8s.io/v1.
var EventsGVR = MustLookup("events").GVR()

// WarningsKey is the list of Warning events behind the markers of list k
// (of resource res): the same namespace for namespaced resources. Of the
// cluster-scoped resources only nodes have markers, from the Node events of
// all namespaces; listing every event in the cluster for the others isn't
// worth it. The events list itself has no markers.
func WarningsKey(k Key, res Resource) (Key, bool) {
	switch {
	case k.GVR == EventsGVR:
		return Key{}, false
	case res.Namespaced:
		return Key{Context: k.Context, GVR: EventsGVR, Namespace: k.Namespace, Fields: "type=Warning"}, true
	case res.Kind == "Node":
		return Key{Context: k.Context, GVR: EventsGVR, Fields: "type=Warning,involvedObject.kind=Node"}, true
	}
	return Key{}, false
}

// EventsKey is the list of the events about obj, for the events and describe
// views: those naming its kind and name, in its namespace, or in any
// namespace for cluster-scoped objects (node events go to default). The
// selector also matches earlier objects of the same name; EventsAbout sorts
// those out, and the events the demo's fake client returns unfiltered.
func EventsKey(ctx string, obj *unstructured.Unstructured) Key {
	return Key{Context: ctx, GVR: EventsGVR, Namespace: obj.GetNamespace(),
		Fields: "involvedObject.kind=" + obj.GetKind() + ",involvedObject.name=" + obj.GetName()}
}

// WarningWindow is how far back the table's warning markers look. Clusters
// keep events for an hour by default; some keep them much longer.
const WarningWindow = time.Hour

// EventTime is when an event was last seen.
func EventTime(e *unstructured.Unstructured) time.Time {
	for _, f := range [][]string{
		{"series", "lastObservedTime"}, {"lastTimestamp"}, {"eventTime"}, {"firstTimestamp"},
	} {
		if t, err := time.Parse(time.RFC3339Nano, str(e, f...)); err == nil {
			return t
		}
	}
	return e.GetCreationTimestamp().Time
}

// EventCount is how often an event was seen.
func EventCount(e *unstructured.Unstructured) int64 {
	if n := num(e, "series", "count"); n > 0 {
		return n
	}
	return max(num(e, "count"), 1)
}

// IsWarning reports whether an event has type Warning.
func IsWarning(e *unstructured.Unstructured) bool { return str(e, "type") == "Warning" }

// EventObject names an event's object like kubectl: pod/catalog-6b9.
func EventObject(e *unstructured.Unstructured) string {
	return strings.ToLower(str(e, "involvedObject", "kind")) + "/" + str(e, "involvedObject", "name")
}

// About reports whether event e is about obj. The kubelet sets a node's name
// as the uid of its events, so a uid equal to the name also matches.
func About(e, obj *unstructured.Unstructured) bool {
	if str(e, "involvedObject", "kind") != obj.GetKind() ||
		str(e, "involvedObject", "name") != obj.GetName() ||
		str(e, "involvedObject", "namespace") != obj.GetNamespace() {
		return false
	}
	uid := str(e, "involvedObject", "uid")
	return uid == "" || uid == string(obj.GetUID()) || uid == obj.GetName()
}

// EventsAbout returns the events about obj, newest first.
func EventsAbout(events []unstructured.Unstructured, obj *unstructured.Unstructured) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for i := range events {
		if About(&events[i], obj) {
			out = append(out, &events[i])
		}
	}
	SortEvents(out)
	return out
}

// SortEvents orders events newest first.
func SortEvents(events []*unstructured.Unstructured) {
	slices.SortStableFunc(events, func(a, b *unstructured.Unstructured) int {
		return EventTime(b).Compare(EventTime(a))
	})
}

// WarningIndex counts recent Warning events per object, for the markers in
// the table. Events are counted by uid when they have one, so that a
// recreated object with the same name does not inherit the old one's
// warnings, and by kind/namespace/name otherwise (node events carry the
// node's name as uid).
type WarningIndex struct {
	byUID  map[string]int
	byName map[string]int
}

// IndexWarnings counts the Warning events seen since since.
func IndexWarnings(events []unstructured.Unstructured, since time.Time) WarningIndex {
	ix := WarningIndex{byUID: map[string]int{}, byName: map[string]int{}}
	for i := range events {
		e := &events[i]
		if !IsWarning(e) || EventTime(e).Before(since) {
			continue
		}
		name := str(e, "involvedObject", "name")
		if uid := str(e, "involvedObject", "uid"); uid != "" && uid != name {
			ix.byUID[uid]++
		} else {
			ix.byName[str(e, "involvedObject", "kind")+"/"+str(e, "involvedObject", "namespace")+"/"+name]++
		}
	}
	return ix
}

// Count returns the number of recent warnings about obj.
func (ix WarningIndex) Count(obj *unstructured.Unstructured) int {
	return ix.byUID[string(obj.GetUID())] + ix.byName[obj.GetKind()+"/"+obj.GetNamespace()+"/"+obj.GetName()]
}

// eventColumns are the columns of the events table. LAST SEEN sorts first,
// newest on top.
func eventColumns() []Column {
	return []Column{
		{
			Name:        "LAST SEEN",
			Value:       func(u *unstructured.Unstructured) string { return LastSeen(u) },
			Sort:        func(u *unstructured.Unstructured) any { return -EventTime(u).UnixNano() },
			DefaultSort: true,
		},
		// Warnings also show in red, so TYPE can go first.
		col("TYPE", field("type")).dropFirst(2),
		col("REASON", field("reason")),
		col("OBJECT", EventObject),
		intCol("COUNT", EventCount).dropFirst(1),
		{Name: "MESSAGE", Value: EventMessage, Flex: true},
	}
}

// LastSeen renders how long ago an event was last seen.
func LastSeen(e *unstructured.Unstructured) string {
	t := EventTime(e)
	if t.IsZero() {
		return "<unknown>"
	}
	return duration.HumanDuration(time.Since(t))
}

// EventMessage is an event's message on one line.
func EventMessage(e *unstructured.Unstructured) string { return oneLine(str(e, "message")) }

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
