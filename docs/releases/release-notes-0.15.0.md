# Klyx v0.15.0

v0.15.0 is **the first-class coverage programme**: the tools you run beside Kubernetes are read in their own vocabulary, with honest evidence and named gaps. It covers:
- Flux in full;
- Velero;
- admission;
- on-prem load balancing;
- workload identity;
- pod resources;
- Gateway API currency;
- alerts and logs;
- KEDA;
- node provisioning;
- GPUs and DRA.

It also ships two follow-ups to The Line, a generic resource browser and an inspector that widens and expands, and a fix that makes the YAML editor usable again.

Every slice had its own design note and a Codex design review, then a staged build and Codex implementation reviews until ACCEPT. The roadmap is `docs/superpowers/specs/2026-10-16-klyx-coverage-roadmap.md`. Each slice's note is beside it, and its "As built" section records what landed and how it was verified. The work reached `main` directly (S0 to S1c), then as PR #50 and PR #51.

## Flux, complete

- **Flux Operator** (`FluxInstance`, `FluxReport`, `ResourceSet`, `ResourceSetInputProvider`).
  - An Operator view in the Flux lens, broken first.
  - FluxInstance's declared components, distribution and sync. FluxReport as the operator's as-of report, never a replacement for Klyx's own counts.
  - ResourceSets with their inputs (bounded and redacted, never sent to AI) and their generated inventory.
  - Flux is detected by the operator as well as by kustomize-controller.
- **Image automation** (`ImageRepository`, `ImagePolicy`, `ImageUpdateAutomation`).
  - An Images view: scan health, the latest image per policy, and the automation's last push joined to its source and push branch.
  - Verbs: Scan now, Run now, reconcile a policy, suspend and resume.
  - A push is never read as proof that a workload applied it.
- **Source composition and notifications.**
  - ExternalArtifacts and the source-watcher's ArtifactGenerators in the dependency graph and the bound-source panel.
  - Composition and Notifications views. Providers, Alerts and Receivers are shown as facts.
  - Verbs: a generator's by source-watcher's own annotations, a Receiver's reconcile, and suspend and resume for Receivers, Providers and Alerts.

## Velero (read-only)

- **A Velero lens.** Schedules with their run rail, storage locations, recent unscheduled backups, and Backups and Restores.
- **Faults:**
  - an unavailable storage location;
  - a missed schedule, from Velero's own cron with its time zone and DST, after a grace period;
  - recent failed and partially failed backups, kept distinct.
- **Schedule states are named:** new, paused, FailedValidation, `skipImmediately`, a backup in progress, and no known success. Velero's phases are kept as they are.
- **The fleet's "latest retained successful backup" per schedule.** It never says "restorable": Completed is not a restore test.

## Admission

- **Webhooks and admission policies.**
  - A fail-closed webhook whose backend is unavailable reads "matching requests may fail". That covers a missing Service, no ready endpoints, or an unmapped port.
  - Each evidence limit is said: URL-backed webhooks, unreadable EndpointSlices, ready endpoints that do not prove reachability.
  - ValidatingAdmissionPolicy and MutatingAdmissionPolicy with their bindings.
- **Proven attribution of a refused write.** Klyx reads its own writes' refusals from the server's own status, in five exact shapes: a webhook's denial, a webhook without dry-run support, a webhook that could not be called (read after its URL), and a ValidatingAdmissionPolicy's or another policy's denial.
  - The YAML panel's headline names the webhook or policy that refused the write, with the server's message. The webhooks and policies whose rules could match are listed below, labelled as candidates.
  - Toasts say "<verb> refused: <who>" and every step of a composite action: written, refused, or unverified when the answer was lost. The Activity log keeps the line and the candidates' names.
  - A Secret's or payload kind's refusal keeps structured facts only. Any server text is scrubbed of credentials and bounded.
  - A live-vs-Git difference is never attributed to a mutating webhook, and never discounted as "not drift".

## On-prem load balancing

- **A provider-neutral diagnostic** for a LoadBalancer Service with no address: its class and the controller that should serve it, the allocation status, its events, and how long it has been pending. Past a grace period it reaches Needs you.
- **Linked controller evidence:**
  - MetalLB's IPAddressPools and advertisements;
  - Cilium LB IPAM pools, with their conflict and exhaustion conditions;
  - k3s ServiceLB's `svclb` pods.

  An allocated address is not proof of working traffic.

## Workload identity

- **On demand, per provider:** EKS IRSA, EKS Pod Identity (in-cluster evidence only), Azure Workload Identity and GKE.
- **The chain:** service account, its pods, and the External Secrets stores that authenticate through it.
- **Each pod's current injected identity** against the service account's current one, never an inferred change time.
- **Never read:** token contents, Secret values or environment values. Cloud trust and effective permissions stay explicitly unverified.

## Pods, correct

- **In-place resize:** desired, allocated and actual resources per container, and the pending and in-progress resize states.
- **Scheduled-resource accounting** takes the scheduler's own rule (the maximum during an incomplete resize), with pod-level resources and overhead. The headroom guard uses it.
- **A pod's `observedGeneration`** reads as acknowledged, not completed.
- **Ephemeral containers** in the pod inspector. The debug verb takes an explicit image and profile, so a debug shell's privilege no longer depends on the local kubectl's default.

## Gateway API, current

- Per-resource versions, listener status and **ListenerSets**.
- **Every route kind:** HTTP, GRPC, TLS, TCP and UDP, with one attachment per parentRef.
- Controller-scoped status, ReferenceGrant and `allowedRoutes` evidence, and backends by port and transport.
- The experimental group is recognised.

## Alerts and logs

- **External endpoints** (Settings > External endpoints).
  - One endpoint shape for metrics, Alertmanager and Loki, with credentials in the OS keychain bound to their origin.
  - A plaintext metrics token in `fleet.yaml` keeps working and can be moved into the keychain.
  - Redirects to another origin are refused, and a capped response errors instead of truncating.
- **Alerts from Alertmanager** (`/api/v2/alerts`).
  - An Alerts lens, an Alerts block in each inspector, and a third count in Needs you, kept apart from object faults.
  - Active, suppressed and unprocessed stay distinct, and HA replicas are deduplicated.
  - A match by name is a correlation, said as such. Read-only, and no OS notifications for alerts.
- **Log history from Loki.**
  - History in the logs dock, beyond the previous container and for pods that are gone, over a chosen time range, with a pop-out.
  - Klyx builds the LogQL from the object and shows the selector it used. A weaker selector than the pod's UID is said as such.

## KEDA

- **ScaledObjects joined to their HPA by evidence,** never by the `keda-hpa-` name prefix; ScaledJobs joined to their Jobs.
- **KEDA's own vocabulary:** Ready, Active and Fallback, with Paused read from its annotations. Active=False can be healthy and idle.
- **Trigger authentications are configuration.** A failed one lights the scaler that uses it.
- **The scale verb** names what owns the replica count.

## Node provisioning

- **Karpenter NodeClasses by provider adapter:** AWS `EC2NodeClass`, Azure `AKSNodeClass` and the EKS Auto Mode NodeClass. Any other provider reads "not supported", never an error.
- **`nodeClassRef` is resolved** by group, kind and name. A class failure is inherited by its NodePools without being counted twice.
- **A NodeClasses view** in the Karpenter lens.

## GPUs and DRA

- **Extended resources** (`amd.com/gpu`, `nvidia.com/gpu`). Capacity, allocatable and requested per node, kept apart from utilization. DRA-backed resources are never counted twice.
- **Dynamic Resource Allocation** (`resource.k8s.io/v1`).
  - A Devices (DRA) lens: ResourceSlices in every access form, each pool read per node under the scheduler's own rules; ResourceClaims (template-generated included) with their reservations; DeviceClasses.
  - A DRA block on each node and a Claims block on each pod, with the kubelet's device health.
  - A pending claim shows the scheduler's and the driver's own evidence, never a second scheduler's verdict. Capped, held or stale reads are said as such, never as established.

## Safer evidence everywhere

- **Embedded payloads and URLs.** Kinds that embed payloads, and URLs that carry credentials, are sanitised on every path:
  - lists, details and summaries;
  - the Manifest;
  - Describe, which is withheld or scrubbed;
  - event messages;
  - the cache diff;
  - chat.

  An apply carries the object's UID.
- **The pod projection keeps pod-level resources and overhead,** so scheduled CPU and memory are no longer under-reported.

## From The Line

- **The generic resource browser** (slice 15).
  - Every kind lists the printer columns its CRD declares, as `kubectl get` does.
  - A generic inspector with a summary, the object's facts and its fields, and one condition-polarity registry.
  - The API browser with a definition inspector for each kind: versions, storage, conversion, printer columns and schema. It still works when CRDs cannot be listed.
- **The inspector widens and expands** (slice 16).
  - It resizes up to the room the list leaves, and the grip is visible at rest.
  - Expand gives it the whole workspace, with the list kept behind it.

## Fixes

- **The YAML editor applies again.** Its local check read any label or annotation key with a `/` as a parse error, and so did a finalizer or owner-reference item and a top-level list such as a Role's `rules:`. Dry-run and Apply stayed disabled for almost every real object. The editor now reads only metadata's own keys, and the server's dry run validates the rest.
- **Re-opening the resource list already shown** keeps its read.

## Platform

- **Go 1.27.3** is the new floor, for crypto/x509's GO-2026-6809. CI's govulncheck reports no vulnerabilities.
- **The native smoke** now walks 208 steps. Its watchdog allows 15 minutes and names the step a stalled runner was on.

## Known limitations / not yet verified

- **The coverage programme is verified on the kind lab:**
  - the native WKWebView smoke, 208/208 in night and day at 1440 and 1024;
  - each slice's parity gate, mutations and captures.

  S7 was also exercised on the AKS dev cluster. Nothing has yet run against your EKS or on-prem clusters.
- **DRA** was verified with the upstream example driver on kind, not on GPUs. The read-only check on the ROCm homelab waits on your authorisation.
- **External endpoint credentials** were not run against a real OS keychain. Their tests use an in-memory store, and the keychain code is the AI keys' existing path.
- **Admission attribution** does not cover drain, debug, exec, port-forward or Helm rollback, whose errors are kubectl's or helm's text.
- **The health load test's full evaluation** measured about 60 ms against its 50 ms budget on the largest fixture, before the coverage work as well as after. The budget is an open decision.
- **No Linux arm64 build,** as before.
- **Every earlier limitation still applies,** and so does the v1.0.0 gate in `docs/backlog.md` §0.
