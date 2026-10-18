# Coverage S4: on-prem load balancing

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S4. It builds on C3 (S3a §2): the shared Services kind gains a third consumer.

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
| `lb/services` | the shared Services kind (C3), consumer `loadbalancing` | one Service of type LoadBalancer: its UID | pods (svclb evidence); the Warning-events snapshot; the MetalLB and Cilium pool kinds. Each missing or unreadable dependency leaves its evidence "unverified"; never a verdict from a gap | the grace: a pending Service turns fault at creation + grace, re-evaluated through `Next` | yes |
| `lb/pools` | observer watches: MetalLB `ipaddresspools` (v1beta1) and Cilium `ciliumloadbalancerippools` (v2), each capability-gated per kind | one pool: its UID | none | none | yes |

**C3's third consumer:**
- `loadbalancing`, whose demand is the core Services API (always yes on a supported cluster).
- The Services projection gains what this part reads:
  - `loadBalancerClass` and `externalTrafficPolicy`;
  - `status.loadBalancer.ingress`: ip, hostname, ipMode, and per port its number, protocol and whether an error is set;
  - `status.conditions`: type, status, reason, a clipped message under the URL rule, and observedGeneration;
  - a fixed allowlist of annotations whose values request an address: `metallb.io/address-pool`, `metallb.io/loadBalancerIPs` and their `metallb.universe.tf/` aliases, `lbipam.cilium.io/ips`, `io.cilium/lb-ipam-ips`, `lbipam.cilium.io/sharing-key` and `kube-vip.io/loadbalancerIPs`.

  It is one projection for every reader, as C3 requires.

## 3. The Service rule (first match wins)

**The verdict, for a Service of type LoadBalancer:**
1. **An address is assigned** (any ingress entry):
   - **ok**, "Address assigned", naming the IPs or hostnames;
   - **degraded**, "Port error", when an ingress port carries an error or `LoadBalancerPortsError` is True;
   - **the limit is said:** an assigned address proves allocation, not that traffic reaches the Service.
2. **No address within the grace** (10 minutes): **progressing**, "Pending (Nm)". Cloud load balancers take minutes to provision; an on-prem controller allocates in seconds.
3. **No address past the grace:** **fault**, "No address after <age>", with the diagnosis (§4) as its reason.

**When pending starts:**
- **Normally** from the Service's creation.
- **A type change Klyx observed:** when this Observer saw the same UID with another type, pending counts from the evaluation that first saw it as LoadBalancer. A small tracker does this, like Velero's resume tracker.
- **A change Klyx did not observe:** the creation stands, and the inspector says "since creation (Klyx cannot see when the type changed)".

## 4. The diagnosis: who should serve it, and what they say

**The expected controller, from the class:**
- **Unset class:**
  - each detected default implementation is a candidate: MetalLB (installed, its class flag unseen), Cilium LB IPAM (pools CRD served), k3s ServiceLB (svclb pods or DaemonSets present), kube-vip (its pods present), or a cloud controller (nodes carry a cloud `providerID`);
  - none detected reads "no controller Klyx can see serves Services without a class".
- **A known class:**
  - **its controller:** `io.cilium/bgp-control-plane` and `io.cilium/l2-announcer` map to Cilium, with the limit "served only when that Cilium feature is enabled (not visible to Klyx)"; `kube-vip.io/kube-vip-class` maps to kube-vip; `service.k8s.aws/nlb` to the AWS Load Balancer Controller; `networking.gke.io/*` to GKE;
  - **when that controller is not detected:** "no <controller> detected for class <c>".
- **Another class:** "no controller Klyx knows serves class <c>".

**The evidence, each said with its source:**
- **Cilium:** the Service's `cilium.io/IPAMRequestSatisfied` condition (reason as fixed wording, and its message).
- **MetalLB and the cloud controller:**
  - the Service's Warning events from the snapshot (reason and message: `AllocationFailed`, `SyncLoadBalancerFailed`, `UnAvailableLoadBalancer`);
  - **its limit:** the snapshot is capped, so an event may be missing.
- **k3s ServiceLB:** the svclb pods for the Service (`svccontroller.k3s.cattle.io/svcname` and `svcnamespace`):
  - count and Ready;
  - a Pending pod's scheduling reason, naming a hostPort conflict when the scheduler says so.
- **The address request:** the annotations above, or `spec.loadBalancerIP` (deprecated).
- **The pools:**
  - for MetalLB, a requested pool and its availability;
  - for Cilium, any pool and its conflict or exhaustion (§5).
- **Silence named:** for a pending Service with none of this, the inspector says which silent case fits:
  - no pools;
  - a class whose feature may be off;
  - no controller;
  - a controller that writes nothing.

**What the inspector never says:**
- that an assigned address is working traffic;
- that a ServiceBGPStatus peer is a live session;
- that Cilium's `IPAMRequestSatisfied` is an announcement.

## 5. The pool rule

**Cilium `CiliumLoadBalancerIPPool`:**
- `spec.disabled`: paused, "Disabled".
- `PoolConflict` True: **fault**, "Conflicts with another pool: disabled by Cilium" (its message, clipped).
- `IPsAvailable` "0" with `IPsTotal` above 0: **degraded**, "Exhausted".
- Otherwise ok, "N of M available".

**MetalLB `IPAddressPool`:**
- Assigned above 0 and available 0 in every family it has: **degraded**, "Exhausted".
- `autoAssign` false is said, never a fault.
- Otherwise ok, "N available".
- Its status counts come from MetalLB's status writer: an empty status reads "counts not reported", unknown.

**MetalLB's `ConfigurationState`** (`result` Invalid) is the MetalLB controller task's issue, with its error summary clipped. The controller task also reads the Deployment's readiness (C2: management is not health).

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
- **Pools tab:** MetalLB and Cilium pools, broken first, each with its counts and conditions.
- **Needs you:**
  - a Service pending past the grace;
  - a conflicting pool;
  - MetalLB's configuration Invalid.

**No verbs.** Pools and classes are desired state, in Git.

## 7. Projections, budget and sensitive data

- **Services:** §2's additions, unbudgeted as today. Ingress lists are capped at 8 entries and conditions at 8, with counts. The annotation allowlist is fixed.
- **Pools:**
  - Cilium: name, `disabled`, block count and the first 8 blocks, the conditions above;
  - MetalLB: name, the address count and the first 8 addresses, `autoAssign`, status counts.
- **Budget:**
  - **new watches:** two pool kinds, where served;
  - **the Services projection** grows by a few fields for LoadBalancer Services only;
  - **the part reads pods** (labels already projected) and the events snapshot (already read);
  - the load test is rerun before and after.
- **Sensitive data:**
  - addresses, pool names and classes are infrastructure identity, not secrets;
  - controller messages (events, conditions) go under the URL rule and are clipped;
  - AI allowlists: classes, states, counts, reasons and the expected controller; no annotation values.

## 8. Tests, lab, parity, smoke

- **Unit (`internal/lb`, pure):**
  - every Service row, at the grace boundary with an injected clock;
  - the type-change tracker;
  - every class mapping, including unset with several or no candidates, and an unknown class;
  - each evidence source and each silent case;
  - every pool row;
  - the projection bounds.
- **observe and healthsrc:**
  - the third consumer;
  - the Service part with pods, events and pools present and absent;
  - a UID replacement.
- **Frontend:** the tabs, the chain, the limits and Needs you.
- **Parity:** a healthy baseline, a LoadBalancer Service with an address. kind has no load balancer, so its status is written by the fixture.
- **The lab (`lab-s4`, kind-klyx):**
  - **CRDs:** MetalLB's and Cilium's pool CRDs, read before they are applied;
  - **pools:**
    - a MetalLB pool with addresses and status counts written by the fixture, plus an exhausted one;
    - a Cilium pool in conflict;
  - **Services:**
    - one with an address;
    - one pending within the grace;
    - one pending past it. A creation time cannot be backdated and Klyx has no grace override, so the smoke checks it within the grace, unit tests cover the boundary with an injected clock, and the live captures are taken more than 10 minutes after the lab is applied, showing the real fault;
    - one with an unknown class;
    - one with a MetalLB `AllocationFailed` Warning event written by the fixture;
    - one with a Cilium `IPAMRequestSatisfied` False condition;
  - **svclb pods** with a hostPort conflict.
- **Native smoke:** the Services tab pending first, the chain and diagnosis, and a pool fault.
- **Live captures:** night and day at 1440 and 1024.

## 9. Not in S4

- **BGP session state** (Cilium's BGP node status, MetalLB's FRR): a later candidate.
- **L2 announcement leases:** a later candidate.
- **Reading controller flags or ConfigMaps** (MetalLB `--lb-class`, Cilium's `cilium-config`): their effects are said as limits.
- **Verbs.**
