# Klyx AI eval: scored investigations on the kind lab

Opened 2026-10-22, on `feat/ai-eval`; revision 1 on 2026-10-24 (§9). A design note for review; nothing is built.

## 0. Why

A1's last open step is the owner's real-LLM smoke (A1 §13.3): one investigation on the lab with a real model. A single run says little: models vary run to run, and what matters is how often a model names a planted cause, on the evidence that shows it, without being drawn to a lookalike. The eval turns the smoke into a repeatable measurement on known faults on the kind lab, so the owner can:

- compare providers and models before choosing one;
- see a model's habits against A-C1: claims without evidence, causes cited to unrelated objects, evidence from before the failure;
- check that a later change does not make investigations worse, Klyx's own regressions included.

The idea is the scenario benchmark of agent projects such as kubectl-ai, applied to Klyx's contract. **What it measures, and what it does not:**
- Klyx's labels are provenance, never proof (A-C1). The eval can check deterministically whether a model's cause rests on the planted evidence. It cannot check by itself that the model's prose states the right cause.
- So the automatic marks are named for what they verify. Whether a statement is the right diagnosis is the owner's call, made in the report (§5).
- The eval introduces no "proven" label into A1, and nothing it scores is shown in the app.

## 1. What it is

A headless runner inside the app binary, built only with `-tags eval`, as the native smoke is built only with `-tags smoke`. No shipped build contains it.

**Startup order.** The fleet starts connecting early in `main` (`main.go:146`), so the lab-only preflight (§3) runs in the eval build's `init()`, before `main`, on the configuration it was given. Like the smoke hook, `init` stands aside in the confined build's child (`cachediff.ChildFlag`), so `git_changes` keeps working. After `main` has wired every service, a hook takes over before any window opens. For each scenario and each repetition, it:

1. checks the plant and the app's readiness (§2);
2. resolves the anchor's UID on the investigation's own connection and pins it;
3. starts an investigation of the anchor through the real `ChatService`, with the configured provider and the default question; no scenario expectation is ever sent to the model;
4. waits for the turn to end, then checks the plant again for drift;
5. assigns the run a status and scores it (§4);
6. clears the investigation.

Reusing `main`'s wiring means the eval measures the shipped code path: the tools, projection, scrubbing, fences, budgets and labelling.

**Inputs, from the environment:**
- `KLYX_CONFIG`: the eval's own fleet config (§3), never the owner's;
- `KLYX_EVAL_OUT`: the report directory;
- `KLYX_EVAL_SCENARIOS`: which scenarios, default all;
- `KLYX_EVAL_RUNS`: repetitions per scenario, default 3, at most 10.

The provider and model are the config's AI settings, read as the app reads them. Comparing models means running once per setting.

## 2. Scenarios (v1)

Each scenario is a directory under `scripts/lab/eval/<name>/`:
- `setup.sh` and `teardown.sh`: they create and remove the scenario's own objects only, on the owned cluster (§3);
- `scenario.yaml`: the anchor, the plant invariants, the app readiness, and the expectations of §4.

**Controllers are not scenario state.** E1 needs Flux's source and kustomize controllers, E3 KEDA, E4 MetalLB. These are cluster-scoped installs: CRDs, cluster roles, webhooks. A separate, explicit `scripts/lab/eval/install.sh` installs them, pinned and checksummed, only on the owned cluster. Teardown never removes them. The existing labs' scripts move into the repository for this; today they live in a session scratchpad, where a cleanup has already deleted them once.

| Scenario | Planted fault | Anchor | What Klyx can show the model | Lookalike |
|---|---|---|---|---|
| E1 `flux-crashloop` | The A1 lab at commit 4: `MODE` set to `crash`, so the container exits at start, and a `release` label changed in the same commit; Flux's health check times out | Kustomization `klyx-a1-flux/a1-web` | `git_changes`' diff check on Deployment `web`, its changed paths naming `env[MODE]` (value never shown); the previous container's log | The `release` label, changed in the same object by the same commit |
| E2 `healthy-control` | None: a Deployment of `pause` that runs, created by the scenario | Deployment `klyx-eval/steady` | Ready, available, no restarts | The other scenarios' failures in the same cluster |
| E3 `keda-no-target` | The S9 lab's ScaledObject whose `scaleTargetRef` names a Deployment that does not exist | ScaledObject `klyx-s9-lab/no-target` | Its Ready=False condition (reason `ScaledObjectCheckFailed`); KEDA's event, whose message names `deployments.apps "no-such-deployment" not found`; Klyx's KEDA verdict | `bad-auth` and `ghost-auth`: the same condition reason, failing for authentication |
| E4 `lb-pool-exhausted` | The S4 lab's pool of one address, held by `one-a`; a real MetalLB refuses `one-b` | Service `lab-s4/one-b` | MetalLB's `AllocationFailed` event: no available IPs in pool `lab-s4-one`; `one-a`'s address | `cilium-l2` and `svclb-demo`, pending in the same namespace for other reasons: a Cilium class with no Cilium, and a ServiceLB stand-in |

E3 replaces revision 0's `keda-bad-auth`: its distinguishing fact, the missing Secret, is not in KEDA's event, and A1 withholds authentication reference names by design (`chat_project.go`). E3 now asks for a cause the evidence states, beside lookalikes whose identical condition reason fails for another cause.

**Plant invariants, checked before each repetition and again after it.** A changed invariant marks the run `drift` (§4):
- **E1:** the Kustomization's attempted revision is commit 4; `status.history` records its health check failing; Deployment `web`'s newest ReplicaSet has pods in `CrashLoopBackOff` with restarts.
- **E2:** Deployment `steady` is Available, its pods Ready with no restarts.
- **E3:** `no-target` reads Ready=False, reason `ScaledObjectCheckFailed`; Deployment `no-such-deployment` does not exist.
- **E4:** `one-a` holds the pool's one address; `one-b` is pending, with an `AllocationFailed` event seen within the hour; `cilium-l2` and `svclb-demo` are pending.

**App readiness.** The anchor's Observer row is live and was evaluated after the plant check, read through the app's own `HealthRowsNow`. A physically ready lab whose app capture is stale or incomplete is not ready.

## 3. Lab only

A context named `kind-*` and a namespace label identify nothing: a context is an alias, and a label does not bound what A1 reads. The boundary is the exact connection and every evidence source behind it.

**The preflight, in `init()`, before any connection starts.** It refuses to run unless all of these hold:
- **One cluster.** `KLYX_CONFIG` names exactly one, whose kubeconfig is the eval's own file, not the owner's.
- **The endpoint is the lab's.** Its context's API server equals the one kind records for the lab cluster (`kind get kubeconfig`), and that address is loopback.
- **The cluster is owned.** A `klyx-eval-owner` ConfigMap in `kube-system` holds a token equal to the eval's local state file. This is `scripts/lab/klyx-parity.sh`'s `owned()` check, reused.
- **Every evidence source is the lab's.** Alertmanager, Loki and Prometheus are service references in the lab cluster, never explicit endpoints. The approved Git repositories are exactly the scenarios' declared fixture repositories.

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

## 4. Statuses and scoring

**Every run gets one status first; only `completed` runs are scored.**

| Status | When |
|---|---|
| `not-ready` | The plant or the app's readiness fails before the run |
| `drift` | A plant invariant, or the anchor's UID, changed during the run |
| `sut-failure` | Klyx failed before the model could answer: the investigation did not start, or its anchor was not read |
| `model-failure` | The exchange ended `failed` or `cancelled` for a provider error, a deadline or a budget |
| `completed` | The exchange was `answered`, and the plant held throughout |

A1 keeps a report even when a turn fails, so a failed run's findings are never scored as a pass.

**The report shows every denominator: requested, completed, and each other status with its count.**
- A scenario with no completed runs has no result. It is never a success.
- `not-ready` is the lab's problem. `sut-failure` is Klyx's, and stays visible as Klyx's regression rather than disappearing from the comparison.

**The automatic marks, for a completed run, are structural.** Each is named for what it verifies.

| Mark | Verifies |
|---|---|
| **grounded cause** | A finding of type cause, labelled suspected by Klyx, about one of the scenario's subjects, that cites the planted evidence: the specific check or read `scenario.yaml` names, matched on Klyx's own record. For E1: the diff check on Deployment `web` whose changed paths include `env[MODE]`, or the read of a `web` pod's previous log. For E3: the read holding KEDA's event about `no-target` that names its target. For E4: the read holding the `AllocationFailed` event about `one-b`. |
| **cited a lookalike** | A cause finding citing only a lookalike's evidence: E3's `bad-auth` or `ghost-auth` reads, E4's `cilium-l2` or `svclb-demo` reads. E1's lookalike shares the planted object and check, so it cannot be told apart by citation and is left to adjudication. |
| **invented causes** | The count of cause findings Klyx labelled unsupported |
| **unsupported findings** | The count of all findings Klyx labelled unsupported |
| **early evidence** | The count of findings whose only evidence was observed before the failure was reported. It is context, not an error: E1's diff precedes its failure by design. |
| **no findings** | The model reported none |

**The control passes only when:**
- the run is completed;
- the window reads "no failure to date";
- the model reported no cause finding at all, whatever Klyx's label. An invented cause labelled unsupported still fails the control.

**Lexical proxies, reported as proxies, never as accuracy.** A cause statement matching the scenario's patterns, such as `\bMODE\b` for E1, or mentioning the lookalike. A match says nothing about negation: "MODE was correct; a registry outage caused it" matches. A correct answer that rejects the lookalike still mentions it. The same holds for proof words in the prose: "no cause is proven" contains one.

**Adjudication.** The report lists each completed run's cause findings: statement, Klyx's label, cited evidence and notes. The owner marks each one right, wrong or partial. "Diagnosis accuracy" exists only once that is done; until then the report shows the structural marks and the proxies, and says the statements are unadjudicated.

**The scorer's own tests.** A fixed corpus of synthetic exchanges, through the real A1 labeller:
- an expected cause stated as negated;
- a correct diagnosis that rejects the lookalike;
- an irrelevant diff of the planted object;
- a cause citing a failed read;
- a cause citing a real but unrelated read ID;
- a failed turn whose empty report would satisfy the control;
- an invented cause in the control;
- an answer that says "no cause is proven".

Each gets the mark this section defines.

## 5. The report

Written to `KLYX_EVAL_OUT` with private permissions (0600): `report.json` and `report.md`.

- **Provenance:**
  - the build's commit and whether it was clean;
  - the scorer and scenario-manifest versions;
  - the fixtures' revisions and pinned UIDs;
  - the provider, model and the investigation mode actually used, including a fallback from tools to snapshot.
- **Per run:**
  - the status;
  - the marks and the proxies;
  - the findings as Klyx labelled them, and Klyx's missing evidence;
  - model calls, reads and duration.
- **Per scenario:** the denominators, each mark's rate over completed runs, and the adjudication column.
- **Never included:**
  - keys;
  - the config's contents;
  - raw provider requests and continuations;
  - raw tool results.

  Enough is kept to adjudicate the cause statements, not a transcript.
- **Bounds:** 10 repetitions per scenario, and 2 MiB per report.

## 6. Modes

Tools mode is the primary measurement. A provider without tools runs A1's snapshot plan, which reads each scenario's planted evidence:
- for E1: the unit, `git_changes` and the logs;
- for E2: the anchor;
- for E3 and E4: the anchor, its events and its dependencies.

The report records the mode, and a fallback, per run. An evidence path the mode cannot reach makes the run `sut-failure`, never a wrong diagnosis.

## 7. The scripted model

`fake-model.mjs`, the A1 lab's scripted OpenAI-compatible model, tests the harness end to end with no key. It plays:
- a grounded E1 run;
- a run citing only a lookalike;
- a negated cause;
- an empty report followed by a failure;
- an invented cause in the control;
- an answer saying no cause is proven.

With fixtures broken on purpose, it also produces each status: not-ready, drift, sut-failure. These runs can go in the gate. Real-model runs are the owner's.

## 8. Stages

1. **The preflight, with its refusal tests.** Then `install.sh`, and the scenario scripts in `scripts/lab/eval/`. Then the `eval` hook and runner with the statuses and the report, E1 and E2, the scorer's corpus and the scripted model's runs.
2. **E3 and E4.**
3. **The owner's first real run.** Its report stays outside the repository; a short as-built section is added here.

Each stage is gated as A1's were. This revision goes to Codex before stage 1.

## 9. Revision 1: Codex's review of 9024d06

Codex requested changes (handoff c220ff50), four P2 findings, all taken:

1. **The lab guard did not identify the cluster or its evidence sources.** §3 now has a preflight in `init()`, before any connection starts. It requires one cluster on kind's recorded loopback endpoint with a parity-style ownership marker, and in-cluster evidence sources only. It refuses anything else, with tests. Controller installs are named as cluster-scoped and separated from scenario setup (§2).
2. **A keyword and an evidence class did not verify a diagnosis.** §4's automatic marks are now structural: the cause must cite the planted check or read itself, and a lookalike is detected by citation where it can be. Keyword matches are reported as proxies. Diagnosis accuracy comes only from the owner's adjudication. The scorer has its own negative corpus.
3. **Failed or invented runs could pass.** Every run gets a status first, and only completed runs are scored. Every denominator is shown, and no completed run means no result. The control fails on any cause finding, whatever its label. Plant invariants and the app's readiness are checked before and after each repetition, with the anchor's UID pinned.
4. **E3's evidence was not observable.** `keda-bad-auth` is replaced by `keda-no-target`, whose cause KEDA's event states, beside two lookalikes with the same condition reason. The actual mode is recorded, and an unreachable evidence path is a Klyx failure, not a wrong diagnosis.
