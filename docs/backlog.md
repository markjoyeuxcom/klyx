# Klyx backlog

Consolidated 2026-07-09. Pulls together the "what's left" that was scattered across
`CLAUDE.md` deferred notes, `docs/brainstorm-questions.md`, and the eyeball
checklists. Living doc — prune items as they ship, add as they surface.

Everything for v1.0 is already on `main` and CI-green. The tag is gated on
verification, not code (section 0). Sections 1–6 are demand-driven — build when
the need is real, not top-to-bottom.

---

## 0. v1.0.0 release gate — the only thing blocking the tag

- [ ] Eyeball checklist: **2026-07-04 GUI pass** (0/50) — `docs/eyeball-checklist-2026-07-04-gui-pass.md`
- [ ] Eyeball checklist: **2026-07-05 Signal Bench** (0/42) — `docs/eyeball-checklist-2026-07-05-signal-bench.md`
- [ ] Eyeball the UI merged **since** those checklists (not yet on any list):
      quota meter bars, the Reloader "reloads on" well, pods/workloads truncation
      banners, and the AI Explain / Suggest-fix panels
- [ ] **Real-LLM smoke test** of the AI feature (local Ollama backend, or a hosted
      key) — everything is currently verified against a fake client
- [ ] Finalize `docs/release-notes-1.0.0.md` and cut the `v1.0.0` tag

This is a walk-the-app-and-tick-boxes exercise; no coding is blocking 1.0.

---

## 1. AI feature follow-ups (slices 1–5 shipped + merged 2026-07-09)

- [ ] **Settings write path for the AI config** — `ConfigService.SetAI` (comment-
      preserving YAML write, reusing the `AppendClusters` machinery) +
      `AIService.Reconfigure` (rebuild the client live, not restart-to-apply) + a
      Settings form. Keep `fleet.yaml` the source of truth; optionally store the
      API key in the OS keychain rather than plaintext. (NOT SQLite — see the
      2026-07-09 discussion: a local DB adds no security and loses diffability.)
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

---

## 2. Feature deferrals (from the milestone notes)

- [ ] **M10-f**: `flux diff --path` per-cluster local-clone-root config (makes
      `flux diff` resolve reliably)
- [ ] **ESO**: `ClusterExternalSecret` / `PushSecret` kinds + a day-2 force-refresh
      verb
- [ ] **Reloader**: search/match forward-view; a capability nav badge/section
      (detection + forward view + reverse drill-in already shipped)
- [ ] MeshStrip patch-bay cosmetics
- [ ] WatchErrorBanner retry countdown (needs per-watch retry state)

---

## 3. Staged for owner review (GUI-pass items — parked, not dropped)

- [ ] **Workload rollout-undo** — a new *destructive* backend verb; wants your
      confirm/arming sign-off before it's built
- [ ] **Pinned resources** — the Q16 product-shape decision
- [ ] Density setting (needs a spacing-token pass)
- [ ] VirtualList ARIA (needs per-row cooperation)
- [ ] Fleet-wide palette search (rides the metadata-informer work)

---

## 4. Engineering backlog

- [ ] **metadata-informers** for heavy list views — the interim (5000-cap + honest
      truncation banner) shipped; the full shared-informer refactor only if
      per-second re-lists actually bite
- [ ] LogsPane virtualization
- [ ] `Conn` interface split
- [ ] golangci-lint over `cmd/klyx` — currently scoped to `internal/` + `cmd/klyxctl/`
      (linting the Wails main needs the GTK deps in the lint job)

---

## 5. Blocked on you

- [ ] **Release signing / notarization** — inert scaffold in `release.yml`; needs
      the Apple notarytool creds + a Windows code-signing path (likely cloud
      signing — Azure Trusted Signing / DigiCert KeyLocker — not a PFX)

---

## 6. Open architectural questions

See `docs/brainstorm-questions.md` (Q16 pinned resources, etc.).

---

## Recently shipped (so it's not mistaken for open)

Merged to `main` — do not re-add to the backlog:
- AI assist: grounded Explain (cert chain + cross-lens), Suggest-fix (Git-only,
  never applied), operator health in the bundle, adversarial-review remediation
  (incl. a P0 secret-egress fix) — PRs #19–#24, 2026-07-09
- Reloader awareness (capability + forward "reloads on" + reverse drill-in +
  search/match), quota meters, pods/workloads list caps, inspector reverse-lookups
  + dangling-roleRef, golangci-lint + ESLint, inspector Facts platform
