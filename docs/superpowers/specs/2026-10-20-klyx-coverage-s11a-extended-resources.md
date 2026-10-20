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
- **Reservations never separate them.** Node-allocatable reservations accept only cpu, memory, ephemeral-storage and pid ("cannot reserve %q resource", `cmd/kubelet/app/server.go` L1413-1428).
- **A manually advertised resource** (a status-subresource patch, the documented method) is kept: the capacity setter starts from the node's existing capacity, "avoid blindly overwriting the capacity in case opaque resources are being advertised" (`setters.go` L208-212). Allocatable follows capacity for every key, but only when the setter next runs (L311-321; the docs: allocatable "is updated automatically ... asynchronously"), and validation checks the two maps independently (`validation.go` L7648). So a capacity raised by hand reads above allocatable until the kubelet's next status sync. On re-registering an existing node with an empty device-plugin directory, the kubelet zeroes every extended resource, manual ones included (`kubelet_node_status.go` L191-206; `manager.go` L1221-1230).
- **So from the node alone**, capacity above allocatable for an extended resource means unhealthy devices or a plugin disconnected in the last 5 minutes when a device plugin owns the name, and a manual capacity raise the kubelet has not yet mirrored when nothing does. The node does not say which.

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
- **A device plugin can never register a `deviceclass.resource.kubernetes.io/` name** (`devicemanager/plugin/v1beta1/server.go` L170), and the ordinary predicate rejects it: `IsExtendedResourceName` refuses any name in `*kubernetes.io/` (`pkg/apis/core/v1/helper/helpers.go` L39-62). The scheduler therefore has a second predicate, `IsDRAExtendedResourceName`, the union of ordinary extended names and the DeviceClass prefix (`pkg/scheduler/util/utils.go` L206-209).

### 1.4 The names in the field

- **NVIDIA** (k8s-device-plugin v0.20.1): `nvidia.com/gpu`. MIG "single" advertises MIG devices as `nvidia.com/gpu`; "mixed" advertises one name per profile, `nvidia.com/mig-1g.5gb` and the like (`internal/rm/rm.go` L117, L142-145). Time-slicing and MPS multiply capacity by `replicas`, per resource: sharing is configured per resource name, so a node can have an unshared `nvidia.com/gpu` beside a shared MIG profile, or different replica factors per profile. A shared resource can be renamed: `renameByDefault` advertises `<name>.shared`, and an explicit `rename` any name (`api/config/v1/replicas.go` L31-59, L83).
- **GPU Feature Discovery labels are per resource:** `<resource-name>.<suffix>`, keyed by the configured (not the renamed) name, with suffixes `product`, `count`, `replicas` and `sharing-strategy` (`none`, `mps`, `time-slicing`), plus `memory` and other attributes for MIG profiles (`internal/lm/resource.go` L127-177; one labeler per mixed profile, `internal/lm/mig-strategy.go` L266-306; the fixture in `resource_test.go` L357-408 has `nvidia.com/mig-1g.1gb.replicas` and `.sharing-strategy`). The product gets a `-SHARED` suffix when shared and not renamed. `replicas` is 0 when sharing is not configured. `nvidia.com/mig.strategy` is node-wide.
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

**Two predicates** (§1.3):
- **A node's names** are ordinary extended names (`IsExtendedResourceName`: domain-qualified, outside `kubernetes.io`, never `requests.`-prefixed; hugepages are not extended). These are what a device plugin or a manual patch advertises.
- **A pod's requests** are classified with the union, `IsDRAExtendedResourceName`: ordinary extended names and `deviceclass.resource.kubernetes.io/*`. A pod requesting only an implicit DeviceClass name still produces its reservation entry ("served by DRA") and its pod-inspector line, even on a node that advertises no extended resource.

### 2.0 Bounds

The new data is derived, so it is bounded where it is derived. Every classification reads the whole input first; a cap cuts only what is kept, and a cut is always said.
- **Names** are qualified names (at most 253 + 1 + 63 = 317 bytes, enforced by the API server) and are kept whole: the joins need them exact.
- **A node's entries** (`NodeSummary`): at most 16, accelerators first, then by name; `extendedOmitted` counts the rest. The reading of §3 inspects every key before this cut and names at most 4 resources, then "and N more". Worst case about 8 KiB encoded per node; typically under 200 bytes.
- **Vendor facts:** at most 6 per resource, for at most 8 resources per node, values clipped to 63 bytes; an omission notice says how many resources' facts were left out. Worst case about 6 KiB per node.
- **A reservations row:** at most 16 entries per node: first every name the node advertises, in the node's order, then the DRA-served and not-advertised names by amount, then by name. Each kept entry's figures are exact (computed over every pod before the cut); `omitted` counts the entries left out, and the inspector says "N more requested resources not shown". Worst case about 8 KiB per row.
- **The aggregation itself** is transient (built per evaluation, then released). Names the node advertises are summed whatever their number (the node already holds them). Other names are tracked up to 256 distinct per node; past that, the node's DRA-served and not-advertised figures read "not established: more than 256 distinct names requested", and the advertised entries stay exact.
- **Per cluster:** reservations rows up to 2 MiB encoded. Past it, rows are dropped (nodes advertising an accelerator kept first, then by name), the part reports how many, and the lens says "requested not shown for N nodes". The node rows' new fields grow with the node count, as node rows already do; their per-node worst cases are above.
- **Integers stay exact.** Extended quantities are integers (validated). Klyx reads them as int64 and sums with an overflow check; an overflow makes that figure "not established", never a wrapped number. Every capacity, allocatable and requested figure crosses to the frontend as a decimal string, and the frontend compares and subtracts with BigInt.

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
- **The row:** one per node that advertises or hosts a request for an extended resource, by node UID, carrying per resource name: requested, served by DRA, and not advertised by the node (§4), within §2.0's bounds. A fact row: no verdict, never counted, never in the ledger.
- **Dependencies:** the pods kind. Requested is only as good as the pods read: unreadable is unknown, stale is "as of", capped is a lower bound. Each is said where the number shows (§6).
- **Clock, notifications:** none.

## 3. The node reading: resources not allocatable

**The rule.** `health.Node` gains one reading, from the node alone, over every key before any display cap. For each extended resource with both keys present and allocatable below capacity, the node reads **Degraded**, "not allocatable", with each resource named by the numbers: "nvidia.com/gpu: 6 of 8 allocatable". The cause is said as an interpretation, never as a finding (§1.1): "for a device-plugin resource the kubelet reports unhealthy devices, or a plugin disconnected in the last 5 minutes, this way; a manually advertised resource reads so until the kubelet's next sync. The node does not say which." The rule is `node.resources-not-allocatable`; the native text is the kubelet's numbers ("nvidia.com/gpu allocatable 6, capacity 8"). Units are not allocatable to new pods either way, which is why it is Degraded.

**Its place among the rows** (N1-N6, `health/node.go`):
- NotReady (N3) and an unknown Ready (N1, N2) stay ahead of it.
- With pressure (N4), the node is Degraded once, its label naming both ("MemoryPressure · not allocatable").
- A cordoned node (N5) with a resource not allocatable is Degraded, as with pressure.

**What it is not:**
- **Capacity 0 with the key present** is not a fault, and the reading is neutral: "0 advertised". For a device-plugin resource that means a plugin gone for more than 5 minutes, a kubelet restart before the plugin re-registered, or a plugin reporting no devices; a manually advertised 0 reads the same, and a DaemonSet that registers zero devices on a non-GPU node would otherwise light every node.
- **Allocatable absent while capacity is present** (a newly advertised resource, before the kubelet's next sync) reads "allocatable not yet reported".

## 4. Requested, without double counting

**The formula** is the pod's desired request, restricted to extended names: regular and sidecar containers summed, the init-container maximum, then overhead (§1.2). It is S6's `Desired` path, not its status-aware reservation: only cpu and memory resize, so the spec is what the device manager allocated at admission, and every supported minor computes the same figure (pod-level resources never carry an extended name). Golden values prove it: S6's generator modules (v0.32.13 to v0.37.1) gain fixtures with extended names, and `PodRequests` with status resources off, per module, must equal Klyx's figure.

**The pods:** bound to the node (`spec.nodeName`) and not terminal. A terminating pod counts until it is terminal.

**Classification comes first.** Each (container, resource) pair is classified with the pod predicate (§2) before any aggregation, and each class is then aggregated by the formula separately: the kubelet removes pairs from containers before computing the pod's request, so subtracting exclusions from an already aggregated pod maximum would be wrong. The classes, mirroring the kubelet's admission rule (§1.3):
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

Any name in `nvidia.com/` or `amd.com/` is an accelerator, since those vendors' device plugins own the domains and a shared resource can be renamed within them.

**A unit is the resource's, not a GPU.** Klyx never converts units into physical GPUs. The unit of each resource:
- `nvidia.com/mig-*`: a MIG device;
- `nvidia.com/gpu` (and its renames): a GPU, except under `nvidia.com/mig.strategy` `single`, where it is a MIG device;
- AMD partition names: a partition ("<compute> compute, <memory> memory");
- anything else: the resource's own unit, unexplained.

**NVIDIA facts are per resource,** never borrowed from another resource. For an advertised name R:
- R's own labels, `R.<suffix>`: `product`, `count`, `memory`, `replicas`, `sharing-strategy`;
- for `B.shared` without labels of its own, B's labels, said "renamed from B (the plugin's default shared rename)"; any other rename gets no NVIDIA facts ("not labelled by GPU Feature Discovery");
- **sharing:** `replicas` above 1 reads "shared (<strategy>): each <unit> is advertised N times, so these count shares of a <unit>"; a strategy of `time-slicing` or `mps`, or a `-SHARED` product, without a replica count above 1 reads "shared (replica count not reported)". Klyx never infers a replica count.
- **MIG:** the node-wide `nvidia.com/mig.strategy` is a node fact ("MIG mixed: each profile is its own resource").

**AMD facts are the node's:** the labeller's `amd.com/gpu.product-name`, `.vram`, `.family`, `.driver-version` and the partition label (both spellings, `beta.amd.com` as a fallback), shown on every `amd.com/` resource of the node as "the node's AMD GPUs". A per-value form (`<kind>.<value>=<count>`) reads "mixed: <value> ×<count>".

The caps are §2.0's.

**Requested is a reservation, not utilisation.** Every surface says "requested", never "used". GPU utilisation is not read (§10).

## 6. What Klyx shows

**Nothing, where nothing exists.** No column, block or line renders on a cluster whose nodes advertise no extended resource and whose pods request none.

**The Nodes lens:**
- **A "GPU" column**, when any node advertises an accelerator: "requested / allocatable" ("6 / 8"), with "· 2 not allocatable" when capacity is higher, and "0 advertised" for a present key at 0. A node with several accelerator names (mixed MIG, AMD partitions) reads "3 resources", each named in its tooltip.
- **Requested carries its read's qualification** (S10's lesson): "?" when the reservations part has no current read, "*" when stale, "+" when the pods read is capped, "+*" both; "?" also for a node whose row the per-cluster bound dropped, or whose entry the per-node bound cut.
- **The footer** adds accelerator totals per name: "16 nvidia.com/gpu allocatable (12 requested)".
- **The verdict** "not allocatable" lights the node like pressure does.

**The node inspector**, a block "Extended resources", one line per name, accelerators first:
- "nvidia.com/gpu · 6 requested · 6 allocatable · 8 capacity · 2 not allocatable", plus "· 1 served by DRA" and "· 2 not advertised by this node" where non-zero;
- the vendor facts and the unit explanation;
- the not-yet-reported and 0-advertised wordings of §3;
- requested's qualification, said in words ("requested as of 10:42: the pods read is failing").

**The pod inspector:** each container's extended requests (request equals limit, so one figure), implicit DeviceClass names included, with "served by DRA (claim <name>)" for a mapped pair. S6's "DRA device resources are not counted" line becomes "extended resources served by DRA are listed per container; their devices are S11b's".

**Inspector continuity.** Node status and labels change without changing the Nodes lens's detail signature, so the node inspector renders the new evidence from the current joined rows (node row plus reservations row), never from the detail fetch. The pod inspector reads its detail on demand: `workloads.EvidenceRev` gains `status.extendedResourceClaimStatus`, so a mapping written after binding re-reads the detail. UID and request fencing are unchanged.

## 7. AI and chat

- **Explain** (the nodes allowlist): a node's extended resources (name, capacity, allocatable, accelerator class) and its vendor facts. Names, numbers and product strings; no identifier.
- **The reservations part** gets its own allowlist entry (resource names and the three figures), though no Explain anchors on it in S11a.
- **Chat:** unchanged. A node's status (capacity and allocatable) and its labels already cross, and a container's resources already cross.

## 8. The budget

- **No new watch, and no projection change.** The nodes projection already keeps the full status; the pods projection already keeps every field §4 reads.
- **The derived data is bounded** as §2.0 states: per node, per row, per cluster, and in the transient aggregation, each with what is said when the bound is reached.
- **The reservations part** evaluates once per capture, in one pass over pods: O(pods), with integer arithmetic for the extended names only. Its rows exist only for nodes with extended resources, and change only when a request or an advertisement does.
- **Measured with `TestLoad`:** the large fixture gains an accelerator on a quarter of its nodes and requests on a tenth of its pods. The steady heap, the first complete summary and the full-evaluation time are recorded before and after, beside S10's figures.
- **Measured at the bounds too:** a node advertising 100 names of 317 bytes; 50 pods on one node requesting 1,000 distinct not-advertised names each; the 2 MiB rows bound reached. Each records the encoded bytes, the retained heap and the evaluation time, and proves the omission is said.

## 9. Tests, lab, parity, smoke

**Unit and projection tests:**
- the extended-name predicate and the accelerator classes (every name in §5, and near misses such as `nvidia.com/gpu-foo`, `kubernetes.io/x`, `hugepages-2Mi`, `requests.nvidia.com/gpu`);
- the reading: allocatable below capacity (one and several names, more than 4 named), a manual capacity raised before the kubelet's sync (16 / 8, read as not allocatable with the neutral cause), capacity 0 with the key, allocatable absent, combined with pressure, cordoned, NotReady ahead;
- requested: regular, sidecar and init containers, overhead, terminal and terminating pods, a pod on another node, unscheduled pods, a DRA-mapped pair, a name the node does not advertise, a mapping on a container that requests nothing, and classification before the init maximum (an init container whose excluded pair would otherwise shrink the counted maximum);
- the implicit DRA name through the whole path: a pod requesting only `deviceclass.resource.kubernetes.io/gpu` on a node with no extended resource produces its reservation entry ("served by DRA") and its pod-inspector line;
- NVIDIA facts per resource: an unshared `nvidia.com/gpu` beside a shared MIG profile; two profiles with different replica factors; MIG single with sharing (the unit is a MIG device); a `.shared` rename with its base's labels; an explicit rename with none; a strategy without a replica count; the facts cap with its omission notice;
- the bounds of §2.0: 16 entries, 4 names in the reading, 256 tracked names, the 2 MiB rows bound, int64 overflow, and large integers across the JSON boundary (a value above 2^53 compared exactly);
- inspector continuity: a node-row-only update re-renders the node block; a mapping written after binding re-reads the pod detail (mounted tests, with UID and request fencing);
- the golden values (§4) across the six modules;
- projection equality: `NodeSummary` and the reservations from raw and projected objects;
- the qualifications: pods unreadable, stale, capped; nodes stale;
- vendor facts: both AMD spellings, the per-value form, the `-SHARED` product suffix, the cap.

**The lab** (`lab-s11a` on kind-klyx, labelled and namespaced, with a teardown):
- **A real device plugin:** the sample device plugin as a DaemonSet in `klyx-s11a-lab`, registering `example.com/resource` (2 devices) through the kubelet's device manager, and a pod requesting 1.
- **Vocabulary fixtures,** stated as such: the node status advertises `nvidia.com/gpu: 8` and `nvidia.com/mig-1g.5gb: 7` through the status subresource (the documented method; the kubelet keeps them and mirrors allocatable), with GPU Feature Discovery labels per resource on the node: `nvidia.com/mig.strategy=mixed`; `nvidia.com/gpu.*` time-sliced with 4 replicas; `nvidia.com/mig-1g.5gb.*` unshared, with its own product and memory. Pods request 3 `nvidia.com/gpu` and 1 `nvidia.com/mig-1g.5gb`. The apply script re-patches after any node restart (§1.1's zeroing).
- **The live reading:** the plugin's DaemonSet is stopped (its nodeSelector patched to a label no node carries, so the DaemonSet controller cannot replace the pod; the object stays for the restore). `example.com/resource` then reads capacity 2, allocatable 0, and the node "not allocatable", for 5 minutes; then capacity reads 0 ("0 advertised"). Restoring the nodeSelector re-registers it. The lab script asserts each node transition (allocatable 0; then capacity 0; then 2/2) before any capture is taken.
- **Not on kind:** DRA-served requests (S11b brings a DRA driver), real GPUs and real vendor labels. DRA-mapped pairs are unit-tested only in S11a.

**The parity gate:** the sample plugin in the baseline on `kind-klyx-parity`, and case 11: the plugin's DaemonSet stopped as above, the node "not allocatable" (degraded, counted once); reverted, the DaemonSet restored and the node ready. The case asserts the node transition first and runs within the 5-minute grace. The reservations part joins the oracle's parts and `ROW_PARTS` as an evidence part.

**The native smoke:** a step on the Nodes lens: the GPU column reads "2 resources" for the kind node (two accelerator names), and its inspector's "Extended resources" block reads `nvidia.com/gpu` "3 requested · 8 allocatable" with "shared (time-slicing): each GPU is advertised 4 times", `nvidia.com/mig-1g.5gb` "1 requested · 7 allocatable" with its own facts, and `example.com/resource` "1 requested · 2 allocatable".

**Captures** (night and day, at 1440 and 1024): the Nodes lens with the column; the node inspector healthy, then with the plugin stopped (not allocatable), then after the grace (0 advertised); a pod inspector with an extended request.

## 10. Not in S11a

- **DRA's own objects** (DeviceClasses, ResourceSlices, ResourceClaims and their allocations), the devices DRA allocated, and pod device health (`allocatedResourcesStatus`): S11b.
- **GPU utilisation** (DCGM, ROCm SMI): a later candidate, kept apart from requests as the roadmap asks.
- **Pending pods' extended requests** across the cluster: the Pods lens already shows a Pending pod with the scheduler's own reason.
- **Cluster-level extended resources** (a scheduler extender's), beyond saying "not advertised by this node".
- **The headroom simulation** stays cpu and memory. Extended resources join it only with a design of their own: a displaced GPU pod needs a node with the same accelerator.
- **MIG or partition reconfiguration** state (NVIDIA's mig-manager, AMD's partition labels in transit).

## 11. Revision 1: Codex's design review of 5240621

| # | Finding | Where answered |
|---|---|---|
| P2 | A capacity gap does not establish the device-plugin cause from a node alone: a manual capacity raise reads above allocatable until the kubelet's next sync | §1.1 states it (the setter mirrors allocatable only when it runs; the maps are validated independently). §3 keeps the observable reading, renamed "not allocatable", with the numbers as the finding and the cause as an interpretation the node cannot settle; the zero wording is neutral; "manual resources never light" is withdrawn. §9 adds the capacity-only raise |
| P2 | The resource predicate excluded the implicit DRA name §4 promises to show | §1.3 and §2: two predicates, the ordinary one for a node's names and the union (`IsDRAExtendedResourceName`) for a pod's requests, applied before classification and aggregation. §9 tests an implicit-only pod through the whole path |
| P2 | NVIDIA sharing is per resource, and the unit can be a MIG device | §1.4 reads the per-resource labels from source. §5 attributes facts per resource (the default `.shared` rename takes its base's labels; any other rename none), states the unit per resource (MIG device for profiles and under MIG single), and never infers a replica count. §9 adds mixed factors, shared beside unshared, MIG single with sharing and the facts cap |
| P2 | The new derived data had no numeric bounds | §2.0: per-node entries (16), vendor facts (6 per resource, 8 resources), reservation rows (16 entries, exact kept figures, omitted counted), the transient aggregation (256 tracked names per node), 2 MiB of rows per cluster, int64 with overflow as "not established", and decimal strings with BigInt across the JSON boundary. §8 measures the worst shapes |

Codex's implementation guidance, taken: the lab stops the DaemonSet (not its pod) and asserts each node transition before captures (§9); inspector continuity renders the node block from the joined rows and adds the claim mapping to `EvidenceRev` (§6); classification precedes the init maximum, with overhead pinned separately (§4, §9).
