# Klyx AI A3: guarded execution

Opened 2026-10-24, on `feat/ai-a3`. The AI programme's third phase (`2026-10-20-klyx-ai-investigations-roadmap.md` §4). The roadmap orders A1, then A2 and A3. A3 comes first here: A2 waits on two of the owner's decisions (roadmap §7, decisions 2 and 4), while A3's own, decision 3, has a default in the roadmap (day-2 verbs only, no Git writes). This note assumes that default (§11).

An investigation (A1) can end with an action proposal: an ordered plan of at most four existing day-2 verbs, each on an object the investigation read by UID. Klyx validates the plan against a fixed verb matrix and renders it as an action card. A step runs only when the operator confirms it, through the same service method the manual verb uses, with the same guards. After each step Klyx reads the step's effect; after the last one, the incident's verdicts.

The programme contracts A-C1 to A-C5 apply as the roadmap states them. The model proposes; Klyx validates and renders; the human confirms; the verb runs unchanged (A-C5).

## 0. What exists, read from the code

**The verbs** (bound service methods in `internal/appbridge`, writes in `internal/fleet`):

| Verb | Method | Kinds | Identity fence |
|---|---|---|---|
| Restart | `WorkloadsService.RolloutRestart` | Deployment, StatefulSet, DaemonSet | `CheckWorkloadArmed`, then `metadata.uid` in the patch |
| Scale | `WorkloadsService.ScaleWorkload` | Deployment, StatefulSet | `CheckWorkloadArmed`, then the Scale's UID |
| Delete pod | `PodsService.DeletePod` | Pod | the server's UID precondition |
| Cordon / uncordon | `NodeOpsService.Cordon` | Node | `CheckNodeArmed`, then `metadata.uid` in the patch |
| Drain | `NodeOpsService.StartDrain` | Node | a GET only: kubectl takes a name |
| Rollout undo | `WorkloadsService.RolloutUndo` | Deployment, StatefulSet, DaemonSet | armed, the history's UIDs, a JSON-patch `test` of the UID |
| Pause / resume | `WorkloadsService.RolloutPause` | Deployment | armed, then `metadata.uid` |
| Flux reconcile, suspend / resume | `GitOpsService.Reconcile`, `SetSuspend` | Flux kinds | `checkArmed`, then `patchArmed` |
| Reconcile with source | `GitOpsService.ReconcileWithSource` | Flux units | the target only: the source is patched by name |
| Argo refresh, sync | `ArgoService.RefreshApp`, `SyncApp` | Application | armed |
| KEDA pause / resume | `KEDAService.SetPaused` | ScaledObject, ScaledJob | armed, then the operator-version gates |
| Helm rollback | `HelmService.HelmRollback` | a release | none: a release name and a revision |

No verb has a generation or resourceVersion precondition: every fence is the UID.

**The grant.** `ProtectGuard.Arm` checks that the typed text is the cluster's name and records a one-shot grant for 30 s, keyed by cluster only. `checkProtected` consumes it whether it passes or fails. `ConfirmDialog` arms, then calls `onConfirm` (or `onConfirmTyped`, which bulk delete uses to re-arm before each further call). Two verbs break the rule that every armed call consumes its grant: scale returns on an out-of-range count before `checkProtected`, and an Argo dry-run sync skips it.

**Outcomes.** `ActionResultDTO` is `{ok, error, admission, steps, conflict}`.
- The written / refused / unverified distinction exists only as error text (`armedWriteErr`), for the workload, pod and node verbs.
- `patchArmed`'s verbs (Flux, KEDA) never say "unverified" for a lost answer.
- Structured flags exist only in `StepDTO`, which only reconcile-with-source fills.
- Helm rollback and drain have no classification.

**The GitOps consequence.**
- The authoritative ownership check, `ClusterConn.GitOpsOwnership` (Kustomization `status.inventory`, Argo `status.resources`, warnings for incomplete reads), is used only by scale (through `ReplicaOwner`) and the YAML editor.
- Rollout undo's "Flux will revert this" line reads Flux labels only, and misses Argo.
- Restart, delete pod, cordon and pause show no consequence.

**Verification.** None. The bridges say "no re-read follows a verb"; KEDA and Flux Operator verbs nudge a refresh.

**Investigations** (A1, A1.1).
- `report_findings` carries a text remedy, `{git, mitigation}`, labelled "proposed, not run". It has no subject, UID or citations.
- Each remedy part is cut to 500 runes by `chatScrub`, although `parseFindings` and A1 §4 allow 2,000. `strictArgs` bounds the whole report at 4 KiB, so the documented maxima cannot fit. This is an A1 defect, fixed here (§2.4).
- `rollout_history` (A1.1) establishes a workload's desired-template revision and its nearest lower retained partner, each with its identity (A1.1 §8).

## 1. The verb matrix

A3 offers these verbs, and only with these kinds and parameters. Each has its own postcondition, the step's "effect observed" (§5), and a bound within which it must hold.

| Verb | Kinds | Parameters | Effect observed | Bound |
|---|---|---|---|---|
| `flux-suspend`, `flux-resume` | Kustomization, HelmRelease | none | `spec.suspend` recorded as requested. A suspension's postcondition is the suspension recorded, not proof that the controller is idle | 30 s |
| `flux-reconcile` | Kustomization, HelmRelease | none | `status.lastHandledReconcileAt` equals the `reconcile.fluxcd.io/requestedAt` value read after the write: Flux's own handshake | 2 min |
| `rollout-restart` | Deployment, StatefulSet, DaemonSet | none | the new pods replaced the old: `observedGeneration` at least the write's generation, and Deployment `updatedReplicas = replicas = status.replicas`; StatefulSet `currentRevision = updateRevision` and `updatedReplicas = replicas`; DaemonSet `updatedNumberScheduled = desiredNumberScheduled` | the Deployment's `progressDeadlineSeconds`, else 10 min, at most 10 min |
| `scale` | Deployment, StatefulSet | `replicas`, 0 to 10,000, not the current value | `spec.replicas` and `status.replicas` equal the requested count at an `observedGeneration` at least the write's | 5 min |
| `rollout-undo` | Deployment, StatefulSet, DaemonSet | `revision`: only the partner revision `rollout_history` established for this workload's UID in this investigation | the partner revision is current: for a Deployment, the current ReplicaSet (A1.1's template equality) is the partner's ReplicaSet by UID; for a StatefulSet or DaemonSet, `status.updateRevision` names the partner's ControllerRevision, by UID. Both at an `observedGeneration` at least the write's. The rollout completing belongs to the incident (§5) | 2 min |
| `rollout-pause`, `rollout-resume` | Deployment | none | `spec.paused` recorded at an `observedGeneration` at least the write's | 30 s |
| `delete-pod` | Pod | none | no pod with that UID at that name; and, for a pod with a controller owner, a pod of the same owner UID that was not among the owner's pods read just before the write (never a creation-time comparison: the server's clock is not this machine's). A pod without one is "not recreated", said before the confirmation | 2 min |
| `cordon`, `uncordon` | Node | none | `spec.unschedulable` recorded as requested | 30 s |
| `keda-pause`, `keda-resume` | ScaledObject, ScaledJob | none | the `autoscaling.keda.sh/paused` annotation recorded as requested, and KEDA's `Paused` condition agreeing | 1 min |

**Not in the matrix, and why:**
- **Drain:** kubectl takes a name, so a gap remains between Klyx's UID check and kubectl's own cordon. It evicts across tenants, and its outcome is a stream that is never classified.
- **Helm rollback:** no UID fence (a release name and a revision) and no outcome classification. It joins when it has a fence; the roadmap's HelmRelease variant of §6.1 needs it.
- **Reconcile with source:** the source is patched by name, not UID-guarded. Plain `flux-reconcile` is in.
- **Argo refresh and sync:** with the Argo-parity work, where the roadmap's §6.2 needs them, and where their postconditions would read `operationState`, which Klyx does not read yet.
- **Flux Operator verbs, and Flux kinds beyond Kustomization and HelmRelease:** no scenario needs them yet. They can join with their own postconditions.
- **Bulk pod delete:** not a verb. A plan may hold several `delete-pod` steps within its four.
- **Anything that is not a day-2 verb:** the YAML editor's apply, deploy yaml, debug pods. Never.

## 2. The proposal

### 2.1 What the model sends

- **Tools mode:** a tool, `propose_actions`, offered only in investigations and only when the setting allows it (§6). One valid call per turn, with one retry after an invalid one, as `report_findings`. It is not a read and does not count against the read budget.
- **Snapshot mode:** a fenced `actions` JSON block in the answer; the last one counts.
- **The input,** parsed strictly (unknown fields, nulls, duplicates and trailing data refused), within its own bound of 8 KiB:
  - `steps`, 1 to 4, each with:
    - `verb`, from §1;
    - `target`: `kind`, `namespace` and `name`;
    - `params`, only the verb's own (`replicas`, `revision`);
    - `cites`: 1 to 10 read or check IDs;
    - `why`: at most 300 characters, scrubbed.
- **No cluster and no UID in the input.** The cluster is the conversation's; the UID is Klyx's, from its own reads.

### 2.2 What Klyx decides, for each step

1. **The matrix:** the verb, the kind and the parameters are valid for each other (§1).
2. **The target's identity, from the ledger:**
   - the target must be an object this investigation read by UID: the anchor, an object a tool read by UID, or one reached by a UID join (the owner chain, a dependency edge read by UID);
   - its UID comes from that read, never from the model;
   - a name-only correlation (an inventory entry, a rendered Git object, a health-check entry) is not enough: "Klyx has not read Deployment web by UID in this investigation". The model can read it first.
3. **The home cluster only:** the target resolves through the conversation's pinned connection, and nothing else is resolvable.
4. **The parameters, against Klyx's reads:**
   - `rollout-undo`'s revision must be the partner revision `rollout_history` established for that UID in this investigation;
   - `scale`'s count must differ from the `spec.replicas` read;
   - a step that would change nothing is refused as "no change": suspending a suspended unit, resuming an active one, cordoning a cordoned node, pausing a paused Deployment;
   - a step that could have no effect is refused: `flux-reconcile` of a unit that is suspended, or that an earlier step suspends, since Flux skips the request and its handshake never completes.
5. **The citations** (A-C1): provenance and subject as A1 §4. At least one cited read or check must be about the target by UID, or about the anchor. Otherwise the step is shown as "unsupported" and is not offered.
6. **The plan as a whole:** the same verb on the same target twice is refused, as is a contradictory pair on one target (suspend, then resume).

A step refused at validation is shown with its reason and never offered. The steps after it are shown as "not offered: step N was refused", since each step needs the one before it (§4). The steps before it stay offered.

A proposal never changes a finding's label, and a finding never makes a step proven. The step's "why" is the model's, labelled as its reading.

### 2.3 One plan at a time

- A later valid proposal in the same conversation supersedes the unrun steps of the earlier one ("superseded by turn N's proposal"). Steps already run keep their records.
- A conversation that is fenced (a disconnect, a settings change, the anchor replaced) withdraws its unrun steps. A step being verified ends "unverified" with that reason.
- Nothing about a plan is persisted beyond the conversation, which lives in memory as chat does. The Activity log keeps each run step's outcome, as it does for the manual verbs (§4).

### 2.4 The remedy (A1's defect)

- `report_findings`' `remedy` keeps its two text parts, `git` and `mitigation`, each scrubbed and capped at 2,000 characters, as A1 §4 states. Today `chatScrub` cuts each at 500.
- `report_findings` gets its own argument bound, 24 KiB, which holds its documented maxima (20 findings of 500 characters, 10 missing notes of 300, two remedy parts of 2,000), instead of `strictArgs`' 4 KiB.
- The text remedy stays "proposed, not run". The action card is the structured mitigation beside it.

## 3. The action card

The card is rendered in the findings panel, under the proposed remedy.

**The header:** "Proposed by the model, checked by Klyx, run only by you", the cluster and its protection, and the number of steps.

**Each step, in Klyx's words from Klyx's reads,** never the model's prose:
- **The verb and the object,** with its UID; the chip opens the object.
- **The change:**
  - `spec.suspend: false → true`;
  - `replicas 3 → 1`;
  - `revision 5 → 4`, with the template difference as A1.1 reports it: paths, values hidden;
  - `pod web-7d9c-x2 deleted; ReplicaSet web-7d9c recreates it`, or "a pod without a controller: it is NOT recreated".
- **The GitOps consequence,** from the authoritative ownership check (`GitOpsOwnership`), never from labels:
  - "Flux's Kustomization a1-web owns this Deployment and reapplies it at its next reconcile (every 1m) unless suspended";
  - when an earlier step suspends that unit: "step 1 suspends it";
  - when the ownership read was incomplete, its warning;
  - for `scale`, also the replica owner the manual popover reads (`ReplicaOwner`: an HPA or a KEDA ScaledObject that would override the count).
- **The expected result and its bound:** "effect: spec.suspend true, within 30 s".
- **The model's why,** labelled as its reading, with its citations as chips.
- **Its state:** offered, waiting for step N, not offered (with the reason), running, then its outcomes (§5).

**Danger has a shape,** as the manual verb's: suspend, undo, restart, scale down, delete pod, cordon and KEDA pause carry the danger treatment of the design rules.

## 4. Execution

**Review.** "Review…" on an offered step re-reads its target by UID (a fresh GET) and recomputes the change and the consequence. A replaced target (another UID at the name), or a precondition that no longer holds (now suspended; the partner revision no longer retained), makes the step "not offered", with the reason. Nothing is sent.

**The ConfirmDialog,** always, including for a scale the manual popover would allow:
- the title "Run step N of the investigation's plan";
- the change, the consequence and the expected result;
- danger as the manual verb's;
- on a protected cluster, the typed confirmation, which arms one grant.

**Confirm calls `InvestigationService.RunStep(conversation, plan, step)`.** RunStep:
1. refuses unless the step is offered: its plan is current, every earlier step's effect was observed, the conversation is not fenced, no other step of the conversation is running;
2. calls the verb's own service method, the same Go method the manual bridge calls (for `flux-suspend`, `GitOpsService.SetSuspend(cluster, kind, namespace, name, uid, true)`), with the pinned UID and the validated parameters. The guards are unchanged: `checkProtected` consumes the grant, the UID arming, the admission attribution;
3. records the request's outcome (§5) and starts the verification;
4. never retries and never compensates.

**The grant is never left armed.** The dialog arms the grant before RunStep is called, so a refusal in step 1, before the verb runs, consumes it as `checkProtected` would. Every armed call consumes its grant (§0), RunStep's own refusals included.

RunStep is the backend's so that the gating and the records are the backend's: a stale or duplicated frontend state cannot run a step out of order. The verb's guards are the same either way. The dispatch table from matrix verb to service method is fixed in code, one entry per verb, and tested against the manual bridges' bindings.

**The Activity log** records every run step as the manual verbs' do, marked "from an investigation's plan".

**The plan stops** at a step that is refused, unverified (its request or its effect) or declined:
- Cancel in the dialog leaves the step offered;
- "Dismiss plan" ends it as declined;
- the card then says which step held the plan, and lists what was left not run.

## 5. Outcomes and verification

Three outcomes, kept apart and said in these words (roadmap §4):

**1. The request:**
- **written:** the verb returned `ok`;
- **refused:** a definite refusal (`definiteRefusal`, or `ErrReplaced` for the UID), with the admission finding when the server attributes it;
- **unverified:** anything else: the write may have landed.

`ActionResultDTO` gains `outcome` (`written`, `refused`, `unverified`), set by `failed` and by the success paths. The classification is then structured, and `patchArmed`'s verbs say "unverified" for a lost answer. The manual verbs' toasts can adopt it; A3 does not change their wording.

**2. The effect:** observed, or unverified with its reason (not observed within the bound; the object was replaced; the read failed).
- It is checked after a request written or unverified, never after one refused. A lost answer whose effect is observed reads "request unverified, effect observed".
- **The read:**
  - a fresh GET by name after the call returns, checked for the pinned UID. Its `metadata.generation` G is at least the write's, since a read after the write sees it;
  - then a watch of that one object from the GET's resourceVersion: watch-based, never polled;
  - each event is checked for the UID and the postcondition, at an `observedGeneration` of at least G where the postcondition names one, until it holds, the UID changes or the bound ends.
- A third party's write in the window, such as Flux reapplying, can make the postcondition fail: it then reads "not observed", never "observed".
- `delete-pod` also watches its namespace's pods matching the owner's selector, for the replacement.

**3. The incident:** resolved, or not, or unverified, read after the last step's effect is observed.
- **Its objects:** the anchor, and each step's target that Klyx's checks found failing in this investigation, by UID.
- **Resolved:** each, read fresh after the last write, has a non-fault verdict: healthy, or suspended for a unit the plan suspended. The card then says the unit stays suspended until the Git fix lands and it is resumed.
- **Not resolved:** a fresh read shows a fault at the bound, said with its verdict and time.
- **Unverified:** missing or stale evidence, never "resolved" and never "still failing".
- **Bound:** 10 minutes after the last write.

**What the model learns.** The next turn's input carries Klyx's record of each step: verb, target (kind, namespace, name), and the three outcomes in these words. No read the verification made is sent; the model reads what it needs through its tools, fenced as ever.

## 6. Egress, protection and the setting

- **The proposal is model output:** nothing new leaves Klyx to produce it.
- **The card is local:** the change and the consequence never reach the model.
- **The model's feedback** carries outcome words and names already in the ledger; literal-needle tests cover it (§8).
- **Protected clusters:** proposals are made and shown as anywhere; each step needs its own typed grant; `allowProtectedEgress` governs the investigation as before.
- **The setting:** Settings > AI gains "Propose actions in investigations", on by default. Off removes the tool, the snapshot instruction and the card; the text remedy stays.
- **Chat** stays read-only: `propose_actions` is never offered in an ordinary conversation.

## 7. The lab and acceptance

The roadmap's §6.1 scenario runs on the kind lab on E1's fixture: the Kustomization `a1-web` failing `HealthCheckFailed` on Deployment `web` (crash commit `a9b3c81`), with the steady revision retained as the partner.

- **The scripted model** proposes `flux-suspend a1-web`, then `rollout-undo web` to the partner revision. The lab check confirms both. It sees the effect observed for each, then the incident resolved: `web` at the partner's template, its pods ready, `a1-web` suspended. It then restores the lab: it resumes `a1-web`, Flux reapplies the crash commit, and E1's setup sees its plant again.
- **A counterexample on the lab:** `rollout-undo` without the suspension. Flux reapplies within its interval, and the card says what it read: the effect not observed, or the incident not resolved.
- **The owner's real-model run:** the §6.1 sentence as the first question, end to end, on a real model. This is the programme's acceptance (roadmap §6.1).
- **The native smoke:** the card renders from the scripted model; a step's Review opens the ConfirmDialog, and Cancel sends nothing.

## 8. Tests

**Validation:**
- every matrix rule: an unknown verb; a kind outside the verb's; unknown parameters; an out-of-range count; an undo to the current revision, to a revision `rollout_history` did not establish in this investigation, and to another workload's partner;
- identity: a target read only by name (an inventory entry, a Git render); a target absent from the ledger; a name that matches a ledger object now replaced. All refused, never offered;
- citations: a step citing only a real read of an unrelated object is unsupported and not offered (the A-C1 counterexample);
- injection (A-C4): a pod log asking to delete every pod in `kube-system` yields a proposal on pods never read by UID, which is refused; a proposal naming another cluster's object cannot resolve;
- five steps; duplicates; a contradictory pair; no-ops.

**Execution:**
- RunStep refuses a step not offered, a superseded plan, a fenced conversation and a concurrent run;
- on a protected cluster, a step without a grant is refused by the verb's own check, a second step cannot use the first one's grant, and a RunStep refusal after the dialog armed consumes the grant;
- the dispatch table maps each verb to exactly the method the manual bridge binds;
- no retry after "unverified"; no compensation after a refused second step.

**Outcomes and verification** (a fake clientset with watches, a fake clock):
- each verb's postcondition holding (observed); the UID replaced mid-watch (unverified); the bound running out (unverified); a third party's write reverting the field (not observed); a lost answer with the effect observed;
- the incident resolved on fresh verdicts; a stale verdict unverified; a suspended anchor counted as non-fault only when the plan suspended it;
- `outcome` on every verb A3 calls: written; refused (4xx, `ErrReplaced`, an attributed admission refusal); unverified (5xx, a deadline, a transport loss). Scale's out-of-range count consumes the grant.

**Egress:** literal needles (env values, the template difference's hidden values, annotation values, a Secret's name read only by the card) never appear in the model's feedback, the proposal's echo or any send.

**Frontend:** the card's states; danger shapes; Review's re-read; one arming per step; dismiss; supersession; withdrawal on a fence; the Activity log entry.

## 9. Stages

1. **Outcomes and the guards A3 relies on:** `ActionResultDTO.outcome` for the matrix verbs; scale's grant order; the manual rollout undo's consequence line from the authoritative ownership check, since it misses Argo today. Each is useful on its own.
2. **The proposal:** `propose_actions` and the `actions` block, the matrix, the validation and labels, and the remedy fix (§2.4).
3. **Execution:** `InvestigationService.RunStep`, the dispatch table, the gating and the records.
4. **Verification:** the postconditions, the single-object watch, and the incident.
5. **The frontend:** the card, Review and the ConfirmDialog, the Activity log, the setting.
6. **The lab, the smoke, captures, the owner's real-model run, and the as-built notes.**

## 10. Not in A3

- Writing to Git (decision 3, §11).
- Argo's verbs (with Argo parity), Helm rollback (it needs a UID fence), drain, reconcile with source, and the Flux Operator verbs (§1).
- Anything unattended. A2's reports may carry a proposal, which runs only from the card, under these rules.
- A causal-proof rule: no proposal makes a cause proven.
- Retries, compensation, an automatic resume.
- A new verb.

## 11. The recorded crossing and the owner's decisions

- **The crossing.** CLAUDE.md's editable-surface crossing says AI Suggest-fix proposes Git changes only and never applies them. A3 crosses that for the existing day-2 verbs only, behind the same human confirmation and guards as the manual verbs, under the 2026-06-17 directive. Git remains the source of truth for desired state.
- **Decision 3, A3's reach:** this note assumes day-2 verbs only, the roadmap's default. Pull requests would be a later addendum.
- **For the owner to confirm:** the matrix (§1: cordon and KEDA pause in, drain and Helm rollback out), and the setting on by default (§6).
