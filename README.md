# coralctl

A mouse-first Kubernetes TUI with tree views, inspired by [k9s](https://k9scli.io). Built in Go with Bubble Tea, Lip Gloss and Bubbles.

## Features

- **Navigator tree:** contexts › namespaces › categories › resources, with pinned clusters, namespaces and resource lists at the top (`1`–`9` jump to pins).
- **Resource table:** click-to-sort headers, filtering with `/`, and columns that fit the terminal width.
- **Folding YAML view:** per-kind favorite and hidden fields, field help from the cluster's OpenAPI schema (`i`).
- **Editing:** `e` opens the object in `$EDITOR` like `kubectl edit`. `ctrl+n` adds a field with schema-driven suggestions.
- **Logs:** `L` streams pod logs and parses JSON (including ECS), klog and plain text, with fields you can pin to every line.
- **Events and describe:** an Events list per namespace, `E` for the selected object's events, a red `⚠N` on rows with recent warnings, and `d` for related objects (owners, pods, services, network policies, what it uses and what uses it) plus events.
- **Theming:** built-in themes (`coral`, `catppuccin`, `kanagawa`), or follows the active [Omarchy](https://omarchy.org) theme live.
- **Demo mode:** `--demo` runs against built-in fake clusters, no cluster needed.

## Install

With [mise](https://mise.jdx.dev), from the prebuilt GitHub releases:

```sh
mise use -g github:hdweiss/coral
```

Or download an archive for your platform from the [releases page](https://github.com/hdweiss/coral/releases).

From source (requires Go 1.26+):

```sh
make install        # or: go install ./cmd/coralctl
```

## Usage

```sh
coralctl                       # current kubeconfig context
coralctl --context prod -n web # start in a context and namespace
coralctl -A                    # all namespaces
coralctl --demo                # fake clusters
```

Other flags: `--kubeconfig`, `--refresh`, `--timeout` and `--theme` (or `$CORALCTL_THEME`). Run `coralctl --help` for details, and press `?` in the app for key bindings.

## Development

```sh
make build   # bin/coralctl
make demo    # build and run with demo clusters
make test
make check   # gofmt, go vet and tests
```

Design notes and status are in [docs/status.md](docs/status.md), and the roadmap is in [docs/plan.md](docs/plan.md).

## License

MIT — see [LICENSE](LICENSE).
