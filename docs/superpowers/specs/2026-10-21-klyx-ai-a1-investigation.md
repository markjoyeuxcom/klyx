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
  - 6 reads at once;
  - 300 s;
  - each tool result 16 KiB, as chat, with `git_changes` at 32 KiB;
  - what one model call carries, at most 384 KiB, as chat.

  A budget that runs out stops the turn, and the findings say what was not read (A-C3).

## 2. Read IDs and checks

**Every ledger entry gets an ID,** `r1`, `r2` and so on within the conversation. Each tool result's envelope carries it as `readId`. Snapshot mode labels each section of its data block with its ID.

**A check is an observation Klyx evaluated itself** on a read's data, deterministically. Klyx writes the text, never the model. Each check has:
- an ID within its read (`r4.c1`);
- a class, one of those below;
- a subject: kind, namespace, name and UID;
- Klyx's statement;
- the evidence's own time: when the condition last changed, the alert started, or the metric window ended;
- the read's coverage: live, stale or partial.

| Class | Evaluated on | Example statement |
|---|---|---|
| `verdict` | the Observer row, by UID (`anchorVerdict`) | "Klyx's verdict for Deployment shop/web: Degraded - CrashLoopBackOff" |
| `condition` | a condition with status False or a failing reason | "Kustomization flux-system/app Ready=False, reason HealthCheckFailed, since 12:04" |
| `healthcheck` | a `HealthCheckFailed` message, parsed (§3.1) and joined to the inventory and to live UIDs | "the health check timed out on Deployment shop/web (status InProgress)" |
| `status` | a pod's container status | "container web of pod shop/web-7d9 restarted 6 times; last terminated with exit code 1 (Error)" |
| `event` | a Warning event about the object, by UID | "BackOff: Back-off restarting failed container web (14 times)" |
| `rollout` | rollout history and the pods' owners | "the failing pods belong to ReplicaSet web-7d9, created from revision 4" |
| `diff` | two rendered revisions (§3.2) | "revision a1b2c3d changed Deployment shop/web: spec.template.spec.containers[web].env[LOG_LEVEL] (value not shown)" |
| `alert` | an active alert whose subject Klyx resolved to the object's UID | "alert KubePodCrashLooping firing for pod shop/web-7d9 since 12:05" |
| `metric` | a template's series past its stated threshold (§3.4) | "restarts of shop/web rose by 6 in the last 15 minutes" |

A check that cannot be evaluated is not made: a stale read, a subject not resolved by UID, an unparsed message. The read stays evidence the model may read.

## 3. New and extended tools

Every tool follows chat's §3: strict arguments, server-side validation, the 16 KiB envelope, `guard()` before every read, and a projection through chat's rules (the never-send list, the annotation allowlist, S0b, S10 and DRA's special projections). `ScrubEvidence` runs before any clip. Tools are offered in investigations; chat gets the read-only ones that need no comparison set.

### 3.1 `gitops_unit` (new)

- **Arguments:** `kind` (Kustomization or HelmRelease), `namespace`, `name`.
- **Reads:**
  - the unit live (`FluxObject`), with its Ready condition, `lastAppliedRevision`, `lastAttemptedRevision`, source reference and inventory (`ParseDetail`);
  - the Observer's verdict for it.
- **The health-check parser** (new, `flux.HealthCheckFailed`):
  - Flux writes `<prefix>: [<Kind>/<namespace>/<name> status: '<Status>'[: <error>], ...]`, where the prefix ends in `timeout waiting for` or `failed early due to stalled resources` (`fluxcd/pkg/ssa`, `manager_wait.go`), and a cluster-scoped object has no namespace segment.
  - The parser finds each entry by its ` status: '...'` marker, not by splitting on commas, because an entry's error text may contain commas.
  - The kind has no group. An entry joins the inventory by kind, namespace and name. Two inventory entries of the same kind name in different groups make the join ambiguous: no check, and the entry is said as unjoined.
  - A joined entry is read live for its UID, then its verdict.
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
- **The rendered diff:**
  - both revisions are built with the cache diff's confined build (`cachediff`; 60 s and 64 MiB each);
  - the rendered objects are compared by group, kind, namespace and name, each projected through chat's rules before comparing;
  - a change inside a dropped or masked subtree is reported as "changed (not shown)", with its path;
  - an object that appears or disappears is said by its identity only;
  - one `diff` check per changed object, naming its changed paths, at most 20 paths per object and 20 objects, each counted beyond.
- **When a build fails** (SOPS, remote bases, the size bound), the tool falls back to the file list and the commits, and says the rendered diff is not established. It never sends raw file content: a YAML file can hold a Secret, values or a payload.
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
  - the window's step keeps every series under 60 points;
  - each series is summarised: min, max, last and the threshold crossing;
  - at most 8 series, each counted beyond.
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
- **Reads:** Loki through the existing reader (`LokiReader.Query`), by the pod's UID where the shipper records it, otherwise by namespace and name.
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
2. **Subject identity:** at least one cited read or check must be about the finding's subject, by UID, or about an object Klyx links to it through a join it holds. Those joins are the owner chain, the inventory, a health-check join or a dependency edge read in this investigation. Otherwise the finding is **unsupported**.
3. **Time:** a check whose evidence time is before the failure began does not support a finding about the failure. The start is the anchor's Ready-false transition, or its verdict's "since". The check is shown as "earlier than the failure". An anchor that is not failing has no start: the rule does not apply, and the findings say there was no failure to date.
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
    - then the model's findings, each labelled checked, reading, suspected or unsupported, with chips for its citations;
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

- **The health-check parser** against Flux's own message builder: every prefix, a namespaced and a cluster-scoped entry, an error containing commas, an entry not in the inventory, and a kind present in two groups.
- **The rendered diff:** a changed image, an env value, a value inside a Secret, a moved object, a failed build's fallback, and the bounds.
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
- **Load:** an investigation's reads at their bounds, against the 2 GiB cache and the Observer.

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
