# Coverage S8a: alerts, from Alertmanager

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S8a, after C5 (`2026-10-18-klyx-coverage-c5-external-credentials.md`, implementation accepted at 6c17617). S8b (Loki) follows with its own note. Revision 1 answers Codex's design review of 4bc4685 and the lab's first findings (§14); revision 2 answers its re-review of a9cfe4e (§15).

The owner's answer (roadmap §5): **S8a reads Alertmanager.** The roadmap fixes the contract this note builds:
- **States:** active, suppressed and unprocessed stay distinct.
- **Deduplication:** HA replicas are deduplicated by fingerprint.
- **Where alerts show:**
  - Active alerts appear in the object's inspector, and as a Needs you source counted *separately* from object faults.
  - Suppressed alerts appear only where a person looks for them, never in Needs you.
- **No OS notifications.**
- **Read-only.**
- **The shared external-evidence contract:**
  - identity by configured cluster labels, with name matches labelled as correlation;
  - a failed or stale poll never manufactures recovery;
  - bounded reads that never read complete when they are not.

## 1. What is there today (read from the code at a043e8d, main after PR #50)

- **Configuration only.** `ClusterConfig.Alertmanager *EndpointConfig` is parsed and validated (`internal/config/config.go:122`, `endpoints.go:86-132`), but nothing reads it.
  - `DefaultConnFactory` copies only `cc.Metrics` into the connection (`internal/fleet/factory.go:29-58`).
- **Credentials work for any endpoint kind already.**
  - `endpointcred.Resolve` and the `Registry` are keyed by cluster and kind.
  - Settings' Set and Remove already move the `alertmanager` revision (`endpoints_service.go:235-255`).
  - Test refuses every kind but metrics: "not implemented until S8".
- **Everything else in the endpoint path is metrics-only:**
  - `metrics.Resolve` takes `MetricsConfig` and words its reasons for Prometheus.
  - The direct transport's request paths are PromQL.
  - The guarded proxy client carries the metrics cap and its error, and lives under `metricsMu`.
  - `credentialMoved`, `metricsAuth`, `SetMetricsConfig` and `ChangeMetricsCredential` are wired to `config.EndpointMetrics`.
  - The metrics `discover()` discards its GET and LIST errors: a forbidden or failed lookup reads as "nothing found". S8a must not inherit that (§2).
- **The periodic read Klyx already has.** The Observer's snapshot (`observe.SnapshotSpec`: a named cadence, a read deadline, single-flight, with `ReadSnapshot` and `Refresh` for on-demand reads) carries Helm (5 min) and Warning Events (60 s).
  - A failed read keeps the last good items.
  - A counted snapshot's batches are `ModeSampled`, and `healthnotify.Apply` returns before any episode logic for a sampled batch (`internal/healthnotify/engine.go:300-302`).
  - A snapshot has no "not installed" outcome today: a read error is a failed read.
- **Needs you.**
  - `fleetQueue` (`frontend/src/fleet/queue.ts:61-120`) adds rows only for lines in the `attention` state and counts `faults` and `attention`.
  - An evidence part (Events) is computed but never reaches Needs you.
  - There is no third count. A row's key already allows a ref without a UID.
- **Inspectors.**
  - `InspectorLayout` has no shared section slot. Each inspector lists its own blocks.
  - `useInspectorAI` is the precedent for a per-object block every inspector places itself.
- **The Observer holds** pods, deployments, statefulsets, daemonsets, replicasets, nodes and namespaces among the kinds an alert can name. It does not hold Jobs, CronJobs, PersistentVolumeClaims or HPAs.

**Alertmanager's API, read from its v0.34.1 code** (the release Klyx's lab runs), as S8a uses it:
- **`GET /api/v2/alerts`** takes `active`, `silenced`, `inhibited` and `unprocessed` (all default true), `filter` (matchers, repeatable), and `receiver`.
  - It drops alerts whose `endsAt` has passed before returning.
  - Its `mutedBy` is always empty: the handler passes no mute information to the converter (`api/v2/api.go:277`, `compat.go:133`). Silences and inhibition are reported.
- **A `gettableAlert`** has:
  - `labels`, `annotations`, `fingerprint`;
  - `startsAt`, `updatedAt`, `endsAt`;
  - `receivers`, `generatorURL`;
  - `status{state: active|suppressed|unprocessed, silencedBy[], inhibitedBy[], mutedBy[]}`.
- **`GET /api/v2/alerts/groups`** (`api.go:469`) takes `active`, `silenced`, `inhibited` and `muted` (all default true), `filter` and `receiver`.
  - It returns aggregation groups: `labels`, `receiver{name}`, and the group's alerts.
  - Each alert has *that group's* status, so time-interval muting is reported here, per route.
  - An alert in several routes appears in several groups. An unprocessed alert is in none.
  - **The two lists are built differently.** The flat list's `receivers` come from matching the alert against the routes now (`api.go:293`, one entry per matching route, so a receiver shared by two routes appears twice). The groups come from the aggregation groups that exist (`dispatch.go:314`), and a new group can be refused at the group limit (`dispatch.go:435`). Two successful GETs therefore do not certify that their routes agree.
- **`GET /api/v2/status`** returns `versionInfo`, `cluster{status: ready|settling|disabled, peers}` and `uptime`.

**The lab confirmed this on a real Alertmanager 0.34.1.** One muted alert reads `active` with `mutedBy: []` on `/alerts`, and `suppressed` with `mutedBy: ["always"]` on `/alerts/groups`. Its lab notes add one behaviour: the mute stage runs in a receiver's notify pipeline, so an alert routed only to a receiver with no integration is never marked muted.

## 2. Configuration and discovery: which Alertmanager, which alerts

The `alertmanager` block keeps C5's shape: `endpoint`, `serviceRef`, `tlsSkipVerify`, `auth`, `tenant`. S8a adds two fields:

- **`match`**, the labels that make an alert this cluster's in a shared Alertmanager, e.g. `match: {cluster: aks-prd-weu}`.
  - Equality matchers only: a valid label name, and a non-empty value without control characters.
  - Sent as `filter` parameters (`cluster="aks-prd-weu"`) on both reads, and checked again on every alert Klyx receives. An Alertmanager-compatible API that ignores `filter` cannot leak another cluster's alerts in.
  - `match` belongs to `EndpointConfig`, so S8b's Loki block takes the same field. It counts in C5's endpoint revision and in the source identity (§4).
- **`serviceRef.path`**, a path prefix behind the Service: Mimir's Alertmanager is served under `/alertmanager`.
  - The same field is honoured by the metrics service proxy, which has the same need (Mimir's Prometheus API under `/prometheus`). One shape, one rule.

**Without `match`,** every alert on the Alertmanager is read as this cluster's. That is right for an Alertmanager inside the cluster, and the source line says so: "every alert on this Alertmanager is read as this cluster's". Two clusters configured with the same Alertmanager and no `match` get a load warning naming both.

**Resolution:**
1. an `endpoint`;
2. else a `serviceRef` through the API server's service proxy;
3. else discovery.

**Discovery goes through the Prometheus Operator, and must prove one installation.**
- **The governing Service is shared.** The operator's governing `alertmanager-operated` Service selects every Alertmanager pod in its namespace by application name (Prometheus Operator v0.94.1, `makeStatefulSetService`). One such Service is therefore not proof of one installation, and Klyx never treats it as one.
- **The rule:**
  1. Read `alertmanagers.monitoring.coreos.com` across the cluster. When the resource is not served, there is no discovery.
  2. **Exactly one Alertmanager resource:** use a Service in its namespace whose selector pins that instance (`alertmanager: <name>`, as kube-prometheus-stack's instance Service does), through its port named `web` or `http-web`. Several such Services select the same pods, so the first by name serves.
  3. **No instance Service:** the governing `alertmanager-operated` is used only with a proof of ownership, read complete:
     - every endpoint in its EndpointSlices has a `targetRef` to a Pod;
     - every such Pod is owned, by UID, by one StatefulSet;
     - that StatefulSet is owned, by UID, by the Alertmanager resource.

     A name pattern is not the proof. A missing `targetRef` or incomplete ownership leaves the governing Service unproven, and discovery reads it as ambiguity.
  4. **More than one Alertmanager resource, or a governing Service fronting pods of more than one instance:** "several Alertmanagers found; set alertmanager.serviceRef". This is ambiguity, never a guess.
- **A standalone Alertmanager without the operator** is configured with a `serviceRef`. The label-only fallback of the first draft is gone: a label does not prove an installation either.
- **Every discovery read must succeed and be complete.** A forbidden list, a timeout, or a capped or continued list makes discovery "could not be read" with the reason, never "not installed".
- **Discovery is cached for five minutes** and re-run at once after a failure or a configuration change. Its outcome joins the source identity (§4).

## 3. The reader

**One kind-neutral endpoint transport** replaces the metrics-only plumbing rather than copying it. A second copy of the credential, redirect, cap and redaction code would be two places to keep the C5 contract.
- `internal/endpointhttp` takes the pieces C5 built and accepted, unchanged in behaviour:
  - the direct transport: auth header, tenant header, the `Live` gate on every round trip, the completion re-check, and error redaction;
  - the guarded proxy REST client: same-origin redirects, and a cap below client-go's decoding;
  - capped reads that error.
- They are offered as `Get(ctx, path, query)`, with the cap, timeout and too-large error per kind.
- `internal/metrics` keeps its API and becomes a caller of it. Every C5 test passes unchanged; that is the gate on the refactor.

**One endpoint state per kind on the connection.**
- `fleet` gets a small `endpointState` (its configuration, discovery cache, resolution, credential revision, and its own lock) for `alertmanager`, and later `loki`.
- It has the metrics path's guarantees:
  - a read drops a result that crossed a revision move;
  - a configuration change moves the revision inside the lock;
  - a credential and the reviewed configuration are installed as one step.
- Metrics keeps its own state for now. Moving it onto `endpointState` is a later cleanup, not a precondition.

**`internal/alertmanager` (pure):**
- `ParseAlerts(body)` to `[]Alert`, tolerant of a missing `mutedBy` and strict about everything else.
- `ParseGroups(body)` to `[]Group{Receiver, Labels, Alerts []GroupAlert{Fingerprint, State, MutedBy, SilencedBy, InhibitedBy}}`.
- `ParseStatus(body)`.
- `Reduce(alerts, groups) []Alert`, the one place states are decided (below).
- `Subjects(labels)`, the correlation (§5).
- `Severity(labels)`, the counting class (§6).
- `Display(alert, redactor)`, the safe display values (below).

**The read is one snapshot read made of two GETs**, both through the resolved transport, with the same `filter`:
1. `GET {base}/api/v2/alerts?active=true&silenced=true&inhibited=true&unprocessed=true&filter=…` gives the members, and each one's state, silences and inhibitions.
2. `GET {base}/api/v2/alerts/groups?active=true&muted=true&silenced=false&inhibited=false&filter=…` gives the mutes, per group.

Both must succeed, or the snapshot read fails as one. A response that is not JSON, has the wrong shape, or holds an alert without `fingerprint`, `labels` or `status.state` is a failed read, never a partial list.

**`Reduce` decides each alert's state:**
- **Membership is the alerts list's.** A fingerprint only in the groups (it arrived between the two GETs) is ignored.
- **Duplicates in the alerts list** (a front that merges HA replicas) collapse to one per fingerprint.
  - When the copies' states differ, the alert is active if any copy is active, and the alert keeps "copies disagreed: active and silenced".
  - When the copies' silences or inhibitions differ, the alert keeps the union.
- **Silenced or inhibited** in the alerts list: suppressed with those causes. The groups are not consulted; silence and inhibition are alert-wide.
- **Active** in the alerts list: its group appearances decide muting, but only once they are shown to cover the routes the alert is expected in.
  - **One appearance is one group in the groups response.** Appearances are never collapsed by receiver and group labels: two routes that share a receiver are two appearances.
  - **The expected appearances** are the alert's own flat `receivers`, as a multiset: a receiver named twice is expected twice.
  - **Covered** means the alert's appearances, as a multiset of receivers, equal its expected multiset.
  - **Covered, every appearance muted:** the alert is **muted**, a suppressed cause, with the time intervals named (`always`).
  - **Covered, some appearances muted:** the alert stays **active and counted**, with "muted for receiver lab-muted; active for lab". Muting is per route, and a route still notifying is not quiet.
  - **Not covered:** the alert stays **active and counted**, with "mute evidence incomplete: expected receivers lab, lab-muted; groups show lab-muted". This covers:
    - an expected receiver with fewer appearances;
    - a receiver appearing more often than expected, or not expected at all (the routes changed between the two GETs);
    - no appearance (not in any group yet, or refused at the group limit).
  - The two GETs are never treated as one instant. Only a match between them lets a mute take an alert out of Needs you.
- **Unprocessed** in the alerts list: unprocessed. It is in no group by definition.
- **An unknown state** is kept as written and never counted.

**Raw values and display values are separate.**
- **Raw values** (labels, annotations, fingerprints, receiver names) are kept as decoded. They serve `match`, deduplication, the reduction and correlation, and nothing else.
- **Every string that leaves `internal/alertmanager`** for a DTO, a summary ref, Needs you, the lens, an inspector, AI or Settings' Test is a display value. That includes alert and receiver names, label values, annotations, URLs, reasons, and the status fields. It is made in C5's order:
  1. the read's own credential redactor: the exact forms of the credential the transport carried, and their Go-quoted spellings;
  2. `scrub.Credentials` for general shapes;
  3. URLs (`generatorURL`, `runbook_url` and any http(s) URL in an annotation): userinfo removed, the rest shown as text;
  4. then the clip (§8), never before.
- **The redactor is the one the reading transport carried.** A response can only echo the credential it was sent.
- **A JSON-escaped echo** (`c5/…`) is whole once decoded, and is redacted after decoding, as C5's revision 1 established for metrics.

## 4. The source contract (C1)

- **Acquisition mode:** an Observer snapshot, `alerts`.
  - It reads every 30 s with a 10 s deadline covering both GETs, single-flight per cluster.
  - The Alerts lens's refresh goes through `ReadSnapshot`, the same flight.
  - Settings' Test does **not**: it is a separate `GET /api/v2/status` through the reviewed endpoint (C5's review fence and configuration sync), and it never touches the snapshot's freshness or result. After a successful Test it asks the snapshot for a fresh read, which has its own outcome.
  - **The snapshot is always registered.** A read returns the new `observe.ErrSourceAbsent` only when the cluster has no `alertmanager` configuration and discovery completed and found no candidate. The snapshot's coverage is then `absent`: not installed, no evidence, no gap.
    - A failed discovery is a failed read, which keeps any retained evidence (§2).
  - **The lens and inspector gate** reads the alerts part's coverage in the cluster summary: hidden when absent, shown otherwise. Retained or newly found evidence and the gate move on the same 30 s cadence. There is no separate capability fact.
- **Scope, projection and versions:**
  - Scope is this cluster's alerts, as `match` defines them (§2).
  - The projection is the reduced `Alert` with its display values; nothing else is held.
  - The API is v2 only. v1 was removed in 0.27.0 and returns 410, which reads as "Alertmanager's v1 API is gone; Klyx reads v2".
- **The source identity, and evidence partitioned by it:**
  - **The source identity** is:
    - the cluster;
    - the resolution (`endpoint`, `serviceRef` or discovered);
    - the effective location: the canonical origin plus path for an endpoint, `namespace/service:port` plus path for a Service;
    - the tenant;
    - the `match` set, sorted.
  - The canonical origin is the credential boundary (C5); the source identity is the evidence boundary. `/am-a` and `/am-b` on one origin are two sources, and so are `cluster=A` and `cluster=B` on one Alertmanager.
  - **A read's whole outcome belongs to one identity, through the snapshot's generation.** Items alone cannot carry it: the Observer keeps a snapshot's coverage, as-of time and truncation in the generation, apart from the items, and an empty read carries no item at all.
    - **`observe.Observer.RekeySnapshot(name, key)`** (new) ends the snapshot's current generation and starts a new one carrying `key`, under the Observer's lock, unless the current generation already carries it.
      - A new generation starts with no items, coverage `checking`, and no as-of time.
      - The existing generation check in `apply` discards every outcome of an ended generation: its items, its error, its truncation and its freshness. So an old in-flight completion can never advance the new identity's freshness.
    - **The snapshot's `Read` receives its generation's key** (`Read func(ctx, key string)`; Helm and Events ignore it).
      - The alerts read resolves its source from `endpointState` and refuses with "the source changed during the read" when the resolved identity is not the key.
      - It returns one envelope item, `{Identity, Alerts, Truncated, ReadAt}`, so even an empty read names the identity it read.
    - **The connection re-keys the snapshot inside `endpointState`'s lock** whenever the identity changes: configuration sync, a Settings action, or a discovery outcome. An identity change and its re-key are therefore one step for every read.
  - **Consequences:**
    - A source change starts an unread collection ("not read yet for this source"), never a recovery.
    - Until the new identity's first read, the part is `checking`. When that read fails, the part is `unreadable`, with no evidence and nothing stale from the old source. When it succeeds empty, the part is `live` with no alerts.
    - "No alert" and every completeness claim need a successful read in the current generation.
  - A read that crosses a credential revision move is dropped as C5 does.
- **The counted entity and its identity:** an alert, identified by its fingerprint within its source identity.
  - A fingerprint is Alertmanager's hash of the labels: the same labels firing again later is the same fingerprint, and `startsAt` tells the occasions apart.
  - Identical fingerprints from different sources never share anything.
  - The ref has no UID and no GVR of its own. Its subject is a correlation (§5).
- **Dependency completeness:** every negative claim needs complete, current evidence, or it is qualified.
  - **A read failing** (endpoint unresolved or unreachable, the credential refused, a malformed answer, discovery not read): the part reads "could not be read" with the reason (a display value), and the last good list of the *same* source identity is kept, marked stale with its as-of time.
    - A failed read never empties the list and never turns an alert into "resolved".
  - **A capped read** (§8): every count reads "at least".
    - Per object, "no alert" becomes "no alert among the 5,000 read (more exist)". An alert absent from a capped read is not established either way.
  - **Klyx keeps no alert history in S8a.** An alert absent from a complete read is simply not listed. Nothing claims "resolved" or "no longer matched".
  - **A subject's object:**
    - "not found" is said only when the subject's kind is one the Observer holds, its coverage is live and complete, and the object is not there;
    - for a held kind that is stale, capped or not yet read, "not established (pods not read complete)";
    - for a kind the Observer does not hold (Jobs, CronJobs, PVCs, HPAs), "named, not looked up". The alert still shows; the subject is a name.
- **Clock boundaries:**
  - The list is as of its read.
  - A row reads "started 2h ago" (Alertmanager's `startsAt`) and "last seen active 14:02" (the successful read's time). Stale, it reads "last seen active 14:02; reads failing since 14:03".
  - Klyx never derives an ending, a resolution, or continued firing from `endsAt` on its own clock: the producer can renew it after Klyx's last read. The alert inspector shows `endsAt` as "end as last reported: 16:00", qualified the same way when stale.
  - The snapshot's freshness (cadence plus grace) marks a list stale when reads stop arriving.
- **Notification eligibility:** none.
  - The counted snapshot's batches are sampled, and `healthnotify` drops sampled batches before any episode logic.
  - The test pins it with the Observer-produced alerts batch fed to the engine, not a hand-built sampled input.
  - An alert fingerprint is not a Kubernetes UID. Notifying on alerts would need its own design, and the roadmap keeps it out of this programme.
- **No synthetic evidence:**
  - Klyx never invents a state, a severity, a subject or a time.
  - A missing `severity` label is "no severity", not "warning".
  - An active alert in no group is "mute not read", not "not muted".

## 5. Alerts on objects: correlation, never a join

An alert names its subject only through labels. The conventions here are kube-state-metrics' and the kube-prometheus rules'.

| Labels | Subject |
|---|---|
| `namespace` + `pod` | Pod |
| `namespace` + `deployment` | Deployment |
| `namespace` + `statefulset` | StatefulSet |
| `namespace` + `daemonset` | DaemonSet |
| `namespace` + `job_name` | Job |
| `namespace` + `cronjob` | CronJob |
| `namespace` + `persistentvolumeclaim` | PersistentVolumeClaim |
| `namespace` + `horizontalpodautoscaler` | HorizontalPodAutoscaler |
| `node` | Node |
| `namespace` alone | Namespace |

**Rules:**
- **Labels never read as subjects:** `job`, `instance`, `service`, `endpoint` and `container`. They describe the scrape, or a part of a pod.
  - `container` refines a pod subject ("container web"), never names one.
- **The most specific subject is the alert's subject.** `namespace` + `pod` is the Pod, and the namespace also lists it.
- **One subject only.** An alert with labels for two subjects (`pod` and `deployment`) takes the first in the table's order, and shows the rest as "also names".
- **Every match is labelled as what it is:** "matched by label `namespace`/`pod`". It is never UID-pinned.
  - When the alert's `startsAt` is earlier than the object's `creationTimestamp`, the alert started before this object existed: "started before this object was created (an earlier object of this name?)".
- **Not-found wording** follows §4's completeness rule.
- **A workload's inspector** also lists alerts naming its *current* pods, under "on its pods". The pods are the ones Klyx holds for that workload today, read complete; otherwise the list is qualified the same way.
- **An alert whose labels name no subject** is a cluster alert. It shows in the Alerts lens and in Needs you, with no object.

## 6. Counting: Needs you, separately

**Counted:** an alert counts in Needs you only when, after `Reduce`, it is **active** and its `severity` label is `critical` or `warning` (case-insensitive).
- Muted, silenced, inhibited and unprocessed alerts are never counted.
- An alert muted for some receivers and active for others is active, and counted.
- Other severities (`info`, `none`, or none at all) are listed in the Alerts lens and in inspectors, never counted.
  - kube-prometheus's `Watchdog` and `InfoInhibitor` (severity `none`) therefore never nag.
  - The lens says how many active alerts carry no counted severity.

**Kept apart from object faults:**
- **Needs you gets an `alert` row** and a third count: "3 faults · 2 need a look · 4 alerts".
  - A row reads: alert name, the subject or "cluster", severity, "started … ago", "last seen active …", and the `summary` annotation if present, all display values.
  - An alert row is never folded into an object's row, so one incident is never counted as several objects, and one object's alert is never counted as a fault.
- **The filters:** Faults keeps critical alerts; prd keeps the prd clusters' alerts.
- **The fleet spine and the cluster lamp do not light for an alert.** Only Klyx's own verdicts light them ("only faults glow"). An alert is external evidence.
  - The mast's reading adds an alerts count beside it: "2 alerts".
- **A failed or stale read** is a coverage row ("Alerts: could not be read - …", or "as of 14:02"), like any part's gap.
  - The same source's last good alerts stay counted, marked stale, so a dead Alertmanager never reads as calm.
- **A source change** before its first read is a coverage row: "Alerts: not read yet for this source".

## 7. What the operator sees

Every string below is a display value (§3).

- **The Alerts lens** is gated by the alerts part (§4). It sits under the cluster's observability group.
  - **Grouping:** active first, then unprocessed, then suppressed, each group ordered by severity then "started".
  - **A row:**
    - alert name, severity, and the subject (a link, labelled as a label match);
    - state and its cause;
    - "started … ago" and "last seen active …";
    - the `summary` annotation.
  - **Suppressed causes:**
    - "silenced (silence 3f1c…)";
    - "inhibited by 0a05fbb6…" (the inhibiting fingerprint, with its alert name when that alert is in the list);
    - "muted by time interval always" (every route muted);
    - and for an active alert, "muted for receiver …; active for …".
  - **The source line:**
    - the source (redacted per C5) and how it was resolved (configured, or discovered as `monitoring/main` through Service `kube-prometheus-stack-alertmanager`);
    - `match`, and the as-of time;
    - "N active alerts without a counted severity".
  - **Filters:** state, severity, namespace, and subject kind.
- **The alert inspector** shows:
  - the labels, sorted;
  - the annotations, including `description` and `runbook_url`;
  - the receivers and each one's state;
  - the fingerprint, `startsAt`, `updatedAt`, and the end as last reported;
  - `generatorURL` as copyable text, which Klyx does not open;
  - the subject correlation with its basis and §4's completeness wording.
- **The Alerts block in inspectors** (Pod, Workload, Node, and the generic inspector for Namespace, PVC, Job, CronJob and HPA) is placed by each inspector like `useInspectorAI`'s block.
  - It lists the alerts correlated to this object: active, unprocessed and suppressed, the suppressed ones marked as such.
  - It carries its as-of time and basis.
  - "No alert" is said only from a complete, live read (§4).
  - With the alerts part absent it renders nothing.
- **Settings > External endpoints:** Test is enabled for Alertmanager, through the separate status read (§4).
  - It reports, for example: "Available: Alertmanager 0.34.1, clustering disabled, answered." The version and cluster status are display values.
- **AI Explain** for an object adds its correlated active alerts as display values: alert name, severity, state, "started", "last seen active", and `summary`. They go through the field allowlist.
  - Labels beyond those and other annotations stay out.
  - Chat is unchanged in S8a. Its attention tool reports the alerts count only.
- **Verbs:** none. Klyx does not create or expire silences, does not acknowledge, and never posts alerts. Silencing stays in Alertmanager's UI and `amtool`.

## 8. Bounds

- **Reads:**
  - one snapshot read (two GETs) per cluster per 30 s, single-flight, within a 10 s deadline, cancelled when the connection or the Observer stops;
  - at most four alert reads in flight fleet-wide (a shared limiter in `fleet`); a read waiting on it counts against its own deadline.
- **Responses:**
  - each GET's response is capped at 8 MiB, which errors rather than truncating;
  - group appearances are capped at 20,000 per read, and past that the read fails, since mutes would be incomplete.
- **Entries:** 5,000 alerts after `match` and deduplication. Past it the list is truncated, and §4's "at least" and per-object qualifications apply.
- **Display caps, applied after redaction:**
  - 64 labels and 64 annotations per alert;
  - 256 bytes per label value, 512 for `summary`, 4 KiB for `description` and other annotations;
  - 2 KiB per URL.
  - The raw values used for matching are not clipped.
- **No per-alert follow-up reads.** Silences and inhibiting alerts are named from the alert's own fields.
- **Memory and DTOs:** the reduced list, in one generation, per cluster.
  - The wire caps bound what can be admitted: each GET is at most 8 MiB, so 5,000 alerts arrive at about 1.6 KiB each, and alerts at their display caps (some 300 KiB each) arrive a few dozen at a time.
  - The budget test measures the retained heap and the encoded DTO of the admitted worst shapes, not of an impossible product of every cap:
    - 5,000 alerts filling an 8 MiB response;
    - the most alerts at their display caps that fit in 8 MiB;
    - a groups response at its 20,000-appearance cap.
- **Discovery:** three API reads, at most once per five minutes per cluster, or after a failure or a change.

## 9. The wiring (C1)

**Backend:**
- `internal/endpointhttp`, with `internal/metrics` moved onto it;
- `internal/alertmanager` (pure);
- `fleet.endpointState` for `alertmanager`: the discovery, the source identity fact, and the fleet-wide limiter;
- `observe.ErrSourceAbsent`, a snapshot read's "not installed", and `RekeySnapshot` with the keyed `Read`, with their coverage tests;
- the `alerts` snapshot and the `alerts` task and part, classifier `alerts`, in `healthsrc`, with deps on the held subject kinds for the completeness wording;
- the rows converter and its DTO mirror;
- the AI allowlist entry;
- the chat attention count;
- Settings' Test (the status read) and `CanTest`;
- the `match` and `serviceRef.path` config fields, with validation and warnings.

**Frontend:**
- `clusterAttention.ts`: an `alerts` line state with its own counts, never `attention`;
- `queue.ts`: an `alert` row and an `alerts` count;
- `NeedsYou.tsx`: the third count;
- the mast's count;
- `reveal.ts` and `openHit.ts` for an alert;
- the Alerts lens and its registry entry, gated by the part;
- the shared Alerts block and its placement in the inspectors listed in §7;
- the smoke `TITLES` entry.

**Not wired:**
- `healthnotify`'s kind label;
- the fleet spine;
- the lamp.

## 10. Tests

- **Pure (`internal/alertmanager`):**
  - parse every state and the groups shape;
  - an unknown state kept;
  - each malformed shape failing the read whole.
  - **Reduce:**
    - covered and all appearances muted;
    - covered and some muted;
    - flat receivers [A, B] with only A, muted, in the groups: not covered, counted;
    - flat receivers [A, A] (two routes, one receiver), with both appearances muted, one muted, and only one present;
    - a receiver in the groups the flat list does not expect: not covered;
    - no appearance;
    - a fingerprint only in the groups;
    - silenced and inhibited not consulting the groups;
    - duplicate copies agreeing and disagreeing.
  - The subject table, each excluded label, precedence, and "also names".
  - Severity classes.
- **Display values:**
  - For every surface (alert name, label value, `summary`, `description`, `generatorURL`, `runbook_url`, receiver name, the status fields in Test, the reasons, the AI facts), three needles: the credential literal, the credential JSON-escaped in the body, and a credential with a quote and a backslash (its quoted spelling).
  - None reaches a DTO, a ref, Needs you, AI or Test, and the raw value still matches and correlates.
  - Userinfo is removed from every URL.
  - Clipping comes after redaction.
  - A synthetic credential equal to a namespace, a pod name or a fingerprint is redacted from display values, while the raw keys still match, deduplicate and correlate.
- **Transport:**
  - `internal/endpointhttp` carries C5's tests, and every metrics test passes unchanged.
  - New per-kind tests:
    - the Alertmanager caps and deadline across both GETs;
    - `filter` encoding;
    - the client-side `match` re-check against a server that ignores `filter`;
    - the path prefix through the service proxy;
    - the groups cap failing the read.
- **Discovery:**
  - two Alertmanager resources sharing one governing Service is ambiguous;
  - an instance Service is preferred;
  - the governing Service accepted only with its ownership proven by UID;
  - an endpoint without a `targetRef`, a Pod with another owner, or a StatefulSet not owned by the resource leaves it unproven;
  - a forbidden or failed list is "could not be read", never absent;
  - the operator not served, with no configuration, is absent.
- **Source identity:**
  - a prior *empty* success of source A, then a change to B, then:
    - an immediate capture before B replies, which reads `checking` and "not read yet", never "no alerts";
    - B failing, which reads `unreadable` with nothing of A and no A freshness;
    - B succeeding empty, which reads `live` with no alerts;
  - the same with a prior non-empty A;
  - a `serviceRef.path` change and a `match` change, each followed by a failed read;
  - a discovered source replaced;
  - A's read in flight completing after the re-key: discarded, freshness untouched;
  - a read whose resolved identity is not its generation's key: refused.
  - `RekeySnapshot` keeps Helm and Events unchanged (they never re-key).
- **Credentials:**
  - C5's Live, revision-fence and one-step-install tests repeated for `alertmanager` through `endpointState`;
  - Remove stopping an Alertmanager read in flight.
- **Recovery and completeness:**
  - a failed read keeps the same source's last list, stale, and never empties it;
  - a capped read counts "at least", and an object's "no alert" is qualified;
  - a subject of a held kind not found only when that kind is live and complete; stale, capped and unread kinds read "not established";
  - an unheld kind reads "named, not looked up".
- **Clock:** an alert renewed upstream (a later `endsAt`) across failed polls, and the local clock past the old `endsAt`. Nothing reads ending or resolved; the row reads "last seen active" at the last good read.
- **Counting:**
  - Needs you counts only active critical and warning alerts, apart from object faults;
  - a partly muted alert counted, a fully muted one not;
  - an object both faulty and alerting is one fault row and one alert row;
  - the lamp and spine stay unlit for an alert alone.
- **Notification:** the Observer-produced alerts batch, fed to `healthnotify`, opens no episode.
- **Test flight:**
  - a successful status read never advances the alerts freshness;
  - it never replaces a failed alerts result;
  - it triggers a separate fresh read.
- **Correlation:** the `startsAt`-before-`creationTimestamp` case, and a workload's "on its pods".
- **Parity:** the alerts part and the lens read the same snapshot. The parity oracle gains the alerts part, and `parityGate.test.tsx` its row.
- **Budget:** the full-shape heap and DTO case of §8.
- **Mutations** on:
  - the reduction's mute rule and its coverage check;
  - the re-key and the generation check;
  - the counting rule;
  - the `match` re-check;
  - the identity filter;
  - the stale-keep rule;
  - the completeness wording;
  - the display order (clip before redact);
  - the correlation precedence.

## 11. The lab

On `kind-klyx`, in namespace `klyx-s8-lab`: ClusterIP only, labelled `klyx.dev/lab=s8a`, with a teardown script.
- **Two real Alertmanager 0.34.1 instances:**
  - `alertmanager`, with route prefix `/`;
  - `alertmanager-prefixed`, under `/alertmanager`, read through a `serviceRef` path.
- **Their configuration:**
  - one route and receiver;
  - an inhibition rule: `KlyxLabCritical` inhibits `KlyxLabWarning` of the same namespace;
  - an always-on mute time interval for `klyx_lab_mute="true"`. It is routed to a receiver with an integration (a discard-port webhook that is never called), because the mute stage needs a notify pipeline.
- **A lab script posts synthetic alerts with `amtool`** inside each Alertmanager. That is lab setup through Alertmanager's own API: Klyx never posts. Each alert has `cluster=kind-klyx` and an end two hours out, re-posted by `relabs.sh`.
  - **The alerts:**
    - a pod alert naming the lab Deployment's pod;
    - a deployment alert, a node alert and a namespace alert (`info`);
    - a cluster alert;
    - a `severity: none` watchdog;
    - an inhibited pair;
    - a muted alert;
    - a silenced alert, with its silence;
    - an alert naming a pod that does not exist;
    - an alert for `cluster=elsewhere`, which `match` must keep out.
  - Each state has been read back through the service proxy: silenced and inhibited on both endpoints, muted only on the groups endpoint, and `cluster=elsewhere` filtered out.
- **The Prometheus Operator path** is covered by fixtures that copy the shapes v0.94.1 creates (its governing Service, StatefulSets and EndpointSlices), including two instances in one namespace. Klyx's lab does not run the operator.
- **The native smoke:**
  - opens the Alerts lens and checks each state group, the muted and the partly muted cases among them;
  - opens a pod's inspector to its Alerts block;
  - checks the Needs you third count;
  - runs Settings' Test for Alertmanager.
- **Live captures** of the lens, an inspector block and Needs you, in night and day at 1440 and 1024.

## 12. Staging

1. `internal/endpointhttp`, with metrics moved onto it. Every C5 test passes unchanged.
2. The config fields (`match`, `serviceRef.path`, honoured for metrics too), validation and warnings.
3. `internal/alertmanager`, pure: the parsers, `Reduce`, `Subjects`, `Severity` and `Display`, with their tests.
4. `fleet.endpointState` for Alertmanager: resolution, operator discovery, the source identity fact, the limiter, the credential path, Set and Test.
5. `observe.ErrSourceAbsent` and `RekeySnapshot` with the keyed `Read`; the `alerts` snapshot and part; the rows converter; the attention count for chat; the AI allowlist.
6. The frontend:
   - Needs you's alert row and third count, and the mast's count;
   - the Alerts lens and inspector;
   - the shared inspector block;
   - Settings' Test.
7. The lab, parity, the smoke step, captures, the budget measurement and the as-built notes.

## 13. Not in S8a

- OS notifications for alerts (roadmap: no).
- Silences or acknowledgements of any kind (read-only).
- Receiver routing views. The groups are read for mutes only.
- Alert history: what fired and stopped between reads.
- Prometheus or Grafana-managed rule state (`/api/v1/rules`): Alertmanager is the source the owner chose.
- Discovery without the Prometheus Operator: a standalone Alertmanager is configured with a `serviceRef`.
- Chat tools beyond the attention count.
- `receiver_matchers` (0.33.0 and later only).
- A configurable set of counted severities, for stacks that page on `page`, `error` or `high`. It is deferred until asked for; meanwhile the lens names the active alerts it does not count.

## 14. Revision 1: Codex's design review of 4bc4685, and the lab

| # | Finding | Where answered |
|---|---|---|
| 1 (P1) | Decoded labels and annotations can echo the credential into Needs you, inspectors and Explain; "not secrets" is no boundary | §3: raw values for matching only; every string that leaves the package is a display value (the read's own redactor, quoted spellings, `scrub.Credentials`, URL userinfo removed, then clipped). §10: three needles on every surface |
| 2 (P1) | `/api/v2/alerts` cannot report time-interval mutes (0.34.1 passes none); the lab's muted alert would read active and enter Needs you; dedupe across receivers was undefined | §1, §3: the read is `/alerts` for membership, silences and inhibitions, plus `/alerts/groups` for mutes. `Reduce`: every route muted is muted; some muted is active and counted; none read is "mute not read". The lab found the same independently (§11) |
| 3 (P1) | Source identity must cover the path, tenant and `match` scope; a failed first read after a change would keep counting the old source | §4: the source identity; items carry it; the part counts only the current identity's items; a change starts an unread collection. §10: path and `match` changes followed by failed reads |
| 4 (P2) | One `alertmanager-operated` Service is not one installation (its selector is namespace-wide) | §2: discovery through Alertmanager resources; an instance Service, or the governing Service only with its EndpointSlices proven; otherwise ambiguity. The label-only fallback is removed |
| 5 (P2) | Negative claims need complete, current evidence: absent discovery, "not found" subjects, omissions from a capped read | §2, §4: absent only from complete discovery; failed discovery is a failed read; "not found" only for held, live, complete kinds, else "not established" or "named, not looked up"; capped reads qualify per object; no history, so nothing claims "no longer matched" |
| 6 (P2) | An ending state or continued firing cannot be derived from an expired `endsAt` | §4: "started" and "last seen active"; `endsAt` is "end as last reported", never a state. §10: a renewed alert across failed polls |

**Codex's guidance, adopted:**
- the notification exclusion pinned with the Observer-produced batch (§4, §10);
- Settings' Test as a separate status read that never moves the alerts snapshot (§4);
- the gate read from the alerts part, on the snapshot's cadence, instead of a five-minute capability fact (§4);
- a full-shape heap and DTO measurement, named display caps, and a fleet-wide limit (§8).

Kept as Codex judged them sound:
- the `endpointhttp` extraction;
- `match` with the client re-check;
- the no-`match` option with its warning;
- the separate alerts count;
- no OS notifications;
- always-register with a proven-absence outcome.

## 15. Revision 2: Codex's re-review of a9cfe4e

| # | Finding | Where answered |
|---|---|---|
| 1 (P2) | "Every returned appearance muted" does not show every expected route was read. The flat receivers come from route matching now; the groups from existing groups, which the group limit can refuse | §1 (how the two lists are built), §3: a mute takes an alert out of Needs you only when its appearances, as a multiset of receivers, equal its flat `receivers`. Routes sharing a receiver count twice. Any mismatch is "mute evidence incomplete", active and counted. §10: [A, B] with only A, [A, A] variants, an unexpected receiver |
| 2 (P2) | Items carrying an identity cannot partition a read's outcome. Coverage, as-of time and truncation live in the snapshot's generation, and an empty read has no item | §4: `RekeySnapshot` gives each identity its own generation (no items, `checking`, no as-of time), and `apply`'s generation check discards an ended generation's outcomes. `Read` receives the key and refuses a mismatch; the envelope names the identity even when empty; the connection re-keys inside `endpointState`'s lock. §10: the prior-empty sequence Codex named, and an old completion after the re-key |

**Codex's guidance, adopted:**
- discovery's proof is ownership by UID, from `targetRef` through the Pod to the StatefulSet to the resource, never a name pattern (§2);
- raw keys stay separate from display values even when a credential equals a namespace, a pod name or a fingerprint (§10);
- the sizing test measures the admitted worst shapes under the 8 MiB wire caps (§8).

**Accepted at e3076c1** (Codex, design revision 2, handoff d753d6a2, 2026-10-19), with no remaining design findings. This accepts the design only; the implementation keeps its own review. Codex's implementation notes:
- **Single-flight across a re-key.** A re-key cancels the old generation, and its worker must finish before the new generation reads, so one cluster never has two alert reads in flight.
- **No appearance is incomplete,** including an empty expected receiver list, which must never make "every appearance muted" vacuously true.

## 16. As built

Built on `feat/coverage-s8` in seven stages after the design's acceptance at e3076c1:
- **e73e8dd** - `internal/endpointhttp`, with `internal/metrics` moved onto it.
- **3bba601** - `match` and `serviceRef.path`.
- **345e1ef** - `internal/alertmanager`.
- **59fc978** - the fleet's Alertmanager endpoint, discovery, identity and Settings.
- **7542eac** - the Observer's absent outcome and re-key, the alerts snapshot and part, rows, Explain and chat.
- **b37abf1** - the frontend.
- **Stage 7** (the commit carrying this section) - the parity fixture, the lab's partly muted alert and the smoke, the budget, the captures, the §10 tests still missing, and these notes.

### 16.1 The transport and the configuration (§2, §3)

- **`internal/endpointhttp` carries C5's transport unchanged in behaviour.**
  - It holds:
    - `Auth`, and the per-kind `Limits`;
    - the direct `Get`: the credential and tenant headers, the revision gate on every round trip, the completion re-check, and redacted errors, with bodies as sent;
    - `ReadCapped`;
    - a guarded service-proxy REST client per kind;
    - a proxy `Get` with a Service path prefix and repeated parameters.
  - `internal/metrics` keeps its API as thin wrappers. Every C5 test and Codex's C5 probes from all three reviews pass unchanged.
- **`match` and `serviceRef.path`:**
  - `match` is equality matchers on label names, with values non-empty and free of control characters.
  - `serviceRef.path` is a plain prefix: no query, fragment or dot segment. The metrics service proxy honours it too.
  - Both move the endpoint's revision.
  - Two clusters reading one Alertmanager or Loki endpoint, one of them without `match`, are warned about by name. The origin is compared canonically and the path as written.

### 16.2 The reader (§3, §4)

- **`internal/alertmanager`:**
  - `ParseAlerts` and `ParseGroups` are strict, and a malformed shape fails the read whole.
  - `Reduce` decides every state once:
    - membership comes from the flat list;
    - duplicates collapse: active, then unprocessed, then suppressed, with causes united and a disagreement kept;
    - silenced and inhibited alerts never consult the groups;
    - a mute takes an alert out of Needs you only when its group appearances, as a multiset of receivers, equal its flat receivers. Otherwise it is partial or incomplete, and counted. An empty expectation is never vacuously muted.
  - `SubjectOf` follows §5's table and its exclusions.
  - `Display` makes every outgoing string in C5's order: the read's redactor with quoted spellings, the general shapes, URL userinfo removed from the raw parse, then the clip.
- **`fleet`'s Alertmanager state:**
  - **Resolution:** an endpoint (C5's credential path, now `endpointAuth` for every kind), a `serviceRef`, or discovery through Alertmanager resources with UID ownership proven from each EndpointSlice `targetRef` through the Pod and StatefulSet to the resource. The route prefix is the path.
  - **A failed discovery read is a failure, never "none".**
  - **The source identity** re-keys the snapshot inside the state's lock when it changes. A credential-only change keeps it.
  - **`ReadAlerts`:**
    - one read per cluster at a time across re-keys, at most four fleet-wide;
    - `/alerts` then `/alerts/groups`;
    - the group-appearance and entry caps, the credential fence, and the `match` re-check.
  - **`AlertmanagerStatus`** serves Settings' Test.
- **The Observer:**
  - `ErrSourceAbsent` makes a snapshot's coverage absent. A later failure is unreadable.
  - `RekeySnapshot` starts a generation carrying a key: no items, checking, no as-of time. `ReadKeyed` receives the key, and the ended generation's outcomes are discarded by `apply`'s generation check.
  - The alerts part is never ledgered. An end-to-end test feeds the Observer's batches to `healthnotify`, and no episode opens.

### 16.3 Beyond the design, or decided while building

- **A read keyed otherwise always re-keys.** An Observer restarted with the empty key would otherwise refuse every read forever: the connection's identity had not changed, so the old code never re-keyed. A regression test pins it.
- **The lens's gate is carried, not probed.** The design read the gate from the alerts part's coverage. Lens gates read the cluster DTO's capabilities, so the connection carries the alerts read's own outcome there as `alertsPresent`, on the snapshot's cadence. It is set when an Alertmanager is configured, or the last keyed read resolved one, and cleared only by a read that found none. It is read lock-free, because a read holds the Alertmanager lock while discovery takes the connection lock.
- **Rows are an array with the source line as facts:** the rows protocol's shape.
- **The "mast's count" is the lens index's.** The open cluster's mast has no reading text in this version. The alerts count shows as a neutral badge on the Alerts entry in the lens index; the fleet indicator's reading lists the alerts line; Needs you has the third count. No surface lights for an alert alone.
- **The inspector block opens no subscription without an Alertmanager.** The generic inspector places it only for the kinds an alert can name.
- **A fake clientset's typed-nil proxy client** fails the read instead of panicking in the snapshot.
- **`safehttp.Redactor.Text`** redacts an exact secret inside a credential shape once: "token=[redacted]", never "[redacted]]".
- **The Alerts lens never drops its State column at a narrow width.** The state and its cause are the reading, so State is the lens's flex column and Summary goes first. The smoke at 1024 found the first layout hiding it; a column-fit test now pins it.

### 16.4 Verified

- **The lab:** two Alertmanager 0.34.1 instances in `klyx-s8-lab` on `kind-klyx` (§11; lab notes in `lab-s8a/FINDINGS.md`).
  - Every state was read back through the service proxy: silenced and inhibited on both endpoints, muted only on the groups, and `cluster=elsewhere` filtered out.
  - The lab gained a partly muted alert while building: two routes for one matcher, the first muted with `continue: true`. The groups show it suppressed for `lab-muted` and active for `lab`, so Klyx reads it partial and counts it. The smoke checks it beside the silence, the inhibition and the full mute.
  - The smoke fleet reads `klyx-s8-lab/alertmanager:web` with `match: {cluster: kind-klyx}`.
- **The parity gate** runs a real Alertmanager in `parity-alerting` with five posted alerts, three of them counted. The restricted identity of cases 7 and 8 gains only `services/proxy` get.
  - Both stages pass. The Go oracle sees three counted alerts in every case.
  - The frontend stage sees the rows equal the summary, and every alerts line read as "alerts", never attention.
  - The baseline's spine lamp stays healthy with three counted alerts.
- **The budget (§8), measured on the admitted worst shapes:**
  - 5,000 alerts in 7.7 MiB retain 21.3 MiB, against a 48 MiB bound.
  - 99 alerts at their display caps retain 8.3 MiB.
  - The part's evaluation with rows takes 4.3 ms against 5,000 pods.
  - A full rows message for 5,000 alerts encodes to 15.9 MiB. It is sent only to a subscribed lens or inspector, and only when the read changed. This is the cost to watch if a fleet runs near the cap.

- **Live captures** (headless Chrome against the server-mode build on the smoke fleet, night and day at 1440 and 1024): the lens, four alert inspectors (a found pod, a pod that is gone, an inhibited alert, a cluster alert), the pod inspector's Alerts block, and Needs you with its third count and alert rows. None scrolls sideways.
  - The captures found one fix: the block's "matched by label" caption was set in the uppercase legend style, and is now secondary text.
- **§10's tests not written by stage 6, added in stage 7:**
  - the re-key sequence after a prior *non-empty* read of A;
  - a re-key leaving an unkeyed sibling snapshot (Helm, Events) untouched;
  - Settings' Test asking the alerts snapshot for a separate read on success, and nothing on failure;
  - an alert whose last reported end has passed, across failing reads, read as "last seen active", never ending or resolved;
  - the State column kept at every width.

### 16.5 Gates

At stage 7:
- **The native smoke:** 185 of 185 steps in each of night and day at 1440 and 1024. The Alerts lens reads 12 rows (7 counted, 2 without a counted severity, 3 suppressed); Needs you reads "45 faults · 17 to look at · 7 alerts".
- **The parity gate:** both stages pass.
- **Go:**
  - `go test -race ./internal/...` passes;
  - golangci-lint v2.14.0 reports 0 issues;
  - gofmt is clean.
- **The frontend:**
  - tsc is clean;
  - eslint reports 0 errors (its three warnings predate S8a);
  - vitest: 2,287 tests in 192 files pass.
- **Mutations:**
  - 87 caught: 9, 7, 15, 22, 19 and 12 in stages 1 to 6, and 3 in stage 7 (Test's separate read, the stale wording, the State column).
  - Two are equivalent and recorded: a clause removed in stage 3 with its reason kept as a comment, and an absent snapshot's retained items, which never reach a part.

## 17. Implementation revision 1: Codex's review of 71c181c

Codex requested changes with eleven P2 findings and four notes. Each is answered below, with tests in the repository beside Codex's probes.

1. **A null response was a complete empty list.** Both parsers now require a top-level array; `null`, an object and an empty body fail the read whole.
2. **Counts read redacted display text.** A credential equal to "active" or "critical" changed the verdict.
   - `Display` carries Klyx's classes from the raw alert, `StateClass` and `SeverityClass`.
   - Counting, the Top order and roles, Explain's choice of active alerts, the lens's groups and words, and the inspector block all use them.
   - The display state and severity stay text, shown only for a state or severity Klyx does not class.
3. **The match left the reader raw.** `Read.Match` is now `DisplayMatch`: names and values redacted with the reading transport's redactor, then clipped. The raw match still filters and keys the identity.
4. **A failed re-discovery read Live through the old Service.** It now fails under the found source's identity, so the snapshot keeps that source's last list, stale. The "discovery could not be re-read" note path is gone.
5. **Discovery ignored the read's context.** It uses the cached discovery's `ServerResourcesForGroupVersionCtx` with the read's context.
6. **The proof never checked the targetRef UID.** Each endpoint's `targetRef` must name a Pod in the namespace by UID, and the Pod read must carry that UID. A same-name replacement proves nothing.
7. **The route prefix came from externalUrl.** The Prometheus Operator v0.94.1 always passes `--web.route-prefix`: `spec.routePrefix`, else "/". Klyx follows that; `externalUrl` is never the route.
8. **Failures hid behind an empty list or a skeleton.**
   - The stale banner shows whatever the last good read held; an empty one reads "No alerts in the last good read."
   - A rows subscription failure with no rows is "Could not read alerts." with its error, and with rows a banner.
   - The inspector block states a failed read too.
9. **"No alert matches this object" was claimed from an incomplete join.**
   - The block says it only when all of these hold:
     - the alerts read is live and complete;
     - the object's own kind is live and complete in the alerts part's dependencies;
     - a workload's pods are live and complete, and so is the workload's own pod set (`podsComplete`);
     - no alert names an object Klyx cannot show (a redacted subject).
   - Otherwise it says why it is not established.
   - An alert whose subject is this object by kind and name only is listed and marked "by name only". That covers a kind Klyx does not hold, or a held one it could not establish.
10. **Selection and reveal were keyed by fingerprint.**
    - Every alert carries an opaque key: its source identity and raw fingerprint, hashed.
    - Rows, Top refs, the lens's selection and cursor, and the reveal all use it.
    - The row facts carry the source's opaque key, never its identity. A source change ends the selection, and the first source back does not reopen it.
    - Codex's mounted probe builds alerts without the key; the same scenario is in `AlertsView.test.tsx` with keys.
11. **Explain omitted name-correlated alerts.** Explain receives the freshly fenced object's kind, namespace and name with its UID, and correlates on the raw subject: display values never decide a join.
    - Jobs, CronJobs, PVCs and HPAs, and held subjects not established, are included by kind and name. The qualification says a same-name object would share them.
    - Rows keep the raw subject in process only; the rows DTO never carries it.
    - Codex's probe sets only the display subject; the same case, with the raw subject and a redacted display name, is in `alerts_s8a_test.go`.

The notes:

- **Discovery cost.** The fleet-wide slot now covers the whole read, discovery included.
  - The governing Service's proof reads each endpoint's Pod (at most 16 endpoints) and its StatefulSet once.
  - It runs at most every five minutes (the discovery cache).
- **The rows resent every poll.** Correcting §16.4: the rows were not sent "only when the read changed".
  - A snapshot's coverage carries its read time, so every successful read moves the part's version, and the 5,000-alert rows message (15.9 MiB) went out every 30 s to a subscribed lens.
  - The alerts part now travels as rows deltas (`deltaParts`), keyed by the alert's opaque key. An unchanged read sends the part and facts with no rows.
  - `Read.ReadAt` is gone: the snapshot's coverage (`AsOf` while live, `LastGoodAt` when stale) is the read's time, for the lens and for Explain.
- **A stale subject read presented as found.** A held object whose kind is not live and complete is now "last observed": the UID kept, the state qualified in the lens, the block and Explain.

Mutations for this revision:
- Go, 18 caught: the array check (both lists), each class use, the key's source, the ref key, last observed, the display match, discovery's context, the targetRef UID and namespace, the route prefix, the alerts deltas, Explain's raw join, its not-established case and its class, and a failed re-discovery re-keying.
- Frontend, 11 caught: the held-kind, pod-set and redacted checks, the source-change clear, the rows-error state, the stale banner, the class grouping, the block's failure line, the name correlation, and the delta key.
- One frontend mutant is equivalent: a reveal also checking display fingerprints, which never equal a key.

Gates for this revision:
- **The native smoke:** 185 of 185 steps in each of night and day at 1440 and 1024.
- **The parity gate:** both stages pass (the manifest is written only after every case passed).
- **Go:** `go test -race ./internal/...` passes, and golangci-lint reports 0 issues.
- **The frontend:** tsc is clean, and eslint has 0 errors (its three warnings predate S8a). Vitest passes 2,292 tests.
- **Codex's probes:**
  - the Go probes pass, except Explain's, whose fixture predates the raw subject;
  - four of the five mounted probes pass; the source probe's alerts carry no key.
  - Both scenarios are covered with the new contract in the repository's tests.

## 18. Implementation revision 2: Codex's re-review of bf78ea0

Codex confirmed the eleven findings fixed, its probes adapted to the new contract, and named four residual issues:

1. **A name match inferred display safety from the redaction marker's text.** A clip can cut the marker: a 253-byte PVC name ending in the credential shows as prefix + "[re…".
   - `DisplaySubject.Exact` says whether namespace and name are shown as written, neither redacted nor clipped. It travels as `subject.exact`.
   - The lens compares names only for an exact subject.
   - An inexact subject that could be the object makes "No alert matches this object" not established: same kind, any subject for a namespace, or a pod for a workload.
2. **Outdated rows read as live.** A failed re-bootstrap after a gap kept the prior rows labelled live.
   - `rowsOutdated` reads the rows' `refreshError` and the counts' refreshing, resync and updating states.
   - In the lens, outdated rows show "Rows as of HH:MM - re-reading; the counts are the cluster summary's. Last re-read failed: …", and the source line drops "live".
   - The inspector block names the failure, says "rows as of", and makes no negative claim until the rows catch up.
3. **The block never said "last observed".** An alert line on the object or on its pods now says "the subject as last observed (its kind's read is not current)".
4. **A re-key to a source never read did not end the selection.** Any change of the source key ends it, including to none: a re-keyed generation not read yet carries no facts. The first source back does not reopen it.

Codex's ten mounted probes pass as written. Nine mutations were caught: the exact requirement, the inexact block and its scope, the outdated claim, the block's and the lens's outdated words, the block's last observed, the unread-generation boundary, and the exact flag itself.

## 19. Implementation revision 3: Codex's re-review of 56455fa

Codex confirmed the four residual issues fixed (its ten earlier mounted probes pass unchanged), and named two more:

1. **A found pod's UID does not establish its namespace.**
   - A credential equal to the namespace redacts it. The pod still resolves by UID, but a Namespace inspector cannot match the redacted namespace text.
   - For a namespace target, every inexact subject now blocks "No alert matches this object", UID or not. The UID resolves the pod, never its namespace.
   - The pod's own inspector still matches it by UID.
2. **The lens's headline counts came from the rows' part.**
   - While the rows are behind, the selected counts are the newer summary's (`taskCounts`).
   - The headline counts and their "+" now come from `lr.counts.part`, with the summary's own cap. The rows, their coverage and facts stay the rows' own.
   - An empty set of rows behind the cluster reads "No alerts in the rows held; they are behind the cluster summary." Its hint names the summary's count. It is never the current "No alerts on this Alertmanager for this cluster."

Codex's fourteen mounted probes pass as written. Four mutations were caught: the namespace guard, the headline's part, its cap, and the outdated empty wording.

## 20. Implementation revision 4: Codex's re-review of cb30938

Codex confirmed both fixes; all fourteen earlier mounted probes pass. One P2 remained in the same outdated path: a historical "no Alertmanager" row part overrode a newer summary.
- The rows held could be an absent part from before an Alertmanager was found, behind a summary now counting seven alerts.
- That old absence still said "No Alertmanager is configured or found on this cluster." and hid the selected count.

The fix:
- Absence is said only from the read that applies now: the rows' own part while they are current, the selected counts' part while they are behind.
- A historical absence reads as the outdated empty state, under the summary's headline.

Codex's fifteen mounted probes pass as written (its new one drives the real health store's bootstrap failure). One mutation was caught: absence from the rows' part.
