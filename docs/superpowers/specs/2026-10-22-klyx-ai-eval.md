# Klyx AI eval: scored investigations on the kind lab

Opened 2026-10-22, on `feat/ai-eval`; revisions 1 to 3 (§11 to §13) on 2026-10-24. The design is accepted (§14); stage 1 is built (§15).

## 0. Why

A1's last open step is the owner's real-LLM smoke (A1 §13.3): one investigation on the lab with a real model. A single run says little: models vary run to run, and what matters is how often a model names a planted cause, on the evidence that shows it, without being drawn to a lookalike. The eval turns the smoke into a repeatable measurement on known faults on the kind lab, so the owner can:

- compare providers and models before choosing one;
- see a model's habits against A-C1: claims without evidence, causes cited to unrelated objects, evidence from before the failure;
- check that a later change does not make investigations worse, Klyx's own regressions included.

The idea is the scenario benchmark of agent projects such as kubectl-ai, applied to Klyx's contract. **What it measures, and what it does not:**
- Klyx's labels are provenance, never proof (A-C1). The eval can check deterministically whether a model's cause rests on the planted evidence, as delivered to the model. It cannot check by itself that the model's prose states the right cause.
- So the automatic marks are named for what they verify. Whether a statement is the right diagnosis is the owner's call, made in the report (§6).
- The eval introduces no "proven" label into A1, and nothing it scores is shown in the app.

## 1. What it is

A headless runner inside the app binary, built only with `-tags eval`, as the native smoke is built only with `-tags smoke`. No shipped build contains it.

**Startup order.** The fleet starts connecting early in `main` (`main.go:146`), so the lab-only preflight (§3) runs in the eval build's `init()`, before `main`, on the configuration it was given. Like the smoke hook, `init` stands aside in the confined build's child (`cachediff.ChildFlag`), so `git_changes` keeps working. After `main` has wired every service, a hook takes over before any window opens. For each scenario and each repetition, it:

1. checks the plant on its own connection, then waits for Klyx's readiness (§2);
2. resolves the anchor's UID on the investigation's own connection and pins it;
3. starts an investigation of the anchor through the real `ChatService`, with the configured provider and the default question; no scenario expectation is ever sent to the model;
4. waits for the turn to end, then checks the plant again for drift;
5. assigns the run a status (§4) and scores it from Klyx's findings and the delivered-evidence record (§5, §6);
6. clears the investigation.

Reusing `main`'s wiring means the eval measures the shipped code path: the tools, projection, scrubbing, fences, budgets and labelling. The one addition inside A1 is the eval build's recorder (§5), which observes and changes nothing.

**Inputs, from the environment:**
- `KLYX_CONFIG`: the eval's own fleet config (§3), never the owner's;
- `KLYX_EVAL_OUT`: the report directory;
- `KLYX_EVAL_SCENARIOS`: which scenarios, default all;
- `KLYX_EVAL_RUNS`: repetitions per scenario, default 3, at most 10.

The provider and model are the config's AI settings, read as the app reads them. Comparing models means running once per setting.

## 2. Scenarios (v1)

Each scenario is a directory under `scripts/lab/eval/<name>/`:
- `setup.sh` and `teardown.sh`: they create and remove the scenario's own objects only, on the owned cluster (§3);
- `scenario.yaml`: the anchor, the plant invariants and their witnesses, Klyx's readiness, and the evidence predicates of §5.

**Controllers are not scenario state.** E1 needs Flux's source and kustomize controllers, E3 KEDA, E4 MetalLB. These are cluster-scoped installs: CRDs, cluster roles, webhooks. A separate, explicit `scripts/lab/eval/install.sh` installs them, pinned and checksummed, only on the owned cluster. Teardown never removes them. The existing labs' scripts move into the repository for this; today they live in a session scratchpad, where a cleanup has already deleted them once.

| Scenario | Planted fault | Anchor | What Klyx can show the model | Lookalike |
|---|---|---|---|---|
| E1 `flux-crashloop` | The A1 lab at commit 4: `MODE` set to `crash`, so the container exits at start, and a `release` label changed in the same commit; Flux's health check times out | Kustomization `klyx-a1-flux/a1-web` | `git_changes`' diff of Deployment `web`, its changed paths naming `env[MODE]` (value never shown); the previous container's log | The `release` label, changed in the same object by the same commit |
| E2 `healthy-control` | None: a Deployment of `pause` that runs, created by the scenario | Deployment `klyx-eval/steady` | Ready, available, no restarts | The other scenarios' failures in the same cluster |
| E3 `keda-no-target` | The S9 lab's ScaledObject whose `scaleTargetRef` names a Deployment that does not exist | ScaledObject `klyx-s9-lab/no-target` | Its Ready=False condition (reason `ScaledObjectCheckFailed`), whose message names the missing Deployment; KEDA's event `deployments.apps "no-such-deployment" not found`; Klyx's KEDA verdict | `bad-auth` and `ghost-auth`: the same condition reason, failing for authentication |
| E4 `lb-pool-exhausted` | The S4 lab's pool of one address, held by `one-a`; a real MetalLB refuses `one-b` | Service `lab-s4/one-b` | MetalLB's `AllocationFailed` event: no available IPs in pool `lab-s4-one`; `one-a`'s address | `cilium-l2` and `svclb-demo`, pending in the same namespace for other reasons: a Cilium class with no Cilium, and a ServiceLB stand-in |

E3 replaces revision 0's `keda-bad-auth`: its distinguishing fact, the missing Secret, is not in KEDA's event, and A1 withholds authentication reference names by design (`chat_project.go`). E3 now asks for a cause the evidence states, beside lookalikes whose identical condition reason fails for another cause. KEDA also writes a generic event for `no-target` under the same reason ("Target resource doesn't exist"). The predicate (§5) names the detailed message, so a read holding only the generic one does not establish it.

**Plant invariants.** They are checked before each repetition and again after it, by the runner on its own connection, never through Klyx:
- **E1:** the Kustomization's attempted revision is commit 4; `status.history` records its health check failing; Deployment `web`'s newest ReplicaSet has pods in `CrashLoopBackOff` with restarts.
- **E2:** Deployment `steady` is Available, its pods Ready with no restarts.
- **E3:** `no-target` reads Ready=False, reason `ScaledObjectCheckFailed`; Deployment `no-such-deployment` does not exist.
- **E4:** `one-a` holds the pool's one address; `one-b` is pending, with an `AllocationFailed` event seen within the hour; `cilium-l2` and `svclb-demo` are pending.

**What "the plant held" means.** These are boundary checks, before and after the run, plus identity witnesses recorded at the first check and compared at the second:
- the anchor's UID and the UIDs of the scenario's other subjects;
- E1's crashing ReplicaSet UID and the Kustomization's attempted revision;
- `one-a`'s address.

A change that comes and goes between the two checks, leaving every witness unchanged, is not detected, and the report says that this is the drift coverage. Ordinary churn, such as a new `resourceVersion` or a repeated event's count, is not drift.

**Klyx's readiness, separate from the lab's.** After the plant check passes, the runner waits for the anchor's Observer row to be live, through the app's own `HealthRowsNow`, with an `EvaluatedAt` after the plant check. The allowance is 120 s for the first repetition after start and 60 s after that, polled with backoff.
- If the row is ready in time, the run starts.
- If not, the run is `sut-not-ready` (§4), with the last reason seen: no Observer, the Observer ended, no row for the anchor, or a row older than the plant check. The plant was correct, so this is on Klyx's side. The reason is recorded and no root cause is inferred: a source behind the row can also be unavailable while the plant check succeeds.

## 3. Lab only

A context named `kind-*` and a namespace label identify nothing: a context is an alias, and a label does not bound what A1 reads. The boundary is the exact connection and every evidence source behind it.

**The preflight, in `init()`, before any connection starts.** It refuses to run unless all of these hold:
- **One cluster.** `KLYX_CONFIG` names exactly one, whose kubeconfig is the eval's own file, not the owner's.
- **The endpoint is the lab's.** Its context's API server equals the one kind records for the lab cluster (`kind get kubeconfig`), and that address is loopback.
- **The cluster is owned.** A `klyx-eval-owner` ConfigMap in `kube-system` holds a token equal to the eval's local state file. This is `scripts/lab/klyx-parity.sh`'s `owned()` check, reused.
- **Every evidence source is the lab's.** Alertmanager, Loki and Prometheus are service references in the lab cluster, never explicit endpoints. The approved Git repositories are exactly the scenarios' declared fixture repositories.

The fleet, the runner's plant checks and the investigation all use the connection the preflight resolved.

**E1's repository is the one source outside the cluster.** Klyx runs Git only over https and ssh (`gitcli.ValidateApprovedURL`), and Flux must fetch the same history. So E1 keeps a public fixture repository holding only the lab's manifests, fetched from GitHub by both Flux and Klyx. Today that is the A1 lab's `klyx-lab-a1`. Keeping it, or moving the fixture to a new repository, is the owner's decision before stage 1. The repository's commits are pinned in `scenario.yaml`, and a force-push changes the plant, so the run is `not-ready`.

`setup.sh`, `install.sh` and `teardown.sh` run the same ownership check before any write.

**What still crosses.** The investigation's egress rules apply unchanged: what a scenario sends is what a real investigation would send. The destination is the provider the owner configured; sending the lab's data there is the point. The key is read as the app reads it; the runner writes no key.

**Cost.** Before the first call, it prints the upper bound of model calls: scenarios × repetitions × 12. A real-model run is the owner's to start.

**Refusal tests:**
- a `kind-` context pointing elsewhere;
- a loopback endpoint that is not kind's record;
- a missing or foreign ownership marker;
- an explicit external Alertmanager, Loki or Prometheus endpoint;
- an approved repository outside the scenario set, or a fixture repository whose pinned commits are missing;
- a second cluster.

## 4. Statuses

**Every run gets one status first; only `completed` runs are scored.**

| Status | When | Whose |
|---|---|---|
| `not-ready` | The runner's own plant check fails before the run | the lab's |
| `sut-not-ready` | The plant check passes, but Klyx's anchor row is not ready within the allowance (§2) | Klyx's, with the reason seen |
| `drift` | A plant invariant or a witness changed during the run | the lab's |
| `sut-failure` | Klyx failed before the model could answer: the investigation did not start, or its anchor was not read | Klyx's |
| `model-failure` | The exchange ended `failed` or `cancelled` for a provider error, a deadline or a budget | the model's or the provider's |
| `report-failure` | The exchange was `answered`, but the model never submitted a report Klyx accepted (below) | the model's |
| `completed` | The exchange was `answered` with an accepted report, and the plant held | |

A1 keeps a report when a turn fails, and turns an answered turn without a report into an empty one with Klyx's missing evidence (`chat_service.go`, `chat_invsnapshot.go`). Both are right for the operator. For the eval, neither is the model's answer. **An accepted report** is, from the recorder (§5), never from the note's text:
- **tools mode:** a `report_findings` call that Klyx accepted. A call Klyx rejected, and a turn that ends with no accepted call, are not one;
- **snapshot mode:** a findings block that parsed. No block, or a block that did not parse, is not one.

An accepted report with no findings is an explicit empty report, and counts as one.

**The report shows every denominator: requested, completed, and each other status with its count.**
- A scenario with no completed runs has no result. It is never a success.
- `sut-not-ready` and `sut-failure` are Klyx's and stay visible as Klyx's regressions, never dropped from a comparison as "not run".

## 5. The delivered-evidence record

The structural marks need to know what the model actually received, after projection, scrubbing and caps. A1's ledger cannot say: a read is metadata and checks, an event check aggregates by reason without the message, and a log read does not record whether it was the previous container. A fresh lab read proves what the fixture holds now, not what the model was given.

**The recorder.** An eval-only, in-memory observer inside `internal/appbridge`, compiled only with `-tags eval`. In other builds its hooks are empty functions with no state. It is scoped to one conversation: a cleared investigation's facts never reach a later repetition. It sees:
- **each read requested or planned:** its tool, arguments and subject, and its outcome, including a failure or a fence refusal;
- **each tool result as delivered:** the final envelope, after projection, `ScrubEvidence` and the result cap. A result counts as delivered only once a model request carrying it is actually sent to the provider. A request refused locally, for its size or by `guard()`, is not a send;
- **each snapshot section as delivered:** the sections the 96 KiB fill kept, once the snapshot's request is sent. The sections it left out are recorded as omitted;
- **each report:** a `report_findings` call accepted or rejected, and a snapshot block found, parsed or not.

A read carried again in later requests of the turn counts once.

**What it evaluates.** The runner registers the scenario's predicates before the run. Each predicate names the reads that can deliver it: a tool and a subject, the anchor's own read among them where it holds the evidence (E3's condition). It is evaluated on the delivered envelope's own JSON, at the moment of delivery:
- **`event`:** an event about the subject, by UID, whose message matches the scenario's pattern. E3: `deployments.apps "no-such-deployment" not found`. E4: `AllocationFailed`, no available IPs, about `one-b`.
- **`condition`:** the subject's condition with the given type, status and reason, whose message matches. E3: Ready=False, `ScaledObjectCheckFailed`, naming `no-such-deployment`.
- **`log`:** a `get_pod_logs` result for a pod in the plant's recorded set (E1: `web`'s pods of the crashing ReplicaSet, by UID), read as the previous container, with at least one delivered line.
- **`diff`:** a `git_changes` result listing Deployment `klyx-a1-app/web` with a changed path matching `env[MODE]`, shown or a "changed (not shown)" marker.

**What it keeps, per predicate:**
- the predicate's ID, and whether it was established;
- the read ID that established it, or the reason it was not (below);
- the checks bound to it: a check is bound only when its own statement satisfies the predicate, on the same subject. Most cannot be: an event check aggregates a reason without the message, and a condition check names its reason, not its message. The association then stays at the read;
- the subject's identity;
- flags: previous instance, pattern matched, result capped.

It keeps no tool body, no message, no log line and no hidden value; at most 64 entries per run. Nothing in A1's ledger, labels or results changes.

**Not established, and why.** Each predicate that was not established gets one reason, from the recorder's own record:

| Reason | When | Whose |
|---|---|---|
| `not-requested` | Tools mode: the model never asked for any read that can deliver it | the model's acquisition choice |
| `not-planned` | Snapshot mode: Klyx's plan has no step that reads it | Klyx's plan |
| `read-failed` | The read was asked for or planned, and failed or was refused | not assigned: the read's own detail is shown |
| `not-sent` | The result was acquired, but the request carrying it was refused locally, or the snapshot fill left the section out | Klyx's coverage gap |
| `turn-ended` | The result was acquired, but the turn ended before any request carried it | not assigned: the turn's outcome is shown |
| `capped` | The result was clipped by its cap, and the predicate did not match what remained | Klyx's coverage gap, "possibly removed by the cap" |
| `no-match` | The result was delivered whole, and did not establish the predicate | not assigned: no reason is assumed |

A completed run with no established predicate cannot earn the grounded-cause mark. It stays in the completed denominator when its report was accepted, and its cause statements are still adjudicated. Only `not-planned`, `not-sent` and `capped` are reported as Klyx coverage gaps.

## 6. Scoring

**The automatic marks, for a completed run, are structural.** Each is named for what it verifies.

| Mark | Verifies |
|---|---|
| **planted evidence delivered** | Per predicate: established, or the reason it was not (§5) |
| **grounded cause** | A finding of type cause, labelled suspected by Klyx, about one of the scenario's subjects, that cites the read which established a predicate, or a check bound to it. Citing another check of that read is not enough. |
| **cited a lookalike** | A cause finding citing only a lookalike's evidence: E3's `bad-auth` or `ghost-auth` reads, E4's `cilium-l2` or `svclb-demo` reads. E1's lookalike shares the planted object and its diff, so it cannot be told apart by citation and is left to adjudication. |
| **unsupported causes** | The count of cause findings Klyx labelled unsupported: their support is missing, which does not make them false |
| **unsupported findings** | The count of all findings Klyx labelled unsupported |
| **early evidence** | The count of findings whose only evidence was observed before the failure was reported. It is context, not an error: E1's diff precedes its failure by design. |
| **explicit empty report** | The model's accepted report held no findings |

**The control passes only when:**
- the run is completed, so its report was accepted;
- the window reads "no failure to date";
- the model reported no cause finding at all, whatever Klyx's label. An unsupported cause still fails the control.

**Lexical proxies, reported as proxies, never as accuracy.** A cause statement matching the scenario's patterns, such as `\bMODE\b` for E1, or mentioning the lookalike. A match says nothing about negation: "MODE was correct; a registry outage caused it" matches. A correct answer that rejects the lookalike still mentions it. The same holds for proof words in the prose: "no cause is proven" contains one.

**Adjudication.** The report lists each completed run's cause findings: statement, Klyx's label, cited evidence and notes. The owner marks each one right, wrong or partial. "Diagnosis accuracy" exists only once that is done; until then the report shows the structural marks and the proxies, and says the statements are unadjudicated.

**The scorer's own tests.** A fixed corpus of synthetic exchanges, through the real A1 labeller and the recorder:
- **cause statements:** an expected cause stated as negated; a correct diagnosis that rejects the lookalike; an answer that says "no cause is proven";
- **citations:** an irrelevant diff of the planted object; a cause citing a failed read; a cause citing a real but unrelated read ID;
- **delivery:** two events under one reason, only one with the planted message; a current-container log beside a previous one; a planted item dropped by the result cap; an omitted snapshot section; a request refused locally for its size;
- **acquisition:** a model that reports at once without reading the planted evidence (`not-requested`); a requested read whose valid result does not match (`no-match`); a failed read (`read-failed`);
- **binding:** a cause citing an unrelated check about the same subject, in the read that established the predicate;
- **reports:** a tools turn with no accepted report; a snapshot answer with no block; a malformed block; a valid empty report in the control;
- **control:** a failed turn whose empty report would satisfy it; an unsupported cause in it.

Each gets the status and the marks these sections define.

## 7. The report

Written to `KLYX_EVAL_OUT` with private permissions (0600): `report.json` and `report.md`.

- **Provenance:**
  - the build's commit and whether it was clean;
  - the scorer and scenario-manifest versions;
  - the fixtures' revisions, witnesses and pinned UIDs;
  - the provider, model and the investigation mode actually used, including a fallback from tools to snapshot.
- **Per run:**
  - the status, and for `sut-not-ready` the reason seen;
  - the recorder's entries, the marks and the proxies;
  - the findings as Klyx labelled them, and Klyx's missing evidence;
  - model calls, reads and duration.
- **Per scenario:** the denominators, each mark's rate over completed runs, and the adjudication column.
- **Never included:**
  - keys;
  - the config's contents;
  - raw provider requests and continuations;
  - raw tool results.

  Enough is kept to adjudicate the cause statements, not a transcript.
- **Bounds:** 10 repetitions per scenario, and 2 MiB per report. A report that reaches the cap says what it left out.

## 8. Modes

Tools mode is the primary measurement. A provider without tools runs A1's snapshot plan, which reads each scenario's planted evidence:
- for E1: the unit, `git_changes` and the logs;
- for E2: the anchor;
- for E3 and E4: the anchor, its events and its dependencies.

The report records the mode, and a fallback, per run. A predicate the run did not establish shows with its reason (§5), never as a wrong diagnosis. A skipped step has a different owner in each mode: in tools mode the model chose its reads, in snapshot mode Klyx's plan did.

## 9. The scripted model

`fake-model.mjs`, the A1 lab's scripted OpenAI-compatible model, tests the harness end to end with no key. It plays:
- a grounded E1 run;
- a run citing only a lookalike;
- a negated cause;
- an answer with no accepted report, and a malformed snapshot block;
- an empty report followed by a failure;
- an unsupported cause in the control;
- an answer saying no cause is proven;
- a report at once, citing only the anchor's read, without the planted evidence;
- a requested read whose result does not establish the predicate.

With fixtures broken on purpose, it also produces the other statuses:
- `not-ready`: a broken plant;
- `sut-not-ready`: a correct plant with the Observer held back;
- `drift`: a witness changed mid-run;
- `sut-failure`: an anchor that cannot be read;
- `completed`: a successful start, as the control case.

These runs can go in the gate. Real-model runs are the owner's.

## 10. Stages

1. **The preflight, with its refusal tests.** Then `install.sh`, and the scenario scripts in `scripts/lab/eval/`. Then the recorder, the `eval` hook and runner with the statuses and the report, E1 and E2, the scorer's corpus and the scripted model's runs.
2. **E3 and E4.**
3. **The owner's first real run.** Its report stays outside the repository; a short as-built section is added here.

Each stage is gated as A1's were. This revision goes to Codex before stage 1.

## 11. Revision 1: Codex's review of 9024d06

Codex requested changes (handoff c220ff50), four P2 findings, all taken:

1. **The lab guard did not identify the cluster or its evidence sources.** §3 now has a preflight in `init()`, before any connection starts. It requires one cluster on kind's recorded loopback endpoint with a parity-style ownership marker, and in-cluster evidence sources only. It refuses anything else, with tests. Controller installs are named as cluster-scoped and separated from scenario setup (§2).
2. **A keyword and an evidence class did not verify a diagnosis.** The automatic marks (§6) are now structural: the cause must cite the planted check or read itself, and a lookalike is detected by citation where it can be. Keyword matches are reported as proxies. Diagnosis accuracy comes only from the owner's adjudication. The scorer has its own negative corpus.
3. **Failed or invented runs could pass.** Every run gets a status first (§4), and only completed runs are scored. Every denominator is shown, and no completed run means no result. The control fails on any cause finding, whatever its label. Plant invariants and Klyx's readiness are checked before and after each repetition, with the anchor's UID pinned.
4. **E3's evidence was not observable.** `keda-bad-auth` is replaced by `keda-no-target`, whose cause KEDA's event states, beside two lookalikes with the same condition reason. The actual mode is recorded, and an unreachable evidence path is a Klyx failure, not a wrong diagnosis.

## 12. Revision 2: Codex's re-review of 18df7c7

Codex requested changes (handoff 073bfb08), three P2 findings, all taken:

1. **A missing or malformed report passed the control.** An answered turn now needs an accepted report to be `completed`: an accepted `report_findings` call, or a snapshot block that parsed, as the recorder sees it, never inferred from the note's text. Otherwise the run is `report-failure`, the model's, in the denominators (§4). An explicit empty report stays valid. The corpus covers each path (§6).
2. **The planted-evidence predicates needed a record A1 does not keep.** An eval-only, in-memory recorder (§5) evaluates the scenario's predicates on each result as delivered: after projection, scrubbing and caps, and only once a model call carries it, or the snapshot fill keeps it. It keeps IDs, identities and flags, never bodies. A1's ledger is unchanged. Evidence never delivered, or capped away, is "not delivered", a Klyx coverage gap, never a model miss. The corpus covers the message, previous-log, cap and omission cases.
3. **An Observer regression was blamed on the lab.** Fixture readiness and Klyx's readiness are now separate (§2). A correct plant whose Klyx row is not ready within a bounded allowance is `sut-not-ready`, Klyx's, with the reason seen and no inferred cause. The scripted model covers it beside a broken plant and a successful start.

Also from the guidance:
- "invented causes" is now "unsupported causes";
- "no findings" is now "explicit empty report";
- drift coverage is stated as boundary checks with identity witnesses, and ordinary churn is not drift (§2);
- a capped report says what it left out (§7).

## 13. Revision 3: Codex's re-review of 76ad5e8

Codex found the three revision 1 findings resolved, and requested changes for one P2 (handoff a5aa523f), taken:

1. **A model that never asked for the planted evidence was reported as a Klyx coverage gap.** Each predicate not established now gets one reason from the recorder (§5): `not-requested`, `not-planned`, `read-failed`, `not-sent`, `turn-ended`, `capped` or `no-match`. Only `not-planned`, `not-sent` and `capped` are Klyx coverage gaps. A `not-requested` predicate is the model's acquisition choice: the run stays completed and adjudicated, and cannot earn the grounded-cause mark. The corpus and the scripted model add the no-request case and a requested read that does not match (§6, §9).

Also from the guidance:
- a check is bound to a predicate only when its own statement satisfies it, otherwise the association stays at the read, and the corpus has a same-subject unrelated-check case;
- delivery is the request actually sent: a request refused locally is not a send, and a read carried again counts once;
- the recorder is scoped to one conversation.

## 14. ACCEPT (2026-10-24)

Codex accepted the design at 3f04d6f (handoff 64c8fe8f), with no remaining findings, after the review and three re-reviews. For the build, it asks that the corpus check how reasons reduce over several attempts: a later delivered match establishes a predicate, and an earlier failure or a local refusal does not override it. A refused request that is retried and sent is not `not-sent`. The decisions stay deterministic, within the 64-entry bound.

Two decisions stay the owner's: E1's public fixture repository, before stage 1, and every real-model run.

## 15. As built: stage 1 (2026-10-24)

**What was built, on `feat/ai-eval`:**
- `internal/evalrun/record` (87d55b5): the delivered-evidence record (§5). Predicates are evaluated on each final envelope. Bound checks are kept, and each unestablished predicate gets its reason. Reads are ordered by read ID, so a batch's arrival order decides nothing.
- Chat's hooks (0828f3b): the seed read, each dispatched or planned read, each tools request sent or refused for its size, the snapshot fill and send, and each report. `SetEvalRecorder` and `EvalAnchorReady` exist only in the eval build.
- `internal/evalrun` (20839df, 7389a90):
  - the preflight (§3);
  - scenario manifests read strictly;
  - plant checks on the runner's own connection;
  - the statuses (§4), the scorer (§6) and the report (§7).
- `cmd/klyx/eval.go` (073bd38): the eval build's `init` and the hand-off from `main`.
- `scripts/lab/klyx-eval.sh` and `scripts/lab/eval/` (8f4bc8e): init, install, setup, teardown, run and selftest; E1 and E2; the scripted model; the self-test's scenarios and checker.

**Evidence:**
- The self-test passes 15 of 15 cases on the kind lab, from a clean build at 7919205. It produces every status but `sut-failure`, and each mark the design names, in tools and snapshot modes.
- The race suite passes in CI's scope (66 packages), and again with `-tags eval` for appbridge and evalrun. Lint is clean in both variants.
- A mutation pass catches 36 of 36. It mutates the record's reasons and bindings, the statuses, the scorer's marks, the plant checks, the preflight's refusals and the runner's readiness and anchor pin. Its two survivors were closed with tests (7919205).

### 15.1 Deviations, for review

1. **`sut-failure` is not produced on the lab.** The unit tests cover it: a refused start, an unread anchor, and an anchor read under another UID. No lab fixture makes Klyx fail to read an anchor that the runner's own read finds. A replaced anchor is `drift`.
2. **The statuses' precedence, which §4 did not state:** `not-ready`, `sut-not-ready`, `drift`, `sut-failure`, `model-failure`, `report-failure`, `completed`. Drift comes before Klyx's and the model's failures: a replaced anchor fails the read, and the lab caused it.
3. **Klyx's readiness also needs a connection chat accepts** (Synced, Degraded or Stale). The first self-test found chat refusing ("the cluster is not connected") seconds after start, while the Observer was already live.
4. **E1's plant reads `status.history`, not the Ready condition.** Flux sets Ready to Unknown while it retries the health check, which made the first self-test's E1 `not-ready`.
5. **E1 is the A1 lab:** its namespaces, and Flux's controllers (`scripts/lab/eval/flux-controllers.yaml`, the A1 lab's export, pinned by digest).
   - `install` requires Flux's CRDs to be present and does not install them.
   - Setup pins the fixture repository by commit: the steady commit applied, then the crash commit attempted.
6. **The eval binary never writes to the cluster.** `klyx-eval.sh` does, after `owned()`.
7. **The hooks exist in every build** as empty methods on a zero-size field (`evalHook struct{}`). The eval build gives them their bodies.
8. **When a request counts as sent:**
   - tools mode: when the provider's call returns without an error; a failed call is not marked sent;
   - snapshot mode: when its one call returns.

   The run is a `model-failure` either way.
9. **The self-test's `sut-not-ready` case** comes from an anchor Klyx's Observer does not classify (a ConfigMap), which is deterministic. A stalled connection reaches the same status through the readiness check (deviation 3).
10. **The scripted model picks its script by the configured model name.** The self-test writes one fleet config per case into the eval directory.
11. **A lookalike is matched by the cited read's references** (resource, namespace and name).

### 15.2 Not done yet

- Stage 2, E3 and E4: the S9 and S4 labs move into `scripts/lab/eval/`.
- The owner's two decisions: E1's fixture repository, and the first real-model run.
- Counting A1.1's change check, naming `env[MODE]`, as E1 evidence beside the Git diff, once A1.1 is merged: an addendum here.

### 15.3 Codex's implementation review 1 and the fixes (2026-10-24)

Codex requested changes at aeeb39e (handoff 607ac265): nine P2 findings and one P3, each with a counterexample probe. It accepted deviations 1 to 7 and 9 to 11, and asked for deviation 8 to change (finding 7). All are taken:

1. **The fleet re-read the config and re-resolved the kubeconfig.** Main now runs on the configuration the preflight checked. The fleet's connections come from `fleet.ResolvingConnFactory` with the preflight's REST config, and any other cluster is refused. The preflight digests both files, and checks them again after resolving. The runner checks them before every repetition, and a change stops the eval: that run is `not-ready`, the report says it stopped, and later runs are not made.
2. **Teardown could delete what was never the eval's.** Every scenario object now carries the eval directory's owner label (`klyx.dev/eval-owner`, a digest of its token, never the token).
   - Setup refuses an existing object without it.
   - The A1 lab's objects (`klyx.dev/lab=a1`) are adopted only on an explicit `KLYX_EVAL_ADOPT=a1`.
   - Teardown deletes only labelled objects and says what it left alone.
   - A test runs the real scripts against stand-in `kind` and `kubectl`.
3. **A replaced ReplicaSet was not drift.** The plant records each predicate's resolved identities as witnesses: the subject's UID, the newest ReplicaSet's UID, and its pods' UIDs.
4. **A recovered pod held the crash plant.** A pod is crash looping when all of these hold:
   - it is not Ready;
   - a container failed at least the minimum number of times, the last within ten minutes;
   - the container is waiting, failed again, or has run for less than ten minutes since.
5. **Only the first matching read grounded a cause.** The record keeps every delivered establishing read and its bound checks, at most eight and sixteen.
6. **Unsent results were reported as `no-match`.** Delivery is decided apart from the match. `no-match` and `capped` are reasons only for delivered results; a result never sent is `not-sent` or `turn-ended`, with `capped` kept as a flag. The record also flags a matching result that was never sent.
7. **A provider error after receipt counted as never sent.** The hooks pass the provider call's error:
   - a size refusal is `Refused`;
   - an answer, or an HTTP status from the provider, is received;
   - a call that failed without a response is `SentUncertain`.

   The new reason `delivery-uncertain` (not assigned) says so. Both modes are tested against a 500 and a transport error. This adds a reason to §5's table.
8. **The early mark ignored read times.** It now applies A1's own rule (`labelFindings`):
   - a check is early by its observation time;
   - a whole read is early only when its historical interval ended before the failure;
   - a live read, or a read whose time is unknown, is never early.
9. **The readiness allowance did not bound the readiness read.** Every attempt runs under a context with the allowance's deadline. A read that never answers ends as `sut-not-ready`, "did not answer within" the allowance.

P3, the lookalikes: a cited check is about its own subject. A whole read is about all its references and all its checks' subjects, so a mixed read is not a lookalike's. References match by group, resource, namespace and name; check subjects by kind.

**Also found, by the self-test.** One self-test run stalled in a single case for its whole bound. Run alone, the case finished correctly. The runner's own API reads had no deadline, so they are now bounded: 30 s per request, and two minutes per plant check. The eval binary also has a watchdog: past its runs' bound, it logs every goroutine's stack and exits.
