# Coverage S11a: extended resources

The roadmap's S11a (`2026-10-16-klyx-coverage-roadmap.md` §2): extended resources (`amd.com/gpu`, `nvidia.com/gpu`), with capacity, allocatable and requested per node, kept apart from utilisation. DRA-backed extended resources are not double-counted. S11b (DRA's own objects) follows.

Today the Observer keeps each node's whole `status.capacity` and `status.allocatable` (`ProjectNode` drops only images and annotations), and S6's request model sums every resource name. Every output is then cut to CPU and memory: `NodeSummary`, `cpuMem()`, the headroom simulation, every DTO and the UI. A GPU shows only in a node's Describe text and its manifest. S6 left "DRA node-allocatable claims" to this slice, "not modelled, and said". Nothing reads `resource.k8s.io`.

The owner has not authorised a read-only check on the ROCm homelab (C6), so S11a's AMD and NVIDIA readings are verified on kind fixtures and a real sample device plugin only (§9).

## 1. The APIs, read from the source

Sources: Kubernetes v1.34.0, v1.35.0, v1.36.0 and v1.37.0 (kubelet device manager and node-status setters, scheduler `noderesources` and `dynamicresources`, kubelet admission, API validation, `component-helpers`); KEP-5004 at kubernetes/enhancements 5b503a75; the Kubernetes docs ("Device Plugins", "Advertise Extended Resources for a Node", "Resource Management for Pods and Containers"); NVIDIA k8s-device-plugin v0.20.1, AMD ROCm k8s-device-plugin v1.31.0.11 and GPU Operator v1.5.1, Intel device plugins v0.37.1, the AWS Neuron SDK v2.33.0, and the EKS and AKS GPU guides. File and line references below are to v1.37.0 unless a version is named.

### 1.1 A device plugin's resource in node status

- **Capacity counts healthy and unhealthy devices; allocatable counts healthy ones.** The device manager keeps both sets per resource (`devicemanager/manager.go` L323-327). `GetCapacity` sets capacity and allocatable to the healthy count, then adds the unhealthy count to capacity only (L488-503). The docs say the same: the kubelet "will decrease the allocatable count for this resource ... Capacity count for the resource will not change".
- **A plugin that disconnects** has every device marked unhealthy (`markResourceUnhealthy`, L256-283, L447-458): capacity stays, allocatable reads 0. After `endpointStopGracePeriod` (5 minutes, `types.go` L125) the resource is "deleted": the node-status setter writes capacity 0 and **keeps the key**, "to indicate that the resource is managed by device plugin and had been registered before" (`nodestatus/setters.go` L274-285). In 1.37 a resource can have several plugin endpoints, and only the last one's disconnect marks it unhealthy (L270-282); in 1.36 and earlier any disconnect did.
- **A kubelet restart** recreates every registered resource empty, "so that the resource capacity will stay zero till the corresponding device plugin re-registers" (L545-571): capacity and allocatable read 0 while running pods keep their devices.
- **Nothing else separates them.** Node-allocatable reservations accept only cpu, memory, ephemeral-storage and pid ("cannot reserve %q resource", `cmd/kubelet/app/server.go` L1413-1428). So for an extended resource, capacity minus allocatable is exactly the devices that are unhealthy, or a plugin that disconnected in the last 5 minutes.
- **A manually advertised resource** (a status-subresource patch, the documented method) is kept: the capacity setter starts from the node's existing capacity, "avoid blindly overwriting the capacity in case opaque resources are being advertised" (`setters.go` L208-212). Allocatable follows capacity for every key (L311-321), so it never reads below. One exception: on re-registering an existing node with an empty device-plugin directory, the kubelet zeroes every extended resource, manual ones included (`kubelet_node_status.go` L191-206; `manager.go` L1221-1230).

### 1.2 A pod's request for an extended resource

- **Request equals limit, an integer.** An extended resource cannot be overcommitted (`validation.go` L8298-8306, L8545-8553).
- **The pod's effective request** is `component-helpers`' `PodRequests`: regular and restartable (sidecar) init containers summed; each regular init container plus the sidecars declared before it; the maximum of the two; then `spec.overhead` (`resource/helpers.go` L152-173, L235-289). Overhead may legally carry an extended resource (`validation.go` L369-372).
- **Pod-level resources never carry one.** `supportedPodLevelResources` is cpu and memory, plus hugepages (`helpers.go` L67-78); validation refuses other names (`validation.go` L7889-7900).
- **Only cpu and memory resize**, so an extended request never changes after admission.
- **Terminal pods do not count.** The scheduler's pod informer selects `status.phase!=Succeeded,status.phase!=Failed` (`scheduler.go` L651-656, the same in 1.34-1.36); `kubectl describe node` lists "Non-terminated Pods" the same way. A terminating pod still counts until it is terminal.

### 1.3 DRA-backed extended resources (KEP-5004)

- **The API.** A DeviceClass may name an extended resource (`spec.extendedResourceName`); every class also has an implicit name, `deviceclass.resource.kubernetes.io/<class>`. When a pod's extended request is served by DRA, the scheduler creates a special ResourceClaim (`<pod>-extended-resources-*`) and records it in `pod.status.extendedResourceClaimStatus`: the claim name and `requestMappings` of container name, resource name and request name.
- **The feature gate `DRAExtendedResource`:** alpha and off in 1.34 and 1.35, beta and on in 1.36, GA and locked in 1.37. kind-klyx (v1.37.0) has it on.
- **One node serves a name one way.** "Either the resource is advertised through `ResourceSlice`, or `Node`'s status.capacity", and another node may serve the same name the other way (KEP-5004 L300-305). The scheduler decides per node: from 1.35, a name whose allocatable is 0 or absent on that node goes to DRA when a DeviceClass maps it (`noderesources/fit.go` L269-291; `extendeddynamicresources.go` L241-250); in 1.34 only an absent key did.
- **The scheduler's own bookkeeping double-counts.** `NodeInfo.Requested` adds every scalar from `PodRequests`, DRA-served ones included (`framework/types.go` L463-474); `Fits` merely skips them. Its numbers are not reusable.
- **The kubelet's admission rule is the one to mirror.** `removeMissingExtendedResources` (`lifecycle/predicate.go` L346-381) drops a (container, resource) pair that `requestMappings` lists, and any extended name missing from the node's allocatable. The mapping check exists from 1.36; 1.34 and 1.35 drop only missing names.
- **A device plugin can never register a `deviceclass.resource.kubernetes.io/` name** (`devicemanager/plugin/v1beta1/server.go` L170).

### 1.4 The names in the field

- **NVIDIA** (k8s-device-plugin v0.20.1): `nvidia.com/gpu`. MIG "single" advertises MIG devices as `nvidia.com/gpu`; "mixed" advertises one name per profile, `nvidia.com/mig-1g.5gb` and the like (`internal/rm/rm.go` L117, L142-145). Time-slicing and MPS multiply capacity by `replicas` (8 GPUs with 10 replicas advertise 80), and `renameByDefault` advertises `nvidia.com/gpu.shared`. GPU Feature Discovery labels: `nvidia.com/gpu.product` (suffixed `-SHARED` when shared but not renamed), `.count`, `.memory` (MiB), `.replicas`, `.sharing-strategy` (`none`, `mps`, `time-slicing`), and `nvidia.com/mig.strategy`.
- **AMD** (ROCm k8s-device-plugin, GPU Operator): `amd.com/gpu`; with `resource_naming_strategy=mixed`, one name per partition type, `amd.com/<compute>_<memory>` (`cpx_nps4`, `spx_nps1`; compute SPX, DPX, QPX, CPX; memory NPS1, 2, 4, 8). The node labeller writes `amd.com/gpu.<kind>` and `beta.amd.com/gpu.<kind>` (`family`, `vram`, `product-name`, `device-id`, `cu-count`, `driver-version`, `compute-memory-partition` and others), as `<kind>=<value>` when every GPU agrees, else `<kind>.<value>=<count>`. The docs spell the partition label `amd.com/compute-memory-partition`; the code emits `amd.com/gpu.compute-memory-partition`.
- **Others:** Intel `gpu.intel.com/i915` and `gpu.intel.com/xe`; Habana `habana.ai/gaudi`; AWS Neuron `aws.amazon.com/neuron` and `aws.amazon.com/neuroncore`.
- **EKS and AKS** run the same NVIDIA device plugin (installed separately on AL2023 GPU AMIs, included on Bottlerocket NVIDIA; managed and not configurable on EKS Auto Mode; installed by the user, or by AKS's managed GPU preview). AKS installs no AMD driver or plugin; the AMD GPU Operator is the documented route.

### 1.5 kind-klyx

v1.37.0, one arm64 node. `resource.k8s.io/v1` is served (DeviceClasses, ResourceSlices, ResourceClaims, ResourceClaimTemplates, DeviceTaintRules), with none present. The node advertises only cpu, memory, ephemeral-storage and pods. The Kubernetes e2e sample device plugin (`registry.k8s.io/e2e-test-images/sample-device-plugin:1.8.0`, multi-arch with arm64) registers `example.com/resource` with two healthy devices through the real device manager.

## 2. What S11a reads, and where

**A node's extended resources come from the node alone.** The nodes part already projects them. `NodeSummary` gains, per extended resource name in capacity or allocatable:
- capacity and allocatable, each `nil` when the key is absent;
- its class: an **accelerator** (§5) or another extended resource;
- the vendor facts in the node's labels (§5).

An extended resource name is `IsExtendedResourceName`: a domain-qualified name outside `kubernetes.io`, never `requests.`-prefixed. Hugepages are not extended.

**What pods reserve on a node is a join,** so it lives in a part of its own: a new task `extended` with one evidence part `reservations` (Counted: nodes; Deps: pods). It is not added to the nodes part, for two reasons:
- the nodes part is a ledger part, and a ledger part's continuity requires every dependency to be live (`liveForLedger`): a pods read that fails or relists would interrupt the nodes' own evidence;
- a task's coverage is the worst of its parts' (`rollup`), so a pods read that is forbidden while nodes are readable would mark Nodes unreadable.

The `extended` task has no area of its own: no navigation entry, no Needs you line, no controller. The Nodes lens joins its rows to node rows by UID. Its coverage qualifies only the requested figures (§6).

### 2.1 The source contracts (C1)

**The nodes part** (changed: one new reading, no new input):
- **Acquisition:** the existing projected node watch; the projection is unchanged.
- **Versions:** the node-status semantics of §1.1 hold from 1.34 to 1.37.
- **The counted entity:** a node by UID, as today.
- **Dependencies:** none.
- **Clock:** none of Klyx's. The kubelet's 5-minute grace changes capacity, and that change arrives as a node update.
- **Notification eligibility:** unchanged. The new reading is Degraded, and a health notification opens only on a fault (`healthnotify`), so it never notifies: a device-plugin rollout, during which every GPU node reads degraded until its plugin re-registers, sends nothing.
- **No synthetic evidence:** the reading is the kubelet's own numbers.

**The reservations part** (new):
- **Acquisition:** the existing projected node and pod watches; no new watch. `ProjectPod` already keeps each container's resources, `spec.overhead`, `status.phase` and `status.extendedResourceClaimStatus`.
- **The row:** one per node that advertises or hosts a request for an extended resource, by node UID, carrying per resource name: requested, served by DRA, and not advertised by the node (§4). A fact row: no verdict, never counted, never in the ledger.
- **Dependencies:** the pods kind. Requested is only as good as the pods read: unreadable is unknown, stale is "as of", capped is a lower bound. Each is said where the number shows (§6).
- **Clock, notifications:** none.

## 3. The node reading: devices not allocatable

**The rule.** `health.Node` gains one reading, from the node alone. For each extended resource with both keys present and allocatable below capacity, the node reads **Degraded**, "devices not allocatable", with each resource named: "nvidia.com/gpu: 2 of 8 not allocatable (unhealthy, or its device plugin disconnected in the last 5 minutes)". The rule is `node.devices-not-allocatable`; the native text is the kubelet's numbers ("nvidia.com/gpu allocatable 6, capacity 8").

**Its place among the rows** (N1-N6, `health/node.go`):
- NotReady (N3) and an unknown Ready (N1, N2) stay ahead of it.
- With pressure (N4), the node is Degraded once, its label naming both ("MemoryPressure · devices not allocatable").
- A cordoned node (N5) with devices not allocatable is Degraded, as with pressure.

**What it is not:**
- **Capacity 0 with the key present** is not a fault. It means a plugin gone for more than 5 minutes, a kubelet restart before the plugin re-registered, or a plugin reporting no devices, and a DaemonSet that registers zero devices on a non-GPU node would otherwise light every node. The reading says "0 advertised: its device plugin is not registered, or reports no devices".
- **Allocatable absent while capacity is present** (a newly advertised resource, before the kubelet's next sync) reads "allocatable not yet reported".
- **A manually advertised resource** never reads below capacity (§1.1), so it never lights the rule.

## 4. Requested, without double counting

**The formula** is the pod's desired request, restricted to extended names: regular and sidecar containers summed, the init-container maximum, then overhead (§1.2). It is S6's `Desired` path, not its status-aware reservation: only cpu and memory resize, so the spec is what the device manager allocated at admission, and every supported minor computes the same figure (pod-level resources never carry an extended name). Golden values prove it: S6's generator modules (v0.32.13 to v0.37.1) gain fixtures with extended names, and `PodRequests` with status resources off, per module, must equal Klyx's figure.

**The pods:** bound to the node (`spec.nodeName`) and not terminal. A terminating pod counts until it is terminal.

**Not double-counted**, mirroring the kubelet's admission rule (§1.3), per (container, resource) pair:
- listed in `status.extendedResourceClaimStatus.requestMappings`: **served by DRA**, not counted against the node;
- named `deviceclass.resource.kubernetes.io/*`: served by DRA;
- an extended name absent from the node's allocatable: **not advertised by the node**, not counted against it. On 1.36 and later with the gate on this is a cluster-level resource; on 1.34 and 1.35, or with the gate off, it may also be DRA-served without a mapping. The reading says both.

The two excluded amounts are kept per resource and shown beside the counted one ("1 requested · 2 served by DRA"). Which devices DRA allocated is S11b's.

**Overhead** naming an extended resource (legal, and unseen in practice) counts against the node when the node advertises the name, and is "not advertised" otherwise. A DRA mapping names containers only, so overhead is never served by DRA.

## 5. Vocabulary

**An accelerator** is a name in this list. Every other extended resource is shown too, unclassified.

| Vendor | Names |
|---|---|
| NVIDIA | `nvidia.com/gpu`, `nvidia.com/gpu.shared`, `nvidia.com/mig-*` |
| AMD | `amd.com/gpu`, `amd.com/<spx|dpx|qpx|cpx>_<nps1|nps2|nps4|nps8>` |
| Intel | `gpu.intel.com/i915`, `gpu.intel.com/xe` |
| Habana | `habana.ai/*` |
| AWS | `aws.amazon.com/neuron`, `aws.amazon.com/neuroncore` |

**A unit is the resource's, not a GPU.** Klyx never converts units into physical GPUs. Where the labels say how a unit relates to a GPU, the inspector says so:
- **Sharing:** `nvidia.com/gpu.sharing-strategy` `time-slicing` or `mps`, or `nvidia.com/gpu.replicas` above 1, reads "shared: each GPU is advertised N times, so these count shares, not GPUs".
- **MIG:** `nvidia.com/mig.strategy` `mixed` reads "MIG: each profile is its own resource"; `single` reads "MIG: these count MIG devices".
- **AMD partitions:** a partition name reads "partitioned: <compute> compute, <memory> memory".

**Vendor facts** are read from labels only, never inferred:
- NVIDIA: `nvidia.com/gpu.product`, `.memory`, `.count`, `.replicas`, `.sharing-strategy`, `nvidia.com/mig.strategy`;
- AMD: `amd.com/gpu.product-name`, `.vram`, `.family`, `.driver-version`, and the partition label in both spellings, with the `beta.amd.com` prefix as a fallback. A per-value form (`<kind>.<value>=<count>`) reads "mixed: <value> ×<count>".

At most 12 facts per node, values clipped to 63 characters.

**Requested is a reservation, not utilisation.** Every surface says "requested", never "used". GPU utilisation is not read (§10).

## 6. What Klyx shows

**Nothing, where nothing exists.** No column, block or line renders on a cluster whose nodes advertise no extended resource and whose pods request none.

**The Nodes lens:**
- **A "GPU" column**, when any node advertises an accelerator: "requested / allocatable" ("6 / 8"), with "· 2 not allocatable" when capacity is higher, and "0 advertised" for a present key at 0. A node with several accelerator names (mixed MIG, AMD partitions) reads "3 resources", each named in its tooltip.
- **Requested carries its read's qualification** (S10's lesson): "?" when the reservations part has no current read, "*" when stale, "+" when the pods read is capped, "+*" both.
- **The footer** adds accelerator totals per name: "16 nvidia.com/gpu allocatable (12 requested)".
- **The verdict** "devices not allocatable" lights the node like pressure does.

**The node inspector**, a block "Extended resources", one line per name, accelerators first:
- "nvidia.com/gpu · 6 requested · 6 allocatable · 8 capacity · 2 not allocatable", plus "· 1 served by DRA" and "· 2 not advertised by this node" where non-zero;
- the vendor facts and the unit explanation;
- the not-yet-reported and 0-advertised wordings of §3;
- requested's qualification, said in words ("requested as of 10:42: the pods read is failing").

**The pod inspector:** each container's extended requests (request equals limit, so one figure), with "served by DRA (claim <name>)" for a mapped pair. S6's "DRA device resources are not counted" line becomes "extended resources served by DRA are listed per container; their devices are S11b's".

## 7. AI and chat

- **Explain** (the nodes allowlist): a node's extended resources (name, capacity, allocatable, accelerator class) and its vendor facts. Names, numbers and product strings; no identifier.
- **The reservations part** gets its own allowlist entry (resource names and the three figures), though no Explain anchors on it in S11a.
- **Chat:** unchanged. A node's status (capacity and allocatable) and its labels already cross, and a container's resources already cross.

## 8. The budget

- **No new watch, and no projection change.** The nodes projection already keeps the full status; the pods projection already keeps every field §4 reads.
- **The reservations part** evaluates once per capture, in one pass over pods: O(pods), with integer arithmetic for the extended names only. Its rows exist only for nodes with extended resources, and change only when a request or an advertisement does.
- **Measured with `TestLoad`:** the large fixture gains an accelerator on a quarter of its nodes and requests on a tenth of its pods. The steady heap, the first complete summary and the full-evaluation time are recorded before and after, beside S10's figures.

## 9. Tests, lab, parity, smoke

**Unit and projection tests:**
- the extended-name predicate and the accelerator classes (every name in §5, and near misses such as `nvidia.com/gpu-foo`, `kubernetes.io/x`, `hugepages-2Mi`, `requests.nvidia.com/gpu`);
- the reading: allocatable below capacity (one and several names), capacity 0 with the key, allocatable absent, a manual resource, combined with pressure, cordoned, NotReady ahead;
- requested: regular, sidecar and init containers, overhead, terminal and terminating pods, a pod on another node, unscheduled pods, a DRA-mapped pair, a `deviceclass.resource.kubernetes.io/` name, a name the node does not advertise, and a mapping on a container that requests nothing;
- the golden values (§4) across the six modules;
- projection equality: `NodeSummary` and the reservations from raw and projected objects;
- the qualifications: pods unreadable, stale, capped; nodes stale;
- vendor facts: both AMD spellings, the per-value form, the `-SHARED` product suffix, the cap.

**The lab** (`lab-s11a` on kind-klyx, labelled and namespaced, with a teardown):
- **A real device plugin:** the sample device plugin as a DaemonSet in `klyx-s11a-lab`, registering `example.com/resource` (2 devices) through the kubelet's device manager, and a pod requesting 1.
- **Vocabulary fixtures,** stated as such: the node status advertises `nvidia.com/gpu: 8` and `nvidia.com/mig-1g.5gb: 7` through the status subresource (the documented method; the kubelet keeps them and mirrors allocatable), with GPU Feature Discovery labels on the node (MIG mixed; time-slicing with 4 replicas; a product, memory and count). Pods request 3 `nvidia.com/gpu` and 1 `nvidia.com/mig-1g.5gb`. The apply script re-patches after any node restart (§1.1's zeroing).
- **The live reading:** deleting the plugin's pod makes `example.com/resource` read capacity 2, allocatable 0, and the node "devices not allocatable", for 5 minutes. Then capacity reads 0 ("0 advertised"). Restoring the DaemonSet re-registers it.
- **Not on kind:** DRA-served requests (S11b brings a DRA driver), real GPUs and real vendor labels. DRA-mapped pairs are unit-tested only in S11a.

**The parity gate:** the sample plugin in the baseline on `kind-klyx-parity`, and case 11: the plugin removed, the node "devices not allocatable" (degraded, counted once); reverted, the plugin restored and the node ready. The case runs within the 5-minute grace. The reservations part joins the oracle's parts and `ROW_PARTS` as an evidence part.

**The native smoke:** a step on the Nodes lens: the GPU column reads "2 resources" for the kind node (two accelerator names), and its inspector's "Extended resources" block reads `nvidia.com/gpu` "3 requested · 8 allocatable" with "shared: each GPU is advertised 4 times", `nvidia.com/mig-1g.5gb` "1 requested · 7 allocatable", and `example.com/resource` "1 requested · 2 allocatable".

**Captures** (night and day, at 1440 and 1024): the Nodes lens with the column; the node inspector healthy, then with the plugin stopped (devices not allocatable), then after the grace (0 advertised); a pod inspector with an extended request.

## 10. Not in S11a

- **DRA's own objects** (DeviceClasses, ResourceSlices, ResourceClaims and their allocations), the devices DRA allocated, and pod device health (`allocatedResourcesStatus`): S11b.
- **GPU utilisation** (DCGM, ROCm SMI): a later candidate, kept apart from requests as the roadmap asks.
- **Pending pods' extended requests** across the cluster: the Pods lens already shows a Pending pod with the scheduler's own reason.
- **Cluster-level extended resources** (a scheduler extender's), beyond saying "not advertised by this node".
- **The headroom simulation** stays cpu and memory. Extended resources join it only with a design of their own: a displaced GPU pod needs a node with the same accelerator.
- **MIG or partition reconfiguration** state (NVIDIA's mig-manager, AMD's partition labels in transit).
