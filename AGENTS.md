# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Coral (`coral`, formerly coralctl) is a mouse-first Kubernetes TUI inspired by k9s, written in Go with Bubble Tea v2, Lip Gloss v2 and Bubbles v2 (`charm.land/*/v2` import paths, not `github.com/charmbracelet/*`). k9s is a reference for features and UX only; none of its code is reused.

## Commands

```sh
make build            # bin/coral (version injected via -X main.version)
make demo             # build and run against fake clusters (--demo --theme kanagawa)
make test             # go test ./...
make check            # gofmt check, go vet, tests — what CI runs; run before committing
make fmt              # gofmt -w cmd internal
go test ./internal/ui -run TestRefreshPausesWhenHidden   # single test
make release-snapshot # goreleaser archives into dist/ without publishing
```

## Developing without a cluster

There is no real cluster in the dev environment. Every feature must work in `--demo` (fake data, a reactor in `internal/k8s/demo.go`, or a stub in the demo provider). The demo's fake dynamic client ignores field selectors, so client-side filters must stay even where a server-side selector is used.

- Set `XDG_CONFIG_HOME` to a temp dir when running/testing so pins and field rules don't touch the real config.
- Check the screen via tmux: `tmux new -d -s coral -x 160 -y 40 ./bin/coral --demo`, then `tmux capture-pane -p`. Mouse events can be sent as SGR sequences, e.g. click at 0-based (x, y): `tmux send-keys -t coral -l $'\e[<0;X+1;Y+1M\e[<0;X+1;Y+1m'` (wheel is `64`/`65`). Hard tabs in `capture-pane` output are a renderer optimisation and harmless.
- `--trace-api <file>` logs every API request (also in demo mode). Use it to check what a change costs in requests; minimizing API traffic is an explicit goal (docs/network.md).

## Architecture

- `cmd/coral/main.go` — cobra root: builds a `k8s.Provider` (real kubeconfig or demo), a `k8s.Store`, resolves the theme, and runs `ui.New` as a Bubble Tea program. klog is silenced so client-go doesn't draw over the TUI.
- `internal/k8s` — data layer. `Provider` abstracts contexts, dynamic clients, OpenAPI and log streams; `kubeProvider` and the demo provider implement it. `Store` caches list results keyed by (context, GVR, namespace, field selector), dedupes in-flight requests, and holds a per-context `Registry` of built-in + custom resources. UI lookups that may involve CRDs must go through `Store.Registry(ctx)`; `k8s.Lookup` is builtins-only. Table columns are computed client-side (`columns.go` for builtins, `printer.go` from CRD `additionalPrinterColumns`) so demo mode and CRDs share one path. `related.go`/`related_crds.go` compute describe relations.
- `internal/ui` — the TUI. `app.go` is the root model: layout, focus, mouse routing (hit-testing via layout rects in `App.layout`, not bubblezone; panels receive panel-local `clickMsg`/`wheelMsg`), commands, header/status bar. Panels: navigator tree (`nav.go`, `pins.go`), resource table (`table.go`), folding YAML detail (`detail.go`), and views that replace the table (`logs.go`, `describe.go`). Views render from the Store cache immediately and refetch in the background.
- `internal/yamltree` — foldable tree model of an object; favorite/hidden field arrangement keyed by `Node.Pattern()` (list indices replaced by `[]`).
- `internal/schema` — field schemas: cluster OpenAPI v3 with fallback to `k8s.io/api` Go types (`schema.Chain{OpenAPI, Builtin}`).
- `internal/edit` — kubectl-edit-style `$EDITOR` sessions and field insertion.
- `internal/config` — persisted user state (`pins.json` / `pins-demo.json`, `fields.json`) under `os.UserConfigDir()/coral`.
- `internal/theme`, `internal/logs` — palettes (built-in and Omarchy), log line parsing (ECS/JSON/klog/plain).

### Conventions that aren't obvious from one file

- Theming: `ui.applyTheme` reassigns package-level `col*`/`st*` vars and every view builds styles from them on each render. Don't capture styles at construction time.
- Key bindings follow k9s where features overlap and don't clash with `h`/`l` panel movement; coral-only actions use ctrl keys. Several letters (`a`, `x`, `space`, `ctrl+k`, `ctrl+d`) are deliberately kept free for planned actions. Check docs/status.md "Design decisions" before assigning a key.
- Edits/updates use the fetched resourceVersion so concurrent changes surface as conflicts; the demo reactor emulates this. `Store.Update` patches the object into cached lists instead of relisting.

## Docs

- `docs/status.md` — handoff notes: detailed file map, design decisions and known issues. Read it before larger changes and keep it updated when behavior changes.
- `docs/plan.md` — roadmap (k9s parity, then coral's own ideas). New ideas and gaps go here, not in status.md.
- `docs/network.md`, `docs/crds.md` — design notes for API traffic and custom resources.
