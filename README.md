# coralctl

A mouse-first Kubernetes TUI with tree views, inspired by [k9s](https://k9scli.io). Built in Go with Bubble Tea, Lip Gloss and Bubbles.

## Features

- **Navigator tree:** contexts › namespaces › categories › resources, with pinned clusters, namespaces and resource lists at the top (`1`–`9` jump to pins).
- **Custom resources:** every CRD's kinds in the navigator, grouped by API group (Gateway API and other well-known groups sit with the builtins), with columns from the CRD's printer columns.
- **Resource table:** click-to-sort headers (or k9s's shift+letter), filtering with `/`, columns that fit the terminal width, and a live mode (`R`) that watches the list instead of polling it.
- **Actions:** `.` or a right click lists what you can do with the selected object: shell (`s`) and attach (`a`) through `kubectl`, delete (`ctrl+d`) and kill (`ctrl+k`) after a confirmation, edit, logs, describe. `--readonly`, or `readonly` context patterns in `config.yaml`, turns off everything that changes the cluster.
- **Folding YAML view:** per-kind favorite and hidden fields, field help from the cluster's OpenAPI schema (`i`).
- **Editing:** `e` opens the object in `$EDITOR` like `kubectl edit`. `ctrl+n` adds a field with schema-driven suggestions.
- **Logs:** `L` streams the logs of a pod (all its containers) or of every pod of a workload, merged by time. It parses JSON (including ECS), logfmt, klog and plain text, with fields you can pin to an app's lines, and has search, wrap, time ranges and saving to a file.
- **Events and describe:** an Events list per namespace, `E` for the selected object's events, a red `⚠N` on rows with recent warnings, and `d` for what `kubectl describe` shows (container states, probes, conditions) plus related objects (owners, pods, services, network policies, what it uses and what uses it, and cert-manager, Cilium, Linkerd and Gateway API resources) and events. A deployment's events include those of its replica sets and pods.
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
make install        # or: go install ./cmd/coral
```

## Usage

```sh
coral                       # current kubeconfig context
coral --context prod -n web # start in a context and namespace
coral -A                    # all namespaces
coral --demo                # fake clusters
```

Other flags: `--kubeconfig`, `--refresh`, `--timeout`, `--readonly`, `--theme` (or `$CORALCTL_THEME`) and `--trace-api <file>` (log every API request). Run `coral --help` for details, and press `?` in the app for key bindings.

Settings live in `config.yaml` in coral's config directory (`~/.config/coral/` on Linux):

```yaml
# Contexts where coral refuses to change anything (shell globs).
readonly:
  - "*prod*"
```

## Development

```sh
make build   # bin/coral
make demo    # build and run with demo clusters
make test
make check   # gofmt, go vet and tests
```

Design notes and status are in [docs/status.md](docs/status.md), and the roadmap is in [docs/plan.md](docs/plan.md).

## License

MIT — see [LICENSE](LICENSE).
