# Klyx AI A1: deeper investigation

The AI programme's first phase (`2026-10-20-klyx-ai-investigations-roadmap.md` §2). An investigation is a chat conversation anchored on one failing object. It reads more than chat can today, and ends with structured findings whose evidence Klyx checks itself. A1 is read-only: it proposes a remedy as text and runs nothing. Action cards are A3.

It builds on cluster chat (`2026-10-14-klyx-cluster-chat.md`): the pinned connection, the turn loop and its fences, the projection, the read ledger and the snapshot mode for providers without tools. Everything chat guarantees holds unchanged. The programme contracts A-C1 to A-C5 apply as the roadmap states them.

## 0. What exists, read from the code

- **Chat** (`internal/appbridge/chat_*.go`):
  - one conversation per cluster, at most 8;
  - seven read tools declared in `ChatToolDefs`;
  - strict argument validation (`strictArgs` and the `check*` helpers);
  - one envelope per result, capped at 16 KiB through `finish` and `shrink`.
  - **The turn:** at most 6 model calls and 8 tool calls, within 120 s, with `guard()` fencing the pinned connection, `AIState.Rev` and egress before every read and model call.
  - **Snapshot mode:** for the Claude Code CLI and `openaiCompatible` without tools, one document of at most 48 KiB per question.
- **The read ledger** (`ChatRead`): it has no ID. Entries are positional, and answers cannot cite them. Reference chips are matched by name in the answer's text.
- **Evidence available, with its gaps:**
  - Flux status: `flux.Resource`, plus `ParseDetail`'s inventory, read live. There is no parser for `HealthCheckFailed` messages, and no backend join from inventory entries to live objects, their UIDs or verdicts.
  - Git:
    - the history cache: a partial clone, commits and trees;
    - `Repo.LogPath`, `DiffNames`, `Diff`;
    - `Store.AcquireDiff`, which fetches the changed blobs. It has no production caller.
  - The Flux cache diff (`cachediff`): a confined build at the source's artifact revision, then a server-side dry run against live. It is not a comparison of two revisions.
  - The dependency graph (`depgraph.Build`): depth 6, 24 nodes, start pinned by UID. Also the cert-manager chain, the Gateway topology, and KEDA's ScaledObject-to-HPA join in the Observer.
  - Metrics: `InstantVector` and `RangeSeries`, with per-pod CPU and memory templates. There is no restart PromQL, and no arbitrary-query path.
  - Alerts: the Observer's snapshot; `alertEvidence` matches by UID or by name.
  - Logs: chat's finite read through the kube API. Loki history (`LogHistoryService`) redacts only its endpoint's credential, so it is not AI-safe.
  - Rollout history (images per revision), fleet compare (`WorkloadCompare`, per cluster), and verdicts by UID through `HealthRowsNow`, `findRow` and `anchorVerdict`.

## 1. The investigation

- **Started by the operator** on one object: "Investigate" in an inspector's More menu, in ⌘K on an object, or under an Explain result. The object is the anchor, pinned by cluster, GVR, namespace, name and UID, like an Explain seed.
- **A conversation of its own kind,** beside chat:
  - at most one investigation per cluster, counted in chat's limit of 8;
  - the same lifetime as chat: in memory, no history on disk, fenced on disconnect and on a settings change;
  - follow-up questions continue it, as in chat.
- **The comparison set** (A-C2) is chosen when the investigation starts. The default is none; the operator can pick fleet members from the drawer before the first question. It is fixed for the investigation's life. A tool call naming a cluster outside it is refused before any read or send.
- **The first turn is the investigation.** The model is asked to investigate the anchor's failure and to end with findings (§4). Later turns are questions about it, and may revise the findings.
- **Budgets for an investigation's turn,** larger than chat's because it reads more:
  - 12 model calls and 24 tool calls per turn;
  - 6 reads at once, counting each read's own API and query fan-out, not only tool calls;
  - a fleet-wide admission for Git renders (§3.2): at most 2 at once across all investigations, the rest queued within the turn's deadline;
  - 300 s;
  - each tool result 16 KiB, as chat, with `git_changes` at 32 KiB;
  - what one model call carries, at most 384 KiB, as chat.

  A budget that runs out stops the turn, and the findings say what was not read (A-C3).

### 1.1 Snapshot investigations: Klyx acquires the evidence (revision 1)

A provider without tools (the Claude Code CLI, `openaiCompatible` without tools) cannot ask for reads. Its first turn therefore gets a **fixed acquisition plan** that Klyx runs from the anchor and the links it establishes, never from model or cluster text.

**The plan, by the anchor's kind:**
- **A Flux Kustomization:**
  - `gitops_unit`;
  - for at most 3 health-check entries that parsed and joined, their current object (by name, with the replacement caveat) and its failing pods, at most 3 per object chosen by Klyx's verdict;
  - each failing pod's previous-container log (kube API, tail 50, then `get_log_history` when the pod is gone and its UID is held);
  - the warning events about the anchor and those objects;
  - `git_changes`;
  - `get_alerts` for the objects;
  - `get_dependencies`.
- **A workload:** the workload, its failing pods (at most 3), their logs and events, its rollout history, its alerts, its Flux owner by labels and that owner's `git_changes`.
- **Anything else:** the object, its events, its dependencies and its alerts.
- **Comparison reads:** with a comparison set, `fleet_difference` for the anchor, or for the joined workload, under the member fences (A-C2).

**Its bounds:** the plan runs within the investigation's read and time budgets. Its snapshot document is at most 96 KiB, filled in the plan's order. Whatever does not fit, or failed, is listed in the document and in missing evidence, with its read ID.

**Its scope, said:** a snapshot investigation reads only what its plan reads. The findings panel says "Snapshot: Klyx read a fixed set of evidence", and follow-up questions use chat's snapshot rules.

## 2. Read IDs and checks

**Every ledger entry gets an ID,** `r1`, `r2` and so on within the conversation. Each tool result's envelope carries it as `readId`. Snapshot mode labels each section of its data block with its ID.

**A check is an observation Klyx evaluated itself** on a read's data, deterministically. Klyx writes the text, never the model. Each check has:
- an ID within its read (`r4.c1`);
- a class, one of those below;
- a subject: kind, namespace and name, and its **identity strength** (revision 1):
  - **by UID:** the subject's UID comes from a read Klyx pinned by UID in this investigation (the anchor, an Observer row found by UID, an owner reference, a pod listed by its workload's controller UIDs);
  - **by name:** the evidence names the object without a UID. Health-check messages, Flux inventory entries, rendered Git objects, Prometheus series and name-mode Loki streams are all by name.

  A by-name subject is never given today's UID for an object of that name. Klyx may add a separate fact, "Deployment shop/web is currently UID B, created at T", and says "possibly a replacement" when it was created after the evidence;
- Klyx's statement, which names the identity it holds: "Kustomization flux-system/app (by UID) reports a failed health check naming Deployment shop/web", never "Deployment UID B failed its health check";
- **two times, kept apart** (revision 1):
  - **observed:** when the evidence describes the world. For a live read that is the read's time; for history it is its own time or interval (a metric's window, a log line's timestamp, an event's last occurrence, a commit's time);
  - **onset:** when the evidence says its condition began (a condition's last transition, an alert's start, a restart count's first increase), if it says;
- the read's coverage: live, stale or partial.

| Class | Evaluated on | Example statement |
|---|---|---|
| `verdict` | the Observer row, by UID (`anchorVerdict`) | "Klyx's verdict for Deployment shop/web: Degraded - CrashLoopBackOff" |
| `condition` | a condition with status False or a failing reason | "Kustomization flux-system/app Ready=False, reason HealthCheckFailed, since 12:04" |
| `healthcheck` | a `HealthCheckFailed` message, parsed strictly (§3.1) and joined to the inventory by name | "Kustomization flux-system/app (by UID) reports a failed health check naming Deployment shop/web (by name), status InProgress" |
| `status` | a pod's container status | "container web of pod shop/web-7d9 restarted 6 times; last terminated with exit code 1 (Error)" |
| `event` | a Warning event about the object, by UID | "BackOff: Back-off restarting failed container web (14 times)" |
| `rollout` | rollout history and the pods' owners | "the failing pods belong to ReplicaSet web-7d9, created from revision 4" |
| `diff` | two source renders under the current spec (§3.2) | "the source render of revision a1b2c3d changed Deployment shop/web (by name): spec.template.spec.containers[web].env[LOG_LEVEL], changed (not shown)" |
| `alert` | an active alert whose subject Klyx resolved to the object's UID | "alert KubePodCrashLooping firing for pod shop/web-7d9 since 12:05" |
| `metric` | a template's series past its stated threshold (§3.4) | "restarts of the pods currently named web-7d9-x and web-7d9-y (by name) rose by 6 in the last 15 minutes" |

A check that cannot be evaluated is not made: a stale read, an ambiguous parse, a by-name subject asked to stand for a UID. The read stays evidence the model may read.

## 3. New and extended tools

Every tool follows chat's §3: strict arguments, server-side validation, the 16 KiB envelope, `guard()` before every read, and a projection through chat's rules (the never-send list, the annotation allowlist, S0b, S10 and DRA's special projections). `ScrubEvidence` runs before any clip. Tools are offered in investigations; chat gets the read-only ones that need no comparison set.

### 3.1 `gitops_unit` (new)

- **Arguments:** `kind` (Kustomization or HelmRelease), `namespace`, `name`.
- **Reads:**
  - the unit live (`FluxObject`), with its Ready condition, `lastAppliedRevision`, `lastAttemptedRevision`, source reference and inventory (`ParseDetail`);
  - the Observer's verdict for it.
- **The health-check parser** (new, `flux.HealthCheckFailed`):
  - Flux writes `<prefix>: [<Kind>/<namespace>/<name> status: '<Status>'[: <error>], ...]`, where the prefix ends in `timeout waiting for` or `failed early due to stalled resources` (`fluxcd/pkg/ssa`, `manager_wait.go`), and a cluster-scoped object has no namespace segment.
  - **Flux does not escape an entry's error text** (revision 1). An error can contain a comma, a closing bracket, or a whole entry-shaped substring: a real `status.Compute` error reads `Widget/shop/real status: 'Unknown': looking up ... accessor error: bad, Deployment/shop/other status: 'Failed' is of the type string, expected int64`, one resource with two markers. No regex makes that unambiguous.
  - **The parse is strict and stops at ambiguity.** Entries are read from the opening bracket, one at a time: `<Kind>/[<namespace>/]<name> status: '<Status>'`, then exactly `, ` (the next entry) or `]` (the end). An entry followed by `: ` carries an error. That entry is kept, because its own text precedes the error, but its error and everything after it are **unparsed**: they stay text the model may read, and create no check and no join.
  - The kind has no group. An entry joins the inventory by kind, namespace and name, **by name**. Two inventory entries of the same kind name in different groups make the join ambiguous: no join, and the entry is said as unjoined.
  - **The check speaks of the Kustomization,** whose UID Klyx holds: "Kustomization flux-system/app reports a failed health check naming Deployment shop/web (status InProgress)". The named object's current UID and verdict are read as a separate fact, by name, with the replacement caveat (§2).
- **Returns:**
  - the unit's facts;
  - the inventory, at most 100 entries, counted beyond;
  - the parsed health-check entries, each with its join;
  - `condition`, `healthcheck` and `verdict` checks.

### 3.2 `git_changes` (new)

- **Arguments:** `kind` (Kustomization), `namespace`, `name`.
- **Resolves:**
  - the unit's GitRepository source, mapped to an approved repository as the cache diff does (`line.NormalizeRepo` and the explicit matches);
  - the two revisions: `lastAppliedRevision` (the last healthy, which Flux sets only after health checks pass) and `lastAttemptedRevision`.

  A unit without both revisions, without an approved repository, or with equal revisions says so and reads nothing more.
- **Reads:**
  - the commits between the two revisions, scoped to `spec.path`: `LogPath`, at most 20, with authors and subjects scrubbed;
  - the changed files: `DiffNames`, at most 200.
- **What is rendered, said exactly** (revision 1). Both revisions are built with the cache diff's confined build (`cachediff`; 60 s and 64 MiB each, behind the fleet-wide admission of §1) under **today's Kustomization spec**: its `targetNamespace`, `images`, patches and path. The result is named a **source render under the current spec**, never the applied objects:
  - the spec in force at the healthy revision is not known. When the spec's own generation changed after the healthy revision was applied, the render says so;
  - `postBuild` substitution is not applied, so variables stay unsubstituted, and the render is **partial** when the Kustomization declares it;
  - SOPS is not decrypted. An encrypted field that changed reads "changed (encrypted)", and the render is partial;
  - a build that fails, or passes its bound, falls back to the file list and the commits: **not established**.
- **Hidden changes are found before the projection** (revision 1). Chat's projection drops env values, keeps Secret key names and ConfigMap value sizes, and masks other subtrees. Two projections can therefore be equal while the objects differ. So Klyx compares the raw old and new objects in process, for every field chat's rules exclude, before anything is projected:
  - the env values, `command` and `args`;
  - Secret data, ConfigMap and Helm values;
  - annotations outside the allowlist;
  - S0b payloads, and S10's and DRA's withheld fields.

  Each difference becomes a path marker, "spec.template.spec.containers[web].env[LOG_LEVEL]: changed (not shown)". The hidden values, and any digest of them, are never serialized.
- **The comparison:** the rendered objects are matched by group, kind, namespace and name. Visible fields are compared after projection, hidden ones as markers. An object that appears or disappears is said by its identity only. A comparison that is capped or unsupported says "change not established", never "unchanged".
- **Checks:** one `diff` check per changed object, by name, naming its visible and hidden changed paths. At most 20 paths per object and 20 objects, each counted beyond, with the render's qualification: partial, current spec, or not established.
- **Never sent:** raw file content. A YAML file can hold a Secret, values or a payload.
- **Returns** at most 32 KiB.

### 3.3 `get_dependencies` (new)

- **Arguments:** `kind`, `namespace`, `name`.
- **Reads the existing drawing for the kind:**
  - a Flux unit: `depgraph.Build`;
  - a Certificate: its issuance chain;
  - a Gateway or a route: its topology;
  - a ScaledObject or HPA: the Observer's join;
  - a workload: its pods and owner chain, from the Observer by UID.
- **Returns:** nodes, each with kind, namespace, name, UID and Klyx's verdict, and typed edges (`dependsOn`, `source`, `producer`, `owner`, `issuer`, `route`, `scaler`), at most 40 nodes, with the drawing's own incompleteness kept.
- **Uses:** these edges, and the owner, inventory and health-check joins, are the links A-C1's subject-identity rule accepts (§4).

### 3.4 `get_metrics` (new, templates only)

- **Arguments:** `template`, `namespace`, `name`, `window` (15m, 1h, 6h or 24h).
- **The templates,** Klyx's own and fixed:
  - `pod_cpu` and `pod_memory`: a pod's containers. The existing per-pod queries, with a threshold against the container's limit when one is set;
  - `workload_cpu` and `workload_memory`: summed over the workload's pods, by UID through the Observer;
  - `restarts`: `kube_pod_container_status_restarts_total` for the workload's pods. Present only when kube-state-metrics answers, otherwise "not available on this cluster";
  - `route_requests`, `route_5xx_ratio` and `route_p99`: the existing Envoy rule metrics for a route.
- **Construction:**
  - label values are escaped with `metrics.Equals` and `MatchAnyOf`;
  - the window's step keeps every series at most 60 points, the inclusive end counted;
  - each series is summarised: min, max, last, gaps, counter resets and the threshold crossing;
  - at most 8 series, each counted beyond.
- **Identity, by name** (revision 1). Series are labelled by pod name. A workload's series are selected by its current pods' names, from the Observer by UID. That is not historical ownership: a pod of the same name recreated inside the window is included, and a pod gone before the read is not. The check says so: "pods currently named web-7d9-x and web-7d9-y, in the last hour".
- **An empty result** is "no data for this selection and window", never proof that a source such as kube-state-metrics is absent.
- **Never written by the model:** the model never writes PromQL. An unknown template is refused.

### 3.5 `get_alerts` (new)

- **Arguments:** `kind`, `namespace`, `name`.
- **Reads:** the Observer's alerts snapshot, matched like `alertEvidence`: by the subject's UID, or by name when the subject is not established, said as such. It also reads alerts on the object's pods and on its namespace.
- **Returns:** active, suppressed and unprocessed alerts, at most 20, with their state, severity and start time.
  - Labels are allowlisted: alertname, severity, namespace, pod, container, job, service and cluster. Values are scrubbed.
  - The summary and description annotations are scrubbed and capped.
- **Checks:** `alert` checks only for alerts whose subject resolved to a UID.

### 3.6 `get_log_history` (new, the AI-safe Loki adapter)

- **Arguments:** `namespace`, `pod`, `container?`, `window` (15m, 1h or 6h), `tail` (1 to 200).
- **The target's identity comes from Klyx, never the model** (revision 1). A pod's UID is used only when this investigation's ledger holds it from a UID-pinned read, which covers a pod that is gone but that an earlier read listed. When the ledger holds several UIDs for that name, the model must pass one of them, as `uid`, and nothing else is accepted. With no UID held, the read is by namespace and name.
- **Reads:** Loki through the existing reader (`LokiReader.Query`): by the UID where the shipper records it, otherwise by namespace and name.
- **Says which, every time:** "by the pod's UID", or "by namespace and name: every pod of that name in the window".
- **Bounds:** at most 200 lines and 16 KiB. The whole text is scrubbed with `ScrubEvidence` before each line is capped at 2,000 characters.
- **Purpose:** the pod that is gone, and history beyond the previous container. The kube-API read (`get_pod_logs`) is unchanged.

### 3.7 `fleet_difference` (new, investigations with a comparison set)

- **Arguments:** `kind` (Deployment, StatefulSet or DaemonSet), `namespace`, `name`.
- **Reads:** `WorkloadCompare` on the home cluster and on each member of the comparison set, through each member's pinned connection.
- **Returns:** per cluster, whether the workload was found, its replicas, images and resources, its spec hash, and its Flux owner's applied revision, plus the differences against the home cluster.
- **Fenced as A-C2 requires:**
  - a member's connection and egress are checked before its read, and before every model send that carries its results;
  - a member that disconnected or lost egress ends the turn.

### 3.8 Extended tools

- **`get_resource`** adds `verdict`, `condition`, `status` and `event` checks.
- **`get_events`** adds `event` checks for the anchor and its linked objects.
- **`gitops_status`** is unchanged.

## 4. Findings

**The model ends an investigation by reporting findings.**
- In tools mode, it calls `report_findings` once per turn.
- In snapshot mode, it ends its answer with a fenced `findings` JSON block.

**The model's input:**
- **findings,** at most 20, each with:
  - `type`: `observation` or `cause`;
  - `statement`: at most 500 characters;
  - `subject`: kind, namespace and name;
  - `cites`: read and check IDs, at most 10;
- **`missing`:** at most 10 notes on what it could not read;
- **`remedy`,** text only: a Git change and a mitigation, each at most 2,000 characters, labelled "proposed, not run".

The input is validated strictly, as tool arguments are. In tools mode an invalid call gets an error result and may be retried once. In snapshot mode an invalid block shows the answer with "the findings could not be read".

**What Klyx decides, for every finding** (A-C1):
1. **Provenance:** every cited ID must be in this investigation's ledger. Otherwise the finding is **unsupported**.
2. **Subject identity:** at least one cited read or check must be about the finding's subject. **By UID** means the subject's own UID, or a UID join Klyx holds: the owner chain, a dependency edge read by UID. **By name** means a name correlation: the inventory, a health-check entry, a rendered Git object, a metric or log stream by name. A finding supported only by name correlations is shown as such ("by name: the object at that name may have been replaced"), never as about a UID. Otherwise the finding is **unsupported**.
3. **Time** (revision 1, observation apart from onset):
   - **The failure's window** starts at the anchor's Ready=False transition, or its verdict's "since". It is an upper bound: the failure may have begun earlier, and a reason that changed while Ready stayed False makes the current episode's start uncertain, which is said.
   - **Evidence observed before that window** (a metric interval or log lines ending before it, a commit long before, an event last seen before) does not establish the current failure. It is shown as "observed before the failure was reported".
   - **Evidence observed during it is current,** whatever its onset. A pod crash-looping since 12:00, read at 12:06 while the Kustomization failed at 12:05, is a current, checked state, and its earlier onset is stated as a relation ("began before the Kustomization reported the failure").
   - An anchor that is not failing has no window: the rule does not apply, and the findings say there was no failure to date.
4. **Coverage:** a citation to a stale or partial read is shown with that qualification.
5. **The label:**
   - a Klyx check cited by a finding is shown beside it in Klyx's own words, as **checked**;
   - an `observation` is shown as the model's **reading** of the cited evidence;
   - a `cause` is **suspected**. No causal-proof rule is defined in A1, so no cause is proven, whatever the model writes.

**What the labels do not do** (A-C4): cluster text can steer the model, and an injected instruction can corrupt a finding, a likely cause or the proposed remedy. Klyx's checks and the labels "reading", "suspected" and "unsupported" limit that; they do not remove it. Nothing in A1 acts on the cluster.

**Missing evidence, from Klyx and not the model:** the ledger's failed, refused, truncated and cancelled reads, stale or unreadable kinds, unjoined health-check entries, budgets that ran out, and `git_changes` without a rendered diff. The model's own `missing` notes are added and labelled as its own.

**Chat answers can cite reads.** A `[r4]` or `[r4.c1]` in any answer, chat or investigation, renders as a chip only when the ID is in that conversation's ledger. It opens the ledger entry. Anything else stays text.

## 5. Frontend

- **Entry points:** "Investigate" in every inspector's More menu beside Explain, in ⌘K on an object, and under an Explain result.
- **The drawer, in investigation mode:**
  - **Header:** the anchor, its verdict, the provider and mode, "read-only", and the comparison set. The set can be edited only before the first question.
  - **The ledger:** each read shows its ID and its checks.
  - **The findings panel:**
    - Klyx's checks first, grouped by subject;
    - then the model's findings, each labelled reading, suspected or unsupported, with the Klyx checks it cites shown beside it as checked. A model finding is never itself "checked";
    - then missing evidence;
    - then the proposed remedy, "proposed, not run".

  Nothing in it acts on the cluster.
- **Rendering:** the existing `SafeMarkdown` subset for prose. Statements are plain text.

## 6. Egress and protection

**Unchanged:** every chat guarantee, the egress gate per cluster (`allowProtectedEgress`), and the snapshot mode's delimited data block for providers without tools.

**Literal-needle tests,** one per new path, assert that a planted value never reaches a serialized result, snapshot or findings document:
- a Secret's data changed in a revision diff;
- a ConfigMap value;
- a HelmRelease's values;
- an env value, and `command` and `args`;
- an annotation outside the allowlist;
- an S0b payload (a ResourceSet's inputs);
- a DRA class's selector;
- a credential in a Loki line;
- an alert annotation;
- a commit subject carrying a token;
- a cloud resource ID in a condition message, redacted before clipping.

## 7. The lab and the scenario

**The acceptance scenario is the roadmap's §6.1:** a Kustomization whose health check times out on a crash-looping Deployment after a commit. On the kind lab it needs:
- **A Git server in the cluster** that Flux reads. The Mac's system git must also reach it over https or ssh, since Klyx approves only those. The approved URL's explicit match then maps the cluster's in-cluster URL to it.
  - The candidate is an in-cluster git server with ssh, port-forwarded to the Mac, and a lab-only key.
  - This touches the owner's ssh configuration (a host entry and a key), so the owner decides it before the lab is built.
- **A Kustomization** with `wait` health checks on a Deployment. Two commits: a healthy one, then one changing an env value so the container exits.
- **Loki** shipping the pods' logs, as S8b's lab does, and **Alertmanager** as S8a's.

**The real-LLM run** needs the owner's provider key, which only the owner enters. It is the owner's step of the v1.0.0 gate. Every other check runs on fakes: a fake converser that replays a scripted investigation, as chat's tests do.

## 8. Tests

- **The health-check parser** against Flux's own message builder: every prefix, a namespaced and a cluster-scoped entry, an error containing commas, an error containing a whole entry-shaped substring (the upstream `status.Compute` example), an entry not in the inventory, and a kind present in two groups.
- **Identity** (revision 1):
  - a Deployment replaced between its health check and the investigation: the check names it by name, and the current UID carries the replacement caveat;
  - a pod gone from the API and the Observer: its logs by the UID the ledger held, or by name when none;
  - a same-name pod recreated inside a metric window;
  - name-only evidence never yields a checked assertion about a UID.
- **Time** (revision 1):
  - a continuing failure read after the timeout stays a current, checked state;
  - evidence observed before the failure was reported does not establish it;
  - a reason that changed while Ready stayed False;
  - a metric window keeps its interval.
- **The rendered diff:**
  - a changed image;
  - an env value, a same-key Secret value and a same-length ConfigMap value, each found as a hidden-change marker, its value absent from the output;
  - a moved object;
  - `postBuild` and SOPS renders labelled partial;
  - a spec changed since the healthy revision;
  - a failed build's fallback;
  - the bounds.
- **Metrics:** gaps, counter resets, a changed limit, the inclusive end within 60 points, and an empty result never read as an absent source.
- **Snapshot investigations:** the scripted Flux scenario's first turn includes the planned evidence with its IDs, or lists what its plan could not read; it honours the 96 KiB and read bounds; and a comparison member's retained data is refused after its egress is revoked.
- **A-C1's counterexamples**, each a scripted investigation whose findings Klyx must label correctly:
  - an annotation-only diff beside an independent failure: the cause stays suspected, and the diff check names the annotation only;
  - a new image failing to pull because the registry is down: suspected;
  - a real ledger ID cited for an unrelated object: unsupported;
  - a check older than the failure: shown as earlier and not supporting.
- **Fences:**
  - a comparison member that disconnects after its read: the next send is refused;
  - a tool call naming a cluster outside the set: refused before any read;
  - the settings and connection fences, as chat's.
- **Budgets:** each bound, and a run that exhausts the tool budget and says what was not read.
- **The literal needles** of §6.
- **Mounted UI tests:** the findings panel's labels, citation chips that resolve only to ledger IDs, and the comparison set locked after the first question.
- **The native smoke:** an investigation on the lab with the fake converser, the findings panel rendered.
- **Captures:** night and day at 1440 and 1024.
- **Parity:** none, since A1 changes no verdict.
- **Load:** simultaneous investigations at their bounds: read fan-out, the Git render admission (two 64 MiB renders at once), retained ledgers and checks, against the 2 GiB cache and the Observer.

## 9. Stages

1. Read IDs, checks and chips: the ledger's IDs, the checks on the existing tools, and citation chips in chat.
2. `gitops_unit`: the health-check parser and the inventory join.
3. `git_changes`: the two rendered revisions through the projection, with the fallback.
4. `get_dependencies`, `get_alerts`, `get_metrics` and `get_log_history`.
5. The investigation: the conversation kind, the anchor, the budgets, `report_findings` and the snapshot block, Klyx's labelling, and missing evidence.
6. `fleet_difference` and the comparison set, with its send fence.
7. The frontend: Investigate, the findings panel and the comparison set.
8. The lab (after the owner's decision on its Git server), the smoke, captures and the as-built notes.

## 10. Not in A1

- Any action: A3's action card and its verification.
- Proactive investigations: A2.
- Arbitrary PromQL or LogQL.
- Writing to Git.
- A causal-proof rule.
- Argo's hook results, which belong to the Argo-parity work.
- HelmRelease upgrade diffs. Values never cross; A1 reads a HelmRelease's status and history only.

## 11. Revision 1: Codex's design review of bf78038

Codex (handoff aa240b9d) requested changes: six P2 findings, each with a probe or a primary source.

| # | Finding | Where answered |
|---|---|---|
| 1 | Flux does not escape an entry's error, so a marker inside it can read as another resource | §3.1: a strict entry-by-entry parse; an entry's error and everything after it are unparsed, with no check and no join. §8: the upstream `status.Compute` example |
| 2 | Looking up today's UID for a name gives historical, name-only evidence a stand-in's identity | §2: identity strength by UID or by name, never upgraded, with the replacement caveat; §3.1, §3.4 and §3.6 say which; §4's subject rule shows name correlations as such; logs use a UID only from the ledger |
| 3 | Observation time was conflated with onset, rejecting a continuing failure that began earlier | §2: observed and onset times apart; §4: the failure's window as an upper bound, evidence observed during it current, earlier onset stated as a relation |
| 4 | Comparing projections hides changes to env values, Secret values and same-length ConfigMap values | §3.2: hidden changes found on the raw objects in process, before projection, for every chat exclusion, emitted as path markers only; capped comparisons say "change not established" |
| 5 | A successful build is not a complete render: postBuild and SOPS are not applied, and today's spec renders both revisions | §3.2: named a source render under the current spec, partial for postBuild and SOPS, the spec's change since the healthy revision said |
| 6 | Snapshot mode had an output block but no way to acquire the new evidence | §1.1: a fixed acquisition plan from the anchor and Klyx's links, its bounds and its scope stated |

Codex's guidance, taken: read fan-out counted within the 6 concurrent reads and a fleet-wide admission for Git renders (§1); empty metric results, gaps, counter resets and the inclusive end (§3.4); a model finding is never itself "checked" (§5); the member fences cover retained history (§3.7, as the roadmap's A-C2).
