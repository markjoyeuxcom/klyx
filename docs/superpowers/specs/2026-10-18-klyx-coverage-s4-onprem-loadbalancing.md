# Coverage S4: on-prem load balancing

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S4. It builds on C3 (S3a §2): the shared Services kind gains a third consumer. Revision 1 answers Codex's design review of b887823 (§10), revision 2 its re-review (§11), revision 3 the next (§12). Design accepted at 72cf021; implementation accepted at b5269f0 (§16).

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
- **A Service first seen pending** (revision 2): its start was not observed, whenever it was created. Evaluations are throttled snapshots, so a Service created while the kind is live can still have become a LoadBalancer, been allocated and lost its address before the tracker first saw it; its creation time records none of that. The episode's start is a lower bound, the first observation: "pending at least <duration> (its start was not observed)". The grace runs from that bound, so a fault is never claimed from a creation time.
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
  - **a pool the Service names** (`metallb.io/address-pool`) is the requested pool, joined by name inside MetalLB's instance namespace (§5), with its availability. Missing from a complete read of that namespace reads "the requested pool does not exist"; with no instance namespace known, or several, the join is qualified, never guessed;
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

**Cilium `CiliumLoadBalancerIPPool`** (revision 2: each condition is judged on its own freshness, and the conflict before the counts). Cilium writes `PoolConflict` when it marks the pool (`markPoolConflicting`) and the counts afterwards (`satisfyAndUpdateCounts`), so a current conflict with no counts, or with counts from an earlier generation, is an ordinary state. A condition is current when its observedGeneration equals the pool's generation.
1. `spec.disabled`: paused, "Disabled".
2. **A current `PoolConflict` True:** **fault**, "Conflicts with another pool: disabled by Cilium" (its message, clipped), whatever the counts say. A conflict from an earlier generation is not current evidence: it is shown as historical and the rule goes on.
3. **A count condition missing, malformed or from an earlier generation:** **unknown**, "Counts not reported yet" (or "for an earlier change").
4. Available is 0 and total above 0: **degraded**, "Exhausted".
5. Otherwise ok, "N of M available".

**MetalLB's instance namespace** (revision 2). MetalLB's admission webhook also refuses pools and advertisements created outside its namespace, so a pool outside it arises only without the webhook; the rule below still covers it. MetalLB reads its pools, advertisements and ConfigurationStates only in its own namespace (`client.InNamespace(r.Namespace)`). That namespace defaults to the pod's own and can be moved by `--namespace` or `METALLB_NAMESPACE`. Klyx's evidence for it, in order:
- the namespace holding a `controller` ConfigurationState, which MetalLB itself writes there;
- otherwise the detected controller workload's namespace, with the limit that the flag can move it.

Every MetalLB join (an advertisement to a pool, a Service's requested pool, a ConfigurationState to its pools) stays inside one namespace. A pool outside every known instance namespace reads **unknown**, "No MetalLB instance found in its namespace: MetalLB reads pools only in its own". Same-named pools in two namespaces are never confused.

**MetalLB `IPAddressPool`:**
1. **No count reported:** unknown, "Counts not reported".
2. **The configuration is not the active one:** when the namespace's `controller` or any `speaker-<node>` ConfigurationState is Invalid, MetalLB keeps running its last valid configuration (`toConfig` fails and the reconciler returns without applying), so the objects Klyx reads may not be what the speakers announce. The advertisement verdict is withheld: **unknown**, "MetalLB's configuration is invalid: it may still run an earlier valid one". The Invalid ConfigurationState is the fault (below).
3. **No advertisement selects it:** no `L2Advertisement` or `BGPAdvertisement` in its namespace selects it. **Degraded**, "No advertisement selects it". This is configuration evidence, worded as such: it says what the cluster's objects configure, not what the speakers announce, and it holds whether or not any address is assigned yet.
   - **Selection, MetalLB's own:** an advertisement with neither `ipAddressPools` nor `ipAddressPoolSelectors` selects every pool in its namespace. Otherwise it selects a pool named in `ipAddressPools`, or one whose labels match any of its `ipAddressPoolSelectors`. Each selector is kept whole (`matchLabels` and `matchExpressions`) and evaluated with `metav1.LabelSelectorAsSelector`, the function MetalLB uses, against the pool's labels.
   - **Unresolved:** a selector that does not convert, or one past the projection's cap (§7), is unresolved; so is (revision 3) an advertisement whose `ipAddressPools` list was cut at the cap, for every pool its retained names do not match. A retained exact name, or a retained selector that matches, still proves selection. If no advertisement proves selection and any advertisement in its namespace is unresolved for the pool, the verdict is **unknown**, "An advertisement's selection could not be evaluated", never degraded.
   - **When it is said:** only from complete advertisement and pool reads; else unknown.
   - **The limit, always said:** "From the cluster's configuration objects, not the speakers' announcement state." When ConfigurationState is absent (MetalLB before the kind), unread or reports no result, it adds: "MetalLB's configuration result is not reported; an invalid configuration would leave the speakers on an earlier one."
4. **Assigned and none available** in every family it reports: **degraded**, "Exhausted".
5. Otherwise ok, "N available". `autoAssign` false is said, never a fault.

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
  - **MetalLB:** namespace, name, labels (for advertisement selectors, capped at 32 with the cut said; a pool whose labels are cut makes selection by labels unresolved), the address count and first 8 addresses, `autoAssign`, `serviceAllocation` presence, and status counts with presence kept.
- **Advertisements** (revision 2): namespace, name, kind, the pool names (capped at 32, with a names-cut flag, revision 3), and the pool selectors kept whole, `matchLabels` and `matchExpressions`, up to 8 selectors of 16 requirements each, past which the advertisement is unresolved (§5); node selector presence, and BGP peers' count.
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
  - revision 2, Cilium: a current conflict with absent counts and with counts from an earlier generation (both fault), and a conflict from an earlier generation (historical, never fault);
  - revision 2, MetalLB: an expression-only selector (`tier In [gold]` never selects a bronze pool), a mixed `matchLabels` and `matchExpressions` selector, a selector that does not convert and one past the cap (unresolved), revision 3's pool named only at the 33rd `ipAddressPools` entry with no selector and no other advertisement (unknown, never degraded) and a pool named within the first 32 of a cut list (selected), a same-named pool and advertisement in another namespace (never joined), a pool outside every instance namespace, and an Invalid ConfigurationState with an unselected pool (withheld, never degraded);
  - the ConfigurationState rows.
- **The episode tracker:** an observed start, a first-seen lower bound, a gap restarting it, a restart, absence, replacement, and clearing. Revision 2: a Service created after the kind went live whose first capture already shows it pending after coalesced intervening states (ClusterIP, then allocated, then lost) gets the first-seen lower bound, never its creation time.
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
    - a pool no advertisement selects reads "No advertisement selects it";
    - MetalLB's own ConfigurationState, Valid;
    - an Invalid configuration, which its ConfigurationState reports and which withholds the advertisement verdict; removing the cause restores Valid. Revision 3: an overlapping pool cannot produce it, because MetalLB's admission webhook (failurePolicy Fail) runs the same validation and refuses it. The lab uses an input the webhook admits but the reconciler refuses: the webhook clears transient fields such as a BGPPeer's password Secret reference before validating, while the reconciler reads the Secret and refuses one whose type is not `kubernetes.io/basic-auth`. The input is proven against the running MetalLB at build; if none produces Invalid there, the case is a fixture and the as-built notes say so.
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

## 11. Revision 2: Codex's re-review of revision 1

| # | Finding | Where answered |
|---|---|---|
| 1 | Creation after the kind went live does not prove the first observed episode began at creation: evaluations are throttled snapshots | §3: every Service first seen pending gets the first-seen lower bound; the creation time is never a start. §8: a first capture after coalesced states |
| 2 | Missing or stale counts came before a current `PoolConflict` and suppressed a proven fault | §5: each condition judged on its own freshness; a current conflict is a fault whatever the counts; a stale conflict is historical; the counts gate only exhaustion and capacity. §8: the three cases |
| 3 | Advertisement matching lost `matchExpressions`, crossed namespaces, and over-claimed announcement | §5: MetalLB's instance namespace bounds every join; selectors kept whole and evaluated with `LabelSelectorAsSelector`, unresolved past the cap; "No advertisement selects it" as configuration evidence with its limit; withheld while a ConfigurationState is Invalid (the last valid configuration). §7: the projection. §8: the cases |

## 12. Revision 3: Codex's re-review of revision 2

| # | Finding | Where answered |
|---|---|---|
| 1 | The named-pool list was capped at 32 with no cut flag, so a pool named only at entry 33 could read "No advertisement selects it" | §5: a cut `ipAddressPools` list leaves the advertisement unresolved for every pool its retained names do not match; a retained exact name still proves selection. §7: the names-cut flag. §8: the 33rd-name regression and a name within the first 32 |
| (own) | The lab's overlapping pool would be refused by MetalLB's webhook | §8: an input the webhook admits but the reconciler refuses (a BGPPeer password Secret of the wrong type), proven at build, else a fixture; §5 notes the webhook's namespace refusal |

## 13. As built (42b321d to ef187bd)

**`internal/lb`** (42b321d): the projections and the rules as §3 to §5.
- **The projections:**
  - MetalLB pools keep their labels sorted and capped, with the cut flagged.
  - Advertisements keep their selectors whole, flagging a cut name list or selector.
  - ConfigurationStates keep their result, error summary and conditions, scrubbed.
  - Cilium pools keep the four conditions the rule reads, whole.
- **Counts:** `Count` holds an exact `big.Int` or a state (missing, malformed or stale), and `CountsOf` fences each count by its own observedGeneration.
- **Selection:** `Selects` uses `metav1.LabelSelectorAsSelector` and refuses another namespace. A selector that matches on retained labels proves selection only when no requirement could be falsified by a cut label (`NotIn`, `DoesNotExist`).
- **The episode tracker:** `Tracker` keys episodes by UID and restarts every one on any change of `Continuity{Generation, Epoch}`.

**A deviation from §3: where the tracker runs.** §3 put it "in `healthsrc`". An evaluator must stay pure, so the state lives elsewhere:
- **`fleet/lbepisodes.go`** follows the Services kind with `WatchKind`, reads it with `VisitKind`, and feeds a live, complete snapshot to the pure tracker. Anything else (not live, not complete, or truncated) is a `Break`.
- **The output** is published as `FactLBEpisodes`.
- **`observe.KindState` gains `Epoch`** (58effc8): the continuity epoch that the ledger already kept. It advances on every recorded error and every complete list, so a relist or a gap restarts each episode as a lower bound.
- **No episode yet:** a pending Service the tracker has not recorded reads a lower bound from the evaluation's own time.

**A deviation from §5: the MetalLB rule's order.** §5 put "no count reported" first. As built, the order is:
1. the instance namespace;
2. Invalid configuration (withheld);
3. the advertisements read;
4. selection;
5. counts;
6. exhaustion.

A missing count therefore never hides the configuration evidence. This is revision 2's Cilium ordering (a proven finding before an unrelated missing count) applied here too.

**`healthsrc`** (58effc8, 24a2cc4):
- **The Services projection:** a LoadBalancer Service keeps its class, families and policy, traffic policy, loadBalancerIP, the allowlisted address annotations, and its whole ingress and condition lists. A port error becomes `(set)`; a condition message is scrubbed. The lists are kept whole, and the rule classifies over all of them. Only the DTO caps them at 8 for display, so classification before the display cap holds by construction.
- **The consumer:** the `loadbalancing` consumer is always yes, and only on Services (EndpointSlices keep their two consumers).
- **The `lb` task:** the parts are `services`, `pools` and `metallb` (§2).
- **The services part's dependencies:**
  - DaemonSets, pods and nodes;
  - the Warning-events snapshot;
  - both pool kinds and ConfigurationStates;
  - the facts `FactLBEpisodes` and `FactLB`.

  The events read is "partial" when the 500-event snapshot is capped or stale: an absent event then proves nothing.
- **Detection beyond §4's list:**
  - kube-vip by pods running its image;
  - the AWS Load Balancer Controller by its `elbv2.k8s.aws` CRDs;
  - a cloud by a node's providerID scheme from a fixed list (`kind://` and `k3s://` are not clouds).
- **The MetalLB controller task** (`capability.LoadBalancing`): Deployments by the native labels and the chart's, deduplicated. The controller issue follows the Velero pattern, and `FactLB` carries the controllers' namespaces (the instance namespace's fallback evidence).

**The lens gate.** `LoadBalancingCapability.Present` is true when any of these holds:
- MetalLB is detected;
- the AWS Load Balancer Controller is detected;
- Cilium is served (its LB IPAM pools come with it);
- a LoadBalancer Service is seen. The tracker reports this from each complete live read, and the snapshot merges it.

ServiceLB acts only on LoadBalancer Services, so they cover §6's "or ServiceLB is detected".

**appbridge and the frontend** (a023184, 5468935):
- **The DTOs:**
  - Cilium counts travel as exact decimal strings;
  - MetalLB counts keep their presence;
  - the limits are said on every row.
- **The AI allowlists** carry classes, states, counts, reasons and candidates. They never carry an address, hostname, address block, annotation value, requested pool name or event message, and a literal-needle test checks this at the serialised boundary.
- **Chat** reads the `lb` verdicts for Services, pools and states.
- **The lens** is the Load balancers lens in the Network group:
  - **Services:** pending first, oldest first, with "≥" marking a lower-bound start;
  - **Pools;**
  - **MetalLB configuration:** shown with MetalLB's objects;
  - **the drawing:** the class to controller to pool to address chain;
  - **the inspectors:** each piece of evidence with its source, and the limits block.
- **Describe:** a Service has no Describe item, because the describe table names no core kinds and never guesses a group.

**Needs you.** §6 names three faults: a Service past the grace, a conflicting pool, an Invalid configuration. As every other lens does, the rule's degraded rows are Included too, so they reach Needs you as look-at entries, never as faults:
- an exhausted pool;
- an unadvertised MetalLB pool;
- a port error;
- a missing required family.

**MetalLB's instance namespaces** (91622da) are known only from positive evidence: a controller ConfigurationState, or a complete controller read. An absent ConfigurationState kind (MetalLB before v0.15) proves nothing, so it never makes a pool read "no instance here".

**Found during the build:**
- **The re-probe never refreshed Velero's capability** (S2a): a server that went not ready after connect was never seen. `applyReprobe` now copies it (24a2cc4).
- **From the live captures** (ef187bd):
  - the expected-controller column now outranks the class;
  - a pending Service's families read "requested", not "- · policy";
  - the ServiceLB block shows only with svclb evidence or a detected ServiceLB;
  - the no-advertisement reason no longer repeats the limit.

**The lab** (scratchpad `lab-s4`, kind-klyx; the manifest's sha256 is checked before apply):
- **A real MetalLB v0.16.1** on the kind network (192.168.97.0/24):
  - `web` is allocated from `lab-s4-main`;
  - `one-a` holds the one address of `lab-s4-one`, and MetalLB itself refuses `one-b` with `AllocationFailed ... no available IPs in pool lab-s4-one`;
  - `silent` is allocated from `lab-s4-silent`, which no advertisement selects;
  - the pool counts and both ConfigurationStates (controller, speaker) are written by MetalLB.
- **Release** (`release.sh`): deleting the holder freed the address, and MetalLB gave it at once to a Service waiting on the same pool (its `IPAllocated` event). The counts stayed one assigned, because the address never sat free. A released address goes to whichever Service waits, so the script rebuilds the order from scratch.
- **Invalid** (`invalid.sh on`): MetalLB's webhook admitted the BGPPeer whose password Secret is Opaque. The speaker's ConfigurationState then read `Invalid` ("secret type mismatch ... kubernetes.io/basic-auth is expected") while the controller's stayed `Valid`: separate writers, as §5 says. `off` restores Valid.
- **Fixtures:**
  - the Cilium pool CRD (from Cilium v1.20.3) with status-written pools: ok, conflict, full, and an IPv6 /64 with exact counts;
  - Cilium's unsatisfied condition on a classed Service (MetalLB ignores another class);
  - a stand-in svclb DaemonSet under the Service's generated name, whose pod a port-holder pod leaves Pending ("didn't have free ports").
- **Not in the lab:** a k3s node version. The kubelet rewrites nodeInfo, so it is covered by `internal/lb`'s tests only. The Cilium CRD also makes `cilium.io` served on kind-klyx, so Cilium LB IPAM is listed as a candidate there; that is the detection rule, as §4 says.

## 14. Gates

- **The parity gate:** passes, both stages, with the S4 baseline and its oracle's LoadBalancer filter. It passed first at dd2086a (dirty with the stage's own uncommitted changes) and again at 91607c0 (run a846eba608e998c8; dirty only with this section, uncommitted while the gate ran).
- **The four-way native smoke:** 167/167 in night-1440, night-1024, day-1440 and day-1024 at 91607c0, clean. The labs (lab-s1b, lab-s1c, lab-s2b, lab-s3a) were re-applied within the hour, and `run4.sh` re-creates lab-s4's within-grace Service before each mode. Two earlier runs missed one step each, both in the smoke's own steps:
  - the Pools tab's rows were read before they replaced the Services rows (4315381);
  - at 1024 the configuration check read the Writer column, which the open inspector hides (91607c0).

  A third run stalled when the host slept; the run that passed held idle sleep off with `caffeinate`.
- **`go test -race`:** ok on lb, observe, healthsrc, appbridge, fleet, capability, crd and healthnotify.
- **golangci-lint v2.14.0:** 0 issues.
- **The frontend:**
  - the suite: 2189 passed;
  - tsc: clean;
  - eslint: 0 errors. The 3 warnings are pre-existing, in InstanceDetail and LogsPane.
- **The live captures** (scratchpad cap-s4, server mode, taken more than 10 minutes after the app started, so past the grace):
  - all four themes and widths, plus a night-1440 pass with `invalid.sh on`: 52 shots;
  - no sideways scroll, no page error, and the lab's BGP password never on screen.

  They show the four pending Services as "No address" faults with their own reasons, Load balancers in Needs you, and the withheld pool verdicts under an Invalid speaker. The probe before them found the four fixes in ef187bd.
- **The load test** (§7), large cluster, two runs each against e56f98c:
  - **full evaluation:** 51.5 and 59.7 ms before, 42.0 and 46.2 ms after. That is no regression; the difference is within this test's spread (S3a saw 66 to 78 ms on the same tree);
  - **steady retained heap:** 58.3 MB before, 59.1 MB after (+0.8 MB); large cluster +0.1 MB, six clusters +0.5 MB;
  - **recovery-storm peak:** 74.3 and 73.4 MB before, 65.0 and 86.4 MB after: noise.

## 15. Implementation review 1: Codex's review of 195d81d

Codex judged the four stated deviations reasonable. Its five findings were fixed in 8dcf3b4:

| # | Finding | Fix |
|---|---|---|
| 1 (P1) | Explain still carried the excluded values: through the rule's derived reason (an event's message), the target's facts and condition messages, and its Warning events | `lbEvidence` applies the policy to the whole document for the lb task: conditions without messages, facts from an address-free allowlist (type, traffic policy, session affinity, LB class), and events and condition reasons as identifier tokens only. The lens fields carry a structural `cause` (the speaking source and its reason token, or the silent case) instead of the free-text reason. The regression runs the serialized prompt through the fake completer, for a Service and a Cilium pool, and fails on all five leaks with the policy removed. |
| 2 | `WatchKind` is newest-wins, so an allocated state between two pending snapshots could vanish, and the old episode carried on into a fault | A LoadBalancer Service's projection keeps its resourceVersion, and the driver passes `contiguous` (the kind's `Seq` advanced by one at most). A pending Service keeps its episode only when it was unwritten since the last snapshot, or the snapshots were contiguous (one write in the whole kind, pending to pending). Otherwise the episode restarts as a lower bound, for that Service alone. |
| 3 | The Pools tab's evidence read only IPAddressPool, so a checking Cilium read showed "No address pools" | The tab's evidence combines both pool kinds (`combineEvidence`), and a pinned pool is proven gone only by its own kind's read. |
| 4 | A failed second selector discarded a proven not-ready controller; a continued list certified health | Both selectors' reads accumulate. A proven not-ready controller stands beside any gap. A failed or continued (cut) list leaves `ControllersRead` false and certifies neither health nor absence. |
| 5 | A family with one count missing was skipped, so "Exhausted" was claimed | A family reporting one count but not the other makes capacity unknown ("Counts incomplete"), unless another family proves free capacity. |

**Gates after the fixes:**
- `go test -race` on the eight packages: ok;
- golangci-lint: 0 issues;
- the frontend: 2190 passed, tsc clean, eslint 0 errors.

**Reruns at 0b1a5ec** (the code of 8dcf3b4 plus this note):
- the parity gate passes, both stages, clean (run c5669a647adb3d64);
- the four-way native smoke passes 167/167 in every mode, clean, after re-applying lab-s1b, lab-s1c, lab-s2b and lab-s3a.

## 16. Implementation review 2: Codex's re-review of 0b1a5ec, and ACCEPT

Codex confirmed findings 2 to 5 resolved. One residual of finding 1 remained: the condition reasons inside the lens fields (a Service's Cilium condition, a pool's conflict) were not held to the token policy. `metav1.Condition` admits colons in a reason, so an IPv6 address could pass for one.

- **The fix** (b5269f0): `lbEvidence` walks the lens fields and reduces every `reason` string, at any depth, to its identifier token.
- **The regression:** Codex's probe, adopted. It fails on both the Service and the pool path with the walk removed.
- **Gates:** race on appbridge ok; golangci-lint 0. The fix touches only Explain's evidence, which neither the parity gate nor the smoke exercises.

**Codex ACCEPT at b5269f0, 2026-10-18:** all findings resolved, none remaining.
