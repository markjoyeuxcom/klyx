# Klyx Settings S3b: Loki and metrics installations and subscriptions

Opened 2026-10-28, on `feat/settings-s3b`, with S3a-1 to S3a-4b accepted and S3a-4c in review. S3b is the redesign's second observability slice (redesign §6, §14): Loki and metrics on the model S3a built for Alertmanager, "with their per-subscription acquisition". It builds, for Loki and metrics:
- installations and subscriptions: the kinds, the schema, resolution and scopes;
- per-subscription acquisition through an installation's transport and tenant, scoped to the member;
- the scope sentence where logs and metrics are shown;
- lineage on every AI read of logs and metrics, and the fences that end later sends when the terms move: S3a §0's gap, closed for Loki and metrics;
- Settings: the installations and subscriptions of each kind, their plans, credentials, Test, Detect for metrics, and the migration offer.

This note is for Codex's review before anything is built.

## 0. What exists

From a read of the code at bfe29154 (S3a-4c's head).

**Configuration** (`internal/config`):
- `clusters[].loki` is `EndpointConfig`, the Alertmanager shape: `endpoint`, `serviceRef`, `auth`, `tlsSkipVerify`, `tenant`, `match`, and S3a's `source`, `scope` and `off`, which are refused for Loki as written ("loki.source is not built yet", `sources.go:134-143`).
- `clusters[].metrics` is its own `MetricsConfig`: `endpoint`, the legacy `token`, `tlsSkipVerify`, `serviceRef` (with `path`), `auth`, `tenant`. **It has no `match`, `source`, `scope` or `off`** (`config.go:137-157`); any of them is an unknown key, a load error (`strict.go`).
- `sources[].kind` is `alertmanager`; `loki` and `metrics` are refused as "not built yet" (`sources.go:75-83`). The registry gives Loki's subscription keys the disposition deferred, S3b's (`registry.go:177-193`).
- Loki is never discovered (`endpoints.go:109-113`, `fleet/loki.go:78-81`); metrics is, when its block has no locator (`fleet/metrics.go:101-148`: named Services in `monitoring`, then two selectors; several matches are unavailable).
- `sharedWithoutMatch` skips metrics (`endpoints.go:249`).

**Metrics reading** (per `ClusterConn`):
- `metrics.Resolve`: the endpoint, else the serviceRef through the cluster's own API server, else discovery (`metrics/resolve.go:28-60`). The tenant is `X-Scope-OrgID` on both transports; responses are capped at 4 MiB (`metrics/transport.go`).
- A cache on the connection: the capability for the connection's life, unavailable for 45 s, samples for 15 s (`fleet/metrics.go:16-23, 132-171`). The credential revision `(cluster, metrics)` is checked after each read (`:177-201`).
- **No template carries a cluster label or a match**: cluster CPU and memory, workload usage, node utilization, pod container usage, sparklines, Envoy route and rule metrics, Hubble, and chat's `get_metrics` (`fleet/metrics.go`, `workload_metrics.go`, `node_metrics.go`, `sparklines.go`, `routemetrics.go`, `rulemetrics.go`, `hubble.go`, `appbridge/chat_metrics.go:205-215`). Selectors are built by `metrics.Equals`/`MatchAnyOf` (`metrics/promql.go`) inside hand-written format strings; some selectors are bare (`count(envoy_cluster_upstream_rq_total)`, `routemetrics/envoy.go:67`).
- **So a shared metrics tenant mixes clusters today:** two clusters reading one Mimir tenant see each other's series in every chart.

**Loki reading** (per `ClusterConn`):
- `Loki()` builds the reader per call, a direct endpoint or the guarded proxy with the tenant (`fleet/loki.go:132-174`); 8 MiB a page, four pages fleet-wide and two per cluster (`:31-44`). Its identity is `how|at|tenant|match` (`alerting.go:247`).
- The block's `match` leads every selector (`loki/loki.go:112-125`); there is no default cluster label.
- History by name or by UID, at most 7 days, pages of 1000 lines, 16 views (`appbridge/loghistory_service.go`). A view locks the reader's `LineageKey()` and revision and starts again when either moves (`:308-327`).
- `get_log_history`: 15 m to 6 h, 200 lines, a UID only from the conversation's ledger, scrubbed (`chat_loghistory.go`).

**Credentials:** `endpoint/<cluster>/<kind>` per cluster and `source/<id>` per installation (`endpointcred.go:20-24`), with revisions keyed `(cluster, kind)`, an installation's as `("source/<id>", kind)`. Test reads Loki's build info and label names in the match over the last hour, never lines (`fleet/loki.go:305-338`); metrics' Test is the capability probe (`endpoints_service.go:565`).

**Lineage and fences:**
- Only alerts carry a lineage (`alertmanager.Lineage`), recorded by `get_alerts` and Explain, checked by the turn's guard and A3's plan fence (`chat_turn.go:227-236`, `chat_service.go:584-590`).
- `get_metrics` and `get_log_history` record nothing. Their reads check the turn's fence (connection, epoch, AI revision, egress, members), never the source. **Retained logs and metrics are sent again after the endpoint, its tenant, its match or its credential changes** (S3a §0's gap, left to S3b).

**S3a's code** is generic in its shape (`SourceConfig`, the `Subscription` record, the credential registry, the lineage's fields) and Alertmanager-specific in its functions: `ResolveAlertmanager` and its scopes read `cl.Alertmanager`; buckets, `LineageProblem`, the lineage holders, `SourcesService` and the page are typed to alerts.

**Consumers** that resolve per cluster today:
- metrics: `MetricsService` (Overview, Nodes, Pods), `WorkloadsService.GetWorkloadMetrics`, `GatewayService` (route metrics, Hubble, rule metrics), chat's `get_metrics` and `route_*`, Settings' Test;
- Loki: `LogHistoryService.ReadPodHistory`, chat's `get_log_history`, the LogHistory capability, Settings' Test and credential actions.

The Observer and the attention surfaces read neither.

## 1. Stages

| Stage | Builds | Behaviour for today's files |
|---|---|---|
| **S3b-1: the model** | the kinds `loki` and `metrics` in `sources`, their subscription fields, `metrics.match`, resolution and scopes per kind (§2) | unchanged, except that `metrics.match` becomes valid (§2.1, ruling 1) |
| **S3b-2: reading** | per-subscription acquisition through an installation (§3), the scope applied to every Loki and metrics read, caches and views keyed by lineage, the scope sentence; the host check in S2's removal for every kind | the legacy modes read as today, with lineage and the scope sentence; a legacy `metrics.match` scopes every template |
| **S3b-3: the fences** | lineage recorded by every AI read of logs and metrics, checked before every component read and every later send (§4) | **a change to a legacy Loki or metrics block's endpoint, tenant, match or credential ends later sends of the logs or metrics a conversation read before it** |
| **S3b-4: Settings** | Observability's installations and subscriptions for every kind, their plans, credentials and Test, Detect for metrics, and the migration offer (§5 to §8) | the legacy rows as today, with the offer where it applies |

Each stage is built, reviewed by Codex and accepted in turn.

## 2. The model (S3b-1)

### 2.1 The schema

**Installations of the two kinds:**

```yaml
sources:
  - id: lgtm-we-logs
    kind: loki
    host: aks-we-prd
    serviceRef: {namespace: monitoring, name: loki-gateway, port: http}
    # or endpoint: https://loki.example.com   (never discover: Loki is not discovered)
    tenant: ops
    memberLabel: k8s_cluster_name
    serves: [{group: we}]
  - id: lgtm-we-metrics
    kind: metrics
    host: aks-we-prd
    discover: true               # or serviceRef, or endpoint
    memberLabel: cluster
    serves: [{group: we}]
```

The fields and their refusals are S3a's (§2.1 there), per kind:
- `kind`: `alertmanager`, `loki` or `metrics`.
- **The locator:** exactly one of `serviceRef`, `discover: true` or `endpoint`. `discover` is refused for `loki` ("Loki is not discovered: give a serviceRef or an endpoint").
- An `endpoint` for `metrics` must not end in `/api/v1`, as today's block.
- Every other field and refusal is unchanged.

**Subscriptions,** under a cluster, for each kind:

```yaml
clusters:
  - name: aks-we-stg
    loki: {source: lgtm-we-logs, tenant: stg-logs, scope: tenant}
    metrics: {source: lgtm-we-metrics, match: {cluster: stg-we}}
  - name: aks-we-sandbox
    metrics: {off: true}
```

- `loki.source`, `loki.scope` and `loki.off` stop being refused; they follow S3a's rules (a `source` block refuses a locator, `auth` and `tlsSkipVerify`; `off` allows nothing beside it; `scope` only with `source`).
- `MetricsConfig` gains `Source`, `Scope`, `Off` and **`Match`**, decoded strictly and recorded as written, as `EndpointConfig` does (S3a-1 R1). The legacy `token` is refused beside `source` and `off`.
- **`metrics.match` is valid on a legacy block too** (ruling 1). Today a cluster sharing a Mimir tenant has no way to scope its charts (§0). With `match`, every template of that cluster carries it (§3.3). `sharedWithoutMatch` then covers metrics as it covers Alertmanager and Loki.

### 2.2 Resolution

One resolution per kind, the same function: S3a's `ResolveAlertmanager` becomes `Resolve(kind)`, reading the kind's block (`cl.Alertmanager`, `cl.Loki`, or `cl.Metrics` as its shared shape), with the steps, modes and load errors of S3a §2.2:

| Step | When | Resolves to |
|---|---|---|
| 1 | the cluster has the kind's key | its own block: `subscribed`, `off`, or a legacy mode |
| 2 | no key, and an installation of the kind serves it by name | `subscribed` |
| 3 | no key, and one serves its group | `subscribed` |
| 4 | none of these | **Alertmanager and metrics:** discovery in the cluster (`discovered`); **Loki:** none, "no Loki is configured for aks-we-dev" |

- **A cluster's own block always wins,** an empty one included, per kind: a cluster may subscribe its metrics and keep its own Loki.
- Two installations of one kind serving a cluster at one step is a load error; of different kinds, never a conflict.
- A `source:` naming an installation of another kind is a load error, naming both kinds.
- The resolution is computed for all three kinds at load, on every Observability plan and every S2 plan, and published together: one active-sources revision covers them.

### 2.3 Scopes

S3a §2.3 unchanged, per kind and per installation: every subscription states its scope (a non-empty effective match, or `scope: tenant`); subscriptions of one installation sharing a tenant are provably disjoint on the member label. For the owner's LGTM layout (a tenant per cluster), `scope: tenant` is the usual subscription, and no label is injected anywhere.

## 3. Reading (S3b-2)

### 3.1 Per-subscription acquisition

**No buckets** (redesign §6.4). An alerts read is one list for many members; a log or metrics read is per object, window and template, so each is acquired per subscription:
- **Hosted** (`serviceRef` or `discover` on a host): the read takes a lease on the host's **current** connection, as Detect does (S3a-4b), and goes through the host's guarded service proxy. The host's discovery outcome (metrics) is cached per host connection and installation revision for today's 45 s unavailable and lifetime available rules.
- **Direct** (`endpoint`): today's direct transport, with the installation's credential `source/<id>`, bound to its origin, and its revision `("source/<id>", kind)`.
- **The tenant** is the subscription's effective tenant, as `X-Scope-OrgID`; **the scope** is its effective match (§3.3).
- Legacy modes keep their transports: their own connection, block and credential.

**The fence on every request** (S3a §4.3's request check, per read): before and after each request, the host's pinned connection is still its current connection and usable, its access generation is the one the read started under, and the installation's and subscription's revisions are unchanged; a direct read checks its access generation and credential revision. A mismatch discards the read. A 401 or 403 moves the access generation first.

**Bounds:** today's per kind, applied per read: Loki's 8 MiB a page and its four pages fleet-wide and two per member; metrics' 4 MiB and its 3 s UI timeout. **A host carries its members' hosted reads,** so each host adds one bound: at most four hosted metrics requests and two hosted Loki pages in flight through one host, beyond which reads wait within their own timeout (ruling 3). Direct installations add none: the endpoint is not a cluster's API server.

### 3.2 Caches, views and keys

- **The lineage** is S3a's, generalised: a `sources.Lineage` with the kind, the mode, the installation's id and revision, the subscription's revision, the host, its connection identity and access generation, the tenant, the effective match as display, the credential revision, and the read's time. It moves out of `internal/alertmanager` into a neutral package; Alertmanager's reads keep every field they carry today.
- **Metrics:** the connection's cache is keyed by the member's metrics lineage key (S3a §3.4's form). A moved installation, subscription or access generation re-keys it, so nothing of the old terms is shown as current; a credential change re-keys nothing, as today.
- **Loki:** a log history view locks its reader's lineage key, which now carries the installation's and subscription's revisions and the access generation. A move starts a new page set, as a source change does today.

### 3.3 The scope on every read

**Loki:** the effective match is the reader's match, which already leads every selector of every branch (`loki/loki.go:112-125`). `scope: tenant` sends none. Test and the label read use it as today.

**Metrics:** every template carries the member's scope in **every** vector selector, by construction:
- `internal/metrics` gains a `Scope` (the effective match's matchers, escaped by `Equals`), and every template becomes a function of it. Each selector is written `metric{<template's own matchers>,<scope>}`; a bare selector (`count(envoy_cluster_upstream_rq_total)`) gains braces.
- **The canary test:** every template, rendered with a canary scope, is scanned by a small PromQL selector scanner (test-only), and every vector selector must carry the canary. A template added without it fails the test. (Ruling 2: or the Prometheus parser as a test-only dependency.)
- `scope: tenant` and a legacy block without `match` render an empty scope, so today's queries are unchanged byte for byte.
- **What a scope cannot show is said, never zero.** A series without the member label is outside the scope. A template that returns nothing under a scope reads "no data for aks-we-dev under cluster=aks-we-dev", never "0" and never "unavailable".

### 3.4 Coverage for a member

| The member's kind | What its views say |
|---|---|
| read through its subscription | the data, with the scope sentence |
| the host is not connected, or not usable | "lgtm-we-metrics's host aks-we-prd is not connected", never "no data" |
| the host's discovery found none (metrics) | "lgtm-we-metrics found no Prometheus in aks-we-prd" |
| no Loki configured or served | today's "no log history" state, naming why: "no Loki is configured for aks-we-dev" |
| `off` | "off in fleet.yaml" |
| legacy modes | as today, with the scope sentence |

**The scope sentence** (S3a §3.5's form) appears where logs and metrics are shown: the log history pane's header, the metrics status lines (Overview, Nodes, Pods, Workloads), the data path's and Hubble's metrics notes, and Observability's rows: "from lgtm-we-metrics, hosted by aks-we-prd, filtered by cluster=aks-we-dev".

### 3.5 Hosts and the fleet

S3a §3.6 for every kind:
- `FleetPlanDeps.Hosts` returns the installations of every kind naming the cluster as host, active and in the file, so S2 refuses removing a host of a Loki or metrics installation.
- A host's reconnect or recovery moves its access generation: its members' metrics caches re-key and their log views start again. S2's reconnect plan lists them among what ends.
- A placement edit that changes a cluster's group, or a cluster added or removed, re-resolves every kind under the fleet Apply lock, and S2's placement plan says each kind's change.

## 4. The fences (S3b-3)

**Recorded:** every AI read of logs or metrics records its lineage as a bounded token, as `get_alerts` does (S3a §4.2):
- `get_log_history`;
- `get_metrics`, its `route_*` templates included, and every other chat or investigation tool that reads metrics (rule metrics);
- a conversation keeps every token cumulatively, its trimmed exchanges' included.

The lineage holders (`conversation.lineages`, `chatTools.record`, Explain's `lineageSet`) take `sources.Lineage`, of any kind.

**Checked:** the turn's guard and A3's plan fence check every token against the current terms of its kind (S3a §4.3), before every component read and every later send:
- subscribed: the installation and subscription unchanged, the host's pinned connection current, usable and at its access generation, or a direct read's access generation and credential revision;
- legacy: the source identity and the credential revision of `(cluster, kind)`. Metrics has no source identity today; its lineage gains one, `how|at|tenant|match` as Loki's and Alertmanager's, with the discovered Service as `at` for a discovered read, so a discovery that now finds another Service ends the conversation's later sends.

A mismatch ends the conversation: "lgtm-we-logs changed since its logs were read: start a new conversation". **This closes S3a §0's gap for Loki and metrics:** a change to a legacy block's endpoint, tenant, match or credential, through Test, a credential action or a restart, ends later sends of the data read before it.

**UI views are not fenced beyond their keys** (§3.2): a view shows data read under its own lineage, re-keyed when the terms move, as S3a's lens does.

## 5. Plans and Apply (S3b-4)

S3a §5, for every kind. Observability's plan covers an installation of any kind added, edited or removed, and a cluster's `alertmanager`, `loki` or `metrics` subscription block written, changed or removed. It says, for each member whose resolution changes:
- its scope sentence before and after, per kind ("Logs for aks-we-dev: none → from lgtm-we-logs, hosted by aks-we-prd, scope: tenant stg-logs");
- what ends: its metrics cache re-keys, its log views start again, and conversations holding the old lineage end at their next send.

The two candidates, the one transaction under the fleet Apply lock, "Use the file's values" (sources and subscription blocks of every kind, never a legacy block) and the refusals are S3a's. Legacy blocks stay file-only and External endpoints', as today.

## 6. Detect (S3b-4)

**Metrics only:** Loki is never discovered, and Detect never reads log lines.
1. In each member, Klyx runs the existing Prometheus discovery. Several candidates are ambiguous, as today, and propose nothing.
2. For the Service found, one bounded sample of label names and values (ruling 4): `/api/v1/labels` over the last hour, then `/api/v1/label/<name>/values` for at most eight labels whose names suggest a cluster (`cluster`, `k8s_cluster_name`, `cluster_name` and the like, or any label among the names), each capped at 1 MiB and 2,000 values. A value is kept only when it equals a fleet cluster's name; no series and no sample value is read.
3. The proposal is S3a §6's: the host and the Service, a member label whose values name fleet clusters, the members seen, the sample's time and bounds, what was ambiguous. "Seen in this sample" never means a member has no metrics.
4. Never inferred from equal Service names; through the fleet-wide metrics bound; only when the person asks, one group or cluster at a time.

## 7. Credentials and the migration offer (S3b-4)

- An installation's credential is `source/<id>`, as S3a's: ids are unique across kinds, so the account names one installation of one kind. Store, Remove and Test as S3a-4a's, per kind. Test for Loki reads the build info and the label names in the subscription's scope, never lines; for metrics, the capability probe.
- **The migration offer** (S3a §7) for each kind: one direct URL on several clusters' `loki` or `metrics` blocks, or `sharedWithoutMatch`'s warning. Its draft, conflicts (auth, the certificate check, left to the person, S3a-4c), scope statements and choices are S3a's.
- **A legacy metrics `token`** is never carried into an installation: the offer says the installation needs its own credential, stored by the person, and the clusters' tokens stay until removed.

## 8. The page (S3b-4)

Observability's "Alert sources" becomes **"Sources"**, one panel per kind (Alertmanager, Logs, Metrics) on S3a-4c's components:
- the installations of the kind, with their locators (no discovery for Loki), credentials, Test, Edit and Remove;
- each running cluster's resolution for the kind, its scope sentence and its subscription editor;
- Detect for metrics; the offer for every kind;
- S2c's discipline, the leave guard and the readback, as S3a-4c's.

**The registry:** `sources[].kind` gains its values; `clusters[].loki.source`, `scope` and `off` become editable; `clusters[].metrics.source`, `scope`, `off` and `match` are new entries, editable (match also file-only on a legacy block, as the other legacy fields).

## 9. Tests

- **The model:** every kind's locators and refusals; resolution per kind, a cluster subscribing one kind and keeping another's own block; cross-kind `source:` refused; `metrics.match` decoded as written, valid on a legacy block, refused beside `token` with `source`; shared-tenant disjointness per installation.
- **Reading:** a hosted read through the host's current connection, its lease released once, a host reconnect re-keying the member's cache and ending its views; a direct read with the installation's credential; the per-request fence discarding a read that crossed a move; the host bound; Loki selectors leading with the effective match; **the canary test over every metrics template**, and every template byte for byte unchanged under an empty scope; "no data under the scope", never zero.
- **The fences:** `get_metrics`, `route_*` and `get_log_history` recording their lineage; a conversation ended at its next send by an installation, subscription, host, access, credential or legacy block change; canaries that a token never enters a request.
- **Settings:** plans per kind, the page's discipline per kind, Detect's sample keeping only names and fleet-name values, the offer per kind with its conflicts left to the person.
- **The native walk:** a metrics installation hosted by the lab cluster, `scope: tenant` or a member label against the lab's Prometheus, its scope sentence in the Nodes view; a Loki installation over the lab's Loki (S8b) with a subscription scoped by `k8s_cluster_name`, its sentence in the log history pane; each removed, the smoke fleet as it began.

## 10. Not in S3b

- Federated reads across tenants (redesign §6.4).
- Discovering Loki.
- Any write to Prometheus, Mimir or Loki.
- Metrics templates written by the person; the templates stay Klyx's.

## 11. For Codex's ruling

1. **`metrics.match` on legacy blocks.** I propose it: without it a cluster sharing a Mimir tenant cannot be scoped at all, and the injection machinery (§3.3) is the same for installations and legacy blocks. The alternative is to allow `match` only with `source`, leaving today's legacy reads unscoped.
2. **How "every selector carries the scope" is proven.** I propose templates as functions of a `Scope` and a test-only scanner that checks a canary in every vector selector. The alternative is the Prometheus parser as a test-only dependency, which is exact but heavy.
3. **A per-host bound for hosted reads** (four metrics requests, two Loki pages in flight per host). Without it, a group installation of many members could crowd one host's API server.
4. **Detect's metrics sample** reads label names and the values of cluster-like labels, never series, bounded as §6 says: an amendment of redesign §6.7 like S3a's ruling 3.
