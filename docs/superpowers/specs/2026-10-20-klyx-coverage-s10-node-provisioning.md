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
- **`status.nodeClassObservedGeneration`** is written by the `nodepool.registrationhealth` controller, not the readiness controller. It is the class generation registration health last observed.
- **`NodeRegistrationHealthy` is reset to Unknown** whenever the condition is missing, the pool's generation moves past the condition's `observedGeneration`, or the class's generation moves past `nodeClassObservedGeneration` (`controllers/nodepool/registrationhealth`). A False value is therefore evidence only for the pool generation and class generation it was written against.
- **Aggregate reason:** when a Ready dependent is False, Ready reads False with operatorpkg's aggregate reason (`UnhealthyDependents`, as re:Post's NodeClassNotFound example shows).
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

- **Self-hosted controllers:**
  - both providers' Helm charts label the Deployment `app.kubernetes.io/name=karpenter`;
  - the controller images identify the provider: `public.ecr.aws/karpenter/controller` (AWS, supports `EC2NodeClass` only) and `mcr.microsoft.com/aks/karpenter/controller` (Azure, `AKSNodeClass`);
  - a custom or mirrored image proves no provider.
- **EKS Auto Mode:**
  - its Karpenter runs outside the cluster;
  - S5's evidence is the node label `eks.amazonaws.com/compute-type: auto` ("built into EKS Auto Mode");
  - the `eks.amazonaws.com` NodeClass API exists only with Auto Mode, but a CRD left behind is not an active feature (C2).
- **AKS NAP:**
  - "AKS manages node auto-provisioning on your behalf and runs it in the managed control plane" (Microsoft Learn, "Enable or disable NAP").
  - **Installation history:** the migration from self-hosted Karpenter is verified by the Karpenter CRDs' annotations `meta.helm.sh/release-name: aks-managed-karpenter-overlay-base` and `meta.helm.sh/release-namespace: kube-system`. A retained annotation proves that NAP installed the CRDs, not that NAP is active now.
  - **Events:** Microsoft documents `kubectl get events --field-selector source=karpenter-events` for inspecting NAP's events. That is a monitoring view, not an enablement or health check: an Event dates an occurrence, it does not prove NAP is enabled now (§5 bounds what it may say).
  - **Networking:** NAP is documented with Azure CNI overlay and the Cilium dataplane.
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

**True fact rows.** A class row carries no `NativeEval` at all. It is not a zero-value Fault, so it is excluded from Top, the ledger, notifications, Needs you, and the Explain and chat verdict gates. Its reading is display evidence beside the row (§4), as S9's authentications and KEDA-owned HPAs are.

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
1. **Not reconciled.** No conditions: Unknown "not reported" (today's reading of a missing Ready). The text adds "no running Karpenter supports <group>/<kind>, so nothing reconciles this pool" only when §5's inventory is complete and every candidate is attributed to a provider. Otherwise it names the reference alone.
2. **The class, current and found:**
   - **Ready False:** Fault "NodeClass <name> not ready", in the class's words: its first failing dependent condition and reason ("SubnetsReady False: SubnetsNotFound: …"). This replaces the copy in the pool's `NodeClassReady`. Both say the same thing, and the class's own read is current. One finding per pool: the incident is the pool's inability to provision.
   - **Terminating:** Fault "NodeClass <name> is terminating".
   - **Ready Unknown or no conditions:** Progressing "NodeClass <name> not yet reconciled", but only when §5 attributes an observed, ready controller to that class's kind (an AWS-imaged Deployment for `EC2NodeClass`, an Azure-imaged one for `AKSNodeClass`). Platform management (Auto Mode, NAP) is never a ready controller: a class it manages with Ready Unknown is Unknown. Otherwise it is Unknown: "no controller attributed to <kind> is established". A ready controller for another provider certifies nothing.
   - **Behind its generation** (a class condition's `observedGeneration` behind the class generation): Progressing "NodeClass <name> syncing", never its old Ready.
   - **Ready True while the pool's `NodeClassReady` is False:** Progressing "NodeClass <name> is ready; the NodePool's status has not caught up". This is not a fault from stale copy, and it has no clock: a stuck controller is the controller task's finding (§5).
3. **The class not found** (a proven absence): Fault "NodeClass <kind>/<name> not found". Karpenter's own `NodeClassNotFound` agrees, but it is not needed.
4. **Kind not served, unreadable, stale or cut:** the pool is judged on its own conditions, with `NodeClassReady` read as reported. The text says the link is not established (or "as of …"), and is never a class verdict from a gap.
5. **The pool's own:**
   - `ValidationSucceeded` False: Fault with its reason;
   - `NodeRegistrationHealthy` False: Degraded "nodes fail to register" with Karpenter's reason (not a Ready dependent, so never a fault alone);
   - Ready False for any other reason: Fault with Ready's reason, as today.
6. **Ready True with every finding healthy:** OK.

The NodePool `Ready=False` that Karpenter derives from `NodeClassReady` is one finding with the class's, never two. Ready False with `UnhealthyDependents`, whose only False dependent is `NodeClassReady`, is that same finding. On a class's recovery, neither the aggregate nor the copy reintroduces it: rule 2's recovered-class reading stands.

**Freshness, per finding:**
- **Each pool condition counts only for the generation it reports.**
  - A condition whose `observedGeneration` is behind the pool's generation is "syncing" for that finding (Progressing).
  - A missing or zero `observedGeneration` reports no generation: the condition is read as reported and the inspector says so. It is never a proven lag.
- **`NodeRegistrationHealthy` False has two fences,** both needed (core resets it when either moves):
  - **current:** its `observedGeneration` equals the pool's generation, and `nodeClassObservedGeneration` equals the class's generation from a current class read. Then it is Degraded "nodes fail to register", with Karpenter's reason;
  - **proven stale:** either fence differs, with both values known. It is historical, a note "last reported before the pool or its NodeClass changed", and never a finding;
  - **not established:** an `observedGeneration` missing or zero, or the class not found, unreadable, stale, cut or unsupported. Unknown "registration health not established".
- **Unrelated findings keep their own freshness.** A stale registration failure never hides a current `ValidationSucceeded` failure, and the reverse.
- **`nodeClassObservedGeneration` is registration health's acknowledgement,** never read as the readiness controller's.

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

The row's reading is display evidence (a fact row, like S9's authentications). The pools carry the verdict. The reading comes from the whole object (§7's cut contract), never from the display list.

## 5. The controller task and management (C2)

**Acquisition: the in-cluster controller inventory.**
- **Candidates:** Deployments labelled `app.kubernetes.io/name=karpenter` in any namespace, plus the existing `karpenter` Deployment probe in `kube-system` and `karpenter`. At most 4 candidates and 8 running pods each, as S9's operator read.
- **Each candidate's provider** comes from its running pods' images, fenced by owner UID (Deployment, then ReplicaSets, then pods):
  - `public.ecr.aws/karpenter/controller` is AWS (`EC2NodeClass`);
  - `mcr.microsoft.com/aks/karpenter/controller` is Azure (`AKSNodeClass`);
  - any other image, or pods that disagree, is unattributed.
- **The inventory read is one of:**
  - **complete:** every list finished, at most 4 candidates, every candidate's pods read;
  - **incomplete:** a continued list, more candidates or pods than are read, or an unattributed candidate;
  - **unreadable:** forbidden or failing.

**Platform evidence** (positive only):
- **EKS Auto Mode:** `nodeclasses.eks.amazonaws.com` served, and at least one node labelled `eks.amazonaws.com/compute-type: auto` (one labelled-node LIST, limit 1).
- **AKS NAP activity** (management evidence only; never a handler, never health):
  - **The read:** core Events cluster-wide, field-selected `source=karpenter-events`, limit 100 per page, at most 3 pages. A continue token past the third page means the read is cut.
  - **Each Event's time:** its `series.lastObservedTime`, else `lastTimestamp`, else `eventTime`, else `firstTimestamp`. An Event with none of these is undated and is no evidence. A repeated Event counts at its latest observation.
  - **The result:** the newest time across every page read, not the first item. The window is one hour, the API server's default Event TTL.
    - **Activity "as of <time>"** when that newest time is within the window. A cut read that already found an in-window Event still establishes it.
    - **Not established** when nothing in the window was found and the read was cut, or the read failed or was forbidden.
    - **No activity found** when the read was complete with nothing in the window.
  - **Refresh:** re-read at the capability re-probe cadence (5 minutes). A failed refresh keeps the last result only until its newest time leaves the window. Then it expires to "not established".
  - **After NAP stops,** retained Events age out within the hour: the reading may say "managed" for up to an hour after NAP is disabled. That is a stated limit, and the reading always says "activity as of <time>".
- **AKS NAP installation:** the CRD annotation `meta.helm.sh/release-name: aks-managed-karpenter-overlay-base`. This is history, never activity on its own.

**Readings, per provider kind** (each pool's class kind is attributed to them; §3 uses them):

| Kind | Proven handler | Reading |
|---|---|---|
| `EC2NodeClass` | an AWS-attributed candidate | its observed health: ready, or **not ready** (a fault) |
| `AKSNodeClass` | an Azure-attributed candidate | its observed health |
| `AKSNodeClass` | NAP activity in the window, no Azure-attributed candidate in a complete read | **managed**: "managed by AKS node auto-provisioning (activity as of <time>); controller health not directly observed" |
| `AKSNodeClass` | NAP installation evidence, without activity in the window | unknown: "CRDs installed by AKS NAP; current activity unverified" |
| `NodeClass` (Auto Mode) | Auto Mode evidence | **managed**: "managed by EKS Auto Mode; controller health not directly observed" |
| any | none, with the inventory complete | unknown: "no controller for <kind> found" |
| any | none, with the inventory incomplete or unreadable | unknown: "the Karpenter controllers could not all be read" |

**The controller task aggregates the kinds in use** (served and referenced, or with classes). Its state is the worst present, in this order:
1. **not ready:** any observed, attributed controller not ready;
2. **unknown:** any in-use kind without a proven handler, or an inventory incomplete or unreadable (a gap);
3. **managed:** every in-use kind proven, and at least one by platform evidence;
4. **healthy:** every in-use kind proven by an observed, ready controller.

**Precedence guarantees:**
- **Platform evidence never clears an unread inventory.** With the Auto Mode or NAP evidence present and the Deployment read forbidden or continued, the task is unknown (the gap). The platform's management is said beside it as a note, never as the task's state.
- **An observed failure is never hidden** by a managed kind beside it. A failing self-hosted AWS controller beside Auto Mode is **not ready**, with Auto Mode's management noted.
- **"Managed" alone** means every in-use kind is proven and the inventory was complete: no unread controller could exist.

**Consumers.** The `managed` state (beside `not ready` and `unknown` in `healthsrc/controller.go`) is a note everywhere: the area, Overview, the lens index, Needs you, the fleet indicator, chat's attention summary, and the Observer's notification batches. It is never attention and never a gap. Unknown stays a gap; not ready stays a fault.

## 6. The AKS networking note

Shown in the AKSNodeClass inspector, and as a Karpenter-area note on an AKS cluster (nodes with an `azure://` providerID) where Cilium is detected.

**Distinction by evidence:**
- **Azure CNI powered by Cilium:** the Cilium DaemonSet in `kube-system` carries AKS's managed-by label. Microsoft manages the dataplane.
- **BYO Cilium (BYOCNI):** established only by positive evidence of a self-installed Cilium (its Helm release metadata). The note: "Microsoft supports Karpenter-specific issues on BYO CNI, but excludes third-party CNI networking troubleshooting."
- **Unknown:** anything else, including the managed-by label's absence. While that label is unverified, its absence is never proof of BYO Cilium.

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

**The cut contract:**
- **The rules classify the whole raw object, before any display cap.** Each projection computes its authoritative fields from every condition:
  - **a class:** Ready, the first failing dependent, and the count of unknown types;
  - **a pool:** Ready, `ValidationSucceeded`, `NodeClassReady` and `NodeRegistrationHealthy`, each with its `observedGeneration`, plus `nodeClassObservedGeneration`.

  These are retained separately from the display list.
- **The display list** keeps 8 conditions, with the total count and a cut flag. A decisive ninth condition is never lost: the reading already holds it.
- **When the retained evidence cannot establish a reading,** for example an object whose authoritative fields themselves were cut (impossible within the caps above, and tested), the reading is Unknown, never OK.
- **Cut flags and counts travel through the DTOs:**
  - a class's `users` is incomplete when the pool kind is cut;
  - a pool's class link is "cut" when the class kind is capped and the class is not retained (§3);
  - an object omitted by its kind's budget is a lower bound on its part, never "absent".
- **Bounded strings:** names 253, condition types and reasons 128, messages 512 (after the §8 identifier redaction for AI paths; the local display keeps the native text within the cap). The cap products are therefore measurable.
- **The budget calibration and `TestLoad`** carry the new kinds, before and after.
- **At most 2.6 MiB** of encoded admission per cluster where all three providers are served (in practice one or two). NodeClaims are the large set: 2 MiB is about 3,000 claims at their typical size, and past it the part reads a lower bound.

## 8. What Klyx shows

- **The Karpenter lens** gains a NodeClasses view, with each class's provider, reading and users.
  - The NodePool inspector names its class: a link to it, the link state (§3), the adapter and the provenance (§5).
  - The NodeClaim inspector names its class.
- **The registry:** the raw `EC2NodeClasses` entry is gated on `karpenter.k8s.aws` being served. Raw entries are added for `AKSNodeClasses` and `NodeClasses` (Auto Mode), each gated on its own API.
- **The controller task's `managed` state** shows as the area's note (§5).
- **AI and chat** (the identifier boundary):
  - **Explicit projections, never the generic one:** chat's projection gains explicit cases for the three class kinds and the changed pool fields (S9's pattern), and the GVR map gains the three class kinds. Only names, the reference, condition types, statuses and reasons, the redacted messages, the counts and the reading are sent; never spec IDs, role ARNs, instance profiles, subnet, security group or AMI IDs, or ARM IDs.
  - **Identifier redaction, before clipping, on every AI and chat path.** Providers write identifiers into condition text (Azure's `SubnetsReady` messages carry the full subnet ARM ID), and core copies that text into every pool's `NodeClassReady` and Ready, so excluding structured fields is not enough. A redactor replaces:
    - ARM resource IDs (`/subscriptions/<guid>/resourceGroups/…`) with `<azure-resource-id>`;
    - ARNs (`arn:aws…:…`) with `<aws-arn>`;
    - AWS resource IDs (`subnet-`, `sg-`, `ami-`, `vpc-`, `eni-`, `lt-`, `cr-`, `pg-` followed by hex) with `<aws-id>`.

    It joins the shared AI scrub chain (`ScrubEvidence` and the chat projection's string pass), so it covers every kind's condition and event text, not only Karpenter's. Egress becomes strictly smaller for every kind.
  - **Karpenter's condition messages never cross,** for every Karpenter kind (the three classes, NodePools and NodeClaims). Pattern redaction cannot catch a bare name: AWS's `ValidationSucceeded` False `InstanceProfileNotFound` reads `Instance profile "<spec.instanceProfile>" not found`, any string, and core copies class text into the pools.
    - **What crosses** to AI and chat is a condition's type, status and reason, plus Klyx's fixed sentence for that type and reason (a table per provider, "the instance profile was not found"; an unknown reason gets "see the condition in Klyx").
    - **Reasons are tokens.** The CRDs' schema pattern (`^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$`) admits no space, slash or hyphen. Klyx re-checks the pattern and withholds any reason outside it.
    - **The native message is a separate field,** for the local inspector only, outside every AI allowlist.
    - **Evaluation reasons are built from the fixed wording.** The rows' `NativeEval.Reason` for these kinds (rule 2's headline, a claim's failing link) uses the type, reason and fixed sentence, so the summary's Top, the ledger, notifications and chat's attention summary carry no provider text.
    - **This holds when the class itself is unavailable:** a pool's inherited `NodeClassReady` or Ready text is never used for AI either.
  - **The paths covered,** each pinned by literal-needle tests on the serialized document:
    - class conditions;
    - pool condition copies and the evaluation reasons built from them (rule 2's headline);
    - Explain's evidence and related-object facts;
    - chat tool results and snapshots;
    - the generic detail projection, which no longer serves these kinds.

    Codex's two design probes become regression tests.
  - **The local UI keeps the native text.** The inspectors show Karpenter's own messages, IDs included: the operator's view, on their machine. The projections keep no ID in their structured fields; only the condition text carries one, and it is redacted before any egress.

## 9. Tests, lab, parity, smoke

**Pure (`internal/karpenter`):**
- every adapter's reading, with the first failing dependent in provider vocabulary and unknown condition types listed;
- every link state;
- every NodePool finding: the class's words replacing the copied `NodeClassReady`, the recovered class whose pool status lags, an unsupported provider, a missing ref, `NodeRegistrationHealthy`, `ValidationSucceeded`;
- the projections' equality with the raw reads for every field a rule uses;
- the cap product and the budget calibration.

**Capability:** §5, row by row:
- a self-hosted Deployment by label in a custom namespace, attributed by image, and a custom image (unattributed);
- Auto Mode with and without its node;
- NAP activity, NAP installation only, and neither;
- an AWS-only controller beside an `AKSNodeClass` (never certifying it);
- mixed providers with one failing;
- an unattributed candidate;
- Auto Mode plus a forbidden or continued Deployment list (unknown, Auto Mode noted);
- a failing controller beside Auto Mode and a read gap (not ready, both said);
- the `managed` state across every consumer, chat's attention and a notification batch included.

**Freshness and cuts:**
- `NodeRegistrationHealthy` against both generation changes, mixed-age conditions, and the unreported case;
- a recovered class with the pool's aggregate Ready=False `UnhealthyDependents` and its copy;
- a decisive ninth condition;
- unknown condition types beyond the display cap;
- a class found in a capped kind;
- a reference omitted by the kind budget.

**The identifier boundary:**
- **Needles:** an ARM ID, an ARN, `subnet-`, `sg-` and `ami-` IDs, and a bare, non-ARN, unprefixed instance-profile name (`Instance profile "ExamplePrivateProfileNeedle" not found`).
- **Where they are planted:** class conditions, a pool's copied messages with its class unavailable, a NodeClaim's failing link, and the spec.
- **Where each is asserted absent:** the serialized Explain document, related facts, chat tool results and snapshots, and chat's attention summary.
- **Where each is asserted present:** the local inspector DTO.

**NAP activity:**
- an old first page with a recent Event on a later page;
- an expired Event, and an undated one;
- a repeated Event at its latest observation;
- a cut read with and without an in-window Event;
- a failed refresh expiring at the window;
- retained activity after NAP stops, ageing out;
- never a handler: a NAP-managed class with Ready Unknown stays Unknown.

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

## 11. Revision 1: Codex's design review of cd00d87

| # | Finding | Where answered |
|---|---|---|
| P1 | The AI and chat exclusion did not cover copied condition text or the complete evidence document | §8: explicit chat projections for the three class kinds; an identifier redactor (ARM IDs, ARNs, AWS resource IDs) applied before clipping on every AI and chat path, in the shared scrub chain; literal-needle tests on the serialized Explain, chat and facts documents; the local UI keeps the native text; §7's wording reconciled |
| P2 | `NodeRegistrationHealthy` needed its pool and class generation fences | §1.1 (core's reset rule, and who writes `nodeClassObservedGeneration`); §3's freshness per finding: current with both fences, historical when proven stale, unknown when not established; unrelated findings keep their own freshness |
| P2 | Auto Mode evidence could win over a forbidden controller read | §5: the inventory's acquisition outcomes; platform evidence never clears an unread inventory; the task's order is not ready, unknown, managed, healthy; managed alone only with a complete inventory |
| P2 | A healthy controller needed provider attribution | §5: candidates attributed by their running pods' images (owner-UID fenced); readings per class kind; §3 rules 1 and 2 use only an attributed handler; "no controller supports this kind" only with a complete, attributed inventory |
| P2 | Per-object projection cuts needed a verdict and completeness contract | §7: the rules classify the whole object before display caps; authoritative fields kept apart from the 8-condition display list; Unknown, never OK, when evidence cannot establish a reading; cut flags and counts through the DTOs; bounded strings |

**Guidance adopted:**
- the NAP facts (managed control plane, the CRD annotations, `karpenter-events`) in §1.5 and §5;
- true fact rows for classes (§2);
- the `managed` state pinned across every consumer (§5);
- the aggregate `UnhealthyDependents` on recovery (§3);
- the absence of the managed-by label never proving BYO Cilium (§6).

## 12. Revision 2: Codex's re-review of 33c12b4

| # | Finding | Where answered |
|---|---|---|
| P1 | A bare instance-profile name in condition text passed every identifier pattern | §8: Karpenter's condition messages never cross to AI or chat, for every Karpenter kind. Only the type, status, reason token (pattern-checked) and Klyx's fixed sentence cross. Evaluation reasons are built from that wording, inherited pool text included. The native message stays a local inspector field. §9's needles add a bare profile name |
| P2 | NAP's Event predicate lacked a time and acquisition contract | §5: a bounded field-selected read (3 pages of 100); the timestamp precedence; the newest time across pages; a one-hour window; cut, failed and forbidden outcomes; expiry at the window on a failed refresh; up to an hour after NAP stops, stated. It is management evidence only, said "as of <time>", and never a handler or health (§3 rule 2) |

**Design accepted at 15652ed** (Codex, revision 2, handoff b937964f, 2026-10-20), with no remaining findings. This accepts the design only; the implementation and its gates keep their own review.

## 13. As built

Built on `feat/coverage-s8` after the design's acceptance at 15652ed:
- **c5f8c49** - the pure package: the projections, the class adapters and readings, the class link, the NodePool rule as worst-of findings, and Klyx's fixed wording.
- **4c64781** - the capability: the controller inventory with provider attribution, the class kinds in use, EKS Auto Mode and AKS NAP evidence, §5's readings and the task's order.
- **acbaba0** - the Observer: the class kinds, the `nodeclasses` evidence part, the pool join, and the "managed" controller state.
- **38e672b** - the identifier boundary on every AI path, and the S10 DTOs.
- **fb3e89e** - the frontend: the NodeClasses view, the class link in the inspectors, and "managed" as a note everywhere.
- **baaab79** - the gates: parity with case 10, the smoke steps, the calibration and load fixtures, the conditions layout and the registration phrase.
- **649973f** - the mutations' tests.
- **723ac27** - the AKS networking note (§6).
- **The commit carrying this section** - these notes.

### 13.1 What it is

- **`internal/karpenter`:**
  - the projections of NodePools, NodeClaims and the three class kinds, with authoritative conditions read from the whole object before the 8-condition display cap, every string clipped and copied, and the reason checked against the schema's token pattern (`ReasonToken`);
  - the adapters (`ClassKinds`, `KindOf`, `KindFor`) and each adapter's reading (`NodeClass.Read`): terminating, then syncing, then Ready with the first False dependent in the provider's order; unknown condition types listed and counted;
  - the class link (`Resolve`) with every state kept distinct;
  - the NodePool rule (`PoolRule`) as worst-of findings, each with its own freshness, and `NodeRegistrationHealthy`'s two fences;
  - Klyx's fixed wording (`Sentence`, `Phrase`) and the provider's own (`Native`, local only); the NodeClaim reason switched to the fixed wording.
- **`capability`:** the controller inventory (labelled Deployments and the conventional `karpenter`, at most 4 candidates and 8 pods each), attribution by the running pods' images fenced by owner UID, the class kinds in use, EKS Auto Mode's and AKS NAP's evidence, §5's per-kind readings (`Reading`, `NoneSupports`) and the task's state (`Task`), and the NAP refresh rule (`Refreshed`, applied at the re-probe).
- **`healthsrc`, `fleet`:** the three class kinds pinned to their versions, projected and budgeted beside the projected NodePools and NodeClaims; the `nodeclasses` evidence part (fact rows with their reading and users); the pool join reading the capture's coverage and the detector's readings (`FactKarpenter`); "managed" beside "not ready" and "unknown"; per-kind served flags on the cluster DTO.
- **`scrub`, `ai`, `appbridge`:**
  - the identifier redactor (`scrub.Identifiers`) in `ScrubEvidence`, before the cap;
  - the Karpenter evidence policy (`karpenterEvidence`, `karpenterBundle`), explicit chat projections for the five kinds, the chat event and list rules, the GVR map's class kinds, Suggest-fix's refusal for classes and status-free manifests for pools and claims;
  - lens allowlists that name no native field;
  - the DTOs: a pool's link, findings, handler reading and conditions, a claim's class, and the class rows.
- **§6, the AKS networking note:** the detector reads one node's `providerID` with the AKSNodeClass kind served (`AzureNodes`, an `azure://` node). Where that and Cilium are both detected, the Karpenter lens and the AKSNodeClass inspector say "Cilium on AKS: BYO or Azure-managed is unverified", with the support boundary stated as conditional. No managed-by label is read: it is unverified, so the note is never a finding and its absence never proves BYO Cilium.
- **The frontend:** the NodeClasses view and its inspector; the pool inspector's headline in Klyx's words, its class block and other findings; the claim's class; the task-driven controller notice; "managed" as a note on the area, Overview, the lens index, the fleet lamp and reading, the axes and Needs you; the raw entries gated per class kind.

### 13.2 Decided while building

- **The per-object maxima stay at §7's table, by measurement.** With every retained field at its cap, an EC2NodeClass projects to 15,880 bytes, an AKSNodeClass to 15,629, a NodePool to 16,334 and a NodeClaim to 2,735 (`TestMaxObjectBoundsTheCapProduct`, every input past its cap). Each fits its MaxObject (16, 16 and 8 KiB). A NodeClaim keeps no labels of its own (the pool, capacity type and instance type are read into fields), and an authoritative condition counts its strings only when the display list does not hold them.
- **Two link states beyond §3's list.**
  - **Unsupported version:** §2's "served only at another version" made a link state. The detector records it per kind (`KarpenterKind.OtherVersion`), since the Observer's probe reads it as an error.
  - **Unresolved:** no class evidence was supplied (the zero value). It reads "class link not read", beside the pool's own conditions, and a found link without its class falls back to it.
- **A NodePool's verdict carries no `Since`.** The role is the worst of several inputs (the class, its link, the controllers), so its start is not provable (The Line §8.1). The deciding condition stays the dated evidence (`Cond`). The relocated golden case "no state" left the parity harness: the Go rule now reads it "not reported".
- **The fixed wording is keyed by reason, across condition types.** Core copies a class's reason into `NodeClassReady`, and `NodeRegistrationHealthy` carries a claim's launch reason, so one sentence per reason serves every condition that carries it.
- **A finding with no `observedGeneration` is flagged, not worded** (`Finding.Unversioned`): the inspector says "read as reported", and the AI text stays Klyx's sentence.
- **A class's syncing reads Ready and its failing dependent.** Either behind the generation means neither is the current reading.
- **Karpenter's event messages are withheld too, stricter than §8.** The controllers' launch errors carry the same text as their conditions (an instance profile's name among it). Explain's events for a Karpenter target and chat's events for any Karpenter object send their reason only.
- **Suggest-fix, which §8 did not list.** A class's spec holds the role, the instance profile, selector IDs and possibly user data, so Suggest-fix refuses a NodeClass before anything is read. A NodePool's or NodeClaim's manifest is sent without its status (the condition text) and with identifiers redacted.
- **The lens's own controller probe is retired.** It read one Deployment by name and would contradict the task on EKS Auto Mode. The lens's notice says not ready, unverified or managed from the task's reading, and the snapshot carries only the disruption feed.
- **"Managed" in the lens index is a title note,** never one of the coverage words on the row, so it neither lights nor qualifies a count.
- **The kinds in use** are read at the capability probe: classes (a one-item list per served kind) or a NodePool's reference (one page of up to 500 pools). A pool list that fails or continues rules no kind out.
- **NAP's window is evaluated at the probe** (every 5 minutes), so a reading can stay "managed" for up to one probe past the hour; `Refreshed` applies the failed-refresh rule at the re-probe.
- **The lab's pre-S10 stub pool** (`general-purpose-spot`, created by an earlier slice with no `nodeClassRef`) gains a reference to `lab-ready` in `lab-s10/apply.sh`: a v1 NodePool must have one, and S10 reads its absence as a fault. Teardown removes it.
- **Parity's controllers are attributed stand-ins.** The pause image is tagged in the parity node's containerd under each provider's controller image (no pull), one labelled Deployment per provider, and the node carries EKS Auto Mode's label. Karpenter's controller task reads "managed" in every case.

### 13.3 Verified

- **The lab** (`lab-s10` on kind-klyx, `relabs.sh` applies it with the other labs): the real EC2NodeClass and AKSNodeClass CRDs accept every written status. Read through Klyx:
  - **NodePools** "7 not ready or failing to register · 1 unknown": `lab-subnets-a` and `lab-subnets-b` each "NodeClass lab-subnets not ready" (once per pool, in Klyx's words: "SubnetsReady False: SubnetsNotFound: no subnets matched the selector terms", the provider's own text beside it locally); `lab-missing` "NodeClass EC2NodeClass/lab-does-not-exist not found"; `lab-terminating` "NodeClass lab-terminating is terminating"; `lab-aks` and `lab-auto` their classes' failures (`SubnetNotFound`, `UnauthorizedNodeRole`); `lab-registration` "nodes fail to register" (degraded); `lab-behind` "NodeClass lab-behind syncing"; `lab-recovered` "NodeClass lab-ready is ready; the NodePool's status has not caught up"; `lab-gcp` "not reported", "no running Karpenter supports karpenter.k8s.gcp/GCENodeClass, so nothing reconciles this pool" (no controller runs on kind-klyx, so the inventory is complete and empty); `lab-ok` and `general-purpose-spot` ready.
  - **NodeClasses:** six rows, never counted; `lab-subnets` used by "lab-subnets-a, lab-subnets-b"; `lab-aks`'s facts (Kubernetes version 1.33.2, one image, AzureLinux, the subnet set, never its ID).
  - **The controller task:** unknown, "EC2NodeClass: no controller for EC2NodeClass found; AKSNodeClass: …; NodeClass: …".
  - **Not on kind:** management (EKS Auto Mode, AKS NAP) and §6's note (kind's nodes are `kind://`) are fixture and unit tested only, as §1.5 planned: no AKS or EKS cluster is reachable.
- **The parity gate,** both stages, on `kind-klyx-parity`: the baseline with one class per provider adapter and Karpenter's controller task "managed" in every case; case 10 lights `parity-pool` and `parity-pool-b` once each and the class (an evidence part) not at all. Every case and both recoveries pass.
- **The captures** (headless, the server-mode build on the smoke fleet; night and day at 1440 and 1024), none scrolling sideways, every expected text present: Overview's Karpenter area, the NodePools list and the inspectors of nine pools (above), the NodeClasses list and the inspectors of `lab-subnets`, `lab-aks`, `lab-auto-role` and `lab-ready`.
- **The budget calibration** (`TestKarpenterHeapCalibration`): each kind filled to its budget, each source built and dropped inside the measurement.

| Kind | Shape | Objects | Encoded each | Retained heap | Heap per encoded byte |
|---|---|---|---|---|---|
| nodepools | typical | 250 | 1,047 B | 519 KB | 1.98 |
| nodepools | at the caps | 17 | 14,970 B | 306 KB | 1.20 |
| nodeclaims | typical | 4,723 | 445 B | 2.76 MB | 1.32 |
| nodeclaims | at the caps | 1,069 | 1,960 B | 2.32 MB | 1.11 |
| ec2nodeclasses | typical | 125 | 1,041 B | 244 KB | 1.88 |
| ec2nodeclasses | at the caps | 9 | 13,808 B | 138 KB | 1.11 |
| aksnodeclasses | typical | 119 | 1,098 B | 233 KB | 1.78 |
| aksnodeclasses | at the caps | 9 | 13,558 B | 136 KB | 1.12 |
| nodeclasses | typical | 125 | 1,048 B | 243 KB | 1.86 |
| nodeclasses | at the caps | 9 | 13,557 B | 136 KB | 1.11 |

  The encoded admission budget is 2.62 MiB per cluster with every class kind served (in practice one or two). NodeClaims fill their 2 MiB at about 4,700 typical claims, more than §7's estimate of 3,000; past it the part reads a lower bound.
- **The load test** (`TestLoad`) now carries S10: one class per adapter and 20 NodePools spread over them on the large cluster, 3 on each small one. The same fixture ran on the code before S10 (97fbf7f, the class kinds unwatched) and after, interleaved, with the server-mode review build running throughout for both (so not an idle machine):

| | before (10 runs) | after (6 runs) |
|---|---|---|
| first complete summary, large | 1.05-1.07s | 1.05-1.07s |
| Observers' retained heap, steady | 61.1 MB | 61.2 MB |
| recovery peak / steady | 1.21x-1.64x | 1.20x-1.64x |
| full evaluation, large (max of 5) | 69.0-75.0 ms | 69.6-74.1 ms |
| every kind live after the API recovered | 8.35-8.75s | 8.34-8.93s |

  - **What S10 costs at this scale:** about 0.1 MB of steady heap, and no measurable evaluation time.
  - **The recovery peak is noisy before and after alike.** Four of the six runs after S10 went over the 1.5x budget (1.51x-1.64x), so I measured where the peak comes from. The code before S10 also reached 1.64x (one of ten runs). S10's code with no class kind served reached 1.58x, and with Karpenter's kinds and task removed entirely 1.69x. The peak depends on whether a GC lands while a large kind's old model and new list are both held during the re-lists, not on Karpenter's kinds, which hold under 100 KB here. It is a pre-existing flake in the budget's measurement, recorded for the owner.
  - **Full evaluation** misses 50 ms before and after alike: the open owner question S9 recorded.

### 13.4 Gates

- **The native smoke:** 197 of 197 steps in each of night and day at 1440 and 1024 on 723ac27 (a clean tree), with the two S10 steps:
  - **a failing class:** `lab-subnets-a` leads with "SubnetsReady False: SubnetsNotFound: no subnets matched the selector terms", its class `EC2NodeClass/lab-subnets` linked;
  - **the NodeClasses view:** `lab-subnets` used by "lab-subnets-a, lab-subnets-b", said as configuration.
  - The first run failed the second step in all four modes: it queried the rows before the view had switched, and clicked the selected pool off. The step now waits for the NodeClasses columns.
- **The parity gate:** both stages pass, every case and both recoveries.
- **`relabs.sh`** applies `lab-s10` with the other labs; `lab-s10/teardown.sh` removes it.
- **Go:** `go test -race` passes for `karpenter`, `capability`, `healthsrc`, `fleet`, `appbridge`, `ai`, `scrub`, `observe` and `health`; golangci-lint reports 0 issues.
- **The frontend:** tsc is clean, eslint has 0 errors (its 3 warnings predate S10), and vitest passes 2,339 tests.
- **Mutations:** 70, all caught (68, and two more on §6's note: any node read as AKS, the note without Cilium).
  - 59 on the first pass. Four did not build (a mutation left a variable unused or a case duplicated) and were rewritten to compile.
  - The five survivors each gained a test: a claim's withheld reason; an Unknown dependent never read as the failure; a pool with only notes reading "ready"; NAP activity beside an unread inventory leaving the kind's handler unproven; the local finding keeping its native text.
  - No equivalents.
