# Klyx Settings S2: Fleet

Opened 2026-10-26, on `feat/settings-s0`, after slice F was accepted (F §24). S2 is the redesign's Fleet slice (redesign §5, §14), and it builds:
- the cluster editor;
- protection through any input, with the interlock;
- reconnect and remove.

It applies every change through F's plans and policy registry.

This note is for Codex's review before it is built; revisions 1 to 3 the same day, after that review and two re-reviews (§9 to §11); the design is accepted (§12).

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
- The health Observer gains `Stop()`: its existing cancel. What it publishes after its identity is retired is dropped (§2.4). The end of its generation is delivered by the lifecycle itself, never left to the retired producer (§2.4).
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
- **The binding.** The Observer's identity is bound to its connection's `ConnID` explicitly, when the connection builds it: the Observer is given the `ConnID` as its incarnation. Two counters that both happen to grow are not the same identity. The check and the handoff are one step under the sink's lock, so no publication passes between "is this identity current" and "deliver it".

**The trusted end of a retired identity.** Dropping the retired producer's own final batch must not leave its generation open. So retiring an identity is an ordered, idempotent step of the lifecycle, before the swap:
1. the identity is marked retired, and its callbacks are dropped from then on;
2. the lifecycle delivers an end for that identity through the health sink: the same end a final batch carries, issued by the lifecycle, not by the producer. Each listener acts only on the generation it holds for that identity:
   - the notification engine closes the identity's episodes and drops its pending onsets, so nothing queued by the old connection is sent;
   - the index ends that generation's ownership;
   - the line detaches that attachment;
   - the rows lineage marks it final;
3. only then is the replacement swapped in and started.

Retained history is not deleted by this end; a reconnect forgets nothing by name. The end never acts on the replacement's state, which has another identity. Delivering it twice is a no-op. Late old final, down, up and data callbacks stay dropped: the new generation is never reopened to them.

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
- **The running entry's endpoint blocks are the active endpoint terms.** Today Observability's Test activates an endpoint on the connection alone (`SetMetricsConfig` and its siblings), so the startup copy can say an endpoint the connection no longer uses. In S2 every endpoint activation route publishes into the running entry as well: Test, a credential set, cleared or moved. It does so under the fleet lock, moving the running revision, with the credential revision it activated. The running entry and the connection then agree. A reconnect, Retry or Revert prepares its connection from those terms, read under the same lock, so it can never roll back an endpoint activated after startup. No endpoint is adopted from the file by this (§4.6), and the S3 editor is not built.
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

Applying a plan must change nothing when it is refused before the write: F §11, and Codex's ruling. F's `Begin` cannot be that gate, because it moves the epoch, and its `Publish` moves the epoch again even when protection is unchanged. So S2 adds a reservation to the policy registry, separate from the committed transition.

**The reservation is an overlay, not a state.** Each cluster's entry has its activation state (Active, Held, or absent: a name the running fleet does not have yet) and, separately, at most one reservation owned by the Apply that took it.
- **New admissions** need the activation state Active and no reservation: no ticket, no lease and no AI start otherwise.
- **What was admitted before** stays valid while only the reservation is added: under its epoch, with the activation state Active.
- **`Reserve(cluster)`** adds the overlay to any activation state:
  - Active, for an edit;
  - Held, for Retry or Revert;
  - absent, for a `connect` or an addition, which installs a placeholder that admits nothing and is published nowhere.

  It returns the owner token, and never changes the activation state or the epoch.
- **`Unreserve(token)`** removes the overlay and nothing else. A refused Retry on a Held cluster leaves it Held, with its reason, its epoch and its current attempt's `ConnID`, exactly as it was. A refused `connect` leaves the name absent, the placeholder gone.
- **The committed transitions** act on the activation state, inside the reservation, and the reservation is released last:

| Transition | Activation state | Epoch |
|---|---|---|
| `Advance(cluster, protected)`: a committed protection change | Active, with the new protection | moves once |
| `Hold(cluster, reason, attempt)`: a committed reconnect, Revert or Retry | Held, bound to that attempt's `ConnID` | moves |
| `Release(cluster, protected, attempt)`: that attempt's first usability | Active, if the hold is still bound to that attempt; otherwise nothing | moves |
| `Seed(cluster, protected)` over a placeholder: a committed `connect` or addition | Active, above any tombstone | starts |
| `Forget(cluster)`: a committed removal | absent, tombstoned | gone |

- **A usability during a reservation is not lost.** The held connection keeps retrying while another Apply holds the reservation, for instance a Revert being decided. Its first usability `Release`s the activation state as usual, bound to its attempt, exactly once, and moves the epoch. New admissions stay refused until the reservation is removed. The Apply holding the reservation then sees the epoch moved at its re-plan, and refuses with the new plan. Removing the reservation never restores an older snapshot over this release.
- **The reviewed baseline is the epoch before the reservation.** The re-plan compares the epoch the plan bound with the cluster's epoch, which `Reserve` did not move. Only a committed transition, or a release by the current attempt, moves it.
- **The work snapshot is final under the reservation.** No lease can be acquired while it is held, so the re-plan's list of work is the list the Apply ends.
- **The predicates are separated.** A new admission asks "Active and not reserved". Existing work asks "its epoch is the cluster's epoch, and the state is not Held". The current `epochProblem` serves both today and is split. That applies to tickets, AI read and send fences and A3 verification alike.
- **F's `Begin`, `Publish` and `Hold` keep their meaning** for any other caller. S2 uses the overlay and the transitions above.

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

**The commit gate.** Each cluster's lifecycle has a commit gate: one mutex that orders every decision about the cluster's activation.
- **Usability events pass through it.** A connection never calls into the policy registry from its own state machine. It queues its usability event (`ConnID`, not usable to usable) without blocking. The lifecycle applies the event under the gate: `Release` for a held attempt, or `ConnUp` for an Active cluster's recovery.
- **No lock order between the gate and a connection's own locks.** The gate is never taken inside a connection's lock. A connection's locks are never taken while the gate is held, except to retire or swap a connection whose callbacks are already queued.
- **The policy registry still never calls out** (F §1): the gate is the lifecycle's, not the registry's.

**Apply** for every route takes the same locks, in the same order: an edit, `connect`, an addition, a removal, Revert and Retry.
1. take the fleet Apply lock;
2. `Reserve` the cluster;
3. re-plan, and compare the digests. The plan binds the cluster's epoch and, while Held, the current attempt's `ConnID`;
4. check the interlock;
5. for a reconnect, Revert or Retry, prepare the connection from the running entry's terms (§2.6), outside the gate;
6. **enter the commit gate,** and decide there, finally:
   - the cluster's epoch and current attempt are still the ones the plan bound. A usability applied since, a release or a recovery, has moved them: the Apply leaves the gate and refuses with a fresh plan. The file is unchanged, the recovered connection stays current, and its release is recorded exactly once;
   - write, against the plan's file version;
   - commit by class (§4.2). For a reconnect, Revert or Retry, that is: retire the old identity, delivering its trusted end (§2.4); then `Hold`, bound to the prepared attempt's `ConnID`;
7. leave the gate; then swap in and start the prepared connection, and stop the retired one;
8. `Unreserve`;
9. release the fleet Apply lock.

**A usability that arrives after the decision is stale.** It was queued for the retired attempt, so the gate applies it after the decision, finds the hold bound to another attempt, and does nothing.

**A refusal at any step before the write** `Unreserve`s and releases the lock, and changes nothing. The activation state, its reason, its epoch and its attempt stay as they were. A legitimate release that happened meanwhile stays released: `Unreserve` removes only the overlay.

Endpoint activations take the fleet lock too (§2.6), so they never interleave with a preparation.

**A reconnect is held until its connection is usable.**
- After the write, the cluster is Held ("saved, not connected yet: connecting with context <ctx>"). The new connection is swapped in and starts.
- Nothing new is admitted while it is held: no ticket, no lease and no AI start, even though the connection's clients exist.
- **The release is bound to this attempt.** Only the first usability of the connection this attempt prepared releases the hold, identified by its `ConnID`. `Release` then publishes the new protection, and the new terms become the last terms that connected. It does so even during another Apply's reservation, as §4.1 says.
- **A stale attempt changes nothing.** A usability from an earlier attempt, or from a connection Revert or Retry has since replaced, is retired (§2.4), so it neither releases the hold nor replaces the last connected terms.
- **While held,** Settings says both states, and offers two ways on:
  - **Revert:** a plan back to the last terms that connected. It writes them, and prepares and swaps a connection of its own, held the same way;
  - **Retry:** a fresh connection for the saved terms, with a new identity.

  The connection's own retries continue meanwhile; the first usability of the current attempt is what releases.
- **At start,** a cluster whose connection failed to build is Held with that reason, and offers Retry (§2.2).

### 4.6 Use the file's values

**The field boundary.** "Use the file's values" adopts S2's fields only: context, kubeconfig, group, environment, tags and `protected`.
- An entry's endpoint blocks (metrics, Alertmanager and Loki) keep their existing route: Observability's Test applies them, as today, until S3. Endpoint drift stays reported where it is now.
- A reconnect's new connection takes the running entry's endpoint terms: the active ones, including any activated through Observability since startup (§2.6), never the file's. So adopting a context never silently adopts an endpoint, and never rolls one back.
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
- **The trusted end:** a retired connection's pending notification onset is dropped by the lifecycle's end, never sent while the replacement connects (Codex's probe). The end does not touch the replacement's generation, and delivering it twice is a no-op.
- **Endpoint terms:** an endpoint activated through Test after startup is in the running entry, and a later reconnect keeps it (Codex's probe). An activation racing a preparation is serialized by the fleet lock.
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
  - Held, then a Retry or Revert refused before its write: still Held, with its reason, epoch and attempt;
  - the current attempt's first usability during another Apply's reservation: released exactly once, and the reserving Apply refused with the new plan;
  - a Retry or Revert paused inside its preparation, after a successful re-plan, while the held attempt's first usability is delivered: refused with a fresh plan, the file unchanged, the recovered attempt still current, and its release recorded exactly once (Codex's probe);
  - the same usability delivered at the commit gate, while the decision holds it: applied after the decision, and stale;
  - an Active cluster's recovery during an edit's preparation: the edit refused with a fresh plan;
  - a refused `connect`: the name still absent;
  - existing AI reads, sends and verifications valid while reserved, and new starts refused;
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

## 10. Revision 2: Codex's re-review (2026-10-26)

Codex found R1 to R6 resolved as contracts, and requested changes: three residual P2, where the new reservation, retirement and endpoint rules meet.

| Finding | Change |
|---|---|
| **RR1, P2:** `Unreserve` meant "back to Active", so a refused Retry or Revert on a Held cluster would activate it. A usability during a reservation had no rule, and an absent cluster had no reservation | §4.1: the reservation is an overlay owned by its Apply, over an activation state it never changes: Active, Held, or absent with a placeholder. `Unreserve` removes the overlay only. A refused Retry stays Held with its reason, epoch and attempt, and a refused `connect` leaves the name absent. The current attempt's usability during a reservation is released exactly once, and the reserving Apply then refuses with the new plan. §4.5: one lock order for every route |
| **RR2, P2:** retirement dropped the retired producer's final batch, the only end of its generation, so its pending notifications could still fire | §2.4: retiring is an ordered, idempotent step. The identity is marked retired; then the lifecycle delivers the end of that identity's generation through the health sink, for each listener's generation of that identity only; then the replacement starts. Late producer callbacks stay dropped. The Observer's identity is bound to the `ConnID` explicitly, and the check and the handoff are one step |
| **RR3, P2:** the running entry's endpoint blocks were not the active endpoint terms. An endpoint activated through Test after startup lived only on the connection, so a reconnect would roll it back | §2.6: every endpoint activation route publishes into the running entry under the fleet lock, with its credential revision. A reconnect, Retry or Revert prepares from those active terms. No file endpoint is adopted by this |

**Also adopted, as acceptance details:**
- **Separate predicates for new and existing work.** Reserved preserves everything admitted before it: tickets, AI read and send fences, and A3 verification. So `epochProblem`'s single Active-only predicate is split into one for new admissions and one for existing work.
- **Continuing work keeps its outcome.** A continuing request or CLI child keeps its written or unverified outcome and releases its bookkeeping after retirement. Rejecting a retired lease's late resource never discards an outcome already sent.
- **Work through other connections is listed too.** A fleet comparison's members, and S3a's hosted sources later, depend on other clusters' connections. Their work is listed and ended under those connections as well as the home cluster. The existing read and send fences remain.
- **A kept credential is usable only under the checks it already has:** its origin, and its credential type.

## 11. Revision 3: Codex's second re-review (2026-10-26)

Codex found RR2 and RR3 resolved, and RR1's state rules resolved. It requested changes: one P2, in the order of RR1's concurrent usability.

| Finding | Change |
|---|---|
| **R2-1, P2:** the digest was compared before the preparation, and nothing ordered the write after it with a usability. So a held attempt that became usable during a Revert's preparation would be released, and then replaced by the stale Revert | §4.5: a commit gate per cluster, through which usability events pass. A connection queues its event without blocking, and the lifecycle applies it under the gate. Apply decides inside the gate, after its preparation: the bound epoch and attempt are still current, or it refuses with a fresh plan; then it writes and commits, retiring the old identity and rebinding the hold. A usability that wins first is kept and makes the Apply refuse. One that arrives after is for a retired attempt, and is stale |

**Also noted from the re-review:** a preparation combines the candidate's S2 fields with the running entry's active endpoint terms. Revert restores the last connected S2 fields, without reverting independently activated endpoints.

## 12. ACCEPT (2026-10-26)

Codex accepted the design at revision 3 (8a4a907), after the review of dc54655 and three re-reviews, with no outstanding findings. Its five rulings of §8 stand. It carried three implementation checkpoints, which are acceptance details for the build:
- **Queuing an event is not publishing usability.** F §1 requires a new incarnation to be published before usability is exposed, which `ClusterConn.moveLocked` does today. The queued route must keep that order. While the lifecycle's consumer is held pending, an old ticket or AI epoch must not reach a newly usable incarnation under the old epoch. Once the event is applied, the old pins must refuse.
- **"Stale after the decision" holds only after a committed retirement.** A failed write retires nothing, so its queued current-attempt usability is still applied, once, after the gate opens. A placement or protection change keeps its connection's identity, so a later recovery of that identity still publishes its epoch normally. The tests cover both, beside the stale retired attempt.
- **The gate takes no reentrant lifecycle callback.** Sink delivery during a trusted retirement, and a connection's start and stop, run outside the gate, and the tests run them under the race detector. A refused Apply releases both its gate and its reservation.

S2a builds next.

## 13. S2a as built (2026-10-26)

S2a is the cluster lifecycle, backend only. Nothing in the page changes yet, and S2b drives it from plans.

| Contract | Code |
|---|---|
| §2.1 a connection that stops | `fleet.ClusterConn`: its own context from `Start`, its loops tracked, its informer factories recorded and shut down. `Stop(timeout)` sets the terminal state `Stopped`, which never moves and is never usable. `Start` and `Stop` are serialized, so no loop is added once `Stop` is waiting. `observe.Observer.Stop` |
| §2.2 identities and the registry | `fleet.ConnID` is drawn from the Observer's process-wide counter (`observe.NextIncarnation`) and handed to the Observer (`observe.Config.Incarnation`). The registry offers `Prepare`, `Retire`, `Install`, `Drop`, `Retry`, `IsCurrent` and `IsRetired`. A retired identity is never current again, and the index order follows the running fleet |
| §2.3 leases | `appbridge/lifecycle_lease.go`: `Lifecycle.Acquire`, `Lease.Settle`, `Release`, `Pins` and `ClusterWork`, and `endLeases` on a retirement. The workers acquire through `UseLeases` |
| §2.4 retirement | `appbridge/lifecycle.go`: queued usability events are applied under each cluster's commit gate. `Reconnect` and `Remove` retire under the gate, then deliver the end outside it: the leases, then `HealthSink.EndIdentity` and `fleetindex.Index.End`. The health sink checks identity and delivers under a per-cluster handoff lock, and the index takes a connection's word only from its attached Observer (`SetConnectionOf`) |
| §2.5 forgetting | `policy.Registry.Forget`, with a tombstone that a later `Seed` starts above. Also `HealthSink.ForgetCluster` (the log, the lineage and every listener keeping a cluster: route and applied histories, the line tap, the notification engine), `fleetindex.Index.Forget`, `ProtectGuard.Forget` and `ForgetRunningCluster`. The line drops a removed cluster's history with its station |
| §2.6 the running fleet | `ConfigService.running` with its revision. `addMu` becomes the fleet Apply lock, `applyMu`. `WireEndpointActivation`: every endpoint activation runs under that lock and publishes into the running entry (`SetRunningEndpoint`) |
| §2.7 one protection reader | `SetFleetProtection`, `SetNotifyProtection`, and the Settings table through `ConfigService.IsProtected` |

**Where the build differs or adds:**
- **Usability, pending until published** (§12's first checkpoint):
  - A move into usable is pending until the lifecycle applies its event under the gate. Until then `Snapshot` reports the state before it.
  - The lifecycle publishes the incarnation (`policy.ConnUp`), then acknowledges the move (`ClusterConn.AckUsable`), and only then is it visible. An old ticket or AI pin therefore never reaches a newly usable connection under the old epoch.
  - A tool or test connection without a lifecycle has no hook, so its move is visible at once.
- **The lease API takes no context, and nothing returns one.**
  - A lease's context is its own root: cancelled at retirement unless the work continues.
  - This keeps F's typed guard scan whole:
    - a guarded method (`RunStep`, `StartDrain`, `HelmRollback`) acquires its lease without any context reaching it;
    - `admitCall` returns the `*Lease` rather than a context.
- **A lease's `End` is told why before its context is cancelled.** So a worker records the reason first, and its final chunk, status or turn says it.
- **What each worker does when its connection is retired:**
  - **A port-forward** stays listed as `ended`, with its reason, until it is stopped.
  - **A log stream** ends with "the stream ended: …" in its final chunk.
  - **Live events** send a status with `ended`.
  - **A conversation or investigation** ends explicitly:
    - its turn ends with the reason, never as stopped;
    - its offers are withdrawn, and the version moves;
    - a `chat:ended` event tells the page.
  - **Explain and Suggest-fix** are refused if the connection precheck found has been replaced.
  - **A confirmed A3 request, a drain and a Helm rollback** continue, with continuing leases. Their processes already run on their own contexts (the ticket's write context, or the drain stream's), so the lease lists them under Continues and keeps their outcome; it never cancels them.
  - **An A3 verification** ends with its connection, unverified.
- **Log history.** A page read takes a lease, so a read in flight ends with its connection. An idle view holds no connection and is not running work, so it is not listed. The lifecycle's end hook ends its page set, and its next page says why: "the log history view ended: …; read it again". Chat's history reads ride on the conversation's own lease.
- **One-shot reads keep their lookups:** event lists, inspector reads and describe. A lease covers the work §2.3 lists, the work that holds a connection.
- **`Lease.Pins`.** A worker that found its connection before acquiring its lease (Explain's precheck, a conversation's step) refuses when the two differ. A plain lease, outside any lifecycle, pins what its worker found, so tools and tests run as before.
- **Not yet used:** S2a's `Reconnect` and `Remove` are the primitives S2b's plans wrap in the reservation, the commit gate's final decision and the writes. In S2a only the tests drive them.

**Tests:**
- **The connection:**
  - a stopped connection leaves no goroutine above its starting count, with and without an Observer;
  - a connection stopped before it starts runs nothing;
  - the Observer carries the connection's identity, and a later connection's is above it;
  - a usability move is pending until acknowledged, a superseded acknowledgement is ignored, and a move out of usable clears it.
- **The registry:** retire, install, drop and retry, including installing over a current connection, dropping one, and retrying a running one, all refused. A removed identity never returns on a re-add.
- **The lifecycle:**
  - usability is published before it is seen, and a retired identity's queued move is never published;
  - Codex's re-review probe, end to end: a retired connection's pending onset is never sent;
  - a removal forgets the cluster in the policy registry, the guard, the registry, the index and the log, a late publication recreates nothing, and a re-add publishes;
  - the sink takes only the current identity, and delivers a trusted end once;
  - the index takes a word only from its Observer, ends only the retired incarnation's generation, and forgets.
- **Leases:**
  - Codex's design-review probe: a forward still opening cannot outlive its connection;
  - a settled forward, a log stream, live events and a conversation each end with their connection and say why;
  - a continuing lease runs to its end, while an ending one is told why before its context ends;
  - a drain and a Helm rollback are listed under Continues from their start, run through a reconnect, and leave the list when they finish;
  - an AI call is refused on a connection its precheck did not find, and an admitted one is listed and ends with its connection;
  - a lease is admitted only on an Active cluster's current connection.
- **The running fleet:**
  - it is one list with a revision;
  - Codex's re-review probe, inverted: an endpoint activated through Test is in the running entry, and an activation waits for the fleet lock;
  - every protection reader reads the published value, and the notification engine's protected scope follows it end to end.

**Mutation pass:** 37 of 37 caught. The 37 cover:
- the connection's stop and its pending usability;
- the registry's records;
- the policy tombstone;
- the lifecycle's publication, end and forgetting;
- the sink's identity check and single end;
- lease admission, cancellation, continuation and settling;
- each worker's end;
- endpoint publication and the fleet lock;
- the protection readers;
- the index's attached word, end and forgetting.

The first run left one survivor: the notification scope was tested only through its stored reader. Five more mutants did not compile at first. The end-to-end scope test, the continuing-work test and the AI pin test came from that run.

## 14. S2a revision 1: Codex's review (2026-10-26)

Codex requested changes at 50fa962: seven P2. It found the foundations sound:
- the identity source, the Observer's incarnation binding and the trusted end;
- the policy tombstone and the one protection reader;
- the lease API without a context, End before the cancel, and continuing leases;
- page leases for log history, and Reconnect and Remove as S2b's primitives.

The findings were where workers meet the lifecycle. Each is fixed, and its probe is a regression test (`lifecycle_review_test.go` in appbridge and fleet).

| Finding | Fix |
|---|---|
| **F1:** a chat's `End` read the lease before `Start` had it back, so a retirement in that window lost the end, and raced | `End` finds its work by an identity the worker made before acquiring, never by the lease. Chat binds it to the conversation. `Start` holds the service's lock from the lease to the registration, so an end in between waits, then finds the conversation registered, or refused. The lifecycle records why on the lease itself (`Lease.EndedWhy`), before its context is cancelled, so a log stream says why however the two interleave, with no `End` at all |
| **F2:** an old events `End` closed the subscription by key, so it could close a replacement opened on the same key | Each subscription is an entry that owns its lease and its subscription. `End` detaches that subscription only (`liveRegistry.detach`, compare-and-remove), and tells the page it ended under the registry's lock, so the notice never lands after a replacement opened. A subscription whose connection ended before it was recorded is refused, and the failed-Settle path closes its own subscription only |
| **F3:** comparison members held their connections without leases | Each member takes a lease on its own cluster, listed under that cluster's work, and retiring it ends the investigation explicitly. A replaced set, a refused one, Clear, an end and a removal release them |
| **F4:** a credential action captured the connection before it took the fleet lock | `activate` resolves the connection inside the fleet lock, runs the action on it, and publishes from that same connection. Set, Move and Remove all take it, so every credential action is serialized with an Apply |
| **F5:** a pending usability was hidden only from `Snapshot`, so the Observer and the index heard "connected" before the lifecycle published it | The Observer and index handoff reads the same visible state as `Snapshot`, with the reason from before the move. `AckUsable` reports whether the move became visible, and the lifecycle then calls `Published` after leaving the gate, so the handoff never runs under it |
| **F6:** a removal left ended conversations in their slots | A removal-only forget drops the cluster's conversations after their end is recorded, and releases their leases. A reconnect keeps them, ended, to read. A confirmed step keeps its own continuing lease |
| **F7:** endpoint credential revisions survived a removal, so an old reader stayed current after a re-add | A removal moves every endpoint kind's revision (`endpointcred.Registry.Retire`), without a revocation. Counters only move forward, and a kept keychain record stays usable under its own checks. It is not done at a reconnect, which builds its replacement before it retires the old connection |

**Also from the audit:** the two lifecycle hooks on bound services became plain package functions, so the webview cannot call them. They are the chat's forget (`ForgetChats`) and the log history views' end (`EndLogHistory`, formerly an exported method on the bound service).

**Tests:** Codex's seven probes, inverted. Each has a further case:
- an events `End` that runs before the subscription is recorded is refused;
- a refused comparison holds no member's lease;
- the lifecycle hands the published state to health, and only then;
- removing a credential waits for the fleet lock.

**Mutation pass:** 14 mutants of the fixes, 13 caught. The survivor releases a forgotten conversation's leases again. It is equivalent under the lifecycle, whose end has released them already, and is kept as a net for leases taken outside it.

## 15. S2a revision 2: Codex's re-review (2026-10-26)

Codex found F1, F5, F6 and F7 resolved, and F2, F3 and F4's original counterexamples resolved. It requested changes: three residual P2s, each where an admission meets its first publication.

| Finding | Fix |
|---|---|
| **RR1:** an events open its connection had already ended still listed and emitted once before its refusal, under a context its lease did not end | The subscription is named and bound before anything starts: an entry ended before binding starts nothing. An end while the subscription starts stops it at once, so it never registers, watches or computes, and a second, quiet detach takes it out if it registered meanwhile. Every delivery runs under the subscription's delivery lock, which `detach` also holds while it stops the subscription and sends its ended notice, so nothing is delivered after that notice. The list runs under the lease's context, so a read in flight ends with its connection |
| **RR2:** a comparison member whose end was selected before `SetComparison` refused could still end the investigation | Each member's admission is made before its lease, and its end acts on the investigation only while that admission is committed to the investigation's set. A set with a member retired while it was chosen is refused. A refused set never commits, and a replaced or released member is uncommitted, so their ends act on nothing. A committed member's retirement still ends the investigation explicitly |
| **RR3:** MoveToKeychain wrote the keychain and fleet.yaml before it took the fleet lock, and bumped the revision after it | The whole transfer runs under the fleet lock, in order: the keychain write, the file's compare-and-swap, the live connection and the revision. A partial transfer says what happened and publishes nothing. The lock order is unchanged: endpoints, then the fleet Apply lock, then the file writer |

**Tests:**
- Codex's three probes, as regression tests. Each also has a case of its own:
  - an end while the subscription starts, and one just before it registers;
  - a stopped subscription never starts or replaces another;
  - a detached subscription delivers nothing more;
  - a read in flight ends with its connection;
  - a member retired while chosen, or replaced while its end is in flight, never ends the investigation;
  - the token move changes nothing, its revision included, before it holds the lock.
- The live registry gained one test seam, told the stage before a subscription registers and before its watch starts. It is nil in production.

**Mutation pass:** all 12 mutants of the fixes are caught.

## 16. S2a revision 3: Codex's second re-review (2026-10-26)

Codex found RR2 and RR3 resolved, and RR1's original scenario resolved. It requested changes: one P2 and one P3, both in the events integration.

| Finding | Fix |
|---|---|
| **R2-1, P2:** a newer open replaced an older one that was still starting by releasing its lease only. The older open could then register over its successor, report OK, and outlive the connection's retirement, untracked | Taking an entry's place invalidates the entry first: it is marked, its own subscription is stopped and detached (never another's), and only then is its lease released. That covers a replacement, a close, a shutdown, a retirement and a failed Settle. An open still starting finds itself marked, or its subscription stopped, and refuses. `bind` also refuses an entry that no longer holds its place |
| **R2-2, P3:** the panic net's not-live edge bypassed the delivery fence | It is delivered through the fence like every other delivery, and the old watch still stops |

**Tests:**
- Codex's two probes, as regression tests.
- A close while an open is still starting stops that open too.

**Mutation pass:** 5 mutants of the fixes, 4 caught. The survivor is `bind`'s ownership check. Every path that takes an entry's place marks it, and the stop checks after `bind` refuse it anyway, so the check only narrows a window. It is kept.

## 17. S2a revision 4: Codex's third re-review (2026-10-26)

Codex found R2-2 resolved, and R2-1's registration window fixed. It requested changes: two P2s in the window between an events open taking its lease and taking its place. That window existed because an open became visible only once its lease returned. Rather than close each window in turn, revision 4 removes that window from the design.

| Finding | Fix |
|---|---|
| **R3-1:** an open retired while taking its lease could, returning late, take the place of a replacement already running on the new connection. A close while an open was still taking its lease found nothing to stop | Every open draws its place in the key's order of opens before it takes its lease (`begin`). It adopts its lease in one step under the entry lock (`adopt`), and only while it has not ended and is still the newest open; otherwise it takes nothing, releases its own lease, and leaves the current entry untouched. A close moves the order on, so an open still taking its lease loses; so does shutdown. A newer open that is refused at admission leaves the working subscription running |
| **R3-2:** `invalidate` read the entry's lease outside the entry lock, while the open published it outside the lock too | An entry's lease is written only by `adopt` and read only by `invalidate`, both under the entry lock; the release happens outside it. An end that comes before the lease is adopted marks the entry, and the open, finding it ended, releases its own lease |

**Tests:**
- Codex's three probes, as regression tests: the early end against the adoption, under the race detector; a late retired open against the replacement; a close while an open takes its lease.
- One case of its own: a reopen refused at admission keeps the working subscription.

**Mutation pass:** 4 mutants of the fixes, all caught.

## 18. S2a revision 5: Codex's fourth re-review (2026-10-26)

Codex found R3-1 and R3-2 resolved: the order of opens and the atomic adoption close the handoff. It requested changes: one P2 in the closes.

| Finding | Fix |
|---|---|
| **R4-1:** `CloseLiveEvents` and `CloseAll` ended with a sweep of the key, or of the whole registry. Neither sweep was bound to the entries the close had captured, so an open admitted while the close finished could be stopped, while its entry stayed current and its lease held | `CloseLiveEvents` stops only what it captured: invalidating the entry already stops and detaches that entry's own subscription, so the key-wide sweep is gone, and with it the registry's key-wide `close`. An open admitted after it keeps running. `CloseAll`, at shutdown, marks the service closed in the same step as its snapshot, so nothing more is admitted, and its sweep can stop only what it closes |

**Tests:** Codex's probe, for both closes, as a regression test. **Mutation pass:** 2 mutants, both caught.

## 19. ACCEPT: S2a (2026-10-26)

Codex accepted S2a at revision 5 (3c08253), with no remaining findings. That came after the review of 50fa962 (seven P2) and five re-reviews (§14-§18).

It checked the shutdown path with a probe of its own:
- an adopted subscription drains;
- an acquisition still returning is refused and releases its lease;
- later opens on old and new keys are refused before any lease is taken;
- a repeated `CloseAll` stays closed.

The earlier fixes (the lease handoff, member admissions, the token transfer and the delivery fence) remain supported by their regression tests.

S2a was merged to `main` in #57 at revision 4, at the owner's request; revision 5 follows in #58. S2b builds next.

## 20. S2b as built (2026-10-26)

S2b is the writers and the plans, backend only. The page is S2c. It was built in four commits, with a fifth for its last tests. It was built on S2a while S2a's reviews ran, then rebased onto each revision.

| Contract | Code |
|---|---|
| §3.1 editing an entry | `config.EditClusterAt(path, version, name, ClusterEdit)` (`internal/config/clusterwrite.go`). It writes the changed S2 fields only, line by line, finding them in the node tree. A value is replaced on its own line, keeping its key, indentation, trailing comment and line ending. An absent field is inserted after the entry's last one-line key, and a cleared field's line goes. Tags are rewritten as one block in Klyx's form, sorted. A value is quoted when YAML would not read it back as the same string |
| §3.1 refusals | Each is a `HandEditError`, saying to edit the entry by hand and why: a flow list or entry, an anchor, an alias, a merge key, a changed value written over several lines, inline tags, and a key sharing the list dash's line that would have to go. A duplicated key, and tags that are not a mapping, do not load, so they are refused as a file that does not load. The error never echoes a value |
| §3.1 the candidate | It must load strictly. Decoded, every top-level key and every other cluster is unchanged, and the entry reads back as intended. It is written against the plan's file version through slice 0's policy: the symlink, the mode and the compare-and-swap |
| §3.2 removing an entry | `config.RemoveClusterAt`: the entry's lines from its dash to its last line of content go, with the comments indented inside it. A comment above it, or one at the dash's column after it, stays. One blank line is kept between items. The last cluster is refused (`ErrLastCluster`) |
| §4.1 the overlay | `policy.Registry`: `Reserve` over Active, Held or absent (a placeholder that admits nothing and is published nowhere), `Unreserve`, `Advance`, `HoldAttempt` (bound to the attempt's identity, with the protection its release publishes), `ReleaseAttempt` (exactly once, during another reservation too), and `Seed` over a placeholder. F's own steps drop an attempt binding |
| §4.1 the split predicates | New admissions (a guard, an AI start, a lease) refuse while a reservation is held (`notAdmitting`). Existing work keeps `notActive` and its epoch |
| §4.5 the commit gate's decision | `Lifecycle.Decide` refuses with `ErrMoved` unless the epoch and the held attempt are still the ones the plan bound. Then it runs the write and the class's transition under the gate. `Switch` is the reconnect class: write, hold for the prepared attempt, retire; then, after the gate, end the retired identity, install and start the attempt, stop the old. `Unseat` is the removal class |
| §4.5 usability | A held attempt's first usability releases it under the gate (`ReleaseAttempt`). `OnReleased` records it after the gate: the attempt's terms become the last connected ones |
| §4.5 at start | `HoldBuildFailures`: a cluster whose connection failed to build starts Held, with why |
| §4.2 plans | `ConfigService.PlanClusterEdit`, `PlanClusterFromFile` (`file`, or `connect` for a cluster not running), `PlanClusterRemove` (with the chosen keychain kinds), `PlanClusterRevert` and `PlanClusterRetry`, then `ApplyClusterPlan(digest, typed)` (`fleet_plan.go`, `fleet_apply.go`). `PlanDTO` gains `Cluster`, `Class` and `Records` |
| §4.2 one Apply at a time | `AddClusters` reserves each name, seeds it over its placeholder and unreserves last; a failed connect forgets the name. Every plan's Apply takes the fleet Apply lock, and a removal with chosen records takes the credential lock first. Each re-plan binds the file version, the running fleet's revision, the epoch, the held attempt, the work identities and the records' revisions |

**Where the build differs or adds:**
- **Effective in, raw out.** The running fleet holds effective entries: the context defaulted, and the tags a context derives. So a plan compares effective entries to class its change and show it, and writes the file's raw entry: only the fields the person changed, against the file's own values. Otherwise a save would write derived tags into fleet.yaml. `config.LoadRaw` reads both, `ClusterConfig.Effective` applies the defaults, and `ProtectedBy` names the input that decides protection.
- **A save is refused while the file differs:** "use the file's values first, or undo that edit". F's block condition, per entry.
- **What a protection change ends.** The work pinned to the epoch: the cluster's conversations and investigations, those comparing it, and its Explain and Suggest-fix calls. They are listed in the plan and ended explicitly at the Apply (`EndChatsOnPolicy`). Other work runs on.
- **The last terms that connected.** They are kept per cluster: the terms running at a reconnect, when the cluster was Active, and then each released attempt's terms. Revert writes them back, and is refused while nothing has connected under other terms this session.
- **Retry.** It is planned from the saved terms, writes nothing, and goes through `Switch`. For a cluster whose build failed, the attempt is installed into the failed slot.
- **Keychain choices.** `EndpointRecords` lists the records present, with their revisions. The chosen ones are deleted under the credential lock (`EndpointCredLock`, `DeleteEndpointRecord`: revoked, then deleted), after the cluster is removed. A failure is said in the result's note, record by record.
- **Hooks for later stages.** S3a fills the host check (`FleetPlanDeps.Hosts`); in S2 it is nil, so there are no hosts. After a commit, the line is told (`Changed`).
- **Test seams.** `fleetPlans.beforeDecide` runs just before the class's decision. It is nil in production.

**Tests:**
- **The writers:**
  - exact output for comments, nested blocks, CRLF, document markers and a file without a final newline;
  - quoting;
  - each refusal;
  - an unchanged multi-line field left alone;
  - a cleared field taking its default;
  - the version, the compare-and-swap, the mode and the symlink;
  - removals of the first, a middle and the last item, blank-separated, with the comments inside going;
  - the candidate checks themselves.
- **The overlay:**
  - reservation without an epoch move;
  - a Held cluster unreserved still Held;
  - placeholders;
  - `Advance` and `HoldAttempt` under the token;
  - the bound attempt's release exactly once, during another reservation;
  - new work refused, and existing work kept, while reserved.
- **The decision:**
  - a moved activation refused, a forgotten cluster too;
  - the release during another Apply's reservation;
  - a usability at the gate, stale after a committed retirement and applied once after a failed write;
  - an identity kept by a protection change;
  - the removal flow, the addition's reservation, and a build failure held until a Retry connects it.
- **The plans:**
  - a placement keeping its epoch;
  - tightening without an interlock, which ends the conversations, invalidates a ticket from before it and moves the epoch once;
  - loosening with its interlock;
  - the name's protection that cannot be loosened;
  - a context that cannot resolve;
  - a reconnect held until its own connection is usable;
  - Revert to the last connected terms, which follow each release;
  - a refused Retry still Held;
  - Codex's design probe: a release during a Retry's preparation;
  - a removal with its interlock and only its chosen records, a failed deletion said, a removal refused at its decision deleting nothing;
  - a record bound to its revision;
  - a save refused over a hand edit;
  - "Use the file's values": adopting S2's fields with the running endpoint kept, connecting a file-only cluster, removing one gone from the file;
  - two clusters' Applies keeping each other;
  - the digest moving when work starts;
  - an arm not surviving a reconnect.

**Mutation pass:** writers 20 of 20, overlay and admission 18 of 18, decision 12 of 12, plans 20 of 20.

## 21. S2b revision 1: Codex's review (2026-10-26)

Codex requested changes at c155378: seven P2 and one P3. It found sound:
- the overlay and the split predicates;
- `Switch`'s bound hold;
- the writers;
- effective comparison with raw writes;
- the order of the keychain choices;
- a protection change ending its conversations outside the gate;
- the nil hosts hook and the decision seam.

Each finding is fixed, and its probe is a regression test (`fleet_review_test.go`).

| Finding | Fix |
|---|---|
| **R1:** a plan that writes nothing (file adoption, Connect, Retry, a file-driven removal) did not check its file version at the final decision | The write step checks the plan's file version under the gate even when it writes nothing, and an addition checks it before its seed. A file moved since the re-plan refuses with the new plan, having changed nothing |
| **R2:** a committed removal left earlier tickets valid until its cleanup finished | `Unseat`, and S2a's `Remove`, forget the cluster in the policy registry under the gate, with the retirement. A ticket or fence admitted before is refused at once, before any callback can block |
| **R3:** a protection edit while Held wrote the file and the running entry, then failed | It is refused at plan time: "saved, not connected yet: change its protection once it connects, or Revert or Retry first" |
| **R4:** the removal and loosening interlocks read the pending entry, not the published protection | The published protection is the baseline: for the class, both interlocks, and protection's effect line ("on, until the pending connection is usable") |
| **R5:** Revert and Retry recorded the file's entry as terms that connected | Raw terms are kept by connection identity: the startup connections' from fleet.yaml as Klyx started, and each new attempt's when it commits. A connection's terms become the last connected ones only when its usability is published (`Lifecycle.OnPublished`). Retry connects with the saved terms that run, never a hand edit made since |
| **R6:** `AddClusters` wrote before preparing | Each connection is prepared before the file is written, so one that cannot be built adds nothing. Each addition then commits under its cluster's gate, and its connection is installed |
| **R7:** writer refusals echoed anchor and alias names | A refusal names the structure and its line, never the name |
| **R8 (P3):** Connect did not name its endpoints | A Connect's plan names the endpoint kinds the entry configures, without secrets |

**Tests:**
- Codex's nine probes, as regression tests.
- An addition re-checking the file at its decision.
- While Held, the published protection deciding the class, the interlock and the effect line.
- Revert's test now has its startup connection connect first: terms count as connected only once their connection's usability is published.

**Mutation pass:** 13 mutants of the fixes, all caught.

## 22. S2b revision 2: Codex's re-review (2026-10-26)

Codex found R1, R2, R7 and R8 closed, and R3 to R6's original probes passing. It requested changes: four residual P2s, in the Held classification, the connection terms' bookkeeping, and the addition's admission.

| Finding | Fix |
|---|---|
| **RR1:** while Held, an edit whose protection equalled the published value, but not the pending attempt's, was classed as placement. The hold's release then published the pending value, undoing the edit | While Held, any edit that is not a reconnect is refused when its protection differs from either the published value or the pending attempt's, before anything is written |
| **RR2:** a startup connection could publish its usability before the plans subscribed, so its terms were never remembered | Wiring seeds each startup connection's terms, subscribes, then records any startup connection already published, so there is no gap. A startup connection's terms are its file entry only while that entry still says what started, never a hand edit made since |
| **RR3:** a placement or protection change on an existing connection left its tracked terms stale, so Retry recorded terms other than those it built | Every committed placement or protection change updates its connection's terms and the cluster's current terms. It updates the last connected terms too, when that connection is the one that connected |
| **RR4:** `AddClusters` found a name already running only after writing it | Every name is checked before anything is prepared or written. A name that runs, or is published, is refused, and the file's values remove it |

**Tests:** Codex's four probes, as regression tests, and an edit on the connected identity keeping its last connected terms. **Mutation pass:** 6 mutants, all caught.

## 23. S2b revision 3: Codex's second re-review (2026-10-26)

Codex found RR1, RR2 and RR4 closed, and RR3 correct for a cluster with a connection. It requested changes: one residual P2.

| Finding | Fix |
|---|---|
| **R2-1:** a cluster whose first build failed has no connection. A placement edit committed then updated nothing, so Retry built the edit's terms but recorded the startup ones, and a later Revert undid the edit | Every committed placement or protection change updates the cluster's current terms, connection or not. Its connection's terms, and the last connected terms, change only when a connection exists, the latter only for the connection that connected: an edit before any connection invents no history |

§22's RR3 line holds with that correction: the current terms follow every committed change, not only those on a connection.

**Tests:** Codex's probe, as a regression test covering a save and a file adoption before the first build: the edit is Retry's terms, no connected terms exist until Retry connects, and Revert restores the connection that succeeded. **Mutation pass:** 5 mutants, 4 caught. The survivor, dropping the return after an edit on a connection, is equivalent: the current terms are then written twice with the same value. No native smoke for this revision alone: the change is bookkeeping on paths the unit tests drive, and the S2c smoke runs on top of it.
