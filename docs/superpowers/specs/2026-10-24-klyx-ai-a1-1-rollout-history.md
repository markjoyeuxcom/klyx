# Klyx AI A1.1: rollout history

Opened 2026-10-24, on `feat/ai-a1-1`. A design addendum to A1 (`2026-10-21-klyx-ai-a1-investigation.md`) for review; nothing is built.

## 0. Why

A1's design defined a `rollout` check class (§2) and a rollout-history step in the workload's snapshot plan (§1.1). As built, no tool read it: deviation 4, which Codex accepted as an explicit scope reduction (A1 §13.1, §13.7). Today the workload plan says "rollout history: no A1 tool reads it" in missing evidence.

The owner decided on 2026-10-23 to build it as A1.1, after A1's acceptance and before A3. A3's rollback mitigation needs the previous revision. An investigation without Git also needs to see what a rollout changed: for a Deployment that Flux does not own, the revision history is the only change record Klyx holds.

**Scope:** one read-only tool, `rollout_history`, its `rollout` checks, and its steps in the snapshot plans. Nothing else in A1 changes.

## 1. The tool: `rollout_history` (new)

- **Arguments:** `kind` (Deployment, StatefulSet or DaemonSet), `namespace`, `name`. Strict, validated server-side, as every A1 tool.
- **Offered** in investigations and in chat: it is read-only and needs no comparison set (A1 §3).
- **Reads,** through `ChatConn` under `guard()` before each one, one after another inside its worker, so A1 §1's bound of 6 reads at once holds, fan-out included (A1 §13):
  1. the workload (`ChatGet`): its UID, selector and current revision;
  2. its revisions (`ChatList` by the workload's selector): ReplicaSets for a Deployment, ControllerRevisions for a StatefulSet or a DaemonSet, at most 50 (`chatReplicaSetsMax`). Only those whose controller owner reference is the workload's UID are kept;
  3. its pods (`ChatList` by the selector), at most 100 (`chatWorkloadPodsMax`), kept as `workloadPods` keeps them, and Klyx's verdicts for them by UID (`verdictsFor`, the Observer's rows, in memory).

  A list at its cap, or with a continuation, makes the read's coverage partial.

**Which revision is current, and which revision a pod runs.** These follow the app's rollout lens (`fleet.RolloutHistory`), joined by UID:

| Kind | A revision | Current | A pod's revision |
|---|---|---|---|
| Deployment | a ReplicaSet with a parsable `deployment.kubernetes.io/revision`; one without is not history, and is counted as skipped | its revision equals the Deployment's annotation | the pod's controller owner UID is the ReplicaSet's |
| StatefulSet | a ControllerRevision, numbered by `.revision` | its name equals `status.updateRevision` | the pod's `controller-revision-hash` label equals the ControllerRevision's name |
| DaemonSet | a ControllerRevision, numbered by `.revision` | the highest revision: a DaemonSet's status names none, and the data says so | the pod's `controller-revision-hash` label equals the ControllerRevision's own `controller-revision-hash` label (its name is `<daemonset>-<hash>`) |

The StatefulSet and DaemonSet joins were checked on the kind lab (`klyx-s9-lab/sts`, `kube-system/kube-proxy`). A pod whose label matches no listed revision stays unjoined and is counted.

**Returns, newest revision first:**
- **The list:** at most 10 revisions, the current always among them, counted beyond.
- **For each revision:**
  - its number, its object (ReplicaSet or ControllerRevision, by name and UID), its creation time, and whether it is current;
  - its images by container, init containers first, at most 8 per revision, counted beyond;
  - for a ReplicaSet, its desired, ready and available replicas;
  - its pods: how many are listed, and how many Klyx's verdict reads as fault or degraded, with at most 3 failing pods' names;
  - `changeCauseRecorded`: whether `kubernetes.io/change-cause` is set. Its text is never sent: kubectl's `--record` writes the whole command line, and `kubectl set env ... --record` puts a value in it;
  - **what changed from the previous listed revision,** for the newest 3 revisions only (below).

**What changed, compared like `git_changes` (A1 §3.2).** The two revisions' pod templates are compared with `objectChanges`, as two objects of kind ReplicaSet in group `apps`, so A1's alignment and projection rules apply unchanged:
- **The templates:** a ReplicaSet's `spec.template`, with the `pod-template-hash` label removed on both sides. A ControllerRevision's `data.spec.template`, with its `$patch` directive removed.
- **What is shown:** a difference chat's projection shows (an image, resources, a probe) is listed with its projected values. Every other difference is a path marker, "changed (not shown)": an env value, a command, args, a template annotation outside the allowlist. No hidden value, and no digest of one, is serialized.
- **Bounds:** at most 20 paths per revision, counted beyond, as `git_changes`.
- **No template:** a ControllerRevision whose data holds no `spec.template` says "template not readable" and is not compared. Nothing is guessed.

**The envelope:** chat's 16 KiB. `ScrubEvidence` runs before the clip. When it clips, the oldest revisions' changes go first, then the oldest revisions themselves; the current and previous revisions stay.

## 2. Identity

The checks speak of the workload this read holds, by its UID: the tool read it live, and its revisions and pods are joined to that UID through owner references (A1 §2's "by UID" sources).
- **The anchor.** When the workload is the anchor and its UID differs from the pinned one, the read says "possibly a replacement" and makes no rollout check: A1's anchor rule.
- **An object a by-name source named.** A health-check entry or an alert can name a workload by name. A1 §2's separate fact applies unchanged: "Deployment shop/web is currently UID B, created at T", with "possibly a replacement" when it was created after that evidence.

## 3. The `rollout` checks

Deterministic, with Klyx's own text. None sets `Failing`. A rollout check attributes; whether something is failing is for the `verdict`, `status` and `condition` checks.

1. **Which revision the failing pods run.** One check per revision holding failing pods, at most 3 such checks:
   - Statement: "3 of the 3 listed pods of Deployment shop/web's revision 4 (ReplicaSet web-7d9, the current revision, created 2026-10-24T12:01:07Z) are failing by Klyx's verdict".
   - Observed: the read's time.
   - Subject: the workload, by UID.
2. **The previous revision still serving.** Made only when the revision before the current one has listed pods and none failing:
   - Statement: "Deployment shop/web's revision 3 (ReplicaSet web-5c4) has 2 listed pods, none failing; 2 of 2 replicas ready".
   - Observed: the read's time.

   Nothing is said about a previous revision with no pods now: its past health is not in the data.
3. **What the current revision changed.** Made when both templates were compared:
   - Statement: "Deployment shop/web's revision 4, created 2026-10-24T12:01:07Z, changed from revision 3: containers[web].image, registry/web:1.2 to registry/web:1.3; containers[web].env[MODE], changed (not shown)". At most 5 paths in the statement, as `git_changes`, and "+N more".
   - Observed: the current revision's creation time, when the change took effect.

   Like a commit's diff, it may precede the failure, and A1's chronology then qualifies it as earlier evidence.

The checks pass through `capChecks` and chat's statement bound (1024 characters), as every A1 check.

## 4. The plans

**Snapshot investigations (A1 §1.1):**
- **A workload:** `rollout_history` of the workload replaces the line "rollout history: no A1 tool reads it". It runs after the workload's events, before its alerts.
- **A Flux Kustomization:** `rollout_history` of each health-check object that is a workload, at most 3, as the plan already selects them. It runs after `git_changes`, so the planted Git evidence keeps its place in the 96 KiB fill.
- **The budgets:** both steps run within the plan's existing read and time budgets. A step that does not fit is listed in missing evidence with its reason.

**Tools mode:** the tool is offered. The investigation's instructions do not change: the model chooses its reads.

## 5. Egress

Nothing reaches the model that chat's projection would not already send:
- **Sent:** images, names, UIDs, revision numbers, creation times, replica counts and Klyx's verdict counts.
- **Withheld:** the change cause's text, a ControllerRevision's raw data, env values, commands and args. Of these, only path markers cross.
- **Unchanged:** the 16 KiB result, the per-call bound and the turn's budgets. A1's readfence covers the three reads: `guard()` before each, and the transport fence underneath.

## 6. A3

A3's rollback card needs the revision to return to. A1.1 gives it the revision number and the UID it was read from. A3 still confirms through the unchanged verb, `RolloutUndo`, armed by the workload's UID; A1.1 adds nothing to the write path.

## 7. Tests

- **Revisions:**
  - a ReplicaSet under the same selector owned by another workload is excluded;
  - one without the revision annotation is skipped and counted;
  - 12 revisions list 10, the current among them, and count 2 beyond;
  - images are per container, init containers first.
- **Joins:**
  - a Deployment's pods join by owner UID;
  - a StatefulSet's by the ControllerRevision's name;
  - a DaemonSet's by its hash label, with the highest revision current;
  - a pod whose label matches no revision is unjoined and counted.
- **Checks:**
  - the failing-pods check names the revision its pods run;
  - the serving check is made only when the previous revision has pods and none failing;
  - the change check lists a shown image change and a hidden env change.
- **What never crosses:** an env value, a command, args and the change cause's text are absent from the serialized result, searched as bytes. A ControllerRevision without `spec.template` reads "template not readable" and makes no change check.
- **Identity:** a replaced anchor makes no rollout check. A by-name health-check object carries the replacement caveat.
- **Bounds:** lists at their caps make the coverage partial. A clipped result keeps the current and previous revisions.
- **Fences:** `guard()` runs before each of the three reads, counted as A1's fence-step test counts them. A fenced read returns the fence error.
- **Plans:**
  - the workload plan reads the history, and the "no A1 tool reads it" line is gone;
  - the Kustomization plan reads each health-check workload's history after `git_changes`.
- **The lab:** on the A1 lab's crash-looping `web`, the history shows the current revision's pods failing, and its change from the previous revision naming `env[MODE]`, not shown.

The gates are A1's: the race suites, a mutation pass over the new tool, the native smoke and the captures four ways.

## 8. Stages

One stage: the tool, its checks, the two plan steps and the tests. Then the gates, the lab check and an as-built section here. This note goes to Codex before it is built.

**The eval.** Once both exist, the eval's E1 (`2026-10-22-klyx-ai-eval.md`) can count A1.1's change check, naming `env[MODE]`, as planted evidence beside the Git diff. That is an addendum to the eval, not part of A1.1.
