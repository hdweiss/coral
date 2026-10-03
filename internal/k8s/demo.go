package k8s

import (
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/openapi"
	k8stesting "k8s.io/client-go/testing"
)

// demoProvider serves in-memory fake clusters, for development without a
// real cluster (coralctl --demo).
type demoProvider struct {
	clients map[string]dynamic.Interface
}

func NewDemoProvider() Provider {
	listKinds := map[schema.GroupVersionResource]string{}
	for _, r := range Builtins {
		listKinds[r.GVR()] = r.Kind + "List"
	}
	p := &demoProvider{clients: map[string]dynamic.Interface{}}
	for _, name := range p.Contexts() {
		objs := buildDemoCluster(name)
		c := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
		c.PrependReactor("update", "*", versionUpdates(c.Tracker()))
		p.clients[name] = c
	}
	return p
}

func (p *demoProvider) Contexts() []string             { return []string{"demo-dev", "demo-prod"} }
func (p *demoProvider) Current() string                { return "demo-dev" }
func (p *demoProvider) DefaultNamespace(string) string { return "shop" }
func (p *demoProvider) OpenAPI(string) (openapi.Client, error) {
	return nil, errors.New("the demo clusters have no OpenAPI document")
}
func (p *demoProvider) Client(ctx string) (dynamic.Interface, error) {
	if c, ok := p.clients[ctx]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("unknown context %q", ctx)
}

// versionUpdates makes updates behave like a real API server: a stale
// resourceVersion is a conflict, and every update bumps the version. The fake
// tracker does neither on its own.
func versionUpdates(tracker k8stesting.ObjectTracker) k8stesting.ReactionFunc {
	return func(action k8stesting.Action) (bool, runtime.Object, error) {
		upd, ok := action.(k8stesting.UpdateAction)
		if !ok {
			return false, nil, nil
		}
		u, ok := upd.GetObject().(*unstructured.Unstructured)
		if !ok {
			return false, nil, nil
		}
		cur, err := tracker.Get(action.GetResource(), action.GetNamespace(), u.GetName())
		if err != nil {
			return false, nil, nil // let the tracker report it
		}
		curRV := cur.(metav1.Object).GetResourceVersion()
		if u.GetResourceVersion() != "" && u.GetResourceVersion() != curRV {
			return true, nil, apierrors.NewConflict(action.GetResource().GroupResource(), u.GetName(),
				errors.New("the object has been modified; please apply your changes to the latest version and try again"))
		}
		n, _ := strconv.Atoi(curRV)
		u.SetResourceVersion(strconv.Itoa(n + 1))
		return false, nil, nil
	}
}

// --- demo object builders. Values must be JSON types (int64, not int). ---

type demo struct {
	cluster string
	now     time.Time
	objs    []runtime.Object
}

type m = map[string]any
type l = []any

func (d *demo) add(apiVersion, kind, ns, name string, age time.Duration, labels map[string]string, body m) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: m{"apiVersion": apiVersion, "kind": kind}}
	for k, v := range body {
		u.Object[k] = v
	}
	u.SetName(name)
	if ns != "" {
		u.SetNamespace(ns)
	}
	u.SetLabels(labels)
	u.SetCreationTimestamp(metav1.NewTime(d.now.Add(-age).Truncate(time.Second)))
	u.SetUID(types.UID(d.uid(kind + "/" + ns + "/" + name)))
	u.SetResourceVersion("1")
	d.objs = append(d.objs, u)
	return u
}

func (d *demo) uid(s string) string {
	h := fnv.New64a()
	h.Write([]byte(d.cluster + s))
	v := h.Sum64()
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", uint32(v>>32), uint16(v>>16), uint16(v), uint16(v>>48), v&0xffffffffffff)
}

func (d *demo) ts(age time.Duration) string {
	return d.now.Add(-age).UTC().Format(time.RFC3339)
}

// event records an event about obj, first and last seen ago. Events of
// cluster-scoped objects go to the default namespace, like the kubelet's
// node events, which also carry the node's name as uid.
func (d *demo) event(obj *unstructured.Unstructured, typ, reason, msg string, count int64, first, last time.Duration, component string) {
	ns, uid := obj.GetNamespace(), string(obj.GetUID())
	if ns == "" {
		ns = "default"
		if obj.GetKind() == "Node" {
			uid = obj.GetName()
		}
	}
	name := obj.GetName() + "." + hash(reason+msg+obj.GetName(), 16)
	src := m{"component": component}
	if component == "kubelet" {
		if node := str(obj, "spec", "nodeName"); node != "" {
			src["host"] = node
		}
	}
	d.add("v1", "Event", ns, name, first, nil, m{
		"involvedObject": m{
			"apiVersion": obj.GetAPIVersion(), "kind": obj.GetKind(), "namespace": obj.GetNamespace(),
			"name": obj.GetName(), "uid": uid, "resourceVersion": "1",
		},
		"type": typ, "reason": reason, "message": msg, "count": count,
		"firstTimestamp": d.ts(first), "lastTimestamp": d.ts(last),
		"source": src, "reportingComponent": component,
	})
}

func ownerRef(owner *unstructured.Unstructured) l {
	return l{m{
		"apiVersion": owner.GetAPIVersion(), "kind": owner.GetKind(), "name": owner.GetName(),
		"uid": string(owner.GetUID()), "controller": true, "blockOwnerDeletion": true,
	}}
}

func hash(s string, n int) string {
	h := fnv.New32a()
	h.Write([]byte(s))
	const chars = "bcdfghjklmnpqrstvwxz2456789"
	v := h.Sum32()
	out := make([]byte, n)
	for i := range out {
		out[i] = chars[v%uint32(len(chars))]
		v = v/uint32(len(chars)) + uint32(i)*7919
	}
	return string(out)
}

// podState is a short description of a demo pod's condition.
type podState string

const (
	running  podState = "Running"
	crash    podState = "CrashLoopBackOff"
	pull     podState = "ImagePullBackOff"
	pending  podState = "Pending"
	complete podState = "Completed"
)

type app struct {
	ns, name, image string
	port            int64
	replicas        int
	states          []podState // per pod; defaults to running
	env             m
	configMap       string
	secret          string
	pvc             string
}

func (a app) labels() map[string]string {
	return map[string]string{"app": a.name, "app.kubernetes.io/part-of": a.ns}
}

func (a app) container() m {
	c := m{
		"name":            a.name,
		"image":           a.image,
		"imagePullPolicy": "IfNotPresent",
		"ports":           l{m{"name": "http", "containerPort": a.port, "protocol": "TCP"}},
		"resources": m{
			"requests": m{"cpu": "100m", "memory": "128Mi"},
			"limits":   m{"memory": "256Mi"},
		},
		"readinessProbe": m{
			"httpGet":             m{"path": "/healthz", "port": "http"},
			"initialDelaySeconds": int64(5),
			"periodSeconds":       int64(10),
		},
	}
	var env l
	for k, v := range a.env {
		env = append(env, m{"name": k, "value": v})
	}
	if a.secret != "" {
		env = append(env, m{"name": "DB_PASSWORD", "valueFrom": m{"secretKeyRef": m{"name": a.secret, "key": "password"}}})
	}
	if env != nil {
		c["env"] = env
	}
	if a.configMap != "" || a.pvc != "" {
		var mounts l
		if a.configMap != "" {
			mounts = append(mounts, m{"name": "config", "mountPath": "/etc/" + a.name, "readOnly": true})
		}
		if a.pvc != "" {
			mounts = append(mounts, m{"name": "data", "mountPath": "/data"})
		}
		c["volumeMounts"] = mounts
	}
	return c
}

func (a app) podSpec() m {
	spec := m{
		"containers":                    l{a.container()},
		"restartPolicy":                 "Always",
		"terminationGracePeriodSeconds": int64(30),
		"serviceAccountName":            "default",
	}
	var vols l
	if a.configMap != "" {
		vols = append(vols, m{"name": "config", "configMap": m{"name": a.configMap}})
	}
	if a.pvc != "" {
		vols = append(vols, m{"name": "data", "persistentVolumeClaim": m{"claimName": a.pvc}})
	}
	if vols != nil {
		spec["volumes"] = vols
	}
	return spec
}

func (a app) state(i int) podState {
	if i < len(a.states) && a.states[i] != "" {
		return a.states[i]
	}
	return running
}

func (a app) readyCount() int64 {
	var n int64
	for i := 0; i < a.replicas; i++ {
		if a.state(i) == running {
			n++
		}
	}
	return n
}

func (d *demo) pod(a app, name string, age time.Duration, owner *unstructured.Unstructured, st podState, node string) {
	spec := a.podSpec()
	status := m{"phase": "Running", "qosClass": "Burstable", "startTime": d.ts(age)}
	ready := st == running
	cs := m{"name": a.name, "image": a.image, "ready": ready, "restartCount": int64(0), "started": ready}
	switch st {
	case running:
		cs["state"] = m{"running": m{"startedAt": d.ts(age)}}
	case crash:
		cs["restartCount"] = int64(17)
		cs["state"] = m{"waiting": m{"reason": "CrashLoopBackOff", "message": "back-off 5m0s restarting failed container"}}
		cs["lastState"] = m{"terminated": m{"exitCode": int64(1), "reason": "Error", "finishedAt": d.ts(3 * time.Minute)}}
	case pull:
		cs["state"] = m{"waiting": m{"reason": "ImagePullBackOff", "message": "Back-off pulling image \"" + a.image + "\""}}
		status["phase"] = "Pending"
	case pending:
		status["phase"] = "Pending"
		status["conditions"] = l{m{"type": "PodScheduled", "status": "False", "reason": "Unschedulable",
			"message": "0/3 nodes are available: 3 Insufficient memory."}}
		cs = nil
		node = ""
	case complete:
		status["phase"] = "Succeeded"
		cs["state"] = m{"terminated": m{"exitCode": int64(0), "reason": "Completed", "finishedAt": d.ts(age - time.Minute)}}
		cs["ready"] = false
	}
	if cs != nil {
		status["containerStatuses"] = l{cs}
		status["podIP"] = fmt.Sprintf("10.244.%d.%d", len(d.objs)%3+1, len(d.objs)%250+2)
		status["hostIP"] = "192.168.1.1" + node[len(node)-1:]
		r := "True"
		if !ready {
			r = "False"
		}
		status["conditions"] = l{
			m{"type": "Initialized", "status": "True", "lastTransitionTime": d.ts(age)},
			m{"type": "Ready", "status": r, "lastTransitionTime": d.ts(age)},
			m{"type": "ContainersReady", "status": r, "lastTransitionTime": d.ts(age)},
			m{"type": "PodScheduled", "status": "True", "lastTransitionTime": d.ts(age)},
		}
	}
	if node != "" {
		spec["nodeName"] = node
	}
	labels := a.labels()
	labels["pod-template-hash"] = hash(a.name, 10)
	p := d.add("v1", "Pod", a.ns, name, age, labels, m{"spec": spec, "status": status})
	if owner != nil {
		p.Object["metadata"].(m)["ownerReferences"] = ownerRef(owner)
	}
	d.podEvents(p, a, st, age)
}

// podEvents records what the scheduler and kubelet would report for a pod in
// state st.
func (d *demo) podEvents(p *unstructured.Unstructured, a app, st podState, age time.Duration) {
	switch st {
	case crash:
		d.event(p, "Normal", "Pulled", "Container image \""+a.image+"\" already present on machine", 17, 70*time.Minute, 4*time.Minute, "kubelet")
		d.event(p, "Normal", "Created", "Created container: "+a.name, 17, 70*time.Minute, 4*time.Minute, "kubelet")
		d.event(p, "Normal", "Started", "Started container "+a.name, 17, 70*time.Minute, 4*time.Minute, "kubelet")
		d.event(p, "Warning", "Unhealthy", "Readiness probe failed: Get \"http://"+str(p, "status", "podIP")+":8080/healthz\": dial tcp: connect: connection refused", 34, 70*time.Minute, 4*time.Minute, "kubelet")
		d.event(p, "Warning", "BackOff", "Back-off restarting failed container "+a.name+" in pod "+p.GetName()+"_"+p.GetNamespace(), 312, 68*time.Minute, 40*time.Second, "kubelet")
	case pull:
		d.event(p, "Normal", "Scheduled", "Successfully assigned "+p.GetNamespace()+"/"+p.GetName()+" to "+str(p, "spec", "nodeName"), 1, age, age, "default-scheduler")
		d.event(p, "Normal", "Pulling", "Pulling image \""+a.image+"\"", 24, age, 6*time.Minute, "kubelet")
		d.event(p, "Warning", "Failed", "Failed to pull image \""+a.image+"\": rpc error: code = NotFound desc = failed to pull and unpack image \""+a.image+"\": not found", 24, age, 6*time.Minute, "kubelet")
		d.event(p, "Warning", "Failed", "Error: ErrImagePull", 24, age, 6*time.Minute, "kubelet")
		d.event(p, "Normal", "BackOff", "Back-off pulling image \""+a.image+"\"", 510, age, 20*time.Second, "kubelet")
		d.event(p, "Warning", "Failed", "Error: ImagePullBackOff", 510, age, 20*time.Second, "kubelet")
	case pending:
		d.event(p, "Warning", "FailedScheduling", "0/3 nodes are available: 3 Insufficient memory. preemption: 0/3 nodes are available: 3 No preemption victims found for incoming pod.", 41, 3*time.Hour, 90*time.Second, "default-scheduler")
	case complete:
		d.event(p, "Normal", "Scheduled", "Successfully assigned "+p.GetNamespace()+"/"+p.GetName()+" to "+str(p, "spec", "nodeName"), 1, age, age, "default-scheduler")
		d.event(p, "Normal", "Started", "Started container "+a.name, 1, age, age, "kubelet")
	}
}

func (d *demo) nodeName(i int) string {
	return fmt.Sprintf("%s-node-%d", d.cluster[len("demo-"):], i%3+1)
}

func (d *demo) deployment(a app, age time.Duration) {
	labels := a.labels()
	replicas := int64(a.replicas)
	ready := a.readyCount()
	dep := d.add("apps/v1", "Deployment", a.ns, a.name, age, labels, m{
		"spec": m{
			"replicas": replicas,
			"selector": m{"matchLabels": m{"app": a.name}},
			"strategy": m{"type": "RollingUpdate", "rollingUpdate": m{"maxSurge": "25%", "maxUnavailable": "25%"}},
			"template": m{
				"metadata": m{"labels": m{"app": a.name}},
				"spec":     a.podSpec(),
			},
		},
		"status": m{
			"observedGeneration": int64(3), "replicas": replicas, "updatedReplicas": replicas,
			"readyReplicas": ready, "availableReplicas": ready,
			"conditions": l{
				m{"type": "Available", "status": fmt.Sprint(ready > 0 && ready == replicas), "reason": "MinimumReplicasAvailable"},
				m{"type": "Progressing", "status": "True", "reason": "NewReplicaSetAvailable"},
			},
		},
	})
	dep.SetAnnotations(map[string]string{"deployment.kubernetes.io/revision": "3"})
	dep.SetGeneration(3)

	rsName := a.name + "-" + hash(a.name, 10)
	rs := d.add("apps/v1", "ReplicaSet", a.ns, rsName, age/2, labels, m{
		"spec": m{
			"replicas": replicas,
			"selector": m{"matchLabels": m{"app": a.name}},
			"template": m{"metadata": m{"labels": m{"app": a.name}}, "spec": a.podSpec()},
		},
		"status": m{"replicas": replicas, "readyReplicas": ready, "availableReplicas": ready},
	})
	rs.Object["metadata"].(m)["ownerReferences"] = ownerRef(dep)

	if age < 24*time.Hour {
		d.event(dep, "Normal", "ScalingReplicaSet", fmt.Sprintf("Scaled up replica set %s from 0 to %d", rsName, replicas), 1, age/2, age/2, "deployment-controller")
	}
	for i := 0; i < a.replicas; i++ {
		d.pod(a, rsName+"-"+hash(fmt.Sprint(a.name, i), 5), age/2-time.Duration(i)*time.Hour, rs, a.state(i), d.nodeName(i))
	}
}

func (d *demo) statefulSet(a app, age time.Duration) {
	replicas := int64(a.replicas)
	sts := d.add("apps/v1", "StatefulSet", a.ns, a.name, age, a.labels(), m{
		"spec": m{
			"replicas":    replicas,
			"serviceName": a.name,
			"selector":    m{"matchLabels": m{"app": a.name}},
			"template":    m{"metadata": m{"labels": m{"app": a.name}}, "spec": a.podSpec()},
		},
		"status": m{"replicas": replicas, "readyReplicas": a.readyCount(), "updatedReplicas": replicas, "availableReplicas": a.readyCount()},
	})
	for i := 0; i < a.replicas; i++ {
		d.pod(a, fmt.Sprintf("%s-%d", a.name, i), age, sts, a.state(i), d.nodeName(i))
	}
}

func (d *demo) daemonSet(a app, age time.Duration) {
	ds := d.add("apps/v1", "DaemonSet", a.ns, a.name, age, a.labels(), m{
		"spec": m{
			"selector": m{"matchLabels": m{"app": a.name}},
			"template": m{"metadata": m{"labels": m{"app": a.name}}, "spec": a.podSpec()},
		},
		"status": m{"desiredNumberScheduled": int64(3), "currentNumberScheduled": int64(3), "numberReady": int64(3)},
	})
	for i := 0; i < 3; i++ {
		d.pod(a, a.name+"-"+hash(fmt.Sprint(a.name, i), 5), age, ds, running, d.nodeName(i))
	}
}

func (d *demo) service(ns, name, typ string, port int64, selector string) {
	spec := m{
		"type":      typ,
		"clusterIP": fmt.Sprintf("10.96.%d.%d", len(d.objs)%200, len(d.objs)%250+1),
		"ports":     l{m{"name": "http", "port": port, "targetPort": "http", "protocol": "TCP"}},
	}
	if selector != "" {
		spec["selector"] = m{"app": selector}
	}
	if typ == "LoadBalancer" {
		spec["ports"].(l)[0].(m)["nodePort"] = int64(31080)
	}
	if typ == "Headless" {
		spec["type"] = "ClusterIP"
		spec["clusterIP"] = "None"
	}
	d.add("v1", "Service", ns, name, 30*24*time.Hour, map[string]string{"app": selector}, m{"spec": spec})
}

func buildDemoCluster(name string) []runtime.Object {
	d := &demo{cluster: name, now: time.Now()}
	prod := name == "demo-prod"
	day := 24 * time.Hour

	nss := []string{"default", "kube-system", "shop", "monitoring", "payments"}
	if prod {
		nss = []string{"default", "kube-system", "shop", "payments"}
	}
	for _, ns := range nss {
		d.add("v1", "Namespace", "", ns, 90*day, map[string]string{"kubernetes.io/metadata.name": ns},
			m{"spec": m{"finalizers": l{"kubernetes"}}, "status": m{"phase": "Active"}})
	}

	for i := 0; i < 3; i++ {
		labels := map[string]string{"kubernetes.io/hostname": d.nodeName(i), "kubernetes.io/os": "linux"}
		if i == 0 {
			labels["node-role.kubernetes.io/control-plane"] = ""
		}
		ready := "True"
		if !prod && i == 2 {
			ready = "False"
		}
		node := d.add("v1", "Node", "", d.nodeName(i), 90*day, labels, m{
			"spec": m{"podCIDR": fmt.Sprintf("10.244.%d.0/24", i)},
			"status": m{
				"capacity":    m{"cpu": "8", "memory": "32Gi", "pods": "110"},
				"allocatable": m{"cpu": "7800m", "memory": "31Gi", "pods": "110"},
				"conditions": l{
					m{"type": "MemoryPressure", "status": "False"},
					m{"type": "DiskPressure", "status": "False"},
					m{"type": "Ready", "status": ready, "reason": "KubeletReady"},
				},
				"nodeInfo": m{"kubeletVersion": "v1.37.1", "osImage": "Ubuntu 26.04 LTS", "containerRuntimeVersion": "containerd://2.2.0"},
			},
		})
		if ready == "False" {
			d.event(node, "Normal", "NodeNotReady", "Node "+node.GetName()+" status is now: NodeNotReady", 1, 25*time.Minute, 25*time.Minute, "node-controller")
			d.event(node, "Warning", "ContainerGCFailed", "rpc error: code = Unavailable desc = connection error: dial unix /run/containerd/containerd.sock: connect: no such file or directory", 25, 25*time.Minute, time.Minute, "kubelet")
		}
	}

	d.service("default", "kubernetes", "ClusterIP", 443, "")

	// kube-system
	d.deployment(app{ns: "kube-system", name: "coredns", image: "registry.k8s.io/coredns/coredns:v1.12.0", port: 53, replicas: 2, configMap: "coredns"}, 90*day)
	d.daemonSet(app{ns: "kube-system", name: "kube-proxy", image: "registry.k8s.io/kube-proxy:v1.37.1", port: 10256}, 90*day)
	d.add("v1", "ConfigMap", "kube-system", "coredns", 90*day, nil, m{"data": m{
		"Corefile": ".:53 {\n    errors\n    health\n    kubernetes cluster.local in-addr.arpa ip6.arpa {\n       pods insecure\n       fallthrough in-addr.arpa ip6.arpa\n    }\n    forward . /etc/resolv.conf\n    cache 30\n    loop\n    reload\n}\n",
	}})

	// shop
	frontendReplicas, catalogStates := 3, []podState{running, crash}
	if prod {
		frontendReplicas, catalogStates = 6, nil
	}
	d.deployment(app{ns: "shop", name: "frontend", image: "ghcr.io/acme/frontend:2.4.1", port: 8080, replicas: frontendReplicas,
		configMap: "frontend-config", env: m{"CART_URL": "http://cart:8080", "CATALOG_URL": "http://catalog:8080"}}, 45*day)
	d.deployment(app{ns: "shop", name: "cart", image: "ghcr.io/acme/cart:1.9.0", port: 8080, replicas: 2, secret: "cart-db",
		env: m{"REDIS_ADDR": "redis:6379"}}, 45*day)
	d.deployment(app{ns: "shop", name: "catalog", image: "ghcr.io/acme/catalog:3.0.0-rc2", port: 8080, replicas: 2, states: catalogStates}, 12*day)
	d.statefulSet(app{ns: "shop", name: "redis", image: "redis:7.4-alpine", port: 6379, replicas: 1, pvc: "data-redis-0"}, 60*day)
	d.service("shop", "frontend", "LoadBalancer", 80, "frontend")
	d.service("shop", "cart", "ClusterIP", 8080, "cart")
	d.service("shop", "catalog", "ClusterIP", 8080, "catalog")
	d.service("shop", "redis", "Headless", 6379, "redis")
	d.add("v1", "ConfigMap", "shop", "frontend-config", 45*day, map[string]string{"app": "frontend"}, m{"data": m{
		"LOG_LEVEL":  "info",
		"FEATURE_X":  "true",
		"nginx.conf": "worker_processes auto;\nevents {\n  worker_connections 1024;\n}\nhttp {\n  server {\n    listen 8080;\n    location / {\n      root /usr/share/nginx/html;\n    }\n    location /api/ {\n      proxy_pass http://cart:8080/;\n    }\n  }\n}\n",
	}})
	d.add("v1", "Secret", "shop", "cart-db", 45*day, nil, m{"type": "Opaque", "data": m{
		"username": "Y2FydA==", "password": "czNjcjN0LXBhc3N3b3Jk",
	}})
	d.add("v1", "Secret", "shop", "shop-tls", 20*day, nil, m{"type": "kubernetes.io/tls", "data": m{
		"tls.crt": "LS0tLS1CRUdJTi...", "tls.key": "LS0tLS1CRUdJTi...",
	}})
	d.add("networking.k8s.io/v1", "Ingress", "shop", "shop", 45*day, nil, m{"spec": m{
		"ingressClassName": "nginx",
		"tls":              l{m{"hosts": l{"shop.example.com"}, "secretName": "shop-tls"}},
		"rules": l{m{"host": "shop.example.com", "http": m{"paths": l{
			m{"path": "/", "pathType": "Prefix", "backend": m{"service": m{"name": "frontend", "port": m{"number": int64(80)}}}},
		}}}},
	}})
	d.add("networking.k8s.io/v1", "NetworkPolicy", "shop", "default-deny", 30*day, nil, m{"spec": m{
		"podSelector": m{}, "policyTypes": l{"Ingress"},
	}})
	d.add("networking.k8s.io/v1", "NetworkPolicy", "shop", "allow-frontend-to-cart", 30*day, nil, m{"spec": m{
		"podSelector": m{"matchLabels": m{"app": "cart"}},
		"policyTypes": l{"Ingress"},
		"ingress": l{m{
			"from":  l{m{"podSelector": m{"matchLabels": m{"app": "frontend"}}}},
			"ports": l{m{"protocol": "TCP", "port": int64(8080)}},
		}},
	}})
	d.add("v1", "PersistentVolumeClaim", "shop", "data-redis-0", 60*day, map[string]string{"app": "redis"}, m{
		"spec":   m{"accessModes": l{"ReadWriteOnce"}, "storageClassName": "standard", "volumeName": "pvc-" + hash("redis", 8), "resources": m{"requests": m{"storage": "10Gi"}}},
		"status": m{"phase": "Bound", "capacity": m{"storage": "10Gi"}},
	})
	d.add("v1", "PersistentVolume", "", "pvc-"+hash("redis", 8), 60*day, nil, m{
		"spec":   m{"capacity": m{"storage": "10Gi"}, "storageClassName": "standard", "claimRef": m{"namespace": "shop", "name": "data-redis-0"}},
		"status": m{"phase": "Bound"},
	})
	d.add("storage.k8s.io/v1", "StorageClass", "", "standard", 90*day, nil, m{
		"provisioner": "rancher.io/local-path", "reclaimPolicy": "Delete", "volumeBindingMode": "WaitForFirstConsumer",
	})
	d.add("autoscaling/v2", "HorizontalPodAutoscaler", "shop", "frontend", 45*day, nil, m{
		"spec": m{
			"scaleTargetRef": m{"apiVersion": "apps/v1", "kind": "Deployment", "name": "frontend"},
			"minReplicas":    int64(2), "maxReplicas": int64(10),
			"metrics": l{m{"type": "Resource", "resource": m{"name": "cpu", "target": m{"type": "Utilization", "averageUtilization": int64(70)}}}},
		},
		"status": m{"currentReplicas": int64(frontendReplicas), "desiredReplicas": int64(frontendReplicas)},
	})
	report := app{ns: "shop", name: "nightly-report", image: "ghcr.io/acme/report:1.0.0", port: 9000}
	cj := d.add("batch/v1", "CronJob", "shop", "nightly-report", 30*day, nil, m{
		"spec":   m{"schedule": "0 2 * * *", "suspend": false, "jobTemplate": m{"spec": m{"template": m{"spec": report.podSpec()}}}},
		"status": m{"lastScheduleTime": d.ts(9 * time.Hour)},
	})
	job := d.add("batch/v1", "Job", "shop", "nightly-report-29312", 9*time.Hour, nil, m{
		"spec":   m{"completions": int64(1), "template": m{"spec": report.podSpec()}},
		"status": m{"succeeded": int64(1), "conditions": l{m{"type": "Complete", "status": "True"}}},
	})
	job.Object["metadata"].(m)["ownerReferences"] = ownerRef(cj)
	d.event(cj, "Normal", "SuccessfulCreate", "Created job "+job.GetName(), 1, 9*time.Hour, 9*time.Hour, "cronjob-controller")
	d.event(cj, "Normal", "SawCompletedJob", "Saw completed job: "+job.GetName()+", condition: Complete", 1, 9*time.Hour-time.Minute, 9*time.Hour-time.Minute, "cronjob-controller")
	d.event(job, "Normal", "Completed", "Job completed", 1, 9*time.Hour-time.Minute, 9*time.Hour-time.Minute, "job-controller")
	d.pod(report, "nightly-report-29312-"+hash("job", 5), 9*time.Hour, job, complete, d.nodeName(1))

	// monitoring
	if !prod {
		d.statefulSet(app{ns: "monitoring", name: "prometheus", image: "quay.io/prometheus/prometheus:v3.5.0", port: 9090, replicas: 2, configMap: "prometheus"}, 80*day)
		d.deployment(app{ns: "monitoring", name: "grafana", image: "grafana/grafana:12.1.0", port: 3000, replicas: 1}, 80*day)
		d.add("v1", "ConfigMap", "monitoring", "prometheus", 80*day, nil, m{"data": m{
			"prometheus.yml": "global:\n  scrape_interval: 30s\nscrape_configs:\n  - job_name: kubernetes-pods\n    kubernetes_sd_configs:\n      - role: pod\n",
		}})
	}

	// payments
	paymentStates := []podState{running, pending}
	if prod {
		paymentStates = nil
	}
	d.deployment(app{ns: "payments", name: "payments-api", image: "ghcr.io/acme/payments:5.2.0", port: 8443, replicas: 2,
		states: paymentStates, secret: "stripe"}, 20*day)
	d.deployment(app{ns: "payments", name: "fraud-check", image: "ghcr.io/acme/fraud-check:0.9.1-typo", port: 8080, replicas: 1,
		states: []podState{pull}}, 2*time.Hour)
	d.service("payments", "payments-api", "ClusterIP", 8443, "payments-api")
	d.add("v1", "Secret", "payments", "stripe", 20*day, nil, m{"type": "Opaque", "data": m{"password": "c2tfdGVzdF9kZW1v"}})

	for _, ns := range nss {
		d.add("v1", "ServiceAccount", ns, "default", 90*day, nil, nil)
	}
	return d.objs
}
