# Status

Handoff notes for picking the work back up. Last updated 2026-10-03 (second session).

## What exists

A first working version of coralctl in Go with Bubble Tea v2 (`charm.land/bubbletea/v2`), Lip Gloss v2 and Bubbles v2. k9s is the template for features and UX only. Its code is built on tview, so none of it is reused.

```
cmd/coralctl/main.go      cobra root (TUI) + `version`; flags --demo --context -n -A --refresh --timeout
internal/k8s/             data layer
  resources.go            built-in resource registry: aliases, GVR, nav category
  columns.go              per-kind table columns (pods, deploy, svc, …) + generic fallback
  store.go                cache of list results keyed by (context, GVR, namespace)
  provider.go             kubeconfig → dynamic client per context
  demo.go                 fake dynamic client with two realistic clusters (--demo)
internal/config/          persisted user state: pins.json
internal/yamltree/        foldable tree model of an object (key ordering, default folds)
internal/ui/              TUI
  app.go                  root model: layout, focus, mouse routing, divider drag, commands, header/status bar, help
  pins.go                 "Pinned" box above the navigator: flat cluster / cluster › namespace favorites
  nav.go                  left tree: context › Cluster | All namespaces | namespace › category › resource
  table.go                resource table: click-to-sort headers, filter, keeps selection across refreshes
  detail.go               folding YAML tree of the selected object
  palette.go              ":" command palette with ranked suggestions
  styles.go, input.go     colors, frame drawing, shared helpers
```

Run it with `make demo` (`make build`, `make test`, `make check` and `make install` also exist). There is no real cluster in the dev environment, so test with `--demo`. tmux is installed; `tmux new -d -s coral -x 160 -y 40 ./bin/coralctl --demo` plus `tmux capture-pane -p` works for checking the screen.

Tests: `go test ./...` (demo store lists every builtin; yamltree ordering and folding; table column layout).

Set `XDG_CONFIG_HOME` to a temp dir when testing so pins don't touch the real config.

Mouse can be scripted in tmux by sending SGR sequences as literal keys, for example a click at 0-based (x, y): `tmux send-keys -t coral -l $'\e[<0;X+1;Y+1M\e[<0;X+1;Y+1m'`. For a drag, add a `\e[<32;…M` motion event before the release. Wheel is `64`/`65`. `tmux resize-window -t coral -x N` tests resizing.

`capture-pane` output contains `\t` where the renderer used hard tabs to move the cursor over blank cells (Bubble Tea's `cursed_renderer` hard-tab optimisation). tmux keeps the HT in its grid. They are not in the rendered content and are harmless.

## Design decisions

- **Cache first:** views render from the Store immediately. They refetch if the data is older than 5s, and the visible list refreshes in the background every `--refresh`. `r` refreshes now.
- **Client-side columns** (not server-side Table), so demo mode and CRDs share one code path. Server-side tables could be added later for CRD printer columns.
- **Mouse hit-testing** uses the layout rects in `App.layout`, not bubblezone. Panels get panel-local `clickMsg`/`wheelMsg`. The header and status bar register `button`s while rendering.
- Double-click is detected in `App.handleMouse` (400ms, same cell).
- Below 100 columns the detail panel replaces the table instead of sitting next to it.
- **Column fitting** (`layoutColumns` in table.go): NAME shrinks to 24, then columns with `Column.Drop > 0` are hidden (highest first: pods IP then NODE, svc CLUSTER-IP, pvc VOLUME/STORAGECLASS, …). Then NAME shrinks to 12, and finally columns are hidden from the right. NAME and the sort column are never hidden. `s` skips hidden columns.
- **Pins** are `config.Pin{Context, Namespace}`, where Namespace "" means the whole cluster. They are stored in `os.UserConfigDir()/coralctl/pins.json`, or `pins-demo.json` with `--demo`, and saved on every change. Ways to pin:
  - `p` in the navigator pins the node's namespace, or its cluster outside a namespace.
  - Click the ☆/★ after the breadcrumb.
  - `:pin` pins the current namespace, or the cluster when the view is all-namespaces or cluster-scoped.

  The Pinned box is hidden when empty and gets at most a third of the left column. Opening a pin keeps the resource type, falling back to pods when a namespace pin meets a cluster-scoped type. A cluster pin opens the context's default namespace. Focus order is pins → nav → table → detail, and `0` focuses pins. Pins whose context is missing from the kubeconfig show "(missing)" and can only be removed.
- On window resize the detail panel keeps its share of the width right of the navigator. The navigator keeps its absolute width.

## Known issues / next up

- Verified in tmux this session: row click/double-click, header click sort, wheel, both divider drags (with clamping), nav click, detail fold click (glyph, or a second click on the selected line), breadcrumb → palette, palette filtering + `:ns`, `/` filter (Enter keeps, Esc clears), help overlay, narrow layout. Not checked in a real terminal emulator, only in tmux.
- Pins have no reordering yet; new ones go to the end. Ideas: `K`/`J` to move a pin, or drag.
- Small nit: `/` while the detail panel has focus leaves focus there, so the status bar shows detail keys while filtering the table.
- Missing compared with k9s: logs, describe, delete, edit, port-forward, attach/exec, events, CRD discovery.
- From the brainstorm: "used by" right panel (ConfigMap/Secret/Service → pods), netpol matched pods, schema-based editing, Secret base64 decode toggle, `R` refresh dialog (meaning still to define), favorite fields, light theme (detect via `tea.BackgroundColorMsg`).
- Module path `github.com/hdweiss/coralctl` is a guess; change it if the repo lives elsewhere.
