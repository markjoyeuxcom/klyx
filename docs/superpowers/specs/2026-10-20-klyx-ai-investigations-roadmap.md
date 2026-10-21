# Klyx AI programme: investigation, proactive reports, guarded execution

**Owner direction.** On 2026-10-19, asked which AI direction to take, the owner answered "all 3": deeper fleet, incident and dependency diagnosis; proactive investigation with suggested fixes; and guarded actions that change cluster state.

The owner's three ideas, 2026-10-20:
1. **Deeper investigation.** Extend the existing tools to inspect fleet differences, Git changes, metrics, alerts and dependencies. Each conclusion points to its evidence and distinguishes a suspected cause from a proven one.
2. **Proactive investigations and proposed fixes.** Users opt into investigations when something changes or fails. Each produces a bounded incident report: what changed, what is affected, likely causes, missing evidence and a proposed remedy.
3. **Guarded execution.** A proposed remedy becomes a concrete action card showing the cluster, the exact objects, the change, the GitOps consequence and the expected result. Klyx's backend protection and identity checks are reused, and the result is verified after execution.

This is a programme roadmap, not a design. Each phase gets its own note, Codex-reviewed before code and again after, like the coverage slices.

**It builds on:**
- `2026-10-13-klyx-ai-providers-and-explain.md`: providers, egress, scrubbing, the CLI contract;
- `2026-10-14-klyx-cluster-chat.md`: read-only tools, the read ledger, budgets.

## 0. What exists

- **Explain:** one object's allowlisted, scrubbed evidence, UID-fenced, anchored on Klyx's verdict. It is in every lens inspector.
- **Suggest-fix:** proposes Git changes only, and never applies them.
- **Cluster chat:**
  - one cluster per conversation, read-only, seven validated tools (or a bounded snapshot for the Claude Code CLI);
  - every read listed under its answer, nothing persisted;
  - the egress gate per cluster, with `allowProtectedEgress` on protected clusters.
- **The evidence the new tools can draw on, all read-only today:**
  - fleet compare;
  - Git history through the system git;
  - Flux diffs from the cache;
  - the dependency drawings: the Flux graph, the issuance and ESO chains, the Gateway data path, KEDA's links;
  - inline Prometheus metrics;
  - Alertmanager alerts (S8a);
  - Loki history (S8b);
  - a refused write's admission finding: the webhook or policy that refused it, and its candidates (S3b);
  - rollout history;
  - the Observer's verdicts and the ledger.
- **The verbs** (C4):
  - each is UID-armed (`checkArmed`/`patchArmed`), behind a ConfirmDialog, with the typed-confirmation grant (ProtectGuard) on protected clusters and a GitOps-ownership consequence line;
  - restart, scale, delete pod, cordon/drain, rollout undo and pause;
  - Flux reconcile and suspend/resume;
  - Argo refresh and sync;
  - KEDA pause and resume.
- **Health notifications** (`healthnotify`): off by default; episodes keyed by UID.

## 1. Programme contracts (every phase)

**A-C1. Evidence and certainty.**
- Every conclusion cites the reads it rests on, by the read ledger's IDs.
- **Klyx verifies the citations, not the model.**
  - A conclusion whose citations are missing from this investigation's ledger is shown as "unsupported".
  - **Proven** requires at least one citation of a *direct* class: Klyx's verdict, a condition or event naming the failure, a Git diff touching the field, an alert naming the object, or a metric past its threshold.
  - Timing, correlation or absence alone is **suspected**, whatever the model calls it.
- **Missing evidence is listed:** reads that failed, kinds that were unreadable or stale, and budgets that ran out.

**A-C2. Boundaries.**
- **One cluster per investigation,** as chat. Fleet difference is the explicit exception: its clusters are named, and each passes its own egress gate.
- **The structural never-send list is unchanged** (Secret data, ConfigMap values, Helm values, env values).
- **Identifier redaction is applied before clipping, across every AI path** (from S10's design review): cloud resource IDs and ARNs that providers write into condition text and that controllers copy onto other objects. It is tested with literal needles on the serialized Explain, chat and report documents.

**A-C3. Budgets.**
- Each investigation has bounded turns, tool calls, bytes and wall time.
- When a budget runs out, the investigation stops, says what it did not read, and never pretends to completeness.

**A-C4. Injection.** Cluster text is data, travelling only as tool results. An injected instruction can at most cause more reads of the same cluster, which are recorded. It can never cause a write: A3's actions require a human confirmation that the model cannot give.

**A-C5. Execution.**
- **The model never executes.** It proposes; Klyx validates the proposal, renders it, and the human confirms.
- **Every write goes through the existing verb path** and its guards, unchanged.

## 2. A1: Deeper investigation

**New read tools,** extending chat's catalogue and validated like it:
- **fleet difference:** fleet compare across named clusters (workload spec, image, Helm chart, Flux revision);
- **Git changes:** the GitOps unit's commits and diffs since its last healthy revision, read-only, from the approved Git URLs;
- **metrics:** Klyx's own PromQL templates for the object (rate, saturation, restarts, 5xx). The model chooses a template and a window and never writes a query;
- **alerts:** S8a's alerts for the object, its namespace and its dependencies;
- **dependencies:** the existing drawings as structured edges (dependsOn, source, issuer chain, store, route path, scaler and HPA), with each node's verdict.

**Output:** structured findings, each with its certainty (A-C1), its citations and the missing evidence. The investigation view renders them, and chat answers can cite them.

**Open:** whether the model may write PromQL or LogQL. The recommendation is templates only, because a model-written query is unbounded egress and cost.

## 3. A2: Proactive investigations

**Triggers, opt-in per cluster and per trigger class, all off by default:**
- a health episode opening (healthnotify's new fault or new degraded state);
- a GitOps revision changing on a unit that then fails;
- a critical alert firing (S8a).

**Unattended egress is consented at opt-in:**
- the settings name what is sent and to which provider;
- a protected cluster needs its own consent;
- a local model (`openaiCompatible`) can be required per cluster;
- one investigation runs per episode (deduplicated), with rate and cost limits per hour and per day.

**The incident report is bounded:**
- **what changed:** Git diffs, the revision, rollout history and events;
- **what is affected:** verdicts and dependents;
- **likely causes:** proven and suspected, each cited;
- **missing evidence;**
- **a proposed remedy:** an A3 action proposal, or a Git change as Suggest-fix gives it today.

**Persistence and notification:**
- **Reports persist locally:** the first durable AI record, with the egress audit of what was sent (`docs/backlog.md` §2's "durable AI egress audit history"), bounded by count and age.
- **OS notifications** only through the existing healthnotify opt-in; the notification links to the report.

## 4. A3: Guarded execution

**The action card** is rendered from a structured proposal that Klyx validates against the verb matrix: only the existing day-2 verbs (§0), with their parameters. It shows:
- the cluster and its protection;
- the exact objects, pinned by UID;
- the change;
- the GitOps consequence, from the same ownership check the manual verb uses;
- the expected result: the verdict to reach, and a bound on when.

**Execution is exactly the manual verb's path:**
- UID preconditions, the ConfirmDialog, and the typed grant on protected clusters;
- the human confirms every action, and nothing runs from a report unattended.

**Verification:**
- After execution, Klyx watches the target's Observer verdict until the bound, and says "resolved", "not yet" or "still failing", with evidence.
- It never retries automatically.

**Never:**
- a model-authored manifest applied to the cluster: desired-state remedies stay Git proposals, and Git remains the source of truth;
- a new verb introduced for A3;
- an action on a cluster other than the investigation's.

**Recorded crossing:** this crosses "AI Suggest-fix proposes Git changes only and never applies them" (CLAUDE.md's editable-surface crossing). It does so for the existing day-2 verbs only, behind the same human confirmation and guards as the manual verbs, under the 2026-06-17 directive.

**Open: writing to Git** (opening a branch or pull request for a desired-state remedy). That would cross "Git history through the system git, never writing a working tree", and stays out unless the owner decides it in.

## 5. Sequencing

**Decided (owner, 2026-10-21):** the coverage programme is complete (v0.15.0, with v0.15.1's fixes), and the AI programme comes next: A1, then A2 and A3, then extensions. That is option 1 below; the choice between the two options lapsed when coverage finished first.

- **Option 1** (the 2026-10-16 order): finish the coverage programme (S10, S11a, S11b, S3b), then A1, A2, A3, then extensions.
- **Option 2:** A1 right after S10. It reuses chat's contract and needs the evidence S8a, S8b and S9 just added. A2 and A3 follow the coverage programme.
- **Either way,** §6.1's Flux scenario needs only the A1-A3 machinery and one health-check parser. §6.2's Argo scenario also needs the Argo-parity work's hook results and any new Argo verbs.

**Each phase's definition of done:**
- its note with Codex review before and after;
- the gates;
- literal-needle egress tests;
- a real-LLM smoke on the kind lab (the v1.0.0 gate's open item);
- captures;
- the as-built notes.

## 6. The acceptance scenarios (owner, 2026-10-20)

The owner proposed an Argo scenario, then suggested Flux, or another scenario, instead. Flux comes first: Klyx already covers most of its chain. Argo follows once the Argo-parity work reads hook results.

### 6.1 The first scenario: a failed Flux rollout

> "Investigate this failed Kustomization, trace the health check that timed out to the Deployment and its crashing pod's logs, show the commit that changed it, propose a remedy, let me approve the mitigation, and check whether it worked."

The programme is accepted when this runs end to end on the kind lab, against a real model.

| Step | Phase | Exists today | Needed |
|---|---|---|---|
| The failed Kustomization | A1 | its verdict (`HealthCheckFailed`), message, `ReadySince`, and the last applied (healthy) and last attempted (failing) revisions | none |
| The health check to the Deployment | A1 | the Kustomization's inventory linked to live objects, and the workload verdicts | a parser for the objects `HealthCheckFailed` names ("timeout waiting for: [Deployment/ns/name status: 'InProgress']"), like `DependencyNamed`, joined to the inventory by kind, namespace and name |
| The crashing pod's logs | A1 | workload to pods, chat's finite log read (the previous container included), S8b's Loki history by UID | none |
| The commit | A1 | Git history through the system git (approved URLs), the Flux diff from the cache | the diff between the two revisions, scoped to `spec.path`, with the change to the Deployment found in it |
| A remedy | A1, A2 | Suggest-fix (a Git change, as text) | a structured proposal with two parts: the Git revert (the fix) and a mitigation |
| The approval | A3 | Flux suspend and resume, rollout history and undo: each UID-armed, behind a ConfirmDialog, with the typed grant on protected clusters | an action card holding an ordered plan of existing verbs: **suspend the Kustomization, then roll the Deployment back to its previous revision.** Each step names its GitOps consequence ("Flux would reapply the failing revision unless the Kustomization is suspended"). Each step runs through its own guarded call; on a protected cluster each needs its grant, as bulk pod delete re-arms per call |
| Whether it worked | A3 | the Observer's verdicts | the verification watch: the Deployment healthy and its pods ready, within the bound. The card then reminds that the Kustomization stays suspended until the Git fix lands and it is resumed |

**The lab:**
- a Kustomization on kind-klyx with a health check on its Deployment;
- a Git repository the lab controls, holding a healthy commit, then one that changes the Deployment so it crash-loops (a bad config value, or an image that exits);
- Loki shipping the pod's logs.

**What it stress-tests:**
- **Certainty:**
  - "the health check timed out on Deployment X" is proven by the Kustomization's own message;
  - "the pod crash-loops" is proven by the pod verdict and its logs;
  - "the commit caused it" is proven only when the diff touches that Deployment, and is suspected otherwise.
- **The GitOps consequence:** a rollback without the suspension would be undone by Flux. The card must show it, and the plan orders suspend first.
- **The Git boundary:** the fix is a revert, proposed for the human to commit unless §7's decision 3 admits pull requests. The mitigation is all Klyx runs.
- **The verb boundary:** both steps are existing verbs; nothing new runs because a model proposed it.

**A variant:** the same on a HelmRelease that fails its upgrade. The mitigation is Helm's rollback, behind the HelmRelease's suspension. Klyx has both verbs.

### 6.2 The second scenario: a failed Argo sync, after Argo parity

> "Investigate this failed Argo deployment, trace the blocking hook to its pod logs, show the relevant recent changes, propose a remedy, let me approve the action, and check whether it worked."

What it needs beyond §6.1:
- **The hook results:** Klyx does not yet read `operationState.syncResult.resources` (each hook's kind, name, `hookType`, `hookPhase` and message). That is the Argo-parity work. A hook's Job and pods are then joined by owner UID.
- **A gone hook pod:** a hook-delete policy often removes it. The logs come from Loki by its UID, or are listed as missing evidence.
- **Verbs:** Argo's terminate-operation and history rollback are not Klyx verbs yet. They belong to the Argo-parity work as ordinary verbs, never added by A3. A retry is the existing sync, behind its guards.

## 7. Open owner decisions

1. ~~**Sequencing:** §5's option 1 or 2.~~ Decided 2026-10-21 (§5): A1 now, then A2 and A3. The decisions below gate A2 and A3, not A1.
2. **Unattended egress for A2:** allowed per cluster at opt-in, and whether a local model is the default for proactive runs.
3. **A3's reach:** day-2 verbs only, or Git pull requests too (§4, and §6's approval step).
4. **Report retention:** how many reports, and for how long.
