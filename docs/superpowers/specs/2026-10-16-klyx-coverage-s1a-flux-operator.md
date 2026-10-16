# Coverage S1a: the Flux Operator

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S1a.

The owner's direction: "flux should be fully supported, flux operator + flux image automation". This slice is the operator. S1b (image automation) and S1c (source composition and notification facts) follow, each with its own note.

## 1. The API

**Sources:** Flux Operator v0.61.0 (2026-10-01): its CRDs, `docs/api/v1/*.md`, and `api/v1` and `internal/controller` sources. The current Flux is v2.9.6. All four kinds are `fluxcd.controlplane.io/v1` (the only served version, also storage) and namespaced, and their conditions carry `observedGeneration`.

**FluxInstance:**
- **Named `flux`**: CEL requires `metadata.name == "flux"`. CEL does not constrain the namespace. The operator restricts its FluxInstance and FluxReport cache to its own runtime namespace (`cmd/operator/main.go`), so a `flux` in another namespace exists but is not reconciled (review 1).
- **spec:**
  - `distribution`: `version` (a semver range such as `2.9.x`), `registry`, `variant` (upstream-alpine or an enterprise variant), and `artifact` (an `oci://` URL the operator polls).
  - `components`: the controllers to run, from seven.
  - `cluster`: type, size, multitenant (and its workload identity variants), networkPolicy, domain, tenant defaults.
  - `sync`: `kind` (GitRepository, OCIRepository or Bucket), url, ref, path, pullSecret, provider, interval, and `name`, which defaults to the namespace. Sync creates a source and a Kustomization of that name.
  - Also `kustomize.patches`, `storage`, `sharding` (which creates `<controller>-<shard>` Deployments), `wait`, `migrateResources` and `commonMetadata`.
- **status:**
  - `components[]{name, repository, tag, digest}` and `inventory.entries[]`;
  - `lastAppliedRevision` and `lastAttemptedRevision` (`v2.9.6@sha256:…`), `lastArtifactRevision`;
  - `history[]` (the five most recent).
- **Conditions:** Ready=True `ReconciliationSucceeded`; Ready=False `ArtifactFailed`, `BuildFailed`, `HealthCheckFailed` or `ReconciliationFailed`; Stalled `BuildFailed`; Reconciling while it runs.

**FluxReport:**
- Named `flux`, in the operator's namespace, and written by the operator.
- **Its spec is its content:**
  - `distribution`: version, status (Installed, "Not Installed" or Unknown), entitlement, managedBy;
  - `components[]{name, image, ready, status}`;
  - `reconcilers[]{apiVersion, kind, stats{failing, running, suspended, totalSize}}`;
  - `sync{id, path, source, ready, status}`;
  - `cluster{serverVersion, platform, nodes}` and `operator{apiVersion, version, platform}`.
- **Its Ready is set True after every run, even when the computation logged errors.** It means reporting succeeded, never that Flux is healthy (and, as below, not even that the report is recent).
- **Refresh:** at operator start, when the FluxInstance changes, on request, and every reporting interval. That is 5 minutes by default, but it is set by the operator's `--reporting-interval` flag or `REPORTING_INTERVAL` (the Helm chart sets it from values), so Klyx cannot know it.
- **The condition time is not a heartbeat.** The pinned conditions runtime (fluxcd/pkg `runtime` v0.111.0) keeps `lastTransitionTime` when status, reason and message are unchanged. The message carries a rounded duration that can repeat across runs. So a report can refresh without its condition time moving, and Ready=True persists when reporting is disabled.

**ResourceSet:**
- **spec:**
  - `inputs` (free-form) and `inputsFrom[]` (providers by name or selector), with `inputStrategy` (Flatten or Permute);
  - `resources`, or `resourcesTemplate` (a templated multi-document string), or `steps[]` (at most 20, ordered, each health-checked before the next, fail-fast);
  - `dependsOn[]{apiVersion, kind, name, namespace, ready, readyExpr}` (`readyExpr` is CEL over the referenced object);
  - `serviceAccountName`, `wait`.
- **status:** `inventory.entries[]`, `lastAppliedRevision`, `history[]`, `externalChecksumRefs[]`.
- **Conditions:**
  - Ready=False `DependencyNotReady`, `BuildFailed`, `ReconciliationFailed` or `HealthCheckFailed`; a failing step reads `step "<name>" health check failed`.
  - Reconciling `Applying step 2/3 "<name>"`.
  - Stalled `InvalidCELExpression` or `BuildFailed`.

**ResourceSetInputProvider:**
- **spec:**
  - `type`: 23 types (Static, Git hosts' branches, tags and change requests, GitLabEnvironment, OCI/ACR/ECR/GAR artifact tags, ExternalService, ExternalArtifact);
  - `url`, `filter`, `skip`, `defaultValues`, `selectors`, `secretRef`, `certSecretRef`, `serviceAccountName`;
  - `schedule[]{cron, timeZone, window}`.
- **status:** `exportedInputs[]`, `lastExportedRevision`, `nextSchedule{cron, timeZone, when, window}` (absent inside a window).
- **Conditions:** Ready=False `ReconciliationFailed`; Stalled `InvalidDefaultValues`, `InvalidSchedule` or `InvalidExportedInputs`.

**The shared reconcile controls:**
- `fluxcd.controlplane.io/reconcile: enabled|disabled` (case-insensitive) suspends.
  - While disabled the operator returns early and emits a `ReconciliationDisabled` Warning. **Its conditions are frozen, not updated.** Only at first initialisation does it set Ready=True with reason `ReconciliationDisabled`.
  - The operator's own CLI resumes by setting `enabled` together with `requestedAt`.
- `reconcile.fluxcd.io/requestedAt` triggers a run (echoed in `status.lastHandledReconcileAt`); `reconcile.fluxcd.io/forceAt` forces one (echoed in `lastHandledForceAt`).
- `reconcileEvery`, `reconcileTimeout` and `reconcileArtifactEvery` set the intervals. The defaults are 1h and 5m for FluxInstance and ResourceSet, with artifact polling every 10m, and 10m and 2m for providers.
- **A scheduled provider ignores a plain request.** An out-of-schedule run needs `requestedAt` **and** `forceAt` with the same value; otherwise it emits `SkippedDueToSchedule` and leaves its status unchanged.

**The operator's Deployment:**
- **The kustomize install:** `flux-operator` in `flux-system`.
- **The Helm chart** (`controlplaneio-fluxcd/charts`, `charts/flux-operator`): the Deployment is named after the Helm release (its fullname), so the name varies. Its selector label is `app.kubernetes.io/name: flux-operator` unless the chart's `nameOverride` is set; a `nameOverride` is unsupported.
- **The identity Klyx uses** is therefore the label `app.kubernetes.io/name: flux-operator`, never the name. The operator's runtime namespace is the namespace that Deployment runs in.
- **Ownership is not readiness.** The Flux components the operator installs carry `app.kubernetes.io/managed-by: flux-operator`, which says who manages them, not whether they are ready.

## 2. What Klyx shows

A new **Operator** view in the Flux lens (design principle 2), beside Reconciliation and Sources: the task "Flux Operator", with its registry entry and console words.

- **The FluxInstance:**
  - its verdict, its conditions, and its suspension (§4);
  - the declared distribution (version range, variant, registry) beside what runs (`status.components`: each controller's image tag and digest);
  - the last applied and attempted revisions;
  - its cluster settings: multitenant, network policy, size and sharding (with its shard Deployments named);
  - **its sync:** the kind, url, ref and path it declares, linked to the GitRepository/OCIRepository/Bucket and Kustomization it creates (named `spec.sync.name`, or the namespace), as rows in the Sources and Reconciliation views;
  - its history (the five most recent runs) and inventory, read on demand in the inspector (§3).
- **The FluxReport,** labelled "as reported by the operator", with two times kept apart: "condition last changed <lastTransitionTime>" and "read by Klyx <observation time>". It makes no claim of when the report was generated:
  - the per-kind reconciler stats (running, failing, suspended), with each kind's source sizes;
  - component readiness;
  - the sync's id, source and status;
  - the cluster and operator versions;
  - who manages the distribution.

  **These are the operator's numbers, not Klyx's.** Klyx's own counts of Flux objects are the Reconciliation and Sources views; where the two disagree, both are shown, each labelled with its source. The report never lights a task. Its Ready is shown as "reporting succeeded", never as Flux's health or as proof of a recent run.
- **ResourceSets, broken-first:**
  - Ready with its reason. A step failure names the step, and a step in progress reads "Applying step n/m".
  - `dependsOn` as named prerequisites, each linked when Klyx can resolve it. A `readyExpr` is shown as text.
  - `inputsFrom` with the providers it selects, and the input strategy.
  - The inventory, on demand, each entry linked to its live object through `resourceRefs.ts`, as the Flux inventory already is.
- **ResourceSetInputProviders:**
  - type and url;
  - each schedule (cron, time zone, window) and `nextSchedule`;
  - the number of exported input sets and `lastExportedRevision`;
  - the ResourceSets that consume them;
  - `secretRef` and `certSecretRef` named, never read.
- **Detection (review 1, finding 4):**
  - **Flux is present** when either the kustomize-controller group or `fluxcd.controlplane.io` is served.
  - **Operators are found by evidence** (revision 2). A bounded, cluster-wide list of Deployments labelled `app.kubernetes.io/name: flux-operator` (limit 10) gives the candidates.
    - **The runtime namespace is read from each candidate's manager container**, because the operator takes it from `RUNTIME_NAMESPACE`, not from its Deployment's metadata, and defaults to `flux-system`:
      - `valueFrom.fieldRef: metadata.namespace` (the chart's form) gives the Deployment's namespace;
      - a literal value gives that namespace;
      - an unset variable gives `flux-system`;
      - any other source (a ConfigMap or Secret reference) is unresolvable and reads "unverified".
    - **The controller-enabled mode is read from its arguments.** A container run with `--web-server-only` runs no reconcilers: it is shown as the operator's web server, never as an operator, and binds no instance.
    - **Failures:** a forbidden or failed list is "unverified", never absent.
    - **Absence needs a complete scan.** A list that returns a continue token (more than 10 candidates) is incomplete, so no namespace is declared orphaned and nothing is counted as absent.
    - **Several reconciling operators with the same runtime namespace** are an ambiguity: shown, never resolved by picking one, and their namespace's instance reads "ambiguous operator".
  - **Instances are bound by namespace:**
    - a FluxInstance or FluxReport `flux` in an operator's namespace is that operator's;
    - a `flux` in a namespace with no operator reads "no operator reconciles this namespace" and is not counted as a reconciled instance;
    - an operator with no FluxInstance in its namespace reads "the operator is installed; no FluxInstance".
  - **Flux's own controller probe moves with the instance.** With an operator-managed instance, kustomize-controller is probed in that namespace, not the fixed `flux-system`.
    - A sharded instance lists its shard Deployments (`<controller>-<shard>`), and readiness is read per Deployment.
    - Without an operator, the existing probe is unchanged.

## 3. The source contract (roadmap C1)

| Part | Acquisition | Projection | Counted entity | Health | Notifications |
|---|---|---|---|---|---|
| `fluxinstance` (new, task `flux`) | observer watch `fluxinstances` | built field by field into a new object (no deep copy then delete): conditions (messages clipped at 1 KiB), generation, the reconcile annotations and `requestedAt`, `lastHandledReconcileAt`, the newest `history[].lastReconciled`, `distribution`, `components` (at most 20), `sync` (URL sanitised by S0b), and the revisions | the FluxInstance UID, counted only in an operator's namespace | §4 | eligible, like Kustomizations |
| `resourcesets` (new, task `flux`) | observer watches `resourcesets` and `resourcesetinputproviders` | built the same way: conditions (clipped), generation, the reconcile annotations and `requestedAt`, `lastHandledReconcileAt`, the newest `history[].lastReconciled`, `dependsOn` (at most 50, without `readyExpr`), `inputsFrom` (at most 50), the step names (at most 20); for providers, `type`, `url` (sanitised), `schedule` (at most 10), `nextSchedule`, `lastExportedRevision`, `lastHandledReconcileAt`, and the count of `exportedInputs` | the ResourceSet or provider UID | §4 | eligible |
| FluxReport | observer watch `fluxreports` | built field by field: `distribution`, `components` (at most 20), `reconcilers` (at most 50), `sync` (sanitised), `cluster`, `operator`, Ready's status and transition time | none: evidence, not health | none | none |

- **Versions:** `v1` only. A cluster serving an older operator API reads "unsupported operator version", never empty.
- **Clocks:** none of these parts change with time alone. `nextSchedule` is shown, not evaluated, and no report-age claim is made (§1).
- **The budget** (revision 2): an aggregate byte budget, enforced and checked, with caps that agree with it.
  - **Watches:** four new list/watches. A watch still *receives* whole objects (inputs, inventories, templates) before projection: "not retained in the projected model", never "not watched".
  - **Per object, checked:** each projection is measured, as its encoded size, after it is built, and must be at most **8 KiB**.
    - **Over the limit:** the display strings (condition messages, `dependsOn` and `inputsFrom` entries) are clipped further, down to 256 bytes each and then dropped, until it fits. Each clip is marked in the inspector.
    - **The classification facts** a rule needs (condition type, status and reason; generation and `observedGeneration`; the reconcile annotations and tokens of §4) are never clipped: at most about 1 KiB together. A projection whose required facts alone exceed 8 KiB is not retained, and its kind's coverage reads "capped".
  - **Per cluster, admitted:** these four kinds together retain at most **4 MiB** of projections per cluster.
    - **Admission is deterministic:** by namespace, then name. Past the budget, further objects are not retained.
    - **Past the budget,** the kind's coverage reads "capped", counts are lower bounds, and a consumer or prerequisite lookup says it is incomplete. A healthy chain is never claimed past the budget.
    - **The count follows from the bytes:** with the 8 KiB worst case, at least 512 objects are always admitted; typical 1-2 KiB projections admit 2,000 to 4,000.
  - **Fleet:** ten clusters reserve **40 MiB**, the per-cluster budget times ten, recorded in the roadmap's "As built".
  - **Calibration:** the budget counts encoded bytes. A benchmark measures actual heap (string backing storage included) for a full 4 MiB cluster, and the recorded ratio is applied to the reservation.
  - **Transient memory,** one list page decoded before projection, is measured separately, by a benchmark over ResourceSets each carrying a 64 KiB template. The page size follows the observer's existing reflector paging.
- **Bounded reads:** inventories and histories are read on demand (bounded: the first 500 inventory entries, the five history entries), with the omission said.
- **Tests beyond parity (C1):**
  - projection equality for every field a rule reads;
  - UID replacement (a FluxInstance deleted and recreated);
  - a frozen condition under a disabled reconcile, and each resume case of §4;
  - two report runs with identical Ready status, reason and message (the condition time does not move, and Klyx claims nothing from it);
  - a v1-less cluster;
  - the budget: ten clusters at their worst-case admission together; one oversized projection (clipped, then refused if its required facts alone exceed the limit); "capped" coverage and incomplete lookups past the budget;
  - detection: an operator in a custom namespace with no FluxInstance, an orphan `flux` in another namespace, `RUNTIME_NAMESPACE` mismatched with its Deployment's namespace, unset (defaulting to `flux-system`) and unresolvable, a `--web-server-only` Deployment, an incomplete list (a continue token), and two reconciling operators for one namespace.

## 4. Health rules

- **The existing rule** (`fluxRole`, `readyBacked`) applies: Ready True is healthy; Ready False is a fault with its reason; Reconciling is checking; Stalled is a fault that will not retry; a generation lag reads Syncing.
- **Suspension comes first.** `fluxcd.controlplane.io/reconcile: disabled` makes the object **Suspended**, an intended paused state (ratified 2026-07-02), never counted broken-first.
  - Because the operator freezes conditions while disabled, a frozen Ready (True or False) is shown as "last known before suspension", never as a live verdict.
- **Resume (revision 2).** Resume evidence rests on identity and observed change, never on comparing timestamps.
  - **Why timestamps cannot work:**
    - Request tokens are opaque correlation ids.
    - `metav1.Time` loses fractional seconds, so a run that finishes in the same second as a request never compares later.
    - Controller and Klyx clocks can skew.
  - **Why a handled request is not proof:**
    - The operator's finalizer copies `requestedAt` into `lastHandledReconcileAt` even on an early return (outside a provider's schedule, or while disabled).
    - Annotation edits do not advance `metadata.generation`.
    - Some failures (`DependencyNotReady`, `InvalidCELExpression`, a FluxInstance fetch or preflight failure) update conditions without touching history.
  - **The resume epoch:**
    - It begins when Klyx observes, through its own watch, an object's reconcile annotation change from disabled to enabled, or when Klyx's own resume verb succeeds.
    - Klyx records the object's UID, the moment it observed the change, and the request token current at that moment.
    - Only a token **first observed within the epoch** counts as evidence: Klyx's own resume writes a fresh token atomically with `enabled`. A token left over from before the suspension never certifies anything, however its value compares.
  - **The evidence of an actual evaluation, per controller path:**
    - **FluxInstance and ResourceSet** (no schedule gating once enabled): `lastHandledReconcileAt` observed changing to an epoch token. With the reconcile enabled the reconciler runs past the disabled check, and every path after it, a `DependencyNotReady` or CEL failure included, is an evaluation. Also an observed change in any condition (type, status, reason, message or `observedGeneration`), or an increase in the summed `history[].totalReconciliations`, within the epoch.
    - **A provider without a schedule:** `lastHandledReconcileAt` observed changing to an epoch token. There is no early return when enabled and unscheduled.
    - **A provider with a schedule:** a skipped run advances `nextSchedule` and copies the token without fetching, so neither is evidence. The evidence is:
      - `lastHandledForceAt` observed changing to an epoch token, which only the "Fetch now, outside its schedule" verb sets (paired with `requestedAt`); or
      - an observed change in `lastExportedRevision` or in a condition within the epoch.

      Until then the provider reads "Resumed · not yet evaluated: a scheduled run leaves no proof unless its inputs change; Fetch now to evaluate it". The verb is offered right there.
  - **While awaiting evidence:**
    - the object reads **"Resumed · awaiting evaluation"**;
    - its role is checking, and it is not counted;
    - its last condition is shown as "last known before suspension";
    - it raises no fault or recovery notification from a frozen condition. When evidence arrives the object is evaluated normally, and a fault's onset is then.
  - **When Klyx did not see the resume** (it was not running, or reconnected after it): there is no epoch, and the object is treated like any freshly observed object, with its conditions current. Klyx never infers a resume it did not see, and never certifies one from a token it cannot place in an epoch.

- **ResourceSet reasons** keep the operator's words: `DependencyNotReady` names what it waits for; a failed step names the step; `InvalidCELExpression` names the `dependsOn` entry where the message does.
- **A provider's failure** is its own fault. A ResourceSet whose provider failed reports whatever the operator records for it, and Klyx links the provider rather than re-deriving the failure.
- **FluxReport** lights nothing (§2).

## 5. Verbs (roadmap C4)

| Kind | Reconcile | Suspend / resume |
|---|---|---|
| FluxInstance | `requestedAt` | `fluxcd.controlplane.io/reconcile: disabled`; resume sets `enabled` and `requestedAt`, as the operator's CLI does |
| ResourceSet | `requestedAt` | the same annotation |
| ResourceSetInputProvider | `requestedAt`. For a provider with a schedule, the verb is named **"Fetch now, outside its schedule"** and sets `requestedAt` and `forceAt` to the same value, as the operator requires; the confirmation says so | the same annotation |
| FluxReport | "Refresh the report": `requestedAt` | none |

- **Every verb carries:**
  - a UID precondition, with the annotations written in one atomic update; the verb refuses if the object was replaced since the user saw it;
  - a unique request token (an RFC 3339 time with nanoseconds);
  - ConfirmDialog, plus ProtectGuard on protected clusters.
- **Honest outcomes:**
  - a verb reports "requested", and only the evidence of §4 reports "completed";
  - a request made while an object is suspended is "requested; the object is suspended", never a completed reconciliation.
- **Ownership consequences, for every target:** any of the four kinds owned by a Flux Kustomization or HelmRelease (Flux ownership labels), or by an Argo Application (Argo tracking), says that an annotation set from Klyx may be removed by its owner on the next apply. The FluxInstance's own sync Kustomization is a Flux object, with the existing verbs.
- **The existing verb code** (`flux/actions.go` `ResourceForKind`, `gitopsactions.go` `fluxGroupForKind`, `gitopssource.go`) learns the four kinds and their annotation mechanics. Flux's `spec.suspend` is not assumed for them.
- **Not offered:** `forceAt` on its own; `fluxcd.controlplane.io/force`, `prune` or `recreateOnFailure`; and suspending a single generated child.

## 6. Sensitive data

All of it goes through S0b (`2026-10-16-klyx-coverage-s0b-embedded-payloads.md`), the preparatory slice Codex asked for in review 1. Its registry covers:
- ResourceSet `resources`, `steps`, templates and inputs;
- provider `defaultValues` and `exportedInputs`;
- FluxInstance patches;
- URLs in `sync.url`, provider `url` and the report's `sync.source`.

On top of S0b, this slice adds only:
- the projections of §3, which never carry those fields;
- the AI field allowlist and chat projection for these kinds: kind, name, conditions, counts and references, never payloads;
- in the inspector, the number of exported input sets and each set's ordinal (1, 2, 3...). Every exported value, `id` included, stays omitted per S0b (an ExternalService provider accepts any string as `id`).

S1a is built after S0b and depends on it.

## 7. Tests, lab and smoke

- **Unit:**
  - the parse and rule of each kind, including the frozen-condition suspension;
  - **resume (§4):**
    - a skipped scheduled run whose `nextSchedule` advances (not evidence);
    - a forced fetch that leaves `nextSchedule` unchanged (evidence by `lastHandledForceAt`);
    - a new failure without history (evidence by condition change);
    - completion in the same second as the request;
    - controller and Klyx clocks skewed;
    - an external resume keeping an old `requestedAt` (no certificate);
    - a resume Klyx did not observe (treated as current);
  - FluxReport parsing, and its "reporting succeeded" wording (never "fresh", never a report age);
  - the verb matrix: each kind's GVR and annotations, the provider's paired `requestedAt`/`forceAt`, and the resume pair;
  - detection, with operator present or absent, with Flux present without the operator, and with an unreadable probe;
  - S0b's behaviour on these kinds:
    - the omitted bytes never cross the bridge, and there is no reveal;
    - Describe follows S0b §13 (withheld while a payload is present);
    - the backend edit guard refuses;
  - a needle in an exported input's `id`, absent from the serialised inspector DTO and the AI and chat payloads.
- **Parity:**
  - the four CRDs on `kind-klyx` and in `klyx-parity.sh`;
  - fixtures: a Ready FluxInstance, a Ready ResourceSet, a ResourceSet failing `DependencyNotReady`, a provider failing `ReconciliationFailed`, and a suspended ResourceSet with a frozen Ready=False;
  - a `flux-operator` stand-in;
  - the oracle's parts (`fluxinstance`, `resourcesets`) and the frontend `ROW_PARTS`.
- **Lab (`kind-klyx`):**
  - the v0.61.0 CRDs;
  - the FluxInstance `flux` in `flux-system`, a FluxReport, the ResourceSets and providers above (with status written through the status subresource);
  - a ResourceSet embedding a Secret, to prove the masking live.
- **Native smoke:** "Flux Operator" opens the Operator view, the FluxInstance card draws its verdict and components, and the report reads "as reported by the operator" with its two labelled times.
- **Live captures:** the Operator view, a ResourceSet inspector with masked inline Secret data, and a suspended ResourceSet, night and day, at 1440 and 1024.

## 8. Not in S1a

- **Image automation** is S1b. **ExternalArtifact and ArtifactGenerator, and the notification facts,** are S1c.
- **Per-child suspension** inside a ResourceSet's inventory (`reconcile: disabled` on a generated object) is not read in this slice. The inventory lists objects, not their annotations.
- **The operator's web UI and MCP server** are outside Klyx.

## 9. Revision 1: Codex's design review of 20c7290

| # | Finding | Where answered |
|---|---|---|
| 1 | The sensitive-data policy did not close the disclosure or masked-edit paths, and the hole is cross-cutting | the preparatory slice S0b; §6 now refers to it |
| 2 | A handled request is not proof of evaluation (the finalizer copies `requestedAt` on early returns) | §4's resume state and evidence per kind; §5's "requested" vs "completed" |
| 3 | FluxReport's condition time is not a heartbeat | §1 and §2: two labelled times, no report-age claim; the clock rule in §3 |
| 4 | The singleton rule does not establish the operator's namespace | §1's identity by label (the Helm chart checked); §2's evidence-based detection; the Flux probe follows the instance |
| 5 | The budget was an estimate, and understated what a watch reads | §3's caps, field-by-field projection, fleet reservation and transient benchmark |

## 10. Revision 2: Codex's re-review of ceee3bd

| # | Finding | Where answered |
|---|---|---|
| 1 | §6 kept an exported input's `id`, against the accepted S0b; §7 described a template collapse | §6: ordinals and a count only; §7: S0b's actual behaviour, and an `id` needle |
| 2 | The resume certificates admitted frozen evidence and suppressed genuine failures (a skipped run advances `nextSchedule`; failures without history; old tokens; same-second times; clock skew) | §4: the resume epoch, tokens first observed in it, evidence per controller path, no timestamp comparisons, "Fetch now" to evaluate a scheduled provider; §7: each case named |
| 3 | The caps did not add up to the 40 MiB reservation | §3: a checked 8 KiB per object with required facts protected, a 4 MiB admitted per-cluster budget with deterministic admission, 40 MiB for ten clusters, and heap calibration |
| 4 | A labelled Deployment's namespace is not the runtime namespace; `--web-server-only` runs no reconcilers | §2: `RUNTIME_NAMESPACE` read from the manager container (field ref, literal, unset default, unresolvable); web-server-only shown but never binding; absence only after a complete scan |
| P3 | "Report fresh" wording remained | §1 and §7 corrected |

