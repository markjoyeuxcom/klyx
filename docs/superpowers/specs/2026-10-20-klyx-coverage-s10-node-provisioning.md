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
