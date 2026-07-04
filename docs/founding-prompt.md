# The founding prompt, rewritten with hindsight

Written 2026-07-04, after the milestone roadmap completed (M1-M12 shipped or
ruled out, v0.5.1 released). This is the CLAUDE.md we would write if starting
Klyx - or the next app in its class - from scratch. The original founding
prompt was ~70% of this; the delta is everything the 2026-07-02 full review
and the months of building taught us. Part 1 is the prompt itself,
self-contained and reusable. Part 2 is the GUI/UX rationale behind its
choices.

---

## Part 1 — the prompt

```markdown
# Klyx

A platform-engineer-grade Kubernetes desktop client. Existing tools treat
clusters as a kubeconfig dropdown, hide GitOps behind plugins, dump CRDs as
alphabetical lists, and render Gateway API as raw YAML. Klyx is fleet-first,
GitOps-primary, and speaks each controller's language correctly.

## Owner context
Built by a Senior Platform Engineer running six AKS clusters (DEV/STG/PRD,
West+North Europe) with Cilium ClusterMesh, Envoy Gateway, FluxCD, External
Secrets Operator + Reloader, cert-manager, and an LGTM stack — plus a k3s
homelab (ARM) running Argo CD. Provider-agnostic by design. The PRD clusters
are 5-10k pods: apiserver load and informer memory are real constraints, not
theoretical ones.

## What it is
Native desktop app (Go + Wails v3, React/TS webview) connecting via standard
kubeconfig. It is THE daily driver: pods/logs/events/nodes/day-2 verbs, plus
diagnosis lenses for Flux, Argo, Gateway API, cert-manager, and ESO. Klyx
drives controllers (reconcile, sync, drain, rollback) but NEVER authors
desired state — Git remains the source of truth. Strictly single-user.

## Locked decisions
- Go 1.2x + client-go, Wails v3, React/TS, zustand, CSS variables (8 themes,
  light + dark mandatory), Tabler icons. All platforms, signed releases.
- Radix UI headless primitives for dialogs/popovers/selects/tooltips
  (bespoke styling on top); TanStack Virtual for every long list. Never
  hand-roll focus traps, dismiss handling, or windowing math.
- Wails codegen (frontend/bindings/) and the built frontend (dist/) are
  GITIGNORED. Every CI job must regenerate them; binding method IDs are
  FNV-1a of "pkgPath.Service.Method" if you ever need one by hand.
- CI on every push from day one: go vet + go test -race + gofmt + tsc +
  vitest + the theme-contrast validator. cmd/klyx only compiles where GTK
  headers exist — add compile-time assertions
  (`var _ ServiceConn = fleet.Conn(nil)`) so interface drift fails
  everywhere, not just in CI. CLI installs in release jobs need retry +
  pinned direct-download fallbacks (package feeds have outages).
- Every milestone starts with a short plan doc in docs/plans/ and ends with
  a native eyeball pass over the /gallery route. Tag releases only after
  the eyeball pass. Wails is alpha: pin it, and schedule upgrade windows.

## Engineering principles (each of these was once a production bug)
1. Informers for live state; never LIST per render. Metadata-only informers
   for high-cardinality kinds; strip managedFields with a TransformFunc;
   TTL-cache discovery. Serve reads from caches you already maintain.
2. Capability detection over assumption. Absent tools get NO nav — not
   dead-end screens. Detection = API group served + controller workload
   healthy, kept live afterward.
3. Vocabulary correctness is a correctness property, not polish. Every
   condition reader gets an explicit per-type POLARITY table (ErrorOccurred:
   True=bad!) with tests pinned against real controller output — never
   invent fixtures from memory, and never let a test enshrine a guess.
4. Error honesty: an empty list and a failing list are different facts.
   Watch errors, RBAC denials, truncation (LIST caps), and staleness
   (generation > observedGeneration → dim + "Syncing…" chip; refresh-time
   lag for ESO) must all render as themselves. Never confidently green.
   Name the family: every degraded data state has a visible costume.
5. Safety is enforced in the BACKEND. Destructive verbs consume a one-shot
   typed-confirmation grant server-side; names are DNS-1123-validated before
   any argv; kubectl/helm flags that destroy data (prune, emptyDir deletion,
   force) are explicit per-operation opt-ins, never silent defaults.
6. Anything opened/closed asynchronously (lazy informers, event listeners,
   subscriptions) gets single-flight + desired-state flags, and a test that
   storms open/close under -race. Frontend effect cleanups must handle the
   promise-resolves-after-unmount case.
7. Confirmation ≠ composition. Optionless verbs get a lightweight typed-
   confirm dialog; multi-option operations get a composer (option tiles with
   explanation bodies, context tiles, a command preview for shell-outs).
   Decide which one a verb needs BEFORE building its dialog.
8. Build the design system FIRST: one primitives module (Badge, Chip, tiles,
   status dots, tone maps) and forbid per-view copies. Ship a /gallery dev
   route rendering every primitive in all themes. Validate every fg/bg token
   pair ≥4.5:1 in ALL themes with alpha-composited WCAG math, in CI.
9. Key all UI state (selection, pending actions) by stable identity, never
   array index — every list is live and reorders under you. Every list view
   ships with j/k/Enter/Esc list-keys or documents why not.
10. Shell-outs (kubectl/helm/flux): argv arrays only, `--` before positional
    names, shared binary resolution (env override → PATH → platform
    fallbacks), stderr folded into errors.

## The repeatable lens recipe (Flux, cert-manager, ESO all fit it)
Pure parser package (unstructured in, models out, zero client-go) → lazy
watch group in the data layer → push service with a domain-snapshot dirty
check + heartbeat → capability-gated, broken-first view where the BOUND
resource's health (source / issuer / secret store) is the headline, and the
deepest stuck node is flagged. Suspended/paused states are neutral, never
broken.

## Milestones
M1 skeleton+one cluster · M2 fleet view · M3 Flux lens · M4 CRD browser ·
M5 Gateway topology + mesh edges · M6 daily driver (pods/logs/day-2 verbs) ·
M7 Argo · M8 inline metrics · M9 cert-manager lens · M10 ESO lens.
No headless/serve mode. No cert-renew verb (ACME rate limits). No Helm
install/browse. Release early (v0.1 at M2), tag minor per milestone.

## Working style
Direct, principle-led, technically precise. Push back on weak assumptions.
Hyphens over em dashes. Stress-test decisions before committing.
```

---

## Part 2 — the GUI/UX rationale

### Shell: Wails again, eyes open
The app's center of gravity is client-go — informers, discovery, SPDY
port-forwarding, exec plugins. Any shell that isn't Go-native forces an IPC
sidecar or a rewrite of that layer. Tauri + kube-rs is the only credible
alternative (for a Rust-native team); Electron stays banned. But the Wails
tax is real and belongs in the founding rules: it is pinned to an alpha; the
codegen bindings are gitignored (hence the FNV-1a hand-binding recipe and
CI regeneration); and cmd/klyx only compiles where GTK headers exist, which
once hid a real type error from every headless environment until CI caught
it. The mitigations — compile assertions, bindings discipline,
regen-in-CI — are day-one rules above.

### UI layer: the one deliberate change from history
Keep React + TS + zustand (cluster-scoped slice guards earned their keep)
and CSS custom properties for theming. Change two things:

- **Radix primitives instead of hand-rolled dialogs/popovers/selects.** The
  history: ConfirmDialog shipped with no Escape, no focus trap, no
  role="dialog", retrofitted months later; the OperationDialog then needed
  the same work hand-copied. Radix is headless — the bespoke platform-tool
  look survives — and the entire a11y/focus/dismiss bug class never exists.
- **TanStack Virtual instead of a hand-rolled VirtualList.** The three bugs
  the review found in ours (stale rAF scrollTop, index keys, no
  ResizeObserver) are precisely the solved problems, and variable-height
  support gives log-pane virtualization for free.

Deliberately NOT changed: no Tailwind, no opinionated component library
(MUI/Chakra). The information-dense 10-11px mono-heavy voice is the app's
identity; heavyweight libraries fight you for it. Inline styles are crude
but agent-friendly (grep-able, colocated) — keep them for layout, put only
the tokened surfaces in primitives.

### UX process: formalize earlier what emerged late
Kept from the original playbook: mockups before code, reference screenshots
per lens, broken-first as product law, ⌘K as primary nav, vocabulary
correctness. Promoted from lessons to laws:

1. **Dialog taxonomy up front** — confirmation ≠ composition arrived at
   dialog #4 (the drain composer, which also surfaced a silently-hardcoded
   `--delete-emptydir-data`); it should exist at dialog #1.
2. **Keyboard-first as a spec** — j/k was retrofitted onto three triage
   views; "every list view ships with list-keys or documents why not" makes
   it free.
3. **The honesty visual language as a named system** — staleness dimming,
   "first 500 only", watch-error banners, and stale-metrics chips all
   express one idea (never confidently green) and were invented one at a
   time. Naming the family makes each instance a lookup, not a design
   decision.
4. **The contrast validator in CI** — ~60 lines of alpha-composited WCAG
   math that would have pre-caught the hardcoded PolicyChip palette, the
   dark-theme danger buttons, and the tertiary label token.
5. **A /gallery route** — every primitive in all 8 themes on one screen
   turns the native eyeball pass from a 50-item walk into one page.
