# Network traffic

**Status (2026-10-09):** items 1–12 are done (see status.md). 6 is opt-in as live mode (`R`): the visible list and its warnings are watched instead of polled. Whether to make it the default is still to decide with `--trace-api` numbers from a real cluster. Item 2 settled on: cursor movement never opens a list, cached or not. Item 10 filters by `involvedObject.kind` and `.name` rather than uid, which also covers events without one; `k8s.EventsAbout` drops earlier objects of the same name. Item 11's CRD list keeps the same 10-minute age on disk as in memory.

Plan for cutting unnecessary API requests, written 2026-10-04 from a review of every call path. Items are in the agreed order: 1–5 first (small, cover most of the waste), then decide on 6. Each needs a demo side, like everything in [plan.md](plan.md).

## Where coral talks to the API today

- **Store.Fetch** (`k8s/store.go`): full `List` of one resource in one namespace, no `resourceVersion`, no paging. Called by `App.fetch` (deduped per key while in flight) and by `Store.Lister` (describe relations and events, not deduped).
- **Startup:** namespaces of the current context, the pods list, and the namespace's events (for the ⚠ markers).
- **Navigator:** expanding a context fetches its namespaces once (`navView.toggle`). Moving the cursor onto a resource activates it, which fetches its list and its events (`nav.go`, "Moving onto a resource previews it").
- **activate:** fetches the list if older than 5s, the events behind the ⚠ markers (`k8s.EventsKey`: the same namespace, or all namespaces for a cluster-scoped list), and the context's namespaces if not cached.
- **Tick:** every `--refresh` (10s) the current list and its events, whether or not the table is visible. Describe: events every 10s, relations every 30s.
- **Describe (`d`):** `k8s.Relations` lists up to ~8 resources (pods, services, network policies, HPAs, ingresses, workloads, …); on a node, every pod in the cluster. `E` lists the namespace's events and filters client-side.
- **Lazy and fine:** the detail panel renders the selected row from the cached list (no requests). OpenAPI (`i`, `ctrl+n`) is fetched per group version on first use and cached in memory. `e` does one fresh GET (needed for conflict detection). Logs stream with tail 500.

## Items

### 1. Request tracing

`--trace-api <file>` logs every request: time, verb, path with query, status, response bytes, duration. Real mode wraps the transport via `rest.Config.WrapTransport`; demo mode logs from a reactor on the fake client (verb, resource, namespace). Every later item gets checked with it. While at it, confirm client-go sends `Accept-Encoding: gzip`.

### 2. Load the table on enter, not on cursor movement

Moving the navigator cursor only highlights. `enter`, `l`/right on a leaf, or a click opens the list. A list that's already cached may still be shown on cursor movement without fetching; decide while implementing. Fallback if enter-only feels sluggish: a ~300ms debounce.

### 3. Pause refresh when nobody's looking

No tick refresh of the list and its events while logs or describe cover the table, or while the terminal has lost focus (Bubble Tea v2 focus reporting, `tea.FocusMsg`/`tea.BlurMsg`). Refresh once when the table or focus returns, if the data is stale.

### 4. Cheaper ⚠ markers

- List only warnings: `fieldSelector=type=Warning` (the markers count nothing else). The fake client ignores field selectors, so keep the client-side `Warning` filter.
- Cluster-scoped lists: no markers, except Nodes with `involvedObject.kind=Node`. Today viewing Nodes, PVs or CRDs lists every event in the cluster every 10s.
- The warnings list then needs its own cache key, separate from the Events table's.

### 5. Lists from the watch cache

`ResourceVersion: "0"` on lists, so the API server answers from its watch cache instead of a quorum read from etcd. Slightly stale, which is fine for a TUI. Optionally page with `Limit: 500` and `Continue`, like kubectl, to cap single response size (pages bypass the watch cache, so measure with #1 first).

### 6. Watch the visible list

After the first list, watch from its resourceVersion so only changes arrive, applied to the Store entry. Re-list on `410 Gone`, and reconnect on errors with backoff. Stop the watch when the view changes or (with #3) is hidden. The refresh timer then only handles reconnects. Events behind the ⚠ markers can be watched the same way. The demo's fake client supports watch. The largest item; decide after 1–5.

### 7. Describe refreshes events only

The timer refreshes only the events of the describe view. Relations are recomputed on `r` or when the view is reopened. With #6, relations could read watched lists.

### 8. Dedupe in the Store

Move the in-flight dedupe from `App.loading` into `Store` (singleflight per key), so `Store.Lister` (describe) and the table share one request when they want the same list. `App.loading` stays for the table's loading indicator.

### 9. Namespaces only for expanded contexts

`activate` no longer fetches namespaces for a context that isn't expanded in the navigator (e.g. a resource pin of another context). Expanding it fetches them, as today.

### 10. Server-side event filter for `E`

`fieldSelector=involvedObject.uid=<uid>` (or kind/name/namespace for events without a real uid, like node events) instead of listing the namespace's events. Demo keeps the client-side `k8s.EventsAbout` filter, which also stays as a guard on real clusters.

### 11. Disk cache for discovery and OpenAPI

`~/.cache/coral/<context>/` (via `os.UserCacheDir`) for the CRD list (see [crds.md](crds.md)) and OpenAPI v3 documents, with a TTL. OpenAPI v3 paths carry a hash, so cached documents can be reused until the index changes, like kubectl's `~/.kube/cache`. `r` on the CRDs list bypasses it.

### 12. CRDs fetched once per context

For the CRD work in [crds.md](crds.md): fetch the CRD list when a context is expanded, keep it for a long TTL (10 minutes, or #11's disk cache), and refresh it only on `r` in the CRDs list. It isn't refreshed on the tick.

## Checks

- After 1–5: startup should need at most namespaces, the pod list and the warning events (in its smaller form). Browsing the navigator with the cursor should send nothing.
- Idle with the table visible: one list per refresh (or a watch, after #6). Idle in logs, describe or an unfocused terminal: nothing but the log stream and describe's events.
