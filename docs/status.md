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
  demo.go                 fake dynamic client with two realistic clusters (--demo)
internal/config/          persisted user state: pins.json, fields.json
internal/schema/          field schemas: cluster OpenAPI v3 (incl. CRDs) with fallback to k8s.io/api Go types + SwaggerDoc
internal/edit/            YAML rendering/line lookup, field insertion, $EDITOR session (kubectl-edit style)
internal/theme/           palette: built-in coral colors, Omarchy colors.toml parsing, change detection
internal/yamltree/        foldable tree model of an object (key ordering, default folds, favorite/hidden arrangement)
internal/ui/              TUI
  app.go                  root model: layout, focus, mouse routing, divider drag, commands, header/status bar, help
  pins.go                 "Pinned" box above the navigator: flat cluster / cluster › namespace favorites
  nav.go                  left tree: context › Cluster | All namespaces | namespace › category › resource
  table.go                resource table: click-to-sort headers, filter, keeps selection across refreshes
  detail.go               folding YAML tree of the selected object; favorite / hide fields
  palette.go              ":" command palette with ranked suggestions
  info.go                 "i" field help popup
  picker.go               "a" add-field dialog: schema-driven suggestions, dotted paths
  editing.go              "e"/"a" flows: fetch fresh → $EDITOR → update, retry on errors
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
- **Favorite and hidden fields** in the YAML view apply per kind (`GroupKind`, e.g. `Pod`, `Deployment.apps`). Rules are keyed by `Node.Pattern()`, the path with list indices replaced by `[]`, so `.spec.containers[].env` covers every container. They are stored in `fields.json` (shared between demo and real mode, because kinds are generic). `yamltree.Node.Arrange` reorders the tree in place:
  - Favorites go to the top of their section and get a ★.
  - Hidden fields move into a synthetic `Hidden` node ("⋯ N hidden") at the end of their section. The node is reused across re-arranges, so its folding survives.

  Hidden fields render in gray, indented one level below the group, with an `unhide` button. Expand-all and collapse-all leave hidden groups alone. Keys are `f` (favorite) and `x` (hide/unhide); the selected row also has clickable ☆/hide buttons. Only map fields can be favorited or hidden, not list items or text lines. Favorite and hidden are mutually exclusive.
- **Editing (`e`)** works like `kubectl edit`:
  - It fetches the object fresh and writes it as YAML (tree key order, no managedFields) to a temp file with a comment header.
  - It runs `$KUBE_EDITOR`/`$VISUAL`/`$EDITOR` (default `vi`) via `tea.ExecProcess`. The cursor starts on the selected detail field for editors known to take a line (`+N` for vi/vim/nvim/nano/emacs/micro/…, `file:N` for hx/subl/zed, `--goto` for code).
  - Quitting without saving (same mtime and content) cancels, and so does saving content equal to the server object.
  - YAML and server errors reopen the editor with `# error:` lines on top. Saving the same rejected content again abandons the edit.
  - The update uses the fetched resourceVersion, so concurrent changes are conflicts.
- **Adding fields (`a`)** opens a dialog below the selected node: the node itself if it's a map or list, otherwise its parent. From the table, it targets the whole object.
  - The schema comes from `schema.Chain{OpenAPI, Builtin}` per context. OpenAPI documents are fetched lazily per group version.
  - The input is a dotted path relative to the target, with an optional `: value`. Map keys take the rest of the path, dots included, so `matchLabels.app.kubernetes.io/name: x` works. Entering a list of objects means "a new item".
  - Enter or click on an object, map or list-of-objects descends (adds `.`). On a leaf it adds the field, and the value is parsed per schema type. Tab completes, and backspace after `.` removes a segment. A "✓ add X" row adds the container itself.
  - The result goes through the edit flow with the field inserted (`edit.Insert`; an existing value is only overwritten when a value was typed) and the cursor on it. Nothing reaches the cluster without saving in the editor.
- **Field help (`i`)** toggles a popup in the details (`info.go`) that follows the cursor. It sits below the selected line, or above it when there's more room there. It shows the path, type, required flag, enum values (OpenAPI only; Go types have no enums) and the description. It uses the same schema chain as `a`, loaded per context and GVK, and reloads when the selection switches kind. List items and map entries are described as "An entry of <field>". `esc` also closes it. URLs in descriptions are replaced by `[n]` and listed whole at the bottom. Each is an OSC 8 hyperlink, and a click opens it via `xdg-open`/`open` (`openURL`). Clicks elsewhere on the popup are swallowed by catch-all `button`s so they don't reach the panel underneath.
- **Theming:** `--theme` (or `$CORALCTL_THEME`) takes `auto` (default: Omarchy's theme when installed, else coral), a built-in name (`theme.Builtin`: `coral`, `kanagawa` — Kanagawa Wave from kanagawa.nvim with crystalBlue as accent, since Omarchy's kanagawa uses the foreground as accent), `omarchy` (fails without Omarchy), or a path to a `colors.toml` / theme directory. The active Omarchy theme is `~/.local/state/omarchy/current/theme/colors.toml`, or `~/.config/omarchy/current/theme/` on older installs. Mapping: accent→Accent, foreground→Fg, dark_foreground→Muted, selection→SelBg and Border, lighter_background→SelBgLo, dark_background→BarBg, background→logo text, magenta→Purple, the rest by name. Missing or non-hex values keep the default. `ui.applyTheme` reassigns the package-level `col*`/`st*` vars, and every view builds output from them on each render, so a new theme shows on the next frame. Don't capture styles at construction time (text inputs get `inputStyles` on creation; the filter input is restyled in `renderStatus`). The 1s tick re-reads the file and applies it when the content changed, so `omarchy-theme-set` recolors a running coralctl. Test with a fake `HOME` holding `.local/state/omarchy/current/theme/colors.toml`.
- **Demo updates:** a reactor in `demo.go` gives the fake client real-server semantics. Updates bump resourceVersion, and a stale version is a Conflict.
- On window resize the detail panel keeps its share of the width right of the navigator. The navigator keeps its absolute width.

## Known issues / next up

- Verified in tmux this session: row click/double-click, header click sort, wheel, both divider drags (with clamping), nav click, detail fold click (glyph, or a second click on the selected line), breadcrumb → palette, palette filtering + `:ns`, `/` filter (Enter keeps, Esc clears), help overlay, narrow layout. Not checked in a real terminal emulator, only in tmux.
- Field favorites/hidden are per kind only; there's no per-cluster scope and no UI listing all rules of a kind. Favorites only reorder within their section; a "favorites summary" pinned at the very top of the view could come next.
- Pins have no reordering yet; new ones go to the end. Ideas: `K`/`J` to move a pin, or drag.
- Small nit: `/` while the detail panel has focus leaves focus there, so the status bar shows detail keys while filtering the table.
- Add-field ideas: insert directly without the editor when a value was typed (needs a confirm/diff step), required-field scaffolding for new list items (e.g. a container needs name+image), enum value suggestions after ":".
- Missing compared with k9s: logs, describe, delete, port-forward, attach/exec, events, CRD discovery.
- From the brainstorm: "used by" right panel (ConfigMap/Secret/Service → pods), netpol matched pods, schema-based editing, Secret base64 decode toggle, `R` refresh dialog (meaning still to define). Light themes work through Omarchy or `--theme <file>`; auto-detecting the terminal background (`tea.BackgroundColorMsg`) for the built-in palette is still open.
- Module path `github.com/hdweiss/coralctl` is a guess; change it if the repo lives elsewhere.
