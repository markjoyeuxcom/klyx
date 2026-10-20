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
- **A pool is whole only at its highest generation, with every slice present.** The types say: "A consumer must only use ResourceSlices with the highest generation number and ignore all others ... a consumer should check the number of ResourceSlices in a pool ... to determine whether its view of a pool is complete" (L83-91). The scheduler's `GatherPools` (`dynamic-resource-allocation/structured/internal/stable/pools_stable.go`):
  - drops older generations (L199-210);
  - counts the pool's slices across every node (L372-395);
  - marks the pool incomplete (L129, L166-173) or invalid (duplicate device names, missing counter sets; L216-311).

  1.34 applies the same rule.
- **The allocator skips incomplete and invalid pools silently** (`allocator_stable.go` L960). Only `allocationMode: All` errors, with "resource pool ... is currently being updated" or "... is currently invalid" (L493-497). §9 uses that error as an oracle.
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
- **The slice projection:**
  - kept: the pool identity, generation and count; the access form; and each device's name, consumed counter-set names and taint effects. A per-device access form is reduced to its kind;
  - dropped: attributes, capacities and selectors.
- **The claim projection:**
  - kept: identity, owner references, the two annotations, `deletionTimestamp`, and requests (name, class, mode, count, adminAccess). Every allocation result is kept (at most 32): driver, pool, device, adminAccess, shareID;
  - `reservedFor`: the count, plus the first 8 consumers (resource, name, UID);
  - device status: per device, conditions (type, status, reason; at most 8). Each message is kept local only, clipped to 256 bytes. Never `data` or `networkData`;
  - selectors, configs and tolerations are dropped.
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
- **Dependencies:** none. A pool's reading needs the slices read complete and current, or stale with its date, since only every slice shows a pool whole. Under a capped read, every pool reads Unknown, "not every slice was read (the slices budget)": a missing slice could be Klyx's cut, or a newer generation.
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
4. **Pending, with a consumer pod** (§2.1). The pod's scheduling evidence, in the scheduler's words:
   - **Its `PodScheduled` condition is False, reason `Unschedulable`, and its message contains one of §1.5's DRA reasons:** Degraded, "the scheduler cannot allocate it", quoting the scheduler's reason with its documented meaning. "Cannot allocate all claims", for instance, covers no matching device, every matching device allocated, tainted devices, an incomplete pool, and no slices at all. The lens adds the cluster's own pool problems (pools being updated or invalid, from the pools part) as "check also", never attributed to this claim. Which pools a claim's class can select is decided by the class's CEL selectors, which Klyx does not evaluate.
   - **Reason `SchedulerError`:** Degraded, "the scheduler's allocation failed", with its message.
   - **`Unschedulable` without a DRA reason:** Progressing, "waiting for its pod: the pod is unschedulable for other reasons". The pod's own verdict carries that fault.
   - **No PodScheduled condition, or "in the process of being allocated":** Progressing, "waiting for the scheduler".
5. **Pending, with no consumer pod:** OK, "pending: not used by any pod". A claim is allocated only when a pod that uses it is scheduled. Klyx says this only under a complete and current pods read (§2.1).

Klyx matches only the fixed reasons of §1.5, pinned per version by a source test (§9). A message that matches none never makes the claim degraded.

### 3.2 A pool

The pool is every slice of one driver and pool name, at the highest generation present.

1. **Invalid** (the scheduler's rules: duplicate device names in the pool, a device consuming a counter set the pool does not define, a counter set defined twice): Degraded, "invalid: the scheduler allocates nothing from it", with the rule that failed.
2. **Incomplete** (fewer slices at the highest generation than its `resourceSliceCount`): Progressing, "being updated (M of N slices)". After 5 minutes: Degraded, "incomplete for over 5 minutes: the scheduler allocates nothing from it".
   - **The start** is the first observation of that pool incomplete at that generation (a lower bound).
   - **The tracker** visits the slices kind when it changes and keeps each incomplete pool's first observation. It keeps an episode only across contiguous complete, current reads, the KEDA tracker's continuity rule; a relist, a gap or a capped read restarts it.
   - **With no episode** (the tracker has not yet seen the pool), the pool reads Progressing with no deadline until the tracker's next visit.
3. **Complete:** OK, with its facts:
   - its devices;
   - allocated devices, distinct by driver/pool/device. Admin-access results are not counted; shared results (`shareID`) are counted once, with the number of shares;
   - tainted devices, by effect, from the slices;
   - its access form: a node, all nodes, a node selector, per device, or mixed.

**Obsolete slices** (an older generation still present) are a fact, never a verdict. "Available" is never claimed: the scheduler holds allocations in flight that the API does not show, so the figure is "not allocated per the API".

### 3.3 A device class

A fact row:
- the number of its selectors (never their CEL);
- its config's driver names;
- its `extendedResourceName`;
- the number of claims that request it, joined in the lens from the claims rows.

### 3.4 A pod's device health

`health.Pod` gains one rule, from the pod alone: **a Running pod with an `allocatedResourcesStatus` resource reported Unhealthy** reads Degraded, "Device unhealthy". It names the claim (or the device-plugin resource) and counts its unhealthy devices.
- **The order:** after the current-failure faults, before the running-pod rules. A crash still leads; an unhealthy device outranks "Running" or "Not ready".
- **Unknown is not a reading.** It is what a driver without a health service reports, and is shown as "health not reported by the driver".

### 3.5 A pod's claims

The pod inspector lists each claim the pod references:
- **The mapping** is computed in Go, in the pod detail, from §1.4's rules. Each entry is the pod's claim name, its source (a claim, a template, or the extended claim), and the claim's name, or "not created yet" for a template with no status entry.
- **Each entry joins the live claims rows** by namespace and name, as S11a joins its rows to a node:
  - **a row found:** its state and devices, and whether the pod is its controller for a template's claim;
  - **none found, under a complete and current claims read:** "does not exist: the scheduler waits for it without an event"; the controller's `FailedResourceClaimCreation` event, if any, is in the pod's events;
  - **otherwise:** "not established".
- **Device health per claim** is read from `allocatedResourcesStatus` by the claim name, never by `resourceID` (§1.6).
- **`EvidenceRev`** gains `status.resourceClaimStatuses`, so a generated claim's name, recorded after creation, refreshes the detail.

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

- **Crosses:**
  - claim and pool states and counts;
  - device identities (driver/pool/device);
  - condition types, statuses and reasons (token-checked);
  - class names, `extendedResourceName`, driver names;
  - the scheduler's DRA reason as its fixed key, never the message.
- **Never crosses:** drivers' device-condition messages, `data` and `networkData` (driver free text, PCI addresses, IPs), CEL selectors, configs, and device attributes.
- **Chat's object reads:** registered in the S0b sanitizer (`crd.SanitizeEmbedded`) for `ResourceClaim` (`status.devices[].data`, `.networkData`, `.conditions[].message`) and `ResourceSlice` (device `attributes`). A test proves chat's projection of a claim and a slice carries none of them.

## 7. The budget

- **Sizes measured at the build** for typical and worst-case objects of each kind, recorded in the as-built:
  - a slice of 128 devices with 63-byte names and 4 taints each;
  - a claim with 32 results and 8 conditions on each device.
- **Evaluation:** measured with `TestLoad`. The large fixture gains 50 node-local pools of 8 devices, two all-nodes pools, 400 claims (half allocated, 50 pending with consumers), and 5 classes. The steady heap, the first complete summary and the full evaluation are recorded before and after.

## 8. The worst shapes, measured

Each records the encoded bytes, the retained heap and the evaluation time, and proves its omission is said:
- 2,000 slices in one pool of 2,000 slices;
- one pool at a generation with one slice missing;
- 1,000 claims pending on 1,000 pods;
- one claim with 256 `reservedFor` entries.

## 9. Tests, lab, parity, smoke

- **Unit tests:** every reading of §3 and its order.
- **The pool oracle.** The scheduler's own allocator (`k8s.io/dynamic-resource-allocation/structured.NewAllocator` at v0.34.x, v0.35.x, v0.36.x and v0.37.x) runs on each pool fixture alone. The claim requests `allocationMode: All` from a class selecting the fixture's driver. Its error says incomplete ("currently being updated") or invalid ("currently invalid"), and success says complete. Klyx's reading must agree on every fixture:
  - a complete pool;
  - one slice missing;
  - an older generation beside a newer one;
  - duplicate device names across slices;
  - a missing counter set;
  - a duplicated counter set;
  - per-device and all-nodes forms.

  The generator lives in the scratchpad with its module hashes; its output is committed as testdata, as S11a's PodRequests golden is.
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
