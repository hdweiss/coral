# Status

Handoff notes for picking the work back up. Last updated 2026-10-03 (second session).

## What exists

A first working version of coralctl in Go with Bubble Tea v2 (`charm.land/bubbletea/v2`), Lip Gloss v2 and Bubbles v2. k9s is the template for features and UX only. Its code is built on tview, so none of it is reused.

```
cmd/coralctl/main.go      cobra root (TUI) + `version`; flags --demo --context -n -A --refresh --timeout --theme
internal/k8s/             data layer
  resources.go            built-in resource registry: aliases, GVR, nav category
  columns.go              per-kind table columns (pods, deploy, svc, …) + generic fallback
  store.go                cache of list results keyed by (context, GVR, namespace)
  provider.go             kubeconfig → dynamic client per context
  logs.go                 LogRequest, pod container order (default-container annotation)
  events.go               event time/count, matching events to objects, Warning index for the ⚠ markers, events columns
  related.go              related objects for describe: owners, selected pods, services, policies, uses / used by
  demo.go                 fake dynamic client with two realistic clusters (--demo)
  demo_logs.go            generated demo logs: ECS JSON, zap JSON, nginx/redis/logfmt text, klog
internal/config/          persisted user state: pins.json, fields.json
internal/schema/          field schemas: cluster OpenAPI v3 (incl. CRDs) with fallback to k8s.io/api Go types + SwaggerDoc
internal/edit/            YAML rendering/line lookup, field insertion, $EDITOR session (kubectl-edit style)
internal/theme/           palette: built-in coral colors, Omarchy colors.toml parsing, change detection
internal/logs/             log line parsing: ECS (dotted keys expanded), other JSON, plain text; time/level/message
internal/yamltree/        foldable tree model of an object (key ordering, default folds, favorite/hidden arrangement)
internal/ui/              TUI
  app.go                  root model: layout, focus, mouse routing, divider drag, commands, header/status bar, help
  pins.go                 "Pinned" section at the top of the navigator tree (namespace / resource-list pins); pinned clusters sort first
  nav.go                  left tree: ★ Pinned, then context › Cluster | All namespaces | namespace › category › resource (Events sits directly under the namespace)
  table.go                resource table: click-to-sort headers, filter, keeps selection across refreshes
  detail.go               folding YAML tree of the selected object or log entry; favorite / hide fields
  logs.go                 log view in place of the table: streaming, follow, filter, pinned fields per line
  describe.go             events (E) / describe (d) view in place of the table: related objects by section, events
  palette.go              ":" command palette with ranked suggestions
  info.go                 "i" field help popup
  picker.go               ctrl+n add-field dialog: schema-driven suggestions, dotted paths
  editing.go              "e"/ctrl+n flows: fetch fresh → $EDITOR → update, retry on errors
  styles.go, input.go     colors, frame drawing, shared helpers
```

Run it with `make demo` (`make build`, `make test`, `make check` and `make install` also exist). There is no real cluster in the dev environment, so test with `--demo`. tmux is installed; `tmux new -d -s coral -x 160 -y 40 ./bin/coralctl --demo` plus `tmux capture-pane -p` works for checking the screen.

Tests: `go test ./...` (demo store lists every builtin; events and warning counts; relations on the demo cluster; yamltree ordering and folding; table column layout).

Set `XDG_CONFIG_HOME` to a temp dir when testing so pins don't touch the real config.

Mouse can be scripted in tmux by sending SGR sequences as literal keys, for example a click at 0-based (x, y): `tmux send-keys -t coral -l $'\e[<0;X+1;Y+1M\e[<0;X+1;Y+1m'`. For a drag, add a `\e[<32;…M` motion event before the release. Wheel is `64`/`65`. `tmux resize-window -t coral -x N` tests resizing.

`capture-pane` output contains `\t` where the renderer used hard tabs to move the cursor over blank cells (Bubble Tea's `cursed_renderer` hard-tab optimisation). tmux keeps the HT in its grid. They are not in the rendered content and are harmless.

## Design decisions

- **Keys follow k9s** where coral has the same feature and it doesn't clash with `h`/`l` panel movement: `0` all namespaces, `1`–`9` open pins (numbered in the navigator, in pin order), `y` YAML details, `p` previous logs, `s`/`f` autoscroll/fullscreen in the log view, `ctrl+r` refresh (`r` too). Coral-only actions moved to ctrl keys to keep k9s's letters free: `ctrl+p` pin/favorite, `ctrl+n` add field, `ctrl+x` hide field. Deliberate differences: `L` logs (k9s `l`, which is "right" here), `s`/`S` sort in the table (k9s sorts with shift+column letter; `s` will need to move when shell lands), and `ctrl+d`/`ctrl+u` no longer page so `ctrl+d` is free for delete. `d` is describe and `E` events (k9s has no `E`; its events are a resource view). `a`, `x` (outside the details), `space` in the table and `ctrl+k` are kept free for attach, Secret decode, marking and kill.

- **Cache first:** views render from the Store immediately. They refetch if the data is older than 5s, and the visible list refreshes in the background every `--refresh`. `r` refreshes now.
- **Client-side columns** (not server-side Table), so demo mode and CRDs share one code path. Server-side tables could be added later for CRD printer columns.
- **Mouse hit-testing** uses the layout rects in `App.layout`, not bubblezone. Panels get panel-local `clickMsg`/`wheelMsg`. The header and status bar register `button`s while rendering.
- Double-click is detected in `App.handleMouse` (400ms, same cell).
- Below 100 columns the detail panel replaces the table instead of sitting next to it.
- **Column fitting** (`layoutColumns` in table.go): NAME shrinks to 24, then columns with `Column.Drop > 0` are hidden (highest first: pods IP then NODE, svc CLUSTER-IP, pvc VOLUME/STORAGECLASS, …). Then NAME shrinks to 12, and finally columns are hidden from the right. NAME and the sort column are never hidden. `s` skips hidden columns.
- **Pins** are `config.Pin{Context, Namespace, Resource}`: Namespace "" means the whole cluster, and Resource (a `k8s.Resource` name like `pods`) pins one list (a namespaced Resource without Namespace is the all-namespaces list). They are stored in `os.UserConfigDir()/coralctl/pins.json`, or `pins-demo.json` with `--demo`, and saved on every change. Ways to pin:
  - `ctrl+p` in the navigator pins the resource list under the cursor, otherwise the node's namespace, or its cluster outside a namespace. On a pin, `ctrl+p`/delete/backspace unpins it. `ctrl+p` in the table pins where you are, like the header ☆.
  - Click the ☆/★ after the breadcrumb.
  - `:pin` pins the current namespace, or the cluster when the view is all-namespaces or cluster-scoped.

  Pins live in the navigator as a foldable "★ Pinned" section above the contexts (hidden when empty), one node per namespace or resource pin (`navNode.pin` set): a namespace pin has the categories, a resource pin is a leaf labelled "Pods 9 · ctx › ns". Cluster pins add no node; they move the context to the front of the context list (pin order, then kubeconfig order; `navView.sortRoots`) and show a ★. A cluster pin whose context is missing from the kubeconfig does appear in the section, so it can be removed. Each pin row ends in a clickable ×. Fold state survives pin changes. Enter or a first click on a namespace pin opens it (`openPinMsg`), keeping the resource type and falling back to pods when it meets a cluster-scoped type. `navView.Reveal` looks at the cursor, then the pin the cursor is in, then namespace/resource pins, then the context tree, so opening a pin unfolds it in place instead of jumping to the tree below. Pinned nodes in the main tree get a ★. Each pin shows its number key (`1`–`9`, before the × or after a pinned cluster's ★); `openPinNumber` opens it: a resource pin its list, a namespace pin like Enter, a cluster pin its default namespace. Pins whose context is missing from the kubeconfig show "(missing)" and can only be removed.
- **Favorite and hidden fields** in the YAML view apply per kind (`GroupKind`, e.g. `Pod`, `Deployment.apps`). Rules are keyed by `Node.Pattern()`, the path with list indices replaced by `[]`, so `.spec.containers[].env` covers every container. They are stored in `fields.json` (shared between demo and real mode, because kinds are generic). `yamltree.Node.Arrange` reorders the tree in place:
  - Favorites go to the top of their section and get a ★.
  - Hidden fields move into a synthetic `Hidden` node ("⋯ N hidden") at the end of their section. The node is reused across re-arranges, so its folding survives.

  Hidden fields render in gray, indented one level below the group, with an `unhide` button. Expand-all and collapse-all leave hidden groups alone. Keys are `ctrl+p` (favorite) and `ctrl+x` (hide/unhide); the selected row also has clickable ☆/hide buttons. Only map fields can be favorited or hidden, not list items or text lines. Favorite and hidden are mutually exclusive.
- **Editing (`e`)** works like `kubectl edit`:
  - It fetches the object fresh and writes it as YAML (tree key order, no managedFields) to a temp file with a comment header.
  - It runs `$KUBE_EDITOR`/`$VISUAL`/`$EDITOR` (default `vi`) via `tea.ExecProcess`. The cursor starts on the selected detail field for editors known to take a line (`+N` for vi/vim/nvim/nano/emacs/micro/…, `file:N` for hx/subl/zed, `--goto` for code).
  - Quitting without saving (same mtime and content) cancels, and so does saving content equal to the server object.
  - YAML and server errors reopen the editor with `# error:` lines on top. Saving the same rejected content again abandons the edit.
  - The update uses the fetched resourceVersion, so concurrent changes are conflicts.
- **Adding fields (`ctrl+n`)** opens a dialog below the selected node: the node itself if it's a map or list, otherwise its parent. From the table, it targets the whole object.
  - The schema comes from `schema.Chain{OpenAPI, Builtin}` per context. OpenAPI documents are fetched lazily per group version.
  - The input is a dotted path relative to the target, with an optional `: value`. Map keys take the rest of the path, dots included, so `matchLabels.app.kubernetes.io/name: x` works. Entering a list of objects means "a new item".
  - Enter or click on an object, map or list-of-objects descends (adds `.`). On a leaf it adds the field, and the value is parsed per schema type. Tab completes, and backspace after `.` removes a segment. A "✓ add X" row adds the container itself.
  - The result goes through the edit flow with the field inserted (`edit.Insert`; an existing value is only overwritten when a value was typed) and the cursor on it. Nothing reaches the cluster without saving in the editor.
- **Field help (`i`)** toggles a popup in the details (`info.go`) that follows the cursor. It sits below the selected line, or above it when there's more room there. It shows the path, type, required flag, enum values (OpenAPI only; Go types have no enums) and the description. It uses the same schema chain as `a`, loaded per context and GVK, and reloads when the selection switches kind. List items and map entries are described as "An entry of <field>". `esc` also closes it. URLs in descriptions are replaced by `[n]` and listed whole at the bottom. Each is an OSC 8 hyperlink, and a click opens it via `xdg-open`/`open` (`openURL`). Clicks elsewhere on the popup are swallowed by catch-all `button`s so they don't reach the panel underneath.
- **Theming:** `--theme` (or `$CORALCTL_THEME`) takes `auto` (default: Omarchy's theme when installed, else coral), a built-in name (`theme.Builtin`: `coral`; `catppuccin` — Omarchy's catppuccin (Mocha) colors.toml through the Omarchy mapping, a test keeps them in sync; `kanagawa` — Kanagawa Wave from kanagawa.nvim with crystalBlue as accent, since Omarchy's kanagawa uses the foreground as accent), `omarchy` (fails without Omarchy), or a path to a `colors.toml` / theme directory. The active Omarchy theme is `~/.local/state/omarchy/current/theme/colors.toml`, or `~/.config/omarchy/current/theme/` on older installs. Mapping: accent→Accent, foreground→Fg, dark_foreground→Muted, selection→SelBg and Border, lighter_background→SelBgLo, dark_background→BarBg, background→logo text, magenta→Purple, the rest by name. Missing or non-hex values keep the default. `ui.applyTheme` reassigns the package-level `col*`/`st*` vars, and every view builds output from them on each render, so a new theme shows on the next frame. Don't capture styles at construction time (text inputs get `inputStyles` on creation; the filter input is restyled in `renderStatus`). The 1s tick re-reads the file and applies it when the content changed, so `omarchy-theme-set` recolors a running coralctl. Test with a fake `HOME` holding `.local/state/omarchy/current/theme/colors.toml`.
- **Logs (`L` on a pod, or `:logs`)** replace the table with a log view; the detail panel shows the selected entry. `esc` (or `L`) goes back, and activating anything in the navigator closes it.
  - `Provider.Logs` streams with timestamps=true, tail 500, follow (not for `p`, the previous container). A goroutine scans lines into a channel and `logView.read` hands them to Update in batches of up to 2000 (`logLinesMsg`, tagged with a stream generation so a replaced stream's lines are dropped). At most 20000 lines are kept.
  - `logs.Parse` strips the kubelet timestamp, then: JSON objects are parsed (numbers as int64/float64); ECS is detected by `ecs.version`, or `@timestamp` plus `log.level`, and its dotted keys are expanded into nested objects so both spellings are the same field. Time, level and message come from the usual keys (`@timestamp`/`time`/`ts` incl. epoch, `log.level`/`level`/…, `message`/`msg`). Plain lines get a level from a klog prefix or a level word near the start.
  - A line shows time, level, pinned fields and the message's first line (⏎ if more). Pinned fields are the favorites of the pseudo kind `(logs)` in `fields.json`, so `ctrl+p`/☆ pin in the entry view pins a field to every line (global, in pin order, with `x` hide also working). Labels are the shortest unique key suffix (`status_code`, `service.name`; generic keys like `name`/`id` keep their parent). Lookup by pattern is `yamltree.Lookup`.
  - Follow: on while the cursor is on the last line; moving up, or focusing the entry view, pauses it, `G` jumps to the end and resumes, `s` toggles it (k9s autoscroll). `f` is fullscreen (zoom). `c` cycles containers, `p` toggles the previous container (on a pod row it opens the log that way), `r` restarts the stream, `/` filters lines (substring terms on the raw line). `e`/`a`/`i` are off for log entries.
  - The entry view keeps folding and the cursor's field when moving between lines. Top-level keys `@timestamp`, `log`, `message` (and non-ECS equivalents) come first (`yamltree.BuildFirst`). Tabs are expanded when rendering (`untab`).
- **Events** are core `v1` (its `involvedObject` is simpler to match than `regarding` in `events.k8s.io/v1`). `k8s.About` matches an event to an object by kind, namespace, name and uid; a uid equal to the name also matches, since the kubelet sets that on node events (which live in `default`).
  - The `events` resource has `Category: CatTop`, so the navigator puts it directly under each namespace and All namespaces, after the categories. The table sorts it by LAST SEEN (`Column.DefaultSort`), newest first; TYPE and REASON are red for warnings. MESSAGE is `Column.Flex`: it shrinks first (there's no NAME) and takes back the room left when columns are hidden. TYPE then COUNT are hidden first.
  - **⚠ markers:** with every list the app also fetches the events that cover it (`k8s.EventsKey`: the same namespace, or all namespaces for a cluster-scoped list) and refreshes them with it. `k8s.IndexWarnings` counts Warning events of the last hour (`WarningWindow`) per uid, or per kind/namespace/name for events without a real uid, so a recreated pod with the same name doesn't inherit the old one's warnings. The count is drawn in red at the right end of the NAME cell (`warningMark`), so it sits at a fixed x; a click there opens the events.
- **Events (`E`) and describe (`d`)** replace the table like the log view (`describeView`, `App.desc`). `E` lists the selected object's events (a cluster-scoped object's from all namespaces), filtered client-side from the cached namespace list. `d` shows `k8s.Relations` by section (Owners, Node, ReplicaSets/Jobs/Pods, Services, Network policies, Autoscalers, Uses, Used by, Ingresses, …), then the events.
  - Relations are computed in a goroutine through `Store.Lister`, which serves lists from the cache when fresh and fetches them otherwise. Rows note how they relate (`volume`, `envFrom`, a pod's status); references to missing objects show in red as "not found". Pods are matched by label selector (`metav1.LabelSelectorAsSelector`), so a deployment's pods are found without walking ReplicaSets. "Used by" lists pod templates of workloads and only standalone pods, not each pod of a deployment.
  - The cursor skips section headers. The detail panel shows the selected row's object (an event or a related object), so `e`, `ctrl+n` and `i` work on it (`App.selection`). `enter` jumps to a related object in its list, staying in all namespaces if that's where you were, and `tableView.Select` puts the cursor on it. `d`/`E` on a related row open its view on top (`describeView.prev`); `esc` (or the same key elsewhere) goes back one level. `L`/`p` open the log of the selected or described pod.
  - Refresh: on the `--refresh` tick events are refetched and relations reused for up to 30s; `r` refetches everything.
  - Demo: `demo.event` seeds events per pod state (CrashLoopBackOff: BackOff, Unhealthy; ImagePullBackOff: Failed, ErrImagePull; Pending: FailedScheduling), the NotReady node, a recent deployment rollout and the CronJob/Job.
- **Help** switches to two columns when one is taller than the screen and the screen is wide enough.
- **Demo updates:** a reactor in `demo.go` gives the fake client real-server semantics. Updates bump resourceVersion, and a stale version is a Conflict.
- On window resize the detail panel keeps its share of the width right of the navigator. The navigator keeps its absolute width.

## Known issues

- Verified in tmux this session: row click/double-click, header click sort, wheel, both divider drags (with clamping), nav click, detail fold click (glyph, or a second click on the selected line), breadcrumb → palette, palette filtering + `:ns`, `/` filter (Enter keeps, Esc clears), help overlay, narrow layout, pins (fold out, × unpin, cluster pins sorting first). Not checked in a real terminal emulator, only in tmux.
- Small nit: `/` while the detail panel has focus leaves focus there, so the status bar shows detail keys while filtering the table.
- Module path `github.com/hdweiss/coralctl` is a guess; change it if the repo lives elsewhere.

## Next up

The roadmap (k9s parity, then coral's own ideas) is in [plan.md](plan.md). Ideas and gaps go there, not here.
