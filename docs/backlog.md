# Klyx backlog

Consolidated 2026-07-09; last pruned 2026-08-04 (fleet compare shipped; earlier:
editable Tiers 1–3 + the cross-platform exec fix). Pulls together the "what's left" that was scattered across `CLAUDE.md`
deferred notes, `docs/brainstorm-questions.md`, and the eyeball checklists.
Living doc — prune items as they ship, add as they surface.

Everything for v1.0 is already on `main` and CI-green. The tag is gated on
verification, not code (section 0). Sections 1–7 are demand-driven — build when
the need is real, not top-to-bottom.

---

## 0. v1.0.0 release gate — the only thing blocking the tag

- [ ] Eyeball checklist: **2026-07-04 GUI pass** (0/50) — `docs/eyeball-checklist-2026-07-04-gui-pass.md`
- [ ] Eyeball checklist: **2026-07-05 Signal Bench** (0/42) — `docs/eyeball-checklist-2026-07-05-signal-bench.md`
- [ ] Eyeball the UI merged **since** those checklists (not yet on any list):
      quota meter bars, the Reloader "reloads on" well, pods/workloads truncation
      banners, the AI Explain / Suggest-fix panels, and the whole editable
      surface — the authoritative "Kustomization X owns this" banner in the YAML
      editor, the per-lens `manifest` buttons, the "deploy yaml" composer with
      its prune warning, the "debug pod" launcher, the Scratch panel, and the fleet-compare
      modal (drift columns against the live DEV/STG/PRD tiers)
- [ ] **Real-LLM smoke test** of the AI feature (local Ollama backend, or a hosted
      key) — everything is currently verified against a fake client
- [ ] **Live-cluster smoke of the write paths** (nothing destructive committed
      blind): a Force:false apply against a Flux-owned object should 409 naming
      kustomize-controller; a create into a pruned namespace should warn red;
      a debug-pod launch + Scratch delete-all should round-trip on the homelab
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

- [ ] **Settings write path for the AI config** — deferred by the 2026-07-09
      review verdict (sub-second restart makes file-edit friction trivial). The
      cheap stopgap first: a read-only "AI backend" panel in Settings showing
      current state + the exact `ai:` YAML to paste. Full write path
      (`ConfigService.SetAI` + `AIService.Reconfigure` + form) only if backend
      churn becomes a recurring complaint. (NOT SQLite — a local DB adds no
      security and loses diffability.)
- [ ] Render the Suggest-fix YAML patch as a real fenced **code block** (currently
      mono pre-wrap) + a "copy just the patch" affordance
- [ ] Streaming responses; per-result caching
- [ ] **Durable AI egress audit history** — every Explain/fix: what was sent, to
      which backend, when. Beyond the 200-entry `localStorage` cap; an append-only
      JSONL likely beats SQLite for a single-user desktop app
- [ ] Fold **metrics correlation** into the bundle (Prometheus / M7) — "the latency
      spike correlates with this reconciliation"
- [ ] Native Explain affordance in the other diagnosis lenses (Flux, ESO, Gateway)
      — today it's the generic inspector + the cert lens
- [ ] **North star: the conversational assistant** — spec'd in
      `docs/superpowers/plans/2026-07-09-klyx-editable-and-features.md` (tool-
      grounded chat over the diagnosis resolvers; per-action system-owned
      write/delete approval, never a blanket grant). Its Tier-1 rails (redaction
      chokepoint, arming) partially exist now; wants its own spec before build.

## 3. Ranked feature candidates (from the 2026-07-09 full-codebase review)

The un-built remainder of the ranked top-12 (see the plan doc for sketches):

- [ ] **RBAC access review** — can-i (as ServiceAccount) + who-can enumerator;
      read-only SubjectAccessReview, the highest-value untracked diagnosis
- [ ] **Live node + per-pod/container utilization** with a rightsizing delta
- [ ] **Hubble L7 flow lens** (drops, identities, cross-cluster backend health) —
      the most differentiated add for the Cilium+ClusterMesh+Envoy stack
- [ ] **Rollout history + undo + pause/resume** (undo is destructive: owner
      sign-off on the confirm shape first)

## 4. Feature deferrals (from the milestone notes)

- [ ] **Fleet compare follow-ups**: HelmRelease *values* compare; AI Explain over
      the cross-env diff ("explain why prd differs" — composes with the AI stack)

- [ ] **M10-f**: `flux diff --path` per-cluster local-clone-root config (makes
      `flux diff` resolve reliably)
- [ ] **ESO**: `ClusterExternalSecret` / `PushSecret` kinds + a day-2 force-refresh
      verb
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

- [ ] **Release signing / notarization** — inert scaffold in `release.yml`; needs
      the Apple notarytool creds + a Windows code-signing path (likely cloud
      signing — Azure Trusted Signing / DigiCert KeyLocker — not a PFX)

## 8. Open architectural questions

See `docs/brainstorm-questions.md` (Q16 pinned resources, etc.).

---

## Recently shipped (so it's not mistaken for open)

Merged to `main` — do not re-add to the backlog:

**2026-08-04 — fleet compare:**
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
