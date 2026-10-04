# Custom resources

**Status (2026-10-04):** implemented as described, except the "Later" section. Differences from the plan: the demo adds a Prometheus kind (integer columns, the `Prometheuses` title); `date` columns in the future render as "in 3d"; relations take a `Registry` so owners and event objects of custom kinds resolve.

Design for the "API discovery and CRDs" item in [plan.md](plan.md), written 2026-10-04 before implementing. Decisions are marked **(decided)**. The rest is the planned approach and can change during implementation.

## UX

### Navigator (decided)

A folded **Custom Resources** category sits under every namespace and under All namespaces, after Access Control and before Events. Inside it, one folded folder per API group, sorted by name, and the kinds of that group inside, sorted by title. Cluster-scoped CRDs go into the same structure under **Cluster**, after the builtin cluster resources.

```
▾ ⎈ demo-dev
  ▾ Cluster
      Nodes
      …
      CRDs
    ▾ Custom Resources
      ▸ cert-manager.io
  ▸ All namespaces
  ▾ shop
    ▸ Workloads
    ▸ Network
    ▸ Config
    ▸ Storage
    ▸ Access Control
    ▾ Custom Resources
      ▾ cert-manager.io
          Certificates 2
          Issuers
      ▸ monitoring.coreos.com
      Events 10
```

- **Well-known groups go into builtin categories (decided).** A small table maps groups whose kinds fit a builtin category: `gateway.networking.k8s.io`, `networking.istio.io`, `traefik.io`, `cilium.io`, `linkerd.io` → Network (an entry also covers its subdomains, so `policy.linkerd.io` too); `snapshot.storage.k8s.io` → Storage; `keda.sh` → Workloads; `external-secrets.io` → Config. Their kinds are listed flat after the builtins of that category (no group folder). Cluster-scoped kinds of a mapped group go flat into Cluster. Each CRD appears in exactly one place.
- The category and group folders are only added when they have kinds. A cluster without CRDs looks exactly as today.
- Fold state of the category and group folders survives CRD refreshes, like namespaces.
- Listing CRDs failing (RBAC) shows an `error: …` info row under Cluster › Custom Resources, so the user sees why nothing is there.
- Kind titles are the plural with the kind's casing: `Certificate` + `certificates` → `Certificates`, `NetworkPolicy` + `networkpolicies` → `NetworkPolicies`, `Prometheus` + `prometheuses` → `Prometheuses`.

### Opening instances from a CRD (decided)

In the CRDs list, **`o`** on a row opens the list of its instances: in the current namespace for namespaced CRDs (all namespaces if that's the current view), cluster-wide otherwise. The navigator reveals the kind. **Double-click** on a CRD row does the same (on other tables it keeps opening the details). The status bar shows `o instances` while the CRDs list has focus, and the help overlay lists it. `o` is meant to become "open related" for other kinds later (the drill-down open question).

### CRDs table

The builtin `customresourcedefinitions` list gets real columns: NAME, GROUP, KIND, SCOPE, VERSIONS (served versions, storage version first), AGE. GROUP drops first, then VERSIONS.

### Instance tables

- Columns come from the version's `additionalPrinterColumns`: NAME, then the printer columns in order, then AGE if no printer column already shows `.metadata.creationTimestamp`. Without printer columns: NAME, AGE (as today).
- JSONPath through `k8s.io/client-go/util/jsonpath`, so filters like `.status.conditions[?(@.type=="Ready")].status` work. Missing values render empty, like kubectl.
- `type: integer|number` sort numerically; `type: date` renders as an age (`3d`) and sorts by time like AGE; `boolean` and `string` as text.
- `priority > 0` columns are hideable (`Column.Drop = priority`), so they go first when space runs out, like `kubectl get` hiding them without `-o wide`.
- Column names are upper-cased like kubectl.

### Command palette

`:` lists every custom resource of the current context next to the builtins: `cmd` is the plural, the description is `Title · group · short names`, and aliases are the short names, singular, kind and `plural.group`. Builtins win on name clashes; a custom resource whose plural clashes (with a builtin or another group) is offered as `plural.group` only. `:certificates`, `:cert`, `:certificate.cert-manager.io` all resolve. `:<res> <ns>` works like for builtins.

### Everything else just works

The detail YAML, favorites/hidden fields (per GroupKind), `e` editing, `ctrl+n` and `i` (cluster OpenAPI already includes CRDs), `E` events and the ⚠ markers, `d` (owners via ownerReferences), pins and `1`–`9`, the breadcrumb and the header ☆ all run on the generic Resource/Key path. The plan is to check each in the demo rather than add code.

## Implementation

### Data layer (`internal/k8s`)

- `Resource` gets `Custom bool`, `ShortNames`/`Singular` (folded into `Aliases`), `Printer []PrinterColumn`, and `ID()`: the plural for builtins, `plural.group` for custom resources. `ID()` is what pins store and what navigator matching uses; `Name` stays the plural because `GVR()` needs it.
- `crd.go`: `CustomResources(items []unstructured.Unstructured) []Resource` parses CRD objects: group, kind, plural, singular, short names, scope, the version (storage version if served, else the first served), its printer columns, and the category (mapped group or `CatCustom`). Skips CRDs that aren't `Established` only if the condition is present and False.
- `Registry`: builtins plus one context's custom resources, with `Lookup(name)` (plural, ID, kind, title, aliases, case-insensitive; builtins first), `InCategory`, and `Groups(cat)` for the Custom Resources folders. `k8s.Lookup`/`InCategory` stay for builtins-only callers.
- The source is the existing Store: the CRD list is just the `Key{ctx, crdGVR, ""}` entry, so the CRDs table and the registry share one cache and one refresh. The list is fetched once when a context is expanded and kept for a long TTL; it is not refreshed on the tick, only by `r` in the CRDs list (item 12 in [network.md](network.md)). `Store.Registry(ctx)` builds (and memoizes per FetchedAt) a Registry from that entry.
- `Columns(r)` returns printer columns for custom resources (`printer.go`) and the new CRD columns for `customresourcedefinitions`.
- Demo: CRDs and instances for `cert-manager.io` (Certificate, Issuer, ClusterIssuer), `monitoring.coreos.com` (ServiceMonitor, PrometheusRule) and `gateway.networking.k8s.io` (Gateway, HTTPRoute, GatewayClass; mapped to Network/Cluster), with printer columns including a filter JSONPath, a date and a priority-1 column. The fake dynamic client needs their list kinds registered.

### UI (`internal/ui`)

- Fetch the CRD list when a context's namespaces are fetched (same trigger), and whenever that entry is refreshed; on arrival, `navView.SetCustom(ctx, registry)` rebuilds the custom nodes of that context's namespace, All namespaces and Cluster nodes, keeping fold state.
- Every `k8s.Lookup(name)` in the UI that can see a custom resource (pins, `runCommand`, current-table lookups) goes through `a.registry(ctx).Lookup`. Pins of custom resources whose context's CRDs aren't loaded yet are shown once they load; contexts with such pins get their CRDs fetched at startup.
- `o` and double-click on the CRDs table; status-bar hint and help entry.
- Palette items from the registry.

### Tests

- CRD parsing: version choice, scope, titles, short names, mapped categories, printer columns.
- Printer columns: JSONPath with a filter, missing fields, integer sort, date as age, priority → Drop, AGE appended or not.
- Registry lookup: builtin wins, clashing plurals need `plural.group`, aliases.
- Demo store lists every custom resource; nav tree has the Custom Resources folders and the mapped kinds under Network.

## Later

- Full API discovery (`ServerPreferredResources`) for builtins without columns and aggregated APIs, falling back to it when listing CRDs is forbidden.
- Schema for `i`/`ctrl+n` from the CRD's own `openAPIV3Schema` when the cluster's OpenAPI is unavailable (would also make it work in the demo).
- `d` on a CRD: instance counts per namespace. Relations by selector for more well-known CRDs (ServiceMonitor → services). Done: cert-manager, Cilium, Linkerd Server and ServiceProfile, Gateway ↔ HTTPRoute → services (see status.md).
- Show only kinds that have instances in a namespace (needs a list per kind; maybe as a toggle).
