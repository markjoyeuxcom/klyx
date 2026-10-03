# Klyx

A platform-engineer-grade Kubernetes desktop client.

## Why

Every existing Kubernetes GUI (Lens, FreeLens, Headlamp, K9s, Aptakube, Portainer, Rancher) has the same problems:

- Single-cluster mindset - the fleet is a kubeconfig dropdown
- GitOps state hidden behind a plugin (or absent entirely)
- CRDs dumped as a flat alphabetical list with no API group structure
- Gateway API CRDs rendered as raw YAML, not the data path they describe
- No ClusterMesh awareness
- Electron - slow startup, 150MB+ binaries
- Either single-user OR enterprise SaaS, nothing in between

Klyx is opinionated about all of these. See `CLAUDE.md` for the full brief.

## Tech stack

- Go + Wails v3 (native binary, no Electron)
- TypeScript + React in the webview
- client-go informer-based data layer
- Builds for macOS, Linux, Windows

## Repository layout

```
klyx/
├── CLAUDE.md                    # canonical project guide for every agent (and humans)
├── AGENTS.md                    # pointer to CLAUDE.md for agents that read AGENTS.md
├── README.md                    # this file
├── Makefile, Taskfile.yml       # build, test and lint entry points
├── .github/workflows/           # CI (push/PR gates, govulncheck) and release builds
├── cmd/klyx/                    # Wails desktop app and React frontend
├── cmd/klyxctl/                 # CLI entry point
├── internal/                    # fleet, capability, GitOps, Gateway, metrics, and bridge layers
├── design/                      # Signal Bench design of record (mockups, notes, tokens)
├── docs/
│   ├── backlog.md               # living status: v1.0.0 gate, shipped log, what's left
│   ├── design-principles.md     # design philosophy
│   ├── eyeball-checklist-*.md   # manual verification checklists
│   ├── release-notes-*.md       # release notes
│   ├── superpowers/{plans,specs}/  # feature plans and specs
│   ├── brainstorm-questions.md  # open architectural questions
│   ├── example-prompts.md       # prompts per phase
│   └── mockups.html             # the original six UI mockups (open in browser)
├── panels/                      # reference screenshots for the panel refit
└── scripts/                     # build helpers (version stamping)
```

## Status

Klyx is well past its initial foundation: fleet overview, Flux and Argo CD
lenses, cert-manager and External Secrets diagnosis, CRD/resource browsing,
Gateway topology, ClusterMesh edges, inline metrics, the editable surface, AI
assist, and daily-driver Kubernetes operations ship today (latest tag
`v0.9.0`). `docs/backlog.md` is the living status - the v1.0.0 release gate
and what's left - and `CLAUDE.md` carries the milestone history.
