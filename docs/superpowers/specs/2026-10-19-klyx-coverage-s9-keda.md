# Coverage S9: KEDA and autoscaling ownership

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S9. It takes the KEDA authentication joins S5 deferred (`2026-10-18-klyx-coverage-s5-workload-identity.md`), and follows C3's shared-kind gate (S3a §2) for a new shared kind, HorizontalPodAutoscalers. Revision 1 answers Codex's design review of 5b165d8 (§14); revision 2 its re-review of 0cca474 (§15); revision 3 its re-review of 76f1fef (§16). **Design accepted by Codex at 279a50d** (handoff f7b5315d).

The roadmap's S9:
- **The kinds:**
  - `ScaledObject`, joined to its HPA by status, reference and UID evidence, never by the `keda-hpa-` prefix; custom HPA names and adopted HPAs supported;
  - `ScaledJob`, joined to its Jobs; it has no HPA;
  - `TriggerAuthentication` and `ClusterTriggerAuthentication`: configuration, not health objects. An authentication failure lights the *scaler* that uses it.
- **KEDA's vocabulary:** Ready, Active and Fallback, with Paused read from the versioned pause annotations. Active=False can be healthy and idle.
- **Fixes:** the owner-reference mapping for non-core kinds (`kindResourceRef`), and the scale verb's ownership warning (C4).

The facts below come from KEDA v2.21.0 (2026-09-23, the latest release) and kedacore/charts v2.21.0, read in source at the tag, with older tags v2.10.0 to v2.20.2 compared. The source facts, each cited to a line at its tag, are in the slice's working file `s9-keda-facts.md`. A real KEDA 2.21.0 runs in the S9 lab on `kind-klyx` (Kubernetes 1.37.0), whose findings 1-14 are in `lab-s9/FINDINGS.md`.

## 1. The API, read from the source and the lab

### 1.1 Kinds and versions

- **Served versions:** `keda.sh/v1alpha1` only, no deprecation:
  - `scaledobjects` (so) and `scaledjobs` (sj), namespaced;
  - `triggerauthentications` (ta), namespaced;
  - `clustertriggerauthentications` (cta), cluster-scoped.
- **Out of scope:** `eventing.keda.sh` (CloudEventSource, ClusterCloudEventSource) (§12).
- **No staleness signal.** KEDA conditions carry type, status, reason and message only. There is no `lastTransitionTime` and no `observedGeneration` anywhere in the status. The ratified "Syncing" rule (generation > observedGeneration) has no input for KEDA objects, and Klyx never fabricates one (C1).

### 1.2 What each condition proves

| Condition | Values (2.21) | What it proves |
|---|---|---|
| Ready | True `ScaledObjectReady`, rewritten by the scale loop on **every polling tick**; False `ScaledObjectCheckFailed` (spec, target, scaler build), `TriggerError` (no trigger active, a scaler errored, no fallback), `ErrorGettingCurrentReplicas`, `ErrorScalingTarget`; Unknown `PartialTriggerError` (some triggers active, some errored) | False and `PartialTriggerError` prove a fault. True is weak: it stays True while a fallback absorbs errors, and reads "ScaledObject is paused" during a full pause |
| Active | True `ScalerActive`; False `ScalerNotActive` or `ScalerCooldown`; Unknown `UnknownState` after a failed check | activation (0 or idle to min), not HPA scaling. False is the intended idle state |
| Fallback | True `FallbackExists`; False `NoFallbackFound` (forced False without a valid fallback spec) | True: at least one trigger failed more than `failureThreshold` times, and KEDA feeds the HPA a replacement metric per its fallback behaviour. That does not prove which replica count results (§3.2) |
| Paused | True `ScaledObjectPaused` for a full pause **and** for a directional one; False `ScaledObjectUnpaused`; initialised False since 2.18, Unknown before | which pause only the annotations say (§1.3) |
| HPAActive | since 2.20.2, appended lazily: True `HPAActive`; True `ScalingDisabled` (target at 0); False with the HPA's own reason passed through (`FailedGetExternalMetric`, `AmbiguousSelector`) or `HPAMetricsUnavailable` | the HPA's own ScalingActive, copied; it never changes Ready |

The lab proves the case S9 exists for (finding 5). A ScaledObject whose only trigger fails, with a fallback, reads Ready=True, Active=True and Fallback=True, with `status.health` `{numberOfFailures: 5, status: Failing}`. Its HPA reads ScalingActive=True, so **the HPA alone hides the failing trigger.**

### 1.3 Pause

| Annotation | Since | Effect |
|---|---|---|
| `autoscaling.keda.sh/paused-replicas: "<n>"` | 2.7 | KEDA deletes the HPA and scales the target to n. Since 2.18 the annotation's presence pauses, whatever `paused` says. Before 2.18 the pause took effect once `status.pausedReplicaCount` was set (non-nil, zero included) |
| `autoscaling.keda.sh/paused` (ScaledObject) | 2.12 | KEDA deletes the HPA and freezes the target at its current replicas. Since 2.13 it is parsed as a bool, and an unparseable value counts as true. Before 2.13 its presence paused even with "false" (fixed in 2.13). It has no effect before 2.12 |
| `autoscaling.keda.sh/paused` (ScaledJob) | 2.11 | KEDA stops creating Jobs; Jobs already running continue |
| `autoscaling.keda.sh/paused-scale-in` / `paused-scale-out` | 2.18 | the HPA is kept, and one scaling direction is disabled. Paused=True, with the same reason and message as a full pause (finding 12) |

**The Paused condition:**
- exists since 2.11;
- is initialised Unknown before 2.18 and False since;
- is written for a full pause and a directional one alike.

**Paused=True does not prove a completed pause.**
- **It can be set before the pause has finished.** 2.21's `handlePause` sets Paused=True *before* it stops the loop, deletes the HPA, scales the target and records `pausedReplicaCount`. A failure after that leaves Paused=True, no HPA, and Ready=False `ScaledObjectCheckFailed` ("failed to scale target to paused replica count: …").
- **Ready's message is not proof either.** Between 2.11 and 2.20, the reconcile after a pause returns the ordinary Ready message. While the target is still moving to its count, KEDA returns Ready=False "ScaledObject paused replicas are being scaled".

**What proves a completed full pause** (§3.2):

| Version | Evidence |
|---|---|
| 2.10 | `paused-replicas`, with `status.pausedReplicaCount` set (non-nil, zero included); no condition exists |
| 2.11 | `paused-replicas`, with `status.pausedReplicaCount` set, Paused=True, and no controlled HPA. `paused` has no effect |
| 2.12 and later | a full-pause annotation in effect for the version, Paused=True, Ready not False, and no controlled HPA. For `paused-replicas` also `status.pausedReplicaCount` equal to the annotation's value |
| every version | "held at n" also needs the target's actual replicas to equal n, from the workloads model for Deployments and StatefulSets. Otherwise the count is "not established" |

**Two counts are shown separately:** the count applied (`status.pausedReplicaCount`) and the count requested (the annotation), so a change from 3 to 5 reads "paused at 3; 5 requested". A malformed `paused-replicas` makes KEDA fail its reconcile, and so reads Ready=False.

### 1.4 The HPA join

- **Name.** `keda-hpa-<so>`, or `advanced.horizontalPodAutoscalerConfig.name`.
- **Ownership.** The HPA carries one ownerReference: `keda.sh/v1alpha1` ScaledObject, `controller: true`, `blockOwnerDeletion: true` (finding 2).
- **Labels.** `app.kubernetes.io/managed-by=keda-operator` and `part-of=<so>`, plus the ScaledObject's labels, which normally include `scaledobject.keda.sh/name`. `scaledobject.keda.sh/hpa-excluded-labels` can remove them, so labels are never identity.
- **`status.hpaName`.** Set when KEDA creates the HPA; set on adoption only since 2.18. A full pause deletes the HPA but leaves `hpaName` (no writer clears it; read from source, not exercised in the lab).
- **Adoption** (since 2.11). The annotation `scaledobject.keda.sh/transfer-hpa-ownership: "true"` plus a `horizontalPodAutoscalerConfig.name` equal to the existing HPA. The adopted HPA gets the same ownerReference and labels (finding 2). Whether earlier owner references are removed is unverified upstream; Klyx reads the controller reference only.
- **The webhooks fail open** (`failurePolicy: Ignore`, finding 9).
  - With the webhook up, a ScaledObject whose target a plain HPA already scales is refused (finding 13).
  - With it down, or in the reverse order, nothing stops two autoscalers on one target.
  - **The HPA controller detects that itself:** both HPAs read ScalingActive=False `AmbiguousSelector`, "pods by selector … are controlled by more than one HPA (e.g. [ns/a ns/b])", whether they are two plain HPAs or KEDA's plus a plain one (finding 14).

### 1.5 Authentication

**Where failures show.**
- TriggerAuthentication and ClusterTriggerAuthentication carry no conditions.
- Their `status.scaledobjects`/`scaledjobs` are comma-joined names without namespaces (ambiguous for the cluster-scoped kind).
- A failure never shows on the authentication object:
  - **A missing TriggerAuthentication and a TriggerAuthentication naming a missing Secret read the same** (findings 5, 11): Ready=False `ScaledObjectCheckFailed`, with the scaler's own parse error ("bearer token=<empty> is required"). KEDA's resolver builds the scaler with empty credentials, and never names the authentication object.
  - A cloud identity failure is only a `KEDAScalerFailed` Warning event ("STS: AssumeRole … failed to refresh cached credentials"), after which Ready=False `TriggerError` (finding 5).

**Sources.**
- `secretTargetRef`, `env`, `configMapTargetRef`, `hashiCorpVault`, `azureKeyVault`, `gcpSecretManager`, `awsSecretManager`, `podIdentity`, `boundServiceAccountToken`, `oauth2`, `azureServicePrincipal`.
- Every credential field is a Secret reference (`valueFrom.secretKeyRef`), except **`hashiCorpVault.credential.token`, a literal token**.

**Who authenticates for `podIdentity`.** It is always the keda-operator pod:

| Provider | `identityOwner: keda` (default) | `identityOwner: workload` |
|---|---|---|
| `aws` | the operator's own AWS identity (its IRSA or EKS Pod Identity); with `roleArn`, the operator assumes it | KEDA reads `eks.amazonaws.com/role-arn` from the **target workload's ServiceAccount** (pod template `serviceAccountName`, else `default`), where it is **required**; the operator then assumes that role. A workload using an EKS Pod Identity association without the annotation fails. `roleArn` plus workload is rejected by the webhook |
| `azure-workload` | the operator's federated identity; `identityId` / `identityTenantId` override the client and tenant, and must federate with the operator's ServiceAccount | ignored |
| `gcp` | the operator's GKE Workload Identity / ADC | ignored |
| `aws-eks` | deprecated | - |

`azure` and `aws-kiam` were removed in 2.15.

**Where secrets are read.**
- ClusterTriggerAuthentication secrets come from `KEDA_CLUSTER_OBJECT_NAMESPACE`, otherwise the operator's own namespace (the chart sets neither).
- With `KEDA_RESTRICT_SECRET_ACCESS=true`, every `secretTargetRef` is read from the KEDA namespace.

### 1.6 ScaledJob and its Jobs

- **Conditions:**
  - Ready: True `ScaledJobReady`; False `ScaledJobCheckFailed`, `TriggerError` or `KEDAJobCreateFailed`; Unknown `PartialTriggerError`.
  - Active: True or False.
  - Paused: `ScaledJobPaused`.
  - **Fallback is never written for a ScaledJob** and stays Unknown. Its `status.Paused` string field is never written either.
- **How each Job links back:**
  - a controller ownerReference to the ScaledJob;
  - `generateName: <sj>-`;
  - the label `scaledjob.keda.sh/name=<sj>` (KEDA itself counts Jobs by this label);
  - the annotation `scaledjob.keda.sh/generation` (finding 7).

### 1.7 Installation

**The release manifest and the chart name things differently.**
- The release manifest (the lab) names the Deployments `keda-operator`, `keda-metrics-apiserver` and `keda-admission`.
- The chart names them `keda-operator`, `keda-operator-metrics-apiserver` and `keda-admission-webhooks`.
- Both label the operator `app=keda-operator`.
- The namespace is the release's (documented as `keda`). Managed add-ons (AKS) may differ: that is unverified, so nothing is hard-coded.

**The metrics adapter.**
- KEDA registers the APIService `v1beta1.external.metrics.k8s.io`, labelled `app.kubernetes.io/part-of=keda-operator`, which points at its metrics server.
- Only one provider can hold that APIService. If another adapter holds it, every KEDA HPA fails to get its metric.

**Other installation facts.**
- The webhook Deployment is optional, and fail-open.
- A ScaledObject with no conditions has never been reconciled: the operator is down, or the object is outside the operator's `WATCH_NAMESPACE` or label-selector shard.
- The release manifest's webhook warns, without refusing, on an irrelevant `pollingInterval` (finding 9).

### 1.8 Reading mixed versions

| Version | What reads differently |
|---|---|
| before 2.20.2 | no `HPAActive`: the HPA's health is read from the joined HPA (§3), and a missing HPAActive is "not reported", never healthy |
| 2.20.0 and 2.20.1 | Ready=False `HPAMetricsUnavailable` or `ScalingDegraded` means HPA trouble, not an invalid ScaledObject: read as the HPA fault (§3's HPA result) |
| before 2.18 | Paused initialised Unknown, read as not paused; `status.hpaName` absent after adoption (the join uses the controller reference anyway); `paused-replicas` paused only once `status.pausedReplicaCount` was set |
| before 2.16 | `status.health` may list metrics without a fallback; `triggersTypes`/`authenticationsTypes` absent (derived from spec) |
| before 2.19 | no `triggersActivity` |
| before 2.13 | `paused: "false"` still paused |
| before 2.12 | `paused` on a ScaledObject has no effect |
| before 2.11 | no Paused condition, and no ScaledJob pause |

**Supported reading range: KEDA 2.10 and later.** That is the range compared in source. Ready, Active and the HPA join read on any version, because they need no version-specific field. Pause and HPA-health readings below 2.10 read "unsupported KEDA version".

**The version is the handling operator's running version** (§6.1): the image tag of its running pods, from the pods model. The Deployment's label is not used, because a rollout's template need not be what runs.
- **One operator in the cluster, or several on one version:** that version applies to every ScaledObject in the observer's rules.
- **Several operators on different versions, or an unreadable tag:** the observer reads "version unknown". Each rule then takes its conservative reading:
  - no HPAActive means read the HPA;
  - Paused=Unknown means not paused;
  - a pause annotation whose effect depends on the version reads "pause requested; whether this KEDA version applies it is unverified" (unknown, never paused).

## 2. The source contract (C1) and C3

| Part | Acquisition | Counted entity, identity | Dependencies and gaps | Clock | Notifications |
|---|---|---|---|---|---|
| `keda/scaledobjects` | observer watch, projected (§10), `keda.sh/v1alpha1`, where served | one ScaledObject: its UID | the HPAs (C3, below); TriggerAuthentications and ClusterTriggerAuthentications, for reference existence. An unread or failed dependency leaves its evidence "unverified", never a verdict from a gap | the no-conditions grace from creation; the pausing, resuming and awaiting-HPA episodes from §3.1's tracker; each re-evaluated through `Next` | yes |
| `keda/scaledjobs` | observer watch, projected | one ScaledJob: its UID | the authentications, as above. Jobs are read on demand in the inspector only (§6) | as for ScaledObjects (§5) | yes |
| `autoscaling/hpas` | the shared HPA kind (C3), consumer `hpas`, `autoscaling/v2` | one HPA **not** controlled by a ScaledObject: its UID. An HPA whose controller reference is a `keda.sh` ScaledObject is that ScaledObject's dependency, folded into its verdict, never counted twice (§4) | none | §4's grace | yes |
| `keda/authentications` | observer watches: `triggerauthentications`, `clustertriggerauthentications` | **not counted**: inventory rows (role only), never Needs you | the ScaledObjects and ScaledJobs that reference them | none | no |

**C3: HPAs become a shared observer kind.**
- **Two consumers:**
  - `hpas`, the standalone HPA part, whose demand is the core `autoscaling/v2` API (always yes on a supported cluster);
  - `keda`, whose demand is `scaledobjects` being served.
- **One projection** for both (§10).
- **No borrowed presence:** the `keda` consumer never reads "KEDA installed" from the HPA kind being open, and the HPA part never depends on KEDA.

**The KEDA controller task** (C2, with `ControllerTasks` and `ControllerIssues`):
- **The operators.**
  - Deployments labelled `app=keda-operator` (or `app.kubernetes.io/name=keda-operator`), in whatever namespaces hold them. Each is a candidate handling operator (§6.1).
  - A readable, not-ready one lights the task; an unreadable one is unknown.
- **The metrics adapter.** The APIService `v1beta1.external.metrics.k8s.io`, read at the capability probe (its re-probe interval), never watched.
  - **Ownership needs positive proof.** Klyx traces `spec.service` to the Service, its EndpointSlices, and their endpoints' target pods. It reads them through the shared Services and EndpointSlices kinds where they are open, and through bounded reads (at most 16 pods) where they are not. Each endpoint's `targetRef` UID must match the pod read.
    - **KEDA's:** every ready backing pod's own running container image (from the pods model, never a Deployment template mid-rollout) is from the `kedacore/keda-metrics-apiserver` repository (ghcr.io or docker.io).
    - **Another adapter:** the backing pods are complete and proven, and none runs that image.
    - **Ownership unverified:** anything short of that, such as an incomplete, mixed or unreadable chain. It is never read as KEDA's or as foreign. Sharing the operator's namespace proves nothing.
  - **The verdicts:**
    - absent, `Available=False`, or proven foreign: a fault. The consequence is qualified: "external metrics are not served by KEDA: its external triggers (every type except cpu and memory) cannot scale". ScaledObjects with only cpu and memory triggers do not depend on it.
    - ownership unverified: a stated limit, not a fault.
  - **Reads stay bounded,** and failures read "unverified".
- **The admission webhooks.** A not-ready webhook Deployment, where one exists, is degraded: "admission checks are off: two autoscalers on one target are not refused".
- **Management** (C2). No managed-install evidence is verified (§1.7), so management reads "unknown". Faults are never suppressed.
- **No operator Deployment visible,** while ScaledObjects carry conditions: "controller health not directly observed", neither healthy nor failed.

**No synthetic evidence.**
- No observedGeneration or condition time is fabricated for KEDA objects. `NativeEval.Cond` stays nil for them, and age comes from Klyx's own observation (The Line §8.1).
- HPA conditions carry `lastTransitionTime`, and are cited as qualified evidence (`CondAt`).
- Phases and reasons are KEDA's own, verbatim.

## 3. The ScaledObject rule (first match is the headline; the inspector lists every finding)

The verdict kinds are the shared engine's (`health.Kind`). Each rule below gives the kind it produces and the label Klyx shows.

**The joined HPA** is the HPA whose controller ownerReference UID is this ScaledObject's UID: never a name, and never the prefix.

**Two properties of the HPA evidence are kept apart:**
- **current:** the HPA kind's watch is live, so the retained object is the latest the API delivered;
- **complete:** the read is uncut and not failed, so absence can be proven.

A retained HPA under a cap is current. A retained HPA from before a watch gap is stale: dated evidence, qualified "as of <time>". It can never establish recovery.

**HPAActive is KEDA's cached copy.** KEDA keeps the last value when it cannot read the HPA, and the copy carries no time. The lab saw HPAActive=False while the HPA itself already read ScalingActive=True (t1, `failing`). The 2.20.0 and 2.20.1 Ready reasons `HPAMetricsUnavailable` and `ScalingDegraded` are read as HPAActive=False.

**The HPA result** (one finding; §3.2 combines it with the others):

| Joined HPA | HPAActive | Result |
|---|---|---|
| retained, current, failed (AbleToScale=False, or ScalingActive=False with any reason but `ScalingDisabled`) | any | **fault**: "the HPA cannot <change scale \| compute replicas>: <reason>: <message>". `AmbiguousSelector` also names the other autoscalers on the same target, each with its owner |
| retained, current, healthy | False | **ok** for the HPA, plus the finding "KEDA last reported HPAActive=False (<reason>); the HPA itself is healthy now". The current direct read is fresh, and the copy is not |
| retained, current, healthy | True or absent | **ok** for the HPA |
| retained, stale, failed | any | **fault**, qualified "as of <time>" |
| retained, stale, healthy | False | **unknown**, "HPA health not established: KEDA reports HPAActive=False (<reason>); Klyx's last HPA read (as of <time>) was healthy". Neither piece can be ordered in time |
| retained, stale, healthy | True or absent | **unknown**, "HPA health not current: last read healthy as of <time>" |
| none retained; absence proven (current and complete) | any | **progressing** "awaiting HPA" while the awaiting-HPA episode (§3.1) is under 2 minutes; then **fault**: "no HPA controlled by this ScaledObject (KEDA reported ready)". A standalone HPA may still scale the target, so this never says "no autoscaler". It applies only when the object is not fully paused and Ready=True |
| none retained; absence not proven (capped, failed or stale) | False | **fault**, qualified "as KEDA last saw it" |
| none retained; absence not proven | True or absent | **unknown**, "HPA join not established", with the coverage limit. A cached True proves nothing |

### 3.1 The episode tracker

Three of the rules depend on how long a state has lasted, and the object's creation cannot date a state that began later: a pause annotation on a months-old ScaledObject has only just been requested. So the part keeps a bounded tracker, per object UID, for three episodes:
- **pausing:** a full-pause annotation is present, without §1.3's completion evidence;
- **resuming:** Paused=True with no pause annotation;
- **awaiting HPA:** not fully paused, Ready=True, and no controlled HPA in a complete read.

**How the tracker behaves:**
- **The start** is the first observation in which the episode's state holds. **An episode first seen on the kind's initial list, or on the first read after a gap,** has a lower-bound start, labelled "at least since <time>". The object's creation time is never used.
- **A gap** (a relist after a disconnect or a failed watch) restarts every open episode with a lower bound at the first read after it. The grace is delayed, never fired early.
- **A new UID** (an object deleted and created again under its name) starts its own episodes. **An episode ends** when its state no longer holds, and the entry is dropped.
- **Next** is the episode's start plus 2 minutes, so a clock-only crossing is re-evaluated without a new event.
- **Bound:** one entry per UID in an open episode, at most the kind's admitted objects. Entries are dropped on deletion.

### 3.2 The findings and the verdict

Each item below is an independent finding. **The verdict is the worst finding** in the engine's triage order (fault, degraded, unknown, progressing, paused, ok). Ties take the order listed. Every other finding stays in the inspector, and pause intent stays as context on the label. "No fault rule matched" never establishes health: ok is the verdict only when every finding is ok.

**Two outcomes stand alone:**
1. **No conditions.** Nothing else is readable. The object was created less than 2 minutes ago: progressing, "waiting for KEDA". Here creation does date the state, because a reconciled object keeps its conditions. Past 2 minutes it is unknown, "KEDA has not reconciled this ScaledObject", naming the reason where §6.1 proves it: no handling operator's scope covers it, or its operator is down. The controller task carries the operator's own state.
2. **A confirmed completed full pause** (§1.3's evidence, version-aware): **paused**, never attention.
   - The evidence includes no controlled HPA and Ready not False, so no HPA or Ready finding can apply.
   - The label reads "paused: held at n replicas" (the target's actual replicas proven equal), or "paused: frozen at the current replicas", or "paused at n" with the count's evidence said. A changed request reads "paused at 3; 5 requested".
   - A directional pause (`paused-scale-in` or `-out` only, 2.18 and later) is never a completed pause. It is a note on the label.

**Otherwise, the findings:**
- **Ready=False** (all reasons except the 2.20.0 and 2.20.1 HPA ones, read in §3's HPA result): **fault**, KEDA's reason and message verbatim. The copy is temporal:
  - **"pause failed: <message>"** only for KEDA's own pause-step errors: "failed to update paused status", "failed to stop the scale loop for paused ScaledObject", "failed to delete HPA for paused ScaledObject", "failed to scale target to paused replica count", "failed to update paused replica count status".
  - **Any other Ready=False while a pause is requested** reads "<message> (while a pause is requested)". It never claims KEDA tried and failed the new request.
  - **KEDA's own transient** "ScaledObject paused replicas are being scaled" (2.11 to 2.20) is **progressing**, "pausing: scaling to n", while the pausing episode is under 2 minutes. Past that it is the fault above.
  - **Klyx's own evidence,** where provable: an `authenticationRef` naming a TriggerAuthentication or ClusterTriggerAuthentication absent from a complete, uncut read of its kind, or one the handling operator's authentication selector excludes (§6.1). The headline is then "TriggerAuthentication X does not exist" or "… is outside the handling operator's authentication selector", with KEDA's message beneath. KEDA's own message never names it (§1.5).
- **The HPA result** (§3's table).
- **Ready=Unknown with `PartialTriggerError`:** **degraded**, "some triggers are failing", naming the failing triggers where `status.health` or events name them. Any other Ready=Unknown, before the first poll, is **progressing**.
- **Fallback=True:** **degraded**, "fallback active (<behavior>): <trigger> failed <n> times (threshold <t>)".
  - **What Klyx does not claim:** a held replica count, or that the HPA is healthy because of the fallback. The `currentReplicas*` and `scalingModifiers` behaviours, another trigger that succeeds, and the HPA's own bounds all change the result.
  - **What it shows instead,** separately and labelled: the configured fallback (behavior, replicas), the HPA's current and desired replicas, and the target's actual replicas.
- **`status.hpaName` names a different HPA** from the joined one: **degraded**, "status names HPA X; the HPA KEDA controls is Y".
- **A pause or resume not yet completed** (§3.1's episodes):
  - **progressing** "pause requested" or "resuming", while the episode is under 2 minutes;
  - then **unknown**, with the reason:
    - "pause requested; completion not established": the HPA read is capped, the target's replicas are unreadable, or the episode has a lower-bound start;
    - "pause requested; this KEDA version does not apply <annotation>": only when the handling operator's version is resolved and does not;
    - "pause requested; whether this KEDA applies it is unverified": the version is unknown;
    - "resume not established".

  Intent never clears an observed failure. A proven HPA fault, or Ready=False, during a pending pause is still the verdict, with "pause requested" as context. **Completion "not established" never reads "not applied".**
- **Healthy facts:** **ok**, in KEDA's vocabulary:
  - "active (n replicas, wants m)";
  - "idle" (Active=False, `ScalerNotActive`);
  - "cooling down" (`ScalerCooldown`);
  - plus a directional-pause note.

  **Active=False never lights.**

The joined HPA's `ScalingLimited=True` with reason `TooManyReplicas` adds "wants more than maxReplicaCount" to an ok label. It is a capacity fact, not attention (§4).

## 4. The standalone HPA rule (`autoscaling/hpas`)

**Counted:** HPAs with no controller ownerReference to a `keda.sh` ScaledObject.
- **A KEDA-owned HPA** is never counted here, whether or not its ScaledObject is in the model. Its owner's deletion is a garbage-collection moment, not a fault.
- **Rows still show KEDA-owned HPAs,** marked "KEDA's (ScaledObject X)", with their own conditions, linking to their ScaledObject.

**Fact rows, never verdicts from a zero evaluation.** KEDA-owned HPA rows and authentication rows are fact rows, behind the same counting gate S2b established: their zero `NativeEval` never becomes a Fault, and they never count toward Needs you.

**Rules:**
1. **No conditions.** Less than 60 seconds since creation: progressing. After that: unknown, "the HPA controller has not reported".
2. **AbleToScale=False.** A fault, with the controller's reason and message: `FailedGetScale`, "deployments.apps "X" not found" (finding 14); `FailedUpdateScale`.
3. **ScalingActive=False.** Any reason except `ScalingDisabled` is a fault:
   - `FailedGetResourceMetric`, "unable to fetch metrics": no metrics server, finding 14;
   - `FailedGetExternalMetric`;
   - `AmbiguousSelector`, which names the other autoscalers, as in §3's HPA result.

   `ScalingDisabled` (the target at 0 replicas) is ok, "scaling disabled: target at 0".
4. **A reported lag.** `status.observedGeneration` is present, positive and below `generation`: progressing, the ratified "Syncing…" mark. Never confirmed healthy while pending.
   - **The field is optional.** Kubernetes 1.32's HPA controller rebuilds status without it, and the lab's failing HPA had none (finding 14).
   - **Absent or zero** reads "the HPA controller does not report which generation it observed", a stated limit, never Syncing.
   - **Reported faults (rules 2 and 3) stand either way.**
5. **Otherwise ok:** "n replicas (min a, max b, wants d)".
   - **`ScalingLimited=True` with `TooManyReplicas`** adds "at maximum, wants d". It is shown and never attention: a pegged HPA is a capacity signal, and lighting it would make peak hours noisy. **This is a stated choice for the owner's ruling.**
   - **`TooFewReplicas`** (at the minimum) is normal and not shown.

## 5. The ScaledJob rule

The same structure as §3.2: the worst finding, with two outcomes that stand alone.
- **No conditions:** as for ScaledObjects.
- **A confirmed completed pause** (2.11 and later): `autoscaling.keda.sh/paused` in effect for the version (§1.3), with Paused=True and Ready not False. It reads **paused**, "paused: no new Jobs; running Jobs continue". A ScaledJob has no HPA, so that is its completion evidence.
- **The findings otherwise:**
  - **Ready=False:** a fault (`ScaledJobCheckFailed`, `TriggerError`, `KEDAJobCreateFailed`), with §3.2's temporal copy and authentication evidence;
  - **Ready=Unknown `PartialTriggerError`:** degraded;
  - **the pausing and resuming episodes** (§3.1), as in §3.2. Before 2.11, the annotation reads "this KEDA version does not apply autoscaling.keda.sh/paused". With the version unknown, it reads "unverified";
  - **healthy facts:** ok, "active" or "idle".
- **Fallback is ignored:** KEDA never writes it for ScaledJobs.

**Jobs are not part of the verdict.** A Job's failure is the workload's, not KEDA's. The inspector lists them (§6).

## 6. Authentications, references and the S5 join

### 6.1 The handling operator

Any claim about a subject, a namespace or a running version depends on which operator handles the ScaledObject or ScaledJob. KEDA allows several operators, scoped by namespace and by label.

**Candidates.** Each Deployment labelled as a KEDA operator (§2), with its running pods: the pods owned through the Deployment's ReplicaSets, fenced by owner UID, from the pods model.

**Each candidate's configuration** comes from its running pods, never the Deployment template, because a rollout's template need not be what runs. On demand, a bounded GET of at most 8 running pods per candidate reads:
- `spec.serviceAccountName`;
- the image;
- the namespace;
- the env of an allowlist of names: `WATCH_NAMESPACE`, `WATCH_LABEL_SELECTOR`, `WATCH_LABEL_SELECTOR_FOR_TRIGGERAUTH`, `KEDA_CLUSTER_OBJECT_NAMESPACE`, `KEDA_RESTRICT_SECRET_ACCESS`.

No other env is read.

**One allowlisted name resolves** in exactly these cases:
- **An explicit literal `value`:** that value.
- **An explicit `valueFrom`:** unresolved. Its source is never read.
- **No explicit entry, while the container has any `envFrom`:** unresolved. The source may supply the name, and its contents are never read, as S5 does.
- **No explicit entry, no `envFrom`, and an official `kedacore/keda` image** (ghcr.io or docker.io): unset. Its Dockerfile at the tag sets no environment on a distroless base, and KEDA's default applies.
- **Any other image:** unresolved. The image may set it.
- **Running pods that disagree** (a rollout in progress): unresolved, with both values listed.
- **An unreadable pod:** unresolved.

**Empty scopes** follow KEDA (`pkg/util/watch.go` at v2.21.0): an empty value, and the literal two-character `""`, mean "no scope". An invalid selector, or user labels cut by the projection (§10), make the candidate's applicability unresolved, never a proven exclusion.

**Who handles a user** (a ScaledObject or ScaledJob) is decided from the user's own scope only:
- **Namespaces.** `WATCH_NAMESPACE` (a comma list; empty means every namespace) must name the user's namespace.
- **Labels.** `WATCH_LABEL_SELECTOR`, where set, must select the user's labels.
- **The result:**
  - exactly one resolved candidate that applies is **the handling operator**;
  - none, with every candidate resolved, is "no KEDA operator watches this object" (§3.2's "no conditions" reason);
  - several, or any unresolved candidate that might apply, is **unverified**, with the candidates listed. Klyx never picks one.

**Authentication visibility is a separate question, asked within each operator that handles the user.**
- **`WATCH_LABEL_SELECTOR_FOR_TRIGGERAUTH`** filters which TriggerAuthentications and ClusterTriggerAuthentications that operator's cache holds. It does not decide whether the operator reconciles the user.
- **Per referenced authentication and per handler,** the result is visible, excluded (the selector, resolved, does not select its labels) or unverified.
- **Excluded is a configuration failure of the scaler** (§3.2's Ready=False evidence: KEDA cannot find it), never evidence that the user has no handler.
- **An unverified handler** lists the visibility per candidate.

### 6.2 References, sources and existence

**References** are computed from the ScaledObjects' and ScaledJobs' specs: `triggers[].authenticationRef {name, kind}`, with kind defaulting to TriggerAuthentication. They are namespaced, so a ClusterTriggerAuthentication's users are listed with their namespaces.
- **The authentications' own `status.scaledobjects` strings are never used** (§1.5).
- **A cut trigger list** (§10) makes the inverse "users" list incomplete. The list says so, and an authentication is never shown as unused while any user's triggers are cut.

**The authentications rows** (`keda/authentications`, fact rows) give each authentication's source kinds with their names (Secret and key, env name, ConfigMap name, Vault address and method, Key Vault URI, provider and `identityOwner`), and its users.

**Where KEDA reads each referenced object,** resolved per user through its handling operator (§6.1):

| Source | TriggerAuthentication | ClusterTriggerAuthentication |
|---|---|---|
| `secretTargetRef` | the authentication's namespace, unless the operator's `KEDA_RESTRICT_SECRET_ACCESS` is "true" (case-insensitive); then the cluster-object namespace | the cluster-object namespace |
| `configMapTargetRef` | the authentication's namespace (the restriction does not apply) | the cluster-object namespace |
| the cloud secret managers, Vault and Key Vault | external, not checked | external, not checked |

**The cluster-object namespace** is the operator's non-empty `KEDA_CLUSTER_OBJECT_NAMESPACE`, else its pods' namespace. An unresolved env or an unverified handling operator makes the namespace, and the existence check, unverified.

**On demand, in the inspector** (never in the observer):
- **Existence:** a metadata-only GET per referenced Secret or ConfigMap, in the resolved namespace. It reads "exists", "not found" or "unverified (forbidden, or unresolved namespace)". Values are never read.
- **The bounds,** as S5's on-demand reads:
  - at most 32 GETs per inspector open, each with a 5-second timeout, and a 15-second total;
  - the reply fenced to the cluster and to the inspected object's UID, so a reply for a replaced object is discarded;
  - a cut beyond 32 is said, and leaves the rest unverified.

### 6.3 The authenticating subject (the S5 join)

A new `TriggerAuthSubject` in `internal/identity`, beside `StoreSubject`, reuses S5's `DeclaredOf` and `PodOf`. It always resolves through the handling operator: KEDA authenticates as its operator pod (§1.5). An unverified operator leaves the subject unverified, with the candidates listed.
- **`identityOwner: keda`** (default), and `azure-workload` or `gcp` whatever the owner: "authenticates as the KEDA operator".
  - **The ServiceAccount** is the operator's running pods' `serviceAccountName` (§6.1). Its declared identity comes from `DeclaredOf`, and the running pod's injected identity from `PodOf`.
  - `roleArn` and `identityId` read "the operator assumes <role>" / "uses client <id>".
- **`aws` with `identityOwner: workload`:** per user, the target workload's pod template ServiceAccount, else `default`.
  - **Its `eks.amazonaws.com/role-arn`** reads "the KEDA operator assumes <role> (from ServiceAccount X)".
  - **No annotation:** "KEDA requires eks.amazonaws.com/role-arn on ServiceAccount X; it has none, so this scaler cannot authenticate (an EKS Pod Identity association is not read by KEDA)". It is shown on the ScaledObject's inspector beside its Ready=False.
- `identityOwner: workload` on `azure-workload` or `gcp` reads "ignored by this provider".
- **Cloud trust and permissions stay unverified** (S5).

**S5's ServiceAccount report** gains "TriggerAuthentications that authenticate as this ServiceAccount", computed through the same handling-operator resolution:
- for an operator's ServiceAccount, the operator-owned ones it handles;
- for a workload's ServiceAccount, the `aws`/workload ones whose users target workloads running as it.

This lands the join S5 deferred. Unverified handling is listed as such, never dropped.

### 6.4 ScaledJob Jobs and events

**The Jobs list:**
- Jobs in the namespace labelled `scaledjob.keda.sh/name=<sj>`, kept only when their controller ownerReference UID is the ScaledJob's (the label alone is not proof).
- **A bounded, paginated read:** label-selected pages of 200, at most 5 pages (1,000 Jobs), kept by the owner-UID filter.
  - **The read completes** (no continuation): "the newest 20" is a global claim, with each Job's outcome and age.
  - **A continuation remains:** the list reads "the newest among the first 1,000 read; more exist", with `remainingItemCount` where the server gives it, and "count unknown" where it does not.
  - **A page the owner filter empties** is not absence. With a continuation, "none among the first 1,000 read" is the claim, never "no Jobs".
- KEDA's own failed-Jobs history limit (default 100) is named.
- The same bounds and fences as §6.2.

**Recent KEDA Warning events** (`KEDAScalerFailed`, `ScaledObjectCheckFailed`, `KEDAJobCreateFailed`) come through the inspector's existing events read, UID-filtered.

## 7. Ownership: owner links, the scale target and the scale verb's warning

**Owner and target links** (the `kindResourceRef` fix).
- **What changes:**
  - `ownerRefs` keeps each reference's `apiVersion`;
  - the HPA's `scaleTargetRef` is read with its own `apiVersion`;
  - each reference is resolved through discovery (`resolveGVK`, `fleet/adhoc.go`, TTL-cached) to group, version and plural.
- **The core switch remains** only as the fallback when discovery is unavailable and the `apiVersion` matches the core kind's group.
- **An unresolvable kind** (an unserved CRD, or a malformed `apiVersion`) renders as plain text, never a guessed plural.
- **The namespace follows the discovered scope:** a cluster-scoped kind is linked without one. Failed discovery leaves the link unverified, as plain text.
- **The result:**
  - an HPA links to its ScaledObject;
  - a Job links to its ScaledJob;
  - an HPA links to an Argo Rollout or any other scalable CRD.
- **The generic inspector's owner link** matches by group, kind and name, not kind and name alone.
- **The HPA detail's target link** opens the target object itself (`InstanceDetail.tsx` today only switches to the workloads section).

**Who owns the replica count.** For one workload, each kind of owner reads as **owned**, **proven unowned** or **unverified**:
- **Autoscalers,** from the HPA and ScaledObject models:
  - **owned:** an HPA whose `scaleTargetRef` (group, kind, name) is the workload, named with its owner ("KEDA ScaledObject X, through HPA Y"); or a ScaledObject targeting it, including a fully paused one ("KEDA ScaledObject X is paused and will resume control");
  - **proven unowned:** none of those, with the HPA read complete and uncut, and the ScaledObject read complete and uncut, or KEDA's API not served;
  - **unverified:** any other case, such as a forbidden, failed or capped read.
- **GitOps,** from the authoritative `GitOpsOwnership` check (`fleet/gitops_ownership.go`), read on demand when the popover opens (bounded, as the edit flow reads it). It reads Flux inventories and Argo Application resources, and covers Argo annotation tracking and inventory-only Flux ownership, which labels miss.
  - **owned:** the object-level owner (Basis `flux-inventory` or `argo-resources`). The line reads "if Git sets replicas, <Flux|Argo> sets it back on its next reconcile"; Klyx cannot see whether the manifest sets replicas.
  - **proven unowned:** no object-level owner and no warnings.
  - **unverified:** any warning, or a failed check.
  - **Labels are hints only.** `extractOwner` and `GitOpsOwnerFromLabels` are shown as such, never as proof either way.

**The workloads inspector** shows a "Replicas owned by" line from the same join, with unverified said.

**The scale verb** (C4). The existing `ScaleWorkload` (Deployments and StatefulSets, with a UID precondition) changes as follows:
- **Owned, or unverified on either axis:** the popover names the owners (or the coverage limit), and its action reads "Scale anyway". The ConfirmDialog is required on every cluster and states the consequence:
  - **HPA:** "the HPA sets replicas back within its sync period (about 15 s)";
  - **ScaledObject:** "pause the ScaledObject instead to hold a count";
  - **GitOps:** the line above;
  - **Unverified:** "Klyx could not prove nothing else sets this count: <limit>".
- **Proven unowned on both axes, on an unprotected cluster:** today's behaviour stays. The popover, with an explicit count and an explicit Scale action, is its confirmation. This predates C4, and **is stated here for the owner's ruling, not silently kept**.
- **Protected clusters** keep the typed confirmation (ProtectGuard).

## 8. Verbs (C4)

| Kind | Pause / Resume | Offered when | Otherwise |
|---|---|---|---|
| ScaledObject | **Pause:** merge-patch `autoscaling.keda.sh/paused: "true"`. **Resume:** remove `autoscaling.keda.sh/paused`, offered only when it is the object's only pause annotation | the handling operator (§6.1) is resolved and runs **2.12 or later** | `paused-replicas`, `paused-scale-in`/`-out` and `force-activation` are never set or removed by Klyx. An object paused by them reads "paused by <annotation>, set outside Klyx". `paused-replicas` is a different operation, not offered (C4) |
| ScaledJob | the same, `autoscaling.keda.sh/paused` only | the handling operator runs **2.11 or later** | - |
| TriggerAuthentication, ClusterTriggerAuthentication | none | - | - |
| HorizontalPodAutoscaler | none | - | editing min, max or metrics is authoring |
| Deployment, StatefulSet | - | - | the existing scale verb, with §7's warning |

**When the version or handling operator is unverified,** Pause and Resume are not offered. The verb says why: "KEDA version unknown: Klyx cannot prove this KEDA applies the pause annotation". A patch that would succeed and pause nothing is never offered.

**Every verb carries:**
- the UID precondition (`guardUID`, `fleet/gitopsactions.go`): the patch acts on the object the user saw, or refuses;
- ConfirmDialog, plus ProtectGuard on protected clusters;
- a unique request token, as S1a's verbs do.

**The consequence copy:**
- **ScaledObject Pause:** "KEDA deletes its HPA (<name>) and holds <target> at its current replicas until resumed".
- **ScaledObject Resume:** "KEDA recreates its HPA and resumes scaling <target>".
- **ScaledJob Pause:** "KEDA stops creating Jobs; Jobs already running continue".
- **ScaledJob Resume:** "KEDA resumes creating Jobs from its triggers".
- **GitOps:** the existing `ownershipLine(owner, "annotation")`. If Git declares the annotation, the controller restores Git's value on its next reconcile.

**Honest outcomes.**
- The verb says "pause requested".
- Completion is §1.3's evidence (§3.2; for a ScaledJob, §5), as of its next read. A Paused=True already present from a directional pause is never read as completion.

**The non-goal crossing.** Pause and Resume are a new write path, under C4's pre-authorised KEDA pause. They drive a controller and author no desired state.

## 9. What Klyx shows

**Placement.** The registry's `autoscaling` group (Karpenter, HPAs) gains:
- **a KEDA section** (capability `keda`), with three lists: ScaledObjects, ScaledJobs and Authentications;
- **HPAs as a native part** (verdict rows), replacing the group's generic HPA entry. The generic resource browser still lists HPAs raw.

**The ScaledObject inspector.**
- **The verdict headline** (`InspectorStatus`).
- **KEDA's conditions as chips:** Ready, Active, Fallback, Paused with its kind, and HPAActive. "not reported" where absent.
- **Links:** the target, resolved (§7); the joined HPA, with its min, current, desired and max replicas and its conditions.
- **The triggers table:** type, metric key, activity (`triggersActivity`), failures (`status.health`, "not tracked without a fallback" when absent), and authentication with its existence.
- **The rest:** the fallback configuration, the pause annotations, §6's authentication block with subjects, and recent Warning events.

**The other inspectors.**
- **ScaledJob:** the same shape, with the Jobs list in place of the HPA.
- **Authentication:** sources, users, and the subject.

**Needs you, the mast, the fleet spine.** The counted parts join the shared engine. A degraded row reads "ScaledObject team/web: fallback active".

**AI and chat.**
- **AI allowlist fields:** names, the target, conditions (type, status, reason, clipped message), `hpaName`, the pause kind, trigger types and metric keys, authentication names and source kinds, and the subject's kind.
- **Never sent:** trigger metadata, authentication source details, and env or Secret names beyond the reference.
- **Chat's GVR-to-task map** gains `keda.sh/scaledobjects`, `keda.sh/scaledjobs` and `autoscaling/horizontalpodautoscalers`.
- **`chat_project.go` gains explicit KEDA projections.** The unknown-CRD projection would otherwise copy trigger metadata and authentication source details.
- **Literal-needle tests pin the exclusions:** a sentinel in trigger metadata, in a Vault token and in an authentication's Secret name. They check that none reaches chat tools, snapshots, Explain, instance facts or notifications.
- **The notification kind labels** gain `scaledobjects`, `scaledjobs` and `horizontalpodautoscalers` (UID objects, eligible; notifications stay off by default).

## 10. Projections, budget and sensitive data

**Per-field caps** (applied in the projection; each cut is flagged, and counts are kept exact):

| Field | Cap |
|---|---|
| condition messages | 512 bytes, scrubbed under the URL rule before clipping |
| conditions | 8 per object |
| triggers | 16 per object, with `triggersCut` and the exact count |
| `health` and `triggersActivity` | 16 keys each, with a cut flag |
| an authentication's references | 16 per source kind, with a cut flag and the exact count |
| names | their Kubernetes maxima |

**Cut semantics.** A cut never manufactures a clean join, and every membership or absence claim requires an uncut input:
- an authentication "does not exist" or "unused";
- "no HPA";
- "proven unowned";
- an inverse identity report.

A cut makes that claim unverified. Known faults stay, with their detail qualified.

**Projections:**
- **ScaledObject:**
  - namespace, name, UID, generation, creation time, and labels (for operator selectors, capped at 32 with a cut flag);
  - the pause and transfer annotations only (an allowlist of keys);
  - `scaleTargetRef` {apiVersion, kind, name}, `horizontalPodAutoscalerConfig.name`, min, max and idle, the fallback {failureThreshold, replicas, behavior}, and whether `scalingModifiers` is present;
  - triggers: type, name, metricType, `authenticationRef` {name, kind} and `useCachedMetrics`, **never `metadata`**;
  - status: conditions, `hpaName`, `scaleTargetGVKR`, `pausedReplicaCount` (presence kept: nil and zero are different), `health`, `triggersActivity` and `lastActiveTime`.
- **ScaledJob:** the same where it applies, plus the history limits. **The `jobTargetRef` pod spec is never retained.**
- **HPA:**
  - namespace, name, UID, generation, creation time, and `observedGeneration` (presence kept);
  - the controller ownerReference {apiVersion, kind, name, uid};
  - `scaleTargetRef`, min and max, current and desired replicas, and `lastScaleTime`;
  - the metric count and types;
  - conditions with `lastTransitionTime`.
- **Authentication:**
  - namespace (if any), name, UID, and labels (capped at 32);
  - source kinds, with their reference names and keys;
  - podIdentity {provider, identityOwner, roleArn present, identityId present};
  - the Vault address and authentication method.
  - **Never `hashiCorpVault.credential.token`:** only the method ("token") is kept.

**Budgets per cluster** (`KindSpec.Budget` and `MaxObject`; S1a §3's admission, where a full budget reads "capped" and the cut semantics above apply):

| Kind | Budget | MaxObject |
|---|---|---|
| `scaledobjects` | 512 KiB | 4 KiB |
| `scaledjobs` | 128 KiB | 4 KiB |
| `horizontalpodautoscalers` (always watched) | 512 KiB | 2 KiB |
| `triggerauthentications` | 128 KiB | 2 KiB |
| `clustertriggerauthentications` | 64 KiB | 2 KiB |

That is 1.31 MiB of encoded admission budget per cluster at most: a bound on what the models admit, not on heap. Of it, 512 KiB (the HPAs) is present on every cluster, and the rest only where KEDA is served.

**The on-demand reads** (§6, §7) are bounded per inspector open:
- 32 existence GETs;
- 8 running pods per operator candidate, with at most 4 candidates read;
- one paginated Jobs read (5 pages of 200);
- one `GitOpsOwnership` check;
- one ServiceAccount GET per subject.

Each read has a 5-second timeout, and each reply is capped at 256 KiB encoded.

**Measurement.** Under a fixture that fills each budget, as S8a and S4 did, Klyx reports both the encoded model bytes (`SizeOf`) and the actual heap retained. The load test is rerun before and after. A tracker entry (§3.1) is counted with its kind.

**Sensitive data:**
- **The Vault token** (`hashiCorpVault.credential.token`) goes through `SanitizeEmbedded`'s policy (S0b), like Secret data:
  - it is masked in the YAML view and the edit surface, with placeholder idempotence, and a sanitized re-apply is refused;
  - generic Describe text is withheld, rather than rewritten, where that policy requires it.
- **Trigger metadata** is configuration. It shows in the on-demand object read and YAML, and is never retained in the observer or sent to AI.
- **Controller messages** are scrubbed before they are clipped.

## 11. Tests, lab, parity, smoke

**Unit tests (`internal/keda`, pure):**
- every §3.2 finding, including:
  - each condition reason in §1.2;
  - all three pause annotations, alone and together (`paused-replicas` wins over `paused: "false"`), an unparseable `paused`, the "pausing" and "resuming" mismatches, and directional pauses;
  - the join by controller UID with a custom name, an adopted HPA, a `hpaName` mismatch, a missing HPA within and past the grace, and an incomplete HPA read;
  - a missing authentication with complete and incomplete reads;
  - `AmbiguousSelector` with KEDA's and a plain HPA;
- every §1.8 version shape as a fixture: 2.17 (no `hpaName` after adoption, no HPAActive, Paused Unknown), 2.20.0 (`HPAMetricsUnavailable`), and 2.15 (health without a fallback);
- §4's and §5's rules;
- the clock seams, with an injected clock: the no-conditions grace, the no-HPA grace and the "pausing" grace;
- **revision 1:**
  - a Fallback=True object whose HPA reads `AmbiguousSelector` (a fault, with the fallback as an additional finding);
  - an HPA with AbleToScale=False and ScalingActive=True;
  - HPAActive against the direct HPA, disagreeing in both directions, with the HPA in and out of a complete model;
  - each fallback behaviour, a healthy second trigger, and a failing HPA, with no "held at" claim;
  - pause on 2.10 (`pausedReplicaCount` set, zero included, and unset), 2.11 (`paused` with no effect), 2.12 (`paused: "false"` pausing), 2.17 and an unknown version;
  - a directional pause already Paused=True, then a full pause requested (not completed until the HPA is gone);
- **revision 2:**
  - a current retained failing HPA in a capped read (the HPA result is a fault);
  - HPAActive disagreeing in both directions under the cap;
  - Fallback=True with a proven missing HPA past the episode (rule 5, a fault, with the fallback as a finding);
  - a failed paused-count write after the HPA's deletion (Paused=True, no HPA, Ready=False: "pause failed");
  - a malformed `paused-replicas`;
  - a change from 3 to 5 while the status still reports 3;
  - a capped HPA read during a pause ("not established", never "not applied");
  - KEDA's transient "paused replicas are being scaled" within and past the episode;
  - 2.11 `paused-replicas` with a nonzero and a non-nil zero count;
  - the episode tracker: an old object newly paused, first seen already pausing (lower bound), a gap during an episode (restart), a UID replacement, and a clock-only crossing through `Next`;
- **revision 3:** every row of §3's HPA result table, including:
  - a 2.17-shaped object (Ready=True, Active=True, no HPAActive, no `hpaName`) with a capped or unreadable HPA read ("HPA join not established", never ok);
  - HPAActive=True with an unreadable join (unknown);
  - a complete empty HPA read within the awaiting-HPA episode (progressing);
  - both directions of disagreement with the direct read current and stale;
  - a full pause requested while a current owned HPA reads `AmbiguousSelector` (fault, with "pause requested" as context);
  - a resuming episode with an HPA fault;
  - KEDA's transient "being scaled" alongside an independent HPA failure (the fault wins);
  - Ready=False present before a new pause annotation ("while a pause is requested", never "pause failed").

**`internal/identity`:**
- `TriggerAuthSubject` for each provider and owner;
- the role-arn annotation, present and absent;
- `roleArn` with the operator's identity;
- `identityId`.

**The HPA rule:** a healthy older-controller HPA with conditions and replicas but no `observedGeneration` (never Syncing); an explicit zero; a positive lag.

**The handling operator and the namespace table:**
- two scoped operators sharing a ClusterTriggerAuthentication;
- a user whose TriggerAuthentication its handler's authentication selector excludes ("outside the selector", never "no operator");
- two user handlers, only one of which sees the authentication;
- `envFrom` that may supply the cluster-object namespace or the watch scope (unresolved);
- an official image with no explicit entry (KEDA's default), and a custom image (unresolved);
- the empty scopes (`""` literal) and an invalid selector;
- an old/new pod rollout with disagreeing env;
- `KEDA_CLUSTER_OBJECT_NAMESPACE` set and unset, and `KEDA_RESTRICT_SECRET_ACCESS` with a ConfigMap reference (unaffected);
- a blank and a `valueFrom` env;
- a partial read;
- **Jobs:** a full first page with an older creation range and a newer owned Job on the next page; a continuation without `remainingItemCount`; a page the owner filter empties.

**The metrics adapter:** an unrelated adapter in the operator's namespace (foreign or unverified, never KEDA's), a positively identified KEDA server, and an unreadable backing-service join.

**Ownership for the scale verb:** Argo annotation tracking without the instance label, inventory-only Flux ownership, and a forbidden or capped HPA read (each unverified, so the full confirmation).

**Budgets:** every cut flag; an authentication used only by trigger 17 (never "unused"); and a full-budget fixture measured as §10.

**Discovery owner links:** a core kind, a CRD owner (a ScaledObject), an unserved kind, and a same-named kind in another group (never linked).

**`observe` and `healthsrc`:**
- the HPA kind's two consumers (C3: either opens it; borrowed presence refused);
- projection equality (raw read == projected read, for every field a rule uses);
- the baseline, continuous, after-gap, loss and cap cases;
- a ScaledObject deleted and created again under its name: a new UID. Its old HPA's controller reference no longer joins until KEDA re-adopts, and the gap reads "no HPA" within the grace, unknown.

**Frontend:** the section, the lists, the inspectors, the scale popover's owner line and "Scale anyway", the Pause and Resume dialogs, and the owner links.

**The lab** (`lab-s9`, kind-klyx; teardown script). It runs a real KEDA 2.21.0 under C6, because the join, the fallback masking and the fail-open webhook are behaviour, not shape. Lab objects:
- **ScaledObjects:** 15, covering active, idle, a custom name, adopted, paused, paused-at-one, failing with fallback, bad-auth, ghost-auth, no-target, a StatefulSet, workload identity, ClusterTriggerAuthentication, one-way and shared;
- **a ScaledJob** with its Jobs;
- **plain HPAs:** a missing target, no metrics, two on one target, and one beside a ScaledObject.

**Fixtures for what the lab cannot produce:**
- older-version status shapes;
- two ScaledObjects on one target with the webhook down;
- an APIService held by another adapter;
- a never-reconciled ScaledObject.

**Parity:**
- **the cluster:** the four KEDA CRDs on `kind-klyx-parity` (`klyx-parity.sh`), and a stand-in `keda-operator` Deployment in `fixture.sh`'s `controllers()`;
- **the fixtures:** a healthy ScaledObject with its HPA, whose controller reference UID is set after creation and whose status is written through the subresource; a Fallback=True one; a paused one; a ScaledJob; and a plain HPA with a written status;
- **the oracle and the gate:** `parity_lab_test.go`'s parts and controller task, and `parityGate.test.tsx`'s `ROW_PARTS` and `CASES`.

**Native smoke:**
- a `TITLES` entry "KEDA";
- a step that opens `failing` and asserts the "fallback active" verdict, with its trigger's failure count and the HPA's replicas shown separately;
- a step that opens `active-web`'s workload and asserts the scale popover names "KEDA ScaledObject active-web".

**Live captures:** night and day at 1440 and 1024, headless.

## 12. Not in S9

- **`eventing.keda.sh`** (CloudEventSource, ClusterCloudEventSource), and the KEDA HTTP add-on (HTTPScaledObject).
- **Metric values:** reading `external.metrics.k8s.io` for a ScaledObject's current value is a later candidate. Klyx shows activity and failures, not numbers.
- **`scalingModifiers`:** formulas are shown as configured, never evaluated.
- **Verbs:** `paused-replicas`, `force-activation`, editing triggers or HPAs.
- **VerticalPodAutoscaler.**
- **Positive evidence for managed KEDA:** the AKS add-on and others are unverified (§1.7).

## 13. Staging

1. **The pure package `internal/keda`:** parse, project and the rules, with their tests.
2. **Discovery-resolved owner and target links** (§7): a small, independent change, with its tests.
3. **`observe`, `healthsrc` and `capability`:**
   - the HPA kind (C3) and `autoscaling/hpas`;
   - the KEDA parts and the controller task;
   - registration, facts and re-probe.
4. **`appbridge`:**
   - the rows converters and DTOs, the AI allowlist, chat and notification labels;
   - the on-demand inspector reads: reference existence, `TriggerAuthSubject`, the ScaledJob's Jobs and the replica owner;
   - Pause and Resume;
   - the scale warning's facts.
5. **The frontend:** the registry and section, the rows and inspectors, the verbs, the scale popover and the owner links.
6. **The lab, parity, smoke, captures and the as-built notes.**

## 14. Revision 1: Codex's design review of 5b165d8

| # | Finding | Where answered |
|---|---|---|
| 1 | A joined HPA's failure ranked after the degraded rules, and KEDA's cached HPAActive read as fresh | §3: the direct HPA is the primary evidence, and HPAActive is evidence only when the HPA is not in a complete model; rule 4 (AbleToScale or ScalingActive failure, `AmbiguousSelector`) runs before PartialTriggerError and Fallback, which become additional findings; §11's combinations |
| 2 | "Held at fallback.replicas" and "healthy only because of fallback" are not proven | §3 rule 6: "fallback active (<behavior>)" with failures and threshold; the configured fallback and the HPA's and target's replicas shown separately; no held claim |
| 3 | An absent HPA `observedGeneration` read as a perpetual lag | §4 rule 4: Syncing only on a reported positive lag; absent or zero is a stated limit; §10 keeps presence |
| 4 | "Unowned" judged from labels and incomplete models | §7: owned / proven unowned / unverified per axis; the authoritative `GitOpsOwnership` check, with labels as hints; unverified takes the full confirmation |
| 5 | The subject, namespace and version claimed from "the operator Deployment" | §6.1: candidates resolved per user from running pods' allowlisted env and watch scope; several or unresolved is unverified. §6.2: the exact namespace table and bounded, fenced reads; §6.3 resolves through the handling operator |
| 6 | Pause and verbs not version-aware | §1.3 and §1.8's version table (2.10 and later); §3 rule 2's positive evidence per version, including the HPA's deletion; §8 offers Pause only on a resolved 2.12+ (ScaledJob 2.11+) operator |
| 7 | APIService ownership inferred from a namespace | §2: traced to the backing pods' image; otherwise "ownership unverified"; the consequence qualified to external triggers |
| 8 | Typical sizes instead of budgets; no cut semantics | §10: per-field caps with flags, per-kind `Budget` and `MaxObject`, bounded on-demand reads, cut semantics for every membership or absence claim, a full-budget measurement |

**Implementation guidance adopted:**
- the S2b fact-row gate (§4);
- ScaledJob pause copy (§5, §8);
- directional pause against completion (§3, §8);
- explicit `chat_project.go` cases and literal-needle tests (§9);
- the Vault token through `SanitizeEmbedded` (§10);
- discovery-scoped namespaces, and failed discovery kept unverified (§7).

## 15. Revision 2: Codex's re-review of 0cca474

| # | Finding | Where answered |
|---|---|---|
| 1 | A retained HPA's conditions were ignored under a cap, and proven HPA absence ranked after degradation | §3: the joined HPA's conditions are used whenever it is retained (stale qualified); completeness only proves absence; rules 2, 4 and 5 (faults) run before rules 6 to 8 (degraded) |
| 2 | Paused=True and the HPA's absence do not prove a completed pause | §1.3: the evidence per version, including the target's actual replicas for "held at n", and applied against requested counts; §3 rule 2's "pause failed" ranks before rule 3; "not established" is never "not applied" |
| 3 | Authentication visibility was used to decide the handler; an absent env was read as the default | §6.1: the handler comes from the user's scope only; authentication visibility is evaluated within each handler (excluded is a scaler configuration failure); env resolution rules for `valueFrom`, `envFrom`, official and custom images; empty scopes as KEDA reads them |
| 4 | The pausing grace had no observable start | §3.1: a UID episode tracker, with lower-bound starts, gap restarts, replacement, clearing, `Next`, and a bound; the awaiting-HPA episode replaces "last unpause seen" |
| 5 | 2.11 `paused-replicas` fell between the cases | §1.3's version table, including 2.11 |
| 6 | The first 200 Jobs are not the newest 20 | §6.4: a bounded paginated read; a global claim only when it completes; partial coverage said otherwise |

**Guidance adopted:**
- the APIService chain uses the backing pods' own images and endpoint target UIDs (§2);
- empty scope forms (§6.1);
- "no HPA controlled by this ScaledObject" (§3 rule 5);
- encoded budget against heap (§10).

## 16. Revision 3: Codex's re-review of 76f1fef

| # | Finding | Where answered |
|---|---|---|
| 1 | An unproven or pre-deadline HPA join fell through to ok | §3's HPA result table: "awaiting HPA" (progressing) during the episode, "HPA join not established" (unknown) without proven absence, and a cached HPAActive=True proving nothing. §3.2: ok only when every finding is ok |
| 2 | A stale retained positive HPA dismissed a KEDA-reported failure | §3: currentness kept apart from completeness. A stale positive is dated evidence that never establishes recovery, and against HPAActive=False it is unknown, "HPA health not established" |
| 3 | A pending pause or resume hid a proven HPA fault, and "pause failed" was claimed too early | §3.2: independent findings with the worst as the verdict. Only a confirmed completed pause stands alone; intent is context and never clears a failure. "Pause failed" applies only to KEDA's own pause-step errors |

**Design accepted at 279a50d** (Codex, revision 3, handoff f7b5315d, 2026-10-19), with no remaining findings. This accepts the design only; the implementation and its gates keep their own review. Two choices remain the owner's: the popover-only scale for a workload proven unowned on an unprotected cluster (§7), and `TooManyReplicas` as a capacity fact without attention (§4).

## 17. As built

Built on `feat/coverage-s8` after the design's acceptance at 279a50d:
- **481c5b7** - the pure packages `internal/hpa` and `internal/keda`.
- **68481ab** - owner and target links resolved through discovery (§7).
- **3b4c639** - the capability, the kinds and parts, the controller task and the episode tracker.
- **374b79b** - the rows, the AI and chat allowlists, and the Vault token mask.
- **199f4eb** - the on-demand reads, the S5 join, replica ownership and Pause/Resume.
- **f67578a** - the Autoscaling lens, the scale verb's owner line and the identity join.
- **0e12561** - parity with a real KEDA and case 9, the smoke steps, the episode goroutine's tests, and a shared destination's strongest line.
- **1a3db14** - the mutations' tests, a scaler's `MaxObject` bounding its cap product, the budget calibration and the load fixture.
- **The commit carrying this section** - these notes.

### 17.1 What it is

- **`internal/hpa`:** the HPA projection (§10, `observedGeneration` presence kept, the controller reference, the target, conditions with their times), §4's rule with `OKLabel`, and the helpers the ScaledObject rule shares (`Failure`, `Lag`, `KEDAOwner`, `Scales`).
- **`internal/keda`:**
  - the projections of all four kinds, with every cut flagged and the Vault token never read;
  - `Version` from an image, the pause intent per version (§1.3), and both completion rules;
  - the episode tracker (§3.1);
  - §3.2's findings and verdict, §3's HPA result table, and §5's ScaledJob rule;
  - the handling operator, its settings and scopes, and §6.2's namespace table.
- **`capability`:** KEDA's detection (§2): the operators and their running version, the admission webhooks, and the external metrics APIService with its owner proven through the Service's EndpointSlices to the backing pods' images.
- **`observe`, `healthsrc`, `fleet`:**
  - the shared HPA kind with its two consumers (C3), `autoscaling/hpas`, and KEDA's three parts and controller task;
  - `Capture.AsOf`, so stale HPA evidence is dated;
  - the episode goroutine, which publishes `FactKEDAEpisodes`;
  - on-demand: `KEDAInspect` (the handler, authentication existence and visibility, the subject), `KEDAJobsOf`, `ReplicaOwner` and `KEDASetPaused`;
  - the S5 join: a ServiceAccount's report lists the authentications that act as it.
- **`appbridge`:** the rows' DTOs (a fact row carries no rule), `KEDAService`, the AI and chat allowlists with literal-needle tests, and the Vault token's `SanitizeEmbedded` surfaces.
- **The frontend:** the Autoscaling section (HPAs, ScaledObjects, ScaledJobs, Authentications) with its inspectors, Pause and Resume behind a confirmation, the scale popover's owner line with "scale anyway", owner links, and KEDA links in the identity blocks.

### 17.2 Decided while building

- **Per-object maxima are larger than §10's table, from measurement.**
  - **Scalers: 40 KiB, bounding the product of every cap.** A ScaledObject with every retained field at its cap projects to 34,849 bytes and a ScaledJob to 25,112 (`TestMaxObjectBoundsTheCapProduct`). No ScaledObject or ScaledJob is left out for its shape, only for the budget. The design's 4 KiB could have dropped exactly the faulty ones, those with long messages.
  - **HPAs: 8 KiB,** which bounds their cap product (pinned by `hpa.TestProject`; 6,167 bytes at the calibration's caps). An HPA keeps 6 conditions.
  - **Authentications: 16 KiB.** Their cap product cannot be bounded sensibly within their 128 KiB and 64 KiB budgets: one at every cap of its in-cluster sources alone is 36 KiB, before the external sources' references. A large realistic one (16 references in each in-cluster source, Vault and a pod identity) is 8.5 KiB. One past 16 KiB is counted, not retained: its kinds read truncated, so a missing authentication stays unverified, never claimed.
  - Sizes count retained bytes, and every clipped string is a copy.
- **A finding can be a note** (`Finding.Note`): informational, never the headline. "KEDA's copy disagrees" (the HPA healthy now) and "the pause annotation is unverified" are notes.
- **The running version is read at the capability probe,** from the operator's ready pods fenced by owner UID (Deployment, ReplicaSets, pods), not from the observer's pods model. It follows the re-probe interval.
- **`KEDA_RESTRICT_SECRET_ACCESS` applies to every Secret reference** (KEDA's `resolveAuthSecret`), not only `secretTargetRef`.
- **Episode continuity is bounded by contiguity.** An HPA created and deleted between two coalesced snapshots within one continuity is not seen. A stretch is carried only when the scaler was not written, or the kinds changed once at most since the last visit; otherwise the start restarts as a lower bound.
- **Scalers carry their GitOps owner** (`flux.OwnerOf` on the full object, before any cut) for the verbs' ownership line, and `GitOpsOwnership` returns one value.
- **Parity runs a real KEDA, scoped to its own namespace.** §11 planned a stand-in operator. A stand-in cannot serve the external metrics API, and that API's absence is a controller issue (§2), so the baseline could not read healthy. The fixture applies KEDA 2.21.0's release manifest, pinned by digest, with the operator and metrics server watching only `keda`. The admission webhooks keep their cluster-wide scope and validate every fixture scaler. The fallback ScaledObject moved to case 9: the baseline carries no fault or degraded object.
- **A destination parts share shows its strongest line.** Parity's case 9 found it. `navCounts` wrote each line's badge in turn, so KEDA's degraded ScaledObject (later in `TASKS`) hid the HPA fault's danger on the shared Autoscalers destination. Flux's operator view (FluxInstance and ResourceSets) had the same last-write-wins. A shared destination now shows its strongest line, as a group does, never a sum.
- **KEDA's authentications are not an attention line.** They are inventory rows (§2: never Needs you), and their kinds' gaps read on the ScaledObjects line as dependencies. The frontend gate declares the part inventory and asserts the model does not carry it.
- **The smoke's title** is the section's, "Autoscaling", since HPAs and KEDA share it.

### 17.3 Verified

- **The lab** (`lab-s9`, a real KEDA 2.21.0 on kind-klyx; `FINDINGS.md` 1 to 14).
- **The parity gate**, both stages, with a real KEDA on `kind-klyx-parity`:
  - **baseline:** a ScaledObject idle at zero with its HPA and authentication, a completed full pause, an idle ScaledJob, and a standalone HPA. Every target is at 0 replicas, so kind's HPA controller writes the same status every sync;
  - **case 9:** a ScaledObject on fallback (degraded) with its HPA, and an HPA whose target does not exist (a fault). It settles in about 2.9 s and the baseline returns in about 2.7 s.
- **The captures** (headless, the server-mode build on the smoke fleet; night and day at 1440 and 1024), none scrolling sideways:
  - **Overview:** HPAs "5 cannot scale" with hpa-gone, plain-api and shared-plain; KEDA's ScaledObjects "7 failing or not scaling" with bad-auth, cluster-auth and ghost-auth's "TriggerAuthentication no-such-auth does not exist"; ScaledJobs "none failing";
  - **the ScaledObjects list,** broken first in KEDA's words: the faults, then `failing` ("Fallback active (static)"), the two completed pauses, then the healthy ones;
  - **the inspectors:** `failing` (its trigger's failure count beside the HPA's replicas), `paused` ("paused (completed)", "none: a full pause deletes it"), `ghost-auth`, `one-way` ("scale-in paused"), `wl-identity`, `hpa-gone`, the ScaledJob `batch` with its Jobs, and the authentication `aws-workload`;
  - **the Pause confirmation** on `active-web` (cancelled): it names the HPA KEDA deletes and the held Deployment, and says "requested" until KEDA's status shows the pause;
  - **the scale popover** on `active-web` (cancelled): "KEDA ScaledObject active-web, through HPA keda-hpa-active-web: pause the ScaledObject instead to hold a count", with "scale anyway".
- **The budget calibration** (`TestKEDAHeapCalibration`): each kind filled to its budget, each source built and dropped inside the measurement.

| Kind | Shape | Objects | Encoded each | Retained heap | Heap per encoded byte |
|---|---|---|---|---|---|
| scaledobjects | typical | 349 | 1,502 B | 853 KB | 1.63 |
| scaledobjects | at the caps | 19 | 27,495 B | 554 KB | 1.06 |
| scaledjobs | typical | 113 | 1,153 B | 163 KB | 1.25 |
| scaledjobs | at the caps | 6 | 20,235 B | 134 KB | 1.10 |
| triggerauthentications | typical | 414 | 316 B | 160 KB | 1.22 |
| triggerauthentications | large | 15 | 8,509 B | 174 KB | 1.37 |
| clustertriggerauthentications | typical | 212 | 308 B | 80 KB | 1.22 |
| clustertriggerauthentications | large | 7 | 8,501 B | 81 KB | 1.36 |
| horizontalpodautoscalers | typical | 566 | 926 B | 483 KB | 0.92 |
| horizontalpodautoscalers | at the caps | 85 | 6,167 B | 586 KB | 1.12 |

  The encoded admission budget is 1.31 MiB per cluster, 512 KiB of it (the HPAs) on every cluster. At full budgets the retained heap is about 1.9 MB per cluster. An authentication at every cap (about 36 KiB) is counted, not retained.
- **The load test** (`TestLoad`) now carries S9: 200 ScaledObjects with their HPAs, 100 plain HPAs, 20 ScaledJobs and 50 authentications on the large cluster, and a tenth of that on each small one. The same fixture ran on the code before S9 (eadf234, the objects unwatched in the fake API) and after, three times each, on an idle machine:

| | before | after |
|---|---|---|
| first complete summary, large | 1.06-1.07s | 1.07s |
| Observers' retained heap, steady | 59.6-59.7 MB | 61.0 MB |
| recovery peak / steady | 1.21x, 1.31x, 1.25x | 1.40x, 1.28x, 1.22x |
| full evaluation, large (max of 5) | 67.7-69.6 ms | 70.6-74.3 ms |
| every kind live after the API recovered | 8.25-8.51s | 8.42-8.63s |

  - **What S9 costs at this scale:** about 1.3 MB of steady heap and about 3 ms of full evaluation (four more parts).
  - **The recovery peak** stays within its 1.5x budget in all six runs.
  - **Full evaluation** misses 50 ms before and after alike. It remains the open owner question.

### 17.4 Gates

- **The native smoke:** 193 of 193 steps in each of night and day at 1440 and 1024 on 1a3db14, with the two S9 steps:
  - **fallback:** `failing` reads "Fallback active (static)" with its trigger's 635 failures, and the HPA line "keda-hpa-failing: 2 → 2 (min 1, max 3)" apart;
  - **the scale verb:** the popover names "KEDA ScaledObject active-web, through HPA keda-hpa-active-web" and offers "scale anyway"; cancelled, never applied.
- **The parity gate:** both stages pass on 1a3db14, every case and both recoveries.
- **`relabs.sh`** applies `lab-s9` with the other labs.
- **Go:** `go test -race` passes for `keda`, `hpa`, `healthsrc`, `capability`, `fleet`, `appbridge`, `identity`, `observe` and `crd`; golangci-lint reports 0 issues.
- **The frontend:** tsc is clean, eslint has 0 errors (its 3 warnings predate S9), and vitest passes 2,328 tests.
- **Mutations:** 75, all caught.
  - 52 on the first pass. Six more did not build (a mutation left a variable unused) and were rewritten to compile.
  - The 16 survivors each gained a test:
    - the capacity fact only on `TooManyReplicas`;
    - a ScaledJob paused while Ready=False;
    - `KEDAOwner`'s group;
    - an HPA never its own "other autoscaler";
    - a complete HPA read gone stale proving nothing;
    - a same-named target in another namespace;
    - AmbiguousSelector naming the others;
    - mixed and non-pod backing endpoints;
    - a not-ready webhook;
    - an unproven ScaledObject read;
    - an unknown handler version's refusal;
    - a 2.11 ScaledJob's pause, in the backend and in the frontend;
    - the protected grant on Pause/Resume;
    - Git ownership in the scale confirmation.
  - `ReplicaOwner`'s read became `autoscalersOf` over the `VisitKind` interface, so its proofs are tested without a running Observer. That added one mutation (no Observer proves nothing).
  - No equivalents.

## 18. Implementation revision 1: Codex's review of aa4bddb

Ten P2 findings, each reproduced by a probe. Every probe is kept as a test (`internal/{keda,fleet,appbridge,identity}/*review1*_test.go`, and the mounted AutoscalingView case). The fixes are in 493bf19.

| # | Finding | Fix |
|---|---|---|
| 1 | An incomplete operator read could establish a unique handler and permit Pause/Resume | `HandlerOf` takes `unread`: with candidates past the four read, no read candidate is the handler and none is never "none". A candidate whose ReplicaSet or Pod list was continued, or which runs more than 8 pods, is `Config.Incomplete`: no setting, ServiceAccount or version resolves. The verb refuses and names the cap. The capability's version read refuses continued lists too |
| 2 | A negative label selector could match because the projection omitted its key | A cut label map decides only the keys it holds (their values are exact). A requirement, positive or negative, on a key it lacks is undecided. KEDA parses both selectors with `metav1.ParseToLabelSelector`, as Klyx does: `!=` is invalid there, so a negative value test is `notin` |
| 3 | A retained HPA with unestablished health cleared KEDA's failure | A joined, current HPA's health is established as §4 establishes it (`hpa.Rule`). One the controller has not reported, or one behind its generation, reads "HPA health not established" (unknown) against `HPAActive=False`, and otherwise the HPA's own state ("HPA: Waiting for the HPA controller", "HPA: Syncing…"). Never ok |
| 4 | A capped scaler kind stopped retained objects' episode clocks | A live kind is observed for the scalers it retains, capped or not. One that drops out of the model starts again as a lower bound when it returns |
| 5 | An unchanged scaler resourceVersion did not prove its HPA-dependent state continuous | A state that reads the HPAs (pausing, awaiting an HPA) carries across a non-contiguous step only when the HPA kind did not change at all. Resuming reads no HPA and keeps the resourceVersion rule. The driver's updates are serialized, so each visit is compared with the one before |
| 6 | The inverse ServiceAccount join gave every authentication to every matching operator account | Derived from actual users: each ScaledObject's and ScaledJob's references, its handler proven as the forward join proves it, and that handler's visibility of the authentication. An unverified handler or visibility, a disputed ServiceAccount, a cut trigger list or an incomplete scaler read makes the read partial |
| 7 | Mounted authentication evidence survived a change to the scaler's references | The scaler DTOs carry `evidenceRev`: a fingerprint of the generation, namespace, labels, target and authentication references. The inspector re-reads on its change, and shows a reply only for its own object and revision |
| 8 | The final inspector reply had no 256 KiB bound | `KEDAInspection.Fit` drops references, unverified ones before any finding, each from the longest list first, and counts every one dropped (`RefsOmitted`, `RefsCut`). `KEDAService.Inspect` tightens the fit until the serialized DTO is within 256 KiB |
| 9 | Projection-level reference cuts disappeared from the inspector | References the projection did not keep (16 per source) are counted in `RefsOmitted` and the check reads `RefsCut`. The inspector says "N more references are not listed" |
| 10 | Azure's requested tenant was discarded | `identityTenantId` is projected and part of the subject ("tenant …", shown only in a tenant's shape). With none, the subject says the operator's default tenant. The AI and chat allowlists are explicit, so the tenant is not sent |

**Added beyond the probes:**
- tests for a continued ReplicaSet list, more running pods than are read, the cap's own refusal message, undecidable visibility (an authentication's cut labels), and a handler whose pods disagree on the ServiceAccount;
- `kedaAuthsFor` had no test before this revision.

**Gates on c40004f:**
- **Codex's probes:** all 11 pass through the original overlay under `-race`.
- **The native smoke:** 193 of 193 in each of night and day at 1440 and 1024.
- **The parity gate:** both stages pass.
- **`relabs.sh`:** every lab applies.
- **The captures:** all 15 screens pass in each of the four modes.
- **Go:** `go test -race` passes for `keda`, `hpa`, `healthsrc`, `capability`, `fleet`, `appbridge`, `identity`, `observe` and `crd`; golangci-lint reports 0 issues.
- **The frontend:** tsc is clean, eslint has 0 errors, and vitest passes 2,331 tests.
- **Mutations:** 38 on this revision's code, all caught.
  - Two survived the first pass: a continued ReplicaSet list, whose test returned no ReplicaSet at all, and an operator in another namespace sharing the ServiceAccount's name. Each gained a test (c40004f).
  - One more was rewritten to compile.

## 19. Implementation revision 2: Codex's re-review of 882e9a1

Two P2 findings under the original finding 6; the other nine were resolved. The probes are kept as tests (`internal/fleet/keda_review2_test.go`). The fixes are in f613489.

| # | Finding | Fix |
|---|---|---|
| 1 | The workload half of the inverse join bypassed the forward join's proof and cut semantics | An `aws` `identityOwner: workload` link now holds only through a ScaledObject in the namespace whose handler is proven and sees the authentication (`kedaHandling.sees`, the same proof as the operator half). No handler, or an authentication the handler's selector excludes, links nothing. An unverified handler or visibility, a cut trigger list, or an operator inventory not read makes the read partial |
| 2 | An unread operator inventory was treated as proof that no operator runs in the namespace | `kedaOperatorIn` is three-valued. An inventory the capability could not read is unknown, never "no operator here", and the read is partial |

**Also:**
- The operator candidates are read once per inverse join and shared by both halves.
- Three edges gained tests: no operator read proves nothing, no handler is a proven absence (no link, complete), and a ScaledObject in another namespace never lends this namespace's ServiceAccount.

**Gates on f613489:**
- **Codex's probes:** all four pass through the overlay under `-race`.
- **The native smoke:** 193 of 193 in each of night and day at 1440 and 1024.
- **Go:** `go test -race` passes for `fleet`, `appbridge` and `identity`; golangci-lint reports 0 issues.
- **Mutations:** 8 on this revision's code, all caught.
  - Three survived the first pass: the no-read guard, the no-handler absence, and another namespace's ScaledObject.
  - Each gained a test.
- **Not rerun:** parity, the captures and the frontend suite, for a revision that changes only `internal/fleet/identity.go`.

**Implementation accepted at bcd6cdb** (Codex, revision 2, handoff e9c044c6, 2026-10-20), with no remaining findings: all ten findings of the first review are closed. Two choices remain the owner's (§16): the popover-only scale for a workload proven unowned on an unprotected cluster, and `TooManyReplicas` as a capacity fact without attention.
