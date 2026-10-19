# Coverage S8a: alerts, from Alertmanager

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S8a, after C5 (`2026-10-18-klyx-coverage-c5-external-credentials.md`, implementation accepted at 6c17617). S8b (Loki) follows with its own note.

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
- **The periodic read Klyx already has.** The Observer's snapshot (`observe.SnapshotSpec`: a named cadence, a read deadline, single-flight, with `ReadSnapshot` and `Refresh` for on-demand reads) carries Helm (5 min) and Warning Events (60 s).
  - A snapshot source is `ModeSampled`, and `healthnotify.Apply` returns before any episode logic for a sampled batch (`internal/healthnotify/engine.go:300-302`).
- **Needs you.**
  - `fleetQueue` (`frontend/src/fleet/queue.ts:61-120`) adds rows only for lines in the `attention` state and counts `faults` and `attention`.
  - An evidence part (Events) is computed but never reaches Needs you.
  - There is no third count. A row's key already allows a ref without a UID.
- **Inspectors.**
  - `InspectorLayout` has no shared section slot. Each inspector lists its own blocks.
  - `useInspectorAI` is the precedent for a per-object block every inspector places itself.
- **The lab has no Alertmanager.** C6 plans one (a small single binary) on `kind-klyx`.

Alertmanager's own API (OpenAPI on `main`, releases through 0.34.1 of 2026-09-17), as S8a uses it:
- **`GET /api/v2/alerts`** takes `active`, `silenced`, `inhibited` and `unprocessed` (all default true), `filter` (matchers, repeatable), and `receiver`.
- **A `gettableAlert`** has:
  - `labels`, `annotations`, `fingerprint`;
  - `startsAt`, `updatedAt`, `endsAt`;
  - `receivers`, `generatorURL`;
  - `status{state: active|suppressed|unprocessed, silencedBy[], inhibitedBy[], mutedBy[]}`.
- **`mutedBy`** (time-interval muting) is reported since 0.28.0. An older Alertmanager omits it, and Klyx treats the field as optional.
- **`GET /api/v2/status`** returns `versionInfo`, `cluster{status: ready|settling|disabled, peers}` and `uptime`.

## 2. Configuration: which alerts are this cluster's

The `alertmanager` block keeps C5's shape: `endpoint`, `serviceRef`, `tlsSkipVerify`, `auth`, `tenant`. S8a adds two fields:

- **`match`**, the labels that make an alert this cluster's in a shared Alertmanager, e.g. `match: {cluster: aks-prd-weu}`.
  - Equality matchers only: a valid label name, and a non-empty value without control characters.
  - Sent as `filter` parameters (`cluster="aks-prd-weu"`), and checked again on every alert Klyx receives. An Alertmanager-compatible API that ignores `filter` cannot leak another cluster's alerts in.
  - `match` belongs to `EndpointConfig`, so S8b's Loki block takes the same field. It counts in C5's endpoint revision.
- **`serviceRef.path`**, a path prefix behind the Service: Mimir's Alertmanager is served under `/alertmanager`.
  - The same field is honoured by the metrics service proxy, which has the same need (Mimir's Prometheus API under `/prometheus`). One shape, one rule.

**Without `match`,** every alert on the Alertmanager is read as this cluster's. That is right for an Alertmanager inside the cluster, and the source line says so: "every alert on this Alertmanager is read as this cluster's". Two clusters configured with the same Alertmanager and no `match` get a load warning naming both.

**Resolution follows metrics':**
1. an `endpoint`;
2. else a `serviceRef` through the API server's service proxy;
3. else discovery.

**Discovery is deliberately narrow:**
- the Prometheus Operator's `alertmanager-operated` Service, the one it creates for every Alertmanager resource, through its port named `web` (it also carries the mesh ports, so "a single port" would not fit);
- else a single Service labelled `app.kubernetes.io/name=alertmanager` with a port named `web` or `http`, or exactly one port.

More than one candidate reads "several Alertmanagers found; set alertmanager.serviceRef". Mimir, Grafana and other path-prefixed or multi-tenant Alertmanagers are configured explicitly.

## 3. The reader

**One kind-neutral endpoint transport** replaces the metrics-only plumbing rather than copying it. A second copy of the credential, redirect, cap and redaction code would be two places to keep the C5 contract.
- `internal/endpointhttp` takes the pieces C5 built and accepted, unchanged in behaviour:
  - the direct transport: auth header, tenant header, the `Live` gate on every round trip, the completion re-check, and error redaction;
  - the guarded proxy REST client: same-origin redirects, and a cap below client-go's decoding;
  - capped reads that error.
- They are offered as `Get(ctx, path, query)`, with the cap, timeout and too-large error per kind.
- `internal/metrics` keeps its API and becomes a caller of it. Every C5 test passes unchanged; that is the gate on the refactor.
- **Per kind:**
  - metrics keeps 4 MiB and a 3 s timeout;
  - Alertmanager reads up to 8 MiB within 10 s.

**One endpoint state per kind on the connection.**
- `fleet` gets a small `endpointState` (its configuration, resolution cache, credential revision, and its own lock) for `alertmanager`, and later `loki`.
- It has the metrics path's guarantees:
  - a read drops a result that crossed a revision move;
  - a configuration change moves the revision inside the lock;
  - a credential and the reviewed configuration are installed as one step.
- Metrics keeps its own state for now. Moving it onto `endpointState` is a later cleanup, not a precondition.

**`internal/alertmanager` (pure):**
- `ParseAlerts(body)` to `[]Alert`, tolerant of a missing `mutedBy` and strict about everything else.
- `Alert`:
  - `Fingerprint`, `Labels`, `Annotations`;
  - `StartsAt`, `UpdatedAt`, `EndsAt`;
  - `State` (`active`, `suppressed`, `unprocessed`, or the unknown value kept as written);
  - `SilencedBy`, `InhibitedBy`, `MutedBy`;
  - `Receivers`, `GeneratorURL`.
- `ParseStatus(body)` for Settings' Test.
- `Dedupe`, by fingerprint: one alert per fingerprint. Where two copies disagree, the copy with the later `updatedAt` stands, and the disagreement is kept as "replicas disagreed" on that alert.
- `Subjects(labels)`, the correlation (§5).
- `Severity(labels)`, the counting class (§6).

**The read:**
- `GET {base}/api/v2/alerts?active=true&silenced=true&inhibited=true&unprocessed=true&filter=…`, through the resolved transport.
- The re-check of `match`, then deduplication.
- Labels and annotations are kept as sent: they are data and display text, not secrets.
- A response that is not JSON, not an array, or holds an alert without `fingerprint`, `labels` or `status.state` is a failed read, never a partial list.

## 4. The source contract (C1)

- **Acquisition mode:** an Observer snapshot, `alerts`.
  - It reads every 30 s with a 10 s deadline, single-flight per cluster.
  - `ReadSnapshot` serves the Alerts lens's refresh and Settings' Test through the same flight.
  - It is always registered. A read that finds no Alertmanager, configured or discovered, returns a new `observe.ErrSourceAbsent`. The snapshot's coverage is then `absent`: not installed, no evidence, no gap, kept distinct from a failed read.
  - The connection keeps a capability fact, `Alerting{Source: configured|discovered|none, Endpoint}`, refreshed on the capability re-probe, never per read. The Alerts lens's gate reads it.
  - A change of source (discovery finding an Alertmanager, or one going away) needs no Observer restart: the next read resolves again.
- **Scope, projection and versions:**
  - Scope is this cluster's alerts, as `match` defines them (§2).
  - The projection is the parsed `Alert`; nothing else is held.
  - The API is v2 only. v1 was removed in 0.27.0 and returns 410, which reads as "Alertmanager's v1 API is gone; Klyx reads v2".
  - An unknown `state` is kept and shown, never counted as active.
- **The counted entity and its identity:** an alert, identified by its fingerprint on its endpoint.
  - The endpoint's identity is the cluster, the canonical origin or `namespace/service:port/path`, and the tenant.
  - A fingerprint is Alertmanager's hash of the labels: the same labels firing again later is the same fingerprint, and `startsAt` tells the occasions apart.
  - The ref has no UID and no GVR of its own. Its subject is a correlation (§5).
- **Dependency completeness:**
  - **The endpoint unresolved, unreachable, refusing the credential, or answering non-JSON:** the snapshot's read fails. The part reads "could not be read" with the reason (redacted per C5), and the last good list is kept, marked stale with its as-of time.
  - **Never cleared, never recovered.** A failed read never empties the list and never turns an active alert into "resolved".
  - **A subject's object missing from the cache** (not listed, not watched, or deleted): the alert stays, and its subject reads "names pod team/api-1, not found in this cluster now".
- **Clock boundaries:**
  - The list is as of its read.
  - An alert whose `endsAt` has passed while it is still listed reads "ending" (Alertmanager keeps a resolved alert until its next cycle).
  - "Firing for" ages from `startsAt` on the clock, never from Klyx's first sight.
  - The snapshot's freshness (cadence plus grace) marks a list stale when reads stop arriving.
- **Notification eligibility:** none. The source is sampled, and `healthnotify` drops sampled batches before any episode logic. A test pins it.
  - An alert fingerprint is not a Kubernetes UID. Notifying on alerts would need its own design, and the roadmap keeps it out of this programme.
- **No synthetic evidence:**
  - Klyx never invents a state, a severity, a subject or a time.
  - A missing `severity` label is "no severity", not "warning".
  - A missing `mutedBy` is "not reported by this Alertmanager", not "not muted".

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
  - `container` refines a pod subject ("container app"), never names one.
- **The most specific subject is the alert's subject.** `namespace` + `pod` is the Pod, and the namespace also lists it.
- **One subject only.** An alert with labels for two subjects (`pod` and `deployment`) takes the first in the table's order, and shows the rest as "also names".
- **Every match is labelled as what it is:** "matched by label `namespace`/`pod`". It is never UID-pinned.
  - When the alert's `startsAt` is earlier than the object's `creationTimestamp`, the alert started before this object existed: "started before this object was created (an earlier object of this name?)".
- **A workload's inspector** also lists alerts naming its *current* pods, joined by the pods Klyx holds for that workload today, under "on its pods".
- **An alert whose labels name no subject** is a cluster alert. It shows in the Alerts lens and in Needs you, with no object.

## 6. Counting: Needs you, separately

**Counted:** an alert counts in Needs you only when it is **active** and its `severity` label is `critical` or `warning` (case-insensitive).
- Other severities (`info`, `none`, or none at all) are listed in the Alerts lens and in inspectors, never counted.
- kube-prometheus's `Watchdog` and `InfoInhibitor` (severity `none`) therefore never nag.
- The lens says how many active alerts carry no counted severity.

**Kept apart from object faults:**
- **Needs you gets an `alert` row** and a third count: "3 faults · 2 need a look · 4 alerts".
  - A row reads: alert name, the subject or "cluster", severity, "firing for", and the `summary` annotation if present.
  - An alert row is never folded into an object's row, so one incident is never counted as several objects, and one object's alert is never counted as a fault.
- **The filters:** Faults keeps critical alerts; prd keeps the prd clusters' alerts.
- **The fleet spine and the cluster lamp do not light for an alert.** Only Klyx's own verdicts light them ("only faults glow"). An alert is external evidence.
  - The mast's reading adds an alerts count beside it: "2 alerts".
- **A failed or stale read** is a coverage row ("Alerts: could not be read - …", or "as of 14:02"), like any part's gap.
  - The last good alerts stay counted, marked stale, so a dead Alertmanager never reads as calm.

## 7. What the operator sees

- **The Alerts lens** is capability-gated: an Alertmanager configured or discovered. It sits under the cluster's observability group.
  - **Grouping:** active first, then unprocessed, then suppressed, each group ordered by severity then "firing for".
  - **A row:** alert name, severity, subject (a link, labelled as a label match), state and its cause, "firing for", and the `summary` annotation.
  - **Suppressed reasons:**
    - "silenced (silence 3f1c…)";
    - "inhibited by 9a2e…" (the inhibiting fingerprint, with its alert name when that alert is in the list);
    - "muted by a time interval".
  - **The source line:** the endpoint (redacted per C5), `match`, the as-of time, and "N active alerts without a counted severity".
  - **Filters:** state, severity, namespace, and subject kind.
- **The alert inspector** shows:
  - the labels, sorted;
  - the annotations, including `description` and `runbook_url`;
  - the receivers, the fingerprint and the timestamps;
  - `generatorURL` as copyable text, which Klyx does not open;
  - the subject correlation with its basis.
- **The Alerts block in inspectors** (Pod, Workload, Node, and the generic inspector for Namespace, PVC, Job, CronJob and HPA) is placed by each inspector like `useInspectorAI`'s block.
  - It lists the alerts correlated to this object: active, unprocessed and suppressed, the suppressed ones marked as such.
  - It carries its as-of time and basis.
  - With no Alertmanager it renders nothing (capability detection).
- **Settings > External endpoints:** Test is enabled for Alertmanager. It reads `GET /api/v2/status` through the reviewed endpoint, under C5's review fence and configuration sync.
  - It reports: "Available: Alertmanager 0.34.1, cluster ready with 3 peers, answered."
- **AI Explain** for an object adds its correlated active alerts: alert name, severity, state, "firing for", and `summary`, through the field allowlist. Labels beyond those and other annotations stay out.
  - Chat is unchanged in S8a. Its attention tool reports the alerts count only.
- **Verbs:** none. Klyx does not create or expire silences, does not acknowledge, and never posts alerts. Silencing stays in Alertmanager's UI and `amtool`.

## 8. Bounds

- **One read per cluster per 30 s**, single-flight, with a 10 s deadline, and cancelled when the connection or the Observer stops.
- **The response cap is 8 MiB**, which errors rather than truncating.
- **The entry cap is 5,000 alerts after `match`.** Past it, the list is truncated and every count reads "at least".
- **No per-alert follow-up reads.** Silences and inhibiting alerts are named from the alert's own fields.
- **Memory:** the parsed list, in one generation, per cluster.
- **Evaluation:** the alerts part's evaluation is linear in the alerts and the subject lookups, which come from cache maps. It is measured in the slice's budget test against 5,000 alerts.

## 9. The wiring (C1)

**Backend:**
- `internal/endpointhttp`, with `internal/metrics` moved onto it;
- `internal/alertmanager` (pure);
- `fleet.endpointState` for `alertmanager`;
- `observe.ErrSourceAbsent`, a snapshot read's "not installed", with its coverage tests;
- the `Alerting` capability fact and the lens gate;
- the `alerts` snapshot and the `alerts` task and part, classifier `alerts`, in `healthsrc`;
- the rows converter and its DTO mirror;
- the AI allowlist entry;
- the chat attention count;
- Settings' Test and `CanTest`;
- the `match` and `serviceRef.path` config fields, with validation and warnings.

**Frontend:**
- `clusterAttention.ts`: an `alerts` line state with its own counts, never `attention`;
- `queue.ts`: an `alert` row and an `alerts` count;
- `NeedsYou.tsx`: the third count;
- the mast's count;
- `reveal.ts` and `openHit.ts` for an alert;
- the Alerts lens and its registry entry;
- the shared Alerts block and its placement in the inspectors listed in §7;
- the smoke `TITLES` entry.

**Not wired:**
- `healthnotify`'s kind label;
- the fleet spine;
- the lamp.

## 10. Tests

- **Pure (`internal/alertmanager`):**
  - parse every state;
  - a missing `mutedBy`;
  - an unknown state kept;
  - each malformed shape failing the read whole;
  - deduplication, including disagreeing replicas;
  - the subject table, each excluded label, precedence, and "also names";
  - severity classes.
- **Transport:**
  - `internal/endpointhttp` carries C5's tests, and every metrics test passes unchanged.
  - New per-kind tests for:
    - the Alertmanager cap and deadline;
    - `filter` encoding;
    - the client-side `match` re-check against a server that ignores `filter`;
    - the path prefix through the service proxy.
- **Credentials:**
  - C5's Live, revision-fence and one-step-install tests repeated for `alertmanager` through `endpointState`;
  - Remove stopping an Alertmanager read in flight.
- **Recovery:**
  - a failed read keeps the last list marked stale, and never empties it;
  - an alert absent from the next good read is gone, never "resolved";
  - a truncated read counts "at least".
- **Counting:**
  - Needs you counts only active critical and warning alerts, apart from object faults;
  - an object both faulty and alerting is one fault row and one alert row;
  - the lamp and spine stay unlit for an alert alone;
  - suppressed and unprocessed alerts are never counted.
- **Notification:** a sampled alerts batch never reaches an episode.
- **Correlation and time:**
  - the `startsAt` before `creationTimestamp` case;
  - a subject not found;
  - a workload's "on its pods".
- **Parity:** the alerts part and the lens read the same snapshot. The parity oracle gains the alerts part, and `parityGate.test.tsx` its row.
- **Budget:** 5,000 alerts parsed and evaluated within the slice's budget.
- **Mutations** on the state classes, the counting rule, the `match` re-check, the stale-keep rule and the correlation precedence.

## 11. The lab

- **`kind-klyx` gets a real Alertmanager 0.34.1** in namespace `klyx-s8-lab`. It is a ClusterIP Service, labelled with a teardown script.
- **Its configuration** gives the states the slice needs:
  - one route and receiver;
  - an inhibition rule (a `KlyxLabCritical` alert inhibits a `KlyxLabWarning` of the same namespace);
  - a mute time interval that always applies, routed by a label, so `mutedBy` is real.
- **A lab script posts synthetic alerts** to Alertmanager's own `/api/v2/alerts`. That is lab setup, not Klyx: Klyx never posts. Each alert has `cluster=kind-klyx` and an `endsAt` two hours out, re-posted by `relabs.sh`.
  - The alerts are:
    - a pod alert, a deployment alert, a node alert and a namespace alert;
    - a cluster alert;
    - a `severity: none` watchdog;
    - an inhibited pair;
    - a muted alert;
    - an alert for another cluster (`cluster=elsewhere`), which `match` must keep out.
  - The script also creates a silence through Alertmanager's API.
- **A second, `match`-less Alertmanager** is reached through the service proxy with a `serviceRef` path prefix, to cover the prefix.
- **The native smoke:**
  - opens the Alerts lens and checks each state group;
  - opens a pod's inspector to its Alerts block;
  - checks the Needs you third count;
  - runs Settings' Test for Alertmanager.
- **Live captures** of the lens, an inspector block and Needs you, in night and day at 1440 and 1024.

## 12. Staging

1. `internal/endpointhttp`, with metrics moved onto it. Every C5 test passes unchanged.
2. The config fields (`match`, `serviceRef.path`, honoured for metrics too), validation and warnings.
3. `internal/alertmanager`, pure, with its tests.
4. `fleet.endpointState` for Alertmanager: resolution, discovery, the credential path, Set and Test.
5. The `alerts` snapshot and part, the rows converter, the attention count for chat, and the AI allowlist.
6. The frontend:
   - Needs you's alert row and third count, and the mast's count;
   - the Alerts lens and inspector;
   - the shared inspector block;
   - Settings' Test.
7. The lab, parity, the smoke step, captures and the as-built notes.

## 13. Not in S8a

- OS notifications for alerts (roadmap: no).
- Silences or acknowledgements of any kind (read-only).
- Alert groups (`/api/v2/alerts/groups`) and receiver routing views.
- Prometheus or Grafana-managed rule state (`/api/v1/rules`): Alertmanager is the source the owner chose.
- Chat tools beyond the attention count.
- `receiver_matchers` (0.33.0 and later only).
- A configurable set of counted severities, for stacks that page on `page`, `error` or `high`. It is deferred until asked for; meanwhile the lens names the active alerts it does not count.
