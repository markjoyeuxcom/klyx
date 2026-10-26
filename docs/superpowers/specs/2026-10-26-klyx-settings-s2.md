# Klyx Settings S2: Fleet

Opened 2026-10-26, on `feat/settings-s0`, after slice F was accepted (F §24). S2 is the redesign's Fleet slice (redesign §5, §14), and it builds:
- the cluster editor;
- protection through any input, with the interlock;
- reconnect and remove.

It applies every change through F's plans and policy registry.

This note is for Codex's review before it is built.

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
- **The keychain:** endpoint credentials are stored as `endpoint/<cluster>/<kind>`. The store cannot list them, so a record left by a removal cannot be found later.
- **The page:** view preferences, each view's search, the palette's frecency ids, and the restored route. The route is checked for existence on restore.

## 1. Stages

S2 is built in three stages, each leaving a working app and each reviewed on its own:
- **S2a, the cluster lifecycle (backend):**
  - a connection that stops;
  - replace, remove and retry in the registry;
  - the work on a cluster, listed and ended by its connection;
  - a removed cluster forgotten;
  - one protection reader;
  - the running fleet.
- **S2b, the writers and plans:**
  - a cluster entry edited node-wise, or removed;
  - plans for an edit, the file's values, a removal and Revert;
  - protection's effect and the interlock;
  - saved, not connected.
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

- `Prepare(cc)` builds a connection through the factory without starting it: the REST configuration, the clients and the capability detector. A factory error returns, and nothing changes. Section 4.4 uses this so that a context that cannot be resolved writes nothing.
- `Replace(ctx, prepared)` swaps the entry under the registry's lock. From then on every `Conn(name)` returns the new connection, which starts. The old one is stopped outside the lock.
- `Remove(name)` takes the entry out of the registry under the lock, then stops its connection.
- `Retry(name)` builds a Failed entry again from the running entry, for a connection that failed to build at start. `Add`'s refusal of a duplicate stays for a running connection.
- **The index order follows the running fleet,** not the startup configuration.
- **Incarnations never recur.** A connection's incarnation is drawn from one process-wide counter. So a replacement, or a cluster removed and added again under the same name, starts above every incarnation before it (F §1).

### 2.3 Work on a cluster, listed and ended by its connection

Each service that runs work on a cluster records the connection it runs on, and implements:

```go
type clusterWorker interface {
	// clusterWork lists the work on conn by identity: what ending its
	// connection stops, and what continues.
	clusterWork(cluster string, conn ConnID) (ends, continues []PlanWorkDTO)
	// endCluster ends the work on conn, saying why. Work started on another
	// connection of the same cluster is untouched.
	endCluster(cluster string, conn ConnID, why string)
}
```

A plan lists the work through these, and Apply ends it through them, by connection. So a reconnect never closes work already started on the new connection.

| Work | Listed as | On a reconnect or removal |
|---|---|---|
| port-forward | "forward of localhost:8080 to shop/web:80, started 10:42" | ends: stopped |
| log stream | "logs of shop/web-7c9 (container web)" | ends: closed. The page says the stream ended and why |
| live events | "live events in shop" | ends |
| log history view | "log history of shop/web" | ends |
| chat or investigation | "the investigation of Deployment shop/web, started 10:40" | ends, explicitly at Apply and with the reason ("aks-we-prd reconnected"), not lazily at its next read |
| A3 offer or review | as in F2a | ends: withdrawn by the epoch |
| A3 confirmed request | as in F2a | continues: "its write is refused if it was not yet sent; once sent, it completes, unverified" |
| Explain, Suggest-fix | as in F2a | ends |
| drain | "the drain of node-3, started 10:41" | continues: the kubectl child runs to its end (F §17a); Cancel still stops it |
| Helm rollback | "the rollback of release shop/web" | continues: it runs to its end |

**What is added to make the table possible:**
- log streams and drains record their cluster and connection;
- live events record their connection (today they capture its pointer);
- a Helm rollback is recorded while it runs, with its cluster, release and start.

**Not listed by identity:** a lens view's row subscription follows the Observer. The page subscribes again to the new one, and a removed cluster's views close.

**Exec and debug terminals are not tracked.** Every reconnect and removal plan carries a fixed line instead: "An exec or debug terminal opened on aks-we-prd keeps its old context. Klyx does not track or close terminals."

**The order:** the work is ended after the swap or the removal. So new work goes to the new connection, and the old connection's work is ended by its identity.

### 2.4 Forgetting a removed cluster

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

### 2.5 The running fleet

- `ConfigService` holds the running clusters as one list: the startup configuration, then each addition, edit and removal. It carries a revision.
- `LiveClusterConfig` and `LiveFleetClusters` read that list, and so does every other runtime reader of a cluster's configuration:
  - the fleet DTOs;
  - the line's placement;
  - the notification engine's place;
  - the Settings table and its drift.

### 2.6 One protection reader

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

### 4.1 The plan

Fleet plans use F's engine: `PlanDTO`, `seal`, the candidate cache and the digest. The section is `fleet`, and each plan is for one cluster.

**Kinds:**
- `save`: the editor's draft;
- `file`: the file's entry (§4.5);
- `remove`;
- `revert`: back to the last terms that connected.

**Classes,** from what the plan changes (redesign §4):

| Change | Apply | Epoch |
|---|---|---|
| placement (region, group, environment, tags) with protection unchanged | write; swap the running entry. The line rescans, and notifications relabel | unchanged |
| protection tightened, through any input | `Begin`, write, `Publish` | moves: arms, reviews and offers are stale, and conversations end |
| protection loosened, through any input | the interlock naming the cluster, then as tightening | moves |
| context or kubeconfig | `Begin`; prepare the connection; write; `Replace`; `Publish` | moves: both the policy and the connection |
| removal | the interlock on a protected cluster; `Begin`; write; `Remove`; forget | gone, and tombstoned |

**What a plan shows:**
- **Changes:** each field from and to. Protection's effect is its own line, with the input that decides it: "Protection: on, inferred from environment prd → off".
- **Ends and Continues:** §2.3's list for this cluster, by identity. The work version is in the digest, as in F2a.
- **Interlock:** for loosening protection, or removing a protected cluster. The phrase is the cluster's name, as for the destructive verbs. Tightening never takes one.

### 4.2 Protection that cannot be loosened here

Protection inferred from the name, which is read-only, cannot be loosened in Settings. Nor can protection inferred from a context the person keeps.

The editor says what infers it, and what would change it: "inferred from the name aks-we-prd: a rename is a later slice". `protected: false` is never offered as a way to loosen protection, because protection ORs all its inputs.

### 4.3 Plan-time checks

- **A new context or kubeconfig is resolved when the plan is made.** The REST configuration builds, and the context exists in the effective kubeconfig. A plan that cannot connect is refused before anything is written.
- **An unreachable API server cannot be known when the plan is made.** The new connection's own state says it (§4.4).
- **A removal is checked against the candidate configuration.** That includes S3a's installations once they exist: an installation naming the cluster as its host refuses the removal. In S2 no installation can name a host, so this is the hook S3a fills.
- **The keychain records a removal offers to delete** (`endpoint/<cluster>/<kind>`, those present) are each a separate choice, bound by the plan's digest. None is deleted unless chosen.

### 4.4 Apply, and saved, not connected

**Apply** runs under the cluster's Applying state (`Begin`):
1. re-plan, and compare the digests;
2. check the interlock;
3. write, against the plan's file version;
4. activate, by class.

A refusal before the write `Publish`es the running protection again.

**A reconnect prepares its connection before the write.** If the factory fails because the context cannot be resolved now, nothing is written and the plan is refused with the reason.

**After the swap,** the new connection connects as any connection does, retrying on its own. Until it is first usable, the cluster is "saved, not connected yet":
- Settings says both states;
- it offers Revert: a plan back to the last terms that connected, which are kept until the new connection is usable.

**At start,** a cluster whose connection failed to build offers Retry (§2.2).

### 4.5 Use the file's values

- **A running cluster whose file entry differs:** a plan from the running entry to the file's entry, writing nothing, in its class (relabel, protection or reconnect).
- **A cluster in the file and not running:** "Connect" plans its addition from the file, writing nothing. This replaces "Klyx connects them when it restarts".
- **A running cluster gone from the file:** "Use the file's values" plans its removal, writing nothing, under the same interlock.
- **A file that does not load:** no plan, and Settings says so.

## 5. The page (S2c)

**The table** is a real table, with header semantics and rows at least 40px. A row opens its cluster's editor (redesign §5):
- **name:** read-only, saying why;
- **context and kubeconfig:** the context is picked from the entry's effective kubeconfig;
- **region, group, environment and tags:** each says what it places or ranks;
- **protection:** explicit, inferred (with its input), or none. Its effect is in words, and so is what would loosen it (§4.2);
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
- **Re-adding a name:** epochs, incarnations and credential revisions never recur after a removal.
- **Protection readers:** all of them agree after a publication.

**S2b:**
- **The editor's writes:** other fields and comments kept; the refusals (flow, alias, anchor, merge, duplicate, multi-line); the tags block.
- **A removal's writes:** the last cluster refused; writes held to their file version.
- **Plans, by class:**
  - the interlock taken for loosening and for removing a protected cluster, and not for tightening;
  - protection's effect line, including protection inferred from the name that cannot be loosened;
  - a context that cannot be resolved, refused at plan time, and at Apply before the write;
  - a guard between `Begin` and `Publish` refused (F's R1 probe);
  - a reconnect ending the listed work;
  - the digest moving when work starts;
  - the keychain choices bound to the digest, and only the chosen records deleted;
  - "Use the file's values" for each kind of drift.
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
