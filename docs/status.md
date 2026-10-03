# Status

Handoff notes for picking the work back up. Last updated 2026-10-03.

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
internal/yamltree/        foldable tree model of an object (key ordering, default folds)
internal/ui/              TUI
  app.go                  root model: layout, focus, mouse routing, divider drag, commands, header/status bar, help
  nav.go                  left tree: context › Cluster | All namespaces | namespace › category › resource
  table.go                resource table: click-to-sort headers, filter, keeps selection across refreshes
  detail.go               folding YAML tree of the selected object
  palette.go              ":" command palette with ranked suggestions
  styles.go, input.go     colors, frame drawing, shared helpers
```

Run it with `go build -o bin/coralctl ./cmd/coralctl && ./bin/coralctl --demo`. There is no real cluster in the dev environment, so test with `--demo`. tmux is installed; `tmux new -d -s coral -x 160 -y 40 ./bin/coralctl --demo` plus `tmux capture-pane -p` works for checking the screen.

Tests: `go test ./...` (demo store lists every builtin; yamltree ordering and folding).

## Design decisions

- **Cache first:** views render from the Store immediately. They refetch if the data is older than 5s, and the visible list refreshes in the background every `--refresh`. `r` refreshes now.
- **Client-side columns** (not server-side Table), so demo mode and CRDs share one code path. Server-side tables could be added later for CRD printer columns.
- **Mouse hit-testing** uses the layout rects in `App.layout`, not bubblezone. Panels get panel-local `clickMsg`/`wheelMsg`. The header and status bar register `button`s while rendering.
- Double-click is detected in `App.handleMouse` (400ms, same cell).
- Below 100 columns the detail panel replaces the table instead of sitting next to it.

## Known issues / next up

- Table column widths: only NAME shrinks when space runs out, so at 160 columns NAME is truncated while RESTARTS and IP are wide. Better: shrink proportionally, or hide low-priority columns.
- Captured screens showed stray tab characters at the end of some detail lines. They're probably from the renderer's cursor movement and not real content, but this hasn't been confirmed.
- Not yet manually verified: mouse interactions (click, drag dividers, wheel), the palette, filter and help overlay. Only the first render was checked in tmux.
- Missing compared with k9s: logs, describe, delete, edit, port-forward, attach/exec, events, CRD discovery.
- From the brainstorm: "used by" right panel (ConfigMap/Secret/Service → pods), netpol matched pods, schema-based editing, Secret base64 decode toggle, `R` refresh dialog (meaning still to define), favorite fields, light theme (detect via `tea.BackgroundColorMsg`).
- Module path `github.com/hdweiss/coralctl` is a guess; change it if the repo lives elsewhere.
