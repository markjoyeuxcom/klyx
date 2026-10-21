# Klyx AI eval: scored investigations on the kind lab

Opened 2026-10-22, on `feat/ai-eval` from `feat/ai-a1`. A design note for review; nothing is built.

## 0. Why

A1's last open step is the owner's real-LLM smoke (A1 §13.3): one investigation on the lab with a real model. A single run says little: models vary run to run, and the question that matters is how often a model names a planted cause, on the right evidence, without being drawn to a decoy. The eval turns the smoke into a repeatable measurement. Each run is scored against known faults on the kind lab, so the owner can:

- compare providers and models before choosing one;
- see a model's habits against A-C1: claims without evidence, causes cited to unrelated objects, evidence from before the failure;
- check that a later A-phase change does not make investigations worse.

The idea comes from the scenario-benchmark approach of agent projects such as kubectl-ai, applied to Klyx's own contract: Klyx's labels are deterministic, so most of the score can be too.

## 1. What it is

A headless runner inside the app binary, built only with `-tags eval`, as the native smoke is built only with `-tags smoke`. After `main` has wired every service, a hook takes over before any window opens. For each scenario in turn, it:

1. checks the scenario is ready on the lab (§3);
2. starts an investigation of its anchor through the real `ChatService`, with the configured provider;
3. waits for the turn to end;
4. scores the exchange (§4);
5. clears the investigation.

It writes a JSON report and a markdown summary. Reusing `main`'s wiring means the eval measures the shipped code path: the same tools, projection, scrubbing, fences, budgets and labelling. No shipped build contains the hook.

Inputs, all from the environment:
- `KLYX_CONFIG`: the lab's fleet config, never the owner's (as the smoke requires);
- `KLYX_EVAL_OUT`: the report directory;
- `KLYX_EVAL_SCENARIOS`: which scenarios to run, default all;
- `KLYX_EVAL_RUNS`: runs per scenario, default 3.

The provider and model are the lab config's AI settings, read as the app reads them; the report names them. Comparing models means running once per setting.

## 2. Scenarios (v1)

Each scenario is a directory under `scripts/lab/eval/<name>/` holding:
- `setup.sh` and `teardown.sh`: kind-only, labelled `klyx.dev/lab=eval`, namespaced, exposing nothing;
- `scenario.yaml`: the anchor, a readiness probe and the expectations of §4.

The A1 lab's scripts move into the repository for this; today they live in a session scratchpad.

| Scenario | Planted fault | Anchor | The cause, as Klyx can see it | Decoy |
|---|---|---|---|---|
| E1 `flux-crashloop` | The A1 lab at commit 4: `MODE` set to `crash`, so the container exits at start; Flux's health check times out | Kustomization `klyx-a1-flux/a1-web` | The `git_changes` diff of `spec.template.spec.containers[web].env[MODE]`; the previous container's log | The same commit's `release` label change |
| E2 `healthy-control` | None: a Deployment of `pause` that runs, created by the scenario | Deployment `klyx-eval/steady` | No failure to date | The other labs' failures in the same cluster |
| E3 `keda-bad-auth` | The S9 lab's ScaledObject whose TriggerAuthentication names a Secret that does not exist; KEDA reports it not Ready | ScaledObject `klyx-s9-lab/bad-auth` | Its Ready=False condition and KEDA's event naming the missing Secret | `ghost-auth`, which fails for a different reason (no TriggerAuthentication at all) |
| E4 `lb-pool-exhausted` | The S4 lab's pool of one address, held by `one-a`; a real MetalLB refuses `one-b` | Service `lab-s4/one-b` | MetalLB's `AllocationFailed` event: no available IPs in pool `lab-s4-one` | The S4 lab's other pending Services, pending for Cilium's or a stand-in's reasons |

E1 runs real controllers end to end, and its decoy is real: commit 4 changes a label as well as the environment value. E2 is the control a scored eval needs: a model that always finds a cause fails it, and the cluster around it is full of other labs' failures. E3 (a real KEDA) and E4 (a real MetalLB) test reasoning away from GitOps, each beside a lookalike that fails differently.

The readiness probe is a kubectl JSONPath condition with a deadline. For E1: the Kustomization's attempted revision is commit 4's, and `status.history` records its health check failing. A scenario that is not ready is reported as not run, never as a fail.

## 3. Lab only

- The runner refuses a cluster whose context does not start with `kind-`, and any scenario whose anchor's namespace is not labelled `klyx.dev/lab` (E2's `klyx-eval` is created labelled; the A1, S9 and S4 namespaces already are). An eval never sends a real cluster's data to a model.
- The investigation's egress rules apply unchanged. Nothing about a scenario is sent that a real investigation would not send.
- The key is the app's own, read as the app reads it. The runner writes no key and stores nothing it read from a model except the report.
- Before the first call it prints the upper bound of model calls: scenarios × runs × 12. A real-model run is the owner's to start.

## 4. Scoring

Deterministic, from the exchange the drawer would show: the findings with Klyx's labels, the ledger, the window and the answer. No model grades another in v1.

Each scenario's `scenario.yaml` states its expectations:
- `cause.subjects`: the objects the cause may be about, by kind, namespace and name;
- `cause.patterns`: regular expressions, any of which a cause's statement must match, such as `\bMODE\b` for E1;
- `evidence`: the tools or check classes a correct cause rests on, such as a `diff` check or `get_pod_logs` for E1;
- `decoys`: subjects and patterns a cause must not name;
- for a control, `control: true`.

Each run gets these marks:

| Mark | Meaning |
|---|---|
| **cause** | A finding of type cause, labelled suspected, about one of `cause.subjects`, whose statement matches one of `cause.patterns` |
| **grounded** | That finding cites a read or a check of one of the `evidence` kinds, directly or through a read citation |
| **decoy** | Any cause finding, whatever its label, matches a decoy |
| **unsupported** | The count of findings Klyx labelled unsupported |
| **early evidence** | The count of findings whose only evidence was observed before the failure was reported |
| **overclaim** | The answer's own text claims proof ("proven", "confirmed root cause", "definitely caused"), which Klyx's labels never do |
| **no findings** | The model reported none (Klyx's missing evidence still shows) |

A run passes when it has a grounded cause and no decoy. A control passes when the window reads "no failure to date" and no cause is labelled suspected. The report gives each scenario's pass rate over its runs, and every mark per run, so a pass rate never hides how a run failed.

It also records the cost the app can see: model calls, reads, the turn's duration, and the budget a turn ended on, if any. The providers' token counts are not recorded by `ChatService` today, so v1 reports none.

## 5. The scripted model

`fake-model.mjs`, the A1 lab's scripted OpenAI-compatible model, plays E1 and E2. Run against it, the eval tests itself: E1 passes with every mark as the script intends, and a variant script that names the label gets the decoy mark. That run needs no key and can go in the gate. Real-model runs are the owner's.

## 6. Not in v1

- A model grading the answer's prose.
- Scenarios across clusters (`fleet_difference`).
- Token counts and cost in currency.
- Scheduled or CI runs against real models.
- A1's snapshot mode is scored like tools mode but not compared with it separately.

## 7. Stages

1. The `eval` hook and runner, the report, E1 and E2 with their scripts moved into `scripts/lab/eval/`, and the scripted model's self-test.
2. E3 and E4, from the S9 and S4 labs.
3. The owner's first real run, its report kept outside the repository, and a short as-built section here.

Each stage is gated as A1's were, and the note goes to Codex for design review before stage 1.
