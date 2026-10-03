# Naming

**Decision (2026-10-03):** the project is **coralctl**. People call it "coral". The repo and package are named `coralctl` so they're easy to find in search.

## Why coral

- It's a branching structure that lives under the sea, so it fits both the folding tree view and the nautical naming of k9s and Kubernetes.
- It's short, easy to type and looks good in a status bar.

## Availability check (2026-10-03)

| Channel | `coral` | `coralctl` |
|---|---|---|
| Homebrew formula/cask | free | free |
| krew (kubectl plugin) | free | — |
| crates.io | taken (abandoned cargo-check tool, 2019) | free |
| PyPI | taken (DNA design lib) | not checked |
| npm | taken (Express REST framework) | not checked |
| GitHub `coral/coral` | empty repo, 0 stars | — |

No existing Kubernetes TUI uses the name.

## Search clutter (why the `ctl` suffix)

"coral" is used by many unrelated projects:

- Google Coral (Edge TPU hardware), which dominates "coral kubernetes" results
- CoralOS by Coral-Protocol, which calls itself "Kubernetes for AI agents"
- Coralogix, which has a Kubernetes operator
- Vox Media's Coral comment platform, a robotics framework (swanbeck/coral_cli) and an AI-agent dashboard (cdknorow/coral)

Not checked yet: domain names and trademarks.

## Other names considered

kelp, bosun, bridge, kdeck, origami, bonsai, arbor.
