# Coverage S10: node provisioning, by provider adapter

The roadmap's S10 (`2026-10-16-klyx-coverage-roadmap.md` §2):
- **adapters** for `EC2NodeClass`, `AKSNodeClass` and the EKS Auto Mode `NodeClass`; any other provider is an honest "provider not supported";
- **the class link:** `nodeClassRef` resolved by group, kind and name, with missing, unreadable and stale kept distinct, and a class failure inherited by its NodePools not double-counted;
- **management** per C2, with the positive-evidence table;
- **the AKS networking note.**

S5 deferred the controller task's "managed" state (EKS Auto Mode's Karpenter, AKS NAP) to here.

Today Klyx reads only `karpenter.sh` NodePools and NodeClaims. Both are watched whole (managedFields aside) with no budget, and the NodePool rule reads Ready alone. Nothing reads `nodeClassRef` or a provider class. The controller probe looks for a `karpenter` Deployment in `kube-system`, then `karpenter`, and on EKS Auto Mode (no in-cluster controller) it reads "unknown". The only class surface is a raw `EC2NodeClasses` registry entry, gated on `karpenter.sh` rather than its own API.

## 1. The APIs, read from the source

Sources:
- karpenter (core) v1.15.0;
- karpenter-provider-aws v1.15.0;
- karpenter-provider-azure v1.14.5;
- the EKS user guide ("Create a Node Class") and AWS re:Post troubleshooting articles for the closed-source Auto Mode NodeClass;
- Microsoft Learn ("Node auto-provisioning").

The extract is kept with the lab (`s10-facts.md`).

### 1.1 Core (`karpenter.sh/v1`)

- **NodePool Ready** aggregates `ValidationSucceeded` and `NodeClassReady`. `NodeRegistrationHealthy` is set but is not a Ready dependent.
- **`status.nodeClassObservedGeneration`** is the class generation the pool last observed.
- **The `nodepool.readiness` controller** sets `NodeClassReady` from the class (`controllers/nodepool/readiness`):
  - class not found: False `NodeClassNotFound`;
  - class terminating: False `NodeClassTerminating`;
  - class Ready Unknown: False `NodeClassReadinessUnknown`;
  - **class Ready False: False with the class's own reason and message, copied;**
  - otherwise True.

  One class failure therefore lights every NodePool that uses it, in the class's words. That is the double count S10 folds.
- **A controller manages only the NodePools whose `nodeClassRef` group and kind its provider supports** (`utils/nodepool.IsManaged`, `GetSupportedNodeClasses`). The AWS provider supports `karpenter.k8s.aws/EC2NodeClass` only. A pool whose class kind no running controller supports gets no status from any of them. This is how self-hosted Karpenter and EKS Auto Mode coexist on one cluster.
- **`nodeClassRef`** is `{group, kind, name}`. The class is cluster-scoped.
- **NodeClaims** carry their own `spec.nodeClassRef`.

### 1.2 `EC2NodeClass` (`karpenter.k8s.aws/v1`)

- **Conditions** (operatorpkg, with `observedGeneration` and `lastTransitionTime`): Ready aggregates `AMIsReady`, `SubnetsReady`, `SecurityGroupsReady`, `InstanceProfileReady`, `ValidationSucceeded` and `PlacementGroupReady`. `CapacityReservationsReady` is a dependent only when that feature gate is on.
- **Status:** `subnets[{id, zone, zoneID}]`, `securityGroups[{id, name}]`, `capacityReservations`, `amis[{id, name, deprecated}]`, `instanceProfile`.
- **Spec:** `role` or `instanceProfile`, and the subnet, security group and AMI selector terms.

### 1.3 `AKSNodeClass` (`karpenter.azure.com`)

- **Versions:** v1.14.5 serves `v1beta1` (storage); `v1alpha2` is still listed but not served.
- **v1beta1 conditions:** `ImagesReady`, `KubernetesVersionReady`, `SubnetsReady`, `ValidationSucceeded` and `LocalDNSReady`. `CapacityReservationGroupReady` is a Ready dependent only when `spec.capacityReservation.groupID` is set.
- **Status:** the images, `kubernetesVersion`, the observed versions, the capacity reservation group and the LocalDNS state.
- **Self-hosted and managed Azure Karpenter** (NAP) serve the same API.

### 1.4 The EKS Auto Mode `NodeClass` (`eks.amazonaws.com/v1`)

- **Closed source.** The spec is documented: `role` or `instanceProfile`, node and pod subnet and security group selector terms, capacity reservations, `snatPolicy`, `networkPolicy`, `ephemeralStorage`, advanced networking, security and compute, `certificateBundles` and tags.
- **Built-ins:** the NodeClass `default`, provisioned when a built-in NodePool (`general-purpose`, `system`) is enabled.
- **Conditions** (community evidence, not a published API reference): `SubnetsReady`, `SecurityGroupsReady`, `InstanceProfileReady` (False `UnauthorizedNodeRole` when the node role lacks its access entry), `CapacityReservationsReady` and `ValidationSucceeded`, aggregated into Ready. So the adapter reads Ready and names the first failing condition generically, never assuming the set.
- **References:** a NodePool must use group `eks.amazonaws.com`, kind `NodeClass`. A wrong reference does not resolve.

### 1.5 Management evidence

- **EKS Auto Mode:**
  - its Karpenter runs outside the cluster;
  - S5's evidence is the node label `eks.amazonaws.com/compute-type: auto` ("built into EKS Auto Mode");
  - the `eks.amazonaws.com` NodeClass API exists only with Auto Mode, but a CRD left behind is not an active feature (C2).
- **AKS NAP:** "NAP automatically deploys, configures, and manages Karpenter on your AKS clusters." No documented in-cluster label or annotation distinguishes NAP's objects. Self-hosted Azure Karpenter uses the same APIs and runs as an in-cluster Deployment.
- **The owner's `aks-dev` no longer resolves** (2026-10-20). AKS evidence in this slice is fixture-only (C6).

## 2. The parts and their source contracts (C1)

| Part | Acquisition | Counted entity | Dependencies and gaps | Clock | Notifications |
|---|---|---|---|---|---|
| `karpenter/nodepools` (changed) | observer watch, **projected** (§7), `karpenter.sh/v1` | one NodePool: its UID | the NodeClaims (node counts, as today), and **the three class kinds** for the link (§3). A class kind that is unreadable or stale leaves the link "not established" or "as of", never a verdict from a gap | none (§3 has no deadline) | yes |
| `karpenter/nodeclaims` (unchanged rules) | observer watch, **projected** (§7) | one NodeClaim | none new; `spec.nodeClassRef` is kept for the inspector | the existing launch deadline | yes |
| `karpenter/nodeclasses` (new) | observer watches of `ec2nodeclasses.karpenter.k8s.aws/v1`, `aksnodeclasses.karpenter.azure.com/v1beta1` and `nodeclasses.eks.amazonaws.com/v1`, each where served, projected | **not counted:** inventory rows with the class's own reading (§4), never Needs you | the NodePools that reference them (users) | none | no |

**Why the classes are not counted** (the double count):
- A NodeClass is configuration, as S9's authentications are: its failure lights the NodePools that use it.
- Counting the class and its pools would count one incident twice.
- Counting the class instead of the pools would hide which pools cannot provision.
- So the class's failure is counted once per NodePool that uses it, as that pool's headline in the class's own words. The class row shows its own reading and its users.
- An unused class that fails lights nothing: no pool can provision from it.

**Served versions:**
- Each class kind is pinned to the version this note was read from: `ServedAmong(v1)`, `ServedAmong(v1beta1)` and `ServedAmong(v1)`.
- Any other served version reads "unsupported version" for that kind, a stated limit, never a decode of an unread schema.
- `v1alpha2` AKSNodeClass is not served by any release this note read.

## 3. The class link and the NodePool rule

**Resolution.** A pool's `spec.template.spec.nodeClassRef` is one of:

| Reference | The adapter | The link |
|---|---|---|
| `karpenter.k8s.aws/EC2NodeClass` | ec2 | resolved in the ec2nodeclasses model |
| `karpenter.azure.com/AKSNodeClass` | aks | resolved in the aksnodeclasses model |
| `eks.amazonaws.com/NodeClass` | eks-auto | resolved in the nodeclasses model |
| any other group/kind | none | **"provider not supported"**: Klyx reads no class. The pool is judged on its own conditions; the row says which group/kind |
| missing or empty | none | the pool's own fault, "no nodeClassRef" (Karpenter's CEL rules forbid it; a pre-v1 object may lack it) |

**Link states,** each kept distinct:
- **found:** the class is retained in a live, current model.
- **not found:** absent from a live, current, uncut read of its kind.
- **kind not served:** a supported reference whose API is not served on the cluster. Klyx says it, and the pool's own conditions stand.
- **unreadable:** forbidden or failing.
- **stale:** last good shown with its time, and nothing claimed current.
- **cut:** the kind's model is capped and the class is not retained, so "not found" is never claimed.
- **unsupported:** as above.

**The NodePool rule.** The worst of independent findings, S9's shape:
1. **Not reconciled.** No conditions: Unknown "not reported" (today's reading of a missing Ready). When the reference is unsupported, the text adds "no running Karpenter supports <group>/<kind>, so nothing reconciles this pool" only if the controller task proves which providers run (§5); otherwise it names the reference alone.
2. **The class, current and found:**
   - **Ready False:** Fault "NodeClass <name> not ready", in the class's words: its first failing dependent condition and reason ("SubnetsReady False: SubnetsNotFound: …"). This replaces the copy in the pool's `NodeClassReady`. Both say the same thing, and the class's own read is current. One finding per pool: the incident is the pool's inability to provision.
   - **Terminating:** Fault "NodeClass <name> is terminating".
   - **Ready Unknown or no conditions:** Progressing "NodeClass <name> not yet reconciled", or Unknown when the class's controller task is not ready or unverified.
   - **Behind its generation** (a class condition's `observedGeneration` behind the class generation): Progressing "NodeClass <name> syncing", never its old Ready.
   - **Ready True while the pool's `NodeClassReady` is False:** Progressing "NodeClass <name> is ready; the NodePool's status has not caught up". This is not a fault from stale copy, and it has no clock: a stuck controller is the controller task's finding (§5).
3. **The class not found** (a proven absence): Fault "NodeClass <kind>/<name> not found". Karpenter's own `NodeClassNotFound` agrees, but it is not needed.
4. **Kind not served, unreadable, stale or cut:** the pool is judged on its own conditions, with `NodeClassReady` read as reported. The text says the link is not established (or "as of …"), and is never a class verdict from a gap.
5. **The pool's own:**
   - `ValidationSucceeded` False: Fault with its reason;
   - `NodeRegistrationHealthy` False: Degraded "nodes fail to register" with Karpenter's reason (not a Ready dependent, so never a fault alone);
   - Ready False for any other reason: Fault with Ready's reason, as today.
6. **Ready True with every finding healthy:** OK.

The NodePool `Ready=False` that Karpenter derives from `NodeClassReady` is one finding with the class's, never two.

## 4. The NodeClass reading (inventory, not counted)

Each class row carries:
- **the adapter's reading:** Ready, then the first failing dependent in the provider's vocabulary, named (`SubnetsReady`, `SecurityGroupsReady`, `AMIsReady`, `InstanceProfileReady`, `PlacementGroupReady`, `CapacityReservationsReady`, `ImagesReady`, `KubernetesVersionReady`, `LocalDNSReady`, `CapacityReservationGroupReady`, `ValidationSucceeded`). Unknown condition types are listed as they are, never dropped;
- **staleness:** a condition's `observedGeneration` behind the generation reads "syncing";
- **terminating;**
- **its users:** the NodePools whose reference resolves to it, complete only when the pool kind is live, current and uncut;
- **provider facts,** per adapter (§7's projection):
  - **ec2:** resolved subnet, security group and AMI counts, a deprecated-AMI flag, the instance profile and the role name;
  - **aks:** the Kubernetes version, the image count and the image family;
  - **eks-auto:** subnet and security group counts, pod-subnet selection in use, and the role name.

The row's reading is display evidence (a fact row, like S9's authentications). The pools carry the verdict.

## 5. The controller task and management (C2)

The Karpenter capability gains a management reading, per platform.

**The positive-evidence table** (precedence top to bottom; each row names its evidence in the UI):

| Evidence read | manager | observed | Controller task |
|---|---|---|---|
| A readable `karpenter` Deployment (the existing `kube-system`/`karpenter` probe, plus Deployments labelled `app.kubernetes.io/name=karpenter` in any namespace), not ready | self-hosted | observed | **not ready** (a fault, as today) |
| The same, ready | self-hosted | observed | healthy |
| `nodeclasses.eks.amazonaws.com` served **and** at least one node labelled `eks.amazonaws.com/compute-type: auto` (S5's evidence; one labelled-node LIST, limit 1) | platform (EKS Auto Mode) | not-observed | **managed**: "managed by EKS Auto Mode; controller health not directly observed" |
| The Auto Mode API served, no Auto Mode node | unknown | not-observed | unknown: "the EKS Auto Mode NodeClass API is installed but no Auto Mode node was found; a retained API is not an active feature" |
| `aksnodeclasses.karpenter.azure.com` served, no `karpenter` Deployment found in a complete read | unknown | not-observed | unknown: "no in-cluster Karpenter found; on AKS this may be node auto-provisioning (managed), which Klyx cannot tell apart from a self-hosted controller it could not find" |
| The Deployment read forbidden or failing | unknown | unknown | unknown (unreadable, never "managed") |

- **Coexistence.** A self-hosted Deployment and Auto Mode evidence together: both are named. The task reports the self-hosted Deployment's observed health (never suppressed) and adds "EKS Auto Mode also provisions (`eks.amazonaws.com` NodeClasses), managed by the platform".
- **Per-pool provenance.** A pool's adapter says which controller kind serves it: ec2 is self-hosted AWS Karpenter; eks-auto is Auto Mode; aks is NAP or self-hosted, unverified. The pool inspector names it.
- **Management never suppresses a fault.** A pool, claim or class fault lights whoever manages the controller. "Managed" is a task reading only.
- **The new `managed` task state** sits beside `not ready` and `unknown` (`healthsrc/controller.go`'s issue states):
  - it is not attention and not a coverage gap;
  - the area shows its reading as a note;
  - Overview, the lens index, Needs you and the fleet indicator never count it.

## 6. The AKS networking note

Shown in the AKSNodeClass inspector, and as a Karpenter-area note on an AKS cluster (nodes with an `azure://` providerID) where Cilium is detected.

**Distinction by evidence:**
- **Azure CNI powered by Cilium:** the Cilium DaemonSet in `kube-system` carries AKS's managed-by label. Microsoft manages the dataplane.
- **BYO Cilium (BYOCNI):** Cilium is present without that label. The note: "Microsoft supports Karpenter-specific issues on BYO CNI, but excludes third-party CNI networking troubleshooting."
- **Unknown:** Cilium's DaemonSet is unreadable, or the label is ambiguous. Nothing is said.

The label key (`kubernetes.azure.com/managedby: aks`) is unverified: no AKS cluster is reachable (§1.5). It is a fixture fact until the owner authorises a read on an AKS cluster. Until then the note shows only as "Cilium on AKS: BYO or Azure-managed is unverified", with the support boundary stated as conditional.

## 7. Projections and budgets

**The class kinds** (new) keep:
- identity, generation, creation, deletion and labels (16, never clipped);
- conditions (8; message 512, scrubbed);
- the adapter's provider facts (counts and names, never IDs beyond the role and instance profile);
- `observedGeneration` per condition.

AKS keeps no subnet ARM ID (only whether one is set); EKS keeps no subnet or security group IDs (counts only).

**The pool and claim kinds** change from whole-object models to projections, which also gives the existing parts their missing budgets:
- **NodePool:** identity, generation, labels, `nodeClassRef`, weight, limits, resources, conditions (8) and `nodeClassObservedGeneration`;
- **NodeClaim:** today's parsed fields, plus `spec.nodeClassRef`.

| Kind | Budget | MaxObject |
|---|---|---|
| `ec2nodeclasses`, `aksnodeclasses`, `nodeclasses` | 128 KiB each | 16 KiB |
| `nodepools` | 256 KiB | 16 KiB |
| `nodeclaims` | 2 MiB | 8 KiB |

- **The projections parse once,** into the structs the rules read (`karpenter.NodePool`, `NodeClaim`). Each MaxObject bounds its kind's cap product, measured by a test (as S9 found it must be).
- **The budget calibration and `TestLoad`** carry the new kinds, before and after.
- **At most 2.6 MiB** of encoded admission per cluster where all three providers are served (in practice one or two). NodeClaims are the large set: 2 MiB is about 3,000 claims at their typical size, and past it the part reads a lower bound.

## 8. What Klyx shows

- **The Karpenter lens** gains a NodeClasses view, with each class's provider, reading and users.
  - The NodePool inspector names its class: a link to it, the link state (§3), the adapter and the provenance (§5).
  - The NodeClaim inspector names its class.
- **The registry:** the raw `EC2NodeClasses` entry is gated on `karpenter.k8s.aws` being served. Raw entries are added for `AKSNodeClasses` and `NodeClasses` (Auto Mode), each gated on its own API.
- **The controller task's `managed` state** shows as the area's note (§5).
- **AI and chat:**
  - classes and the new pool fields are allowlisted (names, the reference, conditions and the counts);
  - never role ARNs, instance profiles, subnet IDs or ARM IDs;
  - chat's GVR map gains the three class kinds.

## 9. Tests, lab, parity, smoke

**Pure (`internal/karpenter`):**
- every adapter's reading, with the first failing dependent in provider vocabulary and unknown condition types listed;
- every link state;
- every NodePool finding: the class's words replacing the copied `NodeClassReady`, the recovered class whose pool status lags, an unsupported provider, a missing ref, `NodeRegistrationHealthy`, `ValidationSucceeded`;
- the projections' equality with the raw reads for every field a rule uses;
- the cap product and the budget calibration.

**Capability:** the §5 table, row by row:
- a self-hosted Deployment by label in a custom namespace;
- Auto Mode with and without its node;
- AKS with and without a found Deployment;
- coexistence;
- forbidden reads.

**The lab** (`lab-s10`, kind-klyx; teardown script):
- CRDs: the real `EC2NodeClass` and `AKSNodeClass` from the provider releases, a stub `NodeClass` for Auto Mode (the API is closed), and the existing `karpenter.sh` stubs;
- classes with written statuses: Ready; `SubnetsReady` False; `InstanceProfileReady` False `UnauthorizedNodeRole`; terminating; behind its generation;
- NodePools referencing each, plus a not-found reference, an unsupported provider and a recovered class with a lagging pool;
- fixture-only for management and the AKS note, said in the as-built notes.

**Parity:**
- the three class CRDs;
- a fixture class per provider (Ready) and a pool referencing it;
- the `nodeclasses` part (evidence) and pool members;
- a case where a class fails: its pools light once each, the class not at all.

**Native smoke:**
- a step opening a NodePool whose `EC2NodeClass` fails, asserting the class's words and the link;
- a step opening the NodeClasses view.

**Captures:** night and day at 1440 and 1024.

## 10. Not in S10

- **Cloud reads:** EC2, ARM, the EKS API.
- **Verifying the instance profile, subnets or AMIs against the cloud:** the class's own conditions are the evidence.
- **Other Karpenter providers** (GCP, cluster-api, kwok): an honest "provider not supported".
- **`NodeOverlay` and `CapacityBuffer`:** later candidates.
- **Verbs:** none (C4). Editing a class or pool is Git's.
- **A live AKS or EKS check:** only on the owner's separate authorisation (C6).
