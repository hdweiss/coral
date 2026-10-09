# Plan

Roadmap for coralctl, written 2026-10-03. The goal is k9s parity for day-to-day work, then the coral-specific ideas from [brainstorm.md](brainstorm.md). What exists today and why it is built that way is in [status.md](status.md).

Every feature has to work in `--demo`, since there is no real cluster in the dev environment. Each item below therefore includes its demo side (fake data, a reactor in `demo.go`, or a stub in `demoProvider`).

## Phase 0: foundations for actions

Coral can browse and edit but cannot act on objects. Before adding actions one by one, build the plumbing they share.

- **Typed client and REST config in `Provider`.** Exec, port-forward and metrics need more than the dynamic client. Logs went in as a narrow `Provider.Logs` (the kube provider caches a typed clientset per context); the rest can follow that pattern or add `Clientset(ctx)` and `RESTConfig(ctx)`. The demo uses `k8s.io/client-go/kubernetes/fake` seeded with the same objects, and returns an error for `RESTConfig`, so features that need it can say "not in demo mode".
- **Action registry.** Per kind, a list of `{key, label, run, needsWrite}`, e.g. pods: logs, shell, delete, kill, port-forward. The table, the detail panel, the status-bar hints and the help overlay all read from it, so a new action shows up everywhere at once.
- **Action menu.** `.` or right-click on a row opens a palette scoped to the selected object, listing its actions. This keeps the UI mouse-first and makes actions discoverable without memorizing keys. It reuses `palette.go`.
- **Confirm dialog.** A small modal like `picker.go` (title, text, buttons, `y`/`n`/`enter`/`esc`, clickable). Needed by delete, kill, drain, scale and restart.
- **Read-only mode.** `--readonly` flag and a per-context setting. Actions with `needsWrite`, `e` and `ctrl+n` are hidden or refuse with a flash. This should land before delete does.

## Phase 1: core operations

What people reach for k9s for most of the time.

- **Logs.** Done (see status.md): `L` on a pod, ECS/JSON parsing, entry tree, fields pinned to lines, follow, filter, containers, previous. Still open:
  - Tail length and since-time options, wrap and timestamp toggles, search with highlighting (jump between matches rather than filter), save to a file.
  - Logs of a whole deployment / label selector, prefixed by pod; logs from a deployment row (pick the pod).
  - logfmt parsing (`key=value` lines) into fields, so they can be pinned like JSON.
  - Per-app pinned fields (today global), and a way to pin from the line view.
- **Shell / attach (`s` / `a` on pods).** Run `kubectl exec -it` (and `kubectl attach`) through `tea.ExecProcess`, the same way `$EDITOR` runs. This is what k9s does and avoids SPDY handling in-process. Pick the container first, try `bash` then `sh`. Pass `--context` and `--kubeconfig`. Demo: run `$SHELL` with a banner saying it is fake.
  - `a` is free (add field moved to `ctrl+n`). `s` is still table sort, so shell needs `s` freed (sort to shift+letter like k9s) or another key.
- **Delete and kill (`ctrl+d` / `ctrl+k`).** Confirm dialog with the object name; options for cascade (background/foreground/orphan) and grace period. Kill is delete with grace period 0. The table keeps its cursor on the next row. Demo: the fake tracker already supports delete; also re-create pods owned by a ReplicaSet so it feels real.
- **Events.** Done (see status.md): an `events` resource directly under each namespace in the navigator (LAST SEEN, TYPE, REASON, OBJECT, COUNT, MESSAGE, newest first, warnings in red); `E` lists the selected object's events in place of the table; a red `⚠N` after the name marks rows with Warning events in the last hour (click it for the events). Still open:
  - Watch events instead of polling them with the list refresh. Done for the ⚠ markers in live mode (`R`); the describe view still polls.
  - Use a server-side field selector (`involvedObject.uid`) for `E` on large namespaces; today the namespace's events are listed and filtered client-side (the demo's fake client ignores field selectors).
  - Events of owned objects in `E`/`d` (a deployment's ReplicaSets and pods), like a timeline of a rollout.
  - A ⚠ count per namespace in the navigator.
- **Describe (`d`).** Done as a related summary plus events, in place of the table: owners (up the controller chain), owned/selected objects (ReplicaSets, Jobs, pods by selector, a node's pods), services and network policies selecting it, autoscalers, what its pod spec uses (config maps, secrets, claims, service account) and what uses it, ingress ↔ service/secret, PVC ↔ PV, HPA target, role refs. `enter` on a row jumps to it in its list; `d`/`E` on a row open its view on top (esc goes back). The relation code (`k8s.Relations`) is meant to grow into the "Used by" panel and the NetworkPolicy view in Phase 4. Still open:
  - Full `kubectl describe` text (conditions, container states, probes) via `k8s.io/kubectl/pkg/describe` or `kubectl describe`, perhaps as another section.
  - Relations of CRDs (ownerReferences work already; selectors don't).

## Network traffic

Done (see status.md): lists open on enter rather than cursor movement, refresh pauses when hidden, cheaper ⚠ markers, watch-cache lists, in-flight dedupe, server-side event filters, a disk cache for CRDs and OpenAPI, `--trace-api`, and watches as live mode (`R`). The itemized plan is in [network.md](network.md). Still open:
  - Live mode by default (or a `--live` flag), once `--trace-api` on a real cluster shows how watches behave there.
  - Watches for the describe view's events.

## Phase 2: reach

- **API discovery and CRDs.** Done for CRDs as designed in [crds.md](crds.md) (see status.md): a Custom Resources category grouped by API group, well-known groups in builtin categories, columns from `additionalPrinterColumns`, `:` for every custom resource, `o` in the CRDs list. Still open (the "Later" section of crds.md):
  - Full API discovery (`ServerPreferredResources`) for aggregated APIs and builtins without columns, and as the fallback when listing CRDs is forbidden.
  - Schemas for `i`/`ctrl+n` from a CRD's own `openAPIV3Schema`, `d` on a CRD. Relations of more CRDs (Linkerd authorization policies, Istio, KEDA, Prometheus operator selectors); cert-manager, Cilium, Linkerd Server/ServiceProfile and the Gateway API are done.
- **Drill-down and relations.**
  - Owners to pods: from a deployment, statefulset, daemonset, replicaset or job, show the pods it owns (selector or ownerReferences). From a node, its pods (`spec.nodeName`). From a service, the pods it selects.
  - "Show owner" jumps up the ownerReferences chain.
  - The drill-down becomes a breadcrumb level with `esc`/backspace going back. Key to decide: `enter` focuses the detail panel today; candidates are `o` ("open related"), double-click on the table, or the action menu.
  - Partly there: `d` lists owners, pods and other related objects (see Describe), and `enter` jumps to one in its list. What's missing is a filtered list ("the pods of this deployment") as a breadcrumb level.
  - Later: an xray-style tree (deployment → replicaset → pods → containers) in the navigator.
- **Workload actions.**
  - Scale (deployments, statefulsets, replicasets): a small number input. Restart: patch the pod template annotation like `kubectl rollout restart`. Set image: per container, prefilled.
  - CronJobs: trigger now (create a job from the template), suspend/resume.
  - All through the confirm dialog, and all as patches with the fetched resourceVersion, so conflicts show as in editing.
- **Port-forward (`shift+f`).** On pods, services and deployments, pick a container port and a local port (default: the same). Runs in-process with `k8s.io/client-go/tools/portforward`. A `:pf` view lists active forwards with stop; they end when coral quits. Demo: refuse with a flash (needs `RESTConfig`).

## Phase 3: parity polish

- **Node actions:** cordon, uncordon, drain (with the options `kubectl drain` has: ignore daemonsets, delete emptydir data, grace period, timeout).
- **Filter parity.** `-l app=x` as a server-side label selector, `-f field=value` as a field selector, `!term` to invert, fuzzy matching as an option. The filter also searches inside the detail tree and log view when they have focus (this also fixes the `/`-focus nit in status.md).
- **Multi-select.** `space` marks rows (a mark column on the left), `ctrl+space` clears. Delete, kill, restart and scale act on all marked rows with one confirm.
- **Metrics.** CPU/MEM columns (and %-of-request/limit) for pods and nodes from `metrics.k8s.io` when the server has it, refreshed with the list. A cluster summary (nodes, CPU/MEM use, version) in the header or a `:pulse`-like view. Demo: fake metrics.
- **Secrets.** A toggle in the detail panel that shows `data` decoded from base64; editing writes it back encoded.
- **Small items.** Copy name / YAML / selected field to the clipboard (OSC 52, works over SSH). `:` history with up/down. `--readonly` shown in the header. Wide view toggle for tables (show dropped columns). User aliases and hotkeys in a config file.
- **Later, maybe.** Plugins (user commands with templated arguments, like k9s plugins), RBAC views (who-can, what a service account can do), health checks (popeye-like), HTTP benchmarking.

## Phase 4: coral's own ideas

From the brainstorm and earlier sessions; these are what set coral apart from k9s and can be interleaved with the phases above.

- **"Used by" right panel.** For ConfigMaps, Secrets, Services, PVCs and ServiceAccounts, show what references them (pods, deployments, ingresses), clickable. The data exists (`k8s.Relations`, "Used by" in `d`); what's left is showing it next to the YAML without opening describe.
- **NetworkPolicy view.** Source and destination in the right panel, matched pods highlighted in green. `d` on a policy already lists the pods its podSelector matches, and `d` on a pod the policies selecting it; ingress/egress peers (from/to selectors, namespaceSelector, ipBlock) are the next step in `k8s.Relations`.
- **Schema-based inline validation.** Color invalid or unknown fields in the detail tree using the existing `schema.Chain`, and show the reason in the `i` popup.
- **Add-field improvements.** Insert directly without the editor when a value was typed (needs a confirm/diff step). Required-field scaffolding for new list items (a container needs name and image). Enum value suggestions after `:`.
- **Field favorites.** A "favorites summary" pinned at the very top of the view; a per-cluster scope; a UI listing all favorite/hidden rules of a kind.
- **Pins.** Reordering (`K`/`J`, or drag). Pinning the current resource list from the header ☆ and `:pin` (today only the namespace or cluster).
- **Light themes.** Auto-detect the terminal background (`tea.BackgroundColorMsg`) for the built-in palette.
- **Links.** Open a pod's log URL from an annotation, open ingresses and services in the browser (reuse `openURL`).
- **Subcommands.** Non-TUI subcommands (the brainstorm mentions them), e.g. `coral get` with the same columns.

## Open questions

- Key for drill-down, given `enter` focuses the detail panel today.
- Key for shell, given `s` is sort in the table.
- Whether to depend on `kubectl` at all (exec, attach, describe) or do everything in-process. Shelling out is simpler and matches k9s; in-process works without kubectl installed.
