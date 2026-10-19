# Coverage S9: KEDA and autoscaling ownership

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S9. It takes the KEDA authentication joins S5 deferred (`2026-10-18-klyx-coverage-s5-workload-identity.md`), and follows C3's shared-kind gate (S3a §2) for a new shared kind, HorizontalPodAutoscalers.

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
| Fallback | True `FallbackExists`; False `NoFallbackFound` (forced False without a valid fallback spec) | True: at least one trigger failed more than `failureThreshold` times and KEDA is feeding the HPA a synthetic metric so it lands on `fallback.replicas` |
| Paused | True `ScaledObjectPaused` for a full pause **and** for a directional one; False `ScaledObjectUnpaused`; initialised False since 2.18, Unknown before | which pause only the annotations say (§1.3) |
| HPAActive | since 2.20.2, appended lazily: True `HPAActive`; True `ScalingDisabled` (target at 0); False with the HPA's own reason passed through (`FailedGetExternalMetric`, `AmbiguousSelector`) or `HPAMetricsUnavailable` | the HPA's own ScalingActive, copied; it never changes Ready |

The lab proves the case S9 exists for (finding 5). A ScaledObject whose only trigger fails, with a fallback, reads Ready=True, Active=True and Fallback=True, with `status.health` `{numberOfFailures: 5, status: Failing}`. Its HPA reads ScalingActive=True, so **the HPA alone hides the failing trigger.**

### 1.3 Pause

| Annotation | Since | Effect |
|---|---|---|
| `autoscaling.keda.sh/paused-replicas: "<n>"` | 2.7 (presence pauses since 2.18) | pauses by presence, whatever `paused` says; KEDA deletes the HPA and scales the target to n; `status.pausedReplicaCount` |
| `autoscaling.keda.sh/paused` | 2.12 (ScaledObject) | parsed as a bool, an unparseable value counts as true; KEDA deletes the HPA and freezes the target at its current replicas |
| `autoscaling.keda.sh/paused-scale-in` / `paused-scale-out` | 2.18 | the HPA is kept, one scaling direction disabled; Paused=True with the same reason and message (finding 12) |

A ScaledJob honours `autoscaling.keda.sh/paused` only.

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
| 2.20.0 and 2.20.1 | Ready=False `HPAMetricsUnavailable` or `ScalingDegraded` means HPA trouble, not an invalid ScaledObject: read as the HPA fault (§3 rule 6) |
| before 2.18 | Paused initialised Unknown, read as not paused; `status.hpaName` absent after adoption (the join uses the controller reference anyway); `paused-replicas` paused only once `status.pausedReplicaCount` was set |
| before 2.16 | `status.health` may list metrics without a fallback; `triggersTypes`/`authenticationsTypes` absent (derived from spec) |
| before 2.19 | no `triggersActivity` |

The version comes from the operator Deployment's `app.kubernetes.io/version` label, or its image tag. When neither is readable, the conservative reading applies: no HPAActive means read the HPA, and Paused=Unknown means not paused.

## 2. The source contract (C1) and C3

| Part | Acquisition | Counted entity, identity | Dependencies and gaps | Clock | Notifications |
|---|---|---|---|---|---|
| `keda/scaledobjects` | observer watch, projected (§10), `keda.sh/v1alpha1`, where served | one ScaledObject: its UID | the HPAs (C3, below); TriggerAuthentications and ClusterTriggerAuthentications, for reference existence. An unread or failed dependency leaves its evidence "unverified", never a verdict from a gap | §3's two graces, from creation, re-evaluated through `Next` | yes |
| `keda/scaledjobs` | observer watch, projected | one ScaledJob: its UID | the authentications, as above. Jobs are read on demand in the inspector only (§6) | §5's grace | yes |
| `autoscaling/hpas` | the shared HPA kind (C3), consumer `hpas`, `autoscaling/v2` | one HPA **not** controlled by a ScaledObject: its UID. An HPA whose controller reference is a `keda.sh` ScaledObject is that ScaledObject's dependency, folded into its verdict, never counted twice (§4) | none | §4's grace | yes |
| `keda/authentications` | observer watches: `triggerauthentications`, `clustertriggerauthentications` | **not counted**: inventory rows (role only), never Needs you | the ScaledObjects and ScaledJobs that reference them | none | no |

**C3: HPAs become a shared observer kind.**
- **Two consumers:**
  - `hpas`, the standalone HPA part, whose demand is the core `autoscaling/v2` API (always yes on a supported cluster);
  - `keda`, whose demand is `scaledobjects` being served.
- **One projection** for both (§10).
- **No borrowed presence:** the `keda` consumer never reads "KEDA installed" from the HPA kind being open, and the HPA part never depends on KEDA.

**The KEDA controller task** (C2, with `ControllerTasks` and `ControllerIssues`):
- **The operator.** A Deployment labelled `app=keda-operator` (or `app.kubernetes.io/name=keda-operator`), in whatever namespace holds it. A readable, not-ready Deployment lights the task; an unreadable one is unknown.
- **The metrics adapter.** The APIService `v1beta1.external.metrics.k8s.io`:
  - absent: a fault, "no external metrics API: KEDA's HPAs cannot get their metrics";
  - `Available=False`: a fault, with its reason;
  - held by another adapter: a fault. KEDA's own is positive evidence: labelled `part-of=keda-operator`, or its Service lives in the operator's namespace. Any other APIService reads "held by <ns>/<service>, not KEDA's metrics server";
  - unreadable: unknown.
- **The admission webhooks.** A not-ready webhook Deployment, where one exists, is degraded: "admission checks are off: two autoscalers on one target are not refused".
- **Management** (C2). No managed-install evidence is verified (§1.7), so management reads "unknown" unless the slice gains a positive-evidence table. Faults are never suppressed.
- **No operator Deployment visible,** while ScaledObjects carry conditions: "controller health not directly observed", neither healthy nor failed.

**No synthetic evidence.**
- No observedGeneration or condition time is fabricated for KEDA objects. `NativeEval.Cond` stays nil for them, and age comes from Klyx's own observation (The Line §8.1).
- HPA conditions carry `lastTransitionTime`, and are cited as qualified evidence (`CondAt`).
- Phases and reasons are KEDA's own, verbatim.

## 3. The ScaledObject rule (first match is the headline; the inspector lists every finding)

The verdict kinds are the shared engine's (`health.Kind`). Each rule below gives the kind it produces and the label Klyx shows.

1. **No conditions.** The object was created less than 2 minutes ago: progressing, "waiting for KEDA". Past 2 minutes it is unknown, "KEDA has not reconciled this ScaledObject (the operator may be down, or this namespace outside its watch)". The controller task carries the operator's own state, and the object is not counted as a fault.
2. **A full pause.** Paused=True, with `paused-replicas` present, or `paused` parsing true: **paused**, "paused: held at n replicas" or "paused: frozen at the current replicas". It is never attention.
   - Paused=True with no pause annotation: progressing, "resuming" (the annotation was removed and KEDA has not reconciled yet).
   - A pause annotation with Paused not True: progressing, "pausing".
   - **A directional pause** (`paused-scale-in` or `-out` only) is not a pause verdict. The remaining rules apply, and the label adds "scale-in paused".
3. **Ready=False.** A fault, KEDA's reason and message verbatim. Klyx adds its own evidence where it is provable from complete reads:
   - **An authentication that does not exist.** `authenticationRef` names a TriggerAuthentication or ClusterTriggerAuthentication absent from a complete read of its kind. The headline is "TriggerAuthentication X does not exist", with KEDA's message beneath. KEDA's own message never names it (§1.5).
   - **The 2.20.0 and 2.20.1 HPA reasons** (`HPAMetricsUnavailable`, `ScalingDegraded`) read as rule 6.
4. **Ready=Unknown with `PartialTriggerError`.** Degraded, "some triggers are failing", with the failing triggers named where `status.health` or the HPA's message names them. Any other Ready=Unknown, before the first poll, is progressing.
5. **Fallback=True.** Degraded: "falling back: <trigger> failed <n> times; KEDA holds <target> at fallback.replicas=<r>". The label names the HPA as "healthy only because of the fallback".
6. **The HPA cannot compute replicas.** A fault: "the HPA cannot compute replicas: <reason>: <message>". The evidence is:
   - HPAActive=False, where KEDA reports it;
   - otherwise the joined HPA's ScalingActive=False, any reason but `ScalingDisabled`, cited with its condition time.

   **`AmbiguousSelector` names the other autoscaler.** The HPAs scaling the same target (by group, kind and name in the namespace), each with its owner (a ScaledObject, or a plain HPA), are listed as "also scaling this target".
7. **The join.**
   - **The joined HPA** is the HPA whose controller ownerReference UID is this ScaledObject's UID. Never a name, and never the prefix.
   - **No such HPA.** The rule applies only when the object is not fully paused, Ready=True, and the HPA read is complete. Past 2 minutes from the ScaledObject's creation or its last unpause seen, it is a fault: "no HPA scales this target (KEDA reported ready)". Within that time, or with an incomplete HPA read, it is unknown, never a fault.
   - **`status.hpaName` names a different HPA** from the joined one: degraded, "status names HPA X; the HPA KEDA controls is Y".
8. **Otherwise ok**, in KEDA's vocabulary:
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

**Rules:**
1. **No conditions.** Less than 60 seconds since creation: progressing. After that: unknown, "the HPA controller has not reported".
2. **AbleToScale=False.** A fault, with the controller's reason and message: `FailedGetScale`, "deployments.apps "X" not found" (finding 14); `FailedUpdateScale`.
3. **ScalingActive=False.** Any reason except `ScalingDisabled` is a fault:
   - `FailedGetResourceMetric`, "unable to fetch metrics": no metrics server, finding 14;
   - `FailedGetExternalMetric`;
   - `AmbiguousSelector`, which names the other autoscalers, as in §3 rule 6.

   `ScalingDisabled` (the target at 0 replicas) is ok, "scaling disabled: target at 0".
4. **`generation > status.observedGeneration`.** Progressing, the ratified "Syncing…" mark. Never confirmed healthy while pending.
5. **Otherwise ok:** "n replicas (min a, max b, wants d)".
   - **`ScalingLimited=True` with `TooManyReplicas`** adds "at maximum, wants d". It is shown and never attention: a pegged HPA is a capacity signal, and lighting it would make peak hours noisy. **This is a stated choice for the owner's ruling.**
   - **`TooFewReplicas`** (at the minimum) is normal and not shown.

## 5. The ScaledJob rule

1. **No conditions:** as §3 rule 1.
2. **Paused.** `autoscaling.keda.sh/paused` true, with Paused=True: paused. The mismatches read "pausing" or "resuming", as in §3 rule 2.
3. **Ready=False:** a fault (`ScaledJobCheckFailed`, `TriggerError`, `KEDAJobCreateFailed`), with §3 rule 3's authentication evidence.
4. **Ready=Unknown `PartialTriggerError`:** degraded.
5. **Otherwise ok:** "active" or "idle". Fallback is ignored (never written for ScaledJobs).

**Jobs are not part of the verdict.** A Job's failure is the workload's, not KEDA's. The inspector lists them (§6).

## 6. Authentications, references and the S5 join

**References** are computed from the ScaledObjects' and ScaledJobs' specs: `triggers[].authenticationRef {name, kind}`, with kind defaulting to TriggerAuthentication. They are namespaced, so a ClusterTriggerAuthentication's users are listed with their namespaces. **The authentications' own `status.scaledobjects` strings are never used** (§1.5).

**The authentications rows** (`keda/authentications`) give, for each authentication:
- its source kinds, with names: Secret and key, env name, ConfigMap name, Vault address and method, Key Vault URI, provider and `identityOwner`;
- its users;
- for a ClusterTriggerAuthentication, the namespace its secrets are read from (§1.5):
  - the operator Deployment's literal `KEDA_CLUSTER_OBJECT_NAMESPACE` env value, else the operator's namespace;
  - a `valueFrom` env, or an unreadable Deployment: "unverified".

**On demand, in the inspector** (never in the observer):
- **Referenced Secrets and ConfigMaps:** a metadata-only GET, with the namespace per §1.5. It reads "exists", "not found" or "unverified (forbidden)". Values are never read.
- **The authenticating subject for `podIdentity`,** through S5's machinery (`internal/identity`). A new `TriggerAuthSubject` beside `StoreSubject`:
  - **`identityOwner: keda`** (default), and `azure-workload`/`gcp` whatever the owner: "authenticates as the KEDA operator", the operator Deployment's ServiceAccount. Its declared identity comes from `DeclaredOf`, and the operator pod's injected identity from `PodOf`. `roleArn` and `identityId` read "the operator assumes <role>" / "uses client <id>".
  - **`aws` with `identityOwner: workload`:** per user, the target workload's pod template ServiceAccount (else `default`):
    - **Its `eks.amazonaws.com/role-arn`** reads "the KEDA operator assumes <role> (from ServiceAccount X)".
    - **No annotation:** "KEDA requires eks.amazonaws.com/role-arn on ServiceAccount X; it has none, so this scaler cannot authenticate (an EKS Pod Identity association is not read by KEDA)". It is shown on the ScaledObject's inspector beside its Ready=False.
  - `identityOwner: workload` on `azure-workload` or `gcp` reads "ignored by this provider".
  - **Cloud trust and permissions stay unverified** (S5).
- **S5's ServiceAccount report** gains "TriggerAuthentications that authenticate as this ServiceAccount": for the operator's ServiceAccount, the operator-owned ones; for a workload's ServiceAccount, the `aws`/workload ones whose users target workloads running as it. This lands the join S5 deferred.

**ScaledJob Jobs:**
- Jobs in the namespace labelled `scaledjob.keda.sh/name=<sj>`, kept only when their controller ownerReference UID is the ScaledJob's (the label alone is not proof).
- The newest 20, with each one's outcome and age. The count beyond 20 is said.
- KEDA's own failed-Jobs history limit (default 100) is named.

**Recent KEDA Warning events** (`KEDAScalerFailed`, `ScaledObjectCheckFailed`, `KEDAJobCreateFailed`) come through the inspector's existing events read, UID-filtered.

## 7. Ownership: owner links, the scale target and the scale verb's warning

**Owner and target links** (the `kindResourceRef` fix).
- **What changes:**
  - `ownerRefs` keeps each reference's `apiVersion`;
  - the HPA's `scaleTargetRef` is read with its own `apiVersion`;
  - each reference is resolved through discovery (`resolveGVK`, `fleet/adhoc.go`, TTL-cached) to group, version and plural.
- **The core switch remains** only as the fallback when discovery is unavailable and the `apiVersion` matches the core kind's group.
- **An unresolvable kind** (an unserved CRD, or a malformed `apiVersion`) renders as plain text, never a guessed plural.
- **The result:**
  - an HPA links to its ScaledObject;
  - a Job links to its ScaledJob;
  - an HPA links to an Argo Rollout or any other scalable CRD.
- **The generic inspector's owner link** matches by group, kind and name, not kind and name alone.
- **The HPA detail's target link** opens the target object itself (`InstanceDetail.tsx` today only switches to the workloads section).

**Who owns the replica count** is a join over the HPA and ScaledObject models, as of their last update, and said so:
- **an HPA** whose `scaleTargetRef` (group, kind, name) is the workload, named with its owner. A KEDA-owned one reads "KEDA ScaledObject X, through HPA Y";
- **a ScaledObject** targeting it, including a fully paused one (paused ScaledObjects have no HPA): "KEDA ScaledObject X is paused and will resume control";
- **GitOps,** from the existing ownership facts:
  - **Flux:** `workloads.extractOwner` and `GitOpsOwnerFromLabels`;
  - **Argo:** its tracking label.

  The line reads "if Git sets replicas, <Flux|Argo> sets it back on its next reconcile". Klyx cannot see whether the manifest sets replicas.

**The workloads inspector** shows a "Replicas owned by" line from the same join.

**The scale verb** (C4). The existing `ScaleWorkload` (Deployments and StatefulSets, with a UID precondition) gains:
- **An owned workload:** the popover names the owner, and its action reads "Scale anyway". The ConfirmDialog, required for an owned workload on every cluster, states the consequence:
  - **HPA:** "the HPA sets replicas back within its sync period (about 15 s)";
  - **ScaledObject:** "pause the ScaledObject instead to hold a count";
  - **GitOps:** the line above.
- **An unowned workload on an unprotected cluster** keeps today's behaviour: the popover, with an explicit count and an explicit Scale action, is its confirmation. This predates C4. **It is stated here for the owner's ruling, not silently kept.**
- **Protected clusters** keep the typed confirmation (ProtectGuard).

## 8. Verbs (C4)

| Kind | Pause / Resume | Otherwise |
|---|---|---|
| ScaledObject | **Pause:** merge-patch `autoscaling.keda.sh/paused: "true"`. **Resume:** remove `autoscaling.keda.sh/paused`, offered only when it is the object's only pause annotation | `paused-replicas`, `paused-scale-in`/`-out` and `force-activation` are never set or removed by Klyx. An object paused by them reads "paused by <annotation>, set outside Klyx". `paused-replicas` is a different operation, not offered (C4) |
| ScaledJob | the same, `autoscaling.keda.sh/paused` only | - |
| TriggerAuthentication, ClusterTriggerAuthentication | none | - |
| HorizontalPodAutoscaler | none | editing min, max or metrics is authoring |
| Deployment, StatefulSet | - | the existing scale verb, with §7's warning |

**Every verb carries:**
- the UID precondition (`guardUID`, `fleet/gitopsactions.go`): the patch acts on the object the user saw, or refuses;
- ConfirmDialog, plus ProtectGuard on protected clusters;
- a unique request token, as S1a's verbs do.

**The consequence copy:**
- **Pause:** "KEDA deletes its HPA (<name>) and holds <target> at its current replicas until resumed".
- **Resume:** "KEDA recreates its HPA and resumes scaling <target>".
- **GitOps:** the existing `ownershipLine(owner, "annotation")`. If Git declares the annotation, the controller restores Git's value on its next reconcile.

**Honest outcomes.**
- The verb says "pause requested".
- Completion is the object's own Paused condition, as of its next read, never assumed.

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

**Needs you, the mast, the fleet spine.** The counted parts join the shared engine. A fault reads "ScaledObject team/web: falling back".

**AI and chat.**
- **AI allowlist fields:** names, the target, conditions (type, status, reason, clipped message), `hpaName`, the pause kind, trigger types and metric keys, authentication names and source kinds, and the subject's kind.
- **Never sent:** trigger metadata, authentication source details, and env or Secret names beyond the reference.
- **Chat's GVR-to-task map** gains `keda.sh/scaledobjects`, `keda.sh/scaledjobs` and `autoscaling/horizontalpodautoscalers`.
- **The notification kind labels** gain `scaledobjects`, `scaledjobs` and `horizontalpodautoscalers` (UID objects, eligible; notifications stay off by default).

## 10. Projections, budget and sensitive data

**ScaledObject projection:**
- namespace, name, UID, generation, creation time;
- the pause and transfer annotations only (an allowlist of keys);
- `scaleTargetRef` {apiVersion, kind, name}, `horizontalPodAutoscalerConfig.name`, min, max and idle, the fallback {failureThreshold, replicas, behavior}, and whether `scalingModifiers` is present;
- triggers, up to 16, with the count said: type, name, metricType, `authenticationRef` {name, kind} and `useCachedMetrics`. **Never `metadata`.**
- status:
  - conditions (type, status, reason, and the message clipped under the URL rule);
  - `hpaName`, `scaleTargetGVKR` and `pausedReplicaCount`;
  - `health` and `triggersActivity`, up to 16 keys;
  - `lastActiveTime`.

**ScaledJob projection:** the same where it applies, plus the history limits. **The `jobTargetRef` pod spec is never retained.**

**HPA projection:**
- namespace, name, UID, generation, observedGeneration and creation time;
- the controller ownerReference {apiVersion, kind, name, uid};
- `scaleTargetRef`, min and max, current and desired replicas, and `lastScaleTime`;
- the metric count and types;
- conditions (type, status, reason, clipped message, `lastTransitionTime`).

**Authentication projection:**
- namespace (if any), name and UID;
- source kinds with reference names and keys;
- podIdentity {provider, identityOwner, roleArn present, identityId present};
- the Vault address and authentication method;
- **never `hashiCorpVault.credential.token`:** only the authentication method ("token") is kept.

**Budget:**
- **New watches per cluster:** one, HPAs (C3, always); plus four where KEDA is served.
- **Retained:** about 2 KB per ScaledObject, 1 KB per HPA and 0.5 KB per authentication. Clusters typically run tens to hundreds of each.
- **Measurement:** `SizeOf` per projection, with the load test rerun before and after, as S4 did.
- **Live sets:** the S9 part reads live HPAs and authentications from the model, never per render.

**Sensitive data:**
- **Masked everywhere** (`crd/sanitize.go`): `hashiCorpVault.credential.token`, in the generic YAML view, Describe and the edit surface, as Secret data is.
- **Trigger metadata** is configuration. It shows in the on-demand object read and YAML, and is never retained in the observer or sent to AI.
- **Controller messages** are clipped under the URL rule.

## 11. Tests, lab, parity, smoke

**Unit tests (`internal/keda`, pure):**
- every §3 rule, including:
  - each condition reason in §1.2;
  - all three pause annotations, alone and together (`paused-replicas` wins over `paused: "false"`), an unparseable `paused`, the "pausing" and "resuming" mismatches, and directional pauses;
  - the join by controller UID with a custom name, an adopted HPA, a `hpaName` mismatch, a missing HPA within and past the grace, and an incomplete HPA read;
  - a missing authentication with complete and incomplete reads;
  - `AmbiguousSelector` with KEDA's and a plain HPA;
- every §1.8 version shape as a fixture: 2.17 (no `hpaName` after adoption, no HPAActive, Paused Unknown), 2.20.0 (`HPAMetricsUnavailable`), and 2.15 (health without a fallback);
- §4's and §5's rules;
- the clock seams, with an injected clock: the no-conditions grace and the no-HPA grace.

**`internal/identity`:**
- `TriggerAuthSubject` for each provider and owner;
- the role-arn annotation, present and absent;
- `roleArn` with the operator's identity;
- `identityId`.

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
- a step that opens `failing` and asserts the "falling back" verdict with its HPA shown healthy only through the fallback;
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
