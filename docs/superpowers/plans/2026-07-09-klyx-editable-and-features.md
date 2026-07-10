# Klyx feature review — editable-everything, ad-hoc deploy, ranked features

Date: 2026-07-09. Source: 6-agent full-codebase review + high-effort synthesis (all claims cite real file:line).

## Verdict — AI Settings write path

**Worth it now: NO (defer).**

The pain is real but low-frequency and cheap to escape. AI config (backend hosted/local, BaseURL, APIKey, Model, AllowProtectedEgress, TLSSkipVerify) is read once at startup: main.go:285 says the client is 'built once from the startup config; nil when off', and AIService captures backend/allowEgress/client as fixed fields (ai_service.go:58-59). So changing it means hand-editing the YAML and restarting. But (a) this is a one-time-per-machine setup you paste your key/endpoint into once and essentially never touch again; (b) CLAUDE.md guarantees a sub-second-startup ~10-20MB native binary, so 'restart' is a sub-second cost, not an Electron reload; (c) v1.0.0 is gated only on the two eyeball checklists + the real-LLM smoke test, and this is not on that path. The effort is genuinely medium, not small: a live AI-config write needs a config marshal/write-back (append.go:17 only APPENDS cluster entries, there is no general config writer), a hot-swap of the immutable aiClient/backend/allowEgress fields (would need a mutex-guarded or atomic-pointer swap mirroring protectguard.go:73-76), a new SettingsView section, and the DTO/remap/binding round-trip (the exact MEMORY.md field-drop gotcha). That is a lot of new mutable surface, including holding a plaintext API key in the webview draft, to remove a sub-second one-time friction.

**Recommendation:** Defer the write path. Ship a near-zero-cost honesty stopgap NOW instead: extend the existing SettingsView.tsx (which already copies the config file path to clipboard, :157) with a read-only 'AI backend' panel that reports the current state from the existing AIAvailability DTO (ai_service.go:72 already exposes Backend + Ready) and, when off/misconfigured, shows the exact ai: YAML block to paste and the file path. That kills the real friction (knowing WHAT to write and WHERE) without any new mutable config surface. Revisit the live write path only if AI backend churn becomes a real recurring complaint; when you do, build it on the AddClusters write-plus-hot-add precedent (config_service.go:172) and swap the client behind an atomic pointer like ProtectGuard, and add the reflection field-parity test first so the new DTO fields cannot silently drop.

## Verdict — make everything editable + ad-hoc (non-GitOps) deploy

**Go, but in three sequenced tiers, and never as a resource-authoring wizard. Tier 1 (safety enablers, ship first, all small-to-medium): the DTO field-parity reflection test, a conflict-aware Force:false apply mode, and an authoritative GitOps-ownership warning. Tier 2 (the 'edit anything you can see' payoff): reuse the ALREADY-generic, already-guarded ApplyResourceYAML editor in the Flux/Argo/cert-manager/ESO/pod lens detail panels, which already render YAML but have no apply path. Tier 3 (the deliberately-non-GitOps ad-hoc/scratch surface the owner asked for): a create-from-scratch composer plus a TTL'd diagnostic-pod launcher, both hard-bifurcated from managed objects by labeling, ephemerality, and steering to unmanaged namespaces. Do NOT ship Tier 3 before Tier 1's ownership honesty exists.**

### Design (reuse what exists)
Reuse everything. The apply primitive is already create-capable: ClusterConn.ApplyResourceYAML (fleet/crd.go:119) does an SSA .Apply() that upserts, so it CREATES when the object is absent; it is gated by validateApplyTarget (crd.go:159), the masked-Secret block (crd.go:135-137), checkProtected on real applies (crd_service.go:174-175), and dry-run via metav1.DryRunAll (crd.go:140). For Tier 2, drop the existing InstanceDetail.tsx YamlPanel (~:995, with baseline/draft/live tracking, buildLineDiff, resourceVersion-conflict detection, protected arming) into the lens panels; the backend is already generic over any GVR so this is almost pure frontend wiring. For Tier 3, add exactly two backend primitives: (1) resolveGVK() wrapping the TTL-cached ServerResourcesForGroupVersion (disccache.go) into a generic apiVersion/kind -> GVR+scope RESTMapping so arbitrary pasted/multi-doc YAML resolves without a passed instance; (2) CreateResourceYAML that derives GVK/name/namespace from the doc ITSELF (validateApplyTarget against the parsed YAML, not an open instance), splits on '---' for multi-object bundles with honest per-doc partial-failure results, stamps every object with a klyx.dev/adhoc label + created-by/at + TTL annotation, uses a DISTINCT FieldManager (klyx-adhoc) with Force:FALSE so a name collision errors instead of silently co-owning a controller's object, and makes a passing server dry-run a HARD precondition. The diagnostic-pod launcher creates a labeled netshoot/busybox pod (or a Job with ttlSecondsAfterFinished + activeDeadlineSeconds) into a klyx-scratch namespace it offers to create, then deep-links to the existing live-logs + exec escape hatch (podactions.go, exec_service.go). A Scratch lens lists all klyx.dev/adhoc objects fleet-wide with age + bulk cleanup, reusing the bulk-delete confirm.

### GitOps coexistence (the load-bearing honesty story)
This is the load-bearing honesty story and today it is incomplete in the DANGEROUS direction. The current edit warning, detectGitOwner (InstanceDetail.tsx:83), reads a per-object owner LABEL and renders 'Live edits may be reverted by GitOps' (:1130). That misses two cases that matter more than the one it catches: (1) a Git-managed-but-unlabeled object gets NO warning yet still reverts on reconcile; (2) a brand-new ad-hoc object created INTO a Flux/Argo-managed namespace carries no label, so it shows no warning at all — but if the managing Kustomization/Application has prune enabled, the object is DELETED on the next reconcile, which is strictly worse than the revert case the UI does warn about. The fix uses signals ALREADY in the watched stores: Flux status.inventory.entries (flux.go:304) + spec.prune + spec.targetNamespace, and Argo status.resources[] (argo.go:213) + destination.namespace + syncPolicy.automated.prune. Build a namespace -> managing-unit + prune-state index and gate every write on it. For EDITS: cross-reference the target against the loaded inventory/resources and say precisely 'Kustomization X owns this object; your change reverts on next reconcile', naming which signal fired, falling back to the label only when stores are not loaded. For CREATES: red 'this object is NOT in Git and will be DELETED on the next reconcile' when the target namespace maps to a pruned unit, amber for managed-but-not-pruned, green ONLY when no managing unit is detected. Because this is heuristic (a Kustomization can target a namespace via its manifests without spec.targetNamespace; ApplicationSets fan out; multi-tenant repos exist), it must warn on ANY managing unit and NEVER assert safety beyond 'no managing unit detected' — a false 'safe' that precedes a Flux delete is the worst possible outcome. Finally, once ad-hoc objects are labeled, tag them as 'Klyx scratch object' in the GitOps/Argo OutOfSync drift surfaces (gitopsevents.go) so they read as intentional scratch, not unexplained drift — keeping the primary GitOps lens trustworthy while ad-hoc objects coexist.

### Risks
1) Identity risk: this is the feature that most tests the retired 'never author desired state' non-goal, and it directly undercuts the AI Suggest-fix stance which deliberately drafts-to-Git and never applies (ai_service.go:166). It is defensible ONLY if the bifurcation is hard and visible — ad-hoc objects are LABELED, EPHEMERAL, and STEERED to unmanaged namespaces, and the composer copy says 'scratch/diagnostic, not author your app here' out loud. If that framing blurs, Klyx becomes the creation wizard CLAUDE.md rejects. 2) Force:true is the single most dangerous default for broad editing: on a Flux/Argo-owned object it steals field ownership and the next reconcile fights back — silent drift. Default to Force:false everywhere the edit surface broadens; keep Force:true only for the existing narrow unstick-one-object case if at all. 3) Prune detection is genuinely dangerous heuristic guesswork; a false-negative means 'we said safe, Flux deleted it' — never claim safety. 4) Optimistic concurrency is currently implicit — it works only because ToYAML happens to retain resourceVersion (detail.go:208-214); a future manifest-cleaning step would silently downgrade to last-writer-wins, so pass resourceVersion as an explicit precondition. 5) Ad-hoc Secrets bypass ESO/SOPS and are an exfil foot-gun; extend the masked-Secret block to also gate freshly-typed Secret data and point at ESO. 6) Cleanup reliability: only cluster-enforced TTL (ttlSecondsAfterFinished/activeDeadlineSeconds) is trustworthy — prefer Jobs/deadlines over bare pods; the Scratch lens is the human backstop. 7) Discovery staleness: a just-installed CRD may not resolve inside the 60s disccache TTL — surface a clear 'unknown kind — refresh discovery' path, not a cryptic error. 8) Every new field crossing the pure-type -> DTO -> remap boundary can silently drop (the MEMORY.md gotcha); the field-parity test must land before this work multiplies field traffic in both directions.

## Ranked features (best value first)

### 1. Reflection-based pure-type<->DTO field-parity test  
`high value · S effort · low risk` — _Architecture / correctness enabler_

**Why:** Closes the #1 latent-bug source (the documented MEMORY.md gotcha: a new field on a pure Go type silently drops at the hand-written appbridge DTO/remap hop, caught by neither tsc nor vitest) BEFORE broad editing multiplies field traffic in both directions. Pure honesty-first hygiene; nothing user-facing but everything below depends on it.

**Sketch:** Add a table-driven test alongside appbridge/conn_assert_test.go (which already proves the pattern for METHOD parity via compile-time assertion). Walk each pure-type/DTO pair with reflect, comparing NumField + json/yaml tags, so a missing DTO field fails `go test ./internal/appbridge/...` instead of shipping a silently-empty field to the webview. Runs locally per project memory (tsc+vitest+go test all run; only the Wails GTK build cannot).

### 2. Conflict-aware apply mode (Force:false + surface field-manager conflicts + GitOps-owned warning)  
`high value · M effort · medium risk` — _Mutation / write safety_

**Why:** The hard prerequisite for 'make everything editable'. Today apply uses FieldManager 'klyx' with Force:true (crd.go:139), which steals field ownership from Flux/Argo and creates immediate silent drift on the objects those controllers own — directly crossing the source-of-truth principle. Broadening editing on top of Force:true would make Klyx a drift generator, the exact opposite of its diagnosis-led identity.

**Sketch:** Add a Force:false path in ClusterConn.ApplyResourceYAML (crd.go:139-142); map the resulting SSA conflict error to an actionable 'field X is owned by manager Y' message via formatKubernetesActionError (InstanceDetail.tsx:212). Keep the dry-run round-trip so the managedFields diff is visible pre-apply. Reuse the capability layer (detector.go) already consumed by ai_bundle.go:130-144 to detect Flux/Argo ownership and render 'this object is reconciled from Git — your edit will drift'. Also make resourceVersion an explicit apply precondition passed independently of the YAML body, so a future manifest-clean step cannot silently downgrade to last-writer-wins.

### 3. Authoritative GitOps ownership + create-time prune-reach warning  
`high value · M effort · medium risk` — _GitOps coexistence honesty_

**Why:** The honesty core of the whole editable/ad-hoc push, and incomplete today in the dangerous direction. detectGitOwner (InstanceDetail.tsx:83) reads a per-object LABEL, so it misses Git-managed-but-unlabeled objects on edit AND shows NO warning when you create into a pruned managed namespace — where the object gets DELETED on next reconcile. Fleet-first + honesty-first demand this be authoritative, not label-guessing.

**Sketch:** Build a namespace -> managing-unit + prune-state index from signals ALREADY in the watched stores: Flux status.inventory.entries (flux.go:304) + spec.prune + spec.targetNamespace, Argo status.resources[] (argo.go:213) + destination.namespace + syncPolicy.automated.prune. On EDIT: cross-reference the target and name which unit owns it and whether the change reverts. On CREATE: red 'not in Git, will be DELETED on next reconcile' for a pruned namespace, amber for managed-not-pruned, green only when no unit is detected. Heuristic — warn on ANY unit, never assert safety beyond 'no unit detected'. Also fold klyx.dev/adhoc objects out of the OutOfSync drift surface (gitopsevents.go) as intentional scratch.

### 4. Edit-anything-you-can-see: wire the locked apply into the lens detail panels  
`high value · M effort · medium risk` — _Mutation / write surface_

**Why:** The direct payoff of 'make everything editable', at almost no backend cost. The backend ApplyResourceYAML is already generic over any GVR and the Flux/Argo/cert-manager/ESO/pod panels already fetch and render YAML — they just have no apply path. Reusing the proven, guarded editor honors every rail already ratified (dry-run, version-conflict, protected arming, masked-Secret block).

**Sketch:** Drop the InstanceDetail.tsx YamlPanel component (~:995, with baseline/draft/live tracking, buildLineDiff, resourceVersion conflict at :1050-1052, armProtected typed-confirm at :1017-1021) into the lens detail panels. The name/ns/gvk lock from validateApplyTarget (crd.go:159) carries over unchanged, so a user still cannot rename or retarget an object. Depends on ranks 2+3 landing first so these edits default to Force:false and carry the authoritative GitOps-owned warning.

### 5. Ad-hoc apply/create composer (RESTMapping resolver + CreateResourceYAML + klyx.dev/adhoc marking + mandatory dry-run)  
`high value · L effort · medium risk` — _Ad-hoc / scratch (owner's explicit ask)_

**Why:** The owner explicitly wants a paste-a-manifest / quick-deploy path that is deliberately NOT in GitOps. No create-from-scratch entry point exists today (no .Create in internal/fleet; the sole Apply is welded to an open instance's ResourceRef). Buildable under the 2026-06-17 'functionality overrules non-goals' directive, but only if it is framed as scratch/diagnostic and hard-bifurcated from managed objects.

**Sketch:** Add resolveGVK() wrapping the TTL-cached ServerResourcesForGroupVersion (disccache.go) into a generic apiVersion/kind -> GVR+scope RESTMapping. Add CreateResourceYAML that derives GVK/name/namespace from the parsed doc ITSELF (validateApplyTarget against the YAML, not an instance), splits on '---' with honest per-doc partial-failure results, stamps klyx.dev/adhoc + created-by/at + TTL, uses FieldManager 'klyx-adhoc' with Force:FALSE so a name collision errors, calls checkProtected, and makes a passing server dry-run a HARD precondition. Frontend: a palette 'Apply YAML...' surface reusing YamlPanel wholesale (validateResourceYAML:284, buildLineDiff, formatKubernetesActionError). Composer copy must say 'scratch/diagnostic, not author your app here' out loud. Gate behind ranks 2+3.

### 6. Quick diagnostic-pod launcher into a scratch namespace, TTL'd, deep-linked to logs+exec  
`high value · L effort · medium risk` — _Ad-hoc / scratch (owner's explicit ask)_

**Why:** The headline ad-hoc use case: spin a netshoot/busybox/dnsutils pod to debug connectivity/DNS without dropping to a terminal, the one daily-driver hole still open. Because it targets an unmanaged klyx-scratch namespace by construction, prune risk is designed out — the safe way to cross the create line.

**Sketch:** A palette/node/namespace/workload action that creates a labeled scratch pod (or a Job with ttlSecondsAfterFinished + activeDeadlineSeconds) into a klyx-scratch namespace it offers to create, then deep-links to the existing live-logs (podactions.go) + exec escape hatch (exec_service.go). Pair with a 'Scratch' lens listing all klyx.dev/adhoc objects fleet-wide with age + one-click bulk cleanup (reusing the bulk-delete confirm from PodsView runBulkDelete) as the human backstop for TTL a cluster failed to enforce. Also enables kubectl debug ephemeral-container attach for distroless pods, consistent with the existing shell-out pattern.

### 7. RBAC access review: can-i (as ServiceAccount) + who-can enumerator  
`high value · M effort · medium risk` — _Diagnosis lens_

**Why:** The single highest-value UNTRACKED diagnosis for this stack: when a Flux/ESO/Argo controller is denied, the daily question is 'can SA X do verb Y on resource Z here?' The engine views RBAC objects and dangling roleRefs but has no SelfSubjectAccessReview/SubjectAccessReview surface. Stays strictly inside the read-only-RBAC boundary CLAUDE.md permits (viewing, not managing).

**Sketch:** Add a read-only SelfSubjectAccessReview / SubjectAccessReview surface that impersonates --as system:serviceaccount:ns:name, reusing the existing RBAC views + the reverse 'granted by' lookup (crd_reverse.go:127). Frame explicitly as a simulation. New backend verb hung off the RBAC instance panel; no mutation, so no protected gating needed — the risk is impersonation plumbing and clear framing, not cluster state.

### 8. Live node + per-pod/container utilization with a rightsizing delta  
`high value · M effort · low risk` — _Inline observability_

**Why:** Both inputs already exist (workloads/usage.go live cpu/mem + workloads/resources.go requests/limits) and the diagnostic delta is computed nowhere. NodesView shows only allocatable/capacity — 'which AKS node is hot, do I rebalance' is unanswerable in-app; metrics stop at the workload aggregate so you cannot see which pod/sidecar is hot. Cheap, read-only, high daily payoff on a six-cluster fleet. Frame as diagnosis (throttle/OOM/waste), NOT FinOps.

**Sketch:** Add a per-node instant-vector (or metrics.k8s.io NodeMetrics) reusing the existing metrics.Client + CapacityCell bar in NodesView. Extend the container_cpu/memory_working_set queries (already summed by namespace,pod in workload_metrics.go:16) to a per-pod/container breakdown in the pod inspector, reusing the saturation logic in saturation.ts to call out 'requests 8x actual (waste)' or 'pinned at limit -> throttle/OOM risk'. Keep the nil=no-data discipline (metrics/model.go) so a cluster without metrics-server shows a clear empty state.

### 9. Hubble L7 flow lens (drops, identities, cross-cluster backend health)  
`high value · L effort · medium risk` — _Network / Cilium (differentiation)_

**Why:** The most on-identity, most-differentiated add for this owner's Cilium BYOCNI + ClusterMesh + Envoy stack. The seam is already built (capability.HasHubble at capability.go:74; routemetrics Source interface with a documented Cilium slot at model.go:3) and it pays off design principles 3-5 directly: render the real data path, make ClusterMesh a live edge, solve the stated 'ATM blind to backend health' problem. No general K8s GUI integrates Hubble flows — only Cilium's own separate UI does.

**Sketch:** Add a Hubble Relay gRPC client behind a second routemetrics Source implementation (today only EnvoyClusterSource is wired). Render as a read-only flow/drop/deny overlay on the existing Gateway graph (GatewayTopology.tsx) plus a per-service drop/deny triage. Capability-gate on HasHubble so it degrades cleanly to a clear empty state when Hubble is absent; keep the nil=no-data discipline.

### 10. Cross-environment drift compare (DEV/STG/PRD of the same workload/HelmRelease/values)  
`high value · L effort · medium risk` — _Fleet / GitOps (differentiation)_

**Why:** The killer fleet-first feature no competitor has, and pure read-only so it violates nothing. The owner literally runs matched DEV/STG/PRD tiers across West/North Europe; 'why does PRD behave differently than STG' has no tool answer today — Klyx is fleet-first everywhere except the one place fleets hurt most.

**Sketch:** Compare live spec, image tag/digest, replica count, resolved Flux/Argo revision, and Helm values side-by-side across environments, drift-highlighted, reusing the existing YamlBlock + gitopsdiff.go rendering. Needs a same-workload identity heuristic across clusters (name + a configurable env-tag match off the existing fleet config Env/Region tags). AI Explain over the diff is a natural follow-on that reuses the grounded egress-gated ai_bundle path. Stays a viewer, no editing.

### 11. Rollout history + undo + pause/resume for workloads  
`high value · M effort · medium risk` — _Day-2 verb_

**Why:** restart+scale exist but a bad rollout has no in-app recovery, and rollout undo/history/pause are daily verbs. Undo is a controller verb (like the existing restart), not desired-state authoring, so it fits the revised non-goals. Already named as staged-not-dropped in CLAUDE.md.

**Sketch:** Add revision history (read ControllerRevision / ReplicaSet history) to the workload inspector, plus undo/pause/resume verbs mirroring the existing WorkloadsService.RolloutRestart shape (workloads_service.go:39-53 -> podactions.go patch). Undo is destructive: carry the existing GitOps-owned-will-be-reverted warning and prd arming (checkProtected). Pause/resume is a low-risk spec.paused toggle.

### 12. Cross-platform exec/terminal escape hatch (Linux + Windows)  
`medium value · S effort · low risk` — _Correctness gap_

**Why:** Not a new feature — a correctness bug on the owner's OWN hardware. The exec/debug/plain-terminal escape hatches hard-gate on macOS (exec_service.go:113 and again ~236), so they are dead on the owner's Linux k3s homelab and on the shipped Linux/Windows build targets. Keeps the deliberate 'shell out, don't embed xterm.js' stance while making it actually work everywhere Klyx ships.

**Sketch:** Wire the equivalent terminal-launch for Linux (x-terminal-emulator / $TERMINAL) and Windows (wt / conhost) in OpenExecTerminal (exec_service.go:67), resolving the binary via the existing clibin pattern and keeping the argv validation already in place. No new product surface, just platform coverage.

## North star — a full conversational AI assistant (tool-grounded), phased

Added 2026-07-09 at owner request. This is where the AI arc (Explain -> cross-lens
-> Suggest-fix -> **converse**) was always heading. It is the largest and most
safety-sensitive item in the plan, so it is a distinct initiative with its own
spec, not folded into the tiers above.

**What.** A persistent chat panel (the prax-style assistant sidebar) answering NL
questions via an agentic tool-use loop. The tools are thin wrappers over Klyx's
existing services, and critically over the DIAGNOSIS RESOLVERS (the cross-lens
bundle builder in `ai_service.go`, the cert/Flux/ESO chain walkers, capability
detection, the reverse-lookups in `crd_reverse.go`, metrics). So the assistant
reasons over grounded diagnosis, not raw `kubectl` output — "why is the ollama
route down?" calls the cross-lens tool and returns a cited chain. No general K8s
assistant has this because none compute the chains.

**Why it fits.** Natural culmination of the shipped AI work; extends the
fleet/diagnosis/GitOps moat; on-identity IF it stays grounded + human-in-the-loop.

**Why it is the hardest item (honest).** An agentic loop re-opens, at much larger
scale, the exact egress/actuation/honesty surface hardened in PRs #19-#24. It can
fetch anything (every tool result is a potential secret leak — the P0
last-applied-config bug shows how one unguarded field escapes) and, with write
tools, can actuate. Non-negotiable gates:
- **One systematic redaction chokepoint** — every outbound tool result passes
  through a single masker (not per-call), extending `crd.MaskSecretData` + the
  last-applied-config strip to ALL tool outputs. (Overlaps editable Tier 1.)
- **Read-tools first** — ship a read-only assistant (query / diagnose / navigate by
  emitting the existing `NavIntent`s to drive the UI) before any write tool.
- **Write ONLY through the existing confirm/arming** — the assistant PROPOSES a
  day-2 action; the human confirms via `ConfirmDialog` (protected-cluster arming,
  prd typed-confirm). Never auto-execute. Reuses the ratified palette-verb pattern
  (open the existing confirm, never bypass arming).
- Cite every tool result; egress-gated + audit-logged like today's AI.

**Model tradeoff (honest).** Reliable multi-step tool-use needs a capable model:
hosted Claude does it well; local Ollama models tool-call unreliably. So the
zero-egress local-backend story WEAKENS for the full assistant (it likely defaults
to hosted), which is exactly why the redaction chokepoint + read-first are load-
bearing.

**Dependency + sequencing.** Depends on the same rails as editable Tier 1 (redaction
chokepoint, tool-layer arming) — build the rails once, they serve both. Then phase:
(a) read-only grounded assistant; (b) proposes day-2 through existing confirms;
(c) never auto-actuates. Effort: XL. Its own spec before any build.

## Summary

Klyx's write surface is already correct-by-construction for its narrow "unstick one diagnosed object" purpose: a single name/ns/gvk-locked server-side apply (fleet/crd.go:119), one-shot typed-confirmation grants (protectguard.go), masked-Secret fail-closed, and live-list reads that dodge informer-cache staleness after writes. To honor the owner's "make everything editable" ask WITHOUT betraying honesty-first/fleet-first/diagnosis-led identity, do it in this order: first the cheap safety/correctness enablers (DTO field-parity test, conflict-aware Force:false apply, authoritative GitOps-ownership+prune warning), THEN the editable-everything payoff (wire the existing locked apply into the lens panels), THEN the deliberately-non-GitOps ad-hoc/scratch surface (RESTMapping resolver + CreateResourceYAML + klyx.dev/adhoc marking + TTL'd diagnostic-pod launcher + a Scratch lens). Broadening editing before the Force:false + ownership-honesty work ships would turn Klyx into a silent-drift generator against Flux/Argo — the one thing this tool exists to make visible. AI Settings write path: defer (sub-second restart makes the pain trivial for a one-time-per-machine setup). Second tier not in the top 12: ESO force-refresh, CronJob trigger/suspend, Helm revision diff, fleet-wide search + AI fleet-triage narrative (gated on the metadata-informer refactor), generic delete verb.
