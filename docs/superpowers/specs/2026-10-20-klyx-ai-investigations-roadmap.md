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
  - Loki history (S8b), an operator reader today: no AI tool reads it, and its identity is the pod's UID only where the shipper records it (S8b §5);
  - a refused write's admission finding: the webhook or policy that refused it, and its candidates (S3b);
  - rollout history;
  - the Observer's verdicts and the ledger.
- **The verbs** (C4):
  - each is UID-armed (`checkArmed`/`patchArmed`), with the typed-confirmation grant (ProtectGuard, a guard against mistakes rather than a security boundary) on protected clusters and a GitOps-ownership consequence line;
  - each sits behind a ConfirmDialog, except the owner's decided popover-only scale of a proven-unowned workload on an unprotected cluster;
  - restart, scale, delete pod, cordon/drain, rollout undo and pause;
  - Flux reconcile and suspend/resume;
  - Argo refresh and sync;
  - KEDA pause and resume.
- **Health notifications** (`healthnotify`): off by default; episodes keyed by UID, opened by a fault only (not a degraded reading or an alert), with sampled batches dropped.

## 1. Programme contracts (every phase)

**A-C1. Evidence and certainty** (revision 1: a citation is provenance, not proof).
- **Every finding cites the reads it rests on,** by the read ledger's IDs.
- **What Klyx checks, and only that:**
  - the citation's provenance: a read of this investigation's ledger;
  - its subject's identity: the cited read is of the finding's object (by UID), or of an object Klyx links to it (owner, inventory, dependency edge), never a same-name stand-in;
  - its time and coverage: when it was read, and whether that read was live, stale or partial;
  - any deterministic assertion Klyx itself evaluates on the read (below).

  A valid citation is not verification of the model's prose. A finding whose citations are missing from the ledger, or name another object, is shown as "unsupported".
- **Observations and causes are kept apart.**
  - **An observation** states what a read shows: a verdict, a condition or event, an alert, a threshold crossing, what a diff changed. It is **checked** when Klyx evaluates it itself on the cited read (the Kustomization's own `HealthCheckFailed` naming the Deployment; the pod's CrashLoopBackOff verdict; the diff's changed fields). Otherwise it is the model's reading of the cited text, shown as such.
  - **A cause** ("this commit caused the crash") is an inference. It is **suspected** whatever its citations and whatever the model calls it: a diff proves what changed, a crash-loop proves the crash, and neither proves that one caused the other. Timing, correlation and absence never raise it.
  - A cause becomes **proven** only through a named causal-proof rule that Klyx evaluates deterministically on the cited reads, defined and tested per phase. For example: the failing container's image equals the image the diff introduced, and its failure is an image pull of that exact reference.
- **Missing evidence is listed:** reads that failed, kinds that were unreadable or stale, and budgets that ran out.
- **Each phase's tests include the counterexamples:** a diff that touches the object but not the failing field (an annotation-only change) beside an independent failure; a real ledger ID cited for an unrelated object; evidence read before the failure began.

**A-C2. Boundaries.**
- **One cluster per investigation,** as chat: the home cluster, pinned at the start.
- **Fleet difference is the one exception, and it is bounded by the operator** (revision 1):
  - the comparison set is chosen by the operator when the investigation starts (its clusters named in the UI, or a fleet compare the operator opened), and fixed outside any model or cluster text;
  - a tool call naming a cluster outside the set is refused before any read or send, whatever the model's reason;
  - each member's connection and egress policy are re-checked before every read, as the runner checks the home cluster's;
  - actions (A3) stay on the home cluster.
- **Every per-kind exclusion is unchanged** and applies to every new read: diffs, graphs, comparisons, snapshots and reports. That is the structural never-send list (Secret data, ConfigMap values, Helm values, env values, `command` and `args`), the annotation allowlist, S0b's embedded payloads and S10's and DRA's special projections. A new tool builds its own projection through those rules; reusing an inspector DTO or raw Git or Loki output would bypass them. `ScrubEvidence`'s credential and identifier scrub is heuristic for free text, not proof of secret absence.
- **The structural never-send list is unchanged** (Secret data, ConfigMap values, Helm values, env values).
- **Identifier redaction is applied before clipping, across every AI path** (from S10's design review): cloud resource IDs and ARNs that providers write into condition text and that controllers copy onto other objects. It is tested with literal needles on the serialized Explain, chat and report documents.

**A-C3. Budgets.**
- Each investigation has bounded turns, tool calls, bytes and wall time.
- When a budget runs out, the investigation stops, says what it did not read, and never pretends to completeness.

**A-C4. Injection** (revision 1, worded as it is).
- Cluster text is data. It travels as tool results, or in the snapshot's delimited data block for a provider without tools (the Claude Code CLI), which stays supported.
- An injected instruction can cause more reads within the home cluster and the operator's comparison set, which are recorded, and never beyond them.
- It can also corrupt what the model writes: a finding, a likely cause or a proposed remedy. Klyx's checks (A-C1) and the labels "suspected" and "unsupported" limit that, and do not remove it.
- It can never cause a write: A3's actions require a human confirmation that the model cannot give. Confirmation prevents unattended execution; it does not make a misleading proposal correct.

**A-C5. Execution.**
- **The model never executes.** It proposes; Klyx validates the proposal, renders it, and the human confirms.
- **Every write goes through the existing verb path** and its guards, unchanged.

## 2. A1: Deeper investigation

**New read tools,** extending chat's catalogue and validated like it:
- **fleet difference:** fleet compare across the operator's comparison set only (A-C2): workload spec, image, Helm chart, Flux revision;
- **Git changes:** the GitOps unit's commits and diffs since its last healthy revision, read-only, from the approved Git URLs;
- **metrics:** Klyx's own PromQL templates for the object (rate, saturation, restarts, 5xx). The model chooses a template and a window and never writes a query;
- **alerts:** S8a's alerts for the object, its namespace and its dependencies;
- **dependencies:** the existing drawings as structured edges (dependsOn, source, issuer chain, store, route path, scaler and HPA), with each node's verdict.

- **log history:** Loki's history for a pod, through a new AI-safe adapter: bounded, scrubbed before clipping, its identity strength stated (the pod's UID where the shipper records it, otherwise namespace and name). The operator's reader is not reused: it redacts only the endpoint's credential (S8b §5).

**Output:** structured findings, each an observation (checked or read) or a cause (suspected, or proven by a named rule), with its citations and the missing evidence (A-C1). The investigation view renders them, and chat answers can cite them.

**Templates only.** The model chooses a template, a window and an object, each validated, and never writes PromQL or LogQL. Arbitrary queries would be a future scope decision, not something a phase enables quietly. The phase note bounds the whole read and send.

## 3. A2: Proactive investigations

**Triggers, opt-in per cluster and per trigger class, all off by default:**
- a fault episode opening (healthnotify's opt-in and episode identity);
- a degraded reading, and a critical alert firing (S8a): A2 defines their episodes itself, since healthnotify tracks faults only and drops sampled batches, with its baseline, after-gap and source-identity cases;
- a GitOps revision changing on a unit that then fails.

**Unattended egress is consented at opt-in, and the consent has a lifetime** (revision 1):
- the settings name what is sent and to which destination: the provider, its endpoint and the egress policy;
- the consent covers that destination and policy only. A later change that widens either (another provider, another endpoint, a local-only cluster sent to a remote model, egress opened on a protected cluster) either asks for consent again or stops the affected proactive work; it never carries over silently;
- revoking it stops queued and future investigations before their next read or send, and the running one at its next fence, with the existing settings and connection fences kept;
- a protected cluster needs its own consent;
- a local model (`openaiCompatible`) can be required per cluster;
- one investigation runs per episode (deduplicated), with rate and cost limits per hour and per day, and a fleet-wide bound on concurrent and queued investigations.

**The incident report is bounded:**
- **what changed:** Git diffs, the revision, rollout history and events;
- **what is affected:** verdicts and dependents;
- **likely causes:** proven and suspected, each cited;
- **missing evidence;**
- **a proposed remedy:** an A3 action proposal, or a Git change as Suggest-fix gives it today.

**Persistence and notification:**
- **Reports persist locally:** the first durable AI record, an intentional new scope. The phase states what is kept (the sanitized report and an egress audit of what was sent, never raw reads: `docs/backlog.md` §2's "durable AI egress audit history"), stores it privately, offers deletion, and bounds it by bytes, count and age. Ordinary chat keeps no history on disk.
- **OS notifications** only through the existing healthnotify opt-in; the notification links to the report.

## 4. A3: Guarded execution

**The action card** is rendered from a structured proposal that Klyx validates against the verb matrix: only the existing day-2 verbs (§0), with their parameters. It shows:
- the cluster and its protection;
- the exact objects, pinned by UID;
- the change;
- the GitOps consequence, from the same ownership check the manual verb uses;
- the expected result: the verdict to reach, and a bound on when.

**Execution is exactly the manual verb's path:**
- UID preconditions and the typed grant on protected clusters;
- every AI-proposed action needs the ConfirmDialog, including a scale the manual popover would allow;
- the human confirms every action, and nothing runs from a report unattended.

**Verification** (revision 1: the action's effect, not a target's verdict):
- Each verb has its own postcondition, fenced by identity and freshness: read after the write, of the object written (by UID), at a generation or observation newer than the write. For example: a restart's new pods replacing the old; a scale's new replica count observed; a rollback's revision current; a deleted pod gone and its replacement (by owner) present; a suspension observed in the spec. A suspension's postcondition is the suspension recorded, not proof that its controller is idle.
- Three outcomes are kept apart and said in these words:
  - **request accepted:** the server took the write (written, refused or unverified, as S3b reports it);
  - **effect observed:** the postcondition held in a fresh read;
  - **incident resolved:** the investigation's failing object reached its expected verdict, within the bound.

  Missing or stale evidence reads **unverified**, never resolved and never "still failing".
- **A plan's steps run in order, each on its own confirmation.** Each step records written, refused, unverified or not run. A step whose prerequisite is not effect-observed is not offered: a refused, unverified or declined suspension never counts as the prerequisite for a rollback. The plan then stops, saying which step held it and what was left not run.
- It never retries, and never compensates (no automatic resume).

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
| The crashing pod's logs | A1 | workload to pods, chat's finite log read through the kube API (the previous container included); S8b's Loki history for the operator only | the AI-safe Loki adapter (§2) for a pod that is gone or past its previous container, its identity strength stated |
| The commit | A1 | Git history through the system git (approved URLs), the Flux diff from the cache | the diff between the two revisions, scoped to `spec.path`, with the change to the Deployment found in it: a checked observation of what changed, and a suspected cause (A-C1) |
| A remedy | A1, A2 | Suggest-fix (a Git change, as text) | a structured proposal with two parts: the Git revert (the fix) and a mitigation |
| The approval | A3 | Flux suspend and resume, rollout history and undo: each UID-armed, behind a ConfirmDialog, with the typed grant on protected clusters | an action card holding an ordered plan of existing verbs: **suspend the Kustomization, then roll the Deployment back to its previous revision.** Each step names its GitOps consequence ("Flux would reapply the failing revision unless the Kustomization is suspended"). Each step runs through its own guarded call and confirmation; on a protected cluster each needs its grant, as bulk pod delete re-arms per call. The rollback is offered only once the suspension is effect-observed (§4) |
| Whether it worked | A3 | the Observer's verdicts | each step's outcome (request accepted, effect observed), then the incident: the Deployment at its previous revision, healthy, its pods ready, in fresh reads within the bound, or unverified. The card then reminds that the Kustomization stays suspended until the Git fix lands and it is resumed |

**The lab:**
- a Kustomization on kind-klyx with a health check on its Deployment;
- a Git repository the lab controls, holding a healthy commit, then one that changes the Deployment so it crash-loops (a bad config value, or an image that exits);
- Loki shipping the pod's logs.

**What it stress-tests:**
- **Certainty** (revision 1):
  - "the health check timed out on Deployment X" is a checked observation: Klyx parses the Kustomization's own message;
  - "the pod crash-loops" is a checked observation: the pod's verdict, with its logs as the read evidence;
  - "the commit changed Deployment X's container" is a checked observation of the diff;
  - "the commit caused the crash" is a suspected cause, even when the diff touches that Deployment, unless a named causal-proof rule holds (A-C1). The tests include an annotation-only change beside an independent failure.
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

## 8. Revision 1: Codex's review of 671c6b0

Codex (handoff 5d5bd443) requested changes: four P2 findings on the programme's contracts, and corrections to the baseline. A1-first sequencing stands, and §7's decisions 2-4 stay the owner's.

| # | Finding | Where answered |
|---|---|---|
| 1 | A valid ledger ID and a "direct" evidence class do not prove a conclusion, and a diff touching a Deployment does not prove it caused the crash | A-C1: Klyx checks provenance, subject identity, time and coverage, and its own deterministic assertions; observations are kept apart from causes; a cause is suspected unless a named causal-proof rule holds; the counterexamples are required tests. §6.1's certainty and its table say the commit is a suspected cause |
| 2 | Named clusters passing egress do not keep injection within one cluster: a hostile annotation could steer a comparison to another cluster | A-C2: an operator-chosen comparison set, fixed outside model and cluster text, refused beyond it, each member re-checked; actions stay on the home cluster. A-C4 says what injection can still do (corrupt findings and proposals) and keeps the snapshot data block for providers without tools |
| 3 | Unattended consent had no lifetime: a later provider or policy change could carry it over silently | A2: consent covers its disclosed destination and policy; widening either asks again or stops the work; revocation stops queued and future sends |
| 4 | Verification by the target's verdict cannot tell a restart's effect from an old healthy reading, has no target after a delete, and a suspension's patch does not prove the controller idle | A3: per-verb postconditions fenced by identity and freshness; "request accepted", "effect observed" and "incident resolved" kept apart, missing evidence unverified; a plan's dependent step offered only after its prerequisite's effect is observed |

**Baseline corrections, taken:**
- Loki is an operator reader with no AI tool (A1 adds an AI-safe adapter, with its identity strength);
- healthnotify opens fault episodes only, and drops sampled batches (A2 defines its other triggers);
- the manual scale's popover-only path exists, and every AI-proposed action still needs the ConfirmDialog;
- arbitrary PromQL or LogQL is a future scope decision;
- every per-kind exclusion applies to the new reads;
- A2's local reports are an intentional new scope, with stated content, private storage, deletion and bounds;
- ProtectGuard is a guard against mistakes.
