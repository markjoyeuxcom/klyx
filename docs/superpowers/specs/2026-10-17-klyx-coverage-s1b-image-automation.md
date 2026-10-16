# Coverage S1b: Flux image automation

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S1b. S1a (`2026-10-16-klyx-coverage-s1a-flux-operator.md`) is accepted at 421fb2e; this slice reuses its projection budget and admission (§3).

The owner's direction: "flux should be fully supported, flux operator + flux image automation". The roadmap's S1b asks for scan health and the latest image per policy, and for the automation's last push joined to the actual source and push repository and branch with its own timestamp. A push is not evidence that a workload applied it.

**Stage 0 fixes a defect in the existing Flux rule** that this slice would otherwise inherit (§4.1). It lands first, with its own tests.

## 1. The API

Read from the controllers' own sources at the versions Flux v2.9.6 installs: image-reflector-controller v1.2.5 and image-automation-controller v1.2.5. All three kinds are `image.toolkit.fluxcd.io`.

**Versions,** from the two controllers' changelogs:
- **`v1`** is served from Flux v2.7 (both controllers' 1.0.0, 2025-09-15), which removed `v1beta1`.
- **`v1beta2`** was served from Flux v2.0 for ImageRepository and ImagePolicy (reflector 0.25), and from Flux v2.3 for ImageUpdateAutomation (automation 0.38). Both controllers' 1.2.0 (2026-06-17, Flux v2.9) removed it, so Flux v2.9.6 serves `v1` only.
- **ImagePolicy's status changed shape inside `v1beta2`:**
  - before reflector 0.35 (Flux v2.6) the result was a string, `status.latestImage` (`name:tag`), with `observedPreviousImage`;
  - from 0.35, `status.latestRef` (`name`, `tag`, `digest`) and `observedPreviousRef`, with `latestImage` kept as a deprecated field until 1.0 removed it.
- **What Klyx reads:**
  - per kind, the newest served of `v1` and `v1beta2`, probed per resource as the other Flux kinds are;
  - for a policy, `latestRef` when present, else `latestImage` split at its last `:` after the final `/`;
  - a kind that serves neither (an ImageUpdateAutomation on Flux v2.2 or earlier serves only `v1beta1`) reads "unsupported image automation version", never empty. Flux supports its last three minor releases; v2.2 left support in 2024.

**ImageRepository** (reflector):
- **Spec:** `image`, `interval`, `timeout`, `provider` (generic, aws, azure, gcp), `serviceAccountName`, `secretRef`, `certSecretRef`, `proxySecretRef`, `exclusionList` (default `^.*\.sig$`), `insecure`, `accessFrom`, `suspend`.
- **Status:** `canonicalImageName`, `lastScanResult` (`revision` a checksum of the tags, `tagCount`, `scanTime`, `latestTags` at most ten), `observedExclusionList`, conditions, `observedGeneration`, `lastHandledReconcileAt`.
- **What the controller does:**
  - **Suspended:** it returns before reconciling; nothing in status changes.
  - **A scan** runs when there is no scan result, a reconcile request is new, the image name or exclusion list changed, the tag database is empty, or `interval` has passed since `scanTime`. Otherwise the run only requeues.
  - **`scanTime` is the controller's clock at the last successful scan.** A failed scan leaves the previous `lastScanResult` in place.
  - **Faults, by reason:** `ImageURLInvalid` and `FeatureGateDisabled` (Stalled); `AuthenticationFailed` (credentials could not be built); `ReadOperationFailed` (the registry read failed); `Failure` (the scan decision failed).

**ImagePolicy** (reflector):
- **Spec:** `imageRepositoryRef` (may name another namespace, subject to the repository's `accessFrom`), `policy` (one of `semver.range`, `alphabetical.order`, `numerical.order`), `filterTags` (`pattern`, `extract`), `digestReflectionPolicy` (Never, IfNotPresent, Always), `interval` (required with Always), `suspend` (from v1).
- **Status:** `latestRef` and `observedPreviousRef`, conditions, `observedGeneration`, `lastHandledReconcileAt` (from v1).
- **What the controller does:**
  - **It has no interval of its own** unless digests are reflected Always: it runs when its repository changes, so a quiet policy is not a stale one.
  - **A failure never clears `latestRef`.** The last resolved image stays in status while Ready is False.
  - **Faults, by reason:**
    - `DependencyNotReady`: the repository is not found, or its tag database is empty (requeued);
    - `AccessDenied`: the repository's `accessFrom` refuses this namespace;
    - `ImageURLInvalid`, `FeatureGateDisabled`, `InvalidPolicy` (a bad range or filter regex) and `IntervalNotConfigured` (Stalled);
    - `Failure`: anything else, including no tag matching the policy.

**ImageUpdateAutomation** (automation):
- **Spec:** `sourceRef` (a GitRepository, optionally in another namespace), `git.checkout.ref`, `git.commit` (author, `signingKey.secretRef`, `messageTemplate`, `messageTemplateValues`), `git.push` (`branch`, `refspec`, `options`), `interval`, `policySelector`, `update` (strategy `Setters`, `path`), `suspend`.
- **Status:** `lastAutomationRunTime`, `lastPushCommit`, `lastPushTime`, `observedPolicies` (policy name to image ref), `observedSourceRevision`, conditions, `observedGeneration`, `lastHandledReconcileAt`.
- **What the controller does:**
  - **The policies it uses** are every ImagePolicy in its own namespace that matches `policySelector` and has a `latestRef`, **whether or not that policy is Ready**. A failing policy's last resolved image still drives updates.
  - **`lastAutomationRunTime` is set when a run starts**, before any step can fail. It is the last attempt, not the last success.
  - **The push target:**
    - the branch is `git.push.branch`, else the checkout branch (`git.checkout.ref.branch`, else the GitRepository's `spec.ref.branch`);
    - a `git.push.refspec` pushes there as well;
    - with no branch to infer, the run fails `InvalidSourceConfiguration`.
  - **A push sets `lastPushCommit` and `lastPushTime`.** The time is the controller's clock just after the push, not the commit's own time. The branch is not recorded in status; it is in the controller's event ("pushed commit '…' to branch '…'").
  - **`observedSourceRevision` and `observedPolicies`** are written only on a successful run (no change, or a push). With a different push branch, `observedSourceRevision` is the checkout branch's commit, not the pushed one.
  - **Faults, by reason:**
    - Stalled: `InvalidPolicySelector`, `AccessDenied`, `InvalidSourceConfiguration`, `FeatureGateDisabled`, `InvalidUpdateStrategy`, `RemovedTemplateField`;
    - `SourceManagerFailed`, `GitOperationFailed` (checkout or push), `UpdateFailed` (applying the policies to the files).
- **Common to all three:** Ready, Reconciling and Stalled conditions in the Flux runtime convention; the `reconcile.fluxcd.io/requestedAt` annotation, with `lastHandledReconcileAt` written when a run handles it; and suspension by `spec.suspend` (the controller returns early, so conditions freeze).

## 2. What Klyx shows

An **Images** view in the Flux lens (design principle 2), beside Reconciliation, Sources and S1a's Operator: the task "Flux", a registry entry, and the console words "image", "image repository", "image policy", "image update", "image automation".

- **The list, broken-first,** with a kind filter (Repositories, Policies, Automations) and the shared status chips. Its key column per kind:
  - repository: the last scan's age and tag count;
  - policy: the resolved image (`name:tag`, digest abbreviated);
  - automation: the last push's commit and age.
- **The ImageRepository inspector:**
  - its verdict (InspectorStatus), conditions and events;
  - the image and its canonical name; the provider, `serviceAccountName`, and the Secrets it names (`secretRef`, `certSecretRef`, `proxySecretRef`), never read;
  - interval and timeout; "scanned <age> ago", the time being the controller's own clock at the last successful scan, with the absolute time on hover;
  - the tag count and the controller's ten `latestTags`, labelled **"the ten highest tags by reverse string order"**. They are not "latest" by any policy: the controller sorts them as strings;
  - the exclusion list, and whether the registry is insecure;
  - **the policies that use it,** found by their `imageRepositoryRef` across namespaces, each with its verdict.
- **The ImagePolicy inspector:**
  - **its headline is the bound repository's verdict** (the M10/M11 bound-source pattern): "Repository <ns>/<name>: Ready, scanned 3m ago", or its fault;
  - **the resolved image** (`name:tag@digest`) and the previous one. While the policy is not Ready, the image is labelled **"last resolved, before the failure"**, because the controller never clears it;
  - the policy (`semver` range, or `alphabetical`/`numerical` order), the tag filter (pattern and extract) and the digest reflection policy;
  - **the automations that use it** (same namespace, matching `policySelector`, and a resolved image), each with either "used in its last successful run" (`observedPolicies[name]` equals the resolved image) or **"pending: not yet in a successful run"**.
- **The ImageUpdateAutomation inspector:**
  - **its headline is the bound GitRepository's verdict;**
  - **the last push:**
    - the commit (`lastPushCommit`, abbreviated, with a copy affordance) and "pushed <age> ago", the time being the controller's clock just after the push;
    - the branch from the controller's own event ("pushed commit '…' to branch '…'") while that event exists. Otherwise the branch is shown as **"push target as configured now"**, since status does not record it;
  - **the push target as configured:** the repository URL from the GitRepository (URL rule), the branch, any refspec, and the number of push options;
  - the checkout ref, and **"last run started <age> ago"** (`lastAutomationRunTime`, set when a run begins, so it is an attempt, never a success);
  - `observedSourceRevision`, and the update strategy and path;
  - **the policies it uses now,** each with its resolved image, the image in the automation's last successful run, and "pending" when they differ;
  - the commit author, and the signing key's Secret named, never read;
  - events.
- **Push versus applied** (roadmap), shown under the last push, each line naming its own evidence:
  - **Source:** the GitRepository's artifact revision.
    - Its SHA equals `lastPushCommit`: "the source has fetched this commit".
    - Otherwise: "the source is at <revision>; whether it includes the push is not known here".
    - If the push branch differs from the branch the GitRepository tracks: "pushed to a branch this source does not track".
  - **Applied:** each Kustomization bound to that GitRepository, with its last applied revision.
    - An equal SHA reads "applied this commit".
    - Anything else reads "at another revision".
    - Workloads are not inferred from images, and nothing is called deployed.
  - The rows of The Line keep their own applied facts. This view adds none to them.

## 3. The source contract (roadmap C1)

| Part | Acquisition | Projection | Counted entity | Health | Notifications |
|---|---|---|---|---|---|
| `images` (new, task `flux`) | observer watches of `imagerepositories`, `imagepolicies` and `imageupdateautomations`, each at the newest served of `v1` and `v1beta2` | built field by field into a new object (below) | each object's UID | §4 | eligible, like Kustomizations; the two overdue states (§4.3) like ESO's Stale |

**Projected fields:**
- **Classification facts, never clipped** (all three kinds):
  - `spec.suspend` (ImagePolicy `v1` only);
  - generation and `status.observedGeneration`;
  - Ready's status, reason and own `observedGeneration`, and whether Reconciling and Stalled are present.

  Clock inputs: the repository's `spec.interval` and `lastScanResult.scanTime`; the automation's `spec.interval` and `lastAutomationRunTime`.
- **ImageRepository display:**
  - `image`, `canonicalImageName`, `provider`, `serviceAccountName`, the three Secret names, `interval`, `timeout`, `insecure`;
  - `exclusionList` (at most 10 entries of 256 bytes);
  - `lastScanResult.tagCount`, `revision` and `latestTags` (at most 10).
- **ImagePolicy display:**
  - `imageRepositoryRef`, the policy type with its range or order, `filterTags` (256 bytes each), `digestReflectionPolicy`, `interval`;
  - `latestRef` (or `latestImage`) and `observedPreviousRef` (or `observedPreviousImage`).
- **ImageUpdateAutomation display:**
  - `sourceRef`, `git.checkout.ref`, `git.push.branch` and `refspec`, the count of push options, the author's name and email, the signing key's Secret name and type, `update`;
  - `policySelector`, rendered as a selector string of at most 256 bytes;
  - `lastPushCommit`, `lastPushTime` and `observedSourceRevision`;
  - `observedPolicies` (at most 100 entries; past that the map is marked clipped and the "pending" join says it is incomplete).
- **Never projected:** `messageTemplate`, the values of `messageTemplateValues` and of push options (counts only), and `accessFrom` selectors (a count only).
- **Condition messages,** for display only, clipped at 1 KiB, and further as S1a §3 sets out.

**Versions and installations:**
- Each kind is probed per resource, as the other Flux kinds are. A partial upgrade, with one kind at `v1` and another at `v1beta2`, is normal.
- A kind that serves neither version reads "unsupported image automation version" (§1).
- CRDs installed without their controller is a state of its own (below).

**Controllers (C1's controller task):**
- `image-reflector-controller` and `image-automation-controller`, found by their `app.kubernetes.io/component` label in the Flux namespace that detection resolves: `flux-system`, or an operator's runtime namespace under S1a.
- **Their readiness feeds the Flux task** as kustomize-controller's does: not ready, or not found while their CRDs are served, is a controller issue.
- **An unreadable lookup is "unverified",** never absent.

**Dependency completeness:**
- **No verdict depends on another object.** Each kind is judged by its own status.
- **The joins** (a policy's repository, an automation's policies and GitRepository, the Kustomizations bound to it) read the shared observer rows. A missing object reads "not found", an unreadable one "not readable", and a lookup past the budget "incomplete". None is ever read as healthy.

**Clocks:** two deadlines (§4.3), each published as the part's next re-evaluation (`Next`), as ESO's staleness is.

**The budget,** by S1a §3's mechanism with its own allotment:
- three new list/watches;
- each projection is measured after it is built and must be at most **4 KiB**, with display strings clipped first and classification facts never;
- these three kinds together retain at most **2 MiB per cluster**, admitted by namespace then name. Past the budget, coverage reads "capped", counts are lower bounds, and joins say they are incomplete;
- ten clusters reserve **20 MiB**, recorded in the roadmap's "As built", with S1a's heap calibration.

## 4. Health rules

### 4.1 Stage 0: a failing current generation is a fault, not "Syncing…"

**The defect, in the existing code.** `fluxRole` (`internal/gitops/flux/rules.go`) checks generation lag before failure, so any object with `metadata.generation > status.observedGeneration` reads Progressing "Syncing…". Its Ready is never shown.

**Why that hides failures.** The controllers advance `status.observedGeneration` only on success:
- kustomize-controller v1.9.6 (`finalizeStatus`) does so only when Ready is True (or on its two stall paths);
- `fluxcd/pkg/runtime` `reconcile.AddPatchOptions`, which both image controllers use, does so only when Ready or Stalled.

So a spec change whose reconcile fails and retries keeps `status.observedGeneration` behind indefinitely. Meanwhile Ready=False is written for the **current** generation: `fluxcd/pkg/runtime/conditions.Set` stamps every condition with the object's generation. Klyx shows "Syncing…" for as long as the failure lasts.

**The rule:**
- **Pending:** generation is ahead of the Ready condition's own `observedGeneration`, or Ready is absent. The object reads "Syncing…" as today (Progressing, the ratified dim and grey chip).
- **A current failure:** Ready=False whose own `observedGeneration` equals the generation. It reads as the failure it is, a fault with its reason, even if `status.observedGeneration` lags. The row stays dimmed, because the spec is not realised; the chip is the failure, and its tooltip names the generation it applies to.
- **Never confirmed early:** Ready=True while `status.observedGeneration` lags still reads "Syncing…", as ratified.
- **Missing condition generation:** a Ready condition without `observedGeneration` (written by an older controller) keeps today's rule.

**Scope:** `ReconciliationRule` (Kustomizations, HelmReleases) and the three image kinds. `SourceRule` keeps no generation lag, as today. HelmRelease is affected less, because helm-controller v1.6.5 advances `observedGeneration` on most failures.

### 4.2 The image kinds

- **The shared rule applies,** with §4.1: Ready True is healthy; Ready False is a fault with its reason; Stalled is a fault that will not retry; Reconciling, or Ready Unknown with a progressing reason, is checking.
- **Suspension:** `spec.suspend` is **Suspended**, an intended paused state (ratified 2026-07-02), never counted broken-first.
- **Resume needs no evidence protocol.** Unlike S1a's annotation, clearing `spec.suspend` advances the generation. The frozen Ready keeps its old `observedGeneration`, so the object reads "Syncing…" until the controller writes Ready for the new generation, at the end of its first run (§4.1).
- **ImagePolicy reasons:**
  - `DependencyNotReady` names the repository it waits for, as a "blocked by ImageRepository <ns>/<name>" chip, as Flux `dependsOn` is shown today. It means the repository is missing or has no tags yet;
  - `AccessDenied` names the repository's `accessFrom` refusal;
  - `InvalidPolicy`, `ImageURLInvalid`, `IntervalNotConfigured` and `FeatureGateDisabled` are stalled;
  - a policy matching no tag fails with the controller's message.
- **ImageUpdateAutomation reasons** keep the controller's words. `GitOperationFailed` covers both checkout and push; its message says which.

### 4.3 Clocks

| State | Kind | Condition | Role |
|---|---|---|---|
| **Scan overdue** | ImageRepository | Ready True for the current generation, not suspended, and now ≥ `scanTime` + `interval` + max(`interval`, 5m) | Degraded, "Scan overdue", counted |
| **Run overdue** | ImageUpdateAutomation | Ready True for the current generation, not suspended, and now ≥ `lastAutomationRunTime` + `interval` + max(`interval`, 5m) | Degraded, "Run overdue", counted |

- **Scan overdue** means the controller has not completed a scan for over twice its interval while still reporting Ready.
- **Run overdue** means no run has even started: `lastAutomationRunTime` is set at the start of every run, so a stuck or absent controller is the cause.
- **Each deadline is the part's `Next`**, re-evaluated by the clock as ESO's staleness is. A deadline crossed is the role's start.
- **Clock skew:** the slack (at least five minutes, or one interval) absorbs ordinary skew between the controller's clock and Klyx's. A larger skew can raise an overdue state early. It is Degraded, never a fault, and its tooltip names both times.
- **ImagePolicy has no clock.** It runs when its repository changes, so a quiet policy is not a stale one.

## 5. Verbs (roadmap C4)

| Kind | Reconcile | Suspend / resume |
|---|---|---|
| ImageRepository | **"Scan now"**: `reconcile.fluxcd.io/requestedAt`. A new request makes the controller scan, whatever the interval | `spec.suspend` |
| ImagePolicy | `requestedAt`, on `v1` only. The `v1beta2` status has no `lastHandledReconcileAt`, so a request could never be confirmed | `spec.suspend`, on `v1` only; `v1beta2` has no such field |
| ImageUpdateAutomation | **"Run now"**: `requestedAt` | `spec.suspend` |

- **Every verb carries:**
  - the existing `guardUID` (the UID in the merge patch), so it acts on the object the user saw or refuses;
  - ConfirmDialog, plus ProtectGuard on protected clusters;
  - a unique request token.
- **The ownership consequence** (roadmap C4), shown by the shared Flux verb dialog for every Flux kind, the existing Kustomization and HelmRelease included. The survey found the existing dialogs show none, so this closes that gap too:
  - for an object owned by a Flux Kustomization or HelmRelease, or tracked by an Argo Application: "Owned by <owner>. If Git sets `spec.suspend` for it, the owner sets it back on its next apply";
  - an annotation reads: "may be removed when the owner applies".
- **Honest outcomes:** a verb reports "requested". Completion is the object's own status: a new `lastHandledReconcileAt` with Ready for the current generation.
- **The existing code learns the kinds:** `flux/actions.go` `ResourceForKind`, `gitopsactions.go` `fluxGroupForKind` and `gvrForKind`, and `gitopssource.go` `fluxGVR`. `TestFluxObjectUnsupportedKindIsAnErrorNotAbsence` changes its unsupported kind from ImagePolicy to one Klyx does not support.
- **Not offered:**
  - "reconcile with source" for an automation, which clones Git itself and never reads the GitRepository's artifact;
  - chained verbs: a policy's inspector links to its repository's "Scan now" instead.

## 6. Sensitive data

- **No credential values.** These kinds name Secrets (`secretRef`, `certSecretRef`, `proxySecretRef`, `signingKey.secretRef`) and Klyx shows the names only, never reading the Secrets.
- **The URL rule** (S0b) applies to every displayed string: condition and event messages, and the GitRepository URL in the push target.
- **Not projected or shown:**
  - push option values and `messageTemplateValues` values (counts only);
  - `messageTemplate`.

  Neither is a credential channel by design, but neither has diagnostic value, and both are free text.
- **The generic browser** shows these objects as ordinary custom resources, as kubectl does. They are not S0b payload kinds: they embed no manifests, values or templates.
- **AI facts:**
  - **allowlisted:** kind, verdict, reasons and messages (URL rule), images, tags and digests, policy type and range, times, and commit SHAs;
  - **excluded:** the author's email and the selector string.

## 7. Tests, lab and smoke

- **Stage 0 (§4.1):**
  - a Kustomization failing its current generation with `status.observedGeneration` behind: a fault with its reason, dimmed;
  - a pending generation with an old Ready: "Syncing…";
  - Ready True with `status.observedGeneration` behind: "Syncing…";
  - a Ready without `observedGeneration`: today's rule;
  - the existing staleness and Line tests still pass.
- **Unit:**
  - **parsing** of each kind at `v1`, at `v1beta2` with `latestRef`, and at `v1beta2` with only `latestImage`:
    - `registry.example:5000/team/app:1.2`, split after the last `/`;
    - a digest-pinned image;
  - **rules:**
    - each reason;
    - the blocked-by chip;
    - suspension;
    - resume by generation (Syncing until a current Ready);
    - both clocks with an injected clock, `Next`, and a skewed `scanTime`;
  - **joins:**
    - a policy's repository across namespaces;
    - an automation's policies by namespace, selector and resolved image, including a failing policy that still has an image;
    - the push target's precedence (push branch, checkout ref, the GitRepository's branch, none);
    - the source and applied lines (equal SHA, another revision, an untracked branch, a missing or unreadable GitRepository);
    - "pending" from `observedPolicies`, including a clipped map;
  - **verbs:** the matrix, `guardUID`, the `v1beta2` policy refusals, the protected-cluster grant, and the ownership line for a Flux-owned, an Argo-owned and an unowned object;
  - **C1 beyond parity:**
    - projection equality for every rule field;
    - baseline, continuous, after-gap and loss cases;
    - UID replacement;
    - the budget: an oversized projection clipped, then refused; "capped" coverage and incomplete joins;
    - a `v1beta2`-only discovery, and a `v1beta1`-only automation ("unsupported");
  - **detection:** controllers ready, not ready, absent with CRDs served, and unreadable;
  - **AI:** the allowlist, with a needle in the author's email and in a push option, absent from the AI and chat payloads.
- **Parity:**
  - the three CRDs on `kind-klyx` and in `klyx-parity.sh`;
  - **fixtures:** a ready repository, a repository failing `AuthenticationFailed`, a ready policy, a policy failing with a retained image, a blocked policy, an automation with a push, and a suspended automation;
  - stand-ins for both controllers;
  - the oracle's `images` part and the frontend `ROW_PARTS`.
- **Lab (`kind-klyx`):**
  - the `v1` CRDs from Flux v2.9.6's install manifests;
  - the parity fixtures, plus an overdue repository (an old `scanTime`);
  - a GitRepository and two Kustomizations, one at the pushed SHA and one at another revision;
  - status written through the status subresource. No registry or Git server is needed (C6).
- **Native smoke:**
  - "Images" opens the view, broken-first;
  - the policy inspector's headline is its repository;
  - the automation inspector shows the push with its source and applied lines;
  - Stage 0: a failing Kustomization fixture reads as a fault.
- **Live captures:** the Images view, a policy inspector, and an automation inspector with the push join, in night and day, at 1440 and 1024.

## 8. Not in S1b

- **Which workloads run a policy's image.** Matching by image name is a heuristic; the setter markers are in Git. It needs Git-side reading, a later slice if the owner wants it.
- **A preview of what the automation would change in Git.**
- **ExternalArtifact, ArtifactGenerator and the notification kinds** are S1c.
