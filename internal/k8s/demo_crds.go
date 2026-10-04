package k8s

import (
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// crdSpec describes a demo CRD. versions are served; the first is the
// storage version and has the printer columns.
type crdSpec struct {
	group, kind, plural string
	short               []string
	cluster             bool
	versions            []string
	cols                l
}

func printerCol(name, typ, path string, priority int64) m {
	c := m{"name": name, "type": typ, "jsonPath": path}
	if priority > 0 {
		c["priority"] = priority
	}
	return c
}

func ageCol() m { return printerCol("Age", "date", ".metadata.creationTimestamp", 0) }

func condCol(name, typ string, priority int64) m {
	return printerCol(name, "string", `.status.conditions[?(@.type=="`+typ+`")].`+map[bool]string{true: "message", false: "status"}[priority > 0], priority)
}

func (d *demo) crd(c crdSpec, age time.Duration) {
	scope := "Namespaced"
	if c.cluster {
		scope = "Cluster"
	}
	singular := strings.ToLower(c.kind)
	var versions l
	for i, v := range c.versions {
		ver := m{
			"name": v, "served": true, "storage": i == 0,
			"schema":       m{"openAPIV3Schema": m{"type": "object", "x-kubernetes-preserve-unknown-fields": true}},
			"subresources": m{"status": m{}},
		}
		if i == 0 && c.cols != nil {
			ver["additionalPrinterColumns"] = c.cols
		}
		versions = append(versions, ver)
	}
	names := m{"kind": c.kind, "listKind": c.kind + "List", "plural": c.plural, "singular": singular}
	if c.short != nil {
		var short l
		for _, s := range c.short {
			short = append(short, s)
		}
		names["shortNames"] = short
	}
	d.add("apiextensions.k8s.io/v1", "CustomResourceDefinition", "", c.plural+"."+c.group, age, nil, m{
		"spec": m{"group": c.group, "names": names, "scope": scope, "versions": versions},
		"status": m{
			"acceptedNames":  names,
			"storedVersions": l{c.versions[0]},
			"conditions": l{
				m{"type": "NamesAccepted", "status": "True", "reason": "NoConflicts", "lastTransitionTime": d.ts(age)},
				m{"type": "Established", "status": "True", "reason": "InitialNamesAccepted", "lastTransitionTime": d.ts(age)},
			},
		},
	})
}

func readyCond(status, reason, msg string, age time.Duration, d *demo) l {
	return l{m{"type": "Ready", "status": status, "reason": reason, "message": msg, "lastTransitionTime": d.ts(age)}}
}

// customResources adds cert-manager, Prometheus operator and Gateway API
// CRDs with a few instances. The Prometheus operator runs in demo-dev only.
func (d *demo) customResources(prod bool) {
	day := 24 * time.Hour
	cm := "cert-manager.io"
	d.crd(crdSpec{group: cm, kind: "Certificate", plural: "certificates", short: []string{"cert", "certs"}, versions: []string{"v1"}, cols: l{
		condCol("Ready", "Ready", 0),
		printerCol("Secret", "string", ".spec.secretName", 0),
		printerCol("Issuer", "string", ".spec.issuerRef.name", 1),
		condCol("Status", "Ready", 1),
		printerCol("Expires", "date", ".status.notAfter", 1),
		ageCol(),
	}}, 80*day)
	d.crd(crdSpec{group: cm, kind: "Issuer", plural: "issuers", versions: []string{"v1"}, cols: l{
		condCol("Ready", "Ready", 0), condCol("Status", "Ready", 1), ageCol(),
	}}, 80*day)
	d.crd(crdSpec{group: cm, kind: "ClusterIssuer", plural: "clusterissuers", cluster: true, versions: []string{"v1"}, cols: l{
		condCol("Ready", "Ready", 0), condCol("Status", "Ready", 1), ageCol(),
	}}, 80*day)

	gw := "gateway.networking.k8s.io"
	d.crd(crdSpec{group: gw, kind: "GatewayClass", plural: "gatewayclasses", short: []string{"gc"}, cluster: true, versions: []string{"v1", "v1beta1"}, cols: l{
		printerCol("Controller", "string", ".spec.controllerName", 0),
		condCol("Accepted", "Accepted", 0),
		ageCol(),
		printerCol("Description", "string", ".spec.description", 1),
	}}, 60*day)
	d.crd(crdSpec{group: gw, kind: "Gateway", plural: "gateways", short: []string{"gtw"}, versions: []string{"v1", "v1beta1"}, cols: l{
		printerCol("Class", "string", ".spec.gatewayClassName", 0),
		printerCol("Address", "string", ".status.addresses[*].value", 0),
		condCol("Programmed", "Programmed", 0),
		ageCol(),
	}}, 60*day)
	d.crd(crdSpec{group: gw, kind: "HTTPRoute", plural: "httproutes", versions: []string{"v1", "v1beta1"}, cols: l{
		printerCol("Hostnames", "string", ".spec.hostnames", 0),
		ageCol(),
	}}, 60*day)

	// Instances.
	for _, name := range []string{"letsencrypt-prod", "letsencrypt-staging"} {
		d.add(cm+"/v1", "ClusterIssuer", "", name, 80*day, nil, m{
			"spec":   m{"acme": m{"server": "https://acme-v02.api.letsencrypt.org/directory", "privateKeySecretRef": m{"name": name}}},
			"status": m{"conditions": readyCond("True", "ACMEAccountRegistered", "The ACME account was registered with the ACME server", 80*day, d)},
		})
	}
	d.add(cm+"/v1", "Issuer", "shop", "selfsigned", 40*day, nil, m{
		"spec":   m{"selfSigned": m{}},
		"status": m{"conditions": readyCond("True", "IsReady", "", 40*day, d)},
	})
	cert := d.add(cm+"/v1", "Certificate", "shop", "shop-tls", 20*day, nil, m{
		"spec": m{"secretName": "shop-tls", "dnsNames": l{"shop.example.com"},
			"issuerRef": m{"name": "letsencrypt-prod", "kind": "ClusterIssuer", "group": cm}},
		"status": m{
			"conditions": readyCond("True", "Ready", "Certificate is up to date and has not expired", 20*day, d),
			"notAfter":   d.now.Add(-20 * day).Add(90 * day).UTC().Format(time.RFC3339),
		},
	})
	// cert-manager can own the secrets it issues.
	if sec := d.find("Secret", "shop", "shop-tls"); sec != nil {
		sec.Object["metadata"].(m)["ownerReferences"] = ownerRef(cert)
	}
	certReady, certReason, certMsg := "False", "DoesNotExist", "Issuing certificate as Secret does not exist"
	if prod {
		certReady, certReason, certMsg = "True", "Ready", "Certificate is up to date and has not expired"
	}
	pay := d.add(cm+"/v1", "Certificate", "payments", "payments-api-tls", 2*time.Hour, nil, m{
		"spec": m{"secretName": "payments-api-tls", "dnsNames": l{"payments.internal"},
			"issuerRef": m{"name": "letsencrypt-staging", "kind": "ClusterIssuer", "group": cm}},
		"status": m{"conditions": readyCond(certReady, certReason, certMsg, 2*time.Hour, d)},
	})
	if !prod {
		d.event(pay, "Warning", "Failed", "The certificate request has failed to complete and will be retried: Failed to wait for order resource \"payments-api-tls-1-2381\" to become ready: order is in \"invalid\" state: urn:ietf:params:acme:error:dns: DNS problem: NXDOMAIN looking up A for payments.internal", 6, 2*time.Hour, 12*time.Minute, "cert-manager-certificates-issuing")
	}

	d.add(gw+"/v1", "GatewayClass", "", "nginx", 60*day, nil, m{
		"spec":   m{"controllerName": "gateway.nginx.org/nginx-gateway-controller", "description": "NGINX Gateway Fabric"},
		"status": m{"conditions": l{m{"type": "Accepted", "status": "True", "reason": "Accepted"}}},
	})
	d.add(gw+"/v1", "Gateway", "shop", "shop-gateway", 30*day, nil, m{
		"spec": m{"gatewayClassName": "nginx", "listeners": l{
			m{"name": "https", "port": int64(443), "protocol": "HTTPS", "hostname": "shop.example.com",
				"tls": m{"certificateRefs": l{m{"name": "shop-tls"}}}},
		}},
		"status": m{
			"addresses":  l{m{"type": "IPAddress", "value": "203.0.113.10"}},
			"conditions": l{m{"type": "Programmed", "status": "True", "reason": "Programmed"}},
		},
	})
	d.add(gw+"/v1", "HTTPRoute", "shop", "shop", 30*day, nil, m{"spec": m{
		"parentRefs": l{m{"name": "shop-gateway"}},
		"hostnames":  l{"shop.example.com"},
		"rules": l{m{
			"matches":     l{m{"path": m{"type": "PathPrefix", "value": "/"}}},
			"backendRefs": l{m{"name": "frontend", "port": int64(80)}},
		}},
	}})

	// Cilium and Linkerd: mapped into Network, Linkerd from two groups.
	cil := "cilium.io"
	validCol := printerCol("Valid", "string", `.status.conditions[?(@.type=="Valid")].status`, 0)
	d.crd(crdSpec{group: cil, kind: "CiliumNetworkPolicy", plural: "ciliumnetworkpolicies", short: []string{"cnp", "ciliumnp"},
		versions: []string{"v2"}, cols: l{ageCol(), validCol}}, 70*day)
	d.crd(crdSpec{group: cil, kind: "CiliumClusterwideNetworkPolicy", plural: "ciliumclusterwidenetworkpolicies", short: []string{"ccnp"},
		cluster: true, versions: []string{"v2"}, cols: l{ageCol(), validCol}}, 70*day)
	valid := m{"conditions": l{m{"type": "Valid", "status": "True", "message": "Policy validation succeeded"}}}
	d.add(cil+"/v2", "CiliumNetworkPolicy", "shop", "frontend-ingress", 30*day, nil, m{
		"spec": m{
			"endpointSelector": m{"matchLabels": m{"app": "frontend"}},
			"ingress": l{m{"fromEntities": l{"world"}, "toPorts": l{m{"ports": l{m{"port": "8080", "protocol": "TCP"}},
				"rules": m{"http": l{m{"method": "GET"}, m{"method": "POST", "path": "/api/.*"}}}}}}},
		},
		"status": valid,
	})
	d.add(cil+"/v2", "CiliumNetworkPolicy", "shop", "cart-from-frontend", 30*day, nil, m{
		"spec": m{
			"endpointSelector": m{"matchLabels": m{"app": "cart"}},
			"ingress": l{m{"fromEndpoints": l{m{"matchLabels": m{"app": "frontend"}}},
				"toPorts": l{m{"ports": l{m{"port": "8080", "protocol": "TCP"}}}}}},
		},
		"status": valid,
	})
	d.add(cil+"/v2", "CiliumClusterwideNetworkPolicy", "", "deny-metadata-api", 60*day, nil, m{
		"spec": m{
			"endpointSelector": m{},
			"egressDeny":       l{m{"toCIDR": l{"169.254.169.254/32"}}},
		},
		"status": valid,
	})

	d.crd(crdSpec{group: "linkerd.io", kind: "ServiceProfile", plural: "serviceprofiles", short: []string{"sp"}, versions: []string{"v1alpha2"}}, 50*day)
	d.crd(crdSpec{group: "policy.linkerd.io", kind: "Server", plural: "servers", short: []string{"srv"}, versions: []string{"v1beta3"}, cols: l{
		printerCol("Port", "string", ".spec.port", 0),
		printerCol("Proxy Protocol", "string", ".spec.proxyProtocol", 0),
		ageCol(),
	}}, 50*day)
	d.add("linkerd.io/v1alpha2", "ServiceProfile", "shop", "cart.shop.svc.cluster.local", 20*day, nil, m{"spec": m{"routes": l{
		m{"name": "GET /cart/{id}", "condition": m{"method": "GET", "pathRegex": "/cart/[^/]*"}, "isRetryable": true},
		m{"name": "POST /cart", "condition": m{"method": "POST", "pathRegex": "/cart"}, "timeout": "300ms"},
	}}})
	for _, srv := range []struct{ name, app, proto string }{{"frontend-http", "frontend", "HTTP/1"}, {"cart-grpc", "cart", "gRPC"}} {
		d.add("policy.linkerd.io/v1beta3", "Server", "shop", srv.name, 20*day, nil, m{"spec": m{
			"podSelector": m{"matchLabels": m{"app": srv.app}}, "port": "http", "proxyProtocol": srv.proto,
		}})
	}

	if prod {
		return
	}
	mon := "monitoring.coreos.com"
	d.crd(crdSpec{group: mon, kind: "Prometheus", plural: "prometheuses", short: []string{"prom"}, versions: []string{"v1"}, cols: l{
		printerCol("Version", "string", ".spec.version", 0),
		printerCol("Desired", "integer", ".spec.replicas", 0),
		printerCol("Ready", "integer", ".status.availableReplicas", 0),
		condCol("Reconciled", "Reconciled", 0),
		condCol("Available", "Available", 0),
		ageCol(),
		printerCol("Paused", "boolean", ".status.paused", 1),
	}}, 80*day)
	d.crd(crdSpec{group: mon, kind: "ServiceMonitor", plural: "servicemonitors", short: []string{"smon"}, versions: []string{"v1"}}, 80*day)
	d.crd(crdSpec{group: mon, kind: "PrometheusRule", plural: "prometheusrules", short: []string{"promrule"}, versions: []string{"v1"}}, 80*day)
	d.add(mon+"/v1", "Prometheus", "monitoring", "k8s", 80*day, nil, m{
		"spec": m{"version": "v3.5.0", "replicas": int64(2), "serviceMonitorSelector": m{}},
		"status": m{"availableReplicas": int64(2), "paused": false, "conditions": l{
			m{"type": "Available", "status": "True"}, m{"type": "Reconciled", "status": "True"},
		}},
	})
	for _, sm := range []struct{ ns, name string }{{"monitoring", "frontend"}, {"shop", "cart"}, {"monitoring", "kube-state-metrics"}} {
		d.add(mon+"/v1", "ServiceMonitor", sm.ns, sm.name, 40*day, map[string]string{"release": "k8s"}, m{"spec": m{
			"selector":  m{"matchLabels": m{"app": sm.name}},
			"endpoints": l{m{"port": "http", "interval": "30s"}},
		}})
	}
	d.add(mon+"/v1", "PrometheusRule", "monitoring", "shop-alerts", 40*day, nil, m{"spec": m{"groups": l{m{
		"name": "shop",
		"rules": l{m{"alert": "PodCrashLooping", "expr": `rate(kube_pod_container_status_restarts_total{namespace="shop"}[5m]) > 0`,
			"for": "10m", "labels": m{"severity": "warning"}}},
	}}}})
}

// find returns the demo object of kind in ns named name.
func (d *demo) find(kind, ns, name string) *unstructured.Unstructured {
	for _, o := range d.objs {
		if u, ok := o.(*unstructured.Unstructured); ok && u.GetKind() == kind && u.GetNamespace() == ns && u.GetName() == name {
			return u
		}
	}
	return nil
}
