# Prior art

Survey from 2026-10-03 of tools near coralctl's main idea: a Kubernetes TUI that understands formats, with folding YAML for objects and structured JSON for logs.

## Kubernetes TUIs

| Tool | Lang | YAML view | Logs | Notes |
|---|---|---|---|---|
| [k9s](https://github.com/derailed/k9s) | Go | flat, syntax-highlighted, no folding | raw text stream | The incumbent. Edits go through `$EDITOR`. Has attach, port-forward and plugins. |
| [kubetui](https://github.com/sarub0b0/kubetui) | Rust / ratatui | flat | **JSON pretty-print toggle, jq/JMESPath filters**, regex and label selectors | Closest on the log side. Decodes base64 in Secrets. |
| [kdash](https://github.com/kdash-rs/kdash) | Rust | flat | raw | Dashboard style |
| [b4n](https://github.com/fioletoven/b4n), [k8s-tui](https://github.com/otavioCosta2110/k8s-tui) | Rust / Go | flat | raw | Small or learning projects |

## Log-only tools

- [kl](https://github.com/robinovitch61/kl): an interactive Kubernetes log viewer that pretty-prints structured logs inline across pods, namespaces and clusters. This is the best reference for the log UX.
- [loggo](https://terminaltrove.com/loggo/): a TUI for JSON logs from files, Kubernetes and cloud logging.
- [Gonzo](https://www.controltheory.com/gonzo/): auto-detects JSON, logfmt and plain text, with live analysis.
- json-log-viewer: expandable JSON records.

## Generic tree viewers (not Kubernetes-aware)

- [otree](https://github.com/fioncat/otree): a JSON/YAML/TOML tree in a TUI with expand and collapse, plus a raw pane next to it.
- [yam](https://github.com/simota/yam): a YAML TUI with folding, inline editing, path extraction and structural diff.
- [dtarasov7/yaml-viewer](https://deepwiki.com/dtarasov7/yaml-viewer): a YAML TUI with base64 and X.509 decoding for Kubernetes and CI files.

## The gap coralctl fills

The pieces already exist, but in separate tools. Nobody combines them:

1. **Folding YAML tree for live cluster objects** (otree and yam do this, but only for files)
2. **Schema-aware editing** with OpenAPI validation and a field picker. None of the tools above have this.
3. **Structured logs** (kubetui and kl do this, but without object navigation that matches k9s)
4. **Relationship views** (what uses this ConfigMap/Secret/Service; which pods a NetworkPolicy matches). None of the tools above have this.

The UX to borrow:
- folding keys from yam (`Enter`/`o` toggle, `O` expand all, `C` collapse all)
- tree on one side and raw view on the other, from otree
- the JSON toggle and jq filter from kubetui
- inline pretty-printing from kl
