# Coverage S1a: the Flux Operator

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S1a.

The owner's direction: "flux should be fully supported, flux operator + flux image automation". This slice is the operator. S1b (image automation) and S1c (source composition and notification facts) follow, each with its own note.

## 1. The API

**Sources:** Flux Operator v0.61.0 (2026-10-01): its CRDs, `docs/api/v1/*.md`, and `api/v1` and `internal/controller` sources. The current Flux is v2.9.6. All four kinds are `fluxcd.controlplane.io/v1` (the only served version, also storage) and namespaced, and their conditions carry `observedGeneration`.

**FluxInstance:**
- A **singleton**: CEL requires `metadata.name == "flux"`, in the operator's namespace.
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
- A singleton named `flux` that the operator writes.
- **Its spec is its content:**
  - `distribution`: version, status (Installed, "Not Installed" or Unknown), entitlement, managedBy;
  - `components[]{name, image, ready, status}`;
  - `reconcilers[]{apiVersion, kind, stats{failing, running, suspended, totalSize}}`;
  - `sync{id, path, source, ready, status}`;
  - `cluster{serverVersion, platform, nodes}` and `operator{apiVersion, version, platform}`.
- **Its Ready is set True after every run, even when the computation logged errors.** It means the report is fresh, never that Flux is healthy.
- **Refresh:** at operator start, when the FluxInstance changes, on request, and every 5 minutes by default.

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

**The operator's Deployment:** `flux-operator` in the operator's namespace (`flux-system` in the kustomize install), labelled `app.kubernetes.io/name: flux-operator`. The Helm chart's names are unverified. The Flux components it installs carry `app.kubernetes.io/managed-by: flux-operator`.

## 2. What Klyx shows

A new **Operator** view in the Flux lens (design principle 2), beside Reconciliation and Sources: the task "Flux Operator", with its registry entry and console words.

- **The FluxInstance:**
  - its verdict, its conditions, and its suspension (§4);
  - the declared distribution (version range, variant, registry) beside what runs (`status.components`: each controller's image tag and digest);
  - the last applied and attempted revisions;
  - its cluster settings: multitenant, network policy, size and sharding (with its shard Deployments named);
  - **its sync:** the kind, url, ref and path it declares, linked to the GitRepository/OCIRepository/Bucket and Kustomization it creates (named `spec.sync.name`, or the namespace), as rows in the Sources and Reconciliation views;
  - its history (the five most recent runs) and inventory, read on demand in the inspector (§3).
- **The FluxReport,** labelled "as reported by the operator · <time>" (the time its Ready last transitioned):
  - the per-kind reconciler stats (running, failing, suspended), with each kind's source sizes;
  - component readiness;
  - the sync's id, source and status;
  - the cluster and operator versions;
  - who manages the distribution.

  **These are the operator's numbers, not Klyx's.** Klyx's own counts of Flux objects are the Reconciliation and Sources views; where the two disagree, both are shown, each labelled with its source. The report never lights a task, and its Ready is described as "report fresh".
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
- **Detection:**
  - Flux is present when either the kustomize-controller group or `fluxcd.controlplane.io` is served.
  - The operator's controller probe looks for the Deployment `flux-operator` in the FluxInstance's namespace (the operator's, by the singleton rule), or `flux-system` when there is no FluxInstance, and accepts the `app.kubernetes.io/name: flux-operator` label as the identity.
  - An unreadable probe is "unverified", never absent (roadmap C2's evidence rule).
  - An operator with no FluxInstance says "the operator is installed; no FluxInstance".
  - Flux installed by the operator is recognised by its components' `managed-by: flux-operator` label, and Flux's own controller probe (kustomize-controller) is unchanged.

## 3. The source contract (roadmap C1)

| Part | Acquisition | Projection | Counted entity | Health | Notifications |
|---|---|---|---|---|---|
| `fluxinstance` (new, task `flux`) | observer watch `fluxinstances` | keeps conditions (with `observedGeneration`), generation, reconcile annotations, `distribution`, `components`, `sync`, the revisions; drops `inventory`, `history`, `kustomize.patches` (read on demand) | the FluxInstance UID | §4 | eligible, like Kustomizations |
| `resourcesets` (new, task `flux`) | observer watches `resourcesets` and `resourcesetinputproviders` | keeps conditions, generation, reconcile annotations, `dependsOn` (without `readyExpr`), `inputsFrom`, provider `type`/`url`/`schedule`, `nextSchedule`, `lastExportedRevision`, and the count of `exportedInputs`; drops inputs, resources, templates, steps' content (names and count kept), inventory, history, exported values | the ResourceSet or provider UID | §4 | eligible |
| FluxReport | observer watch `fluxreports` | the whole spec (small) and Ready's transition time | none: evidence, not health | none | none |

- **Versions:** `v1` only. A cluster serving an older operator API reads "unsupported operator version", never empty.
- **Clocks:** none of these parts change with time alone. `nextSchedule` is shown, not evaluated. A FluxReport older than three reporting intervals reads "report not refreshed since <time>", as evidence, not a fault.
- **The budget per cluster:**
  - four new watches;
  - FluxInstance and FluxReport are singletons;
  - ResourceSets and providers are expected in tens to hundreds, projected to about 1 KiB each;
  - inventories, histories, inputs and templates are never watched.
- **Tests beyond parity (C1):**
  - projection equality for every field a rule reads;
  - UID replacement (a FluxInstance deleted and recreated);
  - a frozen condition under a disabled reconcile;
  - a v1-less cluster.

## 4. Health rules

- **The existing rule** (`fluxRole`, `readyBacked`) applies: Ready True is healthy; Ready False is a fault with its reason; Reconciling is checking; Stalled is a fault that will not retry; a generation lag reads Syncing.
- **Suspension comes first.** `fluxcd.controlplane.io/reconcile: disabled` makes the object **Suspended**, an intended paused state (ratified 2026-07-02), never counted broken-first.
  - Because the operator freezes conditions while disabled, a frozen Ready=False is shown as "last known before suspension", not as a live fault.
  - On resume, the verdict waits for the operator's next condition: Syncing until `observedGeneration` and `lastHandledReconcileAt` catch up.
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
  - a UID precondition: the verb refuses if the object was replaced since the user saw it;
  - ConfirmDialog, plus ProtectGuard on protected clusters.
- **Ownership consequences:**
  - A FluxInstance or ResourceSet applied by a Flux Kustomization (Flux ownership labels) says that a suspension set from Klyx is an annotation its owner may remove on the next apply.
  - The FluxInstance's own sync Kustomization is a Flux object, with the existing verbs.
- **The existing verb code** (`flux/actions.go` `ResourceForKind`, `gitopsactions.go` `fluxGroupForKind`, `gitopssource.go`) learns the four kinds and their annotation mechanics. Flux's `spec.suspend` is not assumed for them.
- **Not offered:** `forceAt` on its own; `fluxcd.controlplane.io/force`, `prune` or `recreateOnFailure`; and suspending a single generated child.

## 6. Sensitive data

- **A ResourceSet's `resources`, `steps[].resources` and `resourcesTemplate` can inline Secret data.** Its `inputs` are free-form, `readyExpr` can read a Secret in the namespace, and `copyFrom` copies Secrets.
  - Klyx's Secret masking is keyed on `kind: Secret`, so the generic YAML view of a ResourceSet exposes inline Secret data **today**. This slice fixes that, for ResourceSets and FluxInstance patches:
    - **The Manifest and Describe views:** embedded objects of kind Secret have their `data` and `stringData` values masked, through the existing masking.
    - **Templates:** a `resourcesTemplate` is collapsed behind "Show template", with a note that it may contain secrets. FluxInstance `kustomize.patches` get the same treatment.
    - **The guarded YAML editor** refuses to open a ResourceSet whose spec embeds a Secret, and says why. Editing it would round-trip masked values.
  - **Inputs:** the inspector shows inputs and exported inputs bounded (the first 20 sets, values clipped at 200 characters, omissions said), with values under credential-like keys masked.
- **Provider configuration:** a `secretRef` or `certSecretRef` is a name, never read. Change-request inputs carry author and title, and are shown, not sent anywhere.
- **The AI fence:** the AI field allowlist and the chat projection carry kind, name, conditions, counts and references for these kinds, and never inputs, exported inputs, resources, templates, patches or `readyExpr`.
- **FluxInstance:** `sync.url` is shown. A url carrying embedded credentials (`user:token@`) has them masked.

## 7. Tests, lab and smoke

- **Unit:**
  - the parse and rule of each kind, including the frozen-condition suspension and resume;
  - FluxReport parsing, and its "report fresh" wording;
  - the verb matrix: each kind's GVR and annotations, the provider's paired `requestedAt`/`forceAt`, and the resume pair;
  - detection, with operator present or absent, with Flux present without the operator, and with an unreadable probe;
  - masking of embedded Secrets in ResourceSet resources, steps and patches, the template collapse, and the editor's refusal;
  - the bounded, masked inputs.
- **Parity:**
  - the four CRDs on `kind-klyx` and in `klyx-parity.sh`;
  - fixtures: a Ready FluxInstance, a Ready ResourceSet, a ResourceSet failing `DependencyNotReady`, a provider failing `ReconciliationFailed`, and a suspended ResourceSet with a frozen Ready=False;
  - a `flux-operator` stand-in;
  - the oracle's parts (`fluxinstance`, `resourcesets`) and the frontend `ROW_PARTS`.
- **Lab (`kind-klyx`):**
  - the v0.61.0 CRDs;
  - the FluxInstance `flux` in `flux-system`, a FluxReport, the ResourceSets and providers above (with status written through the status subresource);
  - a ResourceSet embedding a Secret, to prove the masking live.
- **Native smoke:** "Flux Operator" opens the Operator view, the FluxInstance card draws its verdict and components, and the report reads "as reported by the operator".
- **Live captures:** the Operator view, a ResourceSet inspector with masked inline Secret data, and a suspended ResourceSet, night and day, at 1440 and 1024.

## 8. Not in S1a

- **Image automation** is S1b. **ExternalArtifact and ArtifactGenerator, and the notification facts,** are S1c.
- **Per-child suspension** inside a ResourceSet's inventory (`reconcile: disabled` on a generated object) is not read in this slice. The inventory lists objects, not their annotations.
- **The operator's web UI and MCP server** are outside Klyx.
