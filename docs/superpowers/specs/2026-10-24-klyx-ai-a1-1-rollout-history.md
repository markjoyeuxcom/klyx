# Klyx AI A1.1: rollout history

Opened 2026-10-24, on `feat/ai-a1-1`; revisions 1 and 2 the same day (§10, §11). A design addendum to A1 (`2026-10-21-klyx-ai-a1-investigation.md`), accepted (§12); nothing is built.

## 0. Why

A1's design defined a `rollout` check class (§2) and a rollout-history step in the workload's snapshot plan (§1.1). As built, no tool read it: deviation 4, which Codex accepted as an explicit scope reduction (A1 §13.1, §13.7). Today the workload plan says "rollout history: no A1 tool reads it" in missing evidence.

The owner decided on 2026-10-23 to build it as A1.1, after A1's acceptance and before A3. A3's rollback mitigation needs the previous revision. An investigation without Git also needs to see what a rollout changed: for a Deployment that Flux does not own, the revision history is the only change record Klyx holds.

**Scope:** one read-only tool, `rollout_history`, its `rollout` checks, and its steps in the snapshot plans. Nothing else in A1 changes.

**What the history can say, and what it cannot.** Kubernetes keeps a bounded set of revision objects, numbered in the order each became the controller's target. On a rollback it reuses an old object and gives it the next number, without recreating it. So the history tells which retained revision matches the workload's template, and what two retained revisions' templates differ in. It does not tell when a change took effect, or whether a revision missing from the history ever existed. Every statement below is held to that.

## 1. The tool: `rollout_history` (new)

- **Arguments:** `kind` (Deployment, StatefulSet or DaemonSet), `namespace`, `name`. Strict, validated server-side, as every A1 tool.
- **Offered** in investigations and in chat: it is read-only and needs no comparison set (A1 §3).
- **Reads,** through `ChatConn` under `guard()` before each one, one after another inside its worker, so A1 §1's bound of 6 reads at once holds, fan-out included (A1 §13):
  1. the workload (`ChatGet`): its UID, generation, observed generation, selector, pod template and reported revision markers;
  2. its revisions (`ChatList` by the workload's selector): ReplicaSets for a Deployment, ControllerRevisions for a StatefulSet or a DaemonSet, one page of at most 50 (`chatReplicaSetsMax`). Only those whose controller owner reference is the workload's UID are kept;
  3. its pods (`ChatList` by the selector), one page of at most 100 (`chatWorkloadPodsMax`), kept by controller UID;
  4. Klyx's verdicts for those pods by UID (`verdictsFor`): the Observer's capture, in memory, with its own time and coverage.

  These are its only reads. It shares `workloadPods`' join code, not its reads, so no second chain of reads runs. There is no supplementary read: a revision outside the page is not held.

**History coverage.** A page at its cap, or with a continuation, makes the history partial. It is said in the data and in every check that depends on it.

## 2. Revision facts and their evidence

Each fact carries the evidence it rests on. A fact that cannot be established is reported as not established, never filled in with a nearby one.

**Revisions.**
- A Deployment's revision is a ReplicaSet with a parsable `deployment.kubernetes.io/revision`. One without it is not history, and is counted as skipped.
- A StatefulSet's or DaemonSet's revision is a ControllerRevision, numbered by `.revision`.

**The controller's markers, reported as markers.** They are what the controller wrote, not proof of which template the workload asks for:
- **Deployment:** the `deployment.kubernetes.io/revision` annotation;
- **StatefulSet:** `status.updateRevision` and `status.currentRevision`, by name;
- **DaemonSet:** none. The data gives the highest revision in the page read, as "highest retained in this read", never as the workload's highest.

A marker naming a revision the page does not hold is reported as "not in the history read".

**Generation lag.** When `status.observedGeneration` is below `metadata.generation`, the data says the controller has not yet observed the latest spec, with both numbers. No marker then speaks for the current spec.

**The revision matching the desired template.** This is the only "current" Klyx asserts. It is established by template equality, Kubernetes' own rule (`FindNewReplicaSet`): the retained revision whose pod template equals the workload's `spec.template`.
- A Deployment's comparison ignores the `pod-template-hash` label.
- A ControllerRevision's comparison uses its `data.spec.template` without the `$patch` directive.
- No match in the page read is "not established", with the history coverage. Two matches are "ambiguous". Neither falls back to a marker.

**The comparison partner.** This is the nearest lower revision retained in the read, below the desired-template revision. It is said as "the nearest lower retained revision". When its number is not the next lower one, the data says that the revisions between them are not held in this history read. Why they are absent from the cluster is not established. It is never called the previous rollout, or a chronological predecessor.

**Pinned before any cut.** The desired-template revision and its partner, when held, are chosen before the 10-row limit and the byte cut, and kept by both. When either is not held, the data says so.

**Times, kept apart:**
- **an object's creation time:** metadata only. A reused object keeps its old one, so it dates neither the rollout nor the change;
- **the read's time:** when the history and the pods were read;
- **the verdict capture's time and coverage:** when Klyx's verdicts were evaluated (§4).

## 3. What the tool returns

Newest revision number first:
- **The workload:** its markers, generation lag, the desired-template revision or why it is not established, and the history coverage.
- **The list:** at most 10 revisions, the pinned ones always among them, counted beyond.
- **For each revision:**
  - its number, its object (ReplicaSet or ControllerRevision, by name and UID), the object's creation time, and which markers name it;
  - its images by container, init containers first, at most 8 per revision, counted beyond;
  - for a ReplicaSet, its desired, ready and available replicas, qualified by its own generation lag when its status lags;
  - its listed pods: how many, how many are Ready by their pod status, and Klyx's verdict counts (§4);
  - `changeCauseRecorded`: whether `kubernetes.io/change-cause` is set. Its text is never sent: kubectl's `--record` writes the whole command line, and `kubectl set env ... --record` puts a value in it.
- **What changed,** for the desired-template revision against its partner, and for the newest two other retained revisions against their nearest lower ones: at most 3 comparisons per read.

**What changed, compared like `git_changes` (A1 §3.2).** The two revisions' pod templates are compared with `objectChanges`, as two objects of kind ReplicaSet in group `apps`. A1's alignment and projection rules then apply unchanged:
- **The templates:** a ReplicaSet's `spec.template`, with the `pod-template-hash` label removed on both sides. A ControllerRevision's `data.spec.template`, with its `$patch` directive removed. Only that wrapper is built around the template. Nothing else from the raw objects enters it.
- **What is shown:** a difference chat's projection shows (an image, resources, a probe) is listed with its projected values. Every other difference is a path marker, "changed (not shown)": an env value, a command, args, a template annotation outside the allowlist. No hidden value, and no digest of one, is serialized.
- **Bounds:** at most 20 paths per comparison, counted beyond, as `git_changes`. `objectChanges` keeps at most 1000 raw paths, which bounds what it retains, not the work of the walk; that work is measured by the full-shape load test (§9).
- **No template:** a ControllerRevision whose data holds no `spec.template` says "template not readable" and is not compared. Nothing is guessed.

**The envelope:** chat's 16 KiB. Every output string is scrubbed (`ScrubEvidence`) before the clip. When it clips, the unpinned revisions' changes go first, then the unpinned revisions. If the pinned rows still do not fit, the result says their data could not fit, rather than claiming them.

## 4. Pod health: known, current or missing

The pods are read live, but their health is Klyx's verdict from the Observer's capture, which has its own time and coverage. For each revision's listed pods, the tool counts:
- **current:** a verdict in a capture whose coverage is live, split into failing (fault or degraded) and not failing;
- **retained:** a verdict from a stale, partial or checking capture. It is shown as last observed, with the capture's time, and never counted as current;
- **missing:** no verdict for the pod's UID, or the verdict read failed. It counts as unknown.

Readiness is a separate fact, from each pod's own Ready condition in the pod list, at the read's time. No failing verdict is not readiness: a progressing or unclassified pod need not be ready.

## 5. Identity

The checks speak of the workload this read holds, by its UID: the tool read it live, and its revisions and pods are joined to that UID through owner references (A1 §2's "by UID" sources).
- **The anchor.** When the workload is the anchor and its UID differs from the pinned one, the read says "possibly a replacement" and makes no rollout check: A1's anchor rule.
- **An object a by-name source named.** A health-check entry or an alert can name a workload by name. A1 §2's separate fact applies unchanged: "Deployment shop/web is currently UID B, created at T", with "possibly a replacement" when it was created after that evidence. Today's read never becomes that source's historical identity.

## 6. The `rollout` checks

Deterministic, with Klyx's own text. None sets `Failing`. A rollout check attributes; whether something is failing is for the `verdict`, `status` and `condition` checks. Each carries its own sources' times and coverage, never one blanket freshness. The history's coverage is named in each when partial.

1. **Which revision the failing pods run.** One check per revision with at least one current failing verdict among its listed pods, at most 3 such checks.
   - Statement: "2 of the 3 listed pods of Deployment shop/web's revision 4 (ReplicaSet web-7d9) are failing by Klyx's verdict; 1 has no verdict. Revision 4 matches the Deployment's template."
   - The last sentence appears only when established; otherwise the statement names the marker, such as "the revision the controller's annotation names".
   - Observed: the verdict capture's time. Coverage: the capture's.
   - A retained (stale) failure makes no check. The data shows it as last observed.
2. **The partner revision's pods: two checks, one per source.** Each has its own observation time, so A1's chronology qualifies each on its own, whether a finding cites the check or its whole read. No check combines them, and no check says the partner is serving.
   - **2a, readiness:** made only when every listed pod of the comparison partner is Ready by its pod status. Statement: "Deployment shop/web's revision 3 (ReplicaSet web-5c4), the nearest lower retained revision, has 2 listed pods, both Ready." Observed: the pod read's time. Coverage: the pod list's.
   - **2b, verdicts:** made only when every listed pod of the partner has a current verdict, and none is failing. Statement: "none of the 2 listed pods of Deployment shop/web's revision 3 (ReplicaSet web-5c4) is failing by Klyx's verdict." Observed: the verdict capture's time. Coverage: the capture's.
   - When a check's condition does not hold, that check is not made, and the data keeps the neutral inventory. Nothing is said about a partner with no pods now: its past health is not in the data.
3. **What two retained revisions' templates differ in.** Made for the desired-template revision against its partner, when both are held and compared.
   - Statement: "the pod template of Deployment shop/web's revision 4, which matches its current template, differs from revision 3, the nearest lower retained revision, in: containers[web].image, registry/web:1.2 to registry/web:1.3; containers[web].env[MODE], changed (not shown). When revision 4 became active is not in the data."
   - At most 5 paths in the statement, as `git_changes`, and "+N more".
   - Observed: the read's time. It is a comparison of the objects as read now. The objects' creation times are not its observation time, so A1's chronology never treats it as evidence from before the failure.

The checks pass through `capChecks` and chat's statement bound (1024 characters), as every A1 check.

## 7. The plans

**Snapshot investigations (A1 §1.1):**
- **A workload:** `rollout_history` of the workload replaces the line "rollout history: no A1 tool reads it". It runs after the workload's events, before its alerts.
- **A Flux Kustomization:** `rollout_history` of each health-check object that is a workload, at most 3, as the plan already selects them. It runs after `git_changes`, so the Git evidence keeps its place in the 96 KiB fill.
- **The budgets:** both steps run within the plan's existing read and time budgets. A step that does not fit is listed in missing evidence with its reason.

**Tools mode:** the tool is offered. The investigation's instructions do not change: the model chooses its reads.

## 8. Egress and A3

**Egress.** Nothing reaches the model that chat's projection would not already send:
- **Sent:** images, names, UIDs, revision numbers, creation times, markers, replica counts, Ready counts and Klyx's verdict counts.
- **Withheld:** the change cause's text, a ControllerRevision's raw data, env values, commands and args. Of these, only path markers cross.
- **Unchanged:** the 16 KiB result, the per-call bound and the turn's budgets. A1's readfence covers the three API reads: `guard()` before each, and the transport fence underneath.

**A3.** A3's rollback card can use only what A1.1 establishes: the desired-template revision, its nearest lower retained partner, their identities, and how each was established. A1.1 names no rollback target of its own. A3 still confirms through the unchanged verb, `RolloutUndo`, armed by the workload's UID.

## 9. Tests

- **Revisions:**
  - a ReplicaSet under the same selector owned by another workload is excluded;
  - one without the revision annotation is skipped and counted;
  - 12 revisions list 10, the pinned ones among them, and count 2 beyond;
  - images are per container, init containers first.
- **Current and partner:**
  - template equality finds the desired revision for each kind, and two matches read "ambiguous";
  - a lagging Deployment, whose annotation names the old revision while its spec asks for a new one, never calls the old one current, and says the generation lag;
  - a StatefulSet whose `updateRevision` and `currentRevision` differ reports both as markers;
  - a desired revision outside the page read, or below the 10-row range, is "not in the history read", or kept by the pin;
  - a missing intermediate revision makes the partner "the nearest lower retained revision", with the revisions between not held in this history read;
  - a capped DaemonSet list gives "highest retained in this read" with partial coverage.
- **Times:** a rolled-back revision reusing an old object, for each kind, beside a newly created revision as the control. The change check is observed at the read's time, and A1's chronology labels are asserted, not only the text.
- **Pod health:**
  - one listed pod without a verdict;
  - a failed verdict read;
  - a stale fault and a stale non-fault;
  - a checking or capped capture;
  - a partial pod list;
  - a current pod that is progressing or not Ready;
  - a fully current, ready partner as the positive control.

  Each asserts the checks' times, coverage and A1 labels, not only the counts.
- **The partner's two times:** the failure between the verdict capture and the pod read, in both orders, and both sources after it. A finding citing check 2a or 2b directly, and one citing the whole read, each get the time qualification of what it cites, with the source's coverage kept.
- **Joins:** a Deployment's pods by owner UID; a StatefulSet's by the ControllerRevision's name; a DaemonSet's by its hash label; a pod whose label matches no revision is unjoined and counted.
- **What never crosses:** env values, commands, args, probe commands, template annotations outside the allowlist, encrypted values, the change cause's text and a ControllerRevision's raw data are absent from the serialized result, searched as bytes. A maximal-length name and a ControllerRevision without `spec.template` are handled.
- **Identity:** a replaced anchor makes no rollout check. A by-name health-check object carries the replacement caveat.
- **Bounds and load:**
  - a full admitted shape: 50 large ControllerRevisions, 100 pods, 8 images per revision, and maximal strings, paths and checks;
  - the shrink policy with large pinned rows, which are present or explicitly said not to fit;
  - concurrent rollout reads alongside A1's load test (`KLYX_A1_LOAD`), with its heap measured.
- **Fences:** `guard()` runs before each of the three API reads, counted as A1's fence-step test counts them. A fenced read returns the fence error.
- **Plans:** the workload plan reads the history, and the "no A1 tool reads it" line is gone. The Kustomization plan reads each health-check workload's history after `git_changes`.
- **The lab:** on the A1 lab's crash-looping `web`, the history shows the desired-template revision's pods failing, and its difference from the nearest lower retained revision naming `env[MODE]`, not shown.

The gates are A1's: the race suites, a mutation pass over the new tool, the native smoke and the captures four ways.

## 10. Revision 1: Codex's review of de8ad95

Codex requested changes (handoff d71a066e), three P2 findings, all taken:

1. **An object's creation time did not date a change.** A rolled-back revision reuses an old object. Creation time is now metadata only (§2), and the change check is a comparison of retained objects observed at the read's time, saying that when the revision became active is not in the data (§6). Reuse cases for each kind, with a new-revision control and asserted chronology labels (§9).
2. **"Current" and "previous" needed explicit evidence.** Markers are reported as markers, with generation lag. The only asserted current is the revision matching the workload's template by Kubernetes' own equality rule. Otherwise it is not established, with no fallback (§2). The partner is "the nearest lower retained revision", never the previous rollout. Both are pinned before any cut, and a revision outside the page read is said, with no supplementary read (§1, §2). A3 consumes only this (§8).
3. **No joined failure was not health.** Pod verdicts are counted as current, retained or missing, with the capture's time and coverage. Readiness comes from each pod's Ready condition. The partner check needs every listed pod current, not failing and Ready, and otherwise stays neutral inventory. Each check carries its own sources' times and coverage (§4, §6).

Also from the guidance:
- the egress needles add args, probes, annotations, encrypted values and maximal names;
- the reads are counted once: the tool shares `workloadPods`' join code, not its reads;
- a full-shape load measurement beside A1's load test, and a shrink-policy test with large pinned rows (§9).

## 11. Revision 2: Codex's re-review of 8807917

Codex found the three revision 1 findings resolved, and requested changes for one residual P2 (handoff abfe5c0b), taken:

1. **Taking the later source time refreshed the older part of the partner check.** A `ChatCheck` has one observation time, and A1's labeller dates a citation by it alone. The partner check is now two checks, one per source: 2a, readiness from the pod list at the pod read's time; 2b, no failing verdict from the Observer at the capture's time. Each is made only under its own condition, with its own coverage (§6). Tests put the failure between the two times in both orders and after both, and compare a direct citation with a whole-read citation (§9).

Also from the notes:
- a gap in the history read says the revisions between are not held in this read, never why they are absent from the cluster (§2);
- `objectChanges`' 1000-path bound limits what it keeps, not the walk's work, which the load test measures (§3).

## 12. ACCEPT (2026-10-24)

Codex accepted the design at 3a0f9d3 (handoff 353d3d44), with no remaining findings, after the review and two re-reviews. The implementation is reviewed on its own.
