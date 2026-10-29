# Klyx Settings S3b: Loki and metrics installations and subscriptions

Opened 2026-10-28, on `feat/settings-s3b`, with S3a-1 to S3a-4b accepted and S3a-4c in review. S3b is the redesign's second observability slice (redesign §6, §14): Loki and metrics on the model S3a built for Alertmanager, "with their per-subscription acquisition". It builds, for Loki and metrics:
- installations and subscriptions: the kinds, the schema, resolution and scopes;
- per-subscription acquisition through an installation's transport and tenant, scoped to the member;
- the scope sentence where logs and metrics are shown;
- lineage on every AI read of logs and metrics, and the fences that end later sends when the terms move: S3a §0's gap, closed for Loki and metrics;
- Settings: the installations and subscriptions of each kind, their plans, credentials, Test, Detect for metrics, and the migration offer.

This note is for Codex's review before anything is built; revision 1 the same day, after that review (§12), with the implementation contracts it set (§13); revision 2 after its re-review (§14); accepted (§15).

## 0. What exists

From a read of the code at bfe29154 (S3a-4c's head).

**Configuration** (`internal/config`):
- `clusters[].loki` is `EndpointConfig`, the Alertmanager shape: `endpoint`, `serviceRef`, `auth`, `tlsSkipVerify`, `tenant`, `match`, and S3a's `source`, `scope` and `off`, which are refused for Loki as written ("loki.source is not built yet", `sources.go:134-143`).
- `clusters[].metrics` is its own `MetricsConfig`: `endpoint`, the legacy `token`, `tlsSkipVerify`, `serviceRef` (with `path`), `auth`, `tenant`. **It has no `match`, `source`, `scope` or `off`** (`config.go:137-157`); any of them is an unknown key, a load error (`strict.go`).
- `sources[].kind` is `alertmanager`; `loki` and `metrics` are refused as "not built yet" (`sources.go:75-83`). The registry gives Loki's subscription keys the disposition deferred, S3b's (`registry.go:177-193`).
- Loki is never discovered (`endpoints.go:109-113`, `fleet/loki.go:78-81`); metrics is, when its block has no locator (`fleet/metrics.go:101-148`): the named Services in `monitoring` are **ranked**, and the first present wins (`TestDiscoverPrefersFirstNamed`); only when none is present are two label selectors tried, where several matches are unavailable.
- `sharedWithoutMatch` skips metrics (`endpoints.go:249`).

**Metrics reading** (per `ClusterConn`):
- `metrics.Resolve`: the endpoint, else the serviceRef through the cluster's own API server, else discovery (`metrics/resolve.go:28-60`). The tenant is `X-Scope-OrgID` on both transports; responses are capped at 4 MiB (`metrics/transport.go`).
- A cache on the connection: the capability for the connection's life, unavailable for 45 s, samples for 15 s (`fleet/metrics.go:16-23, 132-171`). The credential revision `(cluster, metrics)` is checked after each read (`:177-201`).
- **Time budgets:** the bound UI services give each operation a 30 s context (`appbridge/metrics_service.go:13`, `:41`), which the proxy uses; 3 s is only the direct HTTP client's timeout (`fleet/metrics.go:22`, `:63`). There is no fleet-wide metrics slot.
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
| **S3b-1: the model** | the kinds `loki` and `metrics` in `sources`, their subscription fields, resolution and scopes per kind (§2), and what a connection does until S3b-2 reads through installations (§2.4) | unchanged; `metrics.match` stays a load error until S3b-2 honours it (§12, R1) |
| **S3b-2: reading** | per-subscription acquisition through an installation (§3), the closed metrics query surface and the scope on every Loki and metrics read, caches and views keyed by lineage, the scope sentence; the host check in S2's removal for every kind | the legacy modes read as today, with lineage and the scope sentence; `metrics.match` is admitted, and scopes every query |
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
- **`metrics.match` is valid on a legacy block too** (ruling 1, approved). Today a cluster sharing a Mimir tenant has no way to scope its charts (§0). With `match`, every query of that cluster carries it (§3.3). `sharedWithoutMatch` then covers metrics as it covers Alertmanager and Loki. **It is admitted in S3b-2,** the stage that honours it; in S3b-1 it stays a load error naming it ("metrics.match is read from a later version"), so no file states a scope Klyx does not apply (§12, R1). A legacy `token` may accompany a legacy `match`; `source` and `off` refuse the token. The written-key checks hold through aliases and merges, empty and false spellings included.

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

### 2.4 What a connection does in S3b-1

Reading through an installation is S3b-2. Until then, as S3a-1 did for alerts (S3a §15), nothing an installation serves is read any other way:
- `fleet.SubscribingFactory` resolves all three kinds and gives each connection its blocks resolved against the installations read at start. The running fleet's entry is not changed.
- **`off`** makes no request of any kind: metrics and log history read as absent, "metrics are off for this cluster in fleet.yaml", with no capability.
- **A subscription** is unreadable, said as such: "this cluster's metrics come from the installation lgtm-we-metrics, and this version of Klyx does not read through installations yet". It never falls back to discovery in the member or to another block. A Loki subscription says so too, distinct from "no Loki is configured for this cluster".
- Their identities are `off` and `source|<id>` per kind, so adopting another block re-keys.
- **S2's plans:** the host check covers installations of every kind. A placement or protection edit that would move a cluster's Loki or metrics resolution is refused, naming both sources, since the running connection cannot follow until S3b-2 re-resolves on commit. Reconnects and additions build their connection through the resolving factory, so they follow.
- **Controls:** a file without `sources`, and every legacy block, read exactly as before; tests show discovery counted for a legacy block and never for a subscription or `off`.

## 3. Reading (S3b-2)

### 3.1 Per-subscription acquisition

**No buckets** (redesign §6.4). An alerts read is one list for many members; a log or metrics read is per object, window and template, so each is acquired per subscription:
- **Hosted** (`serviceRef` or `discover` on a host): the read takes a lease on the host's **current** connection, as Detect does (S3a-4b), and goes through the host's guarded service proxy. The host's discovery outcome (metrics) is cached per host connection and installation revision for today's 45 s unavailable and lifetime available rules.
- **Direct** (`endpoint`): today's direct transport, with the installation's credential `source/<id>`, bound to its origin, and its revision `("source/<id>", kind)`.
- **The tenant** is the subscription's effective tenant, as `X-Scope-OrgID`; **the scope** is its effective match (§3.3).
- Legacy modes keep their transports: their own connection, block and credential.

**The fence on every request** (S3a §4.3's request check, per read): before and after each request, the host's pinned connection is still its current connection and usable, its access generation is the one the read started under, and the installation's and subscription's revisions are unchanged; a direct read checks its access generation and credential revision. A mismatch discards the read. A 401 or 403 moves the access generation first.

**Budgets** (§12, R4; ruling 3, approved):
- **The operation deadline** is the caller's: the UI services' 30 s context, the chat tool's turn budget, Detect's 30 s per member. Every wait and request of the operation (a host slot, discovery, each request and its body) runs inside it.
- **Per request, per kind, as today** (§14, RR2): a direct metrics request keeps its 3 s client timeout; a direct Loki request keeps its 30 s page timeout (`lokiPageTimeout`), bounded by the remaining operation deadline; a proxied request of either kind runs under the remaining operation deadline. A composite read (several queries for one result) pins its terms once (§13).
- **The host bound:** at most four hosted metrics requests and two hosted Loki pages in flight through one host, shared by every installation and subscriber using that host. A read waits for a slot within its operation deadline; a cancelled wait leaves without a request. A slot is released when the request and its body have finished, not when cancellation is signalled, and a reconnect does not reset the count while earlier workers run. A member that is its own host acquires one slot, never two. Direct installations take no host slot.
- Loki's four pages fleet-wide and two per member, its 8 MiB a page, and metrics' 4 MiB a response stay as today.

### 3.2 Caches, views and keys

- **The lineage** is S3a's, generalised: a `sources.Lineage` with the kind, the mode, the installation's id and revision, the subscription's revision, the host, its connection identity and access generation, the tenant, the effective match as display, the credential revision, and the read's time. It moves out of `internal/alertmanager` into a neutral package; Alertmanager's reads keep every field they carry today.
- **Metrics:** the connection's cache is keyed by the member's metrics lineage key (S3a §3.4's form). A moved installation, subscription or access generation re-keys it, so nothing of the old terms is shown as current; a credential change re-keys nothing, as today.
- **Loki:** a log history view locks its reader's lineage key, which now carries the installation's and subscription's revisions and the access generation. A move starts a new page set, as a source change does today.

### 3.3 The scope on every read

**Loki:** the effective match is the reader's match, which already leads every selector of every branch (`loki/loki.go:112-125`). `scope: tenant` sends none. Test and the label read use it as today.

**Metrics:** every data query carries the member's scope in **every** vector selector, through a closed surface (§12, R2; ruling 2, approved):
- **The surface.** `internal/metrics` owns a registry of named templates, each rendered from its parameters and a `Scope` (the effective match's equality matchers, escaped by `Equals`). A query exists only as a `metrics.Query`, a type with unexported fields that the registry alone builds. Every sink (`readMetrics`, the range and instant reads, `ChatMetricsRange`, the route, rule and Hubble clients) takes a `metrics.Query`, never a string, so an unregistered query does not compile. The liveness probe `vector(1)` is the one named exception, `metrics.Liveness`, outside data-query admission.
- **The scope is the read's, never the caller's** (§14, RR1). A `metrics.Query` holds only its template and parameters; it carries no scope. The pinned reader renders it, at the sink, with the scope of the terms it pinned: the member's effective match, or an empty scope when those terms say so (`scope: tenant`, a legacy block without `match`), derived from the terms and never chosen by a caller. A zero `metrics.Query{}`, which another package can still write, names no template and is refused before any request.
- **Conjoined, never replaced.** The scope's matchers are added to each selector's own; a template matcher on the same label stays, and both must hold (Prometheus requires every matcher of a selector). A bare selector gains braces.
- **The proof** (test-only, the Prometheus parser pinned as a test dependency): every registered template, over the grid of parameter cases it declares, is rendered with a hostile scope (several labels, values with quotes, backslashes and braces), parsed, and every vector selector (a range selector's included) must carry every scope matcher as an equality with its exact decoded value. The registry is the production list: a template not in it cannot be built. **The grid is enforceable:** each template declares its parameter cases beside its definition, and rendering it with a parameter shape no declared case covers fails the test, so removing a case breaks the build of the proof rather than passing it. Negative fixtures must fail: an unscoped operand, one scope matcher missing, a bare selector, the canary only inside a quoted string, and a removed case. An empty scope renders today's queries byte for byte. **The read owns the scope:** a scoped reader given queries built and tried by a caller with an empty, a foreign member's or an incomplete scope in mind renders each under its own pinned scope, and a zero query is refused before HTTP; the tenant-wide and legacy-empty readers render an empty scope.
- **What a scope cannot show is said, never zero.** A series without the member label is outside the scope. An empty result reads "no data for aks-we-dev under cluster=aks-we-dev"; a failed read, a real zero, and a non-finite or gapped sample stay distinct from it and from each other.

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
1. **Detect's own policy** (§12, R5). Legacy reading keeps its ranked discovery (the first named Service present wins). Detect is stricter: it lists every named candidate present in the member, and the label-selector matches. The same Service found by name and by a selector is one candidate; distinct ports or paths are distinct candidates. One candidate is proposed, named with how it was found; several propose nothing and name every candidate, so the person can write the installation from them. A discovery that could not be read says so, and is never taken as an exhaustive list.
2. **One bounded metadata sample** (ruling 4, approved), never series and never sample values: one `/api/v1/labels` response for a requested last-hour window, then `/api/v1/label/<name>/values` for at most eight labels, in a fixed order (`cluster`, `k8s_cluster_name`, `cluster_name`, `kubernetes_cluster`, then any other label among the names whose values could be checked, alphabetically). Each response is capped at 1 MiB. **The sample is the first 2,000 values of each response, counted before any filtering** (§14, RR3): a value past them is not examined and influences neither the candidates nor the members seen, and a response with more is said to be cut. Label names in paths are path-escaped. Only names, and values exactly equal to a fleet cluster's name, are kept; never other values, decoded error bodies or numbers, in a proposal or in AI context. The labels not queried, any response cut at its cap and any that failed are said.
3. **What it can claim:** metadata returned for a requested window, not proof that a member produced metrics in that hour (Prometheus's metadata bounds are approximate, and can include series without samples in the window). A member absent from the metadata proves no absence of data.
4. The proposal is S3a §6's: the host and the Service, a member label whose values name fleet clusters, the members seen, the sample's time and bounds.
5. **Budget and lifecycle:** at most two metrics Detects in flight fleet-wide, a pool of Detect's own; each takes a lease on its member and a host slot for each request, within a 30 s deadline per member that includes the wait for the pool. As S3a-4b settled it, retirement or the deadline discards the proposal before the lease is released. Never inferred from equal Service names; only when the person asks, one group or cluster at a time.

## 7. Credentials and the migration offer (S3b-4)

- An installation's credential is `source/<id>`, as S3a's: ids are unique across kinds, so the account names one installation of one kind. Store and Remove as S3a-4a's, per kind, resolved inside the endpoint-then-fleet lock order.
- **Two Tests** (§12, R3):
  - **The installation's Test** proves reachability, nothing about any member: Loki's build info, metrics' liveness probe (API liveness, not scoped samples), Alertmanager's status as today. With no subscribers, it is the only Test.
  - **A subscription's Test,** on a running member's row, for Loki: the backend resolves that member's kind, installation, effective tenant and full match, and their revisions; the page sends only the member's name and the revisions it shows, never a tenant or a match. It reads the label names under that scope over the last hour, never lines, and checks the subscription's, installation's, host and access, and credential terms before and after the request, as one target pinned from admission to settlement under the caller's fence and the host's lease and slot rules; a move, the member's included, discards the result. Each member's Test is its own, so one tenant working and another denied are said apart.
- **The migration offer** (S3a §7) for each kind: one direct URL on several clusters' `loki` or `metrics` blocks, or `sharedWithoutMatch`'s warning. Its draft, conflicts (auth, the certificate check, left to the person, S3a-4c), scope statements and choices are S3a's.
- **A legacy metrics `token`** is never carried into an installation: the offer says the installation needs its own credential, stored by the person, and the clusters' tokens stay until removed. A token without an explicit `auth` implies bearer auth: the offer's draft carries that shape (`auth: {type: bearer}`), never the token, in any draft, candidate, plan, cache or output, so the installation is not planned unauthenticated.

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
- **Revision 1's cases** (§12): S3b-1's boundary (a subscription unreadable without discovery, `off` making no request, `metrics.match` refused, legacy controls unchanged); the query surface's proof and its negatives (§3.3); the host bound held, a queued read cancelled without a request, the deadline, and a slot released only when the body has finished; Detect's candidates listed and the ranked legacy choice unchanged, its metadata bounds and qualifications; two Loki subscriptions on different tenants tested apart, one denied, a subscription moving during its Test, an installation with no subscribers; composite reads refused whole across a move; empty and cached reads carrying their own lineage; late UI replies under an old key refused; the smoke's cleanup after an installation was added.
- **Revision 2's cases** (§14): queries built with an empty, a foreign member's and an incomplete scope in mind rendered under the reader's own; a zero query refused before HTTP; the tenant-wide and legacy-empty controls; a removed grid case failing the proof; direct Loki keeping 30 s and direct metrics 3 s; a values response whose first 2,000 are non-members and whose first fleet name lies past them; one Service found two ways as one candidate, and an unreadable discovery never exhaustive.
- **The native walk:** a metrics installation hosted by the lab cluster, `scope: tenant` or a member label against the lab's Prometheus, its scope sentence in the Nodes view; a Loki installation over the lab's Loki (S8b) with a subscription scoped by `k8s_cluster_name`, its sentence in the log history pane; each removed, the smoke fleet as it began.

## 10. Not in S3b

- Federated reads across tenants (redesign §6.4).
- Discovering Loki.
- Any write to Prometheus, Mimir or Loki.
- Metrics templates written by the person; the templates stay Klyx's.

## 11. For Codex's ruling

Asked in the first draft, ruled in Codex's review (§12):
1. **`metrics.match` on legacy blocks:** approved, admitted in S3b-2 (§2.1, §2.4).
2. **The selector proof:** the Prometheus parser as a test-only dependency, over a closed query surface (§3.3).
3. **A per-host bound for hosted reads:** approved, four metrics requests and two Loki pages, shared per host (§3.1).
4. **Detect's metrics sample:** approved, metadata only, bounded and qualified (§6).

## 12. Revision 1: Codex's review (2026-10-28)

Codex found the note's account of today's schema, the absent metrics scopes, Loki's configured-only behaviour and history bounds, the credential revisions, the shared-tenant risk and the missing lineage on logs and metrics supported by the code. It requested changes: three P2 and two P3, and ruled on §11's four questions (all approved, two with conditions).

| Finding | Change |
|---|---|
| **R1:** S3b-1 made `metrics.match`, subscriptions and `off` valid with no reader to honour them: a legacy match on a shared tenant would not filter, and an implied subscription or `off` could fall back to discovery | §2.4: S3b-1's connection boundary, as S3a-1's: `off` makes no request, a subscription is unreadable and never discovered, Loki's subscription is told apart from none, S2 edits a connection cannot follow are refused, reconnects and additions follow, legacy controls unchanged. `metrics.match` is admitted in S3b-2, a load error until then (§2.1) |
| **R2:** a canary over a chosen template list proves only that list: a raw-string sink (`ChatMetricsRange`), an omitted branch or one dropped scope matcher would pass | §3.3: a closed query surface (`metrics.Query`, built only by the template registry; every sink takes it, never a string; `metrics.Liveness` the one exception); the Prometheus parser proves every vector selector carries every scope matcher with its exact value, over a grid covering every branch, with negative fixtures that must fail; scope matchers conjoined with a template's own |
| **R3:** an installation's Loki Test had no subscription to scope it: several tenants, or none | §7: two Tests. The installation's proves reachability only; a subscription's, on a member's row, is resolved by the backend from the member's name and shown revisions, reads label names under that scope, never lines, and is fenced by every term |
| **R4, P3:** "3 s" was the direct client's timeout, not the operation's (30 s), and Detect's "fleet-wide metrics bound" did not exist | §0 corrected; §3.1 defines the operation deadline, the per-request timeouts, the host slots (shared, released when the body finishes, a reconnect not resetting them); §6 gives Detect a pool of two of its own and a 30 s deadline per member |
| **R5, P3:** legacy discovery ranks named Services and picks the first; the note said several were ambiguous "as today" | §0 corrected; §6: legacy reading keeps its ranking, and Detect's own stricter policy lists every candidate and proposes one only when one is found, with its own tests |

**The rulings' conditions,** adopted: a legacy token may accompany a legacy match, while `source` and `off` refuse it (§2.1); the scope matchers conjoin with a template's own (§3.3); the host bound is shared across installations and subscribers, never double-acquired by a member that is its own host (§3.1); Detect's metadata wording and bounds (§6).

## 13. Implementation contracts

Codex's acceptance requirements for the implementation, adopted as they stand (its review, "Remaining contracts and implementation checkpoints"):
- **Model:** the three resolutions are checked in both candidates; the kind is part of every legacy, dependency and cache namespace, so a Loki change is never compared with a metrics revision.
- **Provenance:** a read's lineage travels with its result through projection, caches, DTOs and the AI adapter; cached values keep their acquisition time and credential revision; empty successes, counts, partial and derived results carry the lineage of the read that made them, never a lineage looked up later. Tokens are deduplicated by kind and terms, the acquisition time on the read's envelope.
- **Composite reads:** the terms are pinned once for a logical result, rechecked before and after every component and at settlement; a move discards the whole result, never a nil sample or two generations mixed. `TestEveryMetricsReaderDropsAReadAcrossARevisionMove` is ported to installation, subscription, host and access, and credential moves, range, Envoy rule, Hubble and chat's templates included.
- **Caches and the UI:** the outcome's credential revision is carried though not keyed; a known-down host is checked before a cached value is served; in-place recovery, a reconnect and an observed denial are tested. Frontend stores, sparklines and log page sets refuse late replies from an old key, and keep their sentence tied to the outcome shown; old data is shown only with its own lineage and qualified as stale.
- **Lifecycle and fences:** the caller's context and fence are kept while joining the host lease's cancellation; host work is registered before its I/O, listed in S2's plans, settled before the lease's release, and no idle reader keeps a host lease. The host's pinned connection, usability and access generation are checked on cache hits and every later send. Dependencies survive trimming; every retry, redirect and CLI input release is fenced, with A3's sticky withdrawal.
- **Plans:** the affected-work calculation and its registration lock cover every kind, filtered by the changed kind, member and terms: a metrics-only Apply never ends an alerts-only conversation whose tokens still hold, and work acquiring an affected dependency during the final planning is refused or included. The work identities are in the digest; per-field raw-input comparison and withheld wording are kept; "Use the file's values" adopts no unrelated drift and no legacy block.
- **The page:** S3a-4c's accepted behaviour after its revision, per kind: unique leave-guard owners per panel, refreshed-away drafts kept, late plans and readbacks refused, revision-keyed credential clearing, proposal ids and references, and anchors after every panel has loaded. Every repeated control has a unique address; new fixed limits are registered with the schema paths, and `metrics.match`'s two dispositions are explained.

## 14. Revision 2: Codex's re-review (2026-10-28)

Codex closed R1, R3, R4 and R5, and closed R2 but for its residue; the rulings stand as recorded. Two P2 and one P3 remained.

| Finding | Change |
|---|---|
| **RR1:** the closed surface proved that a registered template renders the scope it is given, not that the scope given is the reading member's: a caller could build a valid query with an empty, foreign or incomplete scope and hand it to another member's sink; and a zero `metrics.Query{}` compiles anywhere | §3.3: a `metrics.Query` carries no scope. The pinned reader renders it at the sink with the scope of its own terms, an empty one only when those terms say so; a zero query names no template and is refused before any request. The proof gains the read-owned cases and the zero query |
| **RR2:** revision 1's "a direct request keeps its 3 s client timeout" would have cut direct Loki's 30 s page timeout | §3.1: per kind, as today: direct metrics 3 s, direct Loki 30 s within the remaining operation deadline, proxied requests under the operation deadline |
| **RR3, P3:** "2,000 values kept" counted after filtering, so a fleet name past the sample could still count | §6: the sample is the first 2,000 values of each response, counted before filtering; nothing past them is examined; a longer response is said to be cut |

**Codex's further details,** pinned in the design and its tests: Detect counts one Service found by name and by a selector once, keeps distinct ports or paths apart, and never takes an unreadable discovery as exhaustive (§6, step 1); the wait for Detect's pool is inside its per-member deadline (§6, step 5); the grid of parameter cases is declared beside each template and enforced, so a removed case fails (§3.3); a subscription's Test pins the member, installation and subscription as one target from admission to settlement (§7). §2.4's rules apply per kind: one kind turned off leaves the others as they are.

## 15. ACCEPT (2026-10-28)

Codex accepted the design at 87f8d14a, after its review and two re-reviews (§12, §14), with no findings remaining. Its four rulings stand: legacy `metrics.match`, admitted in S3b-2; the pinned Prometheus parser over the closed query surface; four hosted metrics requests and two Loki pages per host; Detect's bounded, metadata-only sample. The implementation reviews are to check these at their boundaries: a query refused before I/O when it names no template, rendering from the pinned reader's terms only, an enforced missing-grid-case negative, a slot released when its body has finished, Detect's input counted before filtering, and a Test settled before its lease is released. The page builds on S3a-4c after its review fixes; this acceptance is not that page's.

## 16. S3b-1 as built: the model (2026-10-28)

S3b-1 is §2 and §2.4: the kinds, their subscriptions, resolution and scopes per kind, and what a connection does until S3b-2 reads through installations. Built on S3a-4c's revision 1, then rebased onto S3a's accepted head (e4138343, §37 of the S3a spec); the design's commits moved with it unchanged (accepted at 87f8d14a, the same text now at bd685d0c).

| Contract | Code |
|---|---|
| The kinds (§2.1) | `config.SourceKindLoki` and `SourceKindMetrics` beside Alertmanager's; `validateSources` accepts the three, refuses `discover` for Loki ("Loki is not discovered: give a serviceRef or an endpoint") and a metrics endpoint ending in `/api/v1` |
| Metrics' subscription fields (§2.1) | `MetricsConfig` gains `Match`, `Source`, `Scope` and `Off`, decoded with the keys written (`UnmarshalYAML`, as `EndpointConfig`'s); `Spec()` carries them and the keys |
| The subscription's refusals (§2.1) | `validateSubscription` runs for every kind, its messages naming the kind: Loki's `source`, `scope` and `off` follow Alertmanager's rules; `metrics.match` is refused ("read from a later version of Klyx", §12 R1); a legacy `metrics.token` is refused beside `source` or `off`, and allowed beside a legacy endpoint |
| Resolution per kind (§2.2) | `config.Resolve(kind)`, with `ResolveAlertmanager` its Alertmanager form and `ResolveAll` all three: installations of the kind only, by name, by group, else discovery (Alertmanager, metrics) or `none` (Loki, `AMNone`), a Loki block naming nowhere `none` too; a `source` naming an installation of another kind refused, naming both kinds; conflicts and scope messages naming the kind; disjointness per kind ("their logs cannot be told apart"). The load and `CheckSources` (an Observability plan's runtime candidate) resolve every kind |
| The connection's blocks (§2.4) | `ConnectionBlock(kind)`, `ConnectionLoki` and `ConnectionMetrics` beside `ConnectionAlertmanager`, each taking only its kind's installations; `fleet.SubscribingFactory` gives each connection all three |
| Off and subscriptions (§2.4) | Metrics: `metrics.Resolve` answers `off` ("metrics are off for this cluster in fleet.yaml") and a subscription ("this cluster's metrics come from the installation …, and this version of Klyx does not read through installations yet") before anything else, with no transport; the connection never runs discovery for either. Loki: `off` has no LogHistory capability and reads `ErrLokiOff`; a subscription keeps the capability and every read says it is not read through installations yet; a block naming nowhere is no Loki, as before. The Loki warning "has no endpoint or serviceRef" no longer fires for a subscription or `off` |
| S2's plans (§2.4) | `FleetPlanDeps.Sources`: a placement or protection edit that would move a running cluster's Loki or metrics to another source is refused, naming both ("dev's metrics would come from the installation … instead of discovery in the cluster: this version of Klyx cannot move a running cluster's metrics until it reads through installations…"); a reconnect or an addition builds its connection anew, and follows. The S2 candidate check (`AlertSources.Resolve`) resolves every kind |
| Observability in this stage | Its page, plans and credentials are Alertmanager's: the state lists Alertmanager installations, and compares only them with the file; editing or removing a Loki or metrics installation is refused (edited in fleet.yaml, put in force at a restart); "Use the file's values" adopts the file's Alertmanager installations and keeps the Loki and metrics ones in force; an installation credential's Store, Remove and Test refuse another kind |
| External endpoints | An off row says its kind ("no log history is read", "no metrics are read") |
| The registry | `sources[].kind` names the three; Loki's and metrics' `source`, `scope` and `off` file-only, at a restart, with their effects per kind; `clusters[].metrics.match` deferred ("Not read yet…"); the snapshot regenerated |

**Tests:**
- config: each kind resolving its own installations (an Alertmanager and a Loki installation serving one group, no conflict), Loki's `none` for no block and for a block naming nowhere; the load errors (a metrics source naming a Loki installation, `metrics.match` legacy and subscribed, the token beside `source` and `off`, `off: false`, an empty source, a metrics subscription with no scope, two Loki installations at one step), a legacy token beside an endpoint valid; logs sharing a tenant told apart; a connection taking only its kind's installation; the Loki warning for an unlocated block only; S3a-1's tests that asserted "not built yet" now assert the per-kind rules;
- fleet: metrics off or subscribed never discovered, with the control counting discovery; Loki off, subscribed and unlocated; the factory per kind, a cluster's own block of one kind winning for it alone; the S2 candidate check refusing a Loki conflict;
- appbridge: a placement moving metrics or log history refused, the alerts' move still listed when nothing else moves; Observability listing, planning and adopting Alertmanager installations only, a Loki hand edit neither offered nor adopted; a Loki installation's credential and Test refused; the off row per kind.

**Mutation pass:** 29 mutants over the validation, resolution, connection blocks, readers, factory, S2's refusal, Observability's stage limits and the row text: 27 caught at first; one did not build (an unused variable) and, rewritten, is caught; one is equivalent: dropping `!cfg.Off` from `lokiLocated`, since an `off` block allows nothing beside it, neither a locator nor a source.

**Smoke:** the native walk at 8011c1c5, 217/217 four ways on the kind lab, the smoke fleet as it began after each. The smoke fleet has no Loki or metrics installations, so this is the regression check: nothing changes for a file without them.

## 17. S3b-1 revision 1: Codex's review (2026-10-28)

Codex found per-kind resolution, the connection boundary, S2's refusals (independently for protection edits and S2's file adoption too), Observability's stage limit and the registry sound, and no path where a valid off or subscription block falls back to a legacy read. It requested changes: one P2 and one P3.

| Finding | Fix |
|---|---|
| **R1:** a metrics `token` written empty or null, directly or through a merge, was allowed beside `source` or `off`: the check read the token's value, not its key | The check reads the key as written, whatever its value: `metrics: {off: true, token: ""}`, `{source: m, token: null}` and their merged spellings are refused. An empty token beside a legacy endpoint stays valid, and is still no credential |
| **R2, P3:** the refusal of a placement that would move a cluster's log history said it came "instead of discovery in the cluster"; Loki is never discovered | A Loki reader with no source is said as "no log history (Loki is never discovered)"; metrics and alerts keep their discovery wording, and the comparison is on the raw keys as before |

**Tests:** the six empty and null spellings, direct and merged, for `source` and `off`; an empty token beside a legacy endpoint loading and advertising no credential; the Loki refusal's words. **Mutation pass:** 3 mutants (the value-only check, a key check that always fires, the discovery wording), all caught.

**Smoke:** the native walk at 19d0d6d9, 217/217 four ways on the kind lab, the smoke fleet as it began after each.

## 18. S3b-1 ACCEPT (2026-10-28)

Codex accepted S3b-1 at fe2beb89 (its re-review of revision 1), with no new finding: R1 and R2 are closed, an empty or null token beside a legacy endpoint still loads as no credential, and Loki's placement refusal says "no log history" while metrics and Alertmanager keep their discovery wording. It noted one unrelated test-synchronization issue: `TestAReconnectIsHeldUntilItsOwnConnectionIsUsable` waits for the policy to be active and then expects the release hook to have run, while the hooks run outside the gate after the publication; S3b-2a's commit waits for both.

## 19. S3b-2a as built: the closed query surface (2026-10-28)

S3b-2 is built in three parts, each reviewed and accepted in turn:
- **S3b-2a** (this section): the closed metrics query surface, the scope on every metrics read, and `metrics.match` admitted on a legacy block (§3.3, §2.1).
- **S3b-2b**: reading through installations, for metrics and Loki: acquisition through the host's current connection or the direct transport, the host slots, the per-request fence, the lineage and the caches keyed by it (§3.1, §3.2, §3.5).
- **S3b-2c**: the views: the scope sentence, "no data under the scope", coverage per member, and the frontend's stores keyed by lineage (§3.3's last rule, §3.4).

Until S3b-2c a legacy reader with a `match` shows its scoped data in today's words: S3b-2a changes no wording, and a subscription still reads nothing (§2.4).

| Contract | Code |
|---|---|
| The surface (§3.3) | `metrics.Query` holds a template's name and its renderer, never a scope; only `metrics.Define` builds templates (`Template[P]`), and only `Template.With(params)` builds a Query. The 24 templates are in `internal/metrics/templates.go`, every selector written through `metrics.Sel(metric, own, scope)`. Callers pass values, never PromQL: `None`, `Namespace`, `Pod`, `PodSet`, `Routes`, `RuleProbe`, and `RuleRange` with its proxies as `ProxySet` values (label pairs and pod names; until now they were matcher text the rule reader wrote) |
| Every sink takes a Query | `Client.InstantScalar`, `InstantVector`, `RangeSeries` and `RangeMatrix`; the fleet's cluster, per-pod, per-container and sparkline reads, `ChatMetricsRange`, the route, rule and Hubble sources and the chat tool's templates. The liveness probe (`vector(1)`) is the one exception, unexported and outside the registry |
| The read owns the scope (§14, RR1) | `metrics.NewClient(transport, scope)` renders each Query at the sink with its own scope. The connection pins a client, its transport under `ScopeOf(metricsCfg.Match)` (`metricsCache.client`), and every reader takes that client: no caller chooses a scope, and no reader holds the transport, whose requests take PromQL text. A zero Query is refused before any request (`ErrUnregisteredQuery`), as is a scope whose label is not a label name (`ErrInvalidScope`); the client's redaction keeps both |
| The grid is enforced | Each template declares its cases beside its definition. `shape` names a parameter's structural form (every namespace or one, a rule family, one proxy set or several joined by "or"), and `With` returns a zero Query for a shape no declared case has, so an unproven form is refused rather than sent. `Define` panics on a template with no case or a name defined twice |
| Conjoined, never replaced | `Sel` adds the scope's matchers after the template's own; a template matcher on the same label stays, and a bare selector gains braces |
| The proof | `internal/metrics/promqlproof`, a module of its own (its `go.mod` replaces Klyx with the repository), so the Prometheus parser (v0.315.0) never enters Klyx's build or its `go.mod`. Every registered template's declared cases are rendered under three hostile scopes (several labels; values with quotes, backslashes, braces, a newline, PromQL of their own and non-ASCII text) and parsed: every vector selector, a range selector's and a subquery's included, must carry every scope pair as an equality with its exact decoded value; the query must read at least one selector, and no operand may make a series from nothing (`vector()`). CI runs it as its own step (`go vet` and `go test` in that directory, since `go list ./...` does not reach a nested module), and the local gates run it too |
| `metrics.match` admitted (§2.1) | The load refusal is gone. A legacy block's match scopes every query of the cluster; a subscription's match is resolved as S3b-1 resolves it and read from S3b-2b. Two clusters reading one metrics endpoint, one of them without a match, are warned about as Alertmanager and Loki are; the endpoint row's "unscoped" says it for metrics; the registry's `clusters[].metrics.match` describes what it does (file-only, as Alertmanager's and Loki's) |

**A deviation to rule on:** the proof module resolves its own dependency graph, in which the Prometheus parser requires client-go 0.37.0, so the proof compiles `internal/metrics` against 0.37.0 while Klyx builds with 0.36.2. Rendering uses nothing of client-go's; the proof's subject is the rendered text. The alternative was to raise Klyx's own client-go to take the parser as a test dependency.

**Tests:**
- metrics: every template, under an empty scope, renders the query Klyx sent before scopes byte for byte (the expected strings come from the format strings at a89d5876, with each declared case's parameters, and the test knows all 24 templates by name); a reader renders under its own scope whatever its caller had in mind, and an unscoped reader adds nothing; a zero Query, an undeclared rule family, a rule read with no proxies, a proxy set that writes PromQL in a label, names nothing or names pods under no label, all refused before any request; a scope label that is not a label name refused; `Define` with no case or a name twice panics; `Sel`'s conjunction; the census of every parameter form the readers build, each a declared shape;
- the proof: the three hostile scopes over every case, every template parsing unscoped, and twelve negatives that must fail for their own reason, not a parse error (an unscoped operand, an unscoped range operand, one scope matcher missing, a bare selector, the canary only inside a quoted string, the canary as a regex or a negated matcher, another value, a series from `vector(0)`, no selector, an unscoped subquery operand, and a query that does not parse), with a conjoined control;
- fleet: a legacy block with a two-label match (one value with a quote) scoping every data query of the cluster readout, the workload and route reads, the liveness probe unscoped; without a match nothing added;
- config: `metrics.match` loading on a legacy block, beside a legacy token, and on a subscription; the shared-endpoint warning for metrics;
- converted: every test that sent PromQL text now sends a template, or `raw()`, a test-only Query.
- carried: `TestAReconnectIsHeldUntilItsOwnConnectionIsUsable` waits for the release hook as well as the active state (§18's note).

**Mutation pass:** 31 mutants over the scope's rendering (`Sel`, the scope's matchers, `Equals`), each sink, the shape check, the proxy and scope label checks, `Define`'s refusals, the redaction, the declared cases, the connection's pinned client, the load's admission and warning, the row, and the proof's own checks: 28 caught at first; two did not build (an unused variable) and, rewritten, are caught. One survives where it is expected to: a template with one of its selectors written without the scope passes the fleet test, which checks that each query sent carries the scope; the proof, which checks each selector, catches the same mutant.

**Smoke:** the native walk at 7a7e5d31, 217/217 four ways on the kind lab, the smoke fleet as it began after each. The smoke fleet has no `metrics.match`, so its metrics views send the queries they sent before scopes, as the empty-scope test asserts byte for byte: this is the regression check.

## 20. S3b-2a revision 1: Codex's review (2026-10-28)

Codex agreed with S3b-2's three parts and with the nested proof module, and found every current sink typed, the empty scope unchanged for every declared case, and the proof's walk catching subqueries, offsets, `@`, binary operands and aggregations. It requested changes: three P2.

| Finding | Fix |
|---|---|
| **R1:** two ways around the proof. `Define` was exported, so a template defined in another package was outside the proof's registry and could ignore the scope; and the raw transports were reachable outside `internal/metrics` (their constructors, `Resolution.Transport`, `TransportFactory`) as a `Querier` taking PromQL text | Templates are defined only inside `internal/metrics` (`define` is unexported), so the registry the proof renders is every template there is. A transport is an opaque `metrics.Transport` everywhere outside the package: the constructors, `Resolution` and `TransportFactory` return one, and only a `Client` sends through it. `TransportOf` makes a test's fake one; a `Querier` written outside the package never reaches Prometheus, since a test scans every other package's Go files for the query API's paths (`TestNoOtherPackageWritesThePrometheusQueryAPI`, telling Loki's `/loki/api/v1/` apart) |
| **R2:** `With` checked a parameter's shape once and kept the caller's slices, so changing a proxy's label after admission rendered an unscoped operand | A `Query` keeps its own deep copy of its parameters: each template declares a clone (`PodSet`, `Routes`, `RuleProbe`, `RuleRange` with its `ProxySet`s), and `define` refuses a template whose parameter type holds a slice, map, pointer or interface without one |
| **R3:** a `__name__` scope was admitted, and `Sel` wrote the metric name twice, which PromQL refuses, while liveness still passed | A metrics scope never names a label Prometheus reserves (`__name__` or another `__` prefix): refused at load in a cluster's `metrics.match`, a subscription's match and a metrics installation's `memberLabel` ("is reserved by Prometheus: a scope matches series by their labels, and each query names its own metric"); the reader refuses such a scope before any request (`ErrInvalidScope`), and `Rendered` renders nothing under it. Alertmanager's and Loki's member labels are as before |

Also corrected: §19 says a removed case fails the proof; it is the root census (`TestEveryShapeTheReadersBuildIsDeclared`) that fails, beside the parser proof over the cases that remain. And `routemetrics/envoy.go`'s comment no longer calls the Envoy existence probe unscoped: it reads under the reader's scope, and its wording under one is S3b-2c's.

**Tests:** a query unchanged after its caller changes a proxy's label, its proxies, a pod name or a rule cluster, for every template whose parameters hold references; a template holding references refused without a clone; `__name__` and `__meta_*` scopes refused by the reader and rendering nothing for the proof; the load refusing a reserved label in a legacy match, a subscription's match and a metrics member label, with Alertmanager's member label as the control; the source scan and its own controls. **Mutation pass:** 15 mutants (the clone dropped or made shallow at each level, the reference check, the reserved-label checks in the reader, `Rendered` and each load path, a Prometheus path planted in the fleet, Loki's paths taken for Prometheus's), all caught.

**Smoke:** the native walk at 885158a3, 217/217 four ways on the kind lab, the smoke fleet as it began after each.

## 21. S3b-2a revision 2: Codex's re-review (2026-10-28)

Codex closed R1 to R3: an external `Define` and the raw transport calls no longer compile, the parameter mutations leave an admitted query as it was, and a reserved scope is refused at load, by the reader and by `Rendered`. Three P2 remained.

| Finding | Fix |
|---|---|
| **RR1:** `RuleShared` rendered from the exported, mutable `RuleFamilies` slice: another package could change an element and rewrite an admitted query into one with an unscoped operand | The rule families are the package's own (`ruleFamilies`, unexported). A test parses the package and requires every exported variable to be a registered template or an error, so no list or map a renderer reads can be changed by a caller (`TestNoExportedVariableFeedsARenderer`) |
| **RR2:** the source scan checked each literal alone, so `"/api/v1/" + "query"` passed it; and `TransportOf`'s "a test's fake" was a comment | The scan folds what the source fixes: literals, constants of the package built from constants, sums of them, and `path.Join`, `filepath.Join`, `strings.Join` and `fmt.Sprintf` of them. It flags any `api/v1` outside Loki's `/loki/api/v1/`, the API base alone included, with the load's four validation strings allowed by exact value in their files. It also flags any production call to `metrics.TransportOf` outside the package: the one there was, the connection's failing transport, is `metrics.FailedTransport` now. Fixtures cover a literal, a range literal, constant concatenation, pieces with no `api/v1` in any one, a joined path, a `strings.Join`, a formatted path, the API base alone and a `TransportOf` call, with Loki's paths, Alertmanager's API, a `query` parameter and another package's `TransportOf` as controls |
| **RR3:** the new reserved-label refusal echoed a long cluster name in full (`cluster %q`) | The refusal withholds a long cluster name and a long label, as every diagnostic of the file's values (`describeValue`, `describeKey`). The loop's other refusals - a duplicate name, a trailing `/api/v1`, an endpoint block's own error - withhold it too |

**Tests:** the exported-variable check; the scan's fixtures and controls; a long cluster name and a long reserved label withheld, a short name said as it is, and the loop's other three refusals with a long name. **Mutation pass:** 14 mutants (a renderer input exported, each fold disabled, the `TransportOf` check, the rule narrowed back to `api/v1/query`, the constants dropped, a production `TransportOf` and a formatted path planted in the fleet, and each of the four refusals echoing the name), all caught.

**Smoke:** the native walk at e08d021f, 217/217 four ways on the kind lab, the smoke fleet as it began after each.

## 22. S3b-2a revision 3: Codex's second re-review (2026-10-28)

Codex closed RR1 (the rule families are private, and no exported variable feeds a renderer) and RR3 (long names withheld at every refusal of the cluster loop), and the composed-path sender of its first re-review is caught. One P2 remained.

| Finding | Fix |
|---|---|
| **R2-1:** the scan matched names by their spelling and kept one flat map of constants: an aliased or dot-imported `path.Join` or `strings.Join`, `TransportOf` through an aliased import or as a function value, a numeric format argument, and a same-named constant of another function all passed it; an aliased production sender reached `/api/v1/query` with raw text | The scan is typed. It lists the repository with `go list -e -export -deps` and type-checks every package outside `internal/metrics` with `go/types` over that export data, failing if one does not type-check (a package read half blind is not scanned). `TransportOf` is found as the object it is, through any import spelling, as a call or a value; a constant is the value `go/types` binds where it is used, so each function's constants are its own; `path.Join`, `filepath.Join`, `strings.Join`, `url.JoinPath` and `fmt.Sprintf` are folded by callee identity over constant arguments of any kind, nested folds included. The fixtures add Codex's seven forms (an aliased and a dot-imported `path.Join`, an aliased `strings.Join`, an aliased, a dot-imported and a value `TransportOf`, a numeric format argument, lexically distinct constants) and `filepath.Join`, `url.JoinPath` and a nested fold, against stubs of this package and of another with its own `TransportOf`; the controls add a join of other paths and a constant of another function. The test walks the source tree itself, so go test's cache, blind to what `go list` reads, never keeps a pass once a file is added: a planted file added after a cached run fails it |

What the scan does not claim, as before: a path built at run time from data the source does not fix. CI's Go job runs it with the rest of `internal/metrics`; `go list -export` builds `cmd/klyx`'s dependencies there, as the job's GTK headers and the frontend stub already allow.

**Tests:** the fixtures and controls above; the repository scan, passing. **Mutation pass:** 12 mutants (Codex's aliased sender and an aliased `TransportOf` planted in the fleet, a numeric `Sprintf` and a lexically bound constant planted in the fleet, the object check reduced to a package name, each fold disabled, numeric arguments refused, constants ignored, Loki's paths flagged), all caught.

**Smoke:** the native walk at 5e0d2b76, 217/217 four ways on the kind lab, the smoke fleet as it began after each.

## 23. S3b-2a revision 4: Codex's third re-review (2026-10-28)

Codex closed R2-1: its seven binding cases and its aliased end-to-end sender are caught, an added Go file reruns a cached scan (go test hashes the walked directories' entries), and CI installs the GTK headers and stubs the frontend's dist before the listing. One P2 and one P3 remained.

| Finding | Fix |
|---|---|
| **R3-1:** the folds took each argument as a string, so a keyed literal (`strings.Join([]string{0: "", 1: "api", 2: "v1", 3: "query"}, "/")`) and a spread one (`path.Join([]string{...}...)`) passed, and both senders reached `/api/v1/query` | A string slice or array literal is read as Go lays it out: a keyed element at its constant index, a gap the empty string, an array filled to its length. A spread literal is the call's last arguments, for `path.Join`, `filepath.Join` and `url.JoinPath`, and a spread `[]any` literal for `fmt.Sprintf`. Any string slice or array literal whose pieces name the API, joined as a path or run together, is flagged wherever it is held, a variable's initializer included |
| **R3-2, P3:** `go list -e` exits 0 with a package's error recorded (cmd/klyx without its frontend's dist), and the scan dropped the record | A package in scope with an error record, or a dependency's, fails the scan, saying so, with the hint for cmd/klyx's dist; a listing that says nothing fails too. The platform build directories stay out of scope, errors and all. CI's stub is unchanged; a fresh clone runs the scan once the frontend is built or the stub created |

What the scan does not claim, as before: a path built at run time, including from a variable, and a joining function held in a variable (the analysis follows direct callees). Every literal and constant the source fixes, in any of the forms above, is read.

**Tests:** the fixtures add a keyed literal, a keyed literal with gaps, a spread `path.Join`, a spread `url.JoinPath`, a spread `Sprintf`, a literal slice and an array in variables, and pieces run together, with Loki's path, another API and other strings in pieces as controls; the listing failing on a failing command, an empty listing, an error record and a dependency's, the build directories out of scope; cmd/klyx without its dist failing the repository scan with the hint (checked by moving the dist away). **Mutation pass:** 11 mutants (keys refused, spreads ignored for the joins and for `Sprintf`, the slice check and its run-together form dropped, each listing check dropped, the build directories brought into scope, and Codex's keyed and spread senders planted in the fleet), all caught.

**Smoke:** the native walk at 1e5bf950, 217/217 four ways on the kind lab, the smoke fleet as it began after each.

## 24. S3b-2a revision 5: Codex's fourth re-review (2026-10-28)

Codex closed R3-1 (the keyed `strings.Join` and spread `path.Join` senders are caught in production source) and R3-2 (a missing dist fails the scan with its hint; CI's prepared job passes), and accepted the stated limits - a variable passed to a join, and a join held in a variable - as those of a bounded regression check. One P2 remained.

| Finding | Fix |
|---|---|
| **R4-1:** a keyed `[]any` spread into `fmt.Sprintf` (`fmt.Sprintf("/api/%s/query", []any{0: "v1"}...)`, a reordered key, or a gap read by `%[2]s`) was flattened as written, leaving a keyed element that abandoned the fold, and the literal check took string element types only; a planted sender passed the scan and the parser proof | One layout of a slice or array literal, as Go makes it - a keyed element at its constant index, the next unkeyed one after it, a gap, an array to its length - serves the string folds and `Sprintf`'s spread arguments, where a gap is a nil argument. A literal of any type whose elements are all strings is checked as a string one |

**Tests:** the fixtures add a keyed spread, a reordered keyed spread, a gapped spread read by an explicit index, and a literal of `any` holding the path in pieces; the unkeyed spread stays as the control. **Mutation pass:** 5 mutants (the spread flattened again, keys refused, gaps dropped from the arguments, `any` elements refused, and Codex's sender planted in the fleet), all caught.

**Smoke:** this revision changes only `internal/metrics/boundary_test.go`; the product code is the one smoked at 1e5bf950 (§23), 217/217 four ways, so no walk was repeated.

## 25. S3b-2a revision 6: Codex's fifth re-review (2026-10-28)

Codex closed R4-1: the keyed, reordered and gapped `[]any` spreads are folded, its planted keyed-format sender fails the repository gate, and thirteen further fixed forms and three controls hold. One P2 remained.

| Finding | Fix |
|---|---|
| **R5-1:** a written `nil` (`fmt.Sprintf("/api/%[2]s/query", []any{nil, "v1"}...)`, the keyed `[]any{0: nil, 1: "v1"}...`, or `nil, "v1"` as arguments) abandoned the fold, because nil is not a constant, while the omitted slot it equals was folded; a planted fleet sender passed the scan and the parser proof | A format argument the source does not fix is an opaque marker in the fold, never part of a path with what surrounds it: a format that never prints it (`%[2]s` skips the first) reads the same whatever it holds, and a result names the API only through what the source fixes. A written `nil` is such an argument, and so are `any(nil)`, `(*T)(nil)` and a variable in a skipped slot. Among a literal's pieces, the predeclared nil is a gap, known by its type: a constant named `nil` is the string it holds |

**Tests:** the fixtures add the three explicit-nil forms, a converted nil and a variable in a skipped slot, a variable beside a path whose fixed text names the API (`fmt.Sprintf("%s/api/v%d/query", x, 1)`), a nil among a literal's pieces, and a constant named `nil` holding a piece of the path; the controls add nil printed (`/api/<nil>/query`), a variable printed in the path's place, and a constant named `nil` that makes the pieces Loki's path, which a test by spelling would flag. **Mutation pass:** 5 mutants (a nil among the pieces no longer a gap, nil known by its spelling, an unread argument abandoning the fold again, and Codex's explicit-nil sender planted in the fleet as a spread and as arguments), all caught.

What the scan does not claim is unchanged: a path built at run time, a variable's value, and a joining function held in a variable.

**Smoke:** this revision changes only `internal/metrics/boundary_test.go`; the product code is the one smoked at 1e5bf950 (§23), 217/217 four ways, so no walk was repeated.

## 26. S3b-2a ACCEPT (2026-10-28)

Codex accepted S3b-2a at 801f760a, after its review and five re-reviews (§20 to §25), with no P1 or P2 remaining. Its rulings stand: the closed query surface, the Prometheus parser's proof over it, and the repository scan as a bounded regression check, not a compiler-enforced prohibition. Standing an unread argument in for a format that never consumes it is sound, and neither variable propagation nor an analysis of joining functions held as values is asked for; the stated limits are as §23 says.

**R6-1, P3, fixed as a follow-up:** a zero precision (`%.0v`) erases the unread marker as it erases any string, so `fmt.Sprintf("/api/v%.0v%d/query", nil, 1)`, which Go prints as `/api/v<nil>1/query`, was flagged as the API's path: a false alarm, never a way past the scan. The fold now keeps the predeclared nil as itself (known by its type, so a constant named `nil` stays its string), and the marker stands only for an argument the source does not fix. §25's marker is opaque except where a precision erases it, and there the result is flagged as a string in that place would make it. **Tests:** nil at zero precision, directly and spread, and the omitted slot it equals, none flagged. **Mutation:** the nil left to the marker again, caught.

## 27. S3b-2b as built: reading through installations (2026-10-28)

S3b-2b is §3.1, §3.2's keys and §3.5: a cluster's Loki and metrics subscriptions are read through their installations, per subscription, and a running connection follows its Loki and metrics to another source. The views' words (the scope sentence, "no data under the scope", coverage per member) and the lineage carried into DTOs and the AI adapter are S3b-2c's; Observability, Detect and the credentials of Loki and metrics installations stay S3b-4's.

| Contract | Code |
|---|---|
| Subscriptions per kind (§2.2, §3.5) | `AlertSources` resolves every kind in an Apply and keeps each cluster's subscription per kind (`SubscriptionOf(kind, cluster)`; `Subscription` stays Alertmanager's); an Apply reports the clusters whose subscription moved, by kind (`Moved`) |
| The terms (§13) | A read pins its terms once (`sourceTerms`): the subscription at its revisions, the installation's access generation, the host connection it was pinned to and a direct installation's credential revision. Their lineage key - kind, installation and its revision, subscription revision, access, tenant and scope - keys the member's caches; a credential's revision is checked, never keyed (§3.2) |
| The fence on every request (§3.1) | `endpointhttp.Fence.Do`: the terms checked before and after each request (`checkTerms`: the subscription still in force at its revisions, the host connected and usable, the access generation - which names the host's connection, so a reconnect moves it - and the credential revision), a 401 or 403 recorded before the request returns (`Denied`, moving the access generation), the host lease's end cancelling it. Metrics wrap it inside `internal/metrics` (`metrics.Fenced`) so the transport keeps its redactor; Loki fences each page's GET |
| Hosted reads (§3.1) | A lease on the host's current connection through S2's admission (`HostLeaser`), taken per logical read (metrics) or per page (Loki) and released when it settles, so no idle reader keeps one; its work is named ("the metrics of dev", "the log history of dev") in a host's reconnect plan. The host's guarded service-proxy client (metrics' now under its own lock, since a member may be its own host); the Service is the serviceRef, or for metrics the host's discovery, cached per host connection and installation revision (found for the connection's life, none or several for the 45 s unavailable TTL) |
| The host bound (§3.1) | `hostSlots`: four hosted metrics requests and two hosted Loki pages in flight through one host, keyed by the host's name and shared by every installation and member; a wait is within the request's context and a cancelled one leaves without a request; a slot is released once, when the request and its body have finished; a direct installation takes none. Loki's member and fleet-wide page slots are as before |
| Direct reads (§3.1) | The installation's endpoint with its own credential (`source/<id>`, of the kind), its revision gate and the subscription's tenant: metrics with the 3 s client timeout, Loki with the 30 s page timeout |
| Metrics on the member (§3.2) | A subscribed connection never resolves or discovers in itself (`ensureSubscribedMetricsLocked`): it probes through the installation, under the subscription's scope (none for `scope: tenant`), mode `subscribed`, its source named ("lgtm-we-metrics, Service monitoring/prom:9090 in prd"). The capability is cached under the lineage key while the terms check passes, so a known-down host or moved terms serve nothing cached; each reader builds its client for its read alone and defers its release; a result read across a move of the terms, or of the block's revision, is dropped whole and says why ("the read was dropped: the subscription changed; it is read again next time") |
| Loki on the member (§3.2) | `Loki()` builds a subscription's reader with the effective match (none for `scope: tenant`) and the tenant; its identity is the lineage key, so a move makes it no longer `Current` and the next page reads `ErrLokiSourceChanged`; a host that is down says so |
| The running fleet follows (§3.5) | `PublishSources` adopts each running connection's changed Loki and metrics blocks (`SetLokiConfig`, `SetMetricsConfig`, moving their revisions); a subscription moved under the same block re-keys its reads at the next one. S2 no longer refuses a placement or protection edit that moves a cluster's Loki or metrics: its plan lists the move ("Log history: no log history (Loki is never discovered) to the installation lgtm-we-logs", "Metrics: ...") |
| The Overview | The metrics line names a subscription's installation and where it is read ("monitoring: installation · lgtm-we-metrics, Service ... in prd") |
| The registry | Loki's and metrics' subscription keys and an installation's kind no longer say they are read from a later version; Settings edits them from S3b-4, a change put in force at a restart until then |

**Tests:** endpointhttp: the fence's check before (nothing sent) and after (the answer discarded), a 401 or 403 recorded, the slot held for the request and given back, a slot not had sending nothing, the lease's end cancelling the request. fleet: a hosted metrics subscription read through the host's proxy, liveness unscoped and every data query scoped, the tenant on each, and a `scope: tenant` subscription sending no matcher; a direct metrics installation without and with its credential, the secret never shown, and a credential replaced mid-read dropping the read; a read across a move of the installation, the subscription, the host's connection, its usability incarnation and an observed denial, each dropped whole for the cluster readout and for chat; a down host serving nothing cached; a 403 moving the access generation and re-keying the cache; the host bound (four metrics, two Loki pages, another host apart, a cancelled wait, a release freeing one once), two members' eight concurrent metrics reads never exceeding four in flight and six Loki pages never exceeding two; a hosted installation found by the host's discovery, cached for the host connection; a block naming another installation than the subscription in force reading nothing; a hosted Loki subscription's reader (match, tenant, the proxied path), a page after a denial of a new source, a down host said; a direct Loki installation with its credential; `Resubscribe` moving and clearing both blocks on a running connection; an Apply reporting moves per kind and leaving the other kinds' revisions. appbridge: a placement listing a metrics and a log-history move, never discovery for Loki, and the control. The S3b-1 boundary tests now assert the stage's contract: a subscription read through its installation, never discovered in the member.

**Mutation pass:** 26 mutants over the terms' check, the fence, the host slots, the direct credential and tenant, the discovery cache, the scope, the member's cache, settle and subscription match, Loki's page check, match and slot, the running fleet's adoption per kind, the Apply per kind and S2's listing: 18 caught at first. Five survived, each a test too weak, and are caught once it was made strict: the credential case asserted only that the read failed, the shared host bound released its hold before the rate-limited client had sent every read, the slot test counted with the constant under test, no test named another installation than the subscription in force, and none bounded Loki's hosted pages. Three did not build and, rewritten, are caught. A twenty-seventh, dropping an explicit check of the host's connection, survived as equivalent: the access generation names the host's connection, so the check repeated it and is gone.

**Smoke:** the native walk at 71b5043, 2b on 2a's revision 1, 217/217 four ways on the kind lab, the smoke fleet as it began after each: the legacy reads as before on a fleet without Loki or metrics installations. 2a's later revisions changed its base (§21 and §22 in product code, then only the scan test and this note); 2b's code on them was walked again within 2c's smoke at 94268bd0 and S3b-3's at f7233560, 217/217 four ways each, and since then 2a changed only its scan test (§23 to §26)

## 28. S3b-2b revision 1: Codex's review (2026-10-28)

Codex found the hosted and direct subscriptions, the effective match and tenant, `scope: tenant`, moves during reads, the per-kind timeouts, the host bound in steady state, the discovery cache, the per-kind adoption and S2's change lines supported by the code, and confirmed S3b-2a's R6-1 follow-up. It requested changes: three P2.

| Finding | Fix |
|---|---|
| **R1:** the fence bracketed a whole transport `Get`, so a redirect's hop (and client-go's retry) was sent after the read's terms moved; only the final answer was dropped | `Fence.Do` puts its check on the request's context as a read fence, which the transports run before every round trip: the direct transport already did, and the guarded service-proxy client - both hosted transports, client-go's `REST` and the raw one - now does too. A request that fails across a move says the move, never the transport's own error |
| **R2:** a hosted installation's discovery ran with the caller's context alone: outside the host lease's cancellation, the terms checks and the host slots, so it read a retired host's Services and ignored a full bound | The discovery's reads are hosted reads: under the caller's context joined with the lease's life, each Service read in one of the host's metrics slots, released before the next is taken, with the terms checked before and after it and at every round trip. A refusal, or the context's end, stops discovery there, with no fallback to the next candidate, and an ended or refused discovery is never cached. The subscribed probe and samples now settle before their host lease is released (§13) |
| **R3:** `Fence.Do` recorded a 401 or 403 only when the request succeeded, and client-go's `Stream` drops a denial's status when its body fails, so a denial with a cut or broken body left the access, and the metrics cached under it, current | An observer in the guarded service-proxy client records a 401 or 403 on the fenced request's context when its headers arrive, whatever its body then does; the direct transport returns a denial's status with its body's error. `Fence.Do` records the denial in either case, once per request |

**Tests:** endpointhttp: a redirect's hop after a move sent nothing, direct and through the raw proxy, each with the redirect followed as its control; client-go's retry of a 429 not sent after a move, its control retried; a 401 and a 403 with a body over its cap and one ending early recorded as a denial by the direct transport and both hosted ones, a 500 with a broken body not. fleet: a metrics range read whose redirect's hop is not sent once the installation's tenant moves, with its control; a hosted discovery read and cached as the control, nothing read on an ended lease, nothing read with every host slot held and nothing cached of it, a lease ending during a Service read cancelling it, the terms moving during a Service read stopping discovery with no fallback and nothing cached, terms moved before discovery reading nothing, and a lease ending in its last read leaving nothing cached as "no Prometheus"; a truncated 403 moving the access so the metrics cached before are not served; the probe and the samples settling before their lease is released. **Mutation pass:** 16 mutants (the fence not at every round trip, a failed request not saying the move, a denial recorded only on success, the observer recording nothing, each hosted transport unobserved or unfenced, discovery without a slot, outliving its lease, without its check before or after, an ended discovery cached, its error ignored, a context's end ignored, and the probe and samples released before settling): 15 caught. Two did not build and, rewritten, are caught; two survived until tests were added, for terms that moved before discovery and a cancellation in its last read, and are caught. One, discovery going on past a refusal, is equivalent and kept as Codex asked: a move or the context's end persists, so the gate refuses every later step and nothing more is sent

**Smoke:** the native walk at d8309b2e, 217/217 four ways on the kind lab, the smoke fleet as it began after each: the legacy reads as before through the fenced transports

## 29. S3b-2b revision 2: Codex's re-review (2026-10-28)

Codex closed R1 and R3, and R2's original probes, and agreed that discovery going on past a refusal is equivalent for a move of the installation or the context's end, the early return kept. One P2 remained.

| Finding | Fix |
|---|---|
| **RR1:** the discovery's gate checked the read's terms alone, and a Service read refused by the caller's own fence - a withdrawn chat read - was taken as a missing candidate, so the attempt cached "no Prometheus" for every later read of that host connection and installation revision | The gate checks the context's fence - the caller's, then the read's terms - before and after each Service read, and a read the fence refused at any round trip, a retry's or a redirect's included, is the discovery's refusal: returned, never taken as an absence, never cached |

**Tests:** through the cluster's client wrapped as production wraps it, a caller's fence refused before discovery (nothing read, nothing cached) and after the first candidate's 404 (one read, nothing cached), each followed by a fresh read that discovers again, and the unchanged fence's control finding the Service; each check on its own: the gate's through a client that does not check the fence, and a refusal at one round trip alone with the gate's checks passing. **Mutation pass:** 2 mutants (the gate checking the read's terms alone, a refused read taken as an absence): both caught, one rewritten to build.

**Smoke:** this revision changes the hosted discovery of a metrics installation, which the walk never reaches - its fleet has no installation (§27) - so no walk was repeated; the code around it is the one walked at d8309b2e (§28).

## 30. S3b-2b revision 3: Codex's second re-review (2026-10-28)

Codex found RR1's fix correct where it was made - discovery returns the caller's refusal and caches nothing - and R1 to R3 still closed. One P2 remained, where that refusal went next.

| Finding | Fix |
|---|---|
| **R2-1:** the member's metrics capability kept the refusal: a discovery the caller's fence refused, or a liveness probe it refused, became the member's "unavailable" for the short TTL, so a later valid reader was told of the old read's withdrawal and sent nothing; and the probe lost the refusal's identity, so checking the fence again after it would miss a refusal at one round trip alone | A refusal keeps its identity end to end: every read-fence refusal is `readfence.ErrRefused`, kept by the metrics client's redaction and the direct transport's (and the Loki reader's), and `metrics.Probe` marks a refused probe `Refused`. The metrics capability answers a refusal for that call alone and keeps nothing - an installation's discovery or probe, a cluster's own discovery or probe (the same flaw, on the legacy path), and its samples, where a refused sample is never taken as an absence. Ordinary unavailability is cached as before, and the moves, leases and slots are unchanged |

**Tests:** through `ChatMetricsRange` or `ClusterMetrics` under a withdrawn fence, then a fresh reader, without a forced re-probe, reading the healthy source: refused before discovery, after the first candidate's 404, before the liveness probe, at one probe round trip alone, a cluster's own endpoint, a cluster's own discovery (nothing kept), its samples, and one sample's round trip alone; a refusal is `ErrRefused` however it is wrapped, an ordinary error is not. **Mutation pass:** 11 mutants (the probe's mark; each place a refusal was kept - the subscribed probe and acquisition, the legacy probe and discovery, the samples; a refused sample taken as an absence; the client's and the direct transport's redaction dropping it; a refusal not `ErrRefused`; an ungated refusal taken as an absence): all caught, one after a test was added for one sample's round trip alone.

**Smoke:** the native walk at 17fef632, 217/217 four ways on the kind lab, the smoke fleet as it began after each: the lab cluster's own metrics and the chat's fenced reads through the changed capability path as before

## 31. S3b-2b revision 4: Codex's third re-review (2026-10-28)

Codex closed R2-1 in all four of its cases, confirmed the refusal's identity through the redactors, found the widening to the legacy path appropriate, and traced the other metrics readers and Loki's history to no other kept result. One P2 remained, in the same caches.

| Finding | Fix |
|---|---|
| **R3-1:** a caller whose context ended - not a fence's refusal, an ordinary cancellation - was still kept: a probe cancelled before or during liveness became the member's "unavailable" for the short TTL, and a sample read cancelled during a query became an available but empty sample for the samples' TTL | A read the call itself ended - its fence refused it, or its context ended - is that call's answer, never kept: the probe is marked `Withdrawn` (renamed from `Refused`) when its context ended too, and an installation's acquisition or discovery, a cluster's own discovery, and each sample the call ended return their outcome uncached. A backend's own failure, the direct client's timeout under a live caller included, is cached as before |

**Tests:** a caller cancelled before the probe, during it (the request arrived, then the context ended), during the samples, during a cluster's own probe, and in an installation's and a cluster's own discovery, each followed by a fresh reader, without forcing, reading the source; the control: a backend's failing probe is kept, a fresh reader sending no probe. **Mutation pass:** 5 mutants (the probe's mark without the context's end, an installation's acquisition and a cluster's own discovery kept when cancelled, a cancelled CPU sample taken as an absence, the call's end a refusal only): 4 caught. The fifth, the CPU sample's check reverted to refusals alone, is equivalent for a cancellation: a context that ended stays ended, so the memory sample's check takes it; the check is there for a refusal at one round trip alone, which R2-1's test holds.

**Smoke:** walked within the whole stack at 0ed8f444 (S3b-4c), 218/218 four ways on the kind lab, the smoke fleet as it began after each: the lab cluster's own metrics, the chat's fenced reads, and a metrics installation read through the changed capability path

## 32. S3b-2b ACCEPT (2026-10-28)

Codex accepted S3b-2b at 6b5dfbea, after its review and four re-reviews (§28 to §31), with no finding remaining. Its checks stand as the contract: every round trip of a read checks its terms; a hosted installation's discovery is a hosted read; a denial is recorded whatever its body does; and a read the call itself ended - its fence's refusal, or its context's end, a caller's deadline included - is that call's answer, never kept, while a backend's own failure is, the direct metrics client's own timeout under a live caller included. The equivalent mutants of §28 and §31 stand as stated, and the explicit checks are kept: the CPU sample's covers a refusal at one round trip alone.

## 33. S3b-2c as built: the views (2026-10-28)

S3b-2c is §3.2's lineage, §3.3's last rule and §3.4: every Loki and metrics read says where it was read and what makes its data this cluster's, an empty answer under a scope says so, and the views key what they show by the read's lineage. Observability's rows (the page) are S3b-4's; the AI's recording and checking of the lineage are S3b-3's.

| Contract | Code |
|---|---|
| The lineage (§3.2) | `internal/sources.Lineage`, moved from `internal/alertmanager` (which keeps it as an alias, its reads unchanged: their Kind and Match stay empty), with the kind and the effective match as display; `Key()` is an opaque, display-safe digest of every field. A subscribed read's lineage is its terms' (installation, revisions, host connection, access, tenant, match, credential revision); a cluster's own block's names its mode, its endpoint or Service, its tenant, its match and the block's revision |
| The scope sentence (§3.4) | S3a §3.5's form for metrics and Loki: a subscription "from lgtm-we-metrics, hosted by prd, tenant ops, filtered by cluster=dev" (or "everything in its tenant", or "at <endpoint>" for a direct installation); a cluster's own block "from https://mimir.example, filtered by cluster=dev", "from the Service monitoring/prom:9090 on this cluster", "from the discovered Service ... on this cluster", "unfiltered: everything there is read as this cluster's" (Loki: "every stream"). Values are display values, clipped as Alertmanager's |
| No data under a scope (§3.3) | "no data for dev under cluster=dev" when every query of a read answered with no series at all, told apart from a failed read, a non-finite value and gaps: `Sample.Empty` (an empty vector) beside `Absent`, `InstantVectorSeen` and `RangeSeriesSeen` counting every series the answer held, non-finite ones included. A read without a scope says an empty answer as before. The cluster readout, the per-pod and per-container usage and the sparklines carry it as `NoData`, apart from a caveat; the route and Hubble reads prefix their own "no series" words with it |
| The DTOs | The cluster readout, sparklines, node and container usage, workload usage, route, per-rule and Hubble statuses carry `sentence` and `lineage` (the key), and the readout, sparklines and usage `noData`; the log history page carries `sentence` |
| The views (§3.4) | One `SourceLine`: "Read from ..." with the no-data words beside it, or "Earlier read from ..., not current" for data kept from an earlier read. Shown under the Overview's monitoring line, in the Workloads and Nodes notices, under a pod's container usage, in the Gateway topology's footer (" · read from ..." or " · earlier read from ..." for kept numbers), in the data path's notes, in Hubble's footer, and in the log history header, which says it in place of the source and tenant. A workload's sparkline row says no data under the scope |
| Late replies and kept data (§13) | A reply applies only when no later read was asked for: the Overview readout and workload usage (a generation per bridge), node usage's refresh (a generation per view); route metrics, Hubble and the per-rule read already had theirs, and the other reads are made once per view. Usage and route numbers kept after a failed read keep their own read's sentence and lineage and are marked stale. A log page set already ends when the reader's lineage moves (S3b-2b) |

**Tests:** sources: the key moving with every field, a field's text never sliding into the next. fleet: the sentence and key of a hosted, tenant-wide and direct subscription, of an endpoint (with and without a match and tenant), a Service and discovery, the key moving with the access generation, and none for an unavailable read; no data under the scope for the readout, container usage, sparklines, routes and Hubble, and not for a non-finite answer, a gapped one, a failed read or an unscoped one; every Loki reader's sentence and lineage. appbridge: every DTO carrying the sentence, key and no data, no data apart from a caveat. Frontend: `SourceLine`; the readout's and usage's late replies; kept usage and route numbers keeping their sentence; the line in the Overview, Workloads (and its stale form), Nodes (and a refresh's late reply), Pods, Gateway (live and kept), data path, Hubble and log history; a sparkline row's no data.

**Mutation pass:** 32 mutants (19 backend: each sentence part, the keys, the empty-answer flag and the series counts, the route and Hubble prefixes, Loki's sentences, the DTOs and the lineage key's fields; 13 frontend: the stale qualification, each late-reply guard, the kept sentences, and the line in each view), all caught.

**Smoke:** the native walk at 94268bd0, 217/217 four ways on the kind lab, the smoke fleet as it began after each: the Overview, Nodes, Pods, Workloads, Gateway, Hubble and log views render with the new lines on a fleet without installations (their legacy sentences); and again within the whole stack at 0ed8f444 (S3b-4c), 218/218 four ways, where the Nodes view says where a metrics installation's data comes from and that there is none under its scope. Since the first walk, 2c changed only where it met S3b-2b's revisions (§28 to §31) in the metrics reader - the sample's no-data words beside the call's own end, the lineage beside the withdrawn probe - keeping both.

## 34. S3b-2c revision 1: Codex's review (2026-10-29)

Codex found the neutral lineage and its opaque key, the result DTOs' source fields, the scalar, vector and range presence distinctions, the source captions in the principal views, the late-request guards and the kept workload and route provenance supported by the code. It requested changes: four P2.

| Finding | Fix |
|---|---|
| **R1:** the metrics sentences and the no-data words were built with an empty redactor, and a subscription's tenant was joined unclipped, so a tenant or match value equal to the reading credential reached `MetricsCapability.Sentence` and `ClusterMetrics.NoData` verbatim | What a read says about itself is said without the credential it reads with: `metrics.Transport.Redactor()` is the transport's credential as a redactor, and the sentence (a cluster's own block and a subscription alike) is built from tenant and match words cleaned with it, each part clipped and the whole bounded as the Loki reader's. The empty answer's words are the same display words, kept beside the lineage; the lineage keeps its terms raw, as identity behind its digest, and the request still carries them raw. Loki's subscribed sentences take the same redactor |
| **R2:** the route and Hubble reads set `NoSeries` from reduced results: `InstantVector` drops a non-finite series, and the route reader's existence check took an absent value for an empty vector, so NaN data read "no data for dev under cluster=dev" and Hubble's said to enable its metrics | Both readers count the series each answer held, a non-finite one included, beside those with a value. No series at all is still `NoSeries`; series that all lack a usable value say so ("no usable value (NaN or Inf)"); the existence check is none only for an empty vector or a zero count, and a non-finite count proves no absence. Hubble's series that hold none of the rates Klyx reads (settled drop reasons at zero, a verdict it does not show) no longer say that Hubble's metrics are off |
| **R3:** the Overview's 30-minute charts, read once per mount, ignored their own sentence, lineage and no-data words, so charts from source A sat under source B's readout caption after a refresh, and an empty range under a scope showed only the chart's own "no data"; the expanded workload charts likewise | A range read's charts say their own source wherever the view's readout does not already say it (`ChartSource`, `chartSentence`): nothing where the readout reads the same lineage, "30m charts: Earlier read from …, not current" where the readout now reads another, their own sentence where there is no current readout, and the range's own no-data words. The expanded workload row compares its charts with the lens's usage readout the same way |
| **R4:** a rejected Overview refresh cleared only `loading`, so the earlier readout's percentages stayed under an unqualified "Read from …" | The metrics slice carries `stale`: the latest request's failure (the generation guard unchanged) marks a kept readout, which says "Earlier read from …, not current" until a read answers; a visit to another cluster starts unmarked |

**Tests:** fleet: a cluster's own block and a direct installation whose tenant and match equal the credential, each an available empty read, say neither the sentence nor the no-data words with it and send both raw; a subscription's long tenant clipped in its sentence; route and Hubble NaN answers not called no series under the scope. routemetrics: non-finite route series and a non-finite existence count are not no series, a zero count still is. hubble: non-finite series and settled zero drops are not no series. Frontend: `ChartSource`'s four cases and an unavailable range; the Overview with one source (no chart caption), an empty range under its scope, and the readout moving to another source under kept charts; the workload row's charts against another usage readout; a lost refresh qualified until the next answer, an older request's failure never marking a newer answer, and a visit to another cluster unmarked. Codex's three probes are adopted as these tests. **Mutation pass:** 24 mutants (each display word unredacted: a cluster's own block, a subscription, the transport's redactor, the empty answer's words in the readout, a range read and a subscription; the subscription's tenant unclipped; a route answer's non-finite series unseen, the existence check's absent value taken as none or as a match; Hubble's usable series taken for its series, settled zero drops called none, its series uncounted; a chart always sharing the readout's source, a moved one not qualified, the Overview's chart caption and its no-data words dropped, the workload row never comparing; a lost refresh unmarked, marked without the generation guard, kept stale by an answer or across a visit, and the readout's caption unqualified): 22 caught. Two survived until tests were added and are caught: the empty answer's words of a range read reverting to the raw match (the credential test read only the instant readout; it now reads a range too), and a subscription's match words without the redactor (the sentence's final redaction caught a short credential; a credential longer than a part's clip, clipped before it was redacted, would have shown a prefix of it - the test now holds one)

**Smoke:** the native walk at 32358904, 217/217 four ways on the kind lab, the smoke fleet as it began after each, the labs applied first from `scripts/lab/smoke` (PR #61)

## 35. S3b-3 as built: the fences (2026-10-28)

S3b-3 is §4: every AI read of logs or metrics records its lineage, and every later send checks it. With it, S3a §0's gap closes for Loki and metrics: a change to a legacy block's endpoint, tenant, match or credential ends later sends of the data read before it. The UI views are not fenced beyond their keys (§3.2, S3b-2c).

| Contract | Code |
|---|---|
| Recorded (§4) | `get_metrics` records the read's lineage (`ChatMetricsRange` returns it with the series); the route templates' per-rule reads record each Gateway's (`routemetrics.Status.Provenance`, the whole lineage beside its key); `get_log_history` records its reader's (`LogHistoryPageDTO.Lineage`, never serialised). The holders already took `sources.Lineage` (S3b-2c's move), so a conversation keeps every kind's tokens cumulatively, its trimmed exchanges' included, as S3a's do. A read already under moved terms is refused, never recorded: "...; ask again to read it under the current terms" (alerts keep their cadence wording) |
| Checked (§4) | `Registry.LineageProblem` dispatches by kind. A subscribed read of any kind: its installation and the member's subscription of that kind at their revisions, the host's pinned connection current, usable and at its access generation, or a direct installation's access generation and credential revision of its kind. A legacy metrics read: the block's revision (its credential and configuration move it) and the source identity the connection now holds - its endpoint, Service or discovered Service, tenant and match - so a discovery that now finds another Service ends later sends; the identity is kept while the source is unreachable, so an outage ends nothing. A legacy Loki read: the reader the block resolves to now, its identity, tenant, match and revision. The turn's guard and A3's plan fence already check every token held, of any kind; the refusal names it ("lgtm-we-logs changed since its logs were read: start a new conversation") |
| Plans' affected work | A conversation is listed with what it holds: "holding alerts, logs and metrics read under the old terms" |

**Tests:** fleet: a subscribed metrics and Loki lineage holding, a denial ending the metrics one and not the logs', an installation edit ending the logs', another cluster's subscription, a host reconnect, a direct installation's credential; a legacy metrics lineage holding through an outage and ended by a block change and by discovery finding another Service; a legacy Loki lineage ended by a tenant change and by removal, and by a tenant change with no revision registry at all. appbridge: a metrics read recorded and the conversation ended at its next send once its terms move, no token in any request; a read under moved terms refused and not recorded, the conversation going on; a log history read recorded and ending the conversation; a route read recording each Gateway's lineage and refusing a moved one; the held kinds' words.

**Mutation pass:** 17 mutants (the kind dispatch, the subscription and credential of the kind, each legacy check, the noun, an outage dropping the identity, each read's lineage and each tool's recording, the stale wording and the held kinds' words): 15 caught at first. One did not build and, rewritten, is caught; one survived, the legacy Loki identity check dropped, because every change of a block also moves its revision: a case without a revision registry now shows the identity check's own work, and it is caught.

**Smoke:** the native walk at f7233560, 217/217 four ways on the kind lab, the smoke fleet as it began after each: the views and the chat drawer as before. The walk reads no logs or metrics through the AI, so the fences are the tests' to show

## 36. S3b-4a as built: plans, credentials and the installation's Test (2026-10-28)

S3b-4 is built in three parts, each reviewed: 4a, every kind's plans and Apply, its credentials and the installation's Test (§5, §7); 4b, the fleet reads Settings asks for - Detect for metrics and a Loki subscription's Test, both fenced reads with a lease, a slot and a deadline - and the migration offer for every kind (§6, §7); 4c, the page and the registry (§8). Until 4c the page is S3a-4c's Alertmanager panel, its bridge asking for its kind.

| Contract | Code |
|---|---|
| A plan is of one kind (§5) | `SourcesDraftDTO.Kind` (none is Alertmanager's, so S3a's drafts are unchanged): the installation added, edited or removed is of that kind, and the subscription blocks written are that kind's. `config.SourcesEdit.Blocks` is keyed by `BlockRef{Cluster, Kind}`, and the writer writes an `alertmanager`, `loki` or `metrics` block alike, refusing any other key. Editing or removing another kind's installation is refused ("logs is a loki installation: its kind does not change"), and an unknown kind is refused by every call |
| The candidates (§5) | the runtime candidate resolves every kind, as S3b-1's; the disk candidate changes only the plan's blocks. A metrics subscription replaces a legacy block whole, a plaintext token with it, and the running entry takes the shared shape's fields (`ClusterConfig.SetBlock`), never a token |
| What a plan says (§5) | the installation's inputs, as S3a's; each block ("dev's loki block: none: by name or group → subscribed to lgtm-we-logs"); for every kind, each running member whose resolution moves, with its scope sentence before and after ("Logs for dev: none → from lgtm-we-logs, hosted by prd, filtered by k8s_cluster_name=dev"); and what ends: "dev's log views start again under the new terms", "dev's metrics are read again under the new terms: its cached readings are dropped", or the alerts read again, as S3a's |
| Affected work (§13) | `Work(moved)`, for each kind the clusters whose terms of that kind move. A conversation, or a running Explain, is named only when it holds a lineage of a moved kind on a cluster where that kind moves, with what it holds of it ("holding logs read under the old terms"): a metrics-only Apply never names an alerts-only conversation, nor one on another cluster. S3b-3's registration lock already covers every kind |
| "Use the file's values" (§5) | per kind: `PlanSourcesFromFile(kind)` adopts the file's installations of that kind and its clusters' subscription blocks of that kind, never a legacy block, and keeps the other kinds' installations in force as they are; `GetSources(kind).FileDiffers` says that kind's difference only, so one panel never adopts another's drift |
| The state | `GetSources(kind)`: the kind's installations; each running cluster's resolution of that kind, its scope sentence and its own block, or that the block is legacy; and the kind's sentence ("Logs: 1 cluster from 1 installation, 1 with none"). In Loki's words a cluster with no Loki has none, never discovery, and its own Loki reads "every stream there" as its own, as its views say (S3b-2c) |
| Credentials (§7) | an installation of any kind takes its credential under `source/<id>`, at its kind's revision: Store and Remove move that kind's revision and revocation, never Alertmanager's, and the credential's state reads its kind's |
| The installation's Test (§7) | `Registry.InstallationProbe`: Loki's build info, or metrics' liveness probe (`vector(1)`, unscoped, no series), through the transport the installation's reads take - its endpoint with its credential, or its Service through a lease on its host (the S3b-2b transports, factored out as `metricsTransport` and `lokiSource` with the fence as a parameter). The request is fenced before and after by the reviewed revision, the host, the access generation and a direct installation's credential revision, so the result settles before the lease is released; an observed 401 or 403 is said and never counted as a loss of access; a Loki Test holds one of the fleet's page slots, within the 30 s deadline. Alertmanager's stays `InstallationStatus` |

**Tests:** config: a plan writing a loki and a metrics block and an off block in one candidate, a metrics subscription replacing a legacy block and its token, a removal keeping the other kinds' blocks, an unknown kind refused. fleet: a direct Loki and metrics installation's Test (the request made, its words and where), a denial said and not counted, a stale revision, an Alertmanager installation sent to its status, an installation moving before the request (nothing sent), and a Loki Test waiting for a fleet page slot within its deadline. appbridge: a panel planning its own kind (another kind's installation refused, each kind's hand edit offered and adopted by its own panel only, an unknown kind refused); a Loki installation and subscription planned and applied (the block, the sentences, the log views and the work holding logs ending, nothing of alerts; the file, the running entry and the subscription in force; the installation's edit; a subscription to another kind's installation refused); a metrics subscription with its match planned, applied and removed; a legacy metrics block never adopted; each kind's words; the work and an Explain named by kind and cluster; a Loki installation's credential stored, removed and tested under its kind, its state revoked in memory, and a metrics Test's refusal said.

**Mutation pass:** 31 mutants (the empty and unknown kinds, each refusal across kinds, the block, the candidate, the Apply and the running entry taking Alertmanager's kind, the file's values and their difference for every kind, the state resolving and listing Alertmanager's, each kind's words and ends, the metrics match, the credential's revision, revocation and state under Alertmanager's kind, the work and the Explain not filtered by kind, the Test not dispatched, a denial counted, the fleet slot, the writer's key): all caught. Twelve were first run under a test filter that matched none of the new tests, three of them not building; under the corrected filter, the three rewritten, all twelve are caught

**Smoke:** the native walk at 86d13587, 217/217 four ways on the kind lab, the smoke fleet as it began after each: the Alertmanager panel, its plans and Test as before, its bridge asking for its kind

## 37. S3b-4b as built: Detect for metrics, a subscription's Test, the offer for every kind (2026-10-28)

S3b-4b is §6 and the rest of §7: the reads Settings asks the fleet for - Detect for metrics and a Loki subscription's Test, each fenced, with a lease, slots and a deadline - and the migration offer for every kind. The page that shows them is 4c's; until then the bridge carries them with the Alertmanager panel's defaults.

| Contract | Code |
|---|---|
| The metadata sample (§6, step 2) | `metrics.SampleMetadata`, beside the query templates in the package that alone writes the Prometheus API's paths: one `/api/v1/labels` answer for the hour asked for, then `/api/v1/label/<name>/values` for at most eight labels - `cluster`, `k8s_cluster_name`, `cluster_name` and `kubernetes_cluster` first, asked for whether or not the names answer listed them, since it may have been cut, then the other names alphabetically. A reserved name (`__name__`) is never asked for, and a name outside the plain label grammar never enters a path, so no name needs escaping; both are counted with the names past the eight as not asked for. Each answer goes through the member's guarded proxy at a 1 MiB cap of its own: one larger is cut and not examined. A list is read in its first 2,000 values, counted before any filtering, and one holding more is said to be cut. Only names, and values exactly equal to a fleet cluster's name, are kept; a warning, an error body or any other value is passed over. Like `Liveness`, it takes no PromQL: its paths are fixed here |
| Detect's discovery (§6, step 1) | `ClusterConn.metricsCandidates`: every named Service present, at its port, and each port of every Service a selector matches; one Service found both ways is one candidate, and distinct ports or paths are distinct ones. A read that fails, or a discovery the deadline cut, is named, and the list is then never taken as the whole one: no sample, and the candidates found so far "may not be all". Reading's ranked discovery is unchanged |
| Detect's lifecycle (§6, step 5) | `Registry.DetectMetrics`: one of Detect's own two places fleet-wide, waited for within the member's 30 s deadline; a lease on the member; for exactly one candidate, the sample through the member's Service proxy, each request in one of its host slots and fenced by the lease; a retirement or the deadline discards what it read before the lease is released, as Alertmanager's Detect does (S3a-4b, R1) |
| The proposal (§6, step 4) | one candidate proposes a metrics installation hosted by the member at that Service (`metrics-<cluster>`), with the member label when exactly one label's values named fleet clusters, the members seen, the sample's time and bounds, and notes: what was cut, failed or not asked for; "label metadata for the hour asked for, not proof that a cluster produced metrics in it"; and that a cluster not seen proves no absence. Several candidates propose nothing and are each named with how they were found. Loki is refused: it is never discovered |
| A subscription's Test (§7) | `SourcesService.TestSubscription(cluster, kind, instRevision, subRevision)`: the page sends the member's name and the revisions its row showed (`SubscriptionRowDTO.InstRevision`, `SubRevision`, for a subscribed row only), never a tenant or a match. Only Loki's subscription has one; an Alertmanager or metrics member is tested through its installation. `Registry.TestLokiSubscription` checks the revisions shown against the subscription in force, leases the member, and reads through the member's subscribed reader, whose terms are pinned from admission: the build info, then the label names of the member's streams over the last hour under its effective match and tenant, never a line, each page fenced by those terms under the host's lease and slot rules. A move of the terms reads as a changed subscription, and the member's reconnection or removal discards the result, settled before its lease is released. It answers with the scope it read under, and whether any stream answered ("an unknown tenant, or a match that names no stream, reads the same") |
| The offer for every kind (§7) | `MigrationOffers(kind)`: one direct URL on several clusters' blocks of a kind, as S3a's, each panel asking for its own; the offered installation (`am-`, `logs-` or `metrics-` and the host) and its subscriptions are that kind's, and each cluster's scope before and after reads in that kind's words. A legacy metrics `token` is never carried: a token without `auth` is bearer auth for the comparison and the draft (`auth: {type: bearer}`), never the token, and the offer says the installation needs its own credential, a cluster's token leaving fleet.yaml only with its block. Its conflicts, choices and scope statements are S3a's |

**Tests:** metrics: a sample keeping names and fleet names only (no other value, number or error body), its order and its eight, the window asked for, a fleet name past the first 2,000 values not counted and its control within them, an answer over its cap not examined, a failed names answer saying nothing of the members, the fence's check sending nothing, and the answer's strict shape. fleet: the one candidate sampled, no candidate, several named with one Service found two ways counted once and its ports apart, an unreadable discovery not taken as all, Detect's pool and the host's slots waited for within the deadline, a lease ending after a body discarding the sample; a member's Loki subscription tested under its scope (the labels query, the tenant, no line read), a stale revision, no subscription, and the terms or the member moving during the Test. appbridge: the metrics proposal (its words, notes and draft, which plans), several Services, an unreadable discovery, an error, none, a sample unread, Loki and an unknown kind refused; a subscription tested from its row, with data, without, another member's failure its own, a revision that is not one, another kind refused, a legacy row with no revisions; the Loki offer and its scopes, the metrics offer carrying no token, bearer's shape, its credential words, its shared-tenant choice, applied with each block and token replaced, and an explicit auth beside a token's in conflict; the Alertmanager panel offered no other kind's.

**Mutation pass:** 38 mutants (the member labels asked for only when listed, a reserved name asked for, any name in a path, no bound on the labels, one value too many, the cap raised, any value kept, the status unchecked, data twice, the window; an unreadable list or one of several sampled, an absent Service taken as unreadable, one Service found two ways not one, the first port only, no host slot, no pool, the lease unsettled; the Test's revisions unchecked, its member unsettled, its move unsaid; Loki detected, metrics detected as Alertmanager, an unreadable discovery or several Services not said, each note dropped, the draft's kind dropped, the subscription Test of any kind, the row's revisions dropped; the offer reading Alertmanager's block, a token not bearer, its note dropped, Alertmanager's id, block, resolution and sentence): all caught, four first rewritten to build

**Smoke:** walked within the whole stack at 0ed8f444 (S3b-4c), 218/218 four ways on the kind lab: a member's Loki subscription Test from its row, saying the member is not connected. Detect for metrics and the offer for Loki and metrics are not walked: the lab's Prometheus is named so that no discovery finds it, which keeps the lab cluster's own metrics as they were, and no two clusters of the smoke fleet share a direct URL; their tests show them

## 38. S3b-4c as built: the page (2026-10-28)

S3b-4c is §8: Observability's "Alert sources" is now **"Sources"**, one panel per kind on S3a-4c's components, and the registry's subscription entries of every kind are editable.

| Contract | Code |
|---|---|
| One panel per kind (§8) | `SourcesPanel` takes its kind: it reads its kind's state and offers, plans its kind's drafts (an Alertmanager draft carries no kind, as S3a's), adopts its kind's file values, and says its kind's words - "Each cluster's logs", "Its own loki block is set in fleet.yaml", "Klyx reads the logs under the new terms". The Observability section shows Alertmanager, Logs and Metrics in that order under "Sources"; its sentence is every kind's once each has read ("Alerts: ...; Logs: ...; Metrics: ..."), and the section is ready, so an anchor applies, only once every panel and the endpoints have read or failed to (S3a-4c, R6; §13) |
| S3a-4c's discipline per kind (§13) | Each panel is its own leave-guard owner (`observability-sources-<kind>`), so one panel's draft never answers for another's; refreshed-away drafts kept, late plans and readbacks refused, revision-keyed credential clearing and proposal ids are each panel's, as S3a-4c built them. Every repeated control has an address of its own: a kind's words in its labels ("use the file's log sources", "add a Loki installation", "what metrics Detect looks in", "edit dev's log subscription"), installation ids being unique across kinds; and a subscription row's anchor is its kind's (`clusters[].<kind>.source@<cluster>`) |
| The editors (§8) | The subscription editor's fields are its kind's (`clusters[].<kind>.off`, `source`, `scope`, `tenant`, `match`), named "Edit dev's log subscription"; the installation editor offers no discovery for Loki |
| Detect and the Test (§6, §7) | The Alertmanager and Metrics panels have Detect, the metrics one asking for metrics and saying what its sample reads; the Logs panel has none. A subscribed Loki row has its own Test, which sends the member's name and the revisions its row showed |
| The registry (§8) | `clusters[].loki` and `clusters[].metrics` `source`, `scope` and `off` are editable on Save, each in its kind's panel; `sources[].kind` says an installation's kind does not change; `serves` names a block of the installation's kind; a `match` entry says its two dispositions - edited on a subscription block, set in fleet.yaml on a cluster's own block (metrics' beside a legacy token) - and an installation credential's Test says what each kind's Test reads |

**Tests:** the Logs panel reading, adopting and planning Loki's, in its words, with no Detect, a subscription draft of Loki's fields; a Loki row's Test sending the member and the revisions shown, and none on a legacy row; the Logs installation editor without discovery; the Metrics panel's Detect and its proposal reviewed as a metrics draft; the three panels' controls each with an address of its own, and a Logs draft asking before leaving; Observability waiting for every kind and saying every kind's sentence; the registry snapshot. S3a-4c's Alertmanager panel tests, with its kind-qualified control names. **Mutation pass:** 17 mutants (each panel reading, drafting and adopting Alertmanager's, one shared leave-guard owner, Detect for Loki, Detect without its kind, no Loki row Test, Alertmanager's anchor, words and legacy words, the editor's fields and name as Alertmanager's, discovery offered for Loki, the section ready on one kind, a partial sentence said, an unaddressed control, and the registry's subscription entries file-only): all caught, one after the test was given another panel re-rendering after a Logs draft

**Smoke:** the native walk at 0ed8f444, 218/218 four ways on the kind lab, the smoke fleet as it began after each. A new step adds a metrics installation hosted by the lab cluster against the lab's Prometheus (a lab of its own, klyx-ss3b-lab, every series relabelled cluster=kind-klyx), serving the lab cluster by name under the member label cluster: its plan says "Metrics for kind-klyx: discovered in this cluster, unfiltered → from lab-metrics, hosted by kind-klyx, filtered by cluster=kind-klyx", its Test answers, and the Nodes view says "Read from lab-metrics, hosted by kind-klyx, filtered by cluster=kind-klyx · no data for kind-klyx under cluster=kind-klyx" (the lab's Prometheus has no node series). It then adds a Loki installation over the lab's Loki serving aks-stg-weu, tenant lab and member label k8s_cluster_name: its plan's "Logs for aks-stg-weu: none → from lab-logs, hosted by kind-klyx, tenant lab, filtered by k8s_cluster_name=aks-stg-weu", its Test ("Loki 3.7.8 answered"), and the member's own Test, which leases the member, saying aks-stg-weu is not connected. Each is removed through its plan. The log history pane's sentence for a subscribed member is not walked: the lab cluster keeps its own Loki block, which Settings never replaces, and the smoke fleet's other members are not connected; S3b-2c's tests show it. Two earlier walks failed on this step's own expectation of that refused Test (its banner is marked ▲), not on the product
