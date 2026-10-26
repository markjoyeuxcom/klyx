# Klyx Settings S2: Fleet

Opened 2026-10-26, on `feat/settings-s0`, after slice F was accepted (F §24). S2 is the redesign's Fleet slice (redesign §5, §14), and it builds:
- the cluster editor;
- protection through any input, with the interlock;
- reconnect and remove.

It applies every change through F's plans and policy registry.

This note is for Codex's review before it is built; revision 1 the same day, after that review (§9).

## 0. What exists

From a read of the code at 39f17cd, with an inventory of every per-cluster piece:

**Nothing per cluster can be stopped.**
- `fleet.Registry` only grows: it has `Start` and `Add`, and no replace or remove.
- `ClusterConn` has no `Stop`. Its loops, its informers and its health Observer all run on the app's context, and end only at quit. The Observer holds a cancel, but nothing calls it.
- A connection that failed to build at start is recorded as Failed and never retried, and `Add` refuses its name.

**Nothing forgets a cluster.** None of these drops a cluster's name:
- the policy registry (`Seed` is a no-op for a known name);
- the metadata index's coordinator and budget;
- the health histories: obslog, the rows lineage, the route, applied and line histories, and the line tap;
- the notification engine;
- the endpoint credential revisions;
- the guard's arms and the registry's entries.

**Work on a cluster, and how it ends today:**

| Work | Identified by | Ends today |
|---|---|---|
| port-forwards | an id with the cluster | `StopForward`, or `StopAll` at quit |
| log streams | a stream id, with no cluster | `CloseLogStream`, `CloseAll`, or eviction at the cap |
| live events | `events:<cluster>:<ns>`, the connection pointer captured | `CloseLiveEvents`, `CloseAll` |
| log history views | a view id, with the cluster | `CloseView` |
| lens row subscriptions | a token, pinned to the Observer | `CloseRows` from the page |
| the line's attachments | its station, by name | `LineService` reconciles: it detaches on a missing cluster or a new Observer |
| chat and investigations | the conversation, by cluster | lazily, at the next read or send: by connection pointer, epoch or AI revision |
| A3 offers, reviews and runs | the plan and the epoch | an offer withdrawn by the epoch; a run refused at its write |
| Explain and Suggest-fix | the call, with the cluster | by the epoch fence, at the next read or send |
| drains | a stream id, with no cluster | `CancelDrain`. A started drain is not stopped by a policy move (F §2) |
| Helm rollbacks | not tracked | runs to its end, with no Klyx deadline |
| exec and debug terminals | not tracked: detached OS terminals | never; Klyx does not see them |

**The running configuration and protection:**
- `ConfigService` keeps the startup configuration and the clusters added this session. Drift from the file is said: "Klyx applies it at a restart".
- **Protection has two readers.** The guard, AI and `ConfigService.IsProtected` read F's published snapshot. The notification engine's protected scope, the fleet DTO and the Settings table read `ClusterConfig.IsProtected()`. They agree today only because protection never changes while Klyx runs.
- **Protection's inputs:** the `protected` flag, the environment (or `tags.env`), the name and the context, each matched for a `prd`, `prod` or `production` token, and OR'd together. So `protected: false` cannot override an inferred protection.

**Writers:**
- Only `AppendClusters` writes the clusters list, and only by inserting text.
- The one in-place node-wise edit is `MoveMetricsTokenToAuth`, which replaces a single line.
- `validate` refuses an empty clusters list and duplicate names.

**Built but unused:** F's `Begin`, `Publish`, `Hold` and `Release` have no callers yet; F built them for S2.

**Kept by name:**
- **The keychain:** endpoint credentials are stored as `endpoint/<cluster>/<kind>`. The store cannot list accounts, so a record left by a removal cannot be found without its cluster's name. A cluster added again under that name computes the same account and finds it (§4.3).
- **The page:** view preferences, each view's search, the palette's frecency ids, and the restored route. The route is checked for existence on restore.

## 1. Stages

S2 is built in three stages, each leaving a working app and each reviewed on its own:
- **S2a, the cluster lifecycle (backend):**
  - a connection that stops;
  - connection identities, and replace, remove and retry in the registry;
  - leases: the work on a cluster admitted, listed and ended by its connection;
  - retiring a connection's identity, so nothing it publishes later is taken;
  - a removed cluster forgotten;
  - one protection reader;
  - the running fleet.
- **S2b, the writers and plans:**
  - a cluster entry edited node-wise, or removed;
  - the reservation, and plans for an edit, the file's values, a connect, a removal and Revert;
  - protection's effect and the interlock;
  - a reconnect held until its own connection is usable.
- **S2c, the page:** the editor, its plans and the removal; drift and file-only clusters; the native smoke.

## 2. The cluster lifecycle (S2a)

### 2.1 A connection that stops

- `ClusterConn.Start` derives its own context from the one it is given. `Stop()` cancels it, then waits, bounded, for its loops to return: connect, refresh and reprobe, the capability informers, the node and pod informers, and the parameter kinds and episodes.
- The health Observer gains `Stop()`: its existing cancel. It publishes its final summary and a BatchEnd, exactly as at quit, and every consumer already handles that end:
  - the line detaches;
  - the index ends that generation;
  - the notification engine closes its episodes.
- **A stopped connection is never usable again.** Its state is Stopped: not usable, never counted as Synced, and `ConnUp` is never called for it.

### 2.2 The registry: replace, remove, retry

**Every connection has an identity:** a `ConnID`, drawn from one process-wide counter when the connection is built. A replacement, or a cluster removed and added again under the same name, therefore has an identity above every one before it. The connection's incarnation (F §1) comes from the same counter, so incarnations never recur either.

**Operations:**
- `Prepare(cc)` builds a connection through the factory without starting it: the REST configuration, the clients and the capability detector. It is given its `ConnID` then. A factory error returns, and nothing changes. §4.5 uses this, so a context that cannot be resolved writes nothing.
- `Replace(prepared)` retires the current connection's identity (§2.4), then swaps the entry under the registry's lock. From then on every lookup returns the new connection, which starts. The old one is stopped outside the lock.
- `Remove(name)` retires the identity, takes the entry out of the registry under the lock, then stops the connection.
- `Retry(name)` builds again a connection that failed to build: one that failed at start, or a reconnect's attempt (§4.5). `Add`'s refusal of a duplicate stays for a running connection.

**The index order follows the running fleet,** not the startup configuration.

### 2.3 Leases: admission, registration and teardown

A connection is never looked up and then used later. Every piece of work on a cluster takes a **lease** from the cluster's lifecycle, as one step, under the lifecycle's lock:

```go
// Acquire admits one piece of work on cluster, now: refused while the
// cluster is Reserved, Held or retired (§4.1, §4.5). It pins the current
// connection and registers the work, pending, before any I/O.
func (l *Life) Acquire(cluster string, w Work) (*Lease, error)

type Lease struct {
	Conn *fleet.ClusterConn // the pinned connection
	Ctx  context.Context    // cancelled when its connection is retired, unless the work continues
	// ...
}

// Settle records what the work became, after its I/O ("forward of
// localhost:8080 to shop/web:80"). It fails if the connection was retired
// meanwhile, and the worker then closes what it opened.
func (l *Lease) Settle(desc string) error
func (l *Lease) Release()
```

**The contract:**
- **Pending work is listed.** A lease is in the list from `Acquire`, as "a port-forward to shop/web, opening", before its I/O. So a plan's work version includes work that has not finished starting.
- **A retired lease cannot settle.** Retiring a connection cancels its leases' contexts and marks them retired. A late completion's `Settle` fails, and the worker closes what it opened. That covers the window Codex's probe found: a forward that captured the old connection, blocked on its I/O, and would otherwise register after the teardown.
- **Retiring holds no lock across I/O.** It marks and cancels under the lock, then waits for nothing. Each worker ends its own I/O when its context is cancelled.
- **Every worker takes a lease,** not only the guarded writes: forwards, log streams, live events, log history views, chat conversations and investigations, A3 offers, reviews and runs, Explain and Suggest-fix, drains and Helm rollbacks.
- **A chat start that pinned its epoch before a transition** takes its lease at registration. If the cluster is Reserved or Held by then, the start is refused, even though its read and send fences would also refuse it later.

**The work, as listed and as ended:**

| Work | Listed as | On a reconnect or removal |
|---|---|---|
| port-forward | "forward of localhost:8080 to shop/web:80, started 10:42" | ends: stopped |
| log stream | "logs of shop/web-7c9 (container web)" | ends: closed. The page says the stream ended and why |
| live events | "live events in shop" | ends |
| log history view | "log history of shop/web" | ends |
| chat or investigation | "the investigation of Deployment shop/web, started 10:40" | ends, explicitly at Apply and with the reason ("aks-we-prd reconnected"). The page is told: a version moves and an event says the reason |
| A3 offer or review | as in F2a | ends: withdrawn |
| A3 confirmed request | as in F2a | continues: "its write is refused if it was not yet sent; once sent, it keeps its written or unverified outcome" |
| Explain, Suggest-fix | as in F2a | ends |
| drain | "the drain of node-3, started 10:41" | continues: the kubectl child runs to its end (F §17a); Cancel still stops it |
| Helm rollback | "the rollback of release shop/web" | continues: it runs to its end |

- **A continuing lease** has no context of its own to cancel. It is listed under Continues, and it is released when the work ends.
- **A lens view's row subscription is not a lease.** It follows the Observer: the page subscribes again to the new one, and a removed cluster's views close.
- **Exec and debug terminals are not tracked.** Every reconnect and removal plan carries a fixed line instead: "An exec or debug terminal opened on aks-we-prd keeps its old context. Klyx does not track or close terminals."

### 2.4 Retiring a connection's identity

A retired connection must not publish into its replacement, nor recreate state a removal forgot. Its in-flight callbacks can outlive a bounded `Stop`. So the barrier is its identity, not its quiescence:
- **The lifecycle keeps each cluster's current `ConnID`, and a set of retired ones.** Retirement happens before the swap or the removal (§2.2).
- **Every callback a connection makes carries its `ConnID`,** and the receiver drops it when that identity is not the cluster's current one. The callbacks:
  - **usability:** the registry's `onUsable`, and so `policy.ConnUp`;
  - **connection state:** the index's `SetConnection`;
  - **the health sink's publications:** transitions, summaries, rows and the final batch. These already carry the Observer's incarnation, which is the same identity;
  - **deferred cleanups.**
- **Every shared sink gates on that check:**
  - the health sink, ahead of its listeners: the line, the notification engine, obslog, the histories and the line tap;
  - the index's coordinator;
  - the policy registry.
- **No publication creates state for an unknown or retired identity.** The index's `coordFor` and the per-name maps in the sinks create an entry only for the current identity.
- **Forgetting keeps the retirement.** A removed cluster's last `ConnID` stays in the retired set. So a late callback after a removal is dropped and recreates nothing, and a re-add installs a new identity above it.
- **A `Stop` that times out** is logged. The connection is already retired, so whatever it publishes later is dropped.

### 2.5 Forgetting a removed cluster

A removal forgets the cluster everywhere:
- **in the policy registry,** with its last epoch kept as a tombstone;
- **the guard's arms** for it;
- **the index:** its coordinator stops, its store and budget are released, and its coverage row goes;
- **the health histories:** obslog, the rows lineage, the route, applied and line histories, and the line tap;
- **the notification engine:** its episodes close silently, so no recovery is notified;
- **its conversations,** once ended.

Its endpoint credential revisions are kept as tombstones too, since revisions are fences.

**Epochs never recur.** A cluster added again under a removed name starts above its tombstoned epoch and revisions. So a ticket, arm, review or credential revision from before the removal can never match.

**A reconnect forgets nothing by name.** Histories continue across incarnations, as today.

### 2.6 The running fleet

- `ConfigService` holds the running clusters as one list: the startup configuration, then each addition, edit and removal. It carries a revision, and each Apply replaces one entry under it, never the whole list from an older copy.
- `LiveClusterConfig` and `LiveFleetClusters` read that list, and so does every other runtime reader of a cluster's configuration:
  - the fleet DTOs;
  - the line's placement;
  - the notification engine's place;
  - the Settings table and its drift.

### 2.7 One protection reader

- **Every reader of effective protection reads the published snapshot.** That adds the fleet DTO, the Settings table and the notification engine's protected scope to the readers already on it.
- **For display,** the running entry supplies the inputs and says which one infers protection. The published value is the authority.

## 3. Writers (S2b)

### 3.1 Editing an entry

`config.EditClusterAt(path, version, name, edit)` writes a cluster entry's changed fields only:
- **The fields:** context, kubeconfig, group, environment, `protected`, and tags (region is `tags.region`). Each field is either set or cleared.
- **Finding the entry:** by name, in the parsed node tree.
- **Refusals,** each saying "edit this entry by hand" and why:
  - the clusters sequence or the entry is a flow collection;
  - an alias, an anchor or a merge key;
  - a duplicated key;
  - a changed scalar written over several lines;
  - tags that are not a block mapping.
- **A scalar field already present:** its value is replaced on its own line, keeping the key, the indentation and a trailing comment.
- **An absent field:** a line is inserted after the entry's last key line, at the entry's indentation.
- **A cleared field:** its line is removed.
- **Tags** are rewritten as one nested block, in Klyx's form. Comments inside the tags block are not kept, and the editor says so.
- **The candidate is checked before it is written:**
  - it loads strictly;
  - every other cluster and top-level key decodes unchanged;
  - the entry decodes to exactly the intended fields.

  Then it is written against the plan's file version, through slice 0's write policy.

### 3.2 Removing an entry

`config.RemoveClusterAt(path, version, name)` removes the entry's lines: from its `- ` line up to the next item, or to the end of the sequence.
- Comments inside the entry go with it. A comment line directly above it stays.
- **Refused:**
  - a flow sequence;
  - the last cluster: "Klyx needs at least one cluster in fleet.yaml". `validate` already refuses an empty list.
- The candidate must decode with only that entry gone.

## 4. Plans (S2b)

### 4.1 The reservation, and what moves the epoch

Applying a plan must change nothing when it is refused before the write: F §11, and Codex's ruling. F's `Begin` cannot be that gate, because it moves the epoch, and its `Publish` moves the epoch again even when protection is unchanged. So S2 adds a reservation to the policy registry, separate from the committed transition:

| State | New admissions | What was admitted before | Entered by | Epoch |
|---|---|---|---|---|
| **Reserved** | refused: no ticket, no lease, no AI start | stays valid under its epoch | `Reserve(cluster)`, at Apply | unchanged |
| back to Active | admitted | valid | `Unreserve(cluster)`: a refusal, or a change that moves no epoch | unchanged |
| Active, moved | admitted under the new epoch | stale | `Advance(cluster, protected)`: a committed protection change | moves once |
| **Held** | refused | stale | `Hold(cluster, reason)`: a committed reconnect, until it is usable (§4.5) | moves |
| Active, released | admitted under the new epoch | stale | `Release(cluster, protected)`, by the bound connection's first usability | moves |

- **A ticket stays valid while Reserved.** A ticket is valid while its cluster is Active or Reserved, under the epoch it captured. A Reserved cluster refuses new tickets, leases and AI starts. So a manual write admitted before the reservation is not refused by the reservation itself, only by a committed move.
- **The reviewed baseline is the epoch before the reservation.** The re-plan under the reservation compares the epoch the plan bound with the cluster's epoch, which `Reserve` did not move.
- **The work snapshot is final under the reservation.** No lease can be acquired while Reserved, so the re-plan's list of work is the list the Apply ends.
- **F's `Begin`, `Publish` and `Hold` keep their meaning** for any other caller. S2 uses `Reserve`, `Unreserve`, `Advance`, `Hold` and `Release`.

### 4.2 The plan

Fleet plans use F's engine: `PlanDTO`, `seal`, the candidate cache and the digest. The section is `fleet`, and each plan is for one cluster.

**Kinds:**
- `save`: the editor's draft;
- `file`: the file's entry (§4.6);
- `connect`: a cluster in the file and not running (§4.6);
- `remove`;
- `revert`: back to the last terms that connected.

**Classes,** from what the plan changes (redesign §4):

| Change | Apply, after `Reserve` and the re-plan | Epoch |
|---|---|---|
| placement (region, group, environment, tags) with protection unchanged | write; swap the running entry; `Unreserve`. The line rescans, and notifications relabel | unchanged |
| protection tightened, through any input | write; swap the entry; `Advance` | moves once: arms, reviews and offers are stale, and conversations end |
| protection loosened, through any input | the interlock naming the cluster, then as tightening | moves once |
| context or kubeconfig | prepare the connection; write; swap the entry; `Hold` ("connecting"); `Replace`; `Release` at the bound first usability (§4.5) | moves, and stays held until usable |
| removal | the interlock on a protected cluster; the host check (§4.4); write; `Remove`; forget | gone, and tombstoned |

**A refusal before the write** (a moved file, a moved digest, a missing interlock, a failed preparation) `Unreserve`s. Nothing is invalidated, and no work ends.

**What a plan shows:**
- **Changes:** each field from and to. Protection's effect is its own line, with the input that decides it: "Protection: on, inferred from environment prd → off". The effect is derived from the complete candidate, defaults included. So removing one input never reports protection off while another input still infers it: a cleared context defaults to the name, which may infer it.
- **Ends and Continues:** §2.3's list for this cluster, by identity, pending leases included. The work version is in the digest, as in F2a.
- **Interlock:** for loosening protection, or removing a protected cluster. The phrase is the cluster's name, as for the destructive verbs. Tightening never takes one.

**One fleet Apply at a time.** Plans for different clusters, and additions and removals, share the fleet file and the running list. So:
- Applies are serialized fleet-wide, by one lock.
- Each re-plan checks three things together: the plan's file version, the running fleet's revision, and the cluster's work identities.
- An Apply replaces only its own entry in the running list.
- `AddClusters`, `connect`, `revert` and `Retry` go through the same transaction.

### 4.3 Protection that cannot be loosened here

Protection inferred from the name, which is read-only, cannot be loosened in Settings. Nor can protection inferred from a context the person keeps.

The editor says what infers it, and what would change it: "inferred from the name aks-we-prd: a rename is a later slice". `protected: false` is never offered as a way to loosen protection, because protection ORs all its inputs.

### 4.4 Plan-time checks

- **A new context or kubeconfig is resolved when the plan is made.** The REST configuration builds, and the context exists in the effective kubeconfig. A plan that cannot connect is refused before anything is written. Resolvable is not reachable: the plan cannot know that the API server answers, so the new connection's own state says that (§4.5).
- **The host check.** A removal, including one through "Use the file's values", is checked against the candidate configuration first. That includes file-only installations, once S3a builds them: an installation naming the cluster as its host refuses the removal, before anything disconnects or is deleted. In S2 no installation can name a host, so this is the hook S3a fills. A host that changes between the plan and the Apply moves the plan's file version, so the Apply refuses.
- **The keychain records** a removal offers to delete are those present: `endpoint/<cluster>/<kind>`.
  - Each record is its own choice, bound by the digest to the record's current credential revision.
  - Deletion is serialized with the endpoint credential actions (set, clear and move) and with a re-add of the same name. A credential changed between the plan and the Apply moves its revision, so the Apply refuses.
  - None is deleted unless chosen. A deletion that fails is said, record by record, and the cluster stays removed.
  - **A kept record** stays in the keychain. A cluster added again under that name finds it, and it is usable for the same endpoint origin: Observability shows it as stored, and can remove it.
- **No secret** is in a plan, in its digest input or in the candidate cache: a credential appears only as present or absent.

### 4.5 Apply, and saved, not connected

**Apply** for a plan:
1. `Reserve` the cluster; on a removal or an addition, take the fleet Apply lock as well;
2. re-plan, and compare the digests;
3. check the interlock;
4. for a reconnect, prepare the connection;
5. write, against the plan's file version;
6. commit by class (§4.2).

A refusal at any step before the write `Unreserve`s, and changes nothing.

**A reconnect is held until its connection is usable.**
- After the write, the cluster is Held ("saved, not connected yet: connecting with context <ctx>"). The new connection is swapped in and starts.
- Nothing new is admitted while it is held: no ticket, no lease and no AI start, even though the connection's clients exist.
- **The release is bound to this attempt.** Only the first usability of the connection this attempt prepared releases the hold, identified by its `ConnID`. `Release` then publishes the new protection, and the new terms become the last terms that connected.
- **A stale attempt changes nothing.** A usability from an earlier attempt, or from a connection Revert or Retry has since replaced, is retired (§2.4), so it neither releases the hold nor replaces the last connected terms.
- **While held,** Settings says both states, and offers two ways on:
  - **Revert:** a plan back to the last terms that connected. It writes them, and prepares and swaps a connection of its own, held the same way;
  - **Retry:** a fresh connection for the saved terms, with a new identity.

  The connection's own retries continue meanwhile; the first usability of the current attempt is what releases.
- **At start,** a cluster whose connection failed to build is Held with that reason, and offers Retry (§2.2).

### 4.6 Use the file's values

**The field boundary.** "Use the file's values" adopts S2's fields only: context, kubeconfig, group, environment, tags and `protected`.
- An entry's endpoint blocks (metrics, Alertmanager and Loki) keep their existing route: Observability's Test applies them, as today, until S3. Endpoint drift stays reported where it is now.
- A reconnect's new connection takes the running entry's endpoint terms, never the file's. So adopting a context never silently adopts an endpoint as well.
- The candidate cache holds S2's fields only. A plan that needs the file's whole entry (`connect`) binds the file version, and re-reads the entry at Apply. It never caches the raw entry, which may hold a legacy `metrics.token`.

**The cases:**
- **A running cluster whose file entry differs:** a plan from the running entry to the file's entry, writing nothing, in its class (relabel, protection or reconnect).
- **A cluster in the file and not running:** "Connect" plans its addition from the file, writing nothing, with the file's endpoint terms: there are no running ones. The plan says which endpoint kinds are configured, and never their secrets. This replaces "Klyx connects them when it restarts". A fleet file that failed to load at start does not block it: whether the file loads is checked again at plan time.
- **A running cluster gone from the file:** "Use the file's values" plans its removal, writing nothing, under the same interlock.
- **A file that does not load:** no plan, and Settings says so. That includes a file emptied of clusters by hand: it does not load, so its emptiness is never adopted as a removal of the last cluster.

## 5. The page (S2c)

**The table** is a real table, with header semantics and rows at least 40px. A row opens its cluster's editor (redesign §5):
- **name:** read-only, saying why;
- **context and kubeconfig:** the context is picked from the entry's effective kubeconfig;
- **region, group, environment and tags:** each says what it places or ranks;
- **protection:** explicit, inferred (with its input), or none. Its effect is in words, and so is what would loosen it (§4.3);
- **endpoints:** a link to Observability, filtered to this cluster.

**Actions:**
- **Review and apply** shows `PlanView`: the changes, protection's effect, what ends and what continues. The interlock is the existing dialog, with the cluster's name.
- **Remove…** shows the removal plan, with the keychain choices and the interlock on a protected cluster.

**Notices:**
- drift, with "Use the file's values";
- a file-only cluster, with "Connect";
- "saved, not connected yet", with Revert;
- a connection that failed to build, with Retry.

**The registry:** context, kubeconfig, group, environment, tags and protection become editable. The name stays file-only, "a later slice".

**After a reconnect or a removal:**
- the lens views follow the new Observer;
- a removed cluster's route goes back to the fleet, with a note;
- view preferences keyed by its name are kept, so a cluster added again under that name has them back.

## 6. Tests

**S2a:**
- **Stopping:** a stopped connection on fakes leaves no goroutine above its starting count.
- **The Observer's end:** its final summary reaches the line, the index and the notification engine.
- **`Replace`:**
  - `Conn` returns the new connection, and the old one is stopped;
  - the old connection's forwards, streams, events and history views end, and the new connection's are untouched;
  - its conversations end with the reason;
  - drains and rollbacks continue, and are listed.
- **`Remove`:** forgets the cluster in every store, each one checked.
- **Re-adding a name:** epochs, identities and credential revisions never recur after a removal.
- **Leases:**
  - a pending start (a forward paused in its I/O) is listed, and cannot settle after its connection is retired: Codex's probe;
  - every worker acquires one;
  - none is admitted while Reserved or Held.
- **Retirement:** delayed down, up, final and batch callbacks from a retired connection, across a swap, a removal and a re-add, including after a `Stop` that timed out. Each is dropped, and none recreates state.
- **Protection readers:** all of them agree after a publication.

**S2b:**
- **The editor's writes:**
  - other fields, comments and unrelated bytes kept;
  - the refusals: flow, alias, anchor, merge, duplicate and multi-line;
  - the tags block;
  - trailing comments, nested blocks, CRLF line endings and document boundaries;
  - the defaulted context, and protection inferred from it, after a field is cleared;
  - slice 0's YAML error redaction, and its symlink, mode and compare-and-swap policy.
- **A removal's writes:** the last cluster refused; writes held to their file version.
- **Plans, by class:**
  - the interlock taken for loosening and for removing a protected cluster, and not for tightening;
  - protection's effect line, including protection inferred from the name that cannot be loosened;
  - a context that cannot be resolved, refused at plan time, and at Apply before the write;
  - a refusal before the write leaving the epoch, the arms, the offers and the conversations as they were;
  - a placement-only Apply leaving the epoch as it was;
  - a guard during the reservation refused, and a ticket admitted before it still valid;
  - a reconnect held until its own connection's first usability; a stale usability from an earlier attempt neither releasing nor recording terms; Revert and Retry bound the same way;
  - two clusters' Applies serialized, neither overwriting the other in the running list;
  - a reconnect ending the listed work;
  - the digest moving when work starts;
  - the keychain choices bound to the digest, and only the chosen records deleted;
  - "Use the file's values" for each kind of drift, adopting S2's fields only; a hand-changed endpoint left to its own route; no raw entry cached;
  - a keychain deletion refused when its credential's revision moved, and a partial failure said record by record.
- **The redesign's named R1 tests** (§15):
  - a hand edit before Apply;
  - protection tightened after a guard and before the write;
  - a stale arm or review after a reconnect;
  - a change while a confirmation is open;
  - "saved, not connected yet", with Revert.

**S2c:**
- **The page:** the editor, its plans, the interlock, the removal, and the notices.
- **The native smoke on kind-klyx:**
  - a placement edit relabels the line;
  - protection is loosened with its interlock, then tightened back;
  - a reconnect to the same context, through an explicit kubeconfig path, ends and restores the cluster's views.

## 7. Not in S2

- A rename, and its keychain migration: a later slice.
- S3a's installations and host check; S2 provides the hook.
- Tracking exec and debug terminals.
- Stopping a started drain or rollback.
- Editing endpoints (S3) and `line.source` (later).

## 8. For Codex's ruling

1. Conversations and investigations end explicitly at Apply, with the reason, rather than lazily at their next read. The plan's Ends then say what has actually ended.
2. Loosening an inferred protection is offered only through the input that infers it. Protection inferred from the name is said, and cannot be loosened until a rename exists.
3. A reconnect prepares its connection before the write, so a context that cannot be resolved writes nothing. "Saved, not connected yet" is then a replacement not yet usable, with Revert to the last terms that connected.
4. The last cluster cannot be removed.
5. Each keychain record of a removed cluster is offered for deletion as its own choice, bound by the digest, and kept unless chosen.

## 9. Revision 1: Codex's review (2026-10-26)

Codex requested changes: five P2 and one P3. It accepted the staging, the foundations of S2a, and the five rulings of §8, the second and fifth with conditions now in §4.2 and §4.4. Each finding is addressed:

| Finding | Change |
|---|---|
| **R1, P2:** `Begin` before the re-plan moves the epoch, so a refusal before the write would end work, and a placement-only change could not keep its epoch | §4.1: a reservation separate from the committed transition. `Reserve` refuses new admissions and keeps the epoch, and what was admitted before stays valid. A refusal `Unreserve`s and changes nothing. Placement `Unreserve`s, protection `Advance`s once, and a reconnect `Hold`s |
| **R2, P2:** recording a connection on registered work leaves a pending start (a forward blocked in its I/O) outside the teardown | §2.3: leases. Every worker acquires one, as one step under the lifecycle's lock, before its I/O. Pending leases are listed and in the work version. A retired lease cannot settle, so a late completion closes what it opened. Retiring holds no lock across I/O |
| **R3, P2:** `Replace` then `Publish` admits work before the new connection is usable | §4.5: a reconnect is Held after its write until its own connection's first usability, identified by `ConnID`. Only that releases it and records the last connected terms. A stale attempt is retired, and neither releases nor records. Revert and Retry are bound the same way |
| **R4, P2:** a retired connection can publish by name into its replacement, or recreate state a removal forgot | §2.4: retirement by identity. Every callback carries its `ConnID`. The registry, the policy registry, the index and the health sink drop a retired or unknown identity, and no sink creates state for one. Forgetting keeps the retirement, and a `Stop` that times out stays retired |
| **R5, P2:** "Use the file's values" had no rule for an entry's endpoint fields | §4.6: adoption takes S2's fields only. Endpoints keep their route (Observability's Test, until S3), and a reconnect keeps the running endpoint terms. `connect` uses the file's endpoint terms, bound to the file version and re-read at Apply, and never caches a raw entry |
| **R6, P3:** a kept keychain record can be found again by its name | §0 and §4.4: the store cannot list accounts, but a re-add under the same name finds the record, and it is usable for the same endpoint origin. Each deletion is bound to the record's revision and serialized with the credential actions; a partial failure is said |

**Also adopted from the review:**
- **One fleet Apply at a time:** each re-plan checks the file version, the running fleet's revision and the work identities together. Every route goes through the same transaction: `AddClusters`, `connect`, `revert` and `Retry`.
- **Load checked again at plan time:** a fleet file that failed to load at start no longer blocks `connect` for the session.
- **The writer's extra cases:** trailing comments, CRLF, document boundaries, and the defaults after a cleared field.
- **The host check** gates a removal through "Use the file's values" too.
- **A file emptied by hand** does not load, so it is never adopted as a removal.
