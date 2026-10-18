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

**The scheduler's reservation** (`resourcehelper.PodRequests` with `UseStatusResources`, which follows the in-place gate) changed shape across versions. Each version's code was read:

| Server minor | Container rule | Pod-level rule |
|---|---|---|
| ≤ 1.32 | spec only (the gate is off by default; with it on, per container `max(spec, actual)`, or actual when `status.resize` is Infeasible) | spec |
| 1.33-1.34 | per container that reports `status.resources`: `max(spec, actual, allocated)`, or `max(actual, allocated)` when the pod's resize is Infeasible; otherwise spec. Sidecars included; regular init containers by spec. | spec |
| 1.35-1.36 | as 1.33-1.34 | `max(spec, status.resources.requests, status.allocatedResources)` (spec dropped when Infeasible) when the pod reports `status.resources`; otherwise spec |
| ≥ 1.37 | per *aggregate*: three pod totals are computed by the init/sidecar formula (from spec; from allocated, else spec; from actual, else allocated, else spec, with no spec fallback when Infeasible), then the per-resource `max(spec, actual, allocated)`, or `max(actual, allocated)` when Infeasible. When the pod reports both pod-level maps, they replace the allocated and actual totals. | as 1.35-1.36 |

Every version then replaces the container total by the pod-level request for each supported resource (cpu, memory, hugepages-*), and adds `spec.overhead`.

The 1.37 change is material: a pod whose container A grows while container B shrinks reserves `Σ max` per container on 1.36, and `max(Σ)` on 1.37.

**Pod generation** (KEP-5067; `PodObservedGenerationTracking` alpha 1.33, beta 1.34, GA 1.35):
- `metadata.generation` increments on every spec update, including the `resize` and `ephemeralcontainers` subresources and binding. Status and metadata changes do not increment it.
- The kubelet sets `status.observedGeneration` to the generation it synced. That is *acknowledged, not completed*: for resources, actuation may still be running.
- A kubelet that predates tracking (or has the gate off) leaves it 0.

**Ephemeral containers:**
- They are added through the `ephemeralcontainers` subresource, have no resources, are never restarted, and cannot be removed: they stay on the pod until it is recreated.
- Their status is in `status.ephemeralContainerStatuses`.
- `kubectl debug --target` shares the target container's process namespace.

**`kubectl debug` profiles** (`--profile`). The ephemeral-container effect of each:

| Profile | Effect on the ephemeral container | kubectl |
|---|---|---|
| `general` | adds `SYS_PTRACE` | 1.27+ |
| `baseline` | clears the security context: no added privilege | 1.27+ |
| `restricted` | non-root, all capabilities dropped, no privilege escalation, RuntimeDefault seccomp | 1.27+ |
| `netadmin` | adds `NET_ADMIN` and `NET_RAW` | 1.27+ |
| `sysadmin` | `privileged: true` | 1.30+ |
| `legacy` | deprecated, to be removed in kubectl 1.39 | not offered |

The flag's default was `legacy` through at least kubectl 1.32 and is `general` in 1.36/1.37. Klyx passes no profile today, so the privilege a debug shell gets depends on the local kubectl. S6 always passes one.

## 2. S6a: the scheduled reservation

`workloads.podRequest` becomes the rule of the cluster's server minor version (§1's table), selected from the version Klyx reads at connect (`snapVersion`):
- `≥ 1.37`: the aggregate rule;
- `1.35-1.36`: per container, plus pod-level status;
- `1.33-1.34`: per container;
- `≤ 1.32`: spec;
- version unknown: the newest rule, said as such.

Klyx implements the rules itself rather than importing `component-helpers`. A module has one version in a build, and Klyx must apply four.

Exactness is proven by golden values:
- A scratchpad generator imports each upstream version (v0.33.13, v0.35.9, v0.36.5, v0.37.1) as its own module and computes `PodRequests` for one shared fixture table.
- The table covers:
  - upward and downward Deferred resizes;
  - Infeasible up and down;
  - in progress;
  - opposite directions in two containers;
  - sidecars and regular init containers;
  - pod-level requests with and without pod-level status;
  - overhead;
  - status fields absent (an older kubelet).
- The JSON is committed with its provenance: module versions and go.sum hashes. A Klyx test asserts equality per version.

What is not modelled, and said in the inspector's limits:
- DRA node-allocatable claim resources (S11);
- `NonMissingContainerRequests`, a scoring default, not fit;
- scheduler feature gates that differ from the version's defaults;
- version skew between the scheduler and a kubelet.

Absent status fields fall back exactly as upstream does, so an older kubelet reads as its spec. That is never zero and never stuck.

**The consumer.** Only the headroom simulation (`fleet/headroom.go`, the Nodes lens's survivability module) reads the scheduled request today. It changes in two ways:
- **A survivor's occupancy** is each staying pod's scheduled reservation (the rule above).
- **A displaced pod's need** is its *desired* request (spec, plus init/sidecar and overhead), because a replacement pod is admitted from its spec.

The module's caveat line names both ("scheduled reservations on survivors; displaced pods by their desired requests"), and the per-pod tooltip says "scheduled" or "desired". `SimPod` gains the second figure.

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

**The verdict** (`health.Pod`). Two rules are added after the existing ones. They apply only when the existing verdict is OK, so a CrashLoop stays a CrashLoop, and Pending or Not ready keep their own rule:
- **`pod.resize-infeasible`:** `PodResizePending` with reason Infeasible. **Degraded**, "Resize infeasible", with the condition's message. The desired resources can never be granted on this node, and the pod runs on its old ones.
- **`pod.resize-error`:** `PodResizeInProgress` with reason Error. **Degraded**, "Resize error", with the message.

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
| **Profile** | Always explicit. The choices and what each adds are §1's table. The default is `general`, kubectl's own current default, named "adds SYS_PTRACE so the shell can inspect the target's processes". |
| **Confirmation** | ConfirmDialog names the image, the profile and its added privilege. `sysadmin` takes the danger shape (hazard tape and the typed interlock): "privileged: this container has full access to the node". On protected clusters the ProtectGuard grant is consumed, as today. |
| **Precondition** | The pod UID the user saw (`CheckPodArmed`), as today. The residual same-name window is said, as today. |
| **Consequence** | "The debug container stays on the pod until the pod is recreated; Git and its controller do not remove it, and adding it increments the pod's generation." |
| **kubectl** | Klyx reads the local kubectl's client version (`kubectl version --client -o json`) before launching. If it is below 1.27, or below 1.30 for `sysadmin`, Klyx refuses with that reason and launches nothing. An unreadable version refuses too. |

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
  - their precedence under a fault and under Not ready;
  - Deferred and in progress as Notes;
  - an exited ephemeral container leaving the verdict unchanged.
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
- `debug-target`: two ephemeral containers added with `kubectl debug` (no `-it`):
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
