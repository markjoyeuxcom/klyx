# Klyx Settings S3a: Alertmanager installations and subscriptions

Opened 2026-10-27, on `feat/settings-s3a`, after S2b was accepted (S2 §24) and with S2c in review. S3a is the redesign's first observability slice (redesign §6, §14, §17a). It builds, for Alertmanager:
- installations and subscriptions: the schema, resolution and validation, and one effective scope per subscription;
- reading in buckets, with the split and what matched no member;
- the scope sentence;
- the host fence on reads and on later sends;
- Detect, and the migration offer.

Loki and metrics follow on the same model in S3b. This note is for Codex's review before anything is built; revisions 1 and 2 the same day, after that review and its re-review (§12, §13).

## 0. What exists

From a read of the code at 10253e1 (S2c's head), with an inventory of the alerts path, the endpoint credentials and the AI fences.

**One block per cluster, read through its own connection.**
- `clusters[].alertmanager` is `config.EndpointConfig`: `endpoint`, `serviceRef`, `auth`, `tenant`, `match` (`internal/config/endpoints.go:38-49`). The factory hands it to the cluster's own connection (`internal/fleet/factory.go:63-65`).
- Resolution, per connection (`resolveAlertmanagerLocked`, `internal/fleet/alerting.go:259-303`): the endpoint, else the serviceRef through the cluster's own API server, else discovery in the cluster, with its proof of one installation (`:389-531`). The block's tenant and match apply to all three.
- `alertmanager: {}` counts as configured at once (the Alerting capability), and resolves by discovery. No block resolves by discovery too, and is present only once a read finds one (`:102-112`).
- The source identity is `how|at|tenant=…|match=…` (`:205-207`). It keys the alerts snapshot: a different identity re-keys it (`:161-181`, `:551-562`). The credential is not in the key.

**The read** (S8a):
- The per-cluster health Observer reads the `alerts` snapshot every 30 seconds, with a 10-second timeout that includes the wait for a slot (`internal/healthsrc/alerts.go:19-68`, `internal/fleet/conn.go:1077-1083`).
- One read per cluster at a time (`readMu`), at most four fleet-wide (`amReads`, `alerting.go:45`, `:540-548`).
- `/api/v2/alerts` and `/api/v2/alerts/groups`, the match sent as equality filters, and every returned alert checked again against the match (`:569-595`).
- Bounds (`internal/bounds/bounds.go:41-43`): 5,000 entries after mute reduction is a truncation, a lower bound; 8 MiB and 20,000 group appearances fail the whole read.
- An alert that fails the match is dropped. Nothing counts it.

**Credentials** (C5):
- The keychain account is `endpoint/<cluster>/<kind>`, a record bound to the endpoint's origin (`internal/endpointcred/endpointcred.go:20-38`, `:110-179`).
- Revisions per cluster and kind, checked before and after a direct request (`endpointcred.go:191-268`, `internal/endpointhttp/endpointhttp.go:128-149`).
- Store, Remove, Move and Test are immediate actions under the endpoint lock, then the fleet Apply lock (`internal/appbridge/endpoints_service.go:94-143`).
- A hand edit of an endpoint block takes effect at restart, or when Test or a credential Store adopts it. Fleet drift ignores endpoints, and S2's plans keep the running endpoint terms.

**What is shown:**
- The Alerts lens header names the source (a URL, a Service, or "discovered …"), its freshness and the match in a tooltip; the footer says the match, or that every alert there is read as this cluster's (`AlertsView.tsx:187-202`, `:255`). There is no scope sentence.
- v0.15.1's hint: several values of a cluster label, with no match, suggest a shared Alertmanager (`alertsModel.ts:232-244`).
- `sharedWithoutMatch` warns when two clusters name one direct URL and one has no match (`endpoints.go:184-219`), in the log, Fleet and Diagnostics.

**AI** (`internal/appbridge`):
- `get_alerts` and Explain read the Observer's snapshot (`chat_alerts.go:73`, `ai_evidence.go:549-600`). Snapshot mode carries alert counts.
- Nothing records where the alerts came from. The turn's guard checks the pinned connection, the policy epoch, the AI revision, egress and comparison members (`chat_turn.go:196-225`), never the alert source.
- **So retained alert data is sent again after the endpoint, its match, its tenant or its credential changes,** until the conversation ends for another reason. Loki and metrics have the same gap (S3b's).

**What S3a starts from:**
- The strict decoder ignores an unknown top-level key with a warning, so `sources:` is ignored today (`internal/config/strict.go:132-133`).
- Slice F's plans (`internal/appbridge/plan.go`) and S2's fleet plans, with the hook S2 left: `FleetPlanDeps.Hosts`, nil, so no cluster hosts anything (S2 §4.4).
- The registry's coverage tests: every schema path needs an entry, and the scopes are a closed set (`internal/settings/registry_test.go:66-132`).

## 1. Stages

| Stage | Builds | Behaviour for today's files |
|---|---|---|
| **S3a-1: the model** | `sources:` and the subscription fields, strict decoding, resolution, validation and scopes (§2); pure, in `internal/config` and a new `internal/sources` | unchanged: a file without `sources:` resolves every cluster as today |
| **S3a-2: reading** | the hosted and direct transports, bucket readers, the split, what matched no member, caps, lineage on every read, the scope sentence; the host check in S2's removal (§3) | the legacy modes read as today, with the scope sentence and lineage added |
| **S3a-3: the fences** | lineage recorded by every AI read of alerts, checked before every component read and every later send (§4) | **a change to a legacy block's endpoint, match, tenant or credential now ends later sends of retained alert data** |
| **S3a-4: Settings** | Observability's installations and subscriptions, their plans and Apply, an installation's credential, Detect, and the migration offer (§5 to §8) | the legacy rows as today, with the offer where it applies |

Each stage is built, reviewed by Codex and accepted in turn, as S2a to S2c were.

## 2. The model (S3a-1)

### 2.1 The schema

**An installation:**

```yaml
sources:
  - id: lgtm-we                  # a DNS label: the keychain account, lineage and display use it
    kind: alertmanager           # alertmanager only in S3a
    name: Shared Alertmanager, West Europe   # display only
    host: aks-we-prd             # with serviceRef or discover: reached through this cluster's API server
    serviceRef: {namespace: monitoring, name: alertmanager-main, port: web, path: /alertmanager}
    # or discover: true          the existing discovery, with its proof of one installation, in the host
    # or endpoint: https://am.example.com/alertmanager   direct; no host
    tlsSkipVerify: false         # endpoint only
    auth: {type: bearer}         # endpoint only: the secret is in the keychain as source/lgtm-we
    tenant: ops                  # the default tenant of its subscriptions
    memberLabel: cluster         # the default scope: cluster = the member's name
    serves: [{group: we}, {cluster: aks-ne-dev}]
```

**A subscription,** under a cluster, when it differs from what an installation implies:

```yaml
clusters:
  - name: aks-we-stg
    alertmanager: {source: lgtm-we, tenant: stg}        # its own tenant, still filtered by cluster
  - name: aks-we-dev
    alertmanager: {source: lgtm-we, match: {cluster: dev-we}}   # the label's value differs from the name
  - name: aks-we-sandbox
    alertmanager: {off: true}                            # no alerts, said as such
```

**The fields, and what each refuses** (each refusal is a load error naming its place):
- `id`: a DNS label (`[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?`), unique among `sources`.
- `kind`: `alertmanager`. `loki` and `metrics` are refused as "built in S3b", so a file written for S3b fails loudly here rather than half-working.
- **The locator:** exactly one of `serviceRef`, `discover: true` or `endpoint`. `serviceRef` and `discover` need `host`; `endpoint` refuses it.
- `host`: a cluster of the fleet.
- `auth` and `tlsSkipVerify`: with `endpoint` only. Today's per-cluster block only warns that `auth` is not sent through a proxy; a new block refuses it.
- `tenant`, `memberLabel`: a header-safe value and a label name, as today's checks.
- `serves`: entries of exactly one key, `group` or `cluster`. A name or group that no cluster has is a warning, not an error: a cluster can be added later.
- A subscription block with `source` refuses `endpoint`, `serviceRef`, `auth` and `tlsSkipVerify`; it allows `tenant`, `match` and `scope`. `off: true` allows nothing beside it. `scope` is allowed only with `source`.

`config.EndpointConfig` gains `Source`, `Scope` and `Off`, so the strict decoder knows every key. `config.Config` gains `Sources []SourceConfig`. **`EndpointConfig` is Loki's shape too,** so `clusters[].loki.source`, `scope` and `off` become schema paths as well. Until S3b builds them, each is a load error naming the key ("loki.source is built in S3b"), never a source-less Loki, and the registry gives each the disposition deferred, with S3b as its slice.

### 2.2 Resolution

A cluster's Alertmanager is resolved in this order (redesign §6.2), and the first step that applies wins:

| Step | When | Resolves to | Mode |
|---|---|---|---|
| 1 | the cluster has an `alertmanager` key | its own block | see below |
| 2 | no key, and an installation serves it by name | a subscription with the installation's defaults | `subscribed` |
| 3 | no key, and an installation serves its group | the same | `subscribed` |
| 4 | none of these | discovery in the cluster | `discovered` (legacy) |
| 5 | discovery completes and finds none | none, said as such | `none` |

**Its own block** (step 1), each a deliberate mode (redesign §17a):

| The block | Mode | Reads |
|---|---|---|
| `{source: …}` | `subscribed` | through the installation, with the block's tenant, match and scope over its defaults |
| `{off: true}` | `off` | nothing; the lens says "off in fleet.yaml" |
| `{endpoint: …}` | `direct` (legacy) | as today, its tenant and match its scope; a `serviceRef` beside it is ignored with today's warning |
| `{serviceRef: …}` | `service` (legacy) | as today, through the cluster's own API server |
| `{tenant: …, match: …}`, no locator | `discovered` (legacy) | discovery in the cluster, with that scope, as today |
| `{}` | `discovered` (legacy) | discovery, unscoped, the capability present at once, as today |

**A cluster's own block always wins,** an empty one included: an installation serves only clusters with no `alertmanager` key. A group installation therefore never takes over a cluster that says anything for itself. When an installation would have served a cluster that has its own block, the cluster's row in Observability says so ("its own block shadows lgtm-we"). **A shadowed configuration creates no reader** (redesign §17a).

**Load errors** (redesign §6.2), each naming what it names, never falling through to discovery:
- a duplicate installation id;
- a `source:` naming no installation, or one of another kind;
- a host that is not a cluster of the fleet;
- a locator that is not exactly one of the three, or a host where one is not allowed;
- two installations serving one cluster at the same step: two by name, or two by its group. By name over by group is not a conflict: it is the precedence;
- an invalid scope (§2.3).

**Resolution is a pure function** of the effective configuration: `sources.Resolve(cfg) (Resolution, error)`. It is computed at load, on every Apply that changes a source, a subscription, a cluster's group, or the fleet's membership, and for every plan's candidate. Its result is a resolution per cluster: the mode, the installation, the effective tenant and match, and the host.

### 2.3 One effective scope per subscription

- **The effective tenant** is the subscription's, else the installation's, else none.
- **The effective match** starts from `{<memberLabel>: <cluster name>}` when the installation has a `memberLabel`. The subscription's `match` extends it: a key it names replaces that key's value, any other key adds an equality. The member label can never be removed: an empty value is refused by today's match check.
- **`scope: tenant`** means everything in the subscription's tenant is the member's. Its effective match is empty, and a `match` beside it is refused. It is allowed only when no other subscription of the installation has the same effective tenant.

**Every subscription has an explicit scope:** a non-empty effective match, or `scope: tenant`. An installation without a `memberLabel`, subscribed without a match or `scope: tenant`, is refused: "lgtm-we has no memberLabel: give aks-we-dev a match, or scope: tenant". Nothing reads "everything there" by default, which is today's legacy unscoped mode and stays only there.

**Shared tenants must be provably disjoint** (redesign §6.3). Subscriptions of one installation with the same effective tenant, none of them `scope: tenant`, must each carry the member label in their effective match, with distinct values. Otherwise it is a load error naming both subscriptions:
- a missing member label, when the installation has none;
- two equal values (`match: {cluster: dev}` on two clusters);
- a `scope: tenant` subscription sharing its tenant with any other.

Label attribution stays a configured correlation, never UID identity (`alertmanager/subject.go`). The scope sentence says "filtered by", never "owned by".

## 3. Reading (S3a-2)

### 3.1 Transports

| Installation | Transport | Credential |
|---|---|---|
| `host` + `serviceRef` | the API server's service proxy, through the host's **current** connection (`NewGuardedProxyREST` on the host's REST configuration) | the host's Kubernetes authentication; no external credential |
| `host` + `discover: true` | the same, to the Service the discovery proved in the host; the outcome is cached per host connection and installation revision for today's five minutes | the same |
| `endpoint` | `safehttp` direct, as today's direct block | `source/<id>`, bound to the endpoint's origin, with its own credential revision |

The legacy modes keep their transports: their own connection, or their own direct block.

### 3.2 Buckets

**A bucket** is an installation's resolved subscriptions with one effective tenant. Its key is the installation's id and revision, the host's access generation when it is hosted (§3.4), and the tenant. The credential revision is checked on every request, before and after, as today, and recorded with each read; it is not in the key. **Only resolved subscriptions are in a bucket:** a shadowed or `off` cluster adds nothing.

**A bucket reads on its own cadence,** with one owner, in `internal/fleet` beside the registry:
- A member's alerts snapshot registers with its bucket when its Observer reads it, and leaves when the Observer stops (a disconnect, a retirement, a re-key). The **cohort** is the registered members, each with its subscription revision.
- The bucket exists while its cohort is not empty. **In steady state it reads once per 30-second interval,** S8a's cadence, single flight.
- **A join asks for one early read.** A member joins when its Observer first reads the bucket, or reads it again under a new subscription revision. The early read starts at once unless one is already starting, and every join made before it starts shares it. The interval then restarts from it. Joins come only from a member connecting, reconnecting or re-keying, so the extra reads are bounded by those events, never by the members' cadences.
- A re-key makes a new bucket and cancels the old one: its worker ends within the read's timeout, and its outcome is never served.
- The read takes one of today's four fleet-wide slots. **Buckets add no slots** (redesign §17a). The read's own 10-second timeout covers its wait for a slot.

**The acquisition filter** is the same for `/api/v2/alerts` and `/api/v2/alerts/groups`, so mute reduction stays complete for every row read (redesign §17a):
- `scope: tenant`: no filter, whether or not the installation has a `memberLabel`;
- otherwise, an installation with a `memberLabel`: one anchored regular-expression matcher of the cohort's escaped member-label values (`cluster=~"aks-we-dev|aks-we-stg"`). Other match keys differ by member, so they are checked after the read;
- otherwise, an installation without one: its subscription in that tenant is alone (§2.3), so its full effective match is sent as equality filters, as today's direct block does.

**The split, and what is kept** (redesign §6.4):
1. The bucket's read is reduced once (`alertmanager.Reduce`) and capped once.
2. Each reduced row's labels are checked against each cohort member's full effective match. Disjoint scopes mean at most one matches.
3. The bucket keeps the read's **outcome**: its time, its cohort with each member's subscription revision, the credential revision, the lineage (§3.4), and each cohort member's share. A share is the member's matching rows, projected for display only after the split. Raw identities and matching facts stay in the bucket: members receive projected rows only.
4. **A row matching no cohort member's scope is counted on the installation** as "matched no member in this read", never added to a member, and never in any member's AI evidence. Rows of a cluster outside the cohort never reach the read: the filter leaves them out. The count is of rows this filtered read returned, never of every alert the installation holds.
5. Each read replaces the outcome. A failed read keeps the last good shares, said with the failure: the members become stale, as today's rule for a failed read.

**A member's read** is served the bucket's latest outcome at once when the outcome's cohort included that member under its current subscription revision. The share keeps the read's own time, credential revision and lineage, never a later one: observe's `SnapshotRead` gains the read's time, and the snapshot's as-of and staleness use it. A member not in the outcome's cohort, under its current revision, waits for the early read its join asked for, within its own 10-second timer; a timeout is an ordinary failed read. No other member read waits on the bucket. A share served from the outcome keeps the outcome's acquisition time and its last failure, so an overdue or failed bucket makes its members stale on their own clocks.

**Caps:** S8a's bounds apply to the bucket's read, once:
- 5,000 entries after reduction is a truncation. A truncated read qualifies **every** member of its cohort: "lower bound: lgtm-we's read reached its cap". A member's share is never presented as complete when its read was cut, and the unmatched count says "of a read cut at its cap".
- 8 MiB, per response as today, and 20,000 group appearances fail the bucket's read whole. Every member's alerts become stale or unreadable.

**Memory:**
- A bucket holds one outcome: at most one capped read, split. A member's Observer holds the share it was last served, which may come from an older outcome than the bucket's: members read on their own cadences, and a joining member's early read does not refresh the others.
- A member's share is at most the cap, as a cluster's own read is today. **The bound is 5,000 rows for each reading member, plus one capped outcome for each bucket,** which is today's per-cluster bound plus one outcome per bucket. In flight: at most four reads fleet-wide, each with two responses of at most 8 MiB and its reduction.
- These bounds are per bucket and per member, so no admission limit is needed. The load test (§9) runs a representative shape (eight installations of eight members, hot members rotating through joins, reads failing) and checks this bound, counting each row once however many holders share it. It does not claim to be the worst shape. No member's held evidence is dropped to meet it.

### 3.3 Coverage for a member

| The bucket's state | The member's alerts |
|---|---|
| read, the member's share | live, with the scope sentence |
| the host is not connected, or not usable | unreadable or stale: "lgtm-we's host aks-we-prd is not connected", never "no alerts" |
| the host's discovery found none | unreadable: "lgtm-we found no Alertmanager in aks-we-prd". A configured installation that resolves nothing is a failure, never "not installed" |
| the read failed | unreadable or stale, with the failure |
| `off` | absent: "off in fleet.yaml" |
| legacy modes | as today |

### 3.4 Keys and lineage

**Revisions** are counters from one process-wide sequence, held by a runtime `sources` registry, and never reused: a removed installation re-created under the same id gets new revisions. An installation's revision moves whenever its effective record changes; a subscription's moves whenever its member's resolution changes (a new installation, tenant, match, scope or mode). A change and its undo are two moves.

**The host's access generation** is what a hosted read pins. Its connection identity alone is not enough, since a connection recovers in place under the same identity (S2a). The generation moves, and never returns, when:
- the host's usability is published again: the usability incarnation S2a publishes into its policy epoch, which moves at a reconnect and at a recovery in place alike;
- a read through the host is refused by the API server or the proxied Service (401 or 403): an observed loss of access.

A direct installation's access generation moves on a 401 or 403 from its endpoint only. Its credential revision is a separate term, checked by the fences, never in the key (below). What a lineage certifies is that no change was observed since the read. Access between reads is not probed, and the scope sentence carries the read's time.

**The snapshot's key** for a subscribed member is its lineage: `source|<id>|r<installation revision>|s<subscription revision>|a<access generation>|tenant=…|match=…`. A changed installation, subscription or access generation therefore re-keys the member's snapshot: a new generation starts at checking, and nothing of the old one is shown as current. The legacy modes keep today's key. **A credential change re-keys no mode,** subscribed or legacy, as today: the lens keeps its rows and selection until the next read, which uses the new credential. The credential revision is in the lineage, where the fences check it: a conversation holding alerts read under the old credential ends at its next send, and the outcome keeps the revision it was read under.

**The opaque row identity** (`AlertKey`) is taken over this key, so it carries the installation, tenant and revision (redesign §17a). A revision change clears the lens's selection, as a source change does today.

**Every alerts read carries its lineage** (`alertmanager.Read.Lineage`): the mode, the installation's id and revision, the subscription's revision, the host's name, connection identity and access generation, the tenant, the effective match as display, the credential revision, and the read's time. It belongs to the read's outcome: its rows, stale ones included, an empty success and its counts alike. The alert rows and parts expose it. A lineage is never looked up again later: the read's own is the one checked.

### 3.5 The scope sentence

One sentence, from the lineage, in the Alerts lens header, the inspector's evidence block and Observability's row:
- subscribed and hosted: "from lgtm-we, hosted by aks-we-prd, tenant ops, filtered by cluster=aks-we-dev";
- subscribed and direct: "from lgtm-we at am.example.com, tenant stg, everything in tenant stg";
- legacy: "from the Service monitoring/alertmanager-main:web on this cluster, filtered by cluster=aks-we-dev", or "discovered in this cluster, monitoring/main, unfiltered";
- qualified when it applies: "lower bound: lgtm-we's read reached its cap", or "lgtm-we's host aks-we-prd is not connected".

v0.15.1's shared-Alertmanager hint stays for the legacy unscoped modes only. A subscription's scope is explicit, so the hint has nothing to add.

### 3.6 Hosts and the fleet

- **The active sources.** The runtime `sources` registry holds the installations and subscriptions in force, with a revision. Only Observability's plan changes it (§5): S2's plans never adopt a source change from the file. An S2 plan composes its candidate from the active sources and its own fleet change, binds the active sources' revision, and its Apply checks it again under the fleet Apply lock.
- **S2's removal check:** `FleetPlanDeps.Hosts` returns the installations naming the cluster as host: the active ones, and those in the file, a file-only or changed installation included. A removal of a host, including one through "Use the file's values", is refused before anything disconnects (S2 §4.4), so neither a running dependency nor one the person may still apply is left without its host. A plan removing an installation and its host together would name both; S3a builds none.
- **A host's reconnect, or its recovery in place,** moves its access generation, so its members re-key. S2's reconnect plan lists the members' alerts from the host's installations among what ends, by the connections they use.
- **A placement edit that changes a cluster's group** can change which installation serves it. S2's placement plan lists the change ("Alerts: from lgtm-we → discovered in this cluster"), and the commit re-resolves and re-keys under the fleet Apply lock.
- **A cluster added or removed** changes its installations' members: the commit re-resolves, so the bucket filters follow.

## 4. The fences (S3a-3)

### 4.1 The host is transport only

The host relationship grants nothing else (redesign §6.6):
- no reads of the host's objects for the member's conversation;
- the host is never added to a conversation's comparison set;
- a model never chooses or widens it.

A member's conversation reads the member's share, through the member's snapshot. Rows the installation holds for other members or tenants never reach it.

### 4.2 Lineage recorded by every AI read

- `get_alerts` records its read's lineage as a bounded token: identities and counters, never data, in a field never sent to the model.
- Explain records it on the call.
- Snapshot mode and attention carry alert counts, so they record the alerts part's lineage too.
- **A conversation keeps every token it has recorded,** cumulatively. Trimming its oldest exchanges drops their reads but keeps their tokens, since a later send still carries what those reads informed.

### 4.3 The checks

**Before and after every component request** of a bucket's read, both of its two: the host's pinned connection is still its current connection and usable, its access generation is the one the read started under, and the installation's and subscription's revisions are unchanged. A direct transport checks its access generation and credential revision instead (redesign §17a). A mismatch discards the read, as a credential change does today; an observed 401 or 403 moves the access generation first.

**Before every later send of retained data,** the turn's guard (`check` in `chat_turn.go`) compares each of the conversation's tokens with the current terms. It runs inside the existing fence, so it is checked at every model request the AI transport makes (retries and redirects included) and at the Claude Code CLI's release of its input, in tools mode, snapshot mode and Explain alike, and later in A2. Each token checks:
- the installation and the subscription are unchanged;
- for a hosted lineage, the host's pinned connection is still its current connection, that connection is usable now, and its access generation is unchanged. All three: a host that has failed and not yet recovered keeps its generation, and a replacement has a new identity before it publishes any usability;
- for a direct lineage, its access generation and credential revision are unchanged;
- for every lineage, the installation's and subscription's revisions, on hosted and direct paths alike;
- for a legacy lineage, the source identity and the credential revision are unchanged;
- the subject member's own connection, as today.

A mismatch ends the conversation, as a reconnect does: "lgtm-we changed since its alerts were read: start a new conversation". Recovery is new work under the new lineage. A request already sent cannot be recalled. Canary tests show that a token never enters an outgoing request.

**A3's offered plans** (ruling 2, §12): the lineage check joins the fences that withdraw an investigation's unrun steps (`planFenceLocked`). A plan offered by a conversation whose tokens no longer hold is withdrawn for good, when its state is next read or before its next send, whichever comes first. An alert alone never justifies a step, which needs a citation of its target by UID. But an alert can be cited beside one, so a plan may rest partly on alerts; Review and Run keep their target-fact and consequence checks.

**The member's consent** belongs to S5 and A2. S3a checks the member's connection only, as today.

### 4.4 What this changes for today's files

A legacy block's lineage is its source identity and credential revision. A change to its endpoint, match, tenant or credential, through Test, a credential action or a restart, now ends later sends of the alerts a conversation read before the change. That closes today's gap (§0) for Alertmanager. Loki and metrics close it in S3b.

## 5. Plans and Apply (S3a-4)

**Observability's plan** follows slice F: planned from what runs, confirmed by its digest, applied in order, and refused when the file, a revision or the active sources moved in between. It covers:
- an installation added, edited or removed;
- a cluster's subscription block written, changed or removed, `off` included;
- the file's sources and subscription blocks as they are, when they differ from the active ones ("Use the file's values").

Legacy blocks stay file-only, except through the migration offer (§7).

**The plan says, for each change:**
- the effective changes, with their inputs;
- the members whose resolution changes, each with its scope sentence before and after;
- what ends: each member's alerts snapshot re-keys, and conversations holding alerts of the old lineage end at their next send;
- what an installation's removal does to the clusters it served: back to discovery, or to another installation's group, said for each.

There is no interlock: alerts are read-only, and none of redesign §1.5's dangers applies.

**Two candidates.** A plan has a **disk candidate**: the file's bytes with the plan's edits, which must load strictly and validate, and which is written. It has a **runtime candidate**: the running fleet (S2 §2.6), the active sources (§3.6), and exactly the terms this plan approves. Only the runtime candidate is resolved and published. Whatever else differs in the file (a hand-edited group, context or protection, another cluster's subscription block) is neither adopted nor published: those stay S2's, or the next Observability plan's, to apply. A file-only cluster is validated for a later Connect, and never becomes a reader. The plan binds the running fleet's revision and the active sources' revision, as well as the file version, and Apply checks all three again.

**"Use the file's values"** for sources adopts the `sources` block and the clusters' subscription blocks (`source` and `off`) as the file says them, and nothing else: never S2's placement, context or protection, and never a legacy block, which stays C5's to adopt.

**Apply is one transaction,** under the fleet Apply lock, so it is serialized with S2's plans:
1. **Build:** every edit of the plan (the `sources` block, each cluster's `alertmanager` block) is applied in memory to the reviewed file's bytes, by the existing edit builders (the block replacement, S2's entry edits), into the disk candidate.
2. **Validate both candidates:** the disk candidate loads, resolves, and changes only the intended blocks; the runtime candidate resolves. A refusal here writes nothing.
3. **Write once:** one compare-and-swap of the disk candidate against the reviewed file version, with the write policy: a symlink kept, the mode kept, the lock.
4. **Publish:** the active sources take the runtime candidate's resolution, computed in step 2; the changed revisions move; the affected members' snapshots re-key, and the bucket readers follow. Publication is in memory, from a resolution already computed, so it cannot fail after the write. A process that dies after the write leaves the written file, which the next start loads.

An edit that cannot be built (an unsupported YAML shape in one block, or a hand edit since the review) refuses the whole plan before anything is written.

**An installation's credential** (`source/<id>`) is an immediate action on the active installation, never adopting its file block, as the cluster endpoints' are: Store, Remove and Test under the endpoint lock, then the fleet Apply lock. Its revision is the installation's credential revision. A credential is never copied from a cluster's record.

## 6. Detect (S3a-4)

Detect, per group or per cluster, proposes. It never proves (redesign §6.7):
1. In each member, Klyx runs the existing discovery, with its proof of one installation.
2. For each installation found, it reads one bounded sample: `/api/v2/alerts`, capped at 1 MiB and 500 alerts. Receiving and parsing that response is reading alert bodies. The redesign's §6.7 is amended to allow it for this one sample, which the person asks for (§12, ruling 3). The parser keeps label names, and the values of labels whose values equal a fleet cluster's name; it keeps no decoded alert. Annotations and every other value are never kept, shown or sent. What the sample could not show, or showed ambiguously, is said. Loki lines are never read for this.
3. It proposes an installation for each host it found one in:
   - the host and the proven Service;
   - a member label: a label whose values in the sample equal members' names;
   - the members seen in the sample.

   "Seen in this sample" never means a member that is missing has no alerts: an Alertmanager with nothing firing for a member shows nothing for it.
4. It never infers that two clusters share an installation from equal Service names. An installation proven in each of two clusters is two proposals.
5. The proposal shows its sample's time, its bounds, and what was ambiguous or could not be read. The person reviews and edits it; saving it is an ordinary plan (§5).

Detect runs only when the person asks, one group or cluster at a time, and through the fleet-wide alert slots.

## 7. Credentials and the migration offer (S3a-4)

**Migration:**
- Today's per-cluster blocks keep their behaviour unchanged. Nothing is migrated on its own.
- Settings offers **"Make a shared installation"** when it sees one direct URL, with the same origin and path, on several clusters, or `sharedWithoutMatch`'s warning. The offer is a proposal: an installation with that endpoint and each cluster's subscription, its existing tenant and match carried over as the subscription's. The person reviews it, edits it (the member label above all) and applies it as an ordinary plan.
- Equal URLs are never merged automatically. Equal Service names on different clusters are not offered at all: they are different installations.
- The proposal validates the result's disjoint scopes, and says each cluster's scope before and after, broadened or narrowed. Two unscoped blocks on one URL and tenant cannot become valid subscriptions as they are: the proposal says what the person must choose, a member label or the matches.
- Different `auth` or `tlsSkipVerify` on equal URLs is a conflict the proposal names, with no default winner.
- **Secrets are never copied between accounts.** The installation's credential is stored once, by the person. Until it is, the plan says so, and the installation's reads fail as "no credential stored for lgtm-we". The clusters' old records stay until the person removes them.

## 8. The page (S3a-4)

**Observability's sentence** names, for the open cluster or the fleet, where alerts come from: "Alerts: 9 clusters from 2 installations, 3 discovered in themselves, 1 off".

**Installations,** a table: id, where (host and Service, or the endpoint's origin), tenant, member label, serves, credential state, not attributed in the last read, and Test. Each opens an editor, with Review and apply.

**Subscriptions,** per cluster: the mode and how it resolved (its own block, by name, by group, discovered, none, off), the scope sentence, and what shadows what. A subscribed cluster's row edits its subscription block; a legacy row is shown as today, with the migration offer where it applies.

**Detect,** per group or cluster, opens its proposal in the plan panel.

**The page keeps S2c's discipline** (S2 §26): a change to a draft withdraws its plan, a changed draft is a draft to the leave guard, nothing is called current before the readback, and drafts are kept across live refreshes of scope and credential state.

**The registry** gains every new path, so the coverage tests hold:
- `sources[].id`, `kind`, `name`, `host`, `serviceRef.*`, `discover`, `endpoint`, `tlsSkipVerify`, `auth.type`, `auth.username`, `tenant`, `memberLabel`, `serves[].group`, `serves[].cluster`;
- `clusters[].alertmanager.source`, `scope`, `off`;
- the new scopes `installation` and `subscription`.

Their dispositions: editable through Observability's plan, taking effect on Apply. The legacy `clusters[].alertmanager.*` fields stay file-only.

## 9. Tests

**Every stage:** its own tests, the registry's two coverage tests, strict decoding for every new key, the write policy for every new writer, and slice 0's probes.

**The model (S3a-1):**
- every load error of §2.2 and §2.3, each naming its place;
- resolution: every row of §2.2's two tables, an empty block shadowing a group installation, by name over by group, `off`;
- scopes: an override replacing the member label's value, an added key, `scope: tenant` alone and shared, the redesign's R3 cases (wrong or missing labels, an empty override, overlapping matches, duplicate values, escaped values);
- `loki.source`, `scope` and `off` refused until S3b, and every new key's disposition, by endpoint kind.

**Reading (S3a-2):**
- R2: one URL with different tenants (two buckets); a tenant-isolated member without a cluster label (`scope: tenant`);
- one read per bucket per interval for staggered members; joins sharing one early read, which restarts the interval; a joining member waiting for that read, then served; a served share keeping its acquisition time and failure; a member under a changed subscription revision is never served an older outcome; a re-keyed bucket's outcome is never served; the fleet-wide limit of four held with buckets;
- the split: rows to their members; unmatched rows counted, and never in a member's evidence; a cluster outside the cohort left out by the filter; a lone subscription without a member label sending its full match;
- R3: a hot member filling its bucket's cap qualifies every member;
- the 8 MiB and appearances failures fail every member's read;
- the host disconnected: "not connected", never "no alerts"; the host's discovery finding none: unreadable, never absent;
- re-keys: an installation, a subscription, a host's reconnect, its recovery in place, and an observed 401 or 403 each start a new generation; a revision never reused after an id is removed and re-created;
- S2 with sources: the host check over active and file installations; an active source removed from the file, a file-only source added, a group edit over source drift, and a source Apply between an S2 plan and its Apply;
- the load test at §3.2's representative shape, hot members rotating through joins and failed reads, with the retained rows, each counted once, checked against §3.2's bound;
- the lab: a real Alertmanager in the kind lab, read through a host, serving members by label, with an alert that passes the acquisition filter and matches no member's full scope.

**The fences (S3a-3):**
- R2: a scope change ends a retained read's later sends;
- R4: the host disconnecting or losing access between a read and a send, and a host that has failed and not yet recovered; a failed component read; retained history; an installation changing; recovery after a fence ended the old work;
- a legacy block's endpoint, match, tenant or credential changing ends later sends (today's gap);
- a credential stored or removed for a subscribed direct installation: the rows and selection kept, the conversation's next send refused, the outcome keeping its revision;
- the host is never added to a comparison set, and a member's conversation never sees another member's rows or the unmatched rows;
- every model request attempt (retries and redirects included) and the CLI's release, checked; canaries showing no token in an outgoing request; tokens kept past trimming;
- A3: an offered plan withdrawn, for good, when its conversation's lineage fails.

**Settings (S3a-4):**
- plans: each change's members, sentences and endings; a refusal on a moved file, revision or active sources; Apply's order;
- two candidates: an Observability edit over an unapplied group or membership drift, an unrelated subscription hand edit, and an S2 commit between a source plan and its Apply, none adopted; a file-only cluster validated, never a reader;
- one transaction: a migration across several clusters, and an installation's removal with its subscribers, each written whole; a block that cannot be built, last, writing nothing;
- an installation's credential: stored, removed, and refused for another origin;
- Detect: the sample's bounds, annotations never kept, two clusters with equal Service names proposed apart, a missing member not said to have no alerts;
- the migration offer: proposed, never applied on its own, no secret copied; a scope broadened or narrowed, said; unscoped same-tenant blocks and differing auth or TLS named as choices;
- the page: S2c's discipline, a refusal, and an unknown outcome after a failed reread; fixtures for long names and URLs, 200% zoom, a narrow window, keyboard and focus, night and day.

**The native smoke** gains a walk: an installation hosted by the lab cluster, serving it by group with an extra match key (`team: platform`); its scope sentence in the Alerts lens; and a planted alert with the lab's cluster label and `team: other`, which passes the acquisition filter and matches no member, counted as unmatched in the read. The walk restores the smoke fleet.

## 10. Not in S3a

- Loki and metrics: S3b, on this model.
- Consent and the proactive runs: S5 and A2.
- Federated reads across tenants (redesign §17).
- Renaming a cluster or an installation id: an id is the credential's account and the lineage's name.

## 11. For Codex's ruling

1. **A cluster's own block always wins, an empty one included** (§2.2). The alternative, letting a group installation take over an empty or scope-only block, would change what today's files read.
2. **A subscription change is fenced by lineage, not by the policy epoch** (§3.4, §4.3). Redesign §4 says the epoch moves when a source subscription feeding the cluster changes. Moving it would also withdraw unrelated arms and offers. The lineage fence ends exactly the work that used the old source, including A3's offered plans that rest partly on alerts (§4.3). I propose that refinement of §4.
3. **Detect reads the bounded response of `/api/v2/alerts`** (§6), which carries annotations; the parser keeps label names and member-like values only, and never keeps, shows or sends anything else. That amends redesign §6.7's "never reads alert bodies".
4. **Every subscription states its scope** (§2.3): an installation without a member label needs a match or `scope: tenant`. The unscoped "everything there" stays a legacy mode only.
5. **A configured installation that resolves nothing is unreadable, never absent** (§3.3).
6. **The host's connection identity is part of the lineage key** (§3.4), so a host's reconnect re-keys all of its members.

## 12. Revision 1: Codex's review (2026-10-27)

Codex found the inventory (§0) faithful to the code, with one size qualification, now in §3.2: the 8 MiB limit is per response. It ruled on §11 and requested changes: five P2 and two P3.

| Finding | Change |
|---|---|
| **R1, P2:** Apply's separate writers could leave some blocks written when a later one refused, and an installation's removal could not be written before its subscribers | §5: Apply is one transaction. Every edit is built in memory into one candidate, validated whole, written by one compare-and-swap, then published from the resolution already computed. A block that cannot be built refuses the whole plan before anything is written |
| **R2, P2:** a connection identity and present usability do not fence a host that failed and recovered in place, or an observed loss of access | §3.4: the host's access generation, moved by every publication of its usability (a recovery in place included) and by an observed 401 or 403, never returning. It is pinned in the key and the lineage, and checked before and after every component request and before every later send, at each model request and at the CLI's release |
| **R3, P2:** cached coalescing contradicted dropping the shares of members that had not asked, and the memory bound covered one split only | §3.2: a bucket reads on its own cadence for its cohort, keeps one outcome with every cohort member's share, serves a member only under its current subscription revision, and keeps each read's time and lineage. The memory bound counts the outcome and the members' held shares: two capped reads per bucket, so never more than today's per-member bound plus one outcome per bucket. The load test is representative, not a worst case |
| **R4, P2:** a subscription without a member label, alone in its tenant with a match, had no acquisition filter | §3.2: it sends its full effective match, as today's direct block |
| **R5, P2:** S2's candidate and host check had no defined source state | §3.6: the active sources, with a revision. S2 composes its candidate from them, binds and re-checks the revision, and never adopts a source hand edit. The host check covers active and file installations |
| **R6, P3:** the shared shape added `clusters[].loki.source`, `scope` and `off` | §2.1: each is a load error until S3b, with a deferred disposition in the registry |
| **R7, P3:** the union filter left out the smoke's planted alert for another member, so it could never be counted | §3.2, §9: the count is of rows the filtered read returned; the smoke plants an alert that passes the filter and fails the member's extra match key |

**The rulings on §11:**
1. Approved: a cluster's own block always wins, an empty one included, and a shadowed installation creates no reader.
2. Approved as a deliberate refinement of redesign §4, with its behaviour stated (§4.3's paragraph on A3) and its justification narrowed: an alert can be cited beside a UID read.
3. Approved as an explicit amendment of redesign §6.7: §6 now says Detect reads the response, and what it never keeps, shows or sends.
4. Approved, with R4's filter.
5. Approved.
6. Approved, and insufficient alone: R2's access generation.

**Also adopted from the review:**
- revisions never reused across an id's re-creation (§3.4);
- a re-keyed bucket cancelled within its read's timeout (§3.2);
- cumulative tokens that survive trimming, with canaries (§4.2, §4.3);
- a lineage belonging to the read's outcome, never looked up later (§3.4);
- unmatched rows never in another member's evidence (§3.2);
- the migration's scope, auth and TLS conflicts (§7);
- S2c's page discipline (§8).

## 13. Revision 2: Codex's re-review (2026-10-27)

Codex found R1 (one file transaction), R4, R6 and R7 resolved, and R2, R3 and R5 resolved in part. The rulings stand. It requested changes: three P2 and one P3.

| Finding | Change |
|---|---|
| **RR1, P2:** comparing the access generation alone dropped the host's current-connection and usability checks: a host that failed and had not recovered kept its generation | §4.3: a hosted check requires the pinned connection to be the host's current one, usable now, and at the same access generation, before and after each component request and before each later send. The generation is kept, never returning |
| **RR2, P2:** publishing the file candidate's resolution could adopt unrelated drift: a hand-edited group switching a member's installation | §5: a disk candidate, validated and written, and a runtime candidate (the running fleet, the active sources and exactly the plan's terms), which alone is resolved and published. The plan binds the running fleet's and the active sources' revisions. "Use the file's values" adopts only the sources and subscription blocks |
| **RR3, P2:** an immediate read at every join broke the cadence, and the two-outcome memory claim did not hold | §3.2: steady state is one read per interval; a join asks for one early read, shared by every join before it starts, which restarts the interval. The bound is 5,000 rows per reading member plus one outcome per bucket, which no member's evidence is dropped to meet |
| **RR4, P3:** a direct credential change both did and did not re-key | §3.4: a credential change re-keys no mode; a direct access generation moves on an observed 401 or 403 only; the credential revision is a fence term in the lineage |

`scope: tenant` now takes precedence over a member label in the acquisition filter (§3.2), and §11's proposal wording matches the rulings.

**Acceptance requirements from the re-review,** for the stages that build them:
- **Lifecycle:** a hosted bucket's reads are work on the host, taken through S2's admission (a lease on the host) before any request, never by a registry lookup alone. The members' dependent work is registered, so the host's plans list and end it. A bucket is cancelled by the retired connection's identity, and a late outcome is refused. S2's lock order is kept, with no lifecycle callback re-entered under a gate, and S2's reservation refuses new bucket work while keeping admitted work until its commit.
- **Single flight:** cancelling an obsolete bucket does not prove its worker has returned. The owner hands over across re-keys, and no acquisition overlaps a previous worker that still runs, nor releases its slot before then. The four slots, a timeout that includes the wait and discovery, and bounded cancellation stay.
- **Member freshness:** a served share keeps its acquisition time and its last failure or overdue state. A member leaves its bucket when its snapshot generation stops or re-keys. `Observer.SetConnection` and `stopSnap` are separate operations, so a connection becoming unreachable does not leave the bucket on its own.
- **Fences:** installation and subscription terms are checked on hosted and direct paths alike. Empty, stale and count-only outcomes and derived evidence carry lineage. Dependencies are kept past trimming, and every API attempt and every CLI input release runs the same check. A3's Review and Run already call `withdrawFencedLocked`, so the lineage term in `planFenceLocked` enforces the sticky withdrawal.
- **Read isolation:** alerts and groups take the same filter; the full match is checked before projection; cap and unmatched counts are qualified; and nothing of the host's objects, another member's rows or unmatched evidence enters a member's AI context.
