# Klyx AI A3: guarded execution

Opened 2026-10-24, on `feat/ai-a3`; revisions 1 and 2 the same day (§12, §13); the design is accepted (§14); stage 1 is built and accepted (§15); stage 2 is built (§16); stage 3 is built (§17). The AI programme's third phase (`2026-10-20-klyx-ai-investigations-roadmap.md` §4). The roadmap orders A1, then A2 and A3. A3 comes first here: A2 waits on two of the owner's decisions (roadmap §7, decisions 2 and 4), while A3's own, decision 3, has a default in the roadmap (day-2 verbs only, no Git writes). This note assumes that default (§11).

An investigation (A1) can end with an action proposal: an ordered plan of at most four existing day-2 verbs, each on an object the investigation read by UID. Klyx validates the plan against a fixed verb matrix and renders it as an action card. A step runs only when the operator confirms it, through the manual verb's own code and guards. After each step Klyx establishes the step's effect from this request's own witness; after the last one, the incident's verdicts.

The programme contracts A-C1 to A-C5 apply as the roadmap states them. The model proposes; Klyx checks that a step is eligible to run (never that it is the right remedy) and renders it; the human confirms; the verb runs through its unchanged guards (A-C5).

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

- **No verb has a generation or resourceVersion precondition:** every fence is the UID.
- **No verb returns a receipt:** the PATCH responses are discarded (`podactions.go:72`, `gitopsactions.go:145`). Restart's `restartedAt` token has second precision (`podactions.go:48`); reconcile's `requestedAt` token is RFC3339Nano (`flux/actions.go:18`).
- **Rollout undo re-selects by number:** it reads the history, then lists ReplicaSets (or ControllerRevisions) again and selects by revision number (`rollout.go:150`, `:193`, `:281`). It never receives the partner's identity or template. The manual history DTO (`RolloutRevisionDTO`) carries no revision object UID.
- **Each service method looks its connection up by cluster name** and builds a `context.Background()` with `actionTimeout` (`workloads_service.go:76`). Nothing binds a write to a particular connection.
- **Pre-write refusals are plain errors:** KEDA's version and annotation gates (`keda.go:698`), Flux's "not offered" checks, a failed arming. Nothing tells them from a write whose answer was lost.

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

**Verification.** None. The bridges say "no re-read follows a verb"; KEDA and Flux Operator verbs nudge a refresh. S9 defines a completed KEDA pause (`keda.PauseCompleted`, `JobPauseCompleted`: the version-specific condition, Ready not False, and for a ScaledObject no HPA it controls in a current, complete read).

**Verdicts** (`internal/health`): fault, degraded, unknown, progressing, paused, ok. Fault and degraded are failures in force (`Attention`); a suspended Flux unit and a workload scaled to zero read paused.

**Investigations** (A1, A1.1).
- `report_findings` carries a text remedy, `{git, mitigation}`, labelled "proposed, not run". It has no subject, UID or citations.
- Each remedy part is cut to 500 runes by `chatScrub`, although `parseFindings` and A1 §4 allow 2,000. `strictArgs` bounds the whole report at 4 KiB. This is an A1 defect, fixed here (§2.5).
- `rollout_history` (A1.1) establishes a workload's desired-template revision and its nearest lower retained partner, each with its identity (A1.1 §8).
- The ledger (`ChatRead`) keeps names and checks, not result content, and `rollout_history`'s retained reference is the workload only (`chat_rollout.go:424`). Nothing retains the facts a proposal would need (§2.2).

## 1. The verb matrix

A3 offers these verbs, and only with these kinds and parameters. Each has its own effect, established as §5 says, and a bound within which it must hold.

**Two kinds of effect:**
- **A state effect** is a requested state on the UID, whoever wrote it: the suspension, the count, the pause, the cordon, the reverted template. A later read showing that state establishes it, said as "the requested state, by this request or another writer" when this request's own answer was lost.
- **An event effect** is a restart, a reconcile or a deletion. A restart or a reconcile is established only by this request's own token, never by another request's token or a post-call generation. A deletion is established by the intended UID's removal, observed; when the request's answer was lost, who removed it stays unverified ("the pod was removed; by this request or another actor").

| Verb | Kinds | Parameters | Effect | Bound |
|---|---|---|---|---|
| `flux-suspend`, `flux-resume` | Kustomization, HelmRelease | none | state: `spec.suspend` as requested. A suspension's effect is the suspension recorded, not proof that the controller is idle | 30 s |
| `flux-reconcile` | Kustomization, HelmRelease | none | event: `status.lastHandledReconcileAt` equals the token this request wrote to `reconcile.fluxcd.io/requestedAt` (RFC3339Nano, unique). A later token handled instead reads "superseded by another reconcile request", unverified | 2 min |
| `rollout-restart` | Deployment, StatefulSet, DaemonSet | none | event: the template carries this request's `kubectl.kubernetes.io/restartedAt` token (made unique, §9 stage 1) at a generation G above the armed read's, and then the controller replaced the old pods at an `observedGeneration` of at least G: Deployment `updatedReplicas = spec.replicas = status.replicas`; StatefulSet `updatedReplicas = spec.replicas` and `currentRevision = updateRevision`; DaemonSet `updatedNumberScheduled = desiredNumberScheduled` | the Deployment's `progressDeadlineSeconds`, else 10 min, at most 10 min |
| `scale` | Deployment, StatefulSet | `replicas`, 0 to 10,000, not the current value | state: `spec.replicas` equals the count at generation G, then `status.replicas` equals it at an `observedGeneration` of at least G | 5 min |
| `rollout-undo` | Deployment, StatefulSet, DaemonSet | `revision`: only the partner revision `rollout_history` established for this workload's UID | state: the workload's canonical template (A1.1's normalisation) has the pinned partner's template digest at generation G, observed by the controller (`observedGeneration` at least G). For a Deployment, also: its current ReplicaSet is the partner's, by UID. The template being selected is the effect; the rollout completing belongs to the incident (§5.3) | 2 min |
| `rollout-pause`, `rollout-resume` | Deployment | none | state: `spec.paused` as requested at generation G, observed at an `observedGeneration` of at least G | 30 s |
| `delete-pod` | Pod | none | event: no pod with that UID at that name. For a pod whose controller owner maintains a count (a ReplicaSet, StatefulSet or DaemonSet), also its replacement, by the watch witness of §5.2. A Job's pod: "recreation is not established". A pod without a controller owner: "NOT recreated", said before the confirmation | 2 min |
| `cordon`, `uncordon` | Node | none | state: `spec.unschedulable` as requested | 30 s |
| `keda-pause`, `keda-resume` | ScaledObject, ScaledJob | none | state: S9's own completed pause (`PauseCompleted`, `JobPauseCompleted`, version-specific), or for resume the pause annotation gone and Ready not False. A Paused condition alone is never presented as scaling stopped | 1 min |

**Refused at validation, as a step that could not take its effect:**
- `rollout-restart` of a paused Deployment, a StatefulSet or DaemonSet with the `OnDelete` update strategy, a StatefulSet with a partition above zero, or a workload with no desired pods: no automatic replacement would follow, or the counters would hold vacuously. A template write is never presented as proof that old pods were replaced.
- `rollout-undo` of a paused Deployment: the reverted template would not roll out.
- `flux-reconcile` of a unit that is suspended, or that an earlier step suspends: Flux skips the request and its handshake never completes.

**Not in the matrix, and why:**
- **Drain:** kubectl takes a name, so a gap remains between Klyx's UID check and kubectl's own cordon. It evicts across tenants, and its outcome is a stream that is never classified.
- **Helm rollback:** no UID fence (a release name and a revision) and no outcome classification. It joins when it has a fence; the roadmap's HelmRelease variant of §6.1 needs it.
- **Reconcile with source:** the source is patched by name, not UID-guarded. Plain `flux-reconcile` is in.
- **Argo refresh and sync:** with the Argo-parity work, where the roadmap's §6.2 needs them, and where their effects would read `operationState`, which Klyx does not read yet.
- **Flux Operator verbs, and Flux kinds beyond Kustomization and HelmRelease:** no scenario needs them yet. They can join with their own effects.
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

### 2.2 The action facts: what Klyx retains to decide

The ledger keeps names and checks, not results (§0). A3 adds a bounded, per-conversation record of **action facts**, extracted by Klyx from its own reads as each tool result is built, never from model text, and never sent:
- **For every object read by UID:** kind, namespace, name, UID, the read's ID and time, its generation, and the fields a verb needs: a workload's `spec.replicas`, `paused`, update strategy, partition and desired count; a Flux unit's `suspend` and interval; a pod's controller owner (kind, name, UID); a node's `unschedulable`; a KEDA object's pause intent and its operator-version facts.
- **From `rollout_history`:** the desired and partner revision numbers, each revision object's UID, and each one's canonical template digest (SHA-256 over A1.1's normalised template).
- **Bounds:** at most 64 subjects per conversation, at most 1 KiB each (64 KiB). A newer read of a UID replaces its older record; past 64, the oldest is dropped. The record lives with the conversation, in memory, and counts toward its retention bounds.

A step is eligible only on a subject with a record. A proposal on a subject whose record was dropped is refused: "Klyx no longer holds what it read about Deployment web; read it again".

### 2.3 What Klyx decides, for each step

1. **The matrix:** the verb, the kind and the parameters are valid for each other (§1), and the step is not one §1 refuses.
2. **The target's identity, from the action facts:**
   - the target must be an object this investigation read by UID: the anchor, an object a tool read by UID, or one reached by a UID join (the owner chain, a dependency edge read by UID);
   - its UID comes from that record, never from the model;
   - a name-only correlation (an inventory entry, a rendered Git object, a health-check entry) is not enough: "Klyx has not read Deployment web by UID in this investigation". The model can read it first.
3. **The home cluster only:** the target resolves through the conversation's pinned connection, and nothing else is resolvable.
4. **The parameters, against the action facts:**
   - `rollout-undo`'s revision must be the partner `rollout_history` established for that UID; its revision object's UID and template digest are pinned from the record;
   - `scale`'s count must differ from the recorded `spec.replicas`;
   - a step that would change nothing is refused as "no change": suspending a suspended unit, resuming an active one, cordoning a cordoned node, pausing a paused Deployment.
5. **The citations** (A-C1): provenance and subject as A1 §4. At least one cited read or check must be about the target by UID, or about the anchor. Otherwise the step is shown as "unsupported" and is not offered.
6. **The plan as a whole:** the same verb on the same target twice is refused, as is a contradictory pair on one target (suspend, then resume).

A step refused at validation is shown with its reason and never offered. The steps after it are shown as "not offered: step N was refused", since each step needs the ones before it (§4). The steps before it stay offered.

A proposal never changes a finding's label, and a finding never makes a step proven. The step's "why" is the model's, labelled as its reading.

### 2.4 One plan at a time

- A later valid proposal in the same conversation supersedes the unrun steps of the earlier one ("superseded by turn N's proposal"). Steps already run keep their records.
- A conversation that is fenced (a disconnect, a settings change, the anchor replaced) withdraws its unrun steps. A step being verified ends "unverified" with that reason.
- **Bounds:** a step's record is at most 1 KiB; a conversation keeps at most 16 step records, and past that the oldest superseded plan's are dropped from the card (the Activity log keeps them, §4.4).
- Nothing about a plan is persisted beyond the conversation, which lives in memory as chat does.

### 2.5 The remedy (A1's defect)

- `report_findings`' `remedy` keeps its two text parts, `git` and `mitigation`, each scrubbed and capped at 2,000 characters, as A1 §4 states. Today `chatScrub` cuts each at 500.
- `report_findings` gets its own aggregate bound, 24 KiB, instead of `strictArgs`' 4 KiB. The field caps count characters, so the aggregate is an additional limit that a report at every field's cap can exceed, in ASCII too (with long subjects and citations). Such a report is refused with the bound named, and may be retried once as today; no claim is made that every combination of field maxima fits.
- The text remedy stays "proposed, not run". The action card is the structured mitigation beside it.

## 3. The action card

The card is rendered in the findings panel, under the proposed remedy.

**The header:** "Proposed by the model. Klyx checked that each step is eligible to run, not that it is the right remedy. A step runs only when you confirm it." Then the cluster and its protection, and the number of steps.

**Each step, in Klyx's words from Klyx's reads,** never the model's prose:
- **The verb and the object,** with its UID; the chip opens the object.
- **The change:**
  - `spec.suspend: false → true`;
  - `replicas 3 → 1`;
  - `revision 5 → 4`, with the template difference as A1.1 reports it: paths, values hidden;
  - `pod web-7d9c-x2 deleted; ReplicaSet web-7d9c keeps 3 pods, so it creates a replacement`; for a Job's pod "recreation is not established"; for a pod without a controller owner "it is NOT recreated".
- **The GitOps consequence,** from the authoritative ownership check (`GitOpsOwnership`), never from labels, and qualified by what that read shows. Ownership is evidence of management, not a promise that this field is reverted at the next tick:
  - "Kustomization a1-web manages this Deployment (its inventory, complete, read 15:01). While it is not suspended, Flux applies its desired state at each reconcile (every 1m), which may revert this change";
  - when an earlier step suspends that unit: "step 1 suspends it";
  - for an Argo Application: whether it auto-syncs, and that fields it ignores are not reverted;
  - a suspended owner: "suspended: Flux applies nothing until it is resumed";
  - when the ownership read was incomplete, its warning;
  - for `scale`, also the replica owner the manual popover reads (`ReplicaOwner`: an HPA or a KEDA ScaledObject that would override the count).
- **The expected result and its bound:** "effect: spec.suspend true, within 30 s".
- **The model's why,** labelled as its reading, with its citations as chips.
- **Each fact with the time it was observed.**
- **Its state:** offered, waiting for step N, not offered (with the reason), running, then its outcomes (§5).

**Danger has a shape,** as the manual verb's: suspend, undo, restart, scale down, delete pod, cordon and KEDA pause carry the danger treatment of the design rules.

## 4. Execution

### 4.1 Review

`InvestigationService.ReviewStep(conversation, plan, step)`, called by "Review…" on an offered step:
- re-reads the target by UID (a fresh GET), and each **standing prerequisite** (§4.3);
- computes the change, the consequence and the expected result from those reads;
- returns them with a **review token**: a digest of the facts they were computed from (the target's UID, generation, resourceVersion and the fields its change and consequence use; each prerequisite's UID, generation and predicate), valid for 2 minutes.

A replaced target (another UID at the name), a precondition that no longer holds (now suspended; the partner revision gone or changed), or a prerequisite that no longer holds makes the step "not offered", with the reason. Nothing is sent.

### 4.2 Confirm and run

**The ConfirmDialog,** always, including for a scale the manual popover would allow:
- the title "Run step N of the investigation's plan";
- the change, the consequence and the expected result, as reviewed;
- danger as the manual verb's;
- on a protected cluster, the typed confirmation, which arms one grant.

**Confirm calls `InvestigationService.RunStep(conversation, plan, step, token)`.** RunStep:
1. refuses unless the step is offered: its plan is current, every earlier step's effect was established, the conversation is not fenced, no other step of the conversation is running, and the token is valid;
2. re-reads what the token covers and refuses if any fact differs: "changed since you reviewed it; review again". A standing prerequisite that no longer holds refuses too (§4.3);
3. runs the verb (§4.4) with the pinned UID and the validated parameters, and, for `rollout-undo`, the pinned partner (§4.5);
4. records the request's outcome (§5.1) and starts the verification;
5. never retries and never compensates.

**The grant is never left armed.** The dialog arms the grant before RunStep is called, so a refusal in steps 1 or 2, before the verb runs, consumes it as `checkProtected` would. Every armed call consumes its grant (§0), RunStep's own refusals included.

**What remains racy, said on the card:** between RunStep's re-read and the write, and across objects. Klyx promises no atomicity across a plan's objects; each fact is shown with the time it was observed.

RunStep is the backend's so that the gating and the records are the backend's: a stale or duplicated frontend state cannot run a step out of order.

### 4.3 Standing prerequisites

- **What they are:** the state effects of a plan's earlier steps (§1): a suspension, a count, a pause, a cordon, a KEDA pause, a reverted template. Every later step depends on them; a step's consequence ("step 1 suspends it") is computed from them.
- **When they are re-read:** at Review, and again in RunStep before the write. Each is read by its UID and its predicate checked.
- **A changed, missing or unreadable prerequisite** stops the dependent step, and the plan holds with the reason: "step 1's suspension no longer holds: a1-web was resumed (spec.suspend false, read 15:02)".
- **Event effects** (a restart, a reconcile, a deletion) happened; they are history, and are not re-checked.

### 4.4 The verb's own code, bound to the conversation's connection

- **The same code:** each manual service method is split into its shell and an inner function. The shell is what the manual bridge calls today: `checkProtected`, a lookup by cluster name, a `Background` context with `actionTimeout`, then the inner function. The inner function takes the connection and the context, and holds everything else: the arming, the write, the admission attribution, the receipt.
- **RunStep** calls the same `checkProtected`, then the same inner function with:
  - the conversation's **pinned connection**, never a fresh lookup by name;
  - a context carrying the run fence (`readfence.With`): the pinned connection's identity, checked before every request the verb and its verification make, discovery included. A disconnect or a reconnect between dispatch and any request fails that request; it is never retargeted to a new connection.
- **Not fenced on AI settings:** they govern egress, and this write is the operator's, confirmed. A settings change still fences the conversation (§2.4), so a running verification ends unverified.
- **The dispatch table** from matrix verb to inner function is fixed in code, one entry per verb, and tested: each entry makes the same request as the manual method against a recording fake.
- **The Activity log** records every run step as the manual verbs' do, marked "from an investigation's plan".

### 4.5 Rollout undo writes only the reviewed partner

The shared undo path (`ClusterConn.RolloutUndo`, the manual verb's too) gains an expectation: the revision object's UID and its canonical template digest.
- After selecting the revision object by number, it refuses before writing if either differs: "revision 4 is no longer the one reviewed". It never re-resolves the number to another object.
- The workload's UID guard stays.
- A3 always passes the expectation, pinned from the action facts. The manual undo passes the one its history now carries: `RolloutRevisionDTO` gains the revision object's UID and template digest, so the manual verb also writes only the revision it showed.
- **What remains:** between the template check and the patch. A ReplicaSet's or ControllerRevision's template is not changed by its controller; a direct edit in that window is not caught.

### 4.6 The plan stops

At a step that is refused, unverified (its request or its effect) or declined:
- Cancel in the dialog leaves the step offered;
- "Dismiss plan" ends it as declined;
- the card then says which step held the plan, and lists what was left not run.

## 5. Outcomes and verification

Three outcomes, kept apart and said in these words (roadmap §4).

### 5.1 The request

- **written:** the verb returned `ok`, with its **receipt**: the written object's UID, generation and resourceVersion from the server's response (for a deletion, the deleted object's UID when the response carries it), and the token this request wrote (restart, reconcile);
- **refused, nothing sent:** Klyx refused before any write was sent (the arming, a version gate, "not offered", no change). Each such return in the inner functions is marked with a typed error (`ErrNotSent`);
- **refused by the server:** a definite refusal (`definiteRefusal`, or `ErrReplaced` for the UID), with the admission finding when the server attributes it;
- **unverified:** a write was sent and its answer lost: it may have landed, or landed later.

`ActionResultDTO` gains `outcome` (`written`, `refused`, `unverified`), `notSent` for Klyx's own refusals, and `receipt`. The manual verbs' toasts can adopt them; A3 does not change their wording.

### 5.2 The effect

Established, or unverified with its reason (not established within the bound; the object was replaced; the read failed; superseded). It is checked after a request written or unverified, never after one refused.

**The witness, by kind of effect** (§1):
- **written, with a receipt:** G is the receipt's generation. The effect is checked only on reads of that UID at a generation of at least G.
- **unverified, an event effect:** established only by a read that shows this request's own witness: its token in the template or `requestedAt` (a late commit), or its pod's UID gone. Otherwise it stays unverified for the bound: Klyx never substitutes a post-call generation or another request's token. A restart whose receipt shows no generation above the armed read's wrote no new template, and is unverified.
- **unverified, a state effect:** established by a read after the call that shows the requested state, said as "the requested state, by this request or another writer". G is that read's generation.

**The reads:**
- a fresh GET by name after the call returns, checked for the pinned UID;
- then a watch of that one object from the GET's resourceVersion: watch-based, never polled;
- each event checked for the UID and the effect, until it holds, the UID changes or the bound ends.

A third party's write in the window, such as Flux reapplying, can make the effect fail: it then reads "not established", never "established".

**`delete-pod`'s replacement: a creation witness, without a membership baseline** (revision 2):
- before the delete, an **unfiltered** LIST of the namespace's pods with `limit=1` gives a collection resourceVersion R;
- after the delete, an **unfiltered** watch of the namespace's pods from R, with no label or field selector and without initial events. The API server reports ADDED for an existing object that starts matching a filtered watch (`cache_watcher.go`); with no filter there is nothing to start matching, so an ADDED event is a creation after R;
- each event's controller owner is checked locally: the first ADDED pod whose controller owner is the owner's UID, and whose UID is not the deleted one, is the replacement. A pod adopted by the owner after R arrives as MODIFIED, and is never counted;
- the same watch tracks that pod: the effect is established only while it has not been deleted, so a replacement removed again before the deletion's own effect completes is not presented as present;
- the claim is modest: "a new pod of that owner was created after the step began", never that this deletion caused it.

No set of existing pods is kept, so neither a large owner nor a label change can make an old pod look new. A `410 Gone` or a failed watch is never followed by a relist that would treat existing pods as new: the replacement stays unverified. The watch reads at most 10,000 events within the bound; past that, the replacement is unverified ("the namespace was too busy to verify").

**Bounds:** one running step per conversation, so at most eight verifications fleet-wide; each holds at most two watches (the object and the replacement); events are decoded one at a time and kept only as their predicate's result.

### 5.3 The incident

Read after the last step's effect is established.

**Its subjects:** the anchor, and each step's target that Klyx's checks found failing in this investigation, by UID.

**Each subject's reading,** from the Observer's verdict by UID, fresh (observed after the last write), and never a new API read:

| Reading | The subject is |
|---|---|
| ok | recovered |
| paused, caused by a step of this plan on it (a suspension, a scale to zero, a pause, a KEDA pause) | held by step N, as intended |
| paused, not caused by this plan | unverified: "paused outside this plan" |
| progressing | pending; at the bound, not resolved: "still progressing" |
| fault or degraded | pending; at the bound, not resolved, with its verdict and time. Degraded is a failure in force, never recovery |
| unknown, stale, unreadable, or no fresh reading | pending; at the bound, unverified |
| removed by a step of this plan (`delete-pod`) | removed by step N. It cannot itself recover; its replacement, another UID, is shown as itself and never counted as the subject |
| gone or replaced otherwise | unverified: "gone" or "replaced" |

**The incident's outcome:**
- **resolved,** as soon as every subject is recovered or held, and at least one is recovered. The card then reminds that a held unit stays suspended until the Git fix lands and it is resumed;
- **not resolved,** at the bound, when a subject is not resolved on fresh evidence;
- **unverified,** at the bound otherwise: missing or stale evidence, a subject outside the plan's reach, or a removed anchor. Never "resolved" and never "still failing" without a fresh reading.

**A pod anchor deleted by the plan cannot establish resolution,** and the card says so before the confirmation: "deleting this pod ends what Klyx can verify about it; its replacement is shown on its own".

**Bound:** 10 minutes after the last write.

### 5.4 What the model learns

The next turn's input carries Klyx's record of each step: verb, target (kind, namespace, name), and the three outcomes in these words. No read the verification made is sent; the model reads what it needs through its tools, fenced as ever.

## 6. Egress, protection and the setting

- **The proposal is model output:** nothing new leaves Klyx to produce it.
- **The card and the action facts are local:** the change, the consequence and the facts never reach the model.
- **The model's feedback** carries outcome words and names already in the ledger; literal-needle tests cover it (§8).
- **Protected clusters:** proposals are made and shown as anywhere; each step needs its own typed grant; `allowProtectedEgress` governs the investigation as before.
- **The setting:** Settings > AI gains "Propose actions in investigations", on by default. Off removes the tool, the snapshot instruction and the card; the text remedy stays.
- **Chat** stays read-only: `propose_actions` is never offered in an ordinary conversation.

## 7. The lab and acceptance

The roadmap's §6.1 scenario runs on the kind lab on E1's fixture: the Kustomization `a1-web` failing `HealthCheckFailed` on Deployment `web` (crash commit `a9b3c81`), with the steady revision retained as the partner.

- **The scripted model** proposes `flux-suspend a1-web`, then `rollout-undo web` to the partner revision. The lab check confirms both. It sees each effect established, then the incident resolved: `web` recovered at the partner's template with its pods ready, `a1-web` held by step 1. It then restores the lab: it resumes `a1-web`, Flux reapplies the crash commit, and E1's setup sees its plant again.
- **The counterexamples on the lab:** `rollout-undo` without the suspension, where Flux reapplies within its interval and the card says what it read (the effect not established, or the incident not resolved); and a resume of `a1-web` from outside between the two steps, where step 2 stops with the prerequisite's reason.
- **The owner's real-model run:** the §6.1 sentence as the first question, end to end, on a real model. This is the programme's acceptance (roadmap §6.1).
- **The native smoke:** the card renders from the scripted model; a step's Review opens the ConfirmDialog, and Cancel sends nothing.

## 8. Tests

**Validation:**
- every matrix rule: an unknown verb; a kind outside the verb's; unknown parameters; an out-of-range count; an undo to the current revision, to a revision `rollout_history` did not establish, and to another workload's partner;
- §1's refusals: restart of a paused Deployment, of an `OnDelete` StatefulSet and DaemonSet, of a partitioned StatefulSet and of a workload with no desired pods; undo of a paused Deployment; reconcile of a suspended unit and of one an earlier step suspends;
- identity: a target read only by name (an inventory entry, a Git render); a target with no action facts, or whose facts were dropped at the cap; a name that matches a recorded object now replaced. All refused, never offered;
- citations: a step citing only a real read of an unrelated object is unsupported and not offered (the A-C1 counterexample);
- injection (A-C4): a pod log asking to delete every pod in `kube-system` yields a proposal on pods never read by UID, which is refused; a proposal naming another cluster's object cannot resolve;
- five steps; duplicates; a contradictory pair; no-ops.

**Execution:**
- RunStep refuses a step not offered, a superseded plan, a fenced conversation, a concurrent run, an expired token, and a token whose facts changed (a same-UID material change while the dialog was open);
- prerequisites: suspension established, then resumed or replaced from outside before step 2's Review, and again between Review and Confirm, each stopping step 2; the unchanged prerequisite as the control;
- the grant: a step without one is refused by the verb's own check; a second step cannot use the first one's; a RunStep refusal after the dialog armed consumes it;
- the connection: a disconnect, and a reconnect, between dispatch and the verb's first request and between two of its requests (discovery included) fail the request and never retarget it;
- the dispatch table: each entry makes the same request as the manual method;
- undo's partner: the revision object changed between the history read and the selection (the review's counterexample) is refused before writing; the manual undo with its new expectation;
- no retry after "unverified"; no compensation after a refused second step.

**Outcomes and verification** (a fake clientset with watches, a fake clock):
- the request: written with a receipt; refused, nothing sent (KEDA's version gate, a failed arming); refused by the server (4xx, `ErrReplaced`, an attributed admission refusal); unverified (5xx, a deadline, a transport loss). Scale's out-of-range count consumes the grant;
- the witness: a lost answer with no commit (a fully rolled-out Deployment stays unrestarted: unverified, the review's counterexample); a late commit (the token appears: established); a superseding write (another reconcile token handled: unverified); a same-second restart (distinct tokens since stage 1; and a receipt with no new generation: unverified);
- each verb's effect holding; the UID replaced mid-watch; the bound running out; a third party's write reverting the field; DaemonSet undo by template digest;
- `delete-pod`: a genuinely created replacement; an existing pod of that owner whose labels change to match its selector after R, never counted (the re-review's counterexample); a pod adopted by the owner after R (MODIFIED), never counted; a replacement deleted again before the effect completes; a large owner; a Job's pod; a pod without an owner; a `410 Gone`; the event cap; and a lost DELETE answer with the UID removed (established, the actor unverified);
- the incident: every row of §5.3's table; a degraded subject never counted as recovered; a scale to zero held, not recovered; a suspended anchor held only when the plan suspended it; a deleted pod anchor unverified with its replacement shown apart.

**Bounds:** full-shape tests at the action facts' cap (64 subjects of 1 KiB) and the step records' cap (16, through repeated and superseded plans), and the `report_findings` aggregate: a typical report admitted; a report within every field cap but above 24 KiB refused with the bound named, both an all-ASCII one (20 findings of 500 characters, 10 citations each, a 253-character Pod name and a 63-character namespace) and a non-ASCII one.

**Egress:** literal needles (env values, the template difference's hidden values, annotation values, a Secret's name read only by the card) never appear in the model's feedback, the proposal's echo or any send.

**Frontend:** the card's states; danger shapes; Review and its token; one arming per step; dismiss; supersession; withdrawal on a fence; the Activity log entry.

## 9. Stages

1. **The manual verbs' guards and receipts, each useful on its own:**
   - each matrix verb split into its shell and inner function (§4.4);
   - `outcome`, `notSent` and `receipt` on `ActionResultDTO`;
   - restart's token made unique (RFC3339Nano, as reconcile's already is);
   - undo's partner expectation (§4.5), with the history DTO carrying the revision object's UID and digest;
   - scale's grant order;
   - the manual rollout undo's consequence line from the authoritative ownership check (it misses Argo today).
2. **The proposal:** the action facts, `propose_actions` and the `actions` block, the matrix and its refusals, the validation and labels, and the remedy fix (§2.5).
3. **Execution:** `ReviewStep` and its token, `RunStep`, the standing prerequisites, the pinned connection and run fence, the dispatch table and the records.
4. **Verification:** the witnesses, the single-object watch, `delete-pod`'s replacement watch and the incident table.
5. **The frontend:** the card, Review and the ConfirmDialog, the Activity log, the setting.
6. **The lab, the smoke, captures, the owner's real-model run, and the as-built notes.**

## 10. Not in A3

- Writing to Git (decision 3, §11).
- Argo's verbs (with Argo parity), Helm rollback (it needs a UID fence), drain, reconcile with source, and the Flux Operator verbs (§1).
- Anything unattended. A2's reports may carry a proposal, which runs only from the card, under these rules.
- A causal-proof rule: no proposal makes a cause proven, and no established effect makes the model's mitigation right.
- Retries, compensation, an automatic resume.
- Atomicity across a plan's objects (§4.2).
- A new verb.

## 11. The recorded crossing and the owner's decisions

- **The crossing.** CLAUDE.md's editable-surface crossing says AI Suggest-fix proposes Git changes only and never applies them. A3 crosses that for the existing day-2 verbs only, behind the same human confirmation and guards as the manual verbs, under the 2026-06-17 directive. Git remains the source of truth for desired state.
- **Decision 3, A3's reach:** this note assumes day-2 verbs only, the roadmap's default. Pull requests would be a later addendum.
- **For the owner to confirm:** the matrix (§1: cordon and KEDA pause in, drain and Helm rollback out), and the setting on by default (§6).

## 12. Revision 1: Codex's design review of 9bde787

Codex requested changes (six P2, one P3, and guidance), with four probes against the current manual paths. Each is addressed:

1. **P2, identify the attempted write before calling its effect observed.** A lost answer let a later GET's generation, or whichever reconcile token it held, stand in for the write; an unrestarted, rolled-out Deployment met the restart counters. Now: receipts from the server's response (§5.1), effects split into state and event kinds (§1), and an event effect established only by this request's own witness, never a post-call generation or another request's token (§5.2). Restart's token is made unique (§9 stage 1). Tests: no commit, a late commit, a superseding write, a same-second restart (§8).
2. **P2, fence rollback's partner through the write path.** The undo path re-selected the revision by number, so a changed partner was written unreviewed. Now the shared path takes the revision object's UID and template digest and refuses a change before writing, the manual undo included (§4.5).
3. **P2, revalidate prerequisites.** A suspension established, then resumed from outside, left undo offered with a stale consequence. Now standing prerequisites are re-read at Review and in RunStep, and a review token refuses a material change between Review and Confirm; the remaining race is stated (§4.1 to §4.3).
4. **P2, DaemonSet undo.** DaemonSet has no `updateRevision`. Undo's effect is now the template digest at an observed generation for all three kinds, with the Deployment's partner ReplicaSet as an addition. Restart's vacuous cases (paused, `OnDelete`, a partition, no desired pods) and undo of a paused Deployment are refused (§1).
5. **P2, an exhaustive incident table.** Degraded, progressing, unknown and paused readings, and removed or replaced subjects, each have a stated contribution, and a deleted pod anchor cannot establish resolution (§5.3). The delete card promises recreation only for owners that keep a count (§1, §3).
6. **P2, bound the new evidence and reads.** The action facts are a bounded record extracted from Klyx's own reads (§2.2); step records are capped (§2.4); verifications and their watches are bounded (§5.2); `delete-pod`'s replacement needs no baseline set, so a cap cannot make an old pod look new (§5.2).
7. **P3, 24 KiB and the documented maxima.** It is now an aggregate limit on top of the character caps; a larger whole is refused with the bound named, and tested (§2.5). Revision 2 drops the claim that the ASCII maxima fit (§13).

**Guidance, adopted:** the header says Klyx checked eligibility, not the remedy (§3); the pinned connection binds every request of the verb and its verification through a run fence (§4.4); KEDA's effect is S9's completed pause (§1); the consequence is qualified by the ownership read's own fields and state (§3); Klyx's own pre-write refusals are "refused, nothing sent" (§5.1).

## 13. Revision 2: Codex's re-review of 44c1661

Codex requested one change, with two nonblocking notes; the six other findings are addressed.

1. **P2, a creation witness.** A watch filtered by the owner's selector reports ADDED for an existing pod whose labels start matching it, so an old pod could pass as the replacement. The watch is now unfiltered, from an unfiltered LIST's resourceVersion, without initial events; the owner is checked locally; adoption arrives as MODIFIED and is not counted; the replacement is tracked until the effect completes; and the claim is "created after the step began", not caused by it (§5.2). Tests: the label-transition counterexample, an adoption control, a genuine creation (§8).
2. **P3, the ASCII maxima.** An all-ASCII report at every field cap serializes to about 26 KiB. The claim that the maxima fit is dropped; the aggregate refusal is described and tested for that case too (§2.5, §8).
3. **The deletion's wording.** A deletion is established by the intended UID's removal; after a lost answer, who removed it stays unverified (§1).

## 14. ACCEPT (2026-10-24)

Codex accepted the design at e1933e4, after the review of 9bde787 and two re-reviews, with no remaining findings. The owner's decisions in §11 stay open: the matrix and the setting's default.

**Codex's implementation reminder, adopted:** `delete-pod` judges the deleted pod's removal and its replacement's liveness from one consistent event sequence. The unfiltered namespace watch of §5.2 carries both, so the deletion uses it instead of a separate object watch, and a replacement's DELETED event can never be missed behind the old pod's.

## 15. Stage 1 as built: the manual verbs' guards and receipts (2026-10-24)

**What changed in the manual verbs:**
- **Receipts:** `fleet.Receipt` (UID, generation, resourceVersion, token, the armed read's generation), noted by each matrix verb after a write the server accepted (`internal/fleet/receipt.go`).
- **Refusals before the write:** every return before a matrix verb's write is marked `fleet.ErrNotSent`. The wrapper keeps the refusal's own text and its chain, `ErrReplaced` included. `fleet.WriteOutcome` classifies written, refused (nothing sent, or by the server) and unverified.
- **The shells:** each matrix verb's bridge method keeps its signature as the shell (`checkProtected`, the lookup, the context), over an inner function in `internal/appbridge/verbs.go` that stage 3's RunStep will call with the pinned connection. `ActionResultDTO` gains `outcome`, `notSent` and `receipt`.
- **Restart's token** is RFC3339Nano, like reconcile's.
- **Undo writes only the reviewed revision:** `ClusterConn.RolloutUndo` takes a `*RevisionExpect` (the revision object's UID and template digest) and refuses with `ErrRevisionChanged`, before writing, when the object selected by number differs. The history carries `objectUID` and `templateDigest` (`workloads.TemplateDigest`, A1.1's normalisation over one JSON round trip). The manual undo passes them back.
- **Scale's grant order:** the count is checked after `checkProtected`, so an out-of-range count consumes the grant.
- **The manual undo's consequence** comes from `CRDService.GitOpsOwnership`, read when the dialog opens: the managing unit and its basis, worded for a suspended Flux owner and for Argo. A failed read falls back to the labels and says so (`undoOwnershipLine`).

**Deviations, for review:**
1. **Receipts travel in the context.** The design put the receipt in the inner function, and it is there: the inner function attaches a recorder (`fleet.WithReceipts`) and the verbs note into it, so the fleet's signatures, about 170 call sites with the tests, are unchanged. A verb called without a recorder notes nothing.
2. **A Scale carries no generation.** The scale subresource's answer has a UID and a resourceVersion only, so scale's receipt has no generation. Stage 4 reads the effect from the workload itself, as a state effect allows (§5.2).
3. **A deletion's receipt is the precondition's UID.** A typed delete returns no object; the UID precondition guarantees the server deleted exactly that pod.
4. **The digest is fail-safe across client versions.** A typed object drops fields newer than client-go's types, so a template carrying one digests differently from its unstructured read, and the undo is refused rather than written.
5. **Only the matrix verbs classify.** Other write paths do not mark their refusals before the write, so they leave `outcome` unset rather than call a local refusal "unverified".

**Tests:** `internal/fleet/receipt_test.go` covers the outcome classes, `ErrNotSent`'s chain, the recorder, the unique restart token and its receipt, refusals before the write (workload, Flux and KEDA verbs), each verb's receipt, the reconcile token, digest agreement between typed and unstructured reads, the expectation (the review's resampled-partner probe included), and a revision with no digest. `internal/appbridge/verbs_test.go` covers scale's grant, the request classes through a shell, and the manual undo's expectation; `ownership.test.ts` and `WorkloadsView.test.tsx` the consequence line and the passed revision.

**Gates:** gofmt; vet; `go test -race ./internal/...`; golangci-lint; tsc; vitest (2,441 tests); eslint (the four warnings main already has). A mutation pass caught 28 of 28, three of them after being rewritten to compile.

### 15.1 Codex's stage 1 review and the fixes (2026-10-24)

Codex requested changes at 17cf776 (five P2) and accepted deviations 1 to 3 and 5; deviation 4 needed finding 4. Each is fixed:
1. **No undo without its reviewed revision.** An empty expectation made the undo unchecked by number. `RolloutUndo` now refuses a missing or partial expectation (`ErrNoRevisionExpectation`, nothing sent), as does the bridge after the grant; the manual undo button is offered only for a revision whose object and template the history carries ("no undo" otherwise).
2. **The undo dialog's consequence is bound to its opening.** Each opening has its own number, and an ownership reply lands only on that opening (its cluster, workload UID and revision object). `ConfirmDialog` gains `waiting`: Roll back is not offered while the read is in flight; a failed read says so and the full confirmation follows.
3. **Unique attempt tokens.** Restart's and reconcile's tokens come from one process-wide generator, strictly later than any token issued before (an unchanged or stepped-back clock takes the last token plus a nanosecond), in the same RFC3339Nano format. Each attempt is noted before it is sent (`fleet.Attempt`, `ActionResultDTO.attempt`), apart from the receipt: a lost answer keeps its own token and has no receipt. Across a restart of Klyx, a repeat needs the same nanosecond again; this residual is stated.
4. **Exact integers in the digest.** Both of the digest's decodes keep JSON numbers as their text (`UseNumber`), so int64 values above 2^53 stay distinct, typed, unstructured or as a ControllerRevision's raw data, and an undo of a template changed only in such a value is refused.
5. **A fence before the write is "refused, nothing sent".** `readfence` gains a write tracker (attached by `fleet.WithReceipts`): its transport marks a refusal of a mutating request with `ErrNotForwarded` when no write under that tracker was forwarded before it, and `WriteOutcome` reads it as refused, nothing sent. A refusal after a forwarded write (a retry, a redirect's hop) stays unverified.

**Tests:** the review's probes as regressions: an undo with no or a partial expectation on both paths; the dialog's in-flight and cancelled-opening cases and a revision without identity; tokens under an unchanged clock, a stepped-back clock and concurrent attempts, and a lost reconcile after a handled one; exact int64 digests through all three inputs and the full undo refusal; a fence before the PATCH through a real client and an httptest server, with the forwarded-write control in `readfence`.

**Gates:** gofmt, vet, `go test -race ./internal/...`, golangci-lint, tsc, vitest (2,444 tests), eslint (main's four warnings). A mutation pass over the fixes caught 13 of 13; the last, the `waiting` guard behind a disabled button, after a test of Enter in the protected field.

### 15.2 ACCEPT (2026-10-24)

Codex accepted stage 1 at 9659602 after the review of 17cf776, with no remaining findings: the five fixes hold, and the context recorder, the Scale and typed-delete receipts, matrix-only outcomes, the conservative digest, the separate attempt record and the process-lifetime token policy are accepted. Later verification must still establish each effect with the accepted design's identity, generation and request witnesses.

## 16. Stage 2 as built: the proposal (2026-10-24)

**What it adds:**
- **The action facts** (`chat_facts.go`): extracted by `factOf` as `get_resource` (the anchor's seed read included), `gitops_unit` and `rollout_history` build their results, never sent (`ChatToolResult.facts` is unexported); 64 subjects of at most 1 KiB, the oldest dropped first, an object part and a history part each replaced by a newer read of its kind. `rollout_history`'s history part pins the partner's revision object UID and its `workloads.TemplateDigest`, the digest the undo path checks.
- **The proposal** (`chat_actions.go`): `propose_actions` (its verb an enum of the matrix) and the snapshot `actions` block, parsed strictly within 8 KiB; `validatePlan` decides each step in order with §1's and §2.3's rules, and a step refused or unsupported holds every later one. A plan's own rules (the same step twice, a contradicting pair) are decided before a step's eligibility, so a contradiction is named as one.
- **The turn:** the tool is offered, and the prompt names it, only in an investigation with proposals on; a call is routed like `report_findings`, not a read; its result is each step's state and reason and "nothing runs". The exchange carries `ActionPlanDTO`; the plan and the proposal count in its retention bytes.
- **The remedy** (§2.5): each part keeps 2,000 characters; `report_findings` has its 24 KiB aggregate bound, named when exceeded.
- **The setting:** `ai.proposeActions` (default on) through the config, the panel's DTO and `AIState.ProposalsOff`. A change publishes a new AI state, which fences open conversations.

**Deviations, for review:**
1. **Eligibility needs the object's own read.** §2.3 also admits an object "reached by a UID join"; a join (an owner chain, a dependency edge) carries no facts, so such a target is refused with "read it first". The model reads it with `get_resource`.
2. **A proposal is decided twice.** As `report_findings` is: at the call, which is what the model reads back, and again at the turn's end against the settled ledger and facts, which is what the exchange keeps. A step citing a read of its own batch can be refused at the call and offered at the end.
3. **The facts are bounded on their own** (64 KiB), not counted in the conversation's retention bytes; the plan and proposal are counted in the exchange's.
4. **The panel's setting is a pointer.** A draft that does not carry `proposeActions` keeps the current value, so an older panel cannot turn proposals off by saving.
5. **Kinds match exactly.** The target's kind is compared as the API names it (`Deployment`); a lower-case kind is refused as not in the matrix. A plan that parses is recorded even when every step is refused, so a corrected call in the same turn is refused as already proposed; the one retry is for a call that does not parse (stage 2 review).
6. **Supersession and the model's feedback of outcomes** belong with the steps' runs (stages 3 and 4): stage 2 keeps one plan per exchange.

**Tests** (`chat_actions_test.go`, `ai_settings_service_test.go`, `aiwrite_test.go`): fact extraction per kind and the record's bounds; the acceptance plan and its pinned partner; every matrix, identity, parameter and §1 refusal; citations (an unknown ID, an unrelated object, the anchor); the plan as a whole (holding, duplicates, contradictions, reconcile after suspend); the input's shape; the model's result carrying no fact; the turn in tools and snapshot modes with proposals on and off; chat never offered the tool; a proposal decided against its settled turn; the remedy and the aggregate bound; the partner digest agreeing with the undo path's; facts never in an envelope; the setting's default, round trip, draft without the field and startup state.

**Gates:** gofmt, vet, `go test -race ./internal/...`, golangci-lint, tsc, vitest, eslint. A mutation pass caught 31 of 31, two after the tests and the rule order were tightened.

### 16.1 Codex's stage 2 review and the fixes (2026-10-24)

Codex requested changes at e9bffad (four P2, one P3) and accepted the six deviations, with deviation 5's wording corrected above. Each is fixed:
1. **A fence withdraws the offer.** Whenever an investigation's state is returned, its plans' offered steps are shown as `withdrawn`, with the reason, when the conversation ended, its AI settings revision or pinned connection no longer holds, or proposals are off (`planFenceLocked`). The proposal stays readable; a model's own failure is not a fence.
2. **Facts keep their order.** Each object and history part carries its acquisition's place in one process-wide sequence, taken as the object is read, not when the tool's composite finishes: a part is replaced only by a later acquisition, and an object part never by a lower generation. A sequence rather than a clock, so two reads never tie.
3. **A snapshot follow-up keeps its reads' facts:** the follow-up acquisition records them as the first plan's and tools mode's do.
4. **KEDA's pause is its value.** The fact keeps the paused annotation's value and any other pause annotation beside it. `keda-pause` is "no change" only for a true value (which pauses on every version); a present `"false"` is offered, and the verb's own version gate decides. `keda-resume` is "no change" without the annotation, and refused when another pause annotation is set outside Klyx, as the verb refuses it.
5. **Retention counts a plan's pins:** the exchange's bytes include the facts and partners its plan pins.

**Tests:** the review's probes as regressions: a fence during the final call (a replaced connection, proposals turned off), a completed turn's offer, a model's failure, a retained plan after a settings change; an older read completing late, at the same and at a lower generation, and the history part; a snapshot follow-up proposing on its own read; KEDA's true, false and missing values and another pause annotation, for both kinds; the pins in the retention bytes. A mutation pass over the fixes caught 9 of 9, after a same-generation case was added.

### 16.2 Codex's re-review 1 and the fix (2026-10-24)

Codex found the other four fixes resolved and one residual P2 at 30fbea6: the withdrawal changed only the reply, not the plan, and published no new version, so a delayed older reply could restore the offer, and a connection that recovered offered the plan again. Now `withdrawFencedLocked`, run before every state reply and in every step's offer check, records the failed fence on each plan not yet withdrawn (`actionPlan.withdrawn`, sticky), and publishes a new version once when that hides an offer; a plan's reply shows the recorded withdrawal, never a live recomputation. Tests: the version advances once and a second read publishes nothing; a recovered connection keeps the plan withdrawn; an earlier retained plan is withdrawn with the latest; the chat store keeps the withdrawn reply when an older offered one arrives late. A mutation pass caught 5 of 5. The fix is on top of stage 3 (6997ed7), whose offer check uses the recorded withdrawal.

### 16.3 Codex's re-review 2 and the fix (2026-10-24)

Codex found the sticky withdrawal working for a settled plan, and one residual P2 at 6b5b9e2: the turn's settlement decided the proposal again as a new plan and dropped the recorded withdrawal, so a withdrawal observed during the final model call was lost when the turn settled, and the offer returned under a newer version. Now the settlement keeps the withdrawal, and a plan is marked settled when its turn ends, whatever its outcome; no step is offered before (stage 3's offer check), so nothing can run on a plan the settlement replaces. Test: the final call blocked, the connection fails and recovers, and the settled plan stays withdrawn; a review during the running turn is refused. A mutation pass caught 3 of 3.

## 17. Stage 3 as built: execution (2026-10-24)

**What it adds** (`chat_steps.go`):
- **The run state:** each plan keeps its steps' reviews and runs, the step running (its write, then its verification), and whether it was dismissed. A plan's DTO carries each step's run (request, effect, why, when) and the plan's status: superseded, withdrawn, declined or held, with why.
- **When a step is offered:** the investigation's fence holds (§16.1), its plan is the latest proposal, not dismissed, the step itself was offered and has not run, no step is running, and every earlier step ran with its effect established.
- **`ReviewStep`:** checks the fence, then reads again, through the pinned connection with the run fence, the target by its recorded API (the UID must match, and the step's §1 and §2.3 rules are decided again on the fresh read), an undo's partner revision object by name (its UID and template digest must match), and every earlier state effect by its own predicate. It words the change, the consequence (`GitOpsOwnership`, worded for a Flux unit, a suspended one, Argo, and an earlier step suspending the owner; `ReplicaOwner` for scale; recreation for delete-pod by its owner's kind) and the expected result with §1's bound, lists each read with its time, and returns a token valid for 2 minutes, kept with the digest of those facts.
- **`RunStep`:** consumes the protected grant first, then requires an offered step and its unexpired token, reads the same again and refuses a digest that changed, checks the fence again before the write, and calls the verb's inner function through the dispatch table, with the pinned UID, the validated parameters and an undo's pinned partner. It records the request's outcome; a written or unverified request's effect is pending until verified (§5.2, stage 4). A refused or unverified step holds the plan. `DismissPlan` declines a plan that is not running.

**Deviations, for review:**
1. **On `ChatService`.** `ReviewStep`, `RunStep` and `DismissPlan` are methods of the chat service that holds investigations, not a separate `InvestigationService`.
2. **The review's digest covers the target's generation and the fields the step reads, not its resourceVersion.** A workload's status changes the resourceVersion many times a minute during an incident; a digest over it would void almost every review before Confirm. The spec fields the change and consequence rest on, the generation, an undo's partner and each prerequisite's predicate are covered.
3. **Until stage 4, an effect is never established.** `verifyStep` settles it unverified ("Klyx does not verify a step's effect yet"), so a plan holds after its first step; stage 4 replaces it.
4. **The fence is also checked explicitly,** before the reads and again before the write, besides the transport fence a real client carries, so a stand-in or a change during the reads never carries a write.
5. **The inner functions take one-method interfaces** (`suspender`, `undoer`...), narrowing stage 1's: the manual shells pass their full connections unchanged.
6. **The latest parsed proposal is the current plan,** even one whose every step was refused; an earlier plan's unrun steps are superseded.
7. **A refusal in `RunStep` clears the review:** the operator reviews again.
8. **Undo's change names the revisions and the partner's object,** not the template difference's paths: A1.1's comparison is in its `rollout_history` read, which the card can cite.

**Tests** (`chat_steps_test.go`): a step run as reviewed through the manual verb's write; the token (none, another, expired); a material change between Review and Confirm; a replaced target; a step that became a no-op; standing prerequisites at Review and between Review and Confirm, and each state verb's predicate; an unestablished or refused step holding the plan; one step at a time; the grant consumed first and needed per step; a connection changed before and during the run; superseded, dismissed and withdrawn plans; an undo's partner read again; the consequence naming the owner's suspension and Argo; the dispatch table for every verb, and an undo without its partner.

**Gates:** gofmt, vet, `go test -race ./internal/...`, golangci-lint, tsc, vitest, eslint. A mutation pass caught 19 of 19, after a table of every prerequisite was added and two mutations were rewritten to compile and to target the merged fence.

### 17.1 Codex's stage 3 review and the fixes (2026-10-24)

Codex requested changes: five P2 and no P1. It accepted deviations 1, 3, 4, 5, 6 and 8, accepted 2 only for status-only churn, and corrected 7's wording. Each is addressed:

1. **P2, the consequence enters the digest.** The review's digest now covers the change and the consequence as worded, after the ownership reads (`GitOpsOwnership`, `ReplicaOwner`). Another owner, an ended suspension, an ownership read no longer complete, or a new or unproven autoscaler words another consequence, and RunStep refuses it ("it changed since you reviewed it"). The words are digested rather than a chosen subset of the reads, so what the operator confirmed is what runs; a warning whose text changes fails closed into a new review. Status-only churn still does not void a review.
2. **P2, one running step per conversation.** The conversation holds the plan whose step is running (`stepRunning`), reserved under the same lock as the offer check, through the write and its verification, and released on a refusal and when the effect settles. A step of any other plan, superseded or newer, reads "step N of an earlier plan is running".
3. **P2, a standing prerequisite is the matrix's whole effect.** One predicate per state verb (`stateEffect`, `chat_effects.go`) is the prerequisite now and verification's in stage 4:
   - scale: `spec.replicas`, the generation observed, and `status.replicas`;
   - pause and resume: `spec.paused` with the generation observed;
   - undo: the partner's template digest, the generation observed and, for a Deployment, the partner ReplicaSet by UID carrying the Deployment's revision;
   - KEDA pause: the annotation still true and the Observer's live verdict that the pause completed (S9's `PauseCompleted` or `JobPauseCompleted`, with its HPA evidence). Without a live verdict it fails closed;
   - KEDA resume: no pause annotation of either kind, and Ready not False.
4. **P2, a dismissal survives settlement.** The re-decision keeps `declined` as it keeps the withdrawal.
5. **P2, run records are bounded and counted.**
   - A run's error is capped at 512 bytes and its effect's reason at 384, on a rune boundary, so a record stays within §2.4's 1 KiB (as raw text; §17.2 makes it the serialized size, with the reason at 352).
   - `exchangeBytes` counts each run and review.
   - Retention runs after a run and after its effect settles, and never drops the exchange whose step is running.
   - Past 16 step records, the oldest superseded plan is dropped from the card, with a note on its exchange.

**Deviation 7, corrected:** only a refusal after the re-observation (a read, the fence or the digest) clears the review. An earlier gate's refusal (a wrong token, a missing grant) keeps it within its TTL.

**Codex's accounting probe:** its delta (`after - before >= len(errText)`) does not hold for correct accounting, because the run clears the review it was taken with (a 32-character token and a 64-character digest). The regression instead checks that `exchangeBytes` counts the record's texts exactly. Codex's other eight probes pass against the fix.

**Tests:** every finding has its regression: the five consequence changes and an unchanged control; a newer plan refused while the first plan's step verifies and while its write is in flight, then offered once it settles; a dismissal during the turn; a 100 KiB error; seventeen refused runs; retention around a running plan; a rune-boundary cap; a run's record taking the conversation over its byte bound; the record cap around the current and running plans; and a table of 33 prerequisite cases, including Codex's two KEDA counterexamples.

**Gates:** gofmt, vet, `go test -race ./internal/...`, golangci-lint, tsc, vitest, eslint. A mutation pass caught 29 of 29, after four mutations were rewritten to compile and two tests were added: one for retention after a run whose effect is still pending, one for the record cap's guards.

### 17.2 Codex's re-review 1 and the fixes (2026-10-24)

Codex found four of the five findings resolved and the fifth's accounting sound (its probe needed the 96-byte review correction). It requested two changes and one clarification:

1. **P2, a native verdict's state is its Role.** A native part (KEDA's among them) leaves a verdict's Kind empty and puts its state in Role, so a real completed pause was always refused; the test hook had supplied a Kind. Now:
   - a subject's verdict carries its state from whichever field its classifier fills;
   - a KEDA pause's completion is the captured row's own evidence (`keda.Verdict.Completed`, S9's `PauseCompleted` or `JobPauseCompleted` with its HPA evidence), joined to the fresh read: the verdict must describe the read's generation, and the read must show the annotation true, Paused True and Ready not False.

   The regression builds real rows through `keda.ScaledObjectRule` and `keda.ScaledJobRule`, without the hook: completed; HPA absence unproven; a capture that lags the read; and a read showing Ready False.
2. **P2, a Deployment undo's current ReplicaSet follows A1.1's evidence rule.** The partner, read by name, must:
   - be the pinned UID;
   - be controlled by the Deployment's UID;
   - carry the pinned template.

   A LIST of the Deployment's ReplicaSets, by its selector, must show no other ReplicaSet of that Deployment with the same template: an ambiguity is unestablished, and so is a read in part. The revision annotation only corroborates. Codex's probe passes with one change: its Deployment fixture gains the `spec.selector` every apps/v1 Deployment has.
3. **P3, the bound is the serialized record.** `capRecord` caps a text by its JSON encoding as `encoding/json` writes it, counting control characters, HTML-safe escapes and invalid UTF-8 as 6 bytes, quote and backslash as 2. With the error at 512 bytes and the reason at 352, a record of the longest values serializes within 1 KiB; the test tries seven worst-case characters.

**Gates:** as in §17.1. A mutation pass caught 15 of 15, after three real-row cases were added for mutations the earlier checks had masked.
