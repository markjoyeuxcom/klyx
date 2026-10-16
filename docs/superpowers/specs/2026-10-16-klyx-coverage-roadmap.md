# Klyx first-class coverage: the roadmap

Owner direction, 2026-10-16:

> "i have on prem clusters, eks clusters. i think it would be good to have key aks support as there are people want to use klyx with aks, i will use it with eks and onprem mainly. flux should be fully supported, flux operator + flux image automation. velero should be supported also. 1 to 9 should be done."

Two follow-up questions, two answers:
- Asked which alerting their stacks use: "alert manager".
- Asked whether on-prem LoadBalancer health belongs in: "yes it should".

Items 1-9 come from the gap analysis of the same day, checked against the code at 1e35bf6 (Prax: "Klyx first-class coverage gaps"). This programme comes **before** the extensions work. The extensions note (`2026-10-16-klyx-extensions-research.md`) and Codex's order for it (inspector adapters, then opt-in fleet health, then trusted actions) stand after it.

This is the parent spec. Each slice gets its own note, design review, build, implementation review, native smoke and live captures, as The Line did. Revision 1 answers Codex's design review of 1487ca2 (§6).

## 0. What first-class means here

- **Klyx's own logic:**
  - joins across objects (an identity chain, a ScaledObject and its HPA, a webhook and its backend);
  - queries to a system outside the cluster (Alertmanager, Loki);
  - the tool's own verbs;
  - a drawing where the shape is the point.

  A CRD that only has a `Ready` condition is pack material, not part of this programme.
- **The tool's own vocabulary:** Velero's phases, KEDA's Active/Fallback/Paused, Flux's Ready/Stalled/Reconciling.
- **Capability-detected, with management kept apart from health** (§1 C2). A tool that is not installed renders nothing.
- **Health-bearing parts join the shared engine** under an explicit source contract (§1 C1), so their faults reach Needs you, the mast and the fleet spine with the same semantics as built-in kinds. A failed or forbidden read renders as an error, never as quiet. API absent, forbidden and unknown stay distinct.
- **Evidence, never inference:** where Klyx cannot prove something (a mutation's author, an annotation's change time, a cloud-side association, a restorable backup), it says what it saw and what it cannot see.
- **Platforms:** the owner's daily clusters are EKS and on-prem (and the k3s ROCm homelab), and AKS stays first-class for other users. Cloud-specific pieces are listed provider adapters, each degrading to an honest "not this provider" elsewhere.

## 1. Shared contracts

**C1. A health-bearing part: its source contract, then its wiring.**

*The source contract*, stated in each slice note for every part:
- **Acquisition mode:** an observer watch (full or projected), on-demand read, coordinated snapshot, or external poll.
- **Scope, projection and supported served versions:** what a mixed-version or partial installation reads as.
- **The counted entity and its identity:** a UID for API objects, or a stated identity for external evidence. Inclusion vs role.
- **Dependency completeness:** what a missing, unreadable or stale dependency does.
- **Clock boundaries:** what changes with time alone, and the clock-driven re-evaluation that notices it.
- **Notification eligibility:** whether the part may raise OS notifications. External evidence may not by default (§2 S8).
- **No synthetic evidence:** no fabricated conditions or `observedGeneration` to fit a fixture or a template. Phase-based APIs (Velero) keep their phases.

*The wiring* (file references from the code map of 2026-10-16):
- **Backend:**
  - a pure package (`Parse*`, `*Rule → health.NativeEval`);
  - a `crdKind` with per-kind served-version probing (`healthsrc/native.go`), and a `DomainSpec`/`PartSpec` (Counted, Deps, Evaluate, Ledger);
  - controller detection in `capability`, and the controller task;
  - the rows converter and its DTO mirror test;
  - the AI field allowlist, the chat GVR-to-task map, and the notification kind label (only where notification-eligible).
- **Frontend:**
  - `nav/clusterAttention.ts` (TASKS, KIND_LABELS), `fleet/queue.ts`, `nav/reveal.ts`, `console/openHit.ts`;
  - the registry group and `ClusterDetail` section;
  - the smoke `TITLES` entry.

*The tests:*
- **The parity gate:** the CRD on `kind-klyx` and in `klyx-parity.sh`, a fixture function in `parity/fixture.sh` called from the baseline, a controller stand-in where there is a controller task, and the oracle's parts and controller tasks (`parity_lab_test.go`) and `ROW_PARTS`/`CASES` (`parityGate.test.tsx`).
- **Beyond parity:**
  - projection equality (raw read == projected read, for every field a rule uses);
  - baseline, continuous, after-gap, loss and cap cases;
  - UID replacement (deleted and created again under its name);
  - clock-seam tests for any deadline (stable live fixtures stay time-free, but deadlines are tested with an injected clock);
  - older, partial and mixed-version installations as fixtures, not only the current version on one cluster.

*The budget:* RoleCap bounds ledger evidence, not retained objects. The programme adds high-churn sets (Velero Backups, DRA ResourceClaims, Flux generated inventories). Each slice states its watch count, retained-model size and projection per cluster, against a fleet-wide budget kept in this spec's "As built" sections. A set too large to watch is read on demand, or kept as a capped projected watch, and its caps are said.

**C2. Management is not health.** Lands before S5, where EKS Auto Mode identity needs it.
- **Two separate facts:**
  - *who manages a controller:* self-hosted, platform-managed, or unknown;
  - *whether Klyx can observe its health:* observed, or not directly observable.
- **Positive evidence only.** Platform management is claimed only on evidence that distinguishes it. An `AKSNodeClass` CRD is not NAP, because Azure's self-hosted Karpenter uses the same API; a retained CRD is not an active feature. Insufficient evidence stays "unverified".
- **A positive-evidence table per platform** belongs in the slice that introduces each platform, with precedence and fallbacks: self-hosted vs managed Azure Karpenter, retained CRDs, managed and self-hosted coexisting, and unreadable evidence.
- **Management never suppresses a fault:**
  - a readable managed Deployment that is not ready still lights the task;
  - a managed controller never turns a forbidden read into absence, and never suppresses resource faults;
  - where health truly cannot be observed, the task says "managed by the platform; controller health not directly observed". It is neither healthy nor failed.

**C3. Shared observer kinds: one gate per consumer, three-state.** Lands before S3's evaluator.
- **The gate logic:**
  - any consumer with *proven* demand opens acquisition;
  - stopping requires every consumer to prove absence;
  - false plus unknown is a coverage gap, never "absent".
- **Consistency:** a consumer's eligibility is captured atomically with its evaluation, never read from mutable UI state. One shared model and projection must satisfy every reader.
- **No borrowed presence:** an inactive consumer never looks installed because another consumer opened the source. Today `services` and `endpointslices` are gated on the Gateway API probe, and S3 and S4 need them without Gateway API.

**C4. Verbs.** Only the tool's own day-2 verbs, in a per-kind verb matrix stated in each slice note.
- **Every verb carries:**
  - a UID precondition (the verb acts on the object the user saw, or refuses);
  - ConfirmDialog, plus ProtectGuard on protected clusters;
  - its GitOps-ownership consequence (a verb on a Flux- or Argo-owned object says Git will reconcile it back).
- **Verb mechanics:**
  - Flux uses each kind's own mechanism: `spec.suspend` where the kind has it, and the Flux Operator's reconcile annotations where it does not (no universal `spec.suspend`).
  - KEDA pause is its pause annotation, never a replica-count change; `paused-replicas` is a different operation, not offered.
  - A debug profile that adds privilege is named in the confirmation, never presented as a cosmetic image choice.
- **The scale verb** warns when an HPA or a ScaledObject owns the replica count.
- **No restore, no "backup now", no authored objects.** Any new write path is noted as a crossing of the non-goals.

**C5. Credentials for external endpoints.** A separate, reviewed preparatory change before S8.
- **Endpoints** (Alertmanager, Loki, and the existing metrics endpoint) are resolved as metrics are: an endpoint, a `serviceRef` through the apiserver proxy, or discovery.
- **Credentials** are endpoint-scoped (a token or basic auth) and kept in the **OS keychain**. The existing plaintext metrics token migrates:
  - the keychain write comes before the plaintext is removed;
  - a failure leaves a recoverable state;
  - an unavailable or locked store is a visible state.
- **Tenant headers** (`X-Scope-OrgID`) are configuration, not secrets.
- **Isolation:** external credentials never ride the kube-apiserver's authentication, and redirects never carry them.
- **Log text bound for AI** goes through `internal/scrub`. The operator's own log view stays unscrubbed and honest.

**C6. The lab.**
- **Fixtures** go on `kind-klyx` (v1.37): CRDs plus objects whose status is written through the status subresource, with pause stand-ins for controllers.
- **Real components only where behaviour is the point:** in-place resize and ephemeral containers (kubelet), Alertmanager and Loki (small single binaries), DRA objects, and a LoadBalancer controller (MetalLB or Cilium LB IPAM in kind).
- Every lab write stays on `kind-klyx` or `kind-klyx-parity`.
- **The lab does not verify** cloud management (EKS, AKS) or GPU drivers. Completion claims for those are fixture-only, unless the owner separately authorises a read-only check on a named cluster (the ROCm homelab for S11), recorded in the slice note.

## 2. The slices, in order

Order: a small correctness fix that stands alone; the owner's two named priorities; then each slice in dependency order (C3 before S3 and S4, C2 before S5, C5 before S8). The owner asked for on-prem load balancing right after admission, which also follows C3.

**S0. The pod projection fix:**
- `ProjectPod` drops pod-level `spec.resources` and `spec.overhead`, while `podRequest` reads both, so observer pod rows under-report scheduled CPU and memory.
- The fix keeps both, with a projection-equality test.
- Resize-aware accounting is S6, not this fix.
- *As built:*
  - `ProjectPod` keeps `spec.resources` and `spec.overhead`, deep-copied.
  - Two fixture pods join `TestProjectionParityPods`: one with pod-level requests (2 CPU, 1Gi) and one with a RuntimeClass overhead (250m, 120Mi).
  - Before the fix the projection read 0.1 CPU for both, against 2 and 0.35 raw. After it, raw and projected summaries are equal.

**S1. Flux, complete** (owner priority). Three reviews and builds:
- **S1a. Flux Operator:** `FluxInstance`, `FluxReport`, `ResourceSet`, `ResourceSetInputProvider` (`fluxcd.controlplane.io`), at supported per-kind versions.
  - **Detection:** Flux is detected by the operator as well as by kustomize-controller.
  - **What FluxInstance shows:** its declared components, distribution and sync.
  - **What FluxReport shows:** an as-of report of what the operator observed. Its Ready means the report succeeded. It never replaces Klyx's own counts of Flux objects, and operator availability is not proof that each Flux component is healthy.
  - **ResourceSets** show their inputs (bounded and redacted; inputs can be sensitive, and stay out of AI facts) and their generated inventory.
  - **Mixed generations are normal:** optional components and mixed CRD generations are not a broken installation.
- **S1b. Image automation:** `ImageRepository`, `ImagePolicy`, `ImageUpdateAutomation` (`image.toolkit.fluxcd.io`).
  - Scan health and the latest image per policy.
  - The automation's last push, joined to the actual source and push repository and branch, with its own timestamp.
  - A push is not evidence that a workload applied it; the line's applied facts stay separate.
- **S1c. Source composition and notification facts:**
  - `ExternalArtifact`, and the source-watcher's `ArtifactGenerator` composing and decomposing sources: a source → generated artifact → reconciler dependency, in the dependency graph and the bound-source panels. The line test that marks ExternalArtifact as an unknown applied source changes.
  - Facts for the notification kinds (Providers, Alerts, Receivers).
- **Placement:** all of it lives inside the Flux lens (design principle 2).
- **Verbs** follow C4's per-kind matrix. The test that asserts ImagePolicy is unsupported changes.

**S2. Velero** (owner priority, read-only). Two reviews and builds:
- **S2a. Health.**
  - **Background evidence, independent of any list being open:**
    - Schedules and BackupStorageLocations are watched;
    - Backups are a capped, projected watch (phase, schedule label and owner, timestamps, errors and warnings counts), with as-of and cap coverage said.
  - **Faults:**
    - *An unavailable storage location:* the BSL's phase and its last validation time.
    - *A missed schedule:* the schedule's cron, in its declared time zone (`CRON_TZ`) and across DST, gives the scheduled opportunities. A schedule has missed when no successful Backup started for an opportunity older than a grace period. An explicitly configured maximum success age may replace this.
    - *Recent failed and partially failed backups*, distinct from each other.
  - **Schedule states, each named and none lighting falsely:** new, paused, FailedValidation, `skipImmediately`, a backup in progress, and no known success.
  - **Phases kept as they are:** no synthetic Ready conditions on Velero's phase-based APIs, and none on VolumeSnapshotLocations, which carry no health.
- **S2b. The fleet horizon and lists:**
  - a fleet-wide "latest retained successful backup per schedule": the newest Completed Backup by `completionTimestamp`, with PartiallyFailed kept distinct;
  - lists of Backups and Restores (Restores are read; only *running* one is out of scope).
- **Identity:**
  - a schedule recreated under the same name is a new schedule (UID), so the schedule-name label alone does not prove association;
  - an old failed run stops lighting Needs you once a newer run succeeds;
  - expiration and deletion take entries out of the history, and the list says when it is partial or truncated.
- **Honest wording:** "latest retained successful backup", never "restorable". Completed is not a restore test.

**S3. Admission.** C3 first, then two reviews and builds:
- **S3a. Backend availability and policy inventory:**
  - Validating and Mutating webhook configurations, ValidatingAdmissionPolicy and MutatingAdmissionPolicy (GA 1.36), with their bindings.
  - **The fault:** a fail-closed webhook whose backend is unavailable reads "matching requests may fail". A backend is unavailable when it names a missing Service, the Service has no ready endpoints, or the Service port does not map. It never reads "blocks these objects": rules alone omit selectors, matchConditions, exclusions and bindings.
  - **Evidence limits, each said:**
    - a URL-backed webhook has no Service to inspect;
    - unreadable EndpointSlices are not empty;
    - a selectorless Service has its own endpoints;
    - ready endpoints do not prove apiserver reachability, TLS trust or admission success.
  - **CEL policies** have no Service; their diagnostics are their own (bindings and params, and the type-checking warnings in status).
- **S3b. Proven attribution:**
  - a write error that names its webhook or policy is attributed to it (the existing parser in `InstanceDetail.tsx`), with the policies and webhooks matching the request listed as *candidates*, labelled so;
  - a live-vs-Git difference is **never** attributed to a mutator, and never discounted as "not drift". Mutation attribution stays unknown; an audit-log integration is a separate scope.

**S4. On-prem load balancing** (owner: "yes it should"):
- **A provider-neutral diagnostic** for a `LoadBalancer` Service with no assigned address: the requested class (`loadBalancerClass`) and the controller that should serve it, the allocation status, the Service's events, and how long it has been pending.
- **A Service pending past a grace period** reaches Needs you.
- **Linked controller evidence** where present: MetalLB `IPAddressPool` and `L2Advertisement`/`BGPAdvertisement`, Cilium LB IPAM `CiliumLoadBalancerIPPool` (with its conflict and exhaustion conditions), and k3s ServiceLB (its `svclb-*` pods).
- **Allocation is not proof of working traffic or BGP.** Full BGP session diagnosis stays a later candidate.
- **Needs C3** (`services` without Gateway API).

**S5. Workload identity.** C2 first; on demand and evidence-qualified.
- **Provider adapters, non-exhaustive:**
  - EKS IRSA (`eks.amazonaws.com/role-arn`);
  - EKS Pod Identity: in-cluster evidence only. EKS Auto Mode includes the capability without a user-installed agent DaemonSet (C2). The association itself lives in AWS, and its absence from Klyx is a visible limit, never an inferred broken association;
  - Azure Workload Identity: the label matters only where Azure is opted in;
  - GKE: the IAM service account annotation, while direct federation needs none.
- **The chain:** service account → pods → the ESO SecretStores that authenticate through it.
  - Klyx compares each pod's *current* injected configuration with the service account's *current* declared identity. It never infers when an annotation changed (managedFields time covers other changes too).
  - The authenticating subject is resolved per provider and version: the ESO controller's own identity, or an explicit `serviceAccountRef`.
  - KEDA authentication joins are deferred to S9, said so here.
- **Never read** token contents, general Secret values or env values. Cloud trust and effective permissions stay explicitly unverified.

**S6. Pods, correct.** Two changes:
- **S6a. Resources and resize:**
  - In-place resize (GA 1.35): desired vs allocated vs actual resources per container; `PodResizePending` (Deferred/Infeasible) and `PodResizeInProgress`.
  - **Accounting:** scheduled-resource accounting takes the scheduler's rule, the maximum of desired, allocated and actual during an incomplete resize, keeping pod-level overrides, init and sidecar maxima, and overhead.
    - It is tested on upward and downward pending resizes, older or missing status fields, and raw/projected equality.
    - Every consumer labels the result "scheduled".
  - **Pod `observedGeneration`** (GA 1.35) means the kubelet acknowledged the change, not that it completed. Equality never hides a remaining resize condition, and a lag never suppresses an independent fault (a CrashLoop stays a CrashLoop).
    - **Presentation:** the Syncing mark and qualified text of `staleness.tsx` (row dimming was retired in The Line 11a).
  - Older kubelets and absent fields read unknown, never zero and never stuck.
- **S6b. Ephemeral containers:**
  - Already there: debug through `kubectl debug --target` in a terminal, gated.
  - New: the pod inspector shows existing ephemeral containers and their status (a failed debug container is diagnostic evidence, not the application's failure), and the debug verb offers an image or profile choice under C4.

**S7. Gateway API, current:**
- **ListenerSet** keeps its own identity, its listeners' authorisation (`allowedListeners` on the Gateway, `allowedRoutes` per listener), listener and parent conditions, `controllerName` and generation. A ListenerSet's listeners join a Gateway only where it allows them, and are never flattened into an accepted path. `RouteForGateway` learns ListenerSet parents.
- **L4 routes:** TLSRoute (v1 since Gateway API 1.5), TCPRoute and UDPRoute (v1 in 1.6) generalise routes to a kind with no HTTP matches, on per-resource version candidates.
- **Hard cases:** cross-namespace grants, mixed served versions, and an installed controller that does not support a feature.
- **The experimental group** `gateway.networking.x-k8s.io` is *recognised*, not implemented API by API.
- **Metrics** stay protocol-qualified (HTTP rule metrics are HTTP only).
- **Unverified, to check during the slice:** Envoy Gateway's ListenerSet support in the lab's version.

**S8. Alerts and logs.** C5 first, then two reviews and builds:
- **The external-evidence contract, for both:**
  - **Identity:** each cluster's labels in the shared stack (cluster, and tenant) are configured per cluster, with the endpoint's identity and evidence timestamps. A match by namespace, workload or pod name is a correlation, not a UID-pinned join, and is labelled so; a same-name replacement can share it.
  - **Recovery:** a failed or stale poll never manufactures recovery. A disappearing label match is "no longer matched", not "resolved".
  - **Bounds:** time windows, response bytes and entries, concurrent polls, cancellation. A partial or truncated query never reads complete.
- **S8a. Alerts, from Alertmanager** (`/api/v2/alerts`, the owner's stack):
  - **States:** active, suppressed (silenced or inhibited) and unprocessed stay distinct.
  - **Deduplication:** HA replicas are deduplicated by fingerprint.
  - **Where they show:** active alerts appear in the object's inspector and as a Needs you source, counted *separately* from object faults, so one incident is not counted as several objects. Suppressed alerts appear only in the inspector, as suppressed.
  - **No OS notifications in this programme.** `healthnotify`'s episode identity is a Kubernetes UID, and an alert fingerprint is not one; a separate design would be needed.
  - **Read-only:** Klyx shows alerts, and never manages, re-emits or invents them.
- **S8b. Loki:**
  - history for a pod beyond its previous container, gone pods included, over a selectable time range, from the logs pane;
  - the LogQL is built by Klyx from the object, not typed by the user, and the selector used is shown. Without pod-UID metadata, Klyx says which weaker selector it used, and never claims the logs belong only to that pod.

**S9. KEDA and autoscaling ownership:**
- **The kinds:**
  - `ScaledObject`: joined to its HPA by status, reference and UID evidence, never by the `keda-hpa-` name prefix. Custom HPA names and adopted HPAs are supported;
  - `ScaledJob`: joined to its Jobs; it has no HPA;
  - `TriggerAuthentication` and `ClusterTriggerAuthentication`: configuration, not condition-bearing health objects. An authentication failure lights the *scaler* that uses it, and the authentication joins deferred from S5 land here.
- **KEDA's vocabulary:** Ready, Active and Fallback, with Paused read from the versioned pause annotations. Active=False can be healthy and idle; Fallback and Paused get native treatment.
- **Fixes:**
  - the owner-reference mapping for non-core kinds (`kindResourceRef`);
  - the scale verb's ownership warning (C4).

**S10. Node provisioning, by provider adapter:**
- **Adapters:** `EC2NodeClass` (today a raw registry entry), `AKSNodeClass` (`karpenter.azure.com`) and the EKS Auto Mode NodeClass. Any other provider is an honest "provider not supported", not an error.
- **The class link:** `nodeClassRef` is resolved by group, kind and name. Missing, unreadable and stale dependencies stay distinct, and a class failure inherited by its NodePools is not double-counted.
- **Management** follows C2, with the positive-evidence table for self-hosted vs managed Azure Karpenter and EKS Auto Mode.
- **The AKS networking note** distinguishes BYO Cilium from Azure CNI powered by Cilium. Microsoft supports Karpenter-specific issues on BYO CNI, but excludes third-party CNI networking troubleshooting.

**S11. GPUs and DRA.** Two changes:
- **S11a. Extended resources** (`amd.com/gpu`, `nvidia.com/gpu`): capacity, allocatable and requested per node, kept apart from utilization. DRA-backed extended resources are not double-counted.
- **S11b. DRA** (`resource.k8s.io/v1`):
  - **ResourceSlices** in all their access forms: a node name, `allNodes`, a node selector, or per-device selection. Pool generations are complete before a pool is read as whole.
  - **ResourceClaims**, template-generated ones included, with their reservations.
  - **Also:** DeviceClasses, and pod device health (`allocatedResourcesStatus`, GA 1.37).
  - **A Pending claim with no allocation** shows the scheduler's and the driver's own evidence. It is not proof of "unsatisfiable", and Klyx does not implement a second scheduler.
- **The live check** on the ROCm homelab only on the owner's separate authorisation (C6).

## 3. Not in this programme

- **Extensions (packs):** after this programme, in Codex's order.
- **Velero writes:** running a restore, or "backup now". Reading Restores is in scope (S2b).
- **Full BGP session diagnosis** (MetalLB/Cilium BGP peering): a later candidate after S4.
- **Out-of-cluster sources:** audit-log-based mutation attribution, EKS Pod Identity association reads (an AWS API), Fleet Manager placements.
- **Pack material:** Crossplane, Azure Service Operator, CloudNativePG, Strimzi, Trivy, Sealed Secrets, external-dns. Flux's own source composition is *not* pack material (S1c).

## 4. Per-slice definition of done

For every slice:
- its own note with C1's source contract per part, Codex-accepted before code and again after;
- the gate (tsc, eslint, vitest, vite build) and `go test` with `-race` where touched;
- parity and the beyond-parity cases of C1;
- a native smoke step (a `TITLES` entry, and a step that proves the slice's drawing or verdict);
- live captures on `kind-klyx` in night and day at 1440 and 1024;
- the slice's watch, model and projection budget;
- the note's "As built" section, and Prax notes for anything durable.

Pushing, PRs and releases are the owner's call.

## 5. The owner's answers in this revision

- **S8a reads Alertmanager.**
- **S4 is on-prem load balancing**, right after admission: provider-neutral, with linked MetalLB, Cilium LB IPAM and k3s ServiceLB evidence.

## 6. Revision 1: Codex's design review of 1487ca2

| # | Finding | Where answered |
|---|---|---|
| 1 | S3 promised mutation attribution the reads cannot prove; "blocks these objects" overstated rules | S3 split into S3a (availability, with evidence limits) and S3b (proven denials, labelled candidates; mutations unknown) |
| 2 | C2 conflated management with health; AKSNodeClass is not proof of NAP | C2 rewritten: two facts, positive evidence, faults never suppressed; it lands before S5 |
| 3 | Velero health had no authoritative source and no valid cron deadline | S2a: watched schedules and BSLs, a capped projected Backup watch, cron-and-time-zone opportunities with grace, named schedule states, phases kept |
| 4 | S4 inferred annotation history and over-claimed provider evidence | S5 (identity): current-vs-current comparison, non-exhaustive adapters, the authenticating subject resolved per provider, KEDA joins moved to S9 |
| 5 | The scheduled-resource fix ignored resize accounting; dimming is retired | S0 (projection) and S6a (the scheduler's max rule, tests, the Syncing mark) |
| 6 | Alerts needed an identity and coverage contract, and must not reuse UID notifications | S8's external-evidence contract; alerts counted separately; no OS notifications |
| 7 | "Flux, complete" omitted ExternalArtifact and ArtifactGenerator; FluxReport is not a count | S1c added; S1a states what FluxReport and operator availability do and do not prove; per-kind verbs |
| 8 | C1/C6 lacked a source contract, tests beyond parity, and a budget | C1's source contract, tests and budget; C6's limits and the owner-authorised homelab check |

Rulings adopted: C3's three-state gate, C4's verb matrix, C5 as a separate preparatory change, and the per-slice splits. Load balancing is S4 rather than beside Gateway API: the owner asked for it after admission, and it needs C3.

**Accepted at 8281c6e** (Codex, revision 1). Two details are carried into their slice notes:
- **S4:**
  - Controller selection is evidence-qualified: a requested class does not prove its controller is installed, and an unclassified Service need not belong to every detected controller.
  - Cover class and default settings, a disabled k3s ServiceLB, pool selectors, hostname allocation and the required IP families.
  - The shared Services projection keeps the new reader's fields.
  - Pending time has an explicit baseline when an old Service turns into a LoadBalancer or loses its allocation; object creation is not the start of the current pending episode.
- **S2a:**
  - Formalise the overdue opportunity, and its reset after a later success.
  - An in-progress run during the grace period, and incomplete retained history.
  - The exact classifier and its fake-clock cases.

