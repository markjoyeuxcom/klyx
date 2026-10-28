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
