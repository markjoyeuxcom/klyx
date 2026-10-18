# Klyx coverage S6: pods, correct

Status: design, for Codex's review before code. Roadmap: `2026-10-16-klyx-coverage-roadmap.md` §2 S6, under the shared contracts C1 (source contract) and C4 (verbs).

S6 has two parts:
- **S6a, resources and resize:** a pod's desired, allocated and actual resources; the resize conditions; the scheduler's reservation by the cluster's own rule; and pod `observedGeneration`, which means acknowledged, not completed.
- **S6b, ephemeral containers:** the existing ones shown in the pod inspector, and the debug verb with an explicit image and profile choice.

S6 adds no new watch and no new part. It changes the pods part's verdict (two new degraded rules), the scheduled-request computation behind the headroom simulation, and the Pods lens.

## 1. The API, read from the source

Read from `k8s.io/api` v0.36.2 (Klyx's pin) and v0.37.1, `k8s.io/component-helpers` v0.32.13 to v0.37.1 (the scheduler's request rule), `k8s.io/kubectl` v0.26.15 to v0.37.1 (`kubectl debug`), KEP-5067 (pod generation) and the feature-gate table.

**In-place resize.** `InPlacePodVerticalScaling` is alpha 1.27-1.32 (off by default), beta 1.33-1.34 (on) and GA from 1.35. The fields, all present in Klyx's v0.36.2 types:
- **Desired:** `spec.containers[].resources` (requests and limits), and the pod-level `spec.resources` (PodLevelResources, beta and on from 1.34).
- **Allocated:** `status.containerStatuses[].allocatedResources`, the requests the kubelet admitted. The pod-level `status.allocatedResources` is populated only under `InPlacePodLevelResourcesVerticalScaling` (alpha 1.35, beta and on from 1.36).
- **Actual:** `status.containerStatuses[].resources` (requests and limits as configured in the runtime), and the pod-level `status.resources`, under the same gate.
- **Conditions** (pod conditions; each carries its own `observedGeneration`):
  - `PodResizePending`, reason `Deferred` (feasible on this node, not now) or `Infeasible` (never on this node);
  - `PodResizeInProgress`, with reason `Error` when actuation failed.

  Both can be set at once: a new resize was requested while an earlier one is still in progress.
- **Legacy:** `status.resize` (Deferred, Infeasible, InProgress) was the 1.27-1.32 signal and is deprecated; Klyx reads it only to display it.
- **Restartable init containers (sidecars)** can be resized from 1.33; regular init containers cannot.

**The scheduler's reservation** (revision 1). The scheduler computes a pod's requests with `resourcehelper.PodRequests`, passing options from its own feature gates. From `pkg/scheduler/framework/types.go`, `(*PodInfo).CalculateResource`, in v1.35.0 and v1.37.1:
- `UseStatusResources` = `InPlacePodVerticalScaling`;
- `InPlacePodLevelResourcesVerticalScalingEnabled` = `InPlacePodLevelResourcesVerticalScaling`;
- `SkipPodLevelResources` = not `PodLevelResources`;
- 1.37 adds `UseDRANodeAllocatableResourceClaimStatus` = `DRANodeAllocatableResources`.

The helper itself changed shape across versions. Each version's `component-helpers` code was read, and the defaults come from the feature-gate table. Klyx's model per server minor, assuming each gate at its default:

| Minor | Helper | Options at default gates | Container rule | Pod-level rule |
|---|---|---|---|---|
| ≤ 1.31 | v0.31 and older | status off; no pod-level field | spec | n/a |
| 1.32 | v0.32.13 | status off; pod-level skipped (alpha) | spec | ignored |
| 1.33 | v0.33.13 | status on; pod-level skipped (alpha) | per container that reports `status.resources`: `max(spec, actual, allocated)`, or `max(actual, allocated)` when the pod's resize is Infeasible; otherwise spec. Sidecars likewise; regular init containers by spec. | ignored |
| 1.34 | v0.34.12 | status on; pod-level on | as 1.33 | `spec.resources.requests` replaces the container total, for each supported key *in spec* |
| 1.35 | v0.35.9 | status on; pod-level on; pod-level status off (alpha) | as 1.33 | as 1.34 |
| 1.36 | v0.36.5 | status on; pod-level on; pod-level status on | as 1.33 | when the pod reports `status.resources`: the effective map `max(spec, status.resources.requests, status.allocatedResources)` (spec dropped when Infeasible), written for each supported key *in spec*, so a key the effective map lacks becomes zero; otherwise as 1.34 |
| ≥ 1.37 | v0.37.1 | as 1.36, DRA node-allocatable off | per *aggregate*. Three pod totals by the init/sidecar formula: from spec; from allocated, else spec; from actual, else allocated, else spec. Infeasible drops spec from both fallbacks. Then the per-resource `max(spec, actual, allocated)`, or `max(actual, allocated)` when Infeasible. When the pod reports both `status.allocatedResources` and `status.resources.requests`, they replace the allocated and actual totals. | the effective map applied for each supported key *in the effective map* (not only spec's), when the pod reports `status.resources`; otherwise as 1.34 |

Every version then adds `spec.overhead`.

The differences are material, as Codex's real-helper probes show:
- **Opposite directions in two containers:** 8 CPU on 1.33-1.36 (`Σ max`), 5 CPU on 1.37 (`max Σ`).
- **Pod CPU 1 with pod-level actual and allocated CPU 4 and memory 2Gi, and container memory 1Gi:** CPU 4 and memory 1Gi on 1.35 and 1.36 (with the gate on), but CPU 4 and memory 2Gi on 1.37.
- **Spec CPU 1, allocated CPU 4, container `status.resources` nil:** 1 on 1.33-1.36 (the container is not counted from status), 4 on 1.37.
- **An Infeasible pod that reports no status resources:** zero on 1.37.

Klyx follows upstream exactly, including these edges.

**Pod generation** (KEP-5067; `PodObservedGenerationTracking` alpha 1.33, beta 1.34, GA 1.35):
- `metadata.generation` increments on every spec update, including the `resize` and `ephemeralcontainers` subresources and binding. Status and metadata changes do not increment it.
- The kubelet sets `status.observedGeneration` to the generation it synced. That is *acknowledged, not completed*: for resources, actuation may still be running.
- A kubelet that predates tracking (or has the gate off) leaves it 0.

**Ephemeral containers:**
- They are added through the `ephemeralcontainers` subresource, have no resources, are never restarted, and cannot be removed: they stay on the pod until it is recreated.
- Their status is in `status.ephemeralContainerStatuses`.
- `kubectl debug --target` shares the target container's process namespace.

**`kubectl debug` profiles** (`--profile`). The ephemeral-container effect of each:

| Profile | Effect on the ephemeral container (kubectl 1.30.0-1.37.1) |
|---|---|
| `general` | adds `SYS_PTRACE` |
| `baseline` | clears the security context: no added privilege |
| `restricted` | `runAsNonRoot` (no numeric user is chosen), all capabilities dropped, `allowPrivilegeEscalation: false`, RuntimeDefault seccomp |
| `netadmin` | adds `NET_ADMIN` and `NET_RAW` |
| `sysadmin` | `privileged: true` |
| `legacy` | deprecated, to be removed in kubectl 1.39; not offered |

The effects differ by version (revision 1):
- kubectl 1.27 had all but `sysadmin`, but its `restricted` set neither `allowPrivilegeEscalation` nor seccomp; 1.28 added both.
- `sysadmin` arrived in 1.30.
- From 1.30.0 to 1.37.1 the ephemeral branch of every profile, and the helpers it calls (`allowProcessTracing`, `clearSecurityContext`, `disallowRoot`, `dropCapabilities`, `disallowPrivilegeEscalation`, `setSeccompProfile`, `allowNetadminCapability`, `setPrivileged`), are identical. They were diffed with comments and the `pointer`→`ptr` rename stripped.

So the table holds for 1.30.0-1.37.1, and that is the range Klyx verifies (§6).

The flag's default was `legacy` through at least kubectl 1.32 and is `general` in 1.36/1.37. Klyx passes no profile today, so the privilege a debug shell gets depends on the local kubectl. S6 always passes one.

`--target` shares the target's process namespace only where the container runtime supports it. Process visibility is not promised; the confirmation says "where the runtime supports it".

## 2. S6a: the scheduled reservation

`workloads.podRequest` becomes a function of a **model**: §1's row for one minor, with its options. Klyx implements the models itself rather than importing `component-helpers`, because a module has one version in a build and Klyx needs six.

**The model, and how sure it is** (revision 1). The API server's minor does not prove the scheduler's. The version-skew policy lets kube-scheduler run one minor behind the API server during an upgrade, and with gates off their defaults. So Klyx states its model as an assumption:
- **Version read** (`snapVersion`, at connect): the model of that minor, said "assuming kube-scheduler 1.N at its default gates".
- **The alternative:** the model of the previous minor, which the skew policy allows. For each pod whose two reservations differ (only pods mid-resize, or with pod-level status), Klyx keeps the larger difference per resource.
- **Version unknown:** the per-resource maximum over every supported model, said "scheduler version unknown: the largest reservation any supported version would make". This is never the newest, optimistic rule.
- **Gates not at their default, and DRA node-allocatable claims (S11):** not modelled, and said.

**Exactness** is proven by golden values, with full provenance:
- A scratchpad generator builds one module per upstream version (v0.32.13, v0.33.13, v0.34.12, v0.35.9, v0.36.5, v0.37.1). Each imports that version unmodified and calls `PodRequests` with that row's options, recorded per minor in the golden file: `UseStatusResources`, `InPlacePodLevelResourcesVerticalScalingEnabled`, `SkipPodLevelResources`, and `UseDRANodeAllocatableResourceClaimStatus=false`.
- The golden JSON records each module version and its `go.sum` hashes.
- The shared fixture table covers:
  - upward and downward Deferred resizes;
  - Infeasible up and down;
  - in progress;
  - opposite directions in two containers;
  - sidecars and regular init containers;
  - pod-level requests with and without pod-level status;
  - partial resource-key maps (pod-level CPU only beside container memory, and status maps with keys spec lacks);
  - nil `status.resources` with allocated set;
  - allocated-only;
  - empty maps;
  - Infeasible with no status maps;
  - overhead;
  - status fields absent.
- A Klyx test asserts equality per model.

**Fallbacks** follow upstream exactly. An older kubelet usually reads as its spec, but not always: §1's edges (zero for an Infeasible pod with no status maps on 1.37) are reproduced, not smoothed.

**The consumer.** Only the headroom simulation (`fleet/headroom.go`, the Nodes lens's survivability module) reads the scheduled request today. It changes in two ways:
- **A survivor's occupancy** is each staying pod's scheduled reservation under the assumed model.
- **A displaced pod's need** is its *desired* request (spec, plus init/sidecar and overhead), because a replacement pod is admitted from its spec.

The module carries its model and confidence (revision 1):
- The caveat line names the split ("scheduled reservations on survivors, an estimate assuming kube-scheduler 1.N at its default gates; displaced pods by their desired requests").
- When the alternative model reserves more on a survivor, the module says by how much: "during an upgrade a 1.(N-1) scheduler may reserve up to +X CPU / +Y memory more on these nodes".
- A survival verdict whose spare capacity on some survivor is below that difference reads "depends on the scheduler version", never "survives".
- The per-pod tooltip says "scheduled (estimate)" or "desired".
- `SimPod` gains the desired figure and the alternative's difference.

## 3. S6a: the resize reading and the verdict

**The reading** (pure, `internal/workloads` beside the summary):
- Per resizable container (app containers and sidecars), for cpu and memory, requests and limits:
  - **desired:** spec;
  - **allocated:** requests only; `nil` when not reported;
  - **actual:** requests and limits; `nil` when not reported;
  - a `differs` flag over the reported values.
- Pod-level likewise when `spec.resources` is set.
- The resize conditions: type, reason, message, `lastTransitionTime` and the condition's `observedGeneration`.
- The legacy `status.resize` value, if set.
- Each container's `resizePolicy`, from the detail read (`NotRequired` / `RestartContainer`).

**The verdict** (`health.Pod`, revision 1). Two rules are added after the existing ones.

They apply only to a running pod: phase Running, no `deletionTimestamp`, and the existing verdict OK. A terminal pod keeps its own verdict ("Completed" is OK for Succeeded, and resize has no meaning there), a CrashLoop stays a CrashLoop, and Pending or Not ready keep their rule.

A resize condition is *active* only with status True. The kubelet always writes True, and any other status is shown in the inspector and never classified.

Each condition's generation means something different, and the rules keep the difference (the resize task page, and the kubelet's `status_manager.go` in v1.37.1):
- **`PodResizePending`'s** `observedGeneration` is the generation of the allocation *last attempted*. The kubelet rewrites it on every attempt.
- **`PodResizeInProgress`'s** is the generation at which the *ongoing* actuation started. The kubelet keeps it until that operation clears, so an older value is still current.

The rules:
- **`pod.resize-infeasible`:** active `PodResizePending` with reason Infeasible, about the current request. **Degraded**, "Resize infeasible", with the condition's message: the desired resources can never be granted on this node, and the pod runs on its old ones. "About the current request" means one of:
  - the condition's `observedGeneration` equals `metadata.generation`;
  - both it and the pod's `status.observedGeneration` are 0 (a kubelet that does not track generations). The verdict then says "for the latest request the kubelet reported; it does not report which generation".
  
  With a condition generation *below* the pod's generation, the evidence is about an earlier request, and the current one is not yet evaluated. There is no verdict change: a Note says "an earlier resize (generation G) was infeasible; generation N is not yet evaluated", and the Syncing presentation (§4) applies.
- **`pod.resize-error`:** active `PodResizeInProgress` with reason Error, whatever its generation, because the ongoing operation is current by construction. **Degraded**, "Resize error", with the message and "started at generation G" (or "generation not reported" for 0).

Both can hold at once: an older active InProgress Error beside a newer Pending is degraded for the error, and the Pending is a Note.

Neither rule has a clock. Both are look-at entries in Needs you, never faults, so they raise no OS notification (faults only, unchanged).

A Deferred resize and a resize in progress without error change no verdict. They are a **Note** on an OK pod ("resize deferred: …", "resizing") and appear in the reading.

## 4. S6a: acknowledgement, not completion

**A pod row is Syncing** when all three hold:
- `metadata.generation > 0`;
- `status.observedGeneration > 0`;
- `generation > observedGeneration`.

It draws the dashed syncing mark and the "Syncing… generation N, observed M" text of `staleness.tsx`, only where the verdict would otherwise draw no mark. A lag never hides a fault.

**Acknowledged is not completed.** With `observedGeneration == generation`, a remaining `PodResizeInProgress` or `PodResizePending` is still shown, as a Note in the row and as the conditions in the inspector. Equality never means "resized".

**`observedGeneration == 0`** is a kubelet that does not report it: no Syncing mark, and the inspector says "the kubelet does not report observedGeneration; whether it acknowledged generation N is not known". It is never stuck Syncing.

The pod DTO gains `observedGeneration`, and the row gains the resize summary (pending reason, in progress, error) and the scheduled cpu and memory.

## 5. S6b: ephemeral containers

**The pod inspector's "Debug containers" block**, from the detail read (a GET: the projection does not keep `spec.ephemeralContainers`):
- each container's name, image and target;
- its profile evidence from its security context: privileged, capabilities added, non-root;
- its state: running since, waiting with reason, or terminated with reason, exit code and finish time;
- at most 20, newest last, with "+N more".

The block says that a debug container's failure is diagnostic evidence, not the application's failure. `health.Pod` already scans only init and app statuses, and a regression test keeps an exited ephemeral container from changing the verdict.

## 6. S6b: the debug verb (C4)

| | |
|---|---|
| **What** | `kubectl debug -it <pod> --target=<container> --image=<image> --profile=<profile> -- sh`, in a terminal, as today. "Copy command" stays ungated and carries the same image and profile. |
| **Image** | A preset (`busybox:1.36`, the default) or a custom reference, validated as an OCI reference (registry, path, tag or digest; no whitespace; at most 255 characters). The argv is passed without a shell, as today. |
| **Profile** | Always explicit. The choices and what each adds are §1's table. The default is `general`, kubectl's own current default, named "adds SYS_PTRACE, for inspecting the target's processes where the runtime supports --target". |
| **Confirmation** | ConfirmDialog names the image, the profile and its added privilege. `sysadmin` takes the danger shape (hazard tape and the typed interlock): "privileged: this container has full access to the node". On protected clusters the ProtectGuard grant is consumed, as today. |
| **Precondition** | The pod UID the user saw (`CheckPodArmed`), as today. The residual same-name window is said, as today. |
| **Consequence** | "The debug container stays on the pod until the pod is recreated; Git and its controller do not remove it, and adding it increments the pod's generation." |
| **kubectl** | (revision 1) Before launching, Klyx reads the client version of the *same resolved kubectl binary* the terminal command runs (`kubectl version --client -o json`). Below 1.30.0, or unreadable: Klyx refuses with that reason and launches nothing, because the profile effects are verified for 1.30.0-1.37.1. Newer than 1.37: allowed, and the confirmation says "profile effects verified for kubectl 1.30-1.37; this is 1.N". |
| **Image compatibility** | `restricted` sets `runAsNonRoot` without a numeric user, so an image that runs as root fails to start unless the pod sets `runAsUser`. The confirmation says so when `restricted` is chosen. |

The verb authors no desired state. It is the existing day-2 debug escape hatch, now with its privilege named.

## 7. The source contract (C1) and the budget

**The pods part** (existing): an Observer watch projected by `ProjectPod`. The projection keeps:
- the complete status: `observedGeneration`, the conditions, container `allocatedResources` and `resources`, and the pod-level maps;
- spec container resources, pod-level resources and overhead.

That is already enough for §2-§4, so the projection does not change. A raw/projected equality test covers every field the new rules read.

| | |
|---|---|
| **Counted entity** | The pod UID, unchanged. |
| **Dependency completeness** | Nothing new. The server version is read at connect; an unread version selects the newest rule and says so. |
| **Clock** | None new: the two rules are status-driven. |
| **Notifications** | Unchanged. Faults only, so the new degraded rules do not notify. |
| **The detail read** | The Pods inspector's GET, as today, now also mapping `ephemeralContainers`, `ephemeralContainerStatuses`, `resizePolicy` and the per-container status resources. |

**Budget:** no new watches, no new retained model, no projection growth. The summary computes the scheduled figures per pod per evaluation, a constant cost.

## 8. What Klyx shows

**The Pods lens row:**
- the verdict (the two new degraded rules);
- the Syncing mark and text (§4);
- a Note for a deferred resize or one in progress.

**The Pods inspector:**
- **"Resources",** per container, cpu and memory:
  - desired, then allocated and actual where reported;
  - differences marked;
  - its resize policy;
  - pod-level resources when set.

  Then:
  - the resize conditions with reason, message, time and generation;
  - the generation acknowledgement line;
  - the scheduled reservation, naming its rule ("the scheduler's reservation, Kubernetes 1.37 rule: …");
  - the limits (§2).

  Rightsizing hints keep comparing usage with the desired (spec) request, labelled "desired".
- **"Debug containers"** (§5).
- **Debug shell** (§6) in More, with the image and profile pickers.

## 9. Tests, lab, parity, smoke

**Unit:**
- The golden per-version request table (§2).
- `health.Pod`:
  - both new rules;
  - their precedence under a fault, under Not ready, and on terminal pods (Succeeded, Failed, terminating);
  - an old Pending Infeasible beside a newer desired generation: a Note, not a verdict;
  - an older active InProgress Error beside a newer Pending: degraded for the error;
  - generation 0 on both;
  - non-True conditions ignored;
  - Deferred and in progress as Notes;
  - an exited ephemeral container leaving the verdict unchanged.
- The headroom model:
  - the assumed and alternative models;
  - version unknown as the per-resource maximum;
  - the "depends on the scheduler version" verdict.
- The kubectl gate: 1.29 refused, 1.30.0 accepted, 1.38 qualified, unreadable refused, and the binary the terminal runs.
- Syncing:
  - lag;
  - equality with a remaining resize;
  - `observedGeneration` 0.
- Raw/projected equality for the resize fields.
- The headroom split: occupancy scheduled, need desired.
- The debug argv:
  - every profile;
  - image validation;
  - the kubectl version gate, including unreadable;
  - sysadmin's interlock in the frontend.

**The lab** (`lab-s6`, kind-klyx v1.37, real kubelet), namespace `klyx-resize-lab`:
- `resize-done`: cpu raised through `kubectl patch --subresource resize`, and completes.
- `resize-infeasible`: cpu raised past the node's allocatable, so `PodResizePending` Infeasible.
- `resize-deferred`: memory raised past what is free now but within allocatable, so Deferred. The target is computed at apply time from the node's allocatable minus scheduled requests.
- `resize-mixed`: two containers, one up and one down, left Deferred. It shows the 1.37 aggregate rule on the live cluster.
- `debug-target`: its pod sets `securityContext.runAsUser: 65534`, so `restricted` (non-root without a numeric user) can start busybox. Two ephemeral containers are added with `kubectl debug` (no `-it`):
  - `--profile=general -- sh -c 'exit 3'` (failed);
  - `--profile=restricted -- sleep 86400` (running).

**Parity:** a resize-infeasible pod is added to `parity/fixture.sh`'s baseline on kind-klyx-parity, expected degraded. The pods part and its oracle are unchanged.

**Native smoke** (three steps, no terminal launched):
- the infeasible pod's row and its Resources block;
- `debug-target`'s Debug containers block with the exited container, and the pod still OK;
- the debug dialog with `sysadmin` chosen, naming "privileged", then cancelled.

**Captures:** night and day at 1440 and 1024.

## 10. Not in S6

- **VPA** objects and recommendations: pack material, or a later slice.
- **DRA** resources in the reservation (S11).
- **Removing** ephemeral containers: the API has no way to.
- **A resize verb:** resizing is desired state, authored in Git (or by VPA).
- **Node-level debug** (`kubectl debug node/…`) and pod-copy debug: not offered.
- **The per-node "requested" meter:** Klyx has none today, and S6 does not add one.

## 11. Revision 1: Codex's design review of 6eceec8

| # | Finding | Where answered |
|---|---|---|
| 1 | kubectl 1.27's `restricted` set neither `allowPrivilegeEscalation` nor seccomp, so the 1.27 gate launched a weaker profile than promised | §1: per-version profile effects, with 1.30.0-1.37.1 diffed identical for every ephemeral branch and helper. §6: the gate is 1.30.0 for every profile, read from the same resolved binary the terminal runs; newer than 1.37 is qualified; `restricted`'s image compatibility is said. The lab supplies `runAsUser`. |
| 2 | An earlier allocation attempt could become a current verdict | §3: conditions are active only when True, and the rules run only on running pods. Pending Infeasible counts only for the current generation (or both generations 0, qualified); an older one is a Note while Syncing applies. InProgress Error is current whatever its generation (the kubelet keeps the start generation). Terminal verdicts are untouched. §9: each case is tested. |
| 3 | The pod-level table collapsed different helper behaviour and options | §1: the scheduler's options from `CalculateResource`, and a per-minor table of helper version, options at default gates, container rule and pod-level rule. Key-set semantics: 1.36 writes spec's keys and can write zero; 1.37 writes the effective map's keys. Pod-level is skipped on 1.32-1.33. §2: golden provenance records the options per minor, plus partial-key, nil, allocated-only, empty and Infeasible-without-status fixtures. "Never zero" is retired: upstream edges are reproduced. |
| 4 | The API server's version does not establish the scheduler's model | §2: the model is an assumption, "kube-scheduler 1.N at its default gates". The previous minor's model (allowed by the skew policy) gives a per-pod difference. An unknown version takes the per-resource maximum over every model. The headroom module states the model, the difference, and "depends on the scheduler version" when the spare capacity is below it. |
| (guidance) | `--target` needs runtime support; `restricted` chooses no numeric user | §1 and §6: process visibility "where the runtime supports it"; the image-compatibility row; the lab's `runAsUser: 65534`. |
