# Coverage S4: on-prem load balancing

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S4. It builds on C3 (S3a §2): the shared Services kind gains a third consumer. Revision 1 answers Codex's design review of b887823 (§10).

The roadmap's S4 (owner: "yes it should"):
- **The diagnostic:** a provider-neutral one for a `LoadBalancer` Service with no assigned address. It names the requested class (`loadBalancerClass`) and the controller that should serve it, the allocation status, the Service's events, and how long it has been pending.
- **Needs you:** a Service pending past a grace period reaches it.
- **Linked controller evidence, where present:**
  - MetalLB `IPAddressPool` and `L2Advertisement`/`BGPAdvertisement`;
  - Cilium LB IPAM `CiliumLoadBalancerIPPool`, with its conflict and exhaustion conditions;
  - k3s ServiceLB, through its `svclb-*` pods.
- **Allocation is not proof of working traffic or BGP.** Full BGP session diagnosis stays a later candidate.

## 1. The API, read from the source

Read from upstream source at:
- `k8s.io/api` and `k8s.io/cloud-provider` v0.37.1, and kubernetes v1.37.1;
- MetalLB v0.16.1, Cilium v1.20.3, k3s v1.37.1+k3s1, kube-vip v1.3.0.

Notes: Prax reference 91b51923 ("On-prem LoadBalancer controller facts for S4"), with file:line citations.

**Core.**
- **A pending LoadBalancer** is simply an empty `status.loadBalancer.ingress`: there is no standard pending condition.
- **An ingress entry** has `ip`, `hostname`, `ipMode` (VIP or Proxy) and `ports[]` with a per-port `error`.
- **The one built-in condition** is `LoadBalancerPortsError`.
- **`spec.loadBalancerClass`:**
  - unset means "the default implementation";
  - a default implementation must ignore a Service that sets a class;
  - it is immutable once set.
- **The cloud service controller** acts only on unset-class Services. Its events, from `service-controller`:
  - `EnsuringLoadBalancer` and `EnsuredLoadBalancer`;
  - Warnings `SyncLoadBalancerFailed`, `UnAvailableLoadBalancer` and `UpdateLoadBalancerFailed`.
- **With no controller at all**, nothing handles an unset class: the Service stays pending with no event.

**MetalLB.**
- **Class:** it serves unset-class Services unless started with `--lb-class`, a flag Klyx cannot read.
- **Allocation failures:** Warning event `AllocationFailed`, with errors such as:
  - "no available IPs";
  - "unknown pool";
  - "is not allowed in config";
  - port and sharing conflicts.
- **Silent cases:**
  - with no pools it is silent (a debug log only);
  - an allocated IP with no matching Advertisement is never announced, also silently.
- **CRDs:**
  - `IPAddressPool` status: assigned and available IPv4 and IPv6;
  - `ServiceL2Status` and `ServiceBGPStatus` (configured announcement, not session state);
  - `ConfigurationState`: Valid, Invalid or Unknown, with an error summary.
- **The controller:** Deployment `controller` in `metallb-system` (`app=metallb, component=controller`), or the chart's `<name>-controller` (`app.kubernetes.io/name=metallb`).

**Cilium LB IPAM.**
- **Pool conditions** (`CiliumLoadBalancerIPPool`, cluster-scoped, v2):
  - `cilium.io/PoolConflict` (True with reason `cidr_overlap`; the newer pool is disabled);
  - `cilium.io/IPsAvailable`, `IPsUsed` and `IPsTotal`, with the count in the message.

  **There is no exhaustion condition:** exhaustion is IPsAvailable "0".
- **The Service condition** `cilium.io/IPAMRequestSatisfied`:
  - `satisfied` is True;
  - `no_pool`, `out_of_ips`, `pool_selector_mismatch`, `already_allocated` and `already_allocated_incompatible_service` are False.
- **No events.**
- **Silent cases:**
  - with no pools LB IPAM writes nothing;
  - `io.cilium/bgp-control-plane` and `io.cilium/l2-announcer` are served only when that feature is enabled, otherwise ignored silently;
  - unset class is served under the default `default-lb-service-ipam=lbipam`.

**k3s ServiceLB.**
- **Class:** the embedded cloud provider, so unset class only.
- **The DaemonSet:** `svclb-<svc>-<uid8>` in `kube-system`, its pods labelled `svccontroller.k3s.cattle.io/svcname` and `svcnamespace`, holding one hostPort per Service port.
- **The ingress** is the IPs of nodes hosting Ready svclb pods.
- **A hostPort conflict** leaves the pods Pending, with the scheduler's reason "node(s) didn't have free ports". A never-satisfied Service gets no event.

**Others, class names only:**
- kube-vip (`kube-vip.io/kube-vip-class`; it also serves unset class by default);
- the AWS Load Balancer Controller (`service.k8s.aws/nlb`);
- GKE (`networking.gke.io/l4-regional-*`);
- Azure (a standard cloud controller, unset class).

## 2. The source contract (C1) and C3

| Part | Acquisition | Counted entity, identity | Dependencies and gaps | Clock | Notifications |
|---|---|---|---|---|---|
| `lb/services` | the shared Services kind (C3), consumer `loadbalancing` | one Service of type LoadBalancer: its UID | pods and DaemonSets (svclb evidence, §4); the Warning-events snapshot; both pool kinds (context, §4). Each missing or unreadable dependency leaves its evidence "unverified"; never a verdict from a gap | the grace, from the pending episode's start (§3), re-evaluated through `Next` | yes |
| `lb/pools` | observer watches: MetalLB `ipaddresspools`, `l2advertisements`, `bgpadvertisements` (v1beta1) and Cilium `ciliumloadbalancerippools` (v2), each capability-gated per kind | one pool: its UID | the advertisement kinds (MetalLB's "is it announced"): unread means "advertisement unverified" | none | yes |
| `lb/metallb` | observer watch: MetalLB `configurationstates` (v1beta1, namespaced), capability-gated; absent on MetalLB older than the kind | one ConfigurationState (`controller`, `speaker-<node>`): its UID | none | none | yes |

**C3's third consumer:**
- `loadbalancing`, whose demand is the core Services API (always yes on a supported cluster).
- The shared Services projection gains, as one projection for every reader:
  - `loadBalancerClass`, `externalTrafficPolicy`, `ipFamilies` and `ipFamilyPolicy`;
  - `status.loadBalancer.ingress`: per entry its ip, hostname and ipMode, and per port its number, protocol and whether an error is set;
  - `status.conditions`: type, status, reason, a clipped message under the URL rule, and observedGeneration;
  - the generation;
  - a fixed allowlist of annotations whose values request an address or a pool:
    - `metallb.io/address-pool`, `metallb.io/loadBalancerIPs` and their `metallb.universe.tf/` aliases;
    - `lbipam.cilium.io/ips`, `io.cilium/lb-ipam-ips` and `lbipam.cilium.io/sharing-key`;
    - `kube-vip.io/loadbalancerIPs`.
- **Classification before display** (revision 1): the projection keeps facts derived over the whole input:
  - any ingress port error;
  - any `LoadBalancerPortsError` True;
  - the address families present;
  - Cilium's `IPAMRequestSatisfied`.

  The display lists are then capped at 8 entries each, with counts. A ninth entry's error is never lost.

**The MetalLB controller task** (C2):
- Capability detection reads MetalLB's controller workload:
  - Deployment `controller` in `metallb-system` (`app=metallb, component=controller`);
  - or the chart's `app.kubernetes.io/name=metallb, app.kubernetes.io/component=controller`.
- Its readiness is the task's controller issue, as for the other tools: a readable not-ready Deployment lights the task, and an unreadable one is unknown.
- `ConfigurationState` is its own part (above), never folded into the workload's readiness.

## 3. The Service rule (first match wins)

**Allocation** (revision 1): an ingress entry with a non-empty `ip` or `hostname`. An entry with neither is no allocation. A hostname-only entry is an allocation; its families are unknown.

**The verdict, for a Service of type LoadBalancer:**
1. **Allocated:**
   - **degraded**, "Port error", when any ingress port carries an error or `LoadBalancerPortsError` is True (from the whole input, §2);
   - **degraded**, "One address family", when `ipFamilyPolicy` is `RequireDualStack` and the allocated IPs cover only one family. It reads "the Service requires both families; <family> is not assigned", with Cilium's current unsatisfied condition when there is one. Whether the controller can assign both is the controller's: that limit is said;
   - otherwise **ok**, "Address assigned", naming the IPs or hostnames. `PreferDualStack` with one family is said, never degraded.

   **The limit, always said:** an assigned address proves allocation, not that traffic reaches the Service.
2. **Not allocated, within the grace** (10 minutes from the pending episode's start): **progressing**, "Pending (Nm)".
3. **Not allocated, past the grace:** **fault**, "No address after <duration>", with the diagnosis (§4) as its reason.

**Pending episodes** (revision 1; the parent's carry-over: object creation is not the start of the current episode). A tracker in `healthsrc`, keyed by UID, records each Service's last observed state (allocated, pending, or not a LoadBalancer) and the start of its current pending episode:
- **Observed start:** a transition this Observer saw into pending (from allocated, or from another type) starts the episode at that evaluation's time.
- **A Service first seen pending:**
  - **created after the Observer's kind went live:** this Observer saw it from its creation, so the creation is the start;
  - **otherwise** the start was not observed. The episode's start is a lower bound, the first observation: "pending at least <duration> (its start was not observed)". The grace runs from that bound, so a fault is never claimed from a creation time.
- **A watch gap:** the Services kind leaving Live, or a relist. Pending before and after the gap cannot prove the Service stayed pending: it may have been allocated and lost meanwhile. So after a gap the episode restarts as a lower bound from the first observation after it.
- **Restart:** a new Observer starts with an empty tracker. Every pending Service is then first seen, with a lower-bound start.
- **Absence and replacement:** a Service gone from a complete read drops its entry. A new UID under the same name is a new Service.
- **Cleared:** an allocation, or a change away from LoadBalancer, ends the episode.

## 4. The diagnosis: who should serve it, and what they say

**The expected controller, from the class** (revision 1; the parent's carry-over: a requested class does not prove its controller is installed, and an unclassified Service need not belong to every detected controller):
- **Unset class:**
  - the detected default implementations are candidates, never owners: MetalLB (installed, its `--lb-class` unseen), Cilium LB IPAM (its pool CRD served), k3s ServiceLB (k3s detected, §4.1), kube-vip (its pods present), or a cloud controller (nodes carry a cloud `providerID`);
  - none detected reads "no controller Klyx can see serves Services without a class".
- **The known classes:**
  - `io.cilium/bgp-control-plane` and `io.cilium/l2-announcer`: Cilium LB IPAM, "served only when that Cilium feature is enabled";
  - `io.cilium/node`: Cilium node IPAM, "served only when node IPAM is enabled", needing no pool;
  - `kube-vip.io/kube-vip-class`: kube-vip;
  - `service.k8s.aws/nlb`: the AWS Load Balancer Controller;
  - `networking.gke.io/l4-regional-internal`, `networking.gke.io/l4-regional-external`, and their `-legacy` variants: GKE.

  The settings named are not visible to Klyx. A known class whose controller is not detected reads "no <controller> detected for class <c>".
- **Another class:** "no controller Klyx knows serves class <c>". A detected MetalLB or kube-vip may be configured for a custom class, through settings Klyx does not read; that is said, and ownership stays unproven.

**4.1 k3s and its ServiceLB:**
- **k3s is detected** when a node's kubelet version carries `+k3s` (from the nodes kind).
- **On k3s, an unset-class Service with no svclb DaemonSet of its own** reads "k3s ServiceLB may be disabled (`--disable=servicelb`), which Klyx cannot see".

**The evidence, each with its join rule** (revision 1):
- **Events:**
  - the Warning events whose involved object's UID equals the Service's, from the capped snapshot (reason and message: `AllocationFailed`, `SyncLoadBalancerFailed`, `UnAvailableLoadBalancer`);
  - an event naming the Service without its UID is never joined: it can belong to a replacement;
  - the snapshot's cap is said.
- **ServiceLB:** the current Service's own DaemonSet, named `svclb-<name, trimmed>-<the Service UID's first 8>` in `kube-system`, and the pods it owns (by owner UID):
  - count and Ready;
  - a Pending pod's scheduling reason, naming a hostPort conflict when the scheduler says so.

  Pods carrying the Service's name labels but owned by another DaemonSet are "unproven candidates (a replaced Service's)".
- **Cilium:** the Service's `cilium.io/IPAMRequestSatisfied` condition, reason as fixed wording. Its observedGeneration must equal the Service's generation, else it is shown as historical and never as current evidence.
- **The address request:** the allowlisted annotations, or `spec.loadBalancerIP` (deprecated).
- **Pools:**
  - **a pool the Service names** (`metallb.io/address-pool`) is the requested pool, joined by name, with its availability. Missing from a complete read reads "the requested pool does not exist";
  - **every other pool is cluster context, never a cause.** Cilium's `serviceSelector` and MetalLB's `serviceAllocation` can exclude the Service, and Klyx does not evaluate them (they need Service and namespace labels the shared projection does not keep). The inspector says "pools in the cluster", not "the pool that should serve it".
- **Silence named:** a pending Service with none of this says which silent case fits:
  - no pools;
  - a class whose feature may be off;
  - no controller;
  - a disabled ServiceLB;
  - a controller that writes nothing.

**What the inspector never says:**
- that an assigned address is working traffic;
- that an advertisement or a ServiceBGPStatus peer is a live session;
- that Cilium's `IPAMRequestSatisfied` is an announcement.

## 5. The pool rule

**Counts are exact or absent** (revision 1):
- **Cilium's counts** are in the messages of `IPsTotal`, `IPsAvailable` and `IPsUsed`, conditions whose status is always Unknown by design. Each message is parsed as an exact non-negative integer (`math/big`, since IPv6 counts exceed int64); anything else is "malformed".
- **MetalLB's** `assignedIPv4`, `availableIPv4`, `assignedIPv6` and `availableIPv6` are optional: absent is "not reported", never zero.

**Cilium `CiliumLoadBalancerIPPool`:**
1. `spec.disabled`: paused, "Disabled".
2. **The conditions describe an earlier generation**, or a count condition is missing or malformed: **unknown**, "Counts not reported yet" (or "for an earlier change").
3. `PoolConflict` True: **fault**, "Conflicts with another pool: disabled by Cilium" (its message, clipped).
4. Available is 0 and total above 0: **degraded**, "Exhausted".
5. Otherwise ok, "N of M available".

**MetalLB `IPAddressPool`:**
1. **No count reported:** unknown, "Counts not reported".
2. **Not advertised:** no `L2Advertisement` or `BGPAdvertisement` selects it. **Degraded**, "Not advertised: its addresses are allocated but never announced".
   - **Selection:** an advertisement with neither `ipAddressPools` nor `ipAddressPoolSelectors` selects every pool; otherwise by name, or by the pool's labels (kept for this).
   - **When it is said:** only from complete advertisement reads, else unknown.
3. **Assigned and none available** in every family it reports: **degraded**, "Exhausted".
4. Otherwise ok, "N available". `autoAssign` false is said, never a fault.

**MetalLB `ConfigurationState`** (`lb/metallb`):
- `result` Invalid: **fault**, "Configuration invalid", with its error summary clipped under the URL rule.
- Unknown or missing: unknown.
- Valid: ok.
- The `controller` and each `speaker-<node>` are separate entities, since they are separate writers.

## 6. What Klyx shows

**A "Load balancers" section** in the Network group, capability-gated:
- **Gated on:** a LoadBalancer Service exists, or MetalLB, Cilium LB IPAM or ServiceLB is detected.
- **Services tab:** pending first, then by age.
  - **Columns:** class (or "default"), address, the expected controller, pending age.
  - **The inspector:**
    - the verdict;
    - the class to controller to pool to allocation chain, drawn;
    - the evidence (§4), each with its source;
    - the limits block.
- **Pools tab:** MetalLB and Cilium pools, broken first, each with its counts, conditions and (MetalLB) the advertisements that select it, plus MetalLB's ConfigurationStates.
- **Needs you:**
  - a Service pending past the grace;
  - a conflicting pool;
  - MetalLB's configuration Invalid.

**No verbs.** Pools and classes are desired state, in Git.

## 7. Projections, budget and sensitive data

- **Services:** §2's additions, unbudgeted as today. The ingress, port and condition lists are capped at 8 for display, after classification over the whole input.
- **Pools:**
  - **Cilium:** name, generation, `disabled`, the block count and first 8 blocks, `serviceSelector` presence, and the conditions with their observedGeneration and exact counts;
  - **MetalLB:** name, labels (for advertisement selectors), the address count and first 8 addresses, `autoAssign`, `serviceAllocation` presence, and status counts with presence kept.
- **Advertisements:** name, kind, the pool names and whether pool selectors are set (with their match labels, to evaluate them against pool labels), node selector presence, and BGP peers' count.
- **ConfigurationState:** name, result, error summary (clipped), and conditions.
- **Budget:**
  - **new watches:** up to five MetalLB and Cilium kinds, where served;
  - **retained:** small (tens of objects);
  - **the Services projection** grows by a few fields;
  - **existing reads:** the part reads DaemonSets and pods, both already watched, and the events snapshot, already read;
  - the load test is rerun before and after.
- **Sensitive data:**
  - addresses, pool names and classes are infrastructure identity, not secrets;
  - controller messages (events, conditions, configuration errors) go under the URL rule and are clipped;
  - AI allowlists: classes, states, counts, reasons and the expected controller; no annotation values.

## 8. Tests, lab, parity, smoke

- **Unit (`internal/lb`, pure):**
  - every Service row, at the grace boundary with an injected clock;
  - **allocation:** an empty entry, hostname-only, `RequireDualStack` with one family, and `PreferDualStack`;
  - an error beyond the display cap;
  - every class mapping, including unset with several or no candidates, an unknown class, and a custom class;
  - each evidence join: an event without a UID, an svclb pod owned by another DaemonSet, a stale Cilium condition, a named pool missing;
  - each silent case;
  - every pool row, including absent and partial status, an old observedGeneration, a malformed count, a large IPv6 count, and advertisement selection;
  - the ConfigurationState rows.
- **The episode tracker:** an observed start, a first-seen lower bound, a gap restarting it, a restart, absence, replacement, and clearing.
- **observe and healthsrc:**
  - the third consumer;
  - the Service part with every dependency present and absent;
  - a UID replacement.
- **Frontend:** the tabs, the chain, the limits and Needs you.
- **Parity:** a healthy baseline, a LoadBalancer Service with an address written by the fixture, and a MetalLB pool with written counts.
- **The lab (`lab-s4`, kind-klyx).** Revision 1: per C6, a real controller where allocation is the point.
  - **A real MetalLB v0.16.1:** its manifest is read before it is applied, with a pool from the kind network's range and an L2Advertisement. The scenario:
    - a Service gets an address from MetalLB itself;
    - a one-address pool is exhausted by a second Service, whose `AllocationFailed` event MetalLB writes;
    - deleting the first Service releases its address, which MetalLB's status counts show;
    - a pool with no advertisement allocates without announcing;
    - MetalLB's own ConfigurationState.
  - **Fixtures for what the lab cannot produce:**
    - a Cilium pool in conflict and an exhausted one (Cilium is not the lab's CNI);
    - a Service with Cilium's unsatisfied condition;
    - svclb pods with a hostPort conflict;
    - a Service with an unknown class;
    - a k3s-style node version.
  - **The pending Service:** a pending one has no address while within the grace. `smoke13/run4.sh` re-applies it, deleted and created again, before each mode, so the "within grace" assertion is never aged past ten minutes.
- **Native smoke:** the Services tab pending first, the chain and diagnosis, and a pool fault.
- **Live captures:** night and day at 1440 and 1024, taken more than 10 minutes after the lab is applied, so the past-grace fault shows.

## 9. Not in S4

- **BGP session state** (Cilium's BGP node status, MetalLB's FRR): a later candidate.
- **L2 announcement leases:** a later candidate.
- **Reading controller flags or ConfigMaps** (MetalLB `--lb-class`, Cilium's `cilium-config`): their effects are said as limits.
- **Verbs.**

## 10. Revision 1: Codex's design review of b887823

| # | Finding | Where answered |
|---|---|---|
| 1 | Allocation loss and unobserved starts bypassed the grace | §3: pending episodes keyed by UID; observed starts; a first-seen lower bound, never a creation time as proof; gaps, restart, absence and replacement |
| 2 | "Any ingress entry" was not allocation; required families; classification before the display cap | §2 and §3: a non-empty IP or hostname; hostname-only success; `RequireDualStack` with one family is degraded with its limit; facts derived over the whole input |
| 3 | No missing, malformed or stale branch for pools; counts as booleans | §5: exact big-integer counts, presence kept, generation lag and malformed as unknown; the Service's Cilium condition is fenced by generation too (§4) |
| 4 | Joins needed identity and eligibility | §4: events by involved-object UID only; svclb by the current Service's generated DaemonSet and pod owners, others unproven; only a named pool is joined, every other pool is context |
| 5 | ConfigurationState, controller health and advertisements had no acquisition contract | §2: the `lb/metallb` part; the controller task's workload readiness; advertisements watched as pool evidence (§5's "not advertised") |
| 6 | The lab needed a real controller (C6) | §8: a real MetalLB with allocation, exhaustion, release and no-advertisement scenarios; fixtures only for what the lab cannot produce; the pending fixture re-applied per smoke mode |
| 7 (P3) | `io.cilium/node`; exact GKE classes; custom classes unproven | §4 |
