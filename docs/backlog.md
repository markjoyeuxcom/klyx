# Klyx backlog

Consolidated 2026-07-09; last pruned 2026-07-25 (the Karpenter lens shipped;
2026-07-22, the fleet-ops batch:
promotion tracker, upgrade readiness, connectivity can-i, survivability
headroom, expiry horizon; 2026-07-21: rollout history/undo/pause — the ranked
top-12 complete — and node + per-container utilization; 2026-07-20: RBAC
access lens, Hubble flow lens, fleet compare, the cross-platform exec fix;
2026-07-10: editable Tiers 1–3). Pulls together the "what's left" that was scattered across `CLAUDE.md`
deferred notes, `docs/brainstorm-questions.md`, and the eyeball checklists.
Living doc — prune items as they ship, add as they surface.

Everything for v1.0 is already on `main` and CI-green. The tag is gated on
verification, not code (section 0). Sections 1–7 are demand-driven — build when
the need is real, not top-to-bottom.

**Shipped in v0.10.0 (2026-10-12): health 1c.** One Observer
per cluster publishes a cluster summary that every attention surface reads -
the open cluster's mast, tree and Overview, the fleet's spine, meters and
cards - and the lenses read its shared rows. Closed by the slice 6
coverage-parity gate on a kind fixture
(`docs/superpowers/specs/2026-10-12-klyx-health-1c-slice6-parity.md` §7).
Acceptance: attention is derived from the cluster summary, with per-kind
coverage and as-of times. The informer-backed parts are live. The Helm and
Events parts are periodic snapshots (Helm every 5 minutes, plus coordinated
reads; Events every 60 seconds), not watches, and Events is evidence only,
never attention. The remaining request reads, each with an as-of time, are
the Helm and Events lens lists, the Gateway topology, Argo CD projects, and
Karpenter's controller and activity reads. A part the summary does not carry
reads "checking…" - a gap, never quiet. Verified on a local kind fixture; the
real-cluster smoke is on the v1.0.0 gate below.

---

## 0. v1.0.0 release gate — the only thing blocking the tag

- [ ] Eyeball checklist: **2026-07-04 GUI pass** (0/50) — `docs/eyeball-checklist-2026-07-04-gui-pass.md`
- [ ] Eyeball checklist: **2026-07-05 Signal Bench** (0/42) — `docs/eyeball-checklist-2026-07-05-signal-bench.md`
      (its visual language is retired by The Line in v0.14.0: only its behaviour items still apply)
- [ ] Eyeball checklist: **2026-10-15 The Line** (0/31) — `docs/eyeball-checklist-2026-10-15-the-line.md`.
      The owner walked slices 13 and 14 on the kind lab on 2026-10-16 ("looking nicely polished");
      the checklist itself is still unticked
- [ ] Eyeball the UI merged **since** those checklists (not yet on any list):
      quota meter bars, the Reloader "reloads on" well, pods/workloads truncation
      banners, the AI Explain / Suggest-fix panels, and the whole editable
      surface — the authoritative "Kustomization X owns this" banner in the YAML
      editor, the per-lens `manifest` buttons, the "deploy yaml" composer with
      its prune warning, the "debug pod" launcher, the Scratch panel, and the fleet-compare
      modal (drift columns against the live DEV/STG/PRD tiers), the Hubble
      flows panel (drop reasons against the live Cilium fleet — also verifies
      the new HasHubble detection fires on the real clusters), the Access
      lens (a good live check: can-i as kustomize-controller on a prd cluster,
      who-can get secrets in a team namespace, and the ServiceAccount
      inspector's "check access" cross-link prefilling the subject), and the
      utilization surfaces (node board cpu/mem used bars against the live AKS
      tiers — is the hot node believable; a pod inspector's per-container
      usage + a rightsizing chip on a known-oversized workload), and the
      rollout surface (history on a Deployment with real revisions; pause →
      resume round-trip on a dev workload; an undo on the homelab incl. the
      GitOps-owned warning + prd typed arming on a protected cluster), and the
      fleet-ops batch: the promotion tracker against the live DEV/STG/PRD Flux
      tiers (does the drift default catch a real straggler), the upgrade lens
      on an AKS cluster + the k3s homelab (EOL runway + skew believable), a
      connectivity check against a real deny-all namespace (and the PARTIAL
      Cilium label on the mesh clusters), a zone-loss simulation on prd-weu
      (is the verdict credible against known capacity), the expiry horizon
      (does it find the webhook CAs and any unmanaged TLS secrets), and the
      Karpenter lens (against the EKS lab once Karpenter lands there: does
      detection fire, do stuck claims name the right chain link, does the
      floor caption appear on the survivability module)
- [ ] **Eyeball the coverage programme (v0.15.0) on your own clusters** - verified
      on the kind lab only (native smoke 208/208 four ways; S7 also on AKS dev).
      Against EKS and on-prem: the Flux lens's Operator, Images, Composition and
      Notifications views; the Velero lens; Admission (a fail-closed webhook's
      backend, and a refused write's finding in the YAML panel and its toast);
      Load balancers (MetalLB, Cilium LB IPAM, k3s ServiceLB); workload identity
      (IRSA, Pod Identity) on an EKS pod; a resize reading; Gateway ListenerSets
      and L4 routes; the Alerts lens and Log history (with Alertmanager and Loki
      set in Settings > External endpoints); KEDA; Karpenter NodeClasses; the
      DRA lens (the ROCm homelab, once authorised); and the YAML editor's Apply
      on a labelled object
- [ ] **Real-cluster smoke of health 1c** (shipped in v0.10.0, verified only on
      the kind parity fixture): the mast, tree, Overview, fleet spine, meters and
      cards against the live AKS fleet - do the attention areas, counts and
      "checking…" gaps match what the lenses and kubectl show
- [ ] **Real-LLM smoke test** of AI (spec 2026-10-13 §5, slice 5) - Test then
      Explain on a local kind cluster through every provider: Claude, OpenAI and
      Gemini keys, a local Ollama server, and the Claude Code CLI on a Pro/Max
      sign-in. The CLI run also captures real stream-json, auth-status and doctor
      fixtures to replace the synthesized ones in
      `internal/ai/testdata/claude-2.1.296/`. Everything is so far verified
      against fake vendors and a no-login check of the real Claude Code binary.
      Cluster chat (v0.13.0, spec 2026-10-14 §9 slice 6) joins this smoke on the
      kind lab: a tools conversation per API-key provider, the snapshot mode on
      the Claude Code CLI, an Explain follow-up, a pod-log read, Stop and Clear.
      Done so far (2026-10-14): one chat question on an Anthropic key (Haiku
      5.5, tools mode: overview and attention reads), which found the chip
      defects fixed in v0.13.1. After v0.13.1, the Claude Code Test also
      admits the auto-updated build (2.1.300 passes the offline contract)
- [ ] **Live-cluster smoke of the write paths** (nothing destructive committed
      blind): a Force:false apply against a Flux-owned object should 409 naming
      kustomize-controller; a create into a namespace a pruning unit deploys into should state that the unit leaves an untracked object alone (amber; red only for an object carrying the Argo app's tracking metadata - semantics corrected in the 2026-10-03 review remediation);
      a debug-pod launch + Scratch delete-all should round-trip on the homelab
- [ ] **Windows check of the exec terminal**: open a pod shell from Klyx on a
      Windows host and type into it. The 2026-10-03 review remediation moved the
      Windows launch to PowerShell via CreateProcess; its Windows-only tests
      compile but have never run (CI and the dev machine are not Windows)
- [ ] Finalize `docs/release-notes-1.0.0.md` and cut the `v1.0.0` tag

This is a walk-the-app-and-tick-boxes exercise; no coding is blocking 1.0.

---

## 1. Editable / ad-hoc follow-ups (Tiers 1–3 shipped 2026-07-10, PRs #27–#31)

- [ ] **Deliberate-override apply** — Tier 2 ships Force:false only; a conscious
      "take ownership anyway" affordance (explicit force toggle with the
      ownership warning inline) was deferred from the plan. Build only if the
      409-and-stop flow proves too rigid in real use.
- [ ] **Scratch objects in the palette** — launch/cleanup as ⌘K verbs (one-shot
      NavIntents through the existing confirm, per the ratified pattern)
- [ ] Ad-hoc **Secret stance review** — the composer refuses `kind: Secret`
      outright (ESO/SOPS pointer). Deliberate; overrule if a real diagnostic
      need appears (e.g. a scratch pull-secret), then gate it hard instead.
- [ ] Scratch TTL for **composer-created** objects — the debug pod is
      cluster-TTL'd; pasted YAML is only inventoried (klyx.dev/adhoc), not
      auto-expired. A TTL annotation + reaper is possible but adds a controller-
      ish loop Klyx deliberately avoids; the Scratch panel is the backstop.

## 2. AI feature follow-ups (slices 1–5 shipped + merged 2026-07-09)

- [ ] **The AI programme** (owner, 2026-10-20): deeper evidence-cited investigation, opt-in proactive incident reports, guarded execution - `docs/superpowers/specs/2026-10-20-klyx-ai-investigations-roadmap.md`, with its open decisions in §6.

- [ ] **Review each new Claude Code release** for the CLI provider: run
      `KLYX_CLAUDE_CONTRACT=1 KLYX_CLAUDE_BIN=<binary> go test ./internal/ai -run
      TestClaudeContract_RealBinary`, capture the version's fixtures, and add its
      `{version, platform, sha256}` to `reviewedClaudeContracts`. Until then a
      Claude Code auto-update turns the CLI provider off (spec §1.5)
- [ ] Claude Code CLI provider on Linux and Windows (a reviewed contract per
      platform; Windows needs Job Object containment)
- [ ] Render the Suggest-fix YAML patch as a real fenced **code block** (currently
      mono pre-wrap) + a "copy just the patch" affordance
- [ ] Streaming responses; per-result caching
- [ ] **Durable AI egress audit history** — every Explain/fix: what was sent, to
      which backend, when. Beyond the 200-entry `localStorage` cap; an append-only
      JSONL likely beats SQLite for a single-user desktop app
- [ ] Fold **metrics correlation** into the bundle (Prometheus / M7) — "the latency
      spike correlates with this reconciliation"
- [ ] **North star: the conversational assistant** — spec'd in
      `docs/superpowers/plans/2026-07-09-klyx-editable-and-features.md` (tool-
      grounded chat over the diagnosis resolvers; per-action system-owned
      write/delete approval, never a blanket grant). Its Tier-1 rails (redaction
      chokepoint, arming) partially exist now; wants its own spec before build.

## 3. Ranked feature candidates (from the 2026-07-09 full-codebase review)

**COMPLETE** — all twelve ranked items are on `main` (the last, rollout
history/undo/pause, landed 2026-07-21 as #43). New feature work is now
entirely demand-driven from sections 1–4.

## 4. Feature deferrals (from the milestone notes)

- [ ] **Fleet compare follow-ups**: HelmRelease *values* compare; AI Explain over
      the cross-env diff ("explain why prd differs" — composes with the AI stack)
- [ ] **Hubble lens follow-ups**: per-namespace drop breakdown (needs Hubble
      context labels configured in Cilium); the topology-graph drop overlay;
      `hubble observe` flow tailing via the CLI escape-hatch pattern
- [ ] **Access lens follow-ups**: nonResourceURL checks (can-i on /metrics,
      /healthz); a "what can this subject do HERE" full listing
      (SelfSubjectRulesReview); who-can across all namespaces (today: one
      namespace per query, or cluster-wide grants only — the honesty caption
      says which)
- [ ] **Utilization follow-ups**: whole-node usage (needs a known node-label
      scheme or node_exporter — today's columns are pod-attributed vs
      allocatable, by design); a "which pod is hot on THIS node" drill-in from
      the node inspector; rightsizing chips on the workloads list (the
      per-container logic exists in `saturation.ts`)
- [ ] **Rollout follow-ups**: undo/pause as ⌘K palette verbs (one-shot
      NavIntents through the existing confirm, the ratified pattern); a
      revision diff (old vs new template YAML) in the history rows
- [ ] **Fleet-ops batch follow-ups**: promotion — pin a source and diff unit
      revisions against ITS head (needs revision-format awareness); upgrade —
      Git-side manifest scanning for deprecated apiVersions (pluto-style, via
      the local-clone-root config M10-f wants anyway); connectivity — evaluate
      CiliumNetworkPolicies natively (today: counted + labeled PARTIAL);
      headroom — taints/affinity modeling; expiry — kubeconfig client-cert
      expiry (config layer), kubeadm control-plane certs (needs node-level
      read), ESO token staleness fold-in

- [ ] **M10-f**: `flux diff --path` per-cluster local-clone-root config (makes
      `flux diff` resolve reliably)
- [ ] **ESO**: `ClusterExternalSecret` / `PushSecret` kinds + a day-2 force-refresh
      verb
- [ ] **Karpenter lens follow-ups** (lens shipped 2026-07-25, #48): NodeClaim →
      Node cross-link into the node board; a "requirements that couldn't be
      met" breakdown for stuck claims (parse status on the owning NodePool's
      unschedulable pods); v1beta1 API fallback verification against an older
      install (preferredVersion handles it, untested live)
- [ ] **Reloader**: a capability nav badge/section (detection + forward view +
      reverse drill-in + search/match all shipped)
- [ ] MeshStrip patch-bay cosmetics
- [ ] WatchErrorBanner retry countdown (needs per-watch retry state)

## 5. Staged for owner review (GUI-pass items — parked, not dropped)

- [ ] **Pinned resources** — the Q16 product-shape decision
- [ ] Density setting (needs a spacing-token pass)
- [ ] VirtualList ARIA (needs per-row cooperation)
- [ ] Fleet-wide palette search (rides the metadata-informer work)

## 6. Engineering backlog

- [ ] **Linux arm64 release build** — off in `release.yml` since v0.10.0 (it
      never built): the Wails Docker cross-compile (`wails3 tool
      docker-mounts`, `Dockerfile.cross`) reads `go.mod` from `cmd/klyx` and
      mounts only `cmd/klyx`, but the module root is the repo root. Options:
      an arm64 runner (GitHub's free arm64 Linux runners are public-repo
      only), an aarch64 cross toolchain with arm64 GTK4/WebKitGTK dev libs on
      the amd64 runner, or a custom Docker build that mounts the module root
      and builds `./cmd/klyx`
- [ ] **metadata-informers** for heavy list views — the interim (5000-cap + honest
      truncation banner) shipped; the full shared-informer refactor only if
      per-second re-lists actually bite
- [ ] LogsPane virtualization
- [ ] `Conn` interface split — grew again with the editable work (GitOpsOwnership,
      CreateResourceYAML, LaunchDiagnosticPod, ListAdhocObjects,
      DeleteAdhocObject); the god-interface pressure is real now
- [ ] golangci-lint over `cmd/klyx` — currently scoped to `internal/` + `cmd/klyxctl/`
      (linting the Wails main needs the GTK deps in the lint job)

## 7. Blocked on you

- [ ] **GitHub Actions budget exhausted** (hit 2026-07-21) — every CI job fails
      in 2s with "an Actions budget is preventing further use" (private repo,
      paid minutes). Raise the spending cap: github.com/settings/billing →
      Budgets and alerts. PRs #41–#48 merged on full local gates (same
      commands CI runs); the first green run on main after the reset
      re-verifies them.
- [ ] **Release signing / notarization** — inert scaffold in `release.yml`; needs
      the Apple notarytool creds + a Windows code-signing path (likely cloud
      signing — Azure Trusted Signing / DigiCert KeyLocker — not a PFX)

## 8. Open architectural questions

See `docs/brainstorm-questions.md` (Q16 pinned resources, etc.).

---

## Recently shipped (so it's not mistaken for open)

Merged to `main` — do not re-add to the backlog:

**2026-10-21 - v0.15.1 (notes `docs/release-notes-0.15.1.md`), from the
owner's first checks of v0.15.0 on their own clusters:**
- An apply conflict names each field's manager from the server's causes and
  advises by kind of manager (Git for Flux and Argo, the owner for a
  Kubernetes controller, UID-fenced one step up, with "Open Deployment"); a
  Pod's editor says its spec is mostly immutable
- Same-name alerts are told apart by the labels that differ; several
  cluster values with no match point at `alertmanager.match`
- Two timing-dependent frontend tests fixed; Codex-reviewed (one revision)

**2026-10-21 - First-class coverage (v0.15.0, roadmap
`docs/superpowers/specs/2026-10-16-klyx-coverage-roadmap.md` and its slice
notes, notes `docs/release-notes-0.15.0.md`; PRs #50 and #51):**
- Flux in full: the Flux Operator (S1a), image automation (S1b), source
  composition and notification facts (S1c)
- Velero, read-only (S2a, S2b); admission backend availability (S3a) and
  proven attribution of Klyx's own refused writes (S3b); on-prem load
  balancing (S4); workload identity (S5); pods, correct (S6); Gateway API
  current (S7a, S7b)
- External endpoints with keychain credentials (C5); Alertmanager alerts
  (S8a) and Loki log history (S8b); KEDA (S9); node provisioning by
  provider adapter (S10); extended resources (S11a) and DRA (S11b)
- The shared contracts C1 to C5; the pod projection fix (S0) and embedded
  payloads sanitised on every path (S0b)
- The Line slices 15 (the generic browser) and 16 (the inspector widens and
  expands); the YAML editor's validator fixed (it refused every object with
  a prefixed label); the Go 1.27.3 floor
- Every slice Codex-reviewed before and after building; native smoke
  208/208 four ways on the kind lab

**2026-10-16 — The Line (v0.14.0, spec
`docs/superpowers/specs/2026-10-14-klyx-the-line-redesign.md` and its slice
notes, notes `docs/release-notes-0.14.0.md`):**
- The redesign from the owner's "Klyx redesign" mockups: night and day
  themes with bundled type and shape-coded marks (slice 1); the fleet drawn
  as a railway line with Needs you, the Ledger and the Horizon (3, 5); the
  location bar, lens index, station strip and UID-pinned inspectors (4);
  the lens sweep and the station page (11); design fidelity against the
  mockups (13)
- New data (2, 7): rule-backed since/reason, the transition stream and
  observation log, the last managed-field update, route metrics per rule
- Read-only Git history through the system git (6): Settings > Git, the
  line ordered against its history, and Flux diffs built from the cache
- Drawings (8): the Flux dependency graph, the issuance and ESO chains, the
  Gateway data path, the chat read ledger
- The fleet metadata index and the command console (9); health
  notifications, off by default (10); the native WKWebView smoke (12)
- Describe (14): a Formatted view of kubectl's report (a conservative
  parser; the Text view is stdout byte for byte), find, copy, a
  kubeconfig-pinned copy command, and Describe in every lens inspector
- Every slice Codex-reviewed; native smoke 114/114 four ways on the kind lab.
  v0.13.1 was written up but never published: its changes ship in v0.14.0

**2026-10-14 — Claude Code any build, chat chips (v0.13.1, notes
`docs/release-notes-0.13.1.md`):**
- The Claude Code CLI admits any build at or above 2.1.296 once Test has
  checked it on this machine (owner decision after 2.1.299 and 2.1.300
  shipped hours apart): tested builds in `claude-builds.json` beside
  fleet.yaml, atomic with revocation, a broken build dropped whatever the
  record's permissions. AI spec §1.5 revision 2026-10-14
- Chat chips name their kind and say how many they hold back. Found by the
  owner's first real-model chat run (Anthropic, Haiku 5.5, tools, kind lab)

**2026-10-14 — Cluster chat (v0.13.0, spec
`docs/superpowers/specs/2026-10-14-klyx-cluster-chat.md`, notes
`docs/release-notes-0.13.0.md`):**
- A read-only chat drawer, one conversation per cluster (at most 8): the mast
  button, ⌘K and "Ask a follow-up" under Explain (seeded, UID-fenced)
- Seven validated read-only tools for API-key providers; a bounded
  per-question snapshot for the Claude Code CLI (no-tools contract unchanged)
  and for an OpenAI-compatible server that positively refuses tools
- One structural projection for every object (Secret, ConfigMap and Helm
  values never sent), a finite UID-checked pod-log read, byte budgets per
  result, call and conversation, turn fencing on Stop, Clear, disconnect and
  settings changes, a per-answer read ledger and metadata-only Activity
- Klyx's own markdown renderer, ledger-backed resource chips, and replies
  ordered by backend version with refusals kept apart from the conversation.
  Each slice Codex-reviewed; real-model smoke pending (§0)

**2026-10-14 — UI polish and code-quality fixes (v0.12.0, notes
`docs/release-notes-0.12.0.md`):**
- The 2026-10-13 joint UI review, all items in three slices: palette ranking
  and new entries, modal cheatsheet, keyboard fleet cards, 4.5:1 text
  contrast (pinned by a test), a calmer Overview, per-tool fleet cards,
  lamp-only syncing dim, one fault glyph, clamped messages, honest Settings
  copy and an unsaved-AI cue, plainer vocabulary, the shared Expiry table,
  Karpenter auto-select and a YAML Wrap toggle
- Codex's ten code-quality findings: a cluster-fenced, UID-pinned,
  discovery-wide Scratch inventory (with client-go-exact cached discovery),
  per-rule Gateway backend verdicts, nil-ready endpoints, periodic-only ESO
  staleness, scheduler-effective headroom, numbered CRD reads,
  reserved log-stream slots and a discarded-answer error for Claude Code.
  Each slice Codex-reviewed; live-cluster smoke pending (§0)
- v0.12.1 (same day): the owner's first real Claude Code Test reached the
  final result and was refused as "subagent use". Claude Code 2.1.296 writes
  all-zero subagent running totals on every result; the contract now accepts
  zero counters only (read from the shipped binary). A full real-run capture
  is still pending

**2026-10-13 — AI providers and Explain everywhere (v0.11.0, spec
`docs/superpowers/specs/2026-10-13-klyx-ai-providers-and-explain.md`):**
- Settings > AI: Claude, OpenAI and Gemini keys in the OS keychain (or a named
  env var), a local OpenAI-compatible server, or the installed Claude Code CLI;
  model picker, Test, origin disclosure, backend-armed protected egress, and one
  guarded write path for the `ai:` block (supersedes the old "Settings write
  path for the AI config" item)
- The Claude API fixes (no temperature, a thinking-sized budget, stop reasons
  decoded) and the same confirmed-finish rule for OpenAI and Gemini
- Explain with AI in every lens inspector and the palette, anchored on Klyx's
  verdict, UID-fenced, from an allowlisted, scrubbed, capped evidence document
  (supersedes "Native Explain affordance in the other diagnosis lenses")
- The Claude Code CLI under a reviewed contract (2.1.296 macOS arm64, Pro/Max
  only, per-call sign-in and policy checks, revocation until Test). Codex and
  agy stay unsupported. Each slice Codex-reviewed; real-model smoke pending (§0)
- v0.11.1 (same day): the first real-sign-in Test found that `--safe-mode` keeps
  Claude Code's built-in plugins on. The contract now pins each reviewed
  version's built-ins, and admission checks the startup report offline, with a
  fixed prompt, before any evidence is sent

**2026-07-25 — the Karpenter lens (#48):**
- Autoscaling diagnosis in Karpenter's vocabulary: capability detection
  (CRDs + controller health, both conventional namespaces), broken-first
  NodePool triage (limits vs provisioned capacity, 90% warning bars),
  stuck-first NodeClaim triage naming the first broken launch-chain link
  (Launched → Registered → Initialized), drifted/consolidatable as intended
  states, spot/on-demand identity, disruption event feed, and the
  autoscaler-aware floor caption on the survivability simulator. Read-only;
  merged on local gates during the CI budget outage

**2026-07-22 — the fleet-ops batch (#45, five features, one commit each):**
- Promotion tracker: fleet modal, applied GitOps revisions per cluster in
  env-ranked columns, drift + not-ready in the attention default, source-heads
  strip. Upgrade readiness lens: version/EOL/skew + served deprecated APIs vs
  a target minor. Connectivity can-i: static NetworkPolicy simulation between
  live pods (new pure internal/netpol), Cilium policies counted as PARTIAL.
  Survivability headroom: pool/zone identity + requests-based loss simulation
  with PDB drain blockers. Expiry horizon: fleet-merged cert lifetimes
  (TLS secrets, webhook CAs, cert-manager statuses), soonest first. All
  read-only; merged on local gates during the CI budget outage

**2026-07-21 — rollout history + undo + pause/resume (ranked top-12 complete):**
- Revision history in the workload inspector (Deployments via owned
  ReplicaSets, STS/DS via ControllerRevisions; images + change-cause; honest
  error/empty states). Undo with kubectl's exact semantics (old RS template
  minus pod-template-hash / ControllerRevision data as the patch), refuses
  the current revision, consumes the one-shot protected grant, confirm
  carries danger + typed prd arming + the GitOps-owned-will-revert warning.
  Pause/resume = spec.paused (Deployment-only, grant-gated); paused renders
  as an intended state. Merged on local gates during the CI budget outage (#43)

**2026-07-21 — node + per-container utilization (rightsizing delta):**
- Node board cpu/mem "used" columns: pod-attributed live usage vs allocatable
  with saturation tiers, joined from the confirmed per-pod cAdvisor series to
  spec.nodeName (no node-label assumptions); overhead exclusion + pod-list-cap
  under-count stated in captions. Pod inspector containers gained live usage,
  declared req/lim, and rightsizing findings (near-limit OOM/throttle danger;
  oversized request ≥8× with 50m/64Mi noise floors). nil = no sample
  throughout; merged on local gates during the CI budget outage (#41)

**2026-07-20 — RBAC access lens** (dates below corrected the same day — the
fleet-compare and Hubble entries were briefly misdated 2026-08-04/05):
- The "Access" section: can-i as the kubeconfig user or an impersonated
  ServiceAccount (SubjectAccessReview with the SA's groups — the authoritative
  answer, every authorizer), and a who-can enumerator over
  (Cluster)Roles/(Cluster)RoleBindings with exact API-server rule semantics,
  grant paths click-through to the binding inspector, and honesty captions
  (static-RBAC-only; cluster-wide-only without a namespace). ServiceAccount
  inspector gained a "check access" cross-link (NavIntent prefill). Read-only
  throughout; new pure pkg `internal/rbac` (#39)

**2026-07-20 — Hubble flow lens:**
- Drop/deny triage from Hubble's Prometheus metrics in the network lens:
  verdict rates, drop reasons ranked by rate, DNS/HTTP error rates; opt-in
  metric sets render only when present, all-empty says how to enable. Also
  fixed the never-populated HasHubble capability stub (#37)

**2026-07-20 — fleet compare:**
- Cross-environment drift compare: the same workload side-by-side across the
  fleet (dev→stg→prd columns), image/revision/replica/resource drift highlighted
  vs the origin, spec-hash "other drift" honesty, live Flux revision per
  cluster (#35)

**2026-07-10 — editable everything + ad-hoc (PRs #27–#31, the 2026-07-09 plan's
Tiers 1–3, complete) + the exec fix:**
- Cross-platform terminal escape hatch: exec/debug/plain terminal now launch on
  Linux ($TERMINAL / x-terminal-emulator / common emulators) and Windows
  (wt / cmd start), fail-closed quoting; was macOS-only (#33)
- Tier 1a: Force:false conflict-aware apply (a 409 names the owning manager,
  never silent drift) + the reflection DTO field-parity tripwire (#27)
- Tier 1b: authoritative GitOps ownership + prune-reach verdict from Flux
  inventory / Argo resources, wired into the YAML editor's warning (#28)
- Tier 2: every lens (cert, ESO, Flux, Argo, pod, workloads) cross-links its
  object to the ONE guarded editor — "edit anything you can see" (#29)
- Tier 3a: ad-hoc "deploy yaml" composer — multi-doc, klyx.dev/adhoc-stamped,
  create-only (collisions refuse), Secrets refused, prune-reach warned (#30)
- Tier 3b+3c: TTL'd diagnostic-pod launcher into klyx-scratch + the Scratch
  inventory with label-guarded delete / delete-all (#31)

**2026-07-09:**
- AI assist: grounded Explain (cert chain + cross-lens), Suggest-fix (Git-only,
  never applied), operator health in the bundle, adversarial-review remediation
  (incl. a P0 secret-egress fix) — PRs #19–#24
- Reloader awareness (capability + forward "reloads on" + reverse drill-in +
  search/match), quota meters, pods/workloads list caps, inspector reverse-lookups
  + dangling-roleRef, golangci-lint + ESLint, inspector Facts platform
