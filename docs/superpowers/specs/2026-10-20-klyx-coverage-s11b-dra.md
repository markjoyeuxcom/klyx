# Coverage S11b: dynamic resource allocation (DRA)

The roadmap's S11b (`2026-10-16-klyx-coverage-roadmap.md` §2): DRA (`resource.k8s.io/v1`), covering:
- **ResourceSlices** in all their access forms (a node name, `allNodes`, a node selector, per-device selection), with pool generations complete before a pool is read as whole;
- **ResourceClaims**, template-generated ones included, with their reservations;
- **DeviceClasses**, and pod device health (`allocatedResourcesStatus`);
- **a Pending claim** with no allocation shows the scheduler's and the driver's own evidence. It is never proof of "unsatisfiable", and Klyx does not implement a second scheduler.

Today nothing reads `resource.k8s.io`. S11a shows a node's extended requests "served by DRA" (from `status.extendedResourceClaimStatus`) and leaves the devices to S11b. ResourceSlices, ResourceClaims and DeviceClasses appear only in the API browser.

The owner has not authorised the read-only check on the ROCm homelab (C6). S11b is verified on kind, with the upstream example DRA driver and stated fixtures (§9).

## 1. The APIs, read from the source

Sources:
- Kubernetes v1.32.0, v1.33.0, v1.34.0, v1.35.0, v1.36.0 and v1.37.0;
- `k8s.io/api` v0.37.1 and `k8s.io/dynamic-resource-allocation` v0.37.1;
- kubernetes/enhancements 5b503a75 (KEP-4381, KEP-4815, KEP-4680, KEP-5004, KEP-5677);
- kubernetes-sigs/dra-example-driver v0.5.0.

The extract is kept with the lab. References are to v1.37.0 unless a version is named. `TYPES` is `staging/src/k8s.io/api/resource/v1/types.go`.

### 1.1 Versions served

- **1.34 to 1.37 serve `resource.k8s.io/v1` by default** (`pkg/controlplane/instance.go` L483). v1beta1 and v1beta2 are off-by-default betas; v1alpha3 is alpha.
- **1.32 and 1.33 serve nothing by default.** DRA is an opt-in beta there (v1beta1; 1.33 adds v1beta2).
- **v1beta1's shape differs** (a device's attributes under `basic`, flat requests). Klyx reads v1 only: a cluster serving DRA only at a beta reads "served at a version Klyx does not read", as S10 does for Karpenter's betas.
- **The API server drops alpha fields** when their gate is off, so a field's absence proves nothing.

### 1.2 ResourceSlices and pools

- **A slice** carries:
  - `spec.driver` and `spec.pool{name, generation, resourceSliceCount}`;
  - exactly one access form: `nodeName`, `nodeSelector`, `allNodes` or `perDeviceNodeSelection` (TYPES L147-190; per-device selection puts a form on each device);
  - `devices`, or, from 1.35 and exclusively, `sharedCounters`.

  A device is identified by driver, pool and device name (L76-82).
- **A pool is whole only at its highest generation, with every slice present.** The types say: "A consumer must only use ResourceSlices with the highest generation number and ignore all others ... a consumer should check the number of ResourceSlices in a pool ... to determine whether its view of a pool is complete" (L83-91).
- **The scheduler judges a pool per node** (`GatherPools`, `dynamic-resource-allocation/structured/internal/stable/pools_stable.go`). It gathers the slices that serve one candidate node, and its rules changed between minors (revision 1, from the oracle of §9 and the sources):
  - **1.34** (v0.34.12): the slices a node sees, at their highest generation, must number `resourceSliceCount`. Completeness is counted per node, so a pool whose node-local slices name two nodes is incomplete on each.
  - **1.35** (v0.35.9): slices carrying only `sharedCounters` are seen by every node. A node whose slices fall short of the count looks across the cluster: if any slice of the pool has a higher generation, the node sees nothing of the pool ("obsolete"); else, if every slice at that generation is present cluster-wide, the pool is complete. Counter sets are validated: a set defined twice, a device consuming a set or a counter the pool does not define.
  - **1.36 and 1.37** (v0.36.5, v0.37.1): as 1.35. An incomplete pool is not validated further (`pools_stable.go` L163-175), so incompleteness is reported before invalidity.
  - **Duplicate device names** count among the slices one node sees, never across nodes: two node-local slices on different nodes may name the same device (L230, L295-297).
  - **1.33** (v0.33.13, read through v1beta1): the 1.34 algorithm and validity rules, line for line, except that with the partitionable-devices feature off it also ignores counter slices. Counter-based partitionable devices were alpha before 1.35, in a different shape.
  - **Feature gates, by minor:** without the partitionable-devices feature, 1.34 ignores per-device slices only; 1.33 and 1.35+ ignore per-device and counter slices. From 1.37, without the compatibility-groups alpha feature, slices using compatibility groups are ignored too.
- **The scheduler may be one minor behind the API server.** The version-skew policy allows kube-scheduler to match the API server's minor or be one older, so a 1.35 API server may run a 1.34 scheduler. The API server also keeps partitionable-device fields already stored when its own gate is turned off (`pkg/registry/resource/resourceslice/strategy.go` L252-264), and the scheduler has its own gate. Neither the server's version nor a stored field proves the running scheduler's rules.
- **The allocator skips incomplete and invalid pools silently** (`allocator_stable.go` L960). Only `allocationMode: All` errors, with "resource pool ... is currently being updated" or "... is currently invalid" (L493-497). §9 uses that error as an oracle, from each node a fixture names.
- **The kubelet owns node-local slices.** The kubelet plugin makes a node-local slice owned by its Node (`kubeletplugin/draplugin.go` L1191-1195). The kubelet deletes the node's slices when it starts, and 30 seconds after a driver becomes unusable (`kubelet/cm/dra/plugin/dra_plugin_manager.go` L165-180; `manager.go` L75, L503-537).
- **No API object records that a driver is registered**, as CSINode does for CSI. Node-local slices are the only evidence that a driver is serving a node now, or was less than 30 seconds ago.
- **Limits:**

  | What | Limit |
  |---|---|
  | Devices per slice | 128 (`ResourceSliceMaxDevices`); 64 with taints or counters (1.35+) or list attributes (1.36+) |
  | Attributes and capacities per device | 32 |
  | String attribute | 64 bytes |
  | Pool name | 253 bytes |
  | Slices per pool | no limit |

### 1.3 ResourceClaims

- **The spec** is `devices.requests[]`, each named, with either:
  - `exactly{deviceClassName, selectors, allocationMode ExactCount|All, count, adminAccess, tolerations, capacity}`, or
  - `firstAvailable[]` (TYPES L1217-1500).

  The types say "Clients must refuse to handle requests with unknown modes" (L1383).
- **The status:**
  - `allocation{devices.results[], nodeSelector, allocationTimestamp}`, at most 32 results (`AllocationResultsMaxSize`). Each result carries `{request, driver, pool, device, adminAccess, shareID, ...}`.
  - `reservedFor`: consumers keyed by UID, at most 256 (`ResourceClaimReservedForMaxSize`).
  - `devices[]`: the drivers' own per-device status, with conditions (at most 8; "the `Ready` condition should be True" once the device is configured, L2590-2592), raw `data` (at most 10 KiB) and `networkData`.
- **There is no claim-level condition** in any release, and `deallocationRequested` was removed in 1.32.
- **A claim's state** is kubectl's (`pkg/printers/internalversion/printers.go` L3286-3302): deleted (a deletion timestamp), pending (no allocation), allocated, reserved.
- **Allocation and release:**
  - The scheduler adds the finalizer `resource.kubernetes.io/delete-protection`, then writes the allocation and `reservedFor` in one status update (`dynamicresources.go` L1910-1942).
  - The resource-claim controller clears an allocation once no consumer is left, then removes the finalizer (`controller/resourceclaim/controller.go` L1600-1666).

### 1.4 A pod and its claims

- **The references:** `spec.resourceClaims[]` lists `{name, resourceClaimName | resourceClaimTemplateName}`. A template's generated claim is recorded in `status.resourceClaimStatuses[]{name, resourceClaimName}`. An unset name there means no claim was needed (core `types.go` L4823).
- **The mapping** (`dynamic-resource-allocation/resourceclaim/resourceclaim.go` L69-81, L123-138):
  - a direct name is used as it is;
  - a template entry is looked up in the status, and its claim counts only if the pod is the claim's controller (`IsForPod`).

  A generated claim carries the annotation `resource.kubernetes.io/pod-claim-name` (controller.go L1105-1139).
- **S11a's extended-resource claim** is `status.extendedResourceClaimStatus.resourceClaimName`, annotated `resource.kubernetes.io/extended-resource-claim: "true"`.
- **`reservedFor` can name a PodGroup** (1.37, beta and off), so a consumer is never assumed to be a pod.
- **Validation** allows at most 32 entries in `spec.resourceClaims`.

### 1.5 The evidence for a Pending claim

- **A missing claim stops the pod before scheduling, silently.** The DRA plugin's PreEnqueue rejects the pod when its claim is not created yet, not found, being deleted, or not owned by the pod. That only gates the pod: there is **no PodScheduled condition and no FailedScheduling event** (`dynamicresources.go` L303-312, L536-561; the queue logs at V(4)). The controller-manager's Warning `FailedResourceClaimCreation` is the only event (controller.go L925).
- **Filter failures** write `PodScheduled=False` (`Unschedulable`, or `SchedulerError` for an error) and a Warning `FailedScheduling` ("0/X nodes are available: ..."). The DRA plugin's reasons, in 1.34 to 1.37:

  | Reason | Notes |
  |---|---|
  | "cannot allocate all claims" | one message for exhaustion, no matching device, tainted devices, an incomplete pool and no slices at all |
  | "resourceclaim not available on the node" | |
  | "resourceclaim in use" | |
  | "resource claim ... is in the process of being allocated" | |
  | "request ...: device class ... does not exist" | |
  | "node ...: timed out trying to allocate devices" | an error, after 10 s |
  | "invalid resource pools were encountered" | 1.35+ |
  | "requires DRAOptionalNodeOperations feature on the node" | 1.37 |
- **The kubelet** writes a Warning `FailedPrepareDynamicResources` (`kuberuntime_manager.go` L1726). Its messages include "DRA driver ... is not registered" and "NodePrepareResources failed for ResourceClaim ...".

### 1.6 Device health and taints

- **Pod device health:** `status.containerStatuses[].allocatedResourcesStatus[]{name, resources[]{resourceID, health Healthy|Unhealthy|Unknown, message}}` (core `types.go` L3606-3690).
  - The name is `claim:<claim>[/<request>]` for DRA, or the resource name for a device plugin.
  - The gate `ResourceHealthStatus` is alpha and off through 1.35, and beta and on in 1.36 and 1.37. **It is not GA in 1.37, as the roadmap says.**
  - A driver without a health service, or one silent for 30 seconds, reads Unknown (`healthinfo.go` L33; KEP-4680 L319-324).
  - The kubelet's `resourceID` is the device's first CDI ID, not driver/pool/device (`kubelet/cm/dra/manager.go` L1032-1037), so it is never joined to a device by string.
- **Device taints:** a slice's devices carry taints with the effects None (1.35+, informational), NoSchedule and NoExecute (evicts consumers). An unknown effect reads as None (L963). `DeviceTaintRule` objects taint selected devices too. The taint field is beta in 1.36 and GA in 1.37; `DeviceTaintRule` is served at v1 only from 1.37.

### 1.7 kind-klyx and the example driver

kind-klyx (v1.37.0, one arm64 node) serves `resource.k8s.io/v1` with no DRA objects. The upstream example driver is dra-example-driver v0.5.0:
- image `registry.k8s.io/dra-example-driver/dra-example-driver:v0.5.0`, which includes arm64;
- chart `registry.k8s.io/dra-example-driver/charts/dra-example-driver:0.5.0`, a Helm release into its own namespace.

It provides:
- driver and DeviceClass `gpu.example.com`;
- devices `gpu-0` to `gpu-7` in a node-local pool named after the node;
- a kubelet health service, driven by `health.example.com/<device>` annotations on its pod;
- driver-written device status;
- KEP-5004's implicit extended-resource name.

On 1.37 its GPU profile needs no feature gate.

## 2. What S11b reads, and where

A new task, **`dra`**, served when `resource.k8s.io/v1` serves its kinds (a probe among `["v1"]`). It has three parts and adds one reading to the pods part:

| Part | Counted | Deps | Classifier | Identity |
|---|---|---|---|---|
| `claims` | resourceclaims | pods | native: a verdict per claim, ledger | the claim's UID |
| `pools` | resourceslices | - | native: a verdict per pool, ledger | driver and pool name, a stated identity (as Helm's releases): the ledger key is `driver/pool`, unambiguous since a driver name has no slash |
| `classes` | deviceclasses | - | evidence: fact rows | the class's UID |

A part's coverage is the worst of every read it lists (`rollup`), so no part lists a read its verdict does not need. A figure that joins two parts (a pool's allocated devices, a class's claims) is joined in the lens from both parts' rows, qualified by both reads, as S11a qualifies its figures.

DeviceTaintRules are not read in S11b (§10). A pool's taint figures come from its slices' own device taints, and say so.

### 2.0 Bounds

- **Budgets per cluster** (projected watches, admission as every kind's): ResourceSlices 2 MiB, ResourceClaims 2 MiB, DeviceClasses 256 KiB. The worst-case objects measured in §7 set each kind's maximum object size.
- **The slice projection** (the field ledger: every field a §3.2 rule reads, and no other):
  - the pool identity, generation and `resourceSliceCount`;
  - the slice's access form: `nodeName`'s value, `allNodes`, whether a `nodeSelector` is set, `perDeviceNodeSelection`;
  - `sharedCounters`: each set's name and its counter names (never the quantities), at most 8 sets of 32;
  - per device: its name; its own access form under per-device selection (the same three facts); each `consumesCounters` entry's set name and counter names; its taints' effects; whether it uses compatibility groups;
  - dropped: attributes, capacities, quantities, selectors' content and every taint's key and value.
- **The claim projection:**
  - kept: identity, owner references, finalizers (the deletion rule names them), the two annotations, `deletionTimestamp`, and requests (name, class, mode, count, adminAccess, and for `firstAvailable` each subrequest's class);
  - every allocation result (at most 32): request, driver, pool, device, adminAccess, `shareID`;
  - `reservedFor`: the count, plus the first 8 consumers (resource, name, UID);
  - device status: per device, its identity and conditions (type, status, reason; at most 8). Each message is kept local only, clipped to 256 bytes. Never `data` or `networkData`;
  - selectors, configs and tolerations are dropped, in spec and in the allocation's status alike.
- **Raw-versus-projected equality** is tested for every rule: the valid and missing-counter pair, a duplicated set, a stuck deletion with finalizers, per-device forms, shared allocations.
- **The class projection:**
  - kept: the number of selectors, its config's driver names, and `extendedResourceName`;
  - never the CEL.
- **Rows:** a claim row names at most 8 consumers found for a pending claim, then a count. A pool row carries counts, never device names. A class row carries §3.3's small facts.

### 2.1 The source contracts (C1)

**The claims part** (new):
- **Acquisition:** the projected claim watch at v1, and the existing projected pod watch. `ProjectPod` gains `spec.resourceClaims`, which holds names only. It already keeps the whole status.
- **The counted entity:** a claim by UID.
- **Dependencies:** the pods kind, read for a pending claim's consumers (§3.1). A pod consumes a claim when it is not terminal, is in the claim's namespace, and references the claim (§1.4's mapping, or the extended claim).
  - A pending claim's consumer evidence needs the pods read complete and current. Otherwise the claim reads Unknown, "its consumers are not established", rather than "not used by any pod".
  - Allocated and deleted claims read from the claim alone. The part's coverage still follows both reads (`rollup`): an unreadable pods read makes the part unreadable, and each claim's row keeps its own reading.
- **Clock:** one boundary. A claim being deleted with no consumer reads Progressing for 5 minutes from its `deletionTimestamp`, then Degraded. That is pure: a ReclassifyAt from the object.
- **Notification eligibility:** no claim reading is a Fault (§3.1), so none notifies (`healthnotify` opens only on a fault).
- **No synthetic evidence:** a claim has no conditions, and none is invented. The drivers' device conditions are read as written. The scheduler's reason is quoted from the pod's condition and never inferred.

**The pools part** (new):
- **Acquisition:** the projected slice watch at v1. Nothing else: a pool's allocated count is joined in the lens from the claims rows (§2).
- **The counted entity:** a pool by driver and pool name, across every slice, whatever node a slice targets.
- **Dependencies:** none among the watched kinds. A pool's reading needs the slices read complete and current, or stale with its date, since only every slice shows a pool whole. Under a capped read, every pool reads Unknown, "not every slice was read (the slices budget)": a missing slice could be Klyx's cut, or a newer generation.
- **A fact: the server's minor version** (`FactServerMinor`), from the GitVersion the connection reads at connect and on every re-probe; a distribution suffix (`-eks-...`) is ignored. §3.2 derives the schedulers it must allow from it (the server's minor and one older), never the running scheduler's version.
- **The joined allocated count** is a lower bound under a capped claims read, and "not established" under an unread one. A pool's verdict never depends on it.
- **Clock:** an incomplete pool reads Progressing for 5 minutes from when Klyx first observed it incomplete, then Degraded (§3.2). The start comes from an episode tracker (§3.2), the S4/S9 pattern: a fact set by the connection, read by a pure evaluator, with the 5-minute crossing a ReclassifyAt.
- **Notification eligibility:** no pool reading is a Fault, so none notifies.
- **No synthetic evidence:** completeness and validity are the scheduler's own rules, held to the scheduler's own allocator by §9's oracle.

**The classes part** (new): the projected class watch. Fact rows, never counted, no clock.

**The pods part** (changed: one new reading, from the pod alone, §3.4; no new dependency).

## 3. The readings

### 3.1 A claim

In this order, first match:

1. **Being deleted** (a deletion timestamp):
   - with consumers in `reservedFor`: Progressing, "being deleted: waiting for N consumers";
   - with none: Progressing for 5 minutes from the deletion timestamp, then Degraded, "deletion not finishing", naming its finalizers.
2. **A driver reports a device not ready.** An allocated device whose status condition `Ready` is False reads Degraded, "device not ready". The device is named (driver/pool/device), with the condition's reason; the driver's message is shown locally only (§6). Degraded, not Fault: the condition is the driver's, and its meaning is the driver's.
3. **Allocated:** OK. "Allocated · in use by N" when `reservedFor` has consumers (a consumer that is not a pod, a PodGroup, is said as such). "Allocated · not in use" when it has none, a transient the controller clears (§1.3).
4. **Pending, with consumer pods** (§2.1): the decision table below.
5. **Pending, with no consumer pod:** OK, "pending: not used by any pod". A claim is allocated only when a pod that uses it is scheduled. Klyx says this only under a complete and current pods read (§2.1).

**A pending claim's decision table** (revision 1). Klyx reads each consumer pod's `PodScheduled` condition and matches only the fixed reasons of §1.5, pinned per version by a source test (§9). In this order:

| # | The evidence | Reading |
|---|---|---|
| a | any consumer's condition says "resource claim <this claim> is in the process of being allocated" | Progressing, "being allocated" |
| b | a consumer's condition is False (`Unschedulable` or `SchedulerError`) with a DRA reason that is **claim-specific**: it names this claim, or names a device class only this claim's requests use among the pod's claims, or this is the pod's only DRA claim (its resolved claims and its extended claim counted). Uniqueness is proven only from complete evidence: every claim of the pod resolved and read, under a complete and current claims read; otherwise the reason is set-wide (row c) | Degraded, "the scheduler cannot allocate it", quoting the reason with its documented meaning |
| c | a consumer's condition is False with a DRA reason that is **set-wide** (for example "cannot allocate all claims" on a pod with several claims: upstream allocates a pod's claims all or nothing, `dynamicresources.go` L1059-1092) | Degraded, "its pod <p> cannot have its claims allocated; which of its N claims is the cause is not established" |
| d | a consumer's condition is `SchedulerError` whose message matches no fixed DRA reason | Progressing, "waiting for its pod: the scheduler reported an error, and its relation to this claim is not established", with the message. DRA produces errors outside the fixed set (a CEL runtime error in a class's selector, for one), so a miss never proves the cause is outside DRA |
| e | a consumer's condition is `Unschedulable` whose message matches no fixed DRA reason | Progressing, "waiting for its pod: it is unschedulable, and the message names no DRA reason Klyx recognises", with the message (the pod's own verdict carries that fault). A reason Klyx recognises as not DRA's (insufficient cpu or memory, node affinity, taints) may be named as such |
| f | a consumer has no `PodScheduled` condition | Progressing, "waiting for the scheduler" |
| g | a consumer is scheduled (`PodScheduled` True or a node name) while the claim is not allocated | Unknown, "its pod is on a node, but the claim is not allocated" |

- **Several consumers:** row a decides first, for the claim as a whole. Otherwise each consumer pod gets its own row of b to g, and the claim's reading is the worst (in the verdict order), first by pod name on a tie. The claim's row keeps each consumer's reading and identity (8 named, then a count).
- **Possible causes are never attributed.** For rows b and c the lens adds the cluster's own pool problems (pools being updated or invalid, from the pools part) as "check also". Which pools a claim's class can select is decided by the class's CEL selectors, which Klyx does not evaluate.
- **A message matching no fixed reason** never makes a claim degraded.

### 3.2 A pool

The pool is every slice of one driver and pool name. Klyx reads it as the scheduler does, per node (§1.2, revision 1).

**A pool's views.** A view is the slices one node sees:
- **each node a slice or a device names** (`nodeName`): that node's slices, the `allNodes` slices, the per-device slices with a device for it or for all nodes, and from 1.35 the slices carrying only `sharedCounters`;
- **a node no slice names, if there is one:** the `allNodes` slices, the per-device slices with an all-nodes device, and from 1.35 the counter slices. Klyx does not read Nodes, so this view is hypothetical configuration behaviour: said as "on a node no slice names (if there is one)", never as a node that exists;
- **a view that needs a node selector evaluated** (a `nodeSelector` slice or device) is not established: Klyx does not evaluate node selectors (§10). The reading names the views it could not establish.

**Each view's outcome**, as the scheduler's allocator reaches it (§1.2): **none** (no slice serves the view, or the scheduler ignores its slices), **obsolete** (1.35+: a newer generation exists elsewhere, so the node sees nothing of the pool), **being updated** (M of N slices), **invalid** (with the rule), or **complete**. Incompleteness is decided before validity within a view, as upstream does.

**The schedulers Klyx allows for** (revision 2). Klyx never assumes the running scheduler's version or gates. It computes each view's outcome under every scheduler the cluster may run:
- **the scheduler's minor:** the API server's minor, or one older (the skew policy, §1.2). An unknown server version allows every minor Klyx models. A server newer than 1.37 is read with the 1.37 rules, and says so;
- **the partitionable-devices feature, on and off,** when the pool uses per-device selection or counters;
- **the rule families:**
  - **1.34 and 1.33:** completeness over the view's own slices; invalid means duplicate device names in the view. A counter-using pool under a possible 1.33 or 1.34 scheduler is not established (alpha, in another shape, §1.2);
  - **1.35 to 1.37:** completeness over the view, else over every slice of the pool at that generation cluster-wide; invalid means duplicate device names in the view, a counter set defined twice, or a device consuming a set or a counter the pool does not define.
- **A pool using compatibility groups** (1.37 alpha) is not established, "completeness depends on the scheduler's compatibility-groups support, which Klyx cannot read".

**The variants agree** when every one reaches the same outcome, or when every one reaches an outcome that reads OK (complete, none or obsolete). Then the view reads that outcome, and the row says what it assumed: "read for a 1.35 or 1.34 scheduler, with the partitionable-devices feature on or off". Otherwise the view is not established, "scheduler health not established: it depends on the scheduler's version or feature gate, which Klyx cannot read", with each variant's outcome listed (for example "invalid with the partitionable-devices feature on; complete with it off").

**Each view reads** (incompleteness is decided before validity within a view, as upstream does):
- **being updated:** Progressing, "being updated on <node | a node no slice names> (M of N slices)"; once the pool has been incomplete for 5 minutes (the tracker's episode is the pool's, not the view's), Degraded, "the pool has been incomplete for over 5 minutes: the scheduler allocates nothing from it there";
  - **the start** is the first observation of that pool incomplete at that generation (a lower bound);
  - **the tracker** visits the slices kind when it changes and keeps each incomplete pool's first observation, keyed by pool and generation. It keeps an episode only across contiguous complete, current reads, the KEDA tracker's continuity rule; a relist, a gap or a capped read restarts it;
  - **with no episode** (the tracker has not yet seen the pool), the view reads Progressing with no deadline until the tracker's next visit;
- **invalid:** Degraded, "invalid on <view>: the scheduler allocates nothing from it there", with the rule;
- **not established:** Unknown, with the reason;
- **complete, obsolete or none:** OK.

**The pool's reading is its worst view's**, in the verdict order, naming the view; a tie names the first view by name. An OK pool carries its facts:
- its devices, per view;
- allocated devices, distinct by driver/pool/device. Admin-access results are not counted; shared results (`shareID`) are counted once, with the number of shares;
- tainted devices, by effect, from the slices;
- its access forms;
- the views not established, and any node on which it is obsolete (a driver updating node by node passes through that, so it is a fact, never a verdict).

**Older-generation slices** still present are a fact, never a verdict. "Available" is never claimed: the scheduler holds allocations in flight that the API does not show, so the figure is "not allocated per the API".

### 3.3 A device class

A fact row:
- the number of its selectors (never their CEL);
- its config's driver names;
- its `extendedResourceName`;
- the number of claims that request it, joined in the lens from the claims rows.

### 3.4 A pod's device health

`health.Pod` gains one rule, from the pod alone: **a Running pod with an `allocatedResourcesStatus` resource reported Unhealthy, in a container that is running now** (a regular container or a restartable init container) reads Degraded, "Device unhealthy". It names the claim (or the device-plugin resource) and counts its unhealthy devices.
- **The order:** after the current-failure faults, before the running-pod rules. A crash still leads; an unhealthy device outranks "Running" or "Not ready".
- **Only current containers count.** A completed regular init container's status is history: its devices are no longer in use by it.
- **Unknown is not a reading,** and it is not absence either. "Unknown" is what the kubelet reports for a driver without a health service or one silent for 30 seconds, said as "health Unknown (reported by the kubelet)". No `allocatedResourcesStatus` at all is said as "no device health reported" (the gate may be off, 1.35 and earlier). Neither is proof of a health service's absence.

### 3.5 A pod's claims

The pod inspector lists each claim the pod references:
- **The mapping** is computed in Go, in the pod detail, from §1.4's rules (`resourceclaim.Name`'s three results kept apart). Each entry is the pod's claim name, its source (a claim, a template, or the extended claim), and one of:
  - the claim's name;
  - "no claim needed": a status entry with no name, which upstream skips on purpose (`dynamicresources.go` L542-546). It is never a missing claim;
  - "not created yet": a template with no status entry.
- **Each entry joins the live claims rows** by namespace and name, as S11a joins its rows to a node:
  - **a row found:** its state and devices, and whether the pod is its controller for a template's claim;
  - **none found, under a complete and current claims read:** "does not exist". For a Pending pod with no node, Klyx adds "the scheduler waits for it without an event", since PreEnqueue writes neither a condition nor an event (§1.5). For a pod already on a node, the absence is said alone: it proves nothing about the scheduler now. The controller's `FailedResourceClaimCreation` event, if any, is in the pod's events;
  - **otherwise:** "not established".
- **Device health per claim** is read from `allocatedResourcesStatus` by the claim name, never by `resourceID` (§1.6).
- **`EvidenceRev`** gains `status.resourceClaimStatuses`, and every container status list's `allocatedResourcesStatus` (regular, init and ephemeral): each entry's name, and each resource's ID, health and message. So a generated claim's name recorded after creation, Healthy turning Unknown, or a different device turning unhealthy at the same count each refresh the detail.

## 4. Vocabulary

| Term | Meaning |
|---|---|
| pending, allocated, in use, being deleted | the claim's state (kubectl's) |
| a pool | driver and pool name |
| being updated, complete, invalid | a pool's state (the scheduler's words) |
| a device | driver/pool/device |
| served by DRA | S11a's term, linked to the claim |

Klyx never says "available", "registered" or "unsatisfiable". A node with no slices from a driver is "no slices from <driver> on this node", never "not registered" (§1.2).

## 5. What Klyx shows

- **A DRA lens**, capability-gated on `resource.k8s.io/v1` with any slice, claim or class present, under Nodes in the lens index:
  - **Claims**, broken first: the state; consumers (pods named, others said); allocated devices; the pending evidence ladder; device conditions.
  - **Pools:** completeness (M of N, generation); devices, allocated, taints by effect; the access form, and the node for a node-local pool.
  - **Device classes:** the facts of §3.3.

  Each tab carries the C5 marks for a capped, stale or unread read.
- **The node inspector, "DRA devices on this node":** its node-local pools with devices and allocated counts. Pools offered to all nodes or by a selector are in the lens, since Klyx does not evaluate node selectors.
- **The pod inspector, "Claims":** §3.5's list, the pending ladder in the scheduler's words, and device health per claim.
- **S11a's figures:** "served by DRA" names the pod's extended claim and links to it.

## 6. AI and chat

**The egress policy** (revision 1): one policy for the DRA kinds, applied at every path that sends cluster content to a model, as S10 does for Karpenter (`karpenter_ai.go`).

| Kind | Never sent |
|---|---|
| DeviceClass | `spec.selectors[].cel`, `spec.config[].opaque.parameters` |
| ResourceClaim | every request's and subrequest's `selectors[].cel`, `spec.devices.config[].opaque.parameters`, `status.allocation.devices.config[].opaque.parameters`, `status.devices[].data`, `.networkData` and `.conditions[].message` |
| ResourceClaimTemplate | the claim spec's fields above, under `spec.spec` |
| ResourceSlice | device `attributes`, `capacity`, and taint keys and values |
| DeviceTaintRule | the selector's CEL, and the taint's key and value |

What is sent instead: claim and pool states and counts; device identities (driver/pool/device); request names, classes, modes and counts; condition types, statuses and reasons (token-checked); class names, `extendedResourceName` and config driver names; the scheduler's DRA reason as its fixed key, never the message.

**Every path, by name:**
- **Explain** (lens evidence and related objects): the DRA parts' allowlists name only the fields above; a related DRA object crosses through the same per-kind projection.
- **Chat `get`, `list` and the Claude Code CLI snapshot:** the five kinds join a `draResources` set. Their objects never take the generic spec and status projection (`chat_project.go` L522); each takes a per-kind projection of the allowed fields, with conditions as type, status and reason only.
- **Suggest-fix:** a DRA manifest is sanitized structurally before redaction: `status` removed, and every never-sent field replaced by the marker `<omitted by Klyx>`, so the model sees that a selector or config exists and never its content. A ResourceSlice gets no manifest at all: it is the driver's, never Git's.
- **Events:** event text is already scrubbed and capped (500 bytes) for chat; S11b adds nothing.

**The tests send synthetic needles** (a CEL expression, an opaque parameter, a device's data, an attribute's value, a condition message) through each path, and search the serialized outgoing document for each one: the Explain request, the chat tool results and CLI snapshot, and the Suggest-fix prompt. Codex's two probes (a class's CEL through chat `get`; a claim's opaque config through Suggest-fix) are among them. Local inspection keeps every field, as the inspectors do today.

## 7. The budget

- **Sizes measured at the build** for typical and worst-case objects of each kind, recorded in the as-built:
  - a slice of 128 devices with 63-byte names and 4 taints each;
  - a counter slice of 8 sets of 32 counters, and a device slice whose devices each consume counters from several sets, with 63-byte names;
  - a claim with 32 results, each device with 8 conditions of maximal type and reason;
  - a pod detail with the most `allocatedResourcesStatus` entries the API allows, and 32 claims.
- **Evaluation:** measured with `TestLoad`. The large fixture gains 50 node-local pools of 8 devices, two all-nodes pools, 400 claims (half allocated, 50 pending with consumers), and 5 classes. The steady heap, the first complete summary and the full evaluation are recorded before and after.

## 8. The worst shapes, measured

Each records the encoded bytes, the retained heap and the evaluation time, and proves its omission is said:
- 2,000 slices in one pool of 2,000 slices;
- one pool at a generation with one slice missing;
- 1,000 claims pending on 1,000 pods;
- one claim with 256 `reservedFor` entries.

## 9. Tests, lab, parity, smoke

- **Unit tests:** every reading of §3 and its order, including:
  - the decision table's rows a to g, a non-DRA `SchedulerError`, an allocation in progress, a pod with two claims of which only one blocks allocation (set-wide, culprit not established), and several consumers reduced to the worst with each kept;
  - `resourceclaim.Name`'s three results, including the intentional "no claim needed";
  - the health rule with a running restartable init container (counts) and a completed regular init container (does not), Unknown, and no health at all;
  - raw-versus-projected equality for every rule (§2.0).
- **The pool oracle** (revisions 1 and 2). The scheduler's own allocator (`k8s.io/dynamic-resource-allocation/structured.NewAllocator`) runs at v0.33.13 (through v1beta1), v0.34.12, v0.35.9, v0.36.5 and v0.37.1, module hashes recorded. For each fixture it runs from every node a slice or device names and from a node none names, and, for a fixture using per-device selection or counters, with the partitionable-devices feature on and off. The claim requests `allocationMode: All` from a class selecting the fixture's driver.
  - **An outcome is proven, never inferred from silence:** "being updated" and "invalid" are the allocator's own errors; "complete" requires an allocation whose devices are named and all from the fixture's pool; "none" (no error, no allocation) is its own outcome, never success.
  - **Klyx's per-variant outcome must equal the oracle's** for every fixture, view, minor and gate variant, except views Klyx states as not established (selectors; counters under a possible 1.33 or 1.34 scheduler), whose oracle answers are recorded beside them.
  - **Klyx's combined reading is tested** for each server minor from 1.34 to 1.38 and an unknown one: where the allowed variants agree, and where they do not. Among them: the 1.35 server with a possibly 1.34 scheduler on a pool whose node-local slices name two nodes (not established), and the gate-dependent pair (a duplicate device name between an all-nodes slice and a per-device slice: invalid with the feature on, complete with it off; not established).
  - **The fixtures** (26, scratchpad `s11b-golden`, 62 outcomes per minor): one and two slices; a missing slice; an older generation beside a newer, and obsolete beside complete; duplicates in one view, across two nodes, an all-nodes slice duplicating a node-local one, and the gate-dependent pair; incomplete and duplicate together; a missing counter set, a duplicated set, a missing counter within a defined set, and a defined one; per-device on one and two nodes; all-nodes complete and missing; mixed node-local and all-nodes; selectors matching and not.

  The generators' output is committed as testdata, as S11a's PodRequests golden is.
- **The decision table's misses:** a CEL runtime error from a class's selector (a DRA `SchedulerError` outside the fixed set) reads row d, never "outside DRA"; a genuinely non-DRA `SchedulerError` reads row d too; a recognised non-DRA `Unschedulable` reason is named as such.
- **Egress:** §6's needle tests.
- **The inspector's evidence:** mounted tests for Healthy turning Unknown, a different device unhealthy at the same count, and a generated claim's name recorded after creation, each refreshing the pod detail; with UID and request fencing, and an unrelated status change as the control.
- **The scheduler's reasons:** a source test pins §1.5's strings per version, quoting the lines.
- **The lab** (`lab-s11b`), labelled with a teardown script that uninstalls everything:
  - the example driver, as a Helm release in its own namespace;
  - a claim from a template, allocated and reserved;
  - a claim shared by two pods;
  - a standalone claim nothing uses ("pending: not used by any pod");
  - a pod requesting nine GPUs of eight ("the scheduler cannot allocate it", with the reason's documented meaning);
  - a pod using the implicit DeviceClass name (S11a's "served by DRA", made live);
  - a device marked unhealthy through the driver's health annotation (the pod reads "Device unhealthy");
  - two fixture pools, stated as fixtures, all-nodes so the kubelet does not delete them, under a driver name no plugin serves: an incomplete pool and an invalid one.
- **Parity:**
  - the example driver is in the baseline;
  - case 12: a pod whose claim cannot be allocated (one claim degraded);
  - case 13: an invalid fixture pool (one pool degraded);
  - the oracle gains the three parts, with `classes` as inventory.
- **The smoke:** the DRA lens's claims and pools; the pending pod's ladder; the node's DRA block; the unhealthy device on the pod.
- **Mutations, captures, as-built,** as every slice.

## 10. Not in S11b

- Any write: admin-access verbs, claim creation, deallocation.
- DeviceTaintRules (v1 only from 1.37); the slices' own taints are read.
- KEP-5677's pool status API (alpha, off by default).
- Evaluating node selectors (which nodes an all-nodes or selector pool serves is the scheduler's question).
- PodGroup consumers beyond saying the consumer is not a pod.
- The v1beta1 shape (the 1.32 and 1.33 opt-in).

## 11. Revision 1: Codex's design review of ca27967

Codex (handoff c3b275ab) requested changes: one P1 and four P2 findings. My own oracle runs, made while the review was open, found the version and per-node behaviour of finding 3 independently; both are folded in.

| # | Finding | Where answered |
|---|---|---|
| P1 | The AI exclusions covered only claim status and slice attributes. A class's CEL crosses chat's generic projection, and a claim's opaque config crosses Suggest-fix, today | §6: one egress policy for the five DRA kinds, by field, applied by name at Explain, chat `get`/`list`/CLI snapshot and Suggest-fix (a per-kind projection; structural markers in the manifest; no slice manifest). Needle tests on the serialized outgoing documents, Codex's two probes among them |
| P2 | The projection lost fields the rules read: counter names (a valid and an invalid counter pool projected alike), the claim's finalizers, ShareID | §2.0: an explicit field ledger (counter sets with their counter names, consumed set and counter names, per-device access forms, compatibility-group use, finalizers, ShareID, request and result identities) and raw-versus-projected equality for every rule |
| P2 | Pool validity and completeness are per node and version-dependent; invalid was ordered before incomplete; "no error" was read as success | §1.2 and §3.2: per-view readings under the server's minor (a new fact), incompleteness before validity within a view, the worst view across views, selector views not established; §9: the oracle at four minors from every named node and an unnamed one, outcomes proven by named devices from the pool, "none" kept apart, feature options recorded, Codex's fixtures added |
| P2 | The pending ladder degraded an in-progress allocation, read any `SchedulerError` as DRA, and blamed one claim for a pod-wide failure | §3.1: the decision table (in-progress first; claim-specific versus set-wide DRA reasons; non-DRA errors as waiting; several consumers reduced to the worst, each kept); §3.5: the PreEnqueue sentence only for an unbound Pending pod, and "no claim needed" never a missing claim |
| P2 | Device-health changes did not refresh the pod inspector | §3.5: `EvidenceRev` hashes every container status list's `allocatedResourcesStatus` (names, IDs, health, messages) and the claim statuses; §9: mounted tests for each change, with fencing and a control |

Codex's guidance, taken: Unknown health is said as reported, never as a proven absent service (§3.4); restartable and completed init containers are tested (§3.4, §9); sizing covers shared-counter slices, maximal conditions and the pod detail's health lists (§7).

## 12. Revision 2: Codex's re-review of 4ceea16

Codex (handoff bf8783f1) requested changes: two residual P2 findings. The P1 egress finding is resolved.

| # | Finding | Where answered |
|---|---|---|
| P2 | The server's minor and the stored fields do not establish the running scheduler's rules: a 1.35 API server may run a 1.34 scheduler, and the API server keeps partitionable-device fields after its own gate is turned off. A gate-dependent pool is invalid with the feature on and allocates with it off | §1.2: the skew policy, the stored-field rule, 1.33 read from source and run, each minor's gate behaviour. §3.2: every view computed under every scheduler the cluster may run (the server's minor or one older; the feature on and off where the pool uses it), a definite reading only where the variants agree, with the assumptions on the row, and otherwise "scheduler health not established" with each variant's outcome. §9: the oracle at five minors with gate variants (62 outcomes each), the skew and gate-dependent fixtures |
| P2 | An unmatched `SchedulerError` was said to be "outside DRA", though DRA produces errors outside the fixed set | §3.1 rows d and e: the relation to the claim is "not established", never an external cause; a recognised non-DRA reason may be named. §9: the CEL runtime-error case |

Codex's implementation guidance, taken: row b's uniqueness needs every claim of the pod resolved and read under a complete, current read (§3.1); the unnamed-node view is said as hypothetical (§3.2); the incomplete duration is the pool's episode, worded so (§3.2).

**Design accepted at a411b56** (Codex, revision 2, handoff 100677db, 2026-10-20), with no remaining findings. Codex's implementation detail, kept: where the variants agree only in severity (complete, none and obsolete all read OK), the row keeps each variant's distinct evidence, and any figure that depends on the variant (the devices a view sees) is qualified by it. This accepts the design only; the implementation and its gates keep their own review.

## 13. As built

Built on `feat/coverage-s11b`, rebased onto S11a's accepted head (123b2d1), after the accepted design (a411b56):

| Commit | What |
|---|---|
| 251f6bb | Stage 1: `internal/dra` (pools read per node under every allowed scheduler, the claim ladder and its decision table, the pod's claim mapping, the fixed reasons); `health.Pod`'s "Device unhealthy"; the pool oracle against the scheduler's allocator at five versions |
| eb27d49 | Stage 2: the `dra` task (claims native with the pods as a dependency, pools native with the server's minor and the incomplete-pool episodes as facts, classes as evidence); the pod's claims in its projection and `EvidenceRev` |
| 9ffe55a | Stage 3: the rows converters, the pod detail's claims and device health, one egress policy for the five DRA kinds |
| dc9f61e | Stage 4: the Devices (DRA) lens, the node's DRA block, the pod's Claims block, the capability that shows them |
| 53d3348 | Parity cases 12 and 13 with the upstream example driver, the dra parts in the oracle, the smoke steps |
| d9754f5 | The allocator's "invalid resource pools were encountered" as a fixed reason (found live) |
| 4852d8a | What the worst shapes and §9's tests found: pools read in linear time, bounded pool rows, each kind's MaxObject from the worst object, no failed list read as absence, device health named |
| 721ac97 | The reasons pinned to their sources, the worst shapes measured, the load fixture's DRA objects, the smoke's unhealthy device |
| 53745cc | The pod's claims and S11a's served-by-DRA claim link to the DRA lens |
| fd9e0a9 | The incomplete-pool episodes follow the KEDA and LoadBalancer trackers' continuity rule |
| c13b9d2 | The smoke's claims line names the pod's claim beside a generated one |
| 2eb1ede | The mutation gate's eleven survivors, each with a test |
| 35e85f1 | Parity renders the example driver (no Helm release) and keys only pools by their stated identity |
| 0d30898 | The load test's update storm changes only pods that report a container |
| 2fc5df4 | An ineffectual assignment golangci-lint refuses, in a test |

### 13.1 Where the build departs from the note

- **"invalid resource pools were encountered" is a fixed reason** (§1.5 missed it). From 1.35 the allocator returns it when nothing could be allocated on a node and a pool read there is invalid, any driver's pool. It is the only allocator error that wraps `ErrFailedAllocationOnNode`, which the plugin passes through as unschedulable; every other allocator error is a `SchedulerError` and stays unmatched (row d). It was found live: lab-s11b's invalid fixture pool, under a driver nothing serves, turned the nine-GPU pod's reason from "cannot allocate all claims" into it. Its meaning says the pool may be any driver's, not necessarily this claim's.
- **The reasons are pinned to their sources** as §9 asked, which the build first did only by message: `testdata/reasons/quotes.json` quotes each line that writes a fixed reason, with its file's sha256 and line number, and each literal, formatted, must match its key.
- **Each kind's `MaxObject` is set from the worst object the API admits** (§2.0), projected; the build had a flat 64 KiB, under which a valid worst-case slice or claim would have been left out and the kind read capped:
  - a slice of 64 devices, each with 16 taints and 2 x 32 counters with 63-byte names (the 1.35+ shape): 323,359 B; 1.34's 32 x 32 shared counters with 128 devices consuming 1,024: 179,610 B; 128 plain devices: 21,917 B. `MaxObject` 384 KiB;
  - a claim of 32 requests with 8 subrequests each, 32 results and device statuses of 8 conditions whose every kept message byte escapes in JSON, 256 consumers and 64 finalizers: 325,227 B. `MaxObject` 384 KiB;
  - a class with 32 selectors and configs and the longest extended resource name: 2,798 B. `MaxObject` 16 KiB.

  To hold those figures, the only free text kept (a device condition's message) is made printable and sized without HTML escaping, so a byte encodes to at most two; condition types and reasons clip at 128 bytes, finalizers keep 8 and count the rest, and the pod-claim-name annotation clips at 253. Messages clip on a rune boundary and copy what they keep, so a projection never retains its object's string. A claim's lists are cut at the API's bounds, and a cut claim reads not established (no valid object reaches it). `TestWorstObjectsFitMaxObject` holds every kind to its bound.
- **A slice's bounds are the larger of 1.34's and 1.35-1.37's**: 1.34 allows 32 counter sets, so the note's 8 would have read a valid 1.34 slice as past the API's bounds.
- **The pool reading is indexed.** Every view scanned every slice: one pool of 2,000 node-local slices read in 690 ms, past the evaluation budget on its own. A per-pool index reads what every view shares once (the pool-wide reasons, each generation's slices, counter sets and failing consumptions) and each view only the slices its node may see, merged back into the capture's order: 2.2 ms. The scan it replaced is kept in the tests as the reference, and a million random views read the same outcome, rule, counts and devices; the oracle still matches the allocator on all 255 outcomes.
- **A pool row is bounded** (§2.0, "a pool row carries counts, never device names"): a read pool keeps device counts, not names, and its row keeps the 16 worst views and obsolete nodes, the rest counted and said ("1999 more nodes not listed: the 16 worst are shown, and the reading above is from every node"). The verdict is read from every view first.
- **The incomplete-pool episodes follow the KEDA and LoadBalancer trackers' rule** (§3.2 named it; the build followed half of it). The tracker restarted every episode on any skipped change of the slices kind, so on a busy kind a stuck pool would never reach its grace; and it ignored the kind's generation and epoch, so a relist could carry a start across changes it never saw. A read that is not complete and live, or a new generation or epoch, now restarts every episode (a lower bound); otherwise an episode continues when no change was skipped, or when its pool's slices are provably the same, by every slice's UID and resourceVersion (slices now carry their resourceVersion for this).
- **A failed DRA list is never read as absence.** The capability first read present only when every list failed; a served API with no object found and any list failed now reads present, unverified, naming the kinds it could not list.
- **Device health is named and its absence said** (§3.4, §3.5): the pod's Claims block names each unhealthy device by the kubelet's ID (four, then counted), so another device unhealthy at the same count reads differently, and a bound pod's claim with no report says "no device health reported". The build said nothing in both cases.
- **Explain is offered on claims only.** A pool has no object UID (its identity is driver and pool), and Explain is UID-fenced; a class row is a fact.
- **The server's minor is re-read on every re-probe**, so a cluster upgraded while Klyx runs reads its pools for the new schedulers.
- **The claim links** (§5: "served by DRA" names the claim and links to it) landed last (53745cc): the pod's Claims block and S11a's extended block open the claim in the DRA lens.

### 13.2 The worst objects and shapes (§7, §8)

`TestWorstObjectsFitMaxObject` (every run) builds the worst object the API admits for each kind and projects it; §13.1 gives the figures and the bounds they set.

`TestDRAShapesMeasured` (`KLYX_LOAD=1 go test ./internal/healthsrc -run TestDRAShapesMeasured -v`):

| Shape | Encoded | Retained heap | Time | The omission, said |
|---|---|---|---|---|
| 2,000 one-device slices | 668,000 B projected in all, within the 2 MiB slices budget | | | none: every slice is read |
| one pool of 2,000 slices, all nodes | the row 770 B | below the probe's resolution | 2.1 ms | none: one view |
| one pool of 2,000 slices, a node each | the row 5,549 B (16 views kept) | the read 521 KB before it is kept | 2.0 ms (690 ms before the index) | 1,985 views counted: "1985 more nodes not listed: the 16 worst are shown, and the reading above is from every node" |
| one pool missing one of 2,000 slices | the row 942 B | | 0.2 ms | "(1999 of 2000 slices)", Degraded past the grace |
| 1,000 claims pending on 1,000 pods | the rows 1,113,001 B | 996 KB | 3.4 ms | none: each row names its one consumer |
| one claim with 256 consumers | the projection 954 B | | | 8 kept, "in use by 256" |

### 13.3 The load test

`TestLoad`'s fixture now carries S11b (§7): 50 node-local pools of 8 devices and two all-nodes pools of 16, 400 claims (half allocated with a device status, 50 pending on Pending pods whose claims cannot be allocated, the rest unused) and 5 classes on the large cluster; a tenth of that on each small one. The same harness ran on S11a's head (123b2d1, without the DRA objects) and on S11b, interleaved, on an otherwise idle machine:

| Round | Full evaluation, before / after | Retained steady heap, before / after |
|---|---|---|
| 1 | 71.3 / 78.6 ms | 62.9 / 63.9 MB |
| 2 | 68.8 / 57.4 ms | 62.9 / 63.9 MB |
| 3 | 58.9 / 62.0 ms | 62.9 / 63.9 MB |

- **The full evaluation** is within the rounds' noise of the code before S11b; it misses the 50 ms budget before and after alike (the open owner question from S9).
- **The heap** rises by 1.0 MB retained (30.1 to 30.5 MB above baseline for the large cluster alone).
- **The first complete summary** stayed at 1.06-1.08 s, and every kind was live again 8.4-8.9 s after the API recovered, before and after alike. The recovery peak missed its 1.5x budget once, on the code before S11b (1.57x), as S10 recorded.
- **The storm's pods:** the update storm now changes only pods that report a container (S11b's pending pods report none).

### 13.4 Gates

- **Parity:** the Go stage passes with cases 12 (a pod whose claim cannot be allocated: one claim degraded) and 13 (an invalid fixture pool: one pool degraded), the example driver rendered from its pinned chart on `kind-klyx-parity`; the frontend stage passes. The first run failed in the harness: a Helm-installed driver is a `helm/releases` member the oracle does not model, and its UID-less row was keyed as a pool (35e85f1).
- **Mutations:** 43 (35 Go, 8 frontend). 32 were caught on the first pass, none failed to build, and the eleven survivors each gained a test (2eb1ede), after which all eleven are caught.
- **Go:** `go test -race ./internal/...` passes at 0d30898; golangci-lint reports 0 issues at 2fc5df4.
- **The frontend:** tsc is clean, eslint has 0 errors (its 4 warnings predate S11b), and vitest passes 2,396 tests.
- **The native smoke:** 205 of 205 steps in each of night and day at 1440 and 1024 on 0d30898 (a clean tree), with lab-s11b applied and lab-template's device marked unhealthy by relabs: the DRA lens's claims and pools, the blocked claim's ladder in the scheduler's words, the node's DRA block, and the pod's claims with its device named unhealthy. Its first run failed on its own regex (c13b9d2): the claims line names the pod's claim beside a generated one.
- **Captures** (headless, server mode, `pw/cov-s11b.mjs`), 32 in all, night and day at 1440 and 1024, every expected text present and no sideways scroll: the claims tab, the blocked claim's inspector (the scheduler's quoted reason, its meaning, the invalid pool pointed at), the pools tab, the invalid fixture pool ("duplicate device name dev-0", read for a 1.36 or 1.37 scheduler), the driver's node pool (Complete, 8 devices, the allocated count), the classes tab (`example.com/dra-gpu`), the node's DRA block and lab-template's claims (linked, "1 device unhealthy (reported by the kubelet): k8s.gpu.example.com/gpu=common"). The first run failed on two script expectations, not the product: the claims tab quotes the scheduler's "0/1 nodes are available", which the vocabulary rule leaves to the scheduler's own words, and the claims line's generated name.
