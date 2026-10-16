# Klyx first-class coverage: the roadmap

Owner direction, 2026-10-16:

> "i have on prem clusters, eks clusters. i think it would be good to have key aks support as there are people want to use klyx with aks, i will use it with eks and onprem mainly. flux should be fully supported, flux operator + flux image automation. velero should be supported also. 1 to 9 should be done."

Items 1-9 come from the gap analysis of the same day, checked against the code at 1e35bf6 (Prax: "Klyx first-class coverage gaps"). This programme comes **before** the extensions work. The extensions note (`2026-10-16-klyx-extensions-research.md`) and Codex's order for it (inspector adapters, then opt-in fleet health, then trusted actions) stand after it.

This is the parent spec. Each slice gets its own note, design review, build, implementation review, native smoke and live captures, as The Line did.

## 0. What first-class means here

- **Klyx's own logic:**
  - joins across objects (an identity chain, a ScaledObject and its HPA, a webhook and its endpoints);
  - queries to a system outside the cluster (Alertmanager, Loki);
  - the tool's own verbs;
  - a drawing where the shape is the point.

  A CRD that only has a `Ready` condition is pack material, not part of this programme.
- **The tool's own vocabulary:** Velero's phases, KEDA's Active/Fallback/Paused, Flux's Ready/Stalled/Reconciling, image automation's latest image.
- **Capability-detected:** a tool that is not installed renders nothing. A managed controller (AKS NAP, EKS Auto Mode, the AKS KEDA add-on) is detected as *managed*, never "unverified" (§1 C2).
- **Health-bearing kinds join the shared engine** (§1 C1), so their faults reach Needs you, the mast, the fleet spine and notifications with the same semantics as built-in kinds. A failed or forbidden read renders as an error, never as quiet.
- **Platforms:** the owner's daily clusters are EKS and on-prem (and the k3s ROCm homelab), and AKS stays first-class for other users. Cloud-specific pieces (Azure Workload Identity, IRSA, EKS Pod Identity, AKSNodeClass, EKS Auto Mode) are each detected, and each degrades to nothing elsewhere.

## 1. Shared contracts (decided once, reused by every slice)

**C1. A health-bearing kind.** This is the pattern the Karpenter, ESO and cert-manager kinds follow; file references are from the code map of 2026-10-16.
- **Backend:**
  - a pure package (`Parse*`, `*Rule → health.NativeEval`);
  - a `crdKind` with per-kind served-version probing (`healthsrc/native.go`), and a `DomainSpec`/`PartSpec` (Counted, Deps, Evaluate, Ledger);
  - controller detection in `capability`, and the controller task;
  - the rows converter and its DTO mirror test;
  - the AI field allowlist, the chat GVR-to-task map, and the notification kind label.
- **Frontend:**
  - `nav/clusterAttention.ts` (TASKS, KIND_LABELS), `fleet/queue.ts`, `nav/reveal.ts`, `console/openHit.ts`;
  - the registry group and `ClusterDetail` section;
  - the smoke `TITLES` entry.
- **Parity gate:**
  - the CRD on `kind-klyx` and in `klyx-parity.sh`'s list;
  - a fixture function in `parity/fixture.sh` (parity labels, full conditions with `observedGeneration`, nothing time-based) called from the baseline;
  - a controller stand-in;
  - the oracle's parts and controller tasks in `parity_lab_test.go`, and `ROW_PARTS`/`CASES` in `parityGate.test.tsx`.

  A kind is not done until the parity gate covers it.

**C2. Managed controllers.** Today detection probes a Deployment in a conventional namespace, so a managed controller reads "unverified" and lights the task as ControllerUnknown. A capability can now be *managed*, proven by evidence the platform leaves in the cluster:
- AKS NAP: its `AKSNodeClass` CRD and the system pools;
- EKS Auto Mode: the `eks.amazonaws.com` NodeClass CRD;
- the AKS KEDA add-on: `keda-operator` in kube-system with its add-on labels.

A managed controller's health is not Klyx's to see. The task says "managed by the platform", never lit and never quiet.

**C3. Shared observer kinds have one gate per consumer.** Kinds are shared by name. `services` and `endpointslices` are gated on the Gateway API probe today, so a second consumer (webhook health) would see them as absent on clusters without Gateway API. A shared kind's gate becomes the union of its consumers' gates, and each consumer reads a kind only when its own gate is satisfied.

**C4. Verbs.** Only the tool's own day-2 verbs are allowed: Flux reconcile and suspend for the new Flux kinds, and KEDA pause through its annotation. Each goes through ConfirmDialog and ProtectGuard on protected clusters, as every verb does. No restore, and no authored objects.
- The scale verb learns to warn when an HPA or a ScaledObject owns the replica count.
- Any new write path is noted as a crossing of the non-goals, as the editable surface was.

**C5. Credentials for external endpoints.** The Alertmanager and Loki endpoints follow the metrics model: an endpoint, a `serviceRef` through the apiserver proxy, or discovery. They add:
- tenant headers (`X-Scope-OrgID`) and basic auth;
- **tokens and passwords in the OS keychain**, as AI keys are, with plaintext `fleet.yaml` tokens migrated (the metrics token included).

Log text bound for AI goes through `internal/scrub`.

**C6. The lab.** Fixtures go on `kind-klyx` (v1.37) as CRDs plus objects whose status is written through the status subresource, with pause stand-ins for controllers, as the parity gate does.
- Real components only where behaviour is the point: in-place resize and ephemeral containers (kubelet), Alertmanager and Loki (small single binaries), and DRA objects (`resource.k8s.io/v1` is served).
- GPUs are fixtures: node status capacity for `amd.com/gpu`.
- Every lab write stays on `kind-klyx` or `kind-klyx-parity`. There are never real clusters.

## 2. The slices, in order

Order: the owner's two named priorities first, then the items that see a fault Klyx cannot see today, then correctness, then the larger external integrations.

**S1. Flux, complete** (owner priority):
- **Flux Operator:** `FluxInstance`, `FluxReport`, `ResourceSet`, `ResourceSetInputProvider` (`fluxcd.controlplane.io`).
  - Detect Flux by the operator as well as by kustomize-controller (`capability/detector.go`).
  - FluxInstance health: version, components, sync, and the report's reconciler stats.
  - ResourceSets show their inputs and generated inventory.
- **Image automation:** `ImageRepository`, `ImagePolicy`, `ImageUpdateAutomation` (`image.toolkit.fluxcd.io`).
  - Scan health and the latest image per policy.
  - The automation's last push and the commit it wrote, linked to the Git history The Line already reads.
- **Placement:** all of it inside the Flux lens (design principle 2), with these as new parts beside reconciliations and sources.
- **Verbs:** reconcile and suspend gain the new kinds (`actions.go`, `gitopsactions.go`, `gitopssource.go`; the test that asserts ImagePolicy is unsupported changes).
- **Also:** the notification kinds (Providers, Alerts, Receivers) get facts beyond the raw registry entry.

**S2. Velero:**
- **What it shows:** Schedules, BackupStorageLocations and VolumeSnapshotLocations, and Backups and Restores listed on demand (they churn).
- **The fleet view:** a fleet-wide "last successful backup per schedule" horizon on the promotion pattern (`useFleetBatch`).
  - The newest `phase=Completed` Backup labelled `velero.io/schedule-name`, never `status.lastBackup`, which is not success.
  - PartiallyFailed is named as such.
- **Health tasks:**
  - a BackupStorageLocation that is Unavailable;
  - a schedule whose last success is older than its expected interval times two (the ESO staleness rule);
  - failed backups.
- **Out of scope:** restore and "backup now". Both are write paths with a large blast radius, kept for a later, explicit decision.

**S3. Admission health and attribution** (item 3):
- **Watch:** Validating and Mutating webhook configurations, and ValidatingAdmissionPolicy and MutatingAdmissionPolicy (GA 1.36) with their bindings.
- **Fault:** a fail-closed webhook whose backend Service has no ready endpoints, or names a missing Service, reaches Needs you. The webhook says what it blocks (its rules: resources and operations).
- **Attribution:** a write error or a live-vs-Git difference is attributed to the policy or webhook that rejected or mutated it, so the guarded editor and the drift views never blame Git for a mutation. The write-error parser already recognises webhook denials (`InstanceDetail.tsx`).
- **Needs C3** (the services/endpointslices gate split).

**S4. Workload identity** (item 2, cloud-neutral):
- **Recognised:** Azure Workload Identity (service-account annotations, the pod label, the injected env and projected token), EKS IRSA (`eks.amazonaws.com/role-arn`), EKS Pod Identity (in-cluster evidence only: the injected `AWS_CONTAINER_CREDENTIALS_FULL_URI`/token and the agent DaemonSet; the association itself lives in AWS and is said to), and GKE (`iam.gke.io/gcp-service-account`).
- **The chain:** service account → pods (flagging pods started before the latest annotation change, and pods missing the required label) → the ESO SecretStores and KEDA TriggerAuthentications that authenticate through it.
- **ESO:** `ParseStore` reads the provider's auth (`serviceAccountRef`, `authType: WorkloadIdentity`, IRSA, Vault Kubernetes auth).
- **Drawn** as an on-demand chain on `Rail`, like the issuance chain.

**S5. Pods, correct** (items 6 and 7):
- **In-place resize** (GA 1.35): desired vs actual resources per container, and `PodResizePending` (Deferred/Infeasible) and `PodResizeInProgress`, shown in the verdict note and the inspector.
- **Pod `observedGeneration`** (GA 1.35): the ratified "dim = not yet realized" language now reaches pods.
- **Fix:** `ProjectPod` drops pod-level `spec.resources` and `spec.overhead`, so observer pod rows under-report scheduled CPU and memory. A parity case covers it.
- **Ephemeral containers:**
  - already there: debug through `kubectl debug --target` in a terminal, gated;
  - new: the pod inspector shows existing ephemeral containers and their status, and the debug verb offers an image or profile choice.
- **Native sidecars** are already handled (`health/pod.go`) and are not reopened.

**S6. Gateway API, current** (item 4):
- **ListenerSet** (v1.5): parsed, and merged into the Gateway's listeners when the Gateway allows it (`allowedListeners`). `RouteForGateway` learns ListenerSet parents.
- **TLSRoute v1, TCPRoute and UDPRoute v1** (v1.6): routes are generalised to a kind with no HTTP matches, on the per-resource version candidates the policy reads already use.
- **The `gateway.networking.x-k8s.io` experimental group** is recognised.
- **The data path** draws the new route kinds. Rule metrics stay HTTP-only and say so.
- **Unverified, to check during the slice:** Envoy Gateway's ListenerSet support in the version the lab runs.

**S7. Alerts and logs from the observability stack** (item 1):
- **S7a. Alerts:**
  - firing alerts for an object (by namespace, workload and pod labels) in its inspector, and as a Needs you source;
  - read from Alertmanager `/api/v2/alerts`, or the Prometheus or Mimir `/api/v1/alerts`, with C5 credentials and a generic GET querier (`metrics.Querier` has instant and range only);
  - read-only: Klyx shows alerts and never manages them.
- **S7b. Loki:** history for a pod beyond its previous container (gone pods, older windows) in the logs pane, with LogQL built by Klyx from the object, not typed by the user; capped and scrubbed.
- **Open for the owner:** which alerting each of their stacks uses (Alertmanager, Mimir ruler, Grafana-managed).

**S8. KEDA and autoscaling ownership** (item 8):
- **What it shows:** `ScaledObject`, `ScaledJob`, `TriggerAuthentication` and `ClusterTriggerAuthentication`, in KEDA's vocabulary (Ready, Active, Fallback, Paused), joined to the HPA KEDA generates.
- **Fixes:**
  - the owner-reference mapping for non-core kinds (`kindResourceRef` drops `keda-hpa-*` → ScaledObject);
  - the scale verb warns when an HPA or a ScaledObject owns the replicas.
- **Pause** is KEDA's annotation (C4).

**S9. Node provisioning, every cloud** (item 5):
- **Parse** `EC2NodeClass` (today a raw registry entry), `AKSNodeClass` (`karpenter.azure.com`) and the EKS Auto Mode NodeClass.
- **Link:** NodePool → node class via `nodeClassRef`, with the class's Ready as the NodePool's dependency.
- **Managed controllers** follow C2 (AKS NAP, EKS Auto Mode).
- **An AKS note:** NAP with BYO CNI (Cilium) is outside Microsoft's networking support, and the lens says so.

**S10. GPUs and DRA** (item 9):
- **Per node:** extended resources (`amd.com/gpu`, `nvidia.com/gpu`) as capacity, allocatable and requested. The projection already keeps them, but requested needs the pods dependency or an on-demand sum.
- **DRA** (`resource.k8s.io/v1`):
  - ResourceSlices per node (on demand);
  - ResourceClaims on the pod inspector, and as their own kind;
  - DeviceClasses;
  - pod device health (`allocatedResourcesStatus`, GA 1.37);
  - a Pending pod whose claim cannot be satisfied says so.
- **Lab:** the ROCm homelab is the live check; `kind-klyx` carries fixtures.

## 3. Not in this programme

- **Extensions (packs):** after this programme, in Codex's order.
- **On-prem LoadBalancer health** (MetalLB, Cilium LB IPAM, BGP): a Service stuck in `<pending>` is the classic on-prem fault. A candidate for the owner, not yet decided.
- **Velero restore** and "backup now"; Fleet Manager placements; EKS Pod Identity associations (an AWS API).
- **Crossplane, Azure Service Operator, CloudNativePG, Strimzi, Trivy, Sealed Secrets, external-dns:** pack material.

## 4. Per-slice definition of done

For every slice:
- its own note, Codex-accepted before code and again after;
- the gate (tsc, eslint, vitest, vite build) and `go test` with `-race` where touched;
- parity cases for every new health-bearing kind (C1);
- a native smoke step (the slice's destination has a `TITLES` entry and a step that proves its drawing or verdict);
- live captures on `kind-klyx` in night and day at 1440 and 1024;
- the note's "As built" section, and Prax notes for anything durable.

Pushing, PRs and releases are the owner's call.
