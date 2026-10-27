# Klyx Settings S3a: Alertmanager installations and subscriptions

Opened 2026-10-27, on `feat/settings-s3a`, after S2b was accepted (S2 §24) and with S2c in review. S3a is the redesign's first observability slice (redesign §6, §14, §17a). It builds, for Alertmanager:
- installations and subscriptions: the schema, resolution and validation, and one effective scope per subscription;
- reading in buckets, with the split and what matched no member;
- the scope sentence;
- the host fence on reads and on later sends;
- Detect, and the migration offer.

Loki and metrics follow on the same model in S3b. This note is for Codex's review before anything is built; revisions 1 and 2 the same day, after that review and its re-review (§12, §13); the design is accepted (§14).

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

## 14. ACCEPT (2026-10-27)

Codex accepted the design at revision 2 (b28c0a4), after the review of cce38ba and two re-reviews, with no remaining findings. It ruled RR1 to RR4 resolved, the original R1 to R7 resolutions intact, and §11's wording in line with its rulings. It found no new conflict with the redesign, S2 or the code cited: the join exception and Detect's bounded sample are deliberate refinements of the redesign, with their limits stated.

§13's acceptance requirements stand for the stages that build them, with one more from the acceptance: **a member that joins while a read is in flight is not in that read's cohort.** It is never served that read as its current share: it waits for the coalesced follow-up within its own deadline, or its read fails as any other.

The historical memory description in §12 records revision 1. §3.2 and §13 supersede it: the load test uses the bound per reading member plus one outcome per bucket.

S3a-1, the model, builds next.

## 15. S3a-1 as built: the model (2026-10-27)

S3a-1 is §2: the schema, its strict decoding and validation, resolution, and the effective scopes. It was built on S2c's revision 1. The design's commits were rebased onto it unchanged, so its accepted text is the same under new hashes.

| Contract | Code |
|---|---|
| The schema (§2.1) | `config.SourceConfig` and `ServeConfig` under `Config.Sources`; `EndpointConfig` gains `Source`, `Scope` and `Off`. The strict decoder knows every key, so an unknown key under `sources` is a load error, and `sources` is no longer an ignored top-level key |
| The installation's refusals (§2.1) | `validateSources`: the id a DNS label, unique; `kind` alertmanager (loki and metrics "not built yet"); exactly one locator; `host` with a serviceRef or discovery, never with an endpoint, and a cluster of the fleet; auth and TLS with an endpoint only; an endpoint URL with a credential refused, never echoed; serviceRef, auth and tenant by today's checks; the member label a label name; one key per `serves` entry |
| The subscription's refusals (§2.1, §12 R6) | `validateSubscription`: `off` alone; `source` with no locator, auth or TLS beside it; `scope` only with `source`, only `tenant`, with no match beside it. On Loki, `source`, `scope` and `off` are each a load error naming the key |
| Resolution (§2.2) | `config.ResolveAlertmanager`, pure, run at load: a cluster's own block always wins, an empty one included; then an installation by name, then by group, then discovery. Each own block is its mode (subscribed, off, direct, service, discovered). A block that wins over an installation records it as shadowed. Two installations at one step, or a source naming none, is a load error |
| Scopes (§2.3) | The effective tenant, and the match from the member label, extended by the block's match; `scope: tenant` takes the tenant. A subscription with neither is refused. Subscriptions sharing an installation's tenant must carry the member label with distinct values, and `scope: tenant` shares with none |
| Warnings | A `serves` entry naming a cluster or group the fleet does not have |
| The registry (§8, §12 R6) | Every `sources[]` path, scope `installation`; `clusters[].alertmanager.source`, `scope`, `off`, scope `subscription`; all file-only in this stage. Loki's three are deferred: "not built yet". Observability describes the installation fields under the endpoint fields |

**What a connection does in this stage.** Reading through an installation is S3a-2. Until then, nothing an installation serves is ever read by discovery in its member:
- `fleet.SubscribingFactory` gives each connection its block resolved against the installations read at start: its own block, or the subscription an installation implies for it. The running fleet's entry is not changed.
- `off` reads as absent ("alerts are off for this cluster in fleet.yaml"), with no Alerting capability.
- A subscription is unreadable, said as such: "this cluster's alerts come from the installation lgtm-we, and this version of Klyx does not read through installations yet". It keeps the Alerting capability.
- Their identities are `off` and `source|<id>`, so adopting another block re-keys, and `sameEndpoint` tells the blocks apart.
- Observability's row says "subscribed to the installation …" or "off in fleet.yaml", never "discovered".

**S2's plans** (§3.6, in this stage's form):
- **The host check:** `FleetPlanDeps.Hosts` is `SourceHosts`: the installations in force and those in fleet.yaml now, each once. A removal of their host is refused.
- **A placement or protection edit** that would move a cluster's alerts (a group an installation serves, or no longer serves) is refused, naming both sources: the running connection cannot follow until S3a-2 re-resolves on commit. Reconnects and additions build their connection through the resolving factory, so they follow.

**Tests:**
- every load error of §2.1 to §2.3, by place;
- §2.2's two tables, by name over by group, shadowing, both conflicts;
- the scopes: an override, an added key, `scope: tenant`, a lone subscription without a member label taking its match, and the disjointness refusals;
- warnings and strict decoding;
- the connection: off absent without discovery, a subscription never discovered, with a control that sees discovery counted; the factory's block, never written back;
- S2: a placement moving the source refused, its own block winning, the host check over active and file installations;
- the row's text, and the registry's coverage.

**Mutation pass:** 26 mutants over the validation, resolution and scopes, the connection, the factory, S2's checks and the row text, all caught.

**Smoke:** 216/216 four ways at a8824ff, the smoke fleet.yaml as it began after every walk. The smoke fleet has no installations, so this is the regression check: nothing changes for a file without `sources`.

## 16. S3a-1 revision 1: Codex's review (2026-10-27)

Codex found the scopes, the tenant and member-label overrides, the disjointness checks, the stage boundary for valid subscriptions, the factory's copy, the S2 refusal and `SourceHosts` as specified. It accepted keeping the pure model in `internal/config`. It requested changes: two P2 and two P3.

| Finding | Fix |
|---|---|
| **R1, P2:** validation read decoded values, so `alertmanager: {source: ""}` loaded and became legacy discovery, and Loki's `source`, `scope` and `off` loaded at their zero values | An endpoint block records the keys it wrote (`UnmarshalYAML`), and the subscription fields are judged as written. A `source` written empty, an `off` written false or a `scope` written empty is refused. So is any key beside `off`, or a locator beside `source`, even at its zero value. On Loki the three keys are refused whatever their value. The legacy `{}` block still loads |
| **R2, P2:** the new diagnostics echoed file values (hosts, source references, tenants, member-label values, serves entries) whole, outside slice 0's redaction | `describeValue` names a value as `describeKey` names a key: quoted, or by its length alone at 25 characters or more, which may be a pasted credential. Every new diagnostic and warning uses it |
| **R3, P3:** Observability's row called off and subscribed blocks unscoped | Only a legacy reader that filters nothing is unscoped |
| **R4, P3:** the registry said an installation's serviceRef port defaulted to the Service's first port, but the loader requires it | The port is required, as the loader says |

Codex also asked that one rule be stated, which this note does: **two installations at one step are a load error even when another step wins.** Two group installations for one group are refused even for a cluster served by name, or with its own block. The conflict is in the file whichever cluster it reaches, and §2.2 lists it without limiting it to the step that applies.

**Tests:** Codex's probes, as regression tests: every key written at its zero value, by kind; a 41-character canary withheld from every new error and warning; the row's scope by mode. **Mutation pass:** 6 mutants over the fixes, all caught.

**Smoke:** 216/216 four ways at 4580148, the smoke fleet.yaml as it began after every walk: every endpoint block now decodes through `UnmarshalYAML`.

## 17. S3a-1 revision 2: Codex's re-review (2026-10-27)

Codex found R3 and R4 closed, R1 fixed for keys written in the block itself, and R2 fixed for its five cases. It requested changes: two P2 and one P3.

| Finding | Fix |
|---|---|
| **RR1, P2:** keys inherited through a YAML merge (`<<: *defaults`) were not recorded as written, so an inherited `source: ""` loaded and became discovery again, and inherited Loki keys and empty siblings loaded | A block's written keys are its effective keys: a merge key's mapping, an alias to one, a sequence of them and nested merges are followed, each node once. A key written anywhere in the merge is written. Valid merges and aliases still load |
| **RR2, P2:** identities and label names still reached diagnostics whole: installation ids in the location and in warnings and scope guidance, duplicate ids, the member-label name, and cluster and group names in conflicts | Every identity in the new diagnostics is named as a value is: quoted, or by its length alone at 25 characters or more. A label name is named as a key. An installation's location is `sources.<id>` only when its id is short enough to show, else `sources[<index>]` |
| **RR3, P3:** the registry said `off` was true or false, but the loader refuses an explicit false | "true, or left out; nothing else in the block" |

**Tests:** Codex's probes, as regression tests: seven inherited forms, plus a merge sequence and a nested merge, all refused, with valid merges and aliases still loading; six long identities and label names withheld; the registry's text for `off`. **Mutation pass:** 6 mutants over the fixes, all caught.

**Smoke:** 216/216 four ways at 85cf6e8, the smoke fleet.yaml as it began after every walk.

## 18. S3a-1 revision 3: Codex's second re-review (2026-10-27)

Codex closed RR1 (merged keys) and RR3 (the registry's text for `off`), and RR2 in the loader's diagnostics. It requested changes: one P2.

| Finding | Fix |
|---|---|
| **R2-1, P2:** the same ids reached Settings through S2's plan refusals: a host's removal named its installations whole ("dev hosts <id>: remove those first"), and a source move named "the installation <id>" | A plan's refusal names an installation by its id, or, at 25 characters or more, as "an installation whose id has 40 characters": the loader's rule (`config.Withheld`). Where a cluster's alerts come from is compared by the id itself (`amSourceKey`), never by those words, so two distinct ids of one length still differ. `SourceHosts` keeps the ids whole for its dependency check |

**Tests:** Codex's probes, as regression tests: an active host and a host in the file, a source move, and two long ids of one length still refusing the move. **Mutation pass:** 5 mutants over the fix, all caught.

**Smoke:** 216/216 four ways at 2f9324e, the smoke fleet.yaml as it began after every walk.

## 19. ACCEPT: S3a-1 (2026-10-27)

Codex accepted S3a-1 at revision 3 (c5b63e5), with no remaining findings. That came after the review of 4647275 (two P2, two P3) and three re-reviews (§16-§18).

Its last re-review reran every probe from the review and the re-reviews under the race detector, including the fleet discovery probe, the three long-id refusals and two distinct ids of one length. It accepts the model and its stage boundary: reading, the buckets, the lineage fences and the Settings transaction for sources are S3a-2 to S3a-4.

## 20. S3a-2 as built: reading (2026-10-27)

S3a-2 is §3: reading an installation in buckets, the split, the lineage on every read, the scope sentence, and S2's commits re-resolving subscriptions. It was built on S3a-1's revision 1 and rebased onto the accepted revision 3.

| Contract | Code |
|---|---|
| The active sources (§3.4, §3.6) | `fleet.AlertSources`: the installations in force and every cluster's subscription, resolved against the running fleet. Revisions come from one process-wide sequence and are never reused: an unchanged installation or subscription keeps its revision, a changed one moves, and an id removed and re-created gets a new one. The set's own revision moves with any change |
| The host's access generation (§3.4) | `ClusterConn.publishedUsability`: the usability incarnation as published (one still pending publication is not in force) and whether it is usable now, with the installation's observed 401s and 403s (`AlertSources.Denied`). A direct installation's generation is its denials alone |
| The snapshot key (§3.4) | `source|<id>|r<installation>|s<subscription>|a<access>|tenant=…|match=…`. A host's reconnect, its recovery in place, an observed denial, or a change to the installation or the subscription re-keys the member at its next read (`ErrSourceChanged`); a credential change re-keys nothing |
| Buckets (§3.2) | `fleet.alertBuckets`, one per registry: a bucket per installation, revision, access generation and tenant; one owner reading on its cadence (30 seconds), early when a member asks for a read the latest outcome did not cover; joins that come before the read starts share it. One fleet-wide slot per read; buckets add none. A per-installation gate: a re-keyed bucket's read never overlaps the old worker. The last member to leave cancels the bucket, whose outcome is never served |
| The member's read (§3.2) | `ClusterConn.readSubscribed`: served the latest outcome at once when its cohort covered the member under its subscription revision, else it waits within its own timer for the read its asking started. A member joining while a read is in flight is not in that read's cohort. A served share keeps the read's acquisition time (`ReadAlertsAt`, observe's `ReadKeyedAt` and `SnapshotRead.ReadAt`), and a share already overdue lands stale |
| The transports (§3.1) | Hosted: a lease on the host through S2's lifecycle (`AlertHostLeaser`, work kind `alerts`), its service proxy (`amProxyREST`), the serviceRef, or the host's discovery cached five minutes per host connection and installation revision. Direct: `source/<id>` from the keychain (`endpointcred.SourceAccount`), bound to its origin, with its own credential revision |
| The read (§3.2) | Alerts and groups with one filter: none for `scope: tenant`; the member label's cohort values as one anchored regular expression; a lone subscription's full match. Checked before and after each request: the installation in force at the bucket's revision, the host connected and usable, the access generation unchanged. An observed 401 or 403 moves the access generation |
| The split (§3.2) | Reduced and capped once; each row to the cohort member whose full scope it matches; a row matching none counted on the installation (`Registry.InstallationRead`), never added to a member. A cut read qualifies every member |
| Coverage (§3.3) | The host not connected: "lgtm-we's host prd is not connected"; discovery finding none: "lgtm-we found no Alertmanager in prd", unreadable, never absent |
| Lineage (§3.4) | `alertmanager.Read.Lineage`: mode, identity, installation and its revision, subscription revision, host, host connection, access generation, tenant, credential revision. Legacy reads carry their mode, identity, tenant and credential revision |
| The scope sentence (§3.5) | Subscribed: "from lgtm-we, hosted by prd, tenant ops, filtered by cluster=dev"; direct "from shared at am.example.com, …"; legacy "from the Service ns/name:port on this cluster, …", or "unfiltered: everything there is read as this cluster's". In the Alerts lens header and the inspector's alerts block |
| S2 (§3.6) | S3a-1's refusal is replaced: a plan checks its runtime candidate (the running fleet with its change) resolves against the active sources, binds their revision, and lists a moved source ("Alerts: discovery in the cluster → the installation lgtm-we"), comparing sources by the installation id itself and naming a long id by its length alone, as S3a-1's refusals do (§18). Every commit puts the running fleet's subscriptions in force (`Registry.Resubscribe`): a connection whose block changes adopts it. The host check reads the active sources |

**Leaving a bucket:** a member leaves when its snapshot re-keys, its connection stops, or its block stops being a subscription.

**Tests:**
- buckets: one read split among members, the steady state reading nothing new, a join during a read waiting for its own, the host not connected, a recovery in place re-keying, a denial moving the access generation, a capped read qualifying every member, a direct installation's own credential and tenant, the filters, the last member cancelling, a changed subscription never served the older outcome, a read whose terms move discarded, an unsubscribed cluster leaving, a re-keyed bucket never overlapping the old read, a pending incarnation not in force, revisions;
- the memory bound at a representative shape: four members made hot in turn through joins, then a failed read, every held row counted once: 20,000 retained against a bound of 25,000;
- observe: a shared read keeping its time, an overdue one landing stale;
- S2: a moved source listed and resubscribed, a change the installations cannot resolve refused; the host lease admitted, refused while reserved, ended at the host's retirement;
- the legacy sentence and lineage; the inspector's sentence.

**The lab:** `TestInstallationLab` (`KLYX_S3A_LAB_CONTEXT=kind-klyx`, read-only, refusing any other context and checking the lab's label) reads the S8a lab's real Alertmanager as an installation hosted by kind-klyx, serving it with `match: {namespace: klyx-s8-lab}`. Its share held the nine namespaced alerts; the three cluster-wide alerts passed the filter and matched no member (unmatched 3); the other cluster's alert never reached the read.

**Mutation pass:** 26 mutants, 20 caught at first. Four survivors got tests and are caught: a read whose access generation or installation moves in flight, an unchanged subscription keeping its revision, and a changed installation moving its. Two are equivalent:
- the immediate overdue check on a landed share: the Observer's loop marks an overdue read stale before it publishes, so no summary shows it live either way. It stays, as a defence;
- the explicit leave on a re-key: the member's next read joins its new bucket, which leaves the old one.

Looking at the second found a real gap, now fixed: a cluster whose block stops being a subscription never joins again, so it now leaves its bucket when its block changes.

**A gate run found a test fault:** the registry test of the running fleet's subscriptions started its connections without clients, and under load a metadata informer could run before the test ended and crash the package. It now builds them with fake clients and stops them. Twelve runs of the package under load pass.

**Smoke:** 216/216 four ways at 56498c0, the smoke fleet.yaml as it began after every walk.

## 21. S3a-2 revision 1: Codex's review (2026-10-27)

Codex found the filter, the split, the shared reduction and cap, the direct credentials, the fleet-wide slot, the acquisition time and the S2 plan path as specified. It requested changes: six P2.

| Finding | Fix |
|---|---|
| **R1:** the read checked the installation, the host and the access generation, never the cohort's subscriptions, so a member whose match changed during a read was served the share read under its old scope | Each request is checked against every cohort member's subscription revision too, before and after. A member whose subscription moved is dropped from the read: it is not in the outcome's cohort and gets no rows, which match no member. Before serving a share, a member's read checks its subscription is still the one in force; one that moved re-keys (`ErrSourceChanged`) and reads under the new one |
| **R2:** the per-installation gate was a mutex taken before the read's timeout began, which no cancellation could end | The gate is a one-slot channel, taken inside the read's budget: a read waiting for its turn times out with the rest of the read, and a cancelled bucket stops waiting. The worker holding the gate keeps it, and its fleet-wide slot, until it returns |
| **R3:** a stopped connection's late read could join again and recreate its bucket, which then outlived the connection | A connection's stop retires it from the buckets for good, in one step under the buckets' lock: a retired connection joins nothing, and a read whose context has ended registers nothing. Connection identities are never reused, so a retirement never touches a newer connection |
| **R4:** every pass of a member's wait asked for an early read, so one join could cause two | A bucket records an early read asked for and not yet started, and the cohort of the read in flight. A member asks only when neither covers it; a read that starts spends any early read asked for meanwhile |
| **R5:** adding clusters (`AddClusters`) neither checked the running fleet against the installations in force nor put their subscriptions in force | The running fleet with the additions is resolved against the installations in force before anything is written; a refusal writes nothing. After the additions install, the running fleet's subscriptions are put in force, as a fleet Apply does: every cluster that joined, none that failed to |
| **R6:** the host lease covered one read at a time, so an idle bucket depended on nothing: the host's plans could not name it and its retirement did not end it | A hosted bucket holds a lease on its host's connection for its whole life (work kind `alerts`, "the alerts lgtm-we reads for its members"), besides each read's own admission. The host's plans list it, and its retirement ends the bucket: its members leave it and re-key at their next read. A host that cannot be leased is tried again before each read; a lease on a connection other than the one the bucket is keyed to ends the bucket at once |

Codex also noted that the memory test's failure phase never waited for a failed read: it now waits for each bucket's failed outcome, which keeps the last good shares, before counting.

**Tests:** Codex's probes, as regression tests: a subscription moved during a read (re-keyed, never served, the read giving it nothing) beside an unchanged one; the gate's wait bounded and cancellable; a stopped connection's late read and a later one joining nothing; one join causing one follow-up; an idle bucket's host lease, named by the host's plan and ending the bucket at its retirement; a bucket keyed to an old host connection; additions putting their subscriptions in force, one failing to install, one failing to prepare, and a refusal before anything is written. **Mutation pass:** 16 mutants over the fixes: 10 caught at first, two did not build and were rewritten, and four survivors got tests (a read whose context ended on a connection still running, an early read spent by a read that started first, a host leased only at a later read, and the moved member's absence asserted on the read itself). All 16 are caught.

**Smoke:** 216/216 four ways at 18eb817, the smoke fleet.yaml as it began after every walk.

## 22. S3a-2 revision 2: Codex's re-review (2026-10-27)

Codex closed R1 to R6, and found the memory test's failure phase now waits for the failed read. It requested changes: two P2.

| Finding | Fix |
|---|---|
| **RR1:** a member's read served a held share while its host was known to be down: the access generation carries the host's usability incarnation, not whether it is usable now, so it did not move until the host recovered | Before serving, a member's read checks its installation's host is connected and usable; a host known to be down fails the read with its reason ("lgtm-we's host prd is not connected"), with no request, so the Observer keeps what it had as stale (§3.3). A usable host's held share is reused as before |
| **RR2:** the read in flight recorded its members by connection only, and ended after its waiters woke: a member whose subscription changed within its bucket could wait for no read until the cadence | The read in flight records each member at the subscription revision it covers, so a new revision asks for its own read; and the read ends in the same step as its outcome is published, so a waiter it wakes never sees it still in flight |

**Tests:** Codex's probes, as regression tests: a known-down host serving no held share, beside a usable one whose share is reused; a new revision within its bucket keeping its follow-up across the end of the old read. **Mutation pass:** 4 mutants, 2 caught at first; one did not build and was rewritten, and is caught. RR2's two fixes each close the gap alone, so removing either survives; removing both is caught.

**Smoke:** 216/216 four ways at 214fafe, the smoke fleet.yaml as it began after every walk.

## 23. ACCEPT: S3a-2 (2026-10-27)

Codex accepted S3a-2 at revision 2 (70ffbee), with no remaining findings. That came after the review of fb8aff7 (six P2) and two re-reviews (§21-§22).

Its last re-review reran every probe from the review and the re-reviews five times under the race detector, and agreed with §22 that RR2's two fixes each close the gap alone. It accepts the reading stage; the send fences are S3a-3, which must carry each read's own lineage through its rows, empty and count-only outcomes, stale evidence and what is derived from them.

## 24. S3a-3 as built: the fences (2026-10-27)

S3a-3 is §4: every AI read of alerts records the terms it was read under, and every later send of what it informed checks they still hold. It was built on S3a-2 and rebased onto its accepted revision 2.

| Contract | Code |
|---|---|
| The token (§4.2) | `alertmanager.Lineage`, carried on the alerts rows (`healthsrc.AlertRows.Lineage`) from the read that made them. It holds identities and counters, never data, and is never sent: no tool result, snapshot or Explain document carries it |
| The check (§4.3) | `fleet.Registry.LineageProblem`, stored once at startup (`appbridge.SetAlertLineageCheck`), as the protect guard is. A subscribed lineage: the installation in force at its revision, the cluster's subscription at its revision and to the same installation, and, hosted, the host's current connection still the one the read pinned, usable now, at the same access generation; direct, the same access generation and the `source/<id>` credential revision. A legacy lineage: the member's source identity and its credential revision. Off and a read without a source hold nothing. "" while the terms hold, else which moved: "lgtm-we's host prd was reconnected or removed since its alerts were read" |
| Recording in a conversation (§4.2) | `get_alerts` records its rows' lineage, an empty read included. `cluster_overview` and `list_attention` show an alerts part's counts from one capture of its rows (`HealthRowsNow`), recording that capture's lineage, so a count is never shown without the terms it was read under; a part whose rows cannot be read keeps no counts and says why. The Claude Code CLI's snapshot runs the same tools, so it records too. Stale rows keep the lineage of the read that made them (the Observer retains that read's envelope), so a hosted installation whose host has gone down refuses them with its reason; an empty share carries its read's lineage too. A conversation keeps every lineage it recorded (`conversation.lineages`), deduplicated; removing or trimming an exchange keeps them |
| The send (§4.3) | The turn's guard (`turnRunner.guard`) checks each lineage after its existing fences. It is the conversation's read fence (`readfence.With(ctx, r.check)`), so it runs at every model request the transport makes, retries and redirects included, and before the CLI's release of its snapshot. A lineage that no longer holds ends the conversation, as a reconnect does: "lgtm-we changed since its alerts were read; start a new conversation" |
| A3 (§4.3, ruling 2) | `planFenceLocked` checks the lineages with the other fences: a plan offered by a conversation whose lineage moved is withdrawn, for good |
| The host is transport only (§4.1) | Nothing in S3a reads the host's objects for a member's conversation or adds the host to a comparison set: the set is the operator's (`SetComparison` alone sets it). A member's rows are its share of the split (S3a-2's tests); the unmatched rows are counted on the installation, never sent |
| Explain (§4.2) | `withLineages` wraps the call's fence: the alerts evidence records its lineage on the call, and the fence checks it at every later read and at the send. Suggest-fix reads no alerts and is unchanged |

**Two refinements found while building:**
- **Rows already stale are refused, not recorded.** A read whose lineage no longer holds when it is made (the alerts snapshot is re-read on its 30-second cadence, so its rows can predate a change for that long) is refused: `get_alerts` fails with the reason and "Klyx reads them again within 30s", the summary tools leave the counts out, and Explain leaves the alerts out. Nothing from those rows is used, so the conversation is not ended over them; recording them would end a new conversation at its first send.
- **A direct installation's outcome is served only under the credential it was read with.** After a credential is stored, replaced or removed, a member's next read asks for an early read under the new credential, keeping its key (§3.4: a credential change re-keys nothing). Before this, a new conversation could be served the outcome read under the old credential, whose lineage no longer held, until the bucket's next read.

**Tests:**
- fleet: a hosted lineage holding while its terms do, and ending at the host's reconnect, its failure (not yet recovered), its recovery in place, its removal, an observed denial, a changed installation, a changed subscription and a removed installation; a direct lineage following its credential (stored, revoked) and its denials, the outcome keeping its revision and not served again; a legacy lineage ending at an endpoint, match, tenant or credential change (§4.4: today's gap closed); lineages without terms;
- appbridge: an alerts read ending the conversation once its terms move, with nothing more sent and the token in no request or state; recovery as a new conversation; stale rows refused and not recorded, the conversation going on; a removed exchange's lineage still ending the conversation; the summary tools' counts from the captured rows with their lineage, left out when the terms moved or the rows are unreadable; the CLI snapshot recording and its next release refused; an offered plan withdrawn for good; Explain refused when the lineage moves before the send, its alerts left out when already moved, the token never sent.

**Mutation pass:** 25 mutants over the checks, the recording and the refusals: 23 caught. Two are equivalent, the lineage check's installation revision and installation id: a subscription's revision moves whenever its installation's revision moves or it names another installation, and revisions come from one process-wide sequence, so the subscription's revision check fails first. Both stay, as §4.3 lists them.

**Smoke:** 216/216 four ways at 1bd4484, the smoke fleet.yaml as it began after every walk.

**Gates:** one existing observe test, `TestErrorThenRecoveryWithoutObjectChangesAdvancesVer`, timed out once in a plain run while other suites loaded the machine; it passed in the same gate's race run and five times alone. S3a-3 does not touch observe.

## 25. ACCEPT: S3a-3 (2026-10-27)

Codex accepted S3a-3 at its first review (418d53b), with no findings. It checked each read's own lineage through its rows, empty and stale outcomes and the summary counts, the cumulative tokens, the sticky guard and the A3 withdrawal. Its probes ran the real OpenAI-compatible provider over an in-memory transport: a moved lineage blocks a redirected request in a chat and in Explain, and the conversation stays ended.

It ruled both refinements of §24 acceptable: rows whose terms have already moved are refused before they are recorded (rows that are only stale, under terms that still hold, stay usable as qualified history), and a direct installation's outcome is served only under its current credential. It agreed the installation's revision and id checks are equivalent behind the subscription's revision. S3a-4, the Settings page, is next.

## 26. S3a-4a as built: plans, Apply and an installation's credential (2026-10-27)

S3a-4 is built in three parts, each reviewed in turn, as S2b and S2c were: **S3a-4a**, the backend of §5 (Observability's plans and Apply, an installation's credential, the registry's dispositions); **S3a-4b**, Detect (§6) and the migration offer (§7); **S3a-4c**, the page (§8) and the native smoke's walk. The split changes no ruling of the design.

| Contract | Code |
|---|---|
| The disk candidate (§5, step 1) | `config.BuildSourcesCandidate`: the fleet file's bytes with the plan's edits, in memory. The sources block by block replacement (`renderSources`: each installation's fields in a fixed order, a field at its zero value left out); each cluster's alertmanager block by an entry edit (`editEntryAlertmanager`), replaced, added after the entry's content, or removed, every other line as written. Settings writes subscription blocks only (an installation with its scope, tenant and match, or off); a legacy block stays the file's. A block on the list dash's line, a flow-style entry, an alias, an anchor or a merge key is a `HandEditError`. The candidate must load strictly, which validates the sources and resolves every cluster, and decoded, change only the plan's blocks (`onlySourcesChanged`) |
| The runtime candidate (§5) | The running fleet (S2 §2.6) with the installations in force and exactly the terms the plan approves: one installation added, edited or removed, and the running clusters' blocks the plan sets. It is checked by `config.CheckSources`: the sources valid, each block valid, every cluster resolved, the scopes disjoint. Only it is published |
| A plan (§5) | `SourcesService.PlanSources` (a draft: one installation's change, and the clusters whose own block changes) and `PlanSourcesFromFile`. It says each installation's change in words, each block's change, each running member whose scope sentence changes (before and after), what ends (each member whose installation or subscription changes re-keys its alerts; the AI work holding alerts read there, `ChatService.AlertWork`), and binds the file version, the running fleet's revision, the active sources' revision and the work's version. An edit of an installation the file no longer has is refused ("use the file's values first, or add it again"); an installation edited in the file and not in force is named as replaced. A cluster in the file and not running is validated for its later connect, and never becomes a reader |
| "Use the file's values" (§5) | The file's sources block, and for each running cluster its subscription or off block where the file's differs (a block the file removed is removed); never a legacy block, which stays C5's, and never S2's placement, context or protection. It writes nothing |
| Apply (§5) | `ApplySources`, one transaction under the fleet Apply lock: re-planned from what runs, refused with the new plan when anything moved; the disk candidate written once (`config.EditSourcesAt`: one compare-and-swap against the planned version, through slice 0's write policy); then the running clusters take the approved blocks (`SetRunningEndpoint`) and the installations and subscriptions are put in force (`Registry.PublishSources`: the changed revisions move, and each connection whose block changes adopts it and re-keys). A plan whose block cannot be built refuses whole, before anything is written |
| An installation's credential (§5) | `EndpointsService.SetSourceCredential`, `RemoveSourceCredential` and `TestSource`, on the installation in force at the revision shown, under the endpoint lock then the fleet Apply lock. The secret is stored once, under `source/<id>`, bound to the endpoint's origin, and the installation's credential revision moves; a hosted installation takes none. Remove takes it out of use at once, then from the keychain. Test reads the installation's status once through the transport its reads take (`Registry.InstallationStatus`, sharing `alertBuckets.transport` with the bucket reads), in a fleet-wide slot, and moves no bucket's outcome; a host known to be down is said with no request. A cluster's record is never copied |
| The state (§8) | `SourcesService.GetSources`: Observability's sentence ("Alerts: 9 clusters from 2 installations, 3 discovered in themselves, 1 off"), each installation (where it is, its credential's state, its subscribers, its latest read and what matched no member), each running cluster's row (mode, how it resolved, the scope sentence, what its own block shadows, its block, or that its block is a legacy one), and whether the file says otherwise |
| The registry (§8) | Every `sources[]` path and the Alertmanager subscription keys are editable, taking effect on Review and apply; Loki's stay deferred to S3b; a new `credential.source` entry. The legacy `clusters[].alertmanager.*` fields stay file-only |

**Tests:**
- the writer: both blocks written and every other line kept; a replacement, an off block and a removal; a block added after the entry's content, CRLF kept; refusals (a block on the dash's line, a legacy block, an unknown installation or host, an unknown cluster) writing nothing; only the planned version;
- plans and Apply: an installation and a subscription added, with the installation's words, the block, each member's sentence before and after, and the endings; a refusal when the file, the running fleet or the installations in force moved, writing and publishing nothing; an unapplied group change and another cluster's block hand edit, neither adopted nor published, kept in the disk candidate; a file-only cluster validated, not read; an installation's removal with its subscriber's block as one transaction, a removal leaving a block that names it refused, a block that cannot be built refusing the whole plan; "Use the file's values" adopting the sources and a subscription, never a legacy block or S2's group; the state;
- an installation's credential: a stale revision and a hosted installation refused; stored under its own account and bound to its origin, its revision moving; a credential for another origin not usable; removed from use and from the keychain; Test available, unavailable, and an installation not in force;
- the registry: a status read once through the host's Service proxy, no alerts read, a down host not asked; `PublishSources` moving an installation's revision and a connection adopting its off block.

**Mutation pass:** 21 mutants: 16 caught at first; two did not build and were rewritten, and two survivors got tests (an edit of an installation the file no longer has, and no change line for a legacy block in the file). All are caught but one, skipping the disk candidate's decoded check, which no candidate the writers build reaches: it stays as a defence, and a direct test shows it refuses another field, another cluster's block, another installation field and a missing block.

**Smoke:** 216/216 four ways at 1e92273, the smoke fleet.yaml as it began after every walk, on the third run. The first lost one step in one walk, Fleet's reconnect of the lab cluster, which never connected within its ten minutes; the second ran on a lab whose rebuild had failed, the kind API server timing out its TLS handshakes. The lab's API server answered normally before the third, run from a clean rebuild. S3a-4a adds no installation to the smoke fleet and does not touch the reconnect.

## 27. S3a-4a revision 1: Codex's review (2026-10-27)

Codex found the two candidates, the strict disk validation, the shared compare-and-swap writer, the separate credential account and the split of S3a-4 sound. It requested changes: six P2 and one P3.

| Finding | Fix |
|---|---|
| **R1:** "Use the file's values" writes nothing, so its Apply never checked the file's version again: a file edited after the re-plan read it was adopted as it had been | Before anything is put in force, a plan that writes nothing checks the file is still the version planned, as S2's no-write plans do; a moved file publishes nothing and returns the new plan |
| **R2:** a credential action checked the installation's revision before taking the fleet Apply lock, so an Apply in between let a stale Store or Remove change the new installation's record | The installation, its revision, its origin and its auth type are resolved inside the fleet Apply lock, the endpoint lock still taken first; a stale action changes neither the record nor its revision |
| **R3:** Test dropped the revision reviewed, and its read had no fence: it could test a newer installation, or send to one already replaced | `InstallationStatus` takes the revision reviewed, refuses another, and fences its request as a bucket's read is, before and after: the installation at that revision, its host connected and usable, its access generation, its credential's revision |
| **R4:** a plan's words dropped the endpoint's path, the Service's scheme and path, the basic username and the display name, so different changes read the same | An edited installation's plan says each material input that changed in a line of its own, what it was and what it becomes: name, host, Service (scheme, namespace, name, port, path), discovery, endpoint (origin and path; a query named, never shown), certificate check, auth (type and username), tenant, member label, serves. An added or removed installation says them all |
| **R5:** publishing re-keyed only connections whose block changed, so a member whose installation changed under the same block kept its old collection until its next read | Publishing re-keys every member whose installation or subscription moved, under the same block too; an unchanged publication and a credential change re-key nothing |
| **R6:** the plan's work omitted a running Explain holding the member's alerts, and a conversation could read the old alerts after the Apply's final work list and before its publication | Each Explain fenced by its lineages is named, while it runs, among the plan's work (`explain:<n>`). Recording an alerts lineage takes a shared lock that an Apply holds from its final work list to its publication: a read made meanwhile records its lineage after the publication, and is refused if the terms it was read under moved, so no work holding the old terms starts unnamed; a refused plan leaves every running work valid |
| **R7, P3:** installations only reordered in the file were offered as a difference, which "Use the file's values" then refused | Installations are compared by id, in any order, for the state and for the plan |

**Tests:** Codex's probes, as regression tests: a stale Store and a stale Remove, moved at the lock, changing nothing; a late file edit refusing the adoption; each changed input said (endpoint path, Service scheme and path, username, name, member label); a running Explain named and refused at its send; a conversation reading during an Apply refused and not recorded, and going on; reordered installations no difference; a member re-keyed under an unchanged block, and an unchanged publication re-keying nothing; a Test fenced by its revision. Codex's probe for a conversation started during the Apply ran the conversation inside the plan's work callback, which the Apply now holds; the regression runs it alongside, as a conversation runs. **Mutation pass:** 14 mutants over the fixes: 12 caught at first; two did not build and were rewritten, and are caught.

**Smoke:** 216/216 four ways at eac06c0, the smoke fleet.yaml as it began after every walk.

## 28. S3a-4a revision 2: Codex's re-review (2026-10-27)

Codex closed R1, R2, R3, R5, R6 and R7, and accepted the concurrent regression that replaced its synchronous R6 probe. One P2 remained in R4.

| Finding | Fix |
|---|---|
| **RR1:** a plan decided a field had changed by comparing its words, and an endpoint's words reduce any query to "(with a query)": an edit of the query alone was refused as changing nothing, and the same edit beside a new name applied with only the name in the confirmation | A plan decides each changed field from its inputs as written, never from their words, and names what the words cannot show, its contents still withheld: an endpoint's changed query ("its query changed, and is not shown"), its fragment (which the words never show) or both, and that only the writing changed (an origin's case or default port: "written differently, to the same origin and path"; a Service scheme written out as its default: "written differently, with the same effect"). A changed query is named beside a changed path too; a query added or removed is already in the words |

**Tests:** Codex's two cases, a query-only edit planned with an endpoint line and a query edited beside the name naming both, and Apply publishing each; the query beside the path, the fragment alone, the query and fragment together, only the writing, a query added, and a Service scheme written out; no withheld part in any line. **Mutation pass:** 12 mutants over the fix, all caught.

**Smoke:** the native walk at 0056fe7a, 216/216 four ways (night and day, 1440 and 1024) on the kind lab, the smoke fleet as it began after each.

## 29. S3a-4a revision 3: Codex's second re-review (2026-10-27)

Codex closed RR1, and R1 to R7 stayed closed. One P2 remained, of RR1's kind in another field.

| Finding | Fix |
|---|---|
| **R2-1:** what an installation serves was still compared through its words, which joined selectors with commas and prefixed a group with "group ": a group "blue, group green" read the same as the groups blue and green, so a selector-only edit was refused as changing nothing, and beside a name it applied with only the name in the confirmation. The fallback could also call colliding words "written differently, with the same effect" | What an installation serves is compared as typed selectors (a group or a cluster, and its value), in any order: order alone still changes nothing. A value that could read as the words around it is quoted, in serves and in a Service's namespace, name, port and path (`group "blue, group green"`, `http "a/b"/c:web`), so two different inputs never read the same. "Written differently, with the same effect" is decided from the inputs, for a Service's scheme written out as its default only; any other field whose words could not show its change says "changed in a way these words cannot show", never that nothing changed |

**Tests:** Codex's two cases, the selectors alone and beside the name, each applying with its serves line; order alone refused as changing nothing, and order beside the name naming only the name; a group replaced by a cluster of the same name; a Service whose parts read alike; and the fallback decided from the inputs. **Mutation pass:** 12 mutants over the fix: 9 caught at first; two survived until the order-beside-the-name and group-for-a-cluster cases were added, one of them rewritten to drop both selector types (it had dropped one), and are caught; one is equivalent: with the quoting, the words of what an installation serves are themselves unambiguous and sorted, so comparing them is the same comparison.

**Smoke:** the native walk at db970710, 216/216 four ways on the kind lab, the smoke fleet as it began after each.

## 30. ACCEPT: S3a-4a (2026-10-27)

Codex accepted S3a-4a at 65cb220a, after its review and three re-reviews (§26 to §29), with one non-blocking P3.

**R3-1, P3:** a plan compared what an installation serves, its Service and its auth as strings joined with NUL and SOH, which the loader allows in a value: one group holding those bytes compared equal to two groups, so a selector-only edit was refused, and beside a name applied unnamed. The words already quoted such a value; the inputs did not. Fixed at 6ee26a6a: those inputs are encoded as JSON, whose boundaries no value can forge, what an installation serves still sorted by kind and value. Tests: Codex's probe, the selectors alone and beside the name, each applying; a Service and an auth whose values hold the old delimiters compare as different. Mutation: 4 mutants (each encoding back to a joined string, and serves unsorted), all caught. §29's equivalent mutant held only for values without those bytes; with the inputs encoded, comparing the words and comparing the inputs agree. The follow-up is reviewed with S3a-4b.

## 31. S3a-4b as built: Detect and the migration offer (2026-10-27)

S3a-4b is §6 and §7, on S3a-4a's plans (accepted, §30): both only propose, and a proposal is saved as an ordinary plan.

| Contract | Code |
|---|---|
| Detect in one cluster (§6, steps 1-2) | `Registry.DetectAlertmanager`: under a lease on the cluster (work kind `alerts`, whose retirement ends it) and in a fleet-wide slot, the existing discovery with its proof of one installation; for a proven one, one sample, `GET /api/v2/alerts` through the cluster's own Service proxy, capped at 1 MiB (a larger response is said unread) and 500 alerts (cut, and said). `sampleLabels` decodes each alert's labels alone: it keeps the label names, and a label's value only when it equals a fleet cluster's name; annotations are never decoded, and no alert is kept |
| A proposal (§6, step 3) | `SourcesService.Detect`, over a group or one running cluster, one member at a time: a proposal for each cluster an installation is proven in, hosted by it, at the Service discovery proved; a member label when exactly one label's values named fleet clusters, else the candidates and a choice; the members seen; the sample's time and bounds; what could not be read; and that a member not seen may simply have nothing firing. Its draft adds the installation (an id from the host, free among those in force) serving the members seen, or its host when no member label was found |
| Never inferred (§6, step 4) | An installation proven in each of two clusters is two proposals, whatever their Services' names |
| The migration offer (§7) | `SourcesService.MigrationOffers`, from the running fleet's legacy blocks: one offer for each direct URL (the origin canonical, the path as written, as the shared-endpoint warning groups them) on several clusters. Its draft adds the installation at that URL, with the auth, certificate check and tenant the clusters share, and subscribes each cluster with its match, and its tenant where theirs differ. Auth or a certificate check that differs is a conflict, with no default winner. Each cluster's scope before and after, broadened, narrowed or the same; a result that is not valid (subscriptions sharing a tenant with no member label to prove them apart, or a block with no scope) is a choice to make, a member label or the matches. A Service shared by name is never offered. The installation's credential is stored once, by the person; no record is copied, and the clusters' own stay |

**Tests:**
- Detect's sample: label names and the values naming fleet clusters kept, an annotation, another value and a canary never; the 500-alert bound; a response that is not a list; one sample through the proven Service's proxy, a response over 1 MiB said unread, a cluster not running, and one with no Alertmanager;
- proposals: one cluster, with its member label, members seen, bounds, the note on members not seen, and its draft (an id free of one in force); a group's two clusters as two proposals, an ambiguous label and a cut sample; a sample not read; an ambiguous discovery proposing nothing;
- the migration offer: a URL on one cluster never offered; proposed and never applied on its own; auth that differs named; an unscoped block making a choice, its scope before said; then a match and a tenant each, valid with no member label and the same in scope, applied as an ordinary plan, the cluster's own record kept and none copied; a Service shared by name never offered; one tenant shared with no member label named as the choice, which a member label in the draft settles.

**Mutation pass:** 15 mutants: 13 caught at first; a survivor got a stricter test (a sample over 1 MiB said as such, not as a failed read), and is caught. One is equivalent: dropping the offer's check that a block has a URL, since a Service's block has none to parse and is passed over there.

**Smoke:** the native walk at a2e535ce, 216/216 four ways on the kind lab, the smoke fleet as it began after each. Detect's sample and the offer have no step of their own in the walk: Detect reads a lab Alertmanager the person points it at, and the smoke fleet has no direct URL on several clusters; S3a-4c's page walk covers Settings' plans.
