# Brainstorm

Initial feature brainstorm for coralctl, a Kubernetes TUI with a folding tree view of YAML objects and better navigation.

- modern, panel based TUI
    - inspiration
        - gh dash
        - herdr
    - status bar etc.
    - mouse first
- also subcommands
- caching
    - cache everything to be super fast
    - keystroke r - refresh
    - keystroke R - refresh dialog
        - default 5 minutes to prevent infinite waits
- editing
    - schema based in-line validation with colors
    - add new field with drop down search
    - adding nested field automatically adds parents
- viewing
    - collapse non-favorite fields
- tree structure
    - context - names pace- Workloads- resources
    - netpol, config, secrets, services show right pannel what uses it
- netpol
    - netpol source destination in right pannel
    - show matched pods with green

## k9s features
- attach pod
- view logs
- port-forward

## Ideas
- annotation for link to logs in viewer
- open in browser for pods
