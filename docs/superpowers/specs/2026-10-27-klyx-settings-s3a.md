# Klyx Settings S3a: Alertmanager installations and subscriptions

Opened 2026-10-27, on `feat/settings-s3a`, after S2b was accepted (S2 §24) and with S2c in review. S3a is the redesign's first observability slice (redesign §6, §14, §17a). It builds, for Alertmanager:
- installations and subscriptions: the schema, resolution and validation, and one effective scope per subscription;
- reading in buckets, with the split and what matched no member;
- the scope sentence;
- the host fence on reads and on later sends;
- Detect, and the migration offer.

Loki and metrics follow on the same model in S3b. This note is for Codex's review before anything is built.

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

`config.EndpointConfig` gains `Source`, `Scope` and `Off`, so the strict decoder knows every key. `config.Config` gains `Sources []SourceConfig`.

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

**A bucket** is an installation's resolved subscriptions with one effective tenant. Its key is the installation's id and revision, the host's connection identity when it is hosted, and the tenant. The credential revision is checked on every read, before and after, as today; it is not in the key. **Only resolved subscriptions are in a bucket:** a shadowed or `off` cluster adds nothing.

**One reader per bucket** lives in `internal/fleet`, beside the registry, and serves its members:
- A member's alerts snapshot still reads on its own Observer's cadence. Its read asks the bucket for its share.
- A request is served by the bucket's latest read when that read started within the cadence and was filtered for the asking member. Otherwise the request starts a read, and requests made meanwhile coalesce into it. Members on staggered cadences therefore share one read per bucket per interval, single flight across re-keys.
- **A share's freshness is its bucket read's time,** never the time the member asked: observe's `SnapshotRead` gains the read's time, and the snapshot's as-of and staleness use it.
- The bucket read takes one of today's four fleet-wide slots. **Buckets add no slots** (redesign §17a). The wait for the slot and for the bucket's read count against the asking member's 10-second timer, as today.
- The read asks for `/api/v2/alerts` and `/api/v2/alerts/groups` with one filter: the member label matching the bucket's member values, as one anchored regular-expression matcher of the escaped values (`cluster=~"aks-we-dev|aks-we-stg"`). Other match keys are not sent, because they differ by member; they are checked after the read. A `scope: tenant` bucket sends no filter.
- **Alerts and groups take the same filter,** so mute reduction stays complete for every row read (redesign §17a).

**The split** (redesign §6.4):
1. The bucket's read is reduced once (`alertmanager.Reduce`) and capped once.
2. Each reduced row's labels are checked against each member's full effective match. Disjoint scopes mean at most one matches.
3. A member's share is its matching rows, projected for display only after the split. Identity and matching facts are split before any member row is projected.
4. **A row matching no member is counted on the installation as not attributed,** never added to a member. Rows of a member that is not reading now (not connected, or not asked yet) are dropped without being counted as not attributed.

**Caps:** S8a's bounds apply to the bucket's read, once:
- 5,000 entries after reduction is a truncation. A truncated bucket qualifies **every** member in it: "lower bound: lgtm-we's read reached its cap". A member's share is never presented as complete when its bucket was cut.
- 8 MiB and 20,000 group appearances fail the bucket's read whole. Every member's alerts become stale or unreadable, as today's rules for a failed read.
- The not-attributed count is of rows actually read, with "of a read cut at its cap" when it was.

**Memory:** a bucket keeps its last read only until it is split; the members' shares are disjoint, so the sum of the shares is at most the bucket's read. No member holds a copy of another's rows. A load test measures the worst admitted shape: eight installations, each serving eight members, each read at the cap (§9).

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

**Revisions** are counters held by a runtime `sources` registry. An installation's revision moves whenever its effective record changes; a subscription's moves whenever its member's resolution changes (a new installation, tenant, match, scope or mode). A change and its undo are two moves, never a return to an old revision.

**The snapshot's key** for a subscribed member is its lineage: `source|<id>|r<installation revision>|s<subscription revision>|host=<host connection identity>|tenant=…|match=…`. A changed installation, subscription or host connection therefore re-keys the member's snapshot: a new generation starts at checking, and nothing of the old one is shown as current. The legacy modes keep today's key. A credential change does not re-key any mode, as today: the lens keeps its rows until the next read, and the credential revision is in the lineage (below), where the fences check it.

**The opaque row identity** (`AlertKey`) is taken over this key, so it carries the installation, tenant and revision (redesign §17a). A revision change clears the lens's selection, as a source change does today.

**Every alerts read carries its lineage** (`alertmanager.Read.Lineage`): the mode, the installation's id and revision, the subscription's revision, the host's name and connection identity, the tenant, the effective match as display, and the credential revision. It is kept with the snapshot's items, and the alert rows expose it.

### 3.5 The scope sentence

One sentence, from the lineage, in the Alerts lens header, the inspector's evidence block and Observability's row:
- subscribed and hosted: "from lgtm-we, hosted by aks-we-prd, tenant ops, filtered by cluster=aks-we-dev";
- subscribed and direct: "from lgtm-we at am.example.com, tenant stg, everything in tenant stg";
- legacy: "from the Service monitoring/alertmanager-main:web on this cluster, filtered by cluster=aks-we-dev", or "discovered in this cluster, monitoring/main, unfiltered";
- qualified when it applies: "lower bound: lgtm-we's read reached its cap", or "lgtm-we's host aks-we-prd is not connected".

v0.15.1's shared-Alertmanager hint stays for the legacy unscoped modes only. A subscription's scope is explicit, so the hint has nothing to add.

### 3.6 Hosts and the fleet

- **S2's removal check:** `FleetPlanDeps.Hosts` returns the installations naming the cluster as host, from the candidate configuration. A removal of a host, including one through "Use the file's values", is refused before anything disconnects (S2 §4.4).
- **A host's reconnect** (S2's reconnect class) changes its connection identity, so its members re-key. The plan lists the members' alerts from the host's installations among what ends.
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

- `get_alerts` records its read's lineage on the `ChatRead`, in a field that is never sent to the model.
- Explain records it on the call.
- Snapshot mode and attention carry alert counts, so they record the alerts part's lineage too.

### 4.3 The checks

**Before every component read** of a bucket: the host's pinned connection is the registry's current connection and usable, and the installation's and subscription's revisions are the ones the read started under. A direct transport keeps C5's endpoint and credential revision checks instead of the host's (redesign §17a). A mismatch after the read discards it, as a credential change does today.

**Before every later send of retained data** (tools mode, snapshot mode, Explain and, later, A2), the turn's guard checks each recorded alerts lineage against the current one:
- the installation and the subscription are unchanged;
- for a hosted lineage, the host's current connection is the recorded one, and usable;
- for a direct lineage, the credential revision is unchanged;
- for a legacy lineage, the source identity and the credential revision are unchanged;
- the subject member's own connection, as today.

A mismatch ends the conversation's later sends, as a reconnect does: "lgtm-we changed since its alerts were read: start a new conversation". Recovery is new work under the new lineage. A request already sent cannot be recalled.

**The member's consent** belongs to S5 and A2. S3a checks the member's connection only, as today.

### 4.4 What this changes for today's files

A legacy block's lineage is its source identity and credential revision. A change to its endpoint, match, tenant or credential, through Test, a credential action or a restart, now ends later sends of the alerts a conversation read before the change. That closes today's gap (§0) for Alertmanager. Loki and metrics close it in S3b.

## 5. Plans and Apply (S3a-4)

**Observability's plan** follows slice F: planned from what runs, confirmed by its digest, applied in order, and refused when the file or a revision moved in between. It covers:
- an installation added, edited or removed;
- a cluster's subscription block written, changed or removed, `off` included.

Legacy blocks stay file-only, except through the migration offer (§7).

**The plan says, for each change:**
- the effective changes, with their inputs;
- the members whose resolution changes, each with its scope sentence before and after;
- what ends: each member's alerts snapshot re-keys, and conversations holding alerts of the old lineage end at their next send;
- what an installation's removal does to the clusters it served: back to discovery, or to another installation's group, said for each.

There is no interlock: alerts are read-only, and none of redesign §1.5's dangers applies.

**Apply,** under the fleet Apply lock, so it is serialized with S2's plans, whose group and membership changes also move resolution:
1. Write: the `sources` block through the block writer (`saveTopLevelBlock`), and each cluster's `alertmanager` block through S2's entry writer, extended to that one block. Both keep the write policy: a symlink kept, the mode kept, compare-and-swap, the lock.
2. Re-resolve from the written candidate, and move the changed revisions.
3. Re-key the affected members' snapshots and update the bucket readers.
4. Publish: Observability, the lenses and the scope sentences read the new resolution.

A write that fails changes nothing. Re-resolution cannot fail after a successful write, since the candidate was validated before it.

**An installation's credential** (`source/<id>`) is an immediate action, as the cluster endpoints' are: Store, Remove and Test under the endpoint lock, then the fleet Apply lock. Its revision is the installation's credential revision. A credential is never copied from a cluster's record.

## 6. Detect (S3a-4)

Detect, per group or per cluster, proposes. It never proves (redesign §6.7):
1. In each member, Klyx runs the existing discovery, with its proof of one installation.
2. For each installation found, it reads one bounded sample: `/api/v2/alerts`, capped at 1 MiB and 500 alerts. The parser keeps label names, and the values of labels whose values equal a fleet cluster's name. Annotations and every other value are discarded as they are parsed: never kept, shown or sent. Loki lines are never read for this.
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
- **Secrets are never copied between accounts.** The installation's credential is stored once, by the person. Until it is, the plan says so, and the installation's reads fail as "no credential stored for lgtm-we". The clusters' old records stay until the person removes them.

## 8. The page (S3a-4)

**Observability's sentence** names, for the open cluster or the fleet, where alerts come from: "Alerts: 9 clusters from 2 installations, 3 discovered in themselves, 1 off".

**Installations,** a table: id, where (host and Service, or the endpoint's origin), tenant, member label, serves, credential state, not attributed in the last read, and Test. Each opens an editor, with Review and apply.

**Subscriptions,** per cluster: the mode and how it resolved (its own block, by name, by group, discovered, none, off), the scope sentence, and what shadows what. A subscribed cluster's row edits its subscription block; a legacy row is shown as today, with the migration offer where it applies.

**Detect,** per group or cluster, opens its proposal in the plan panel.

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
- scopes: an override replacing the member label's value, an added key, `scope: tenant` alone and shared, the redesign's R3 cases (wrong or missing labels, an empty override, overlapping matches, duplicate values, escaped values).

**Reading (S3a-2):**
- R2: one URL with different tenants (two buckets); a tenant-isolated member without a cluster label (`scope: tenant`);
- one read per bucket per interval for several members; coalescing; the fleet-wide limit of four held with buckets;
- the split: rows to their members, not attributed counted, a non-reading member's rows dropped uncounted;
- R3: a hot member filling its bucket's cap qualifies every member;
- the 8 MiB and appearances failures fail every member's read;
- the host disconnected: "not connected", never "no alerts"; the host's discovery finding none: unreadable, never absent;
- re-keys: an installation, a subscription and a host reconnect each start a new generation;
- the host check refusing a host's removal;
- the load test at §3.2's shape, with the retained bytes measured;
- the lab: a real Alertmanager in the kind lab serving two named members by label, through a host, with a row for neither.

**The fences (S3a-3):**
- R2: a scope change ends a retained read's later sends;
- R4: the host disconnecting or losing access between a read and a send; a failed component read; retained history; an installation changing; recovery after a fence ended the old work;
- a legacy block's endpoint, match, tenant or credential changing ends later sends (today's gap);
- the host is never added to a comparison set, and a member's conversation never sees another member's rows or the not-attributed rows.

**Settings (S3a-4):**
- plans: each change's members, sentences and endings; a refusal on a moved file or revision; Apply's order;
- an installation's credential: stored, removed, and refused for another origin;
- Detect: the sample's bounds, annotations never kept, two clusters with equal Service names proposed apart, a missing member not said to have no alerts;
- the migration offer: proposed, never applied on its own, no secret copied;
- the page: fixtures for long names and URLs, 200% zoom, a narrow window, keyboard and focus, night and day.

**The native smoke** gains a walk: an installation hosted by the lab cluster serving it by group, its scope sentence in the Alerts lens, and the lab's planted alert for another member counted as not attributed. The walk restores the smoke fleet.

## 10. Not in S3a

- Loki and metrics: S3b, on this model.
- Consent and the proactive runs: S5 and A2.
- Federated reads across tenants (redesign §17).
- Renaming a cluster or an installation id: an id is the credential's account and the lineage's name.

## 11. For Codex's ruling

1. **A cluster's own block always wins, an empty one included** (§2.2). The alternative, letting a group installation take over an empty or scope-only block, would change what today's files read.
2. **A subscription change is fenced by lineage, not by the policy epoch** (§3.4, §4.3). Redesign §4 says the epoch moves when a source subscription feeding the cluster changes. Moving it would also withdraw A3's offers and arms, which alerts never justify (a step needs a citation of its target by UID). The lineage fence ends exactly the work that used the old source. I propose that refinement of §4.
3. **Detect reads `/api/v2/alerts`, the only Alertmanager API that returns label values** (§6). The response carries annotations; the parser keeps label names and member-like values only. Redesign §6.7 says Detect never reads alert bodies: I read that as never keeping, showing or sending them.
4. **Every subscription states its scope** (§2.3): an installation without a member label needs a match or `scope: tenant`. The unscoped "everything there" stays a legacy mode only.
5. **A configured installation that resolves nothing is unreadable, never absent** (§3.3).
6. **The host's connection identity is part of the lineage key** (§3.4), so a host's reconnect re-keys all of its members.
