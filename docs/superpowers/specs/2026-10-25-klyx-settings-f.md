# Klyx Settings F: the activation foundation

Opened 2026-10-25, on `feat/settings-s0`, after S1's implementation was accepted (S1 §15). F is the redesign's foundation slice (redesign §14). It builds the contracts S2, S3 and S4 apply changes through:
- plans from the running state;
- per-cluster policy epochs in the guard and in every write's and send's fence;
- the invalidation of arms, reviews and offers;
- saved-not-active states.

It is built in two stages under this note:
- **F1, the fences:** the policy registry, tickets, the 21 guarded verbs, A3 and the AI fences. Backend only.
- **F2, the plans:** Apply for the three blocks Settings edits today (AI, notifications, Git) and for the AI credential actions, "Use the file's values", and saved-not-active.

This note is for Codex's review before it is built; revision 1 the same day, after that review (§10).

## 0. What exists

From a read of the code at 8ad6f36:
- **The guard** (`appbridge/protectguard.go`):
  - one-shot grants keyed by the cluster name alone, for 30 seconds;
  - `Arm` judges protection at arm time and records nothing for an unprotected cluster;
  - `Check` consumes the grant on a protected cluster, pass or fail, and passes an unprotected one without consuming anything.

  It has 21 callers, each calling `checkProtected` first, before its lookup and the reads that precede its write. They are listed in §2.
- **No write is fenced on policy.** A manual verb's write context carries no read fence; only object UID preconditions guard the write. The one run fence is A3's `runFence`, which checks the pinned connection pointer and its usability, and by its own comment not the AI settings.
- **There is no connection incarnation.** A connection is its `*ClusterConn` pointer, which the registry never replaces. On the initial connection's retry path, Failed→Synced heals in place, so a pointer comparison passes again afterwards. An ordinary watch failure after a sync goes Synced→Stale, and Stale counts as usable.
- **Protection never changes at run time,** except that `AddClusters` adds a cluster. The running configuration is the startup snapshot plus those additions. `IsProtected` is recomputed on every call, and nothing caches the boolean.
- **A3:**
  - `RunStep` decides protection at entry, and the AI revision under its first lock, and re-checks neither before the write;
  - `ReviewStep` never sees protection;
  - a review lasts 2 minutes, a grant 30 seconds.
- **AI:**
  - The settings revision is global (`AIService.rev`). The chat and investigation turn guard checks it with the connection.
  - Explain and Suggest-fix check protection once and never again.
  - The AI HTTP client (`ai.Harden`) does not evaluate a read fence, so a redirect or an SDK retry is not re-checked.
  - The Claude Code CLI re-checks its own admission at `release`, before the prompt goes on stdin, but not the cluster's policy.
- **The three editable blocks.** Every writer uses slice 0's write policy: the writer lock, the unchanged-file replacement, and the mode and symlink rules. What differs is the block-level condition before the write:

  | Block | Block condition before the write | Running copy | Drift | Adopting the file |
  |---|---|---|---|---|
  | AI | none (`pre=nil`); its egress interlock compares with the file, not with what runs | `s.applied` | "diverged" | Save writes the draft, which starts from the file |
  | notifications | refuses if the file's block is not the running one | initialized at start, updated by `SetSettings` | none | a restart |
  | Git | none (`pre=nil`) | none: every use re-reads the file, so a hand edit applies at once | none | not needed |

- **The AI credential actions** (`SetAPIKey`, `ClearAPIKey`, `MoveKeyToKeychain`) read the current file and activate through the same `apply` as Save. Today `SetAPIKey` therefore activates a hand-edited egress grant without its interlock (Codex's probe, §10 R5).
- **Writes Klyx cannot fence once started:**
  - a drain, a kubectl child, which `CancelDrain` stops but cannot retract;
  - a Helm rollback, a helm child with no Klyx deadline;
  - the debug terminal, which Klyx no longer controls once the terminal opens.

## 1. The policy registry

`internal/policy` is the single published source of each cluster's admission state. For each cluster it holds a **snapshot**:

```go
type Snapshot struct {
	Protected bool     // effective protection, from every input
	Epoch     Epoch    // opaque; compared only for equality
	State     State    // Active, Applying or Held (with its reason)
}
```

**One coherent state (R1).** Every admission reads one snapshot, under one lock: the guard, a ticket's check, an AI start, read or send, and an A3 offer, review or run.
- No admission reads protection from `ConfigService` and an epoch from somewhere else. `ConfigService.IsProtected` returns the published snapshot's value.
- Protection is computed from the running configuration only when it is published: at start for every cluster, by `AddClusters` for a new one, and by S2's Apply.

**The epoch** has two parts, and moves when either does:
- **Policy:** advanced by every publication that changes the snapshot. In S3 that includes a subscription feeding the cluster, and in S4 its local-only policy.
- **Connection:** a new incarnation counter on `fleet.ClusterConn`. It advances each time the connection becomes usable from not usable: Connecting→Synced, the retry path's Failed→Synced, and, in S2, a replaced connection.
  - Usable is today's meaning (Synced, Degraded or Stale). Degraded↔Synced and Stale→Synced leave the counter alone, since the connection never stopped being usable.
  - A blip that only takes the connection Stale therefore needs no re-confirmation. A failure that takes it to Failed does.
  - The new incarnation is published before usability is exposed. A replacement connection starts above its predecessor, so an incarnation can never recur.

**Publication is a transaction.** Changing a cluster's policy goes:
1. `Begin(cluster)`: the state becomes Applying and the epoch moves.
2. `Publish(cluster, protected)`: the state becomes Active and the epoch moves again.

`Hold(cluster, reason)` leaves it Held, and the epoch moves. Release always moves the epoch too. So:
- No ticket can be obtained while a cluster is Applying or Held.
- A ticket, arm, review or offer captured before or during the change is stale after it.
- Releasing a hold never revives anything old.
- A guard that runs between `Begin` and `Publish` is refused, so it cannot pair the old protection with the new epoch: R1's probe.

**Locks.** The registry has one mutex, never held while calling out. The guard holds its own lock and reads the registry under it; the registry never takes the guard's lock. Grants are invalidated lazily: each records the epoch it was armed under, and fails at `Check` when the epoch has moved. A publication therefore never reaches into the guard, and the lock order cannot invert.

**A zero or unknown ticket fails closed.** A cluster the registry does not know has no snapshot, and nothing is admitted for it.

## 2. Tickets: the guard captures, the write checks

`checkProtected(cluster) error` is replaced by `guard(cluster) (Ticket, error)`, reading one snapshot:
- **Not Active** (Applying or Held): refused.
- **Protected:** it consumes the one-shot grant, pass or fail, as today. It refuses when the grant is missing, expired, or was armed under another epoch.
- **Unprotected:** it passes, and the ticket still captures the epoch. A cluster that becomes protected before the write is refused at the write: the unprotected pass of §17a.

`Arm(cluster, typed)` records the epoch with the expiry. One change to today's behaviour: a mismatched typed confirmation now clears an existing grant, as `ArmAIEgress` already does.

**The fence.** `Ticket.Valid()` returns nil while the cluster's snapshot is Active, under the captured epoch. Otherwise it returns an error naming what moved: "aks-we-prd's protection changed since this was confirmed", or "the connection to aks-we-prd restarted since this was confirmed".

**Forwarding evidence is per logical write (R3).** A ticket can cover several writes, but each logical write gets its own forwarding tracker, kept across that write's retries and redirects. A verb obtains a write context only through `t.Write(ctx)`, which puts `readfence.With(ctx, t.Valid)` and a fresh `readfence.WithWriteTracker` on it. That applies to verbs without business receipts as well. Each request then checks the fence: the reads before the write, and every attempt of the write itself. A refusal is classified per write:
- refused before any attempt of that write was forwarded: **not sent**;
- refused after an earlier attempt of the same write was forwarded (a lost answer, a retry): **unverified**, never "not sent".

A multi-write operation reports each write's outcome, the first write's included. It never folds them into one claim that nothing was sent. The operations are `ReconcileWithSource`, a multi-document create, and an Argo sync.

**CLI writes** (drain, Helm rollback, debug terminal):
- `Valid` is checked at the actual child start, after any queued preparation. For the debug terminal that is the terminal handoff; for a drain, the kubectl start after `CheckNodeArmed`.
- A started child is not stopped by a move, since it is already forwarded (§17a). A started child is not proof that the server accepted the write or that it completed. No automatic retry or new child may reuse an invalid ticket.
- `CancelDrain` remains available. S2's plans name a running drain or rollback on the cluster as continuing.

**The 21 guarded methods,** each converted and each in the coverage table:

| Group | Methods |
|---|---|
| Argo | `RefreshApp`, `SyncApp` (a real sync; a dry run takes no ticket) |
| A3 | `RunStep` |
| CRD | `ApplyResourceYAML`, `CreateResourceYAML` (multi-write), `DeleteAdhocObject` |
| exec | `OpenDebugTerminal` (CLI, a detached terminal) |
| Flux | `Reconcile`, `ReconcileWithSource` (multi-write), `SetSuspend`, `FluxOpAct` |
| Helm | `HelmRollback` (CLI) |
| KEDA | `SetPaused` |
| nodes | `Cordon`, `StartDrain` (CLI) |
| pods | `LaunchDiagnosticPod`, `DeletePod` |
| workloads | `ScaleWorkload`, `RolloutUndo`, `RolloutPause`, `RolloutRestart` |

The ungated paths stay ungated: the exec terminal, port-forward, dry-runs, `ReviewStep` and `flux diff`.

**Coverage.**
- `checkProtected` is removed, so a verb cannot compile without the ticket.
- A Go AST test fails on any of these, each with a negative fixture:
  - a ticket not passed to `t.Write` or `t.StartCLI`;
  - a fenced context made but an unfenced one forwarded;
  - a second write outside the fenced path;
  - an asynchronous child start after preparation that skips the check.
- The table test drives every method of the table against fakes. It covers:
  - a move between the guard and the write: refused and not sent;
  - an unprotected pass refused after a tightening;
  - a stale arm after a Failed recovery: refused;
  - a Stale recovery that keeps the ticket;
  - an Applying or Held cluster: refused;
  - a lost-answer retry: unverified.

## 3. A3: offers, reviews and runs

Each of these reads the cluster's snapshot (§1) as well as its own fences:
- **Offers:** a plan records each target's epoch when it is offered. `planFenceLocked` withdraws it when one moves or the cluster is not Active ("aks-we-prd's policy changed"), sticky like its other fences. A withdrawn offer is never revived.
- **Reviews:** a review token records the epoch. `RunStep` refuses a review made under another epoch, so a new review is needed, as for a changed digest.
- **Runs:** `RunStep` takes its ticket at entry; the grant is consumed there, on refused paths too. `runFence` checks `ticket.Valid` with the pinned connection, before and during the write, through the write context of §2. A tightening between the guard and the write is refused at the write, and the epoch check spends no second grant (§17a).
- **Verification:** `verifyFence` checks the epoch as well. A move after the write ends verification as unverified, and the write keeps its outcome.

## 4. AI fences: every send

**Starts.** A conversation, an Explain or a Suggest-fix starts only on an Active cluster. It pins the cluster's epoch beside the settings revision; a fleet comparison pins each member's.

**Every send (R2).** The fence goes to every point where cluster context can leave the machine, not to the provider's entry:
- **HTTP:** `ai.Harden` installs `readfence.Transport` on every provider's client. The fence on the call's context is therefore checked on each HTTP attempt, a permitted same-host redirect and an SDK retry included.
- **The CLI:** the read fence is checked inside the CLI's existing `release`, immediately before the prompt is written to stdin, after preflight. A move during preflight sends nothing.
- **Evidence reads:** Explain and Suggest-fix carry the same fence on their cluster reads, as chat already does, so a read after a move is refused.
- **Uncertainty kept:** if an attempt of a send was already forwarded before a later attempt was refused, the result says the request may have been sent, never that it was not.

**What the fence checks:** the settings revision, and each pinned cluster's snapshot (Active, under its epoch). For a comparison that is every member whose evidence the call carries. A move ends a turn as today's revision change does, sticky.

## 5. Plans (stage F2)

**What a plan is:**

```go
type PlanDTO struct {
	Section     string            // ai | notifications | git | ai-credential (S2 adds clusters, S3 sources, S4 profiles)
	Digest      string            // over everything below that the person reviews
	FileVersion string            // the fleet file's content hash when planned
	Runtime     map[string]string // the revisions it was planned from: the AI revision, each affected cluster's epoch, the work version
	Changes     []PlanChange      // per registry id: from and to, as shown; never a secret, only its state
	Ends        []PlanWork        // the work it ends, each with its identity: "an investigation on aks-we-prd (started 10:42)"
	Continues   []PlanWork        // what it does not stop (S2: a running drain)
	Interlock   *Interlock        // the phrase and why, when §1.5 of the redesign requires one
	Writes      bool              // false for "Use the file's values": the file already says so
	Refused     string            // a candidate that cannot be applied, and why; nothing is written
}
```

**Planned from what runs.** Each section has a running copy, and the plan compares the candidate with it, never the file with the file:
- **AI:** `s.applied`, as today. The egress interlock compares with the running grant, which fixes §0's issue: "Use the file's values" turning egress on takes the interlock, even though the file already says so.
- **Notifications:** its running copy, as today.
- **Git:** gets a running copy, a disclosed behaviour change:
  - It is initialized from the startup configuration, not from a second read.
  - Every consumer uses it: the settings view, the line, the cache diff, "not here yet", chat's Git changes, and background fetches.
  - A hand edit of `git.repos`, an approval or a revocation, shows as drift and applies with "Use the file's values" or at a restart.

A candidate that cannot be built (an AI provider that does not build) is refused at plan time, and nothing is written, as today.

**Bound consequences (R6).** The digest covers each part of the plan, so the confirmation binds to the consequences the person reviewed:
- the changes;
- what ends and what continues, by identity;
- the interlock;
- the file version;
- the runtime revisions, including a **work version** that the chat service advances whenever a turn, an investigation or an offer starts or ends.

Apply runs under the section's Applying state. New work for it is refused there: an AI turn or offer cannot start while an AI Apply decides. Apply then re-plans and compares digests. A new turn started in another window between the plan and Apply changes the work version, so Apply refuses and returns the new plan.

**Confirming:**
- `Apply(digest, typed)` re-plans from what runs now, with the candidate the backend holds for that digest.
  - The candidate cache is bounded: 16 entries and 1 MiB, expiring after 5 minutes, and consumed by a successful Apply. It holds no secret.
  - A digest is not proof that a plan is still eligible; only the re-plan is.
- If the digest differs, because the file, a revision, an epoch or the affected work moved while the dialog was open, it refuses and returns the new plan.
  - A replacement plan never inherits the confirmation or the typed phrase.
  - The frontend keys its confirmation state by digest, so a replan clears it, and nothing applies a replacement automatically.
- Applies are serialized per section, so an older Apply can never publish after a newer one.

**Apply in order** (redesign §4), inside the section's Applying state:
1. Write the block through slice 0's write policy. The write also checks that the file's content is still the version planned from, so a hand edit made while the plan was open refuses the Apply. AI and Git gain the block condition notifications already has. "Use the file's values" writes nothing, but still checks the file version.
2. Advance what it changes:
   - the AI revision, which ends turns and withdraws offers;
   - the affected clusters' epochs, through §1's publication (none in F2).
3. Invalidate what was armed under the old terms: the AI egress grant, and the affected clusters' grants, lazily, by epoch.
4. Activate: build and publish the AI state, set the notification engine, swap Git's running copy. Then leave Applying.

**When activation fails after the write:** the section is saved, not active, and says both states.
- Its work stays fenced. AI is unavailable everywhere with that reason, its revision having moved. It never resumes under the old or the new terms.
- For AI this is rare, since the candidate built at plan time, but a keychain failure between plan and activation can do it.

**Retry and Revert (R4):**
- **Retry** retries the exact failed candidate, with its bound file version and terms. It succeeds only while both are unchanged; identical terms need no second confirmation.
  - If the file or the runtime state moved since, Retry is refused and returns a fresh plan from the current file, with that plan's own interlocks. Retry never activates changed file values under the earlier confirmation.
- **Revert** is a plan from the current state back to the last active terms. It cannot resurrect a superseded activation.

**"Use the file's values":** where a section's file block differs from what runs (AI's "diverged"; for notifications and Git, the new drift), the section offers it.
- It is a plan whose candidate is the file's block, which writes nothing, under the same interlocks.
- It replaces `SaveNotifications`' "restart Klyx to use the file's settings" refusal.

**The AI credential actions (R5)** are activation routes too, and each follows this contract:
- **Their terms:**
  - `SetAPIKey` and `MoveKeyToKeychain` activate the running terms (`s.applied`) with the new key, never the file's block.
  - A file block that differs from what runs beyond the key stays drift.
  - `MoveKeyToKeychain` writes the file. Its block condition is that the file's block equals the running one apart from the plain-text key; otherwise it refuses: "fleet.yaml's AI settings differ from the ones in use: use the file's values, or save, first".
  - A key rotation or migration never adopts a different destination or permission.
- **`ClearAPIKey` stays immediate,** a tightening that takes no confirmation. It revokes even when the file is unreadable or cannot be written, publishing AI unavailable under the running terms.
- **Each advances the AI revision** under the section's Applying state, like an Apply. An in-flight call blocked at its fence is refused after the move.

## 6. Frontend (stage F2)

- **AI:** Save becomes "Review and apply". It shows the plan inline (changes, what ends, the interlock where required) with Apply. The existing egress confirmation becomes the plan's interlock, and confirmation state is keyed by the plan's digest.
- **The draft guard's "Save and leave"** asks the backend for an automatic Apply. The backend applies it only if, at Apply time and atomically under the Applying state, the plan ends nothing and needs no interlock. Otherwise it returns the plan, and the person stays in Settings with it shown.
- **Notifications:** a toggle still applies at once, through a plan that ends nothing, and says so. If a replan ends something (not possible in F2), it would be shown instead.
- **Git:** approvals and aliases stay explicit actions, each one a plan applied by its own confirmation.
- **Each section:** a file-differs notice with "Use the file's values", and a "saved, not active" banner with Retry and Revert as §5 defines them.

## 7. Tests

**F1:**
- **The registry:**
  - Applying and Held refuse tickets.
  - R1's probe: Apply stopped between `Begin` and `Publish`, where an unprotected admission gets no usable ticket.
  - Release moves the epoch, so no old arm, review or offer revives.
  - A zero ticket and an unknown cluster fail closed.
  - Publication never takes the guard's lock: a race test.
- **The incarnation:**
  - Connecting→Synced and the retry path's Failed→Synced advance it; Degraded↔Synced and Stale→Synced do not.
  - A replacement starts above its predecessor.
  - The new incarnation is visible before usability.
- **Every guarded method,** the table of §2, with the AST coverage test and its four negative fixtures.
- **Several writes under one ticket:** `ReconcileWithSource` with a move between its two writes. The first is written, the second not sent, and each is said. Plus a lost-answer retry, which is unverified, and receiptless verbs.
- **CLI writes:**
  - a drain refused at the kubectl start after a move;
  - a started one continuing;
  - the debug terminal checked at its handoff.
- **The guard:** a mismatched arm clears the grant; an arm under an old epoch is refused at `Check`.
- **A3:**
  - an offer withdrawn on a move, and on Held;
  - a review under an old epoch refused, a new review accepted;
  - a tightening between `RunStep`'s guard and its write refused at the write, with no second grant spent;
  - verification after a move ends unverified.
- **AI:**
  - a turn ended by a move;
  - a start refused on Held;
  - Explain's evidence read refused after a move.
  - Codex's provider probe: a same-host 307 followed by a second POST after a move, where the second attempt is refused and the result says the first may have been sent. An SDK retry the same way.
  - A move during the CLI's preflight: the prompt is never written.
  - A comparison member's move.

**F2:**
- the plan from what runs: a hand edit turning egress on, then "Use the file's values", takes the interlock;
- the file changing while a plan is open refuses Apply with a new plan, and so does a revision, an epoch or the work version moving;
- a new turn started from a second window between plan and Apply: Apply refuses, and Save and leave stays;
- activation failing after the write: saved, not active, AI refused everywhere;
  - Retry with unchanged terms activates;
  - Retry after a file edit widening egress or changing the destination is refused and returns a fresh plan;
  - Retry after a runtime policy change is refused the same way;
  - Revert;
- the credential actions with a divergent file:
  - `SetAPIKey` activates the running terms, not the edited egress (Codex's probe);
  - `MoveKeyToKeychain` refuses;
  - `ClearAPIKey` revokes even with the file unreadable;
  - an in-flight blocked call is refused after the move;
- Git's running copy: a hand edit shows as drift and applies only on adoption, for every consumer;
- notifications: the old restart refusal replaced by drift and adoption;
- the candidate cache's bounds and consumption;
- confirmation state keyed by digest: a replan clears it.

**Native smoke:** the AI section's Review and apply on the lab, with the plan shown and applied.

## 8. Not in F

- Editing clusters (S2), observability sources (S3) and AI profiles (S4), the consumers of these contracts. F's registry API and its tests already enforce §1's publication and hold contract, so those slices do not keep separate facts in step by convention.
- Ending forwards and streams on a reconnect (S2, with the reconnect).
- Gating the exec terminal or port-forward.
- Stopping a CLI write that has started.

## 9. Rulings (Codex, 2026-10-25)

1. **The incarnation rule is accepted,** as §1 now states it. The counter moves on not-usable→usable, not on a blip through Stale. A replacement's counter never repeats, and the new incarnation is published before usability.
2. **Git's running copy is accepted** as an intentional, disclosed behaviour change. One snapshot serves every consumer, initialized from the startup configuration.
3. **A started CLI write continues,** checked at the actual child start or terminal handoff. A started child is not proof of acceptance, and no retry reuses an invalid ticket.

## 10. Revision 1: Codex's review (2026-10-25)

Codex requested changes on 8f1129b: six P2 and one P3. It found the direction sound and §0's baseline accurate. It ruled on §9's three choices, now recorded there.

| Finding | Change |
|---|---|
| **R1, P2:** policy, epoch and admission readiness were separate facts; a guard between the epoch's advance and the protection's publication could pair the old protection with the new epoch | §1: one registry publishes a snapshot (protection, epoch, Active, Applying or Held) read by every admission under one lock. Publication is Begin then Publish, each moving the epoch. Applying and Held refuse tickets, AI starts, reads and sends, and A3 offers, reviews and runs. Release moves the epoch, so nothing revives. A zero ticket fails closed. Grants are invalidated lazily by epoch, so the lock order cannot invert. |
| **R2, P2:** the provider's entry is not every send: redirects, SDK retries, the CLI's prompt handoff and Explain's evidence reads came after it | §4: `readfence.Transport` on every AI HTTP client, so each attempt is checked. The CLI's `release` checks the fence before the prompt is written. Explain and Suggest-fix carry the fence on their reads. A forwarded earlier attempt keeps "may have been sent". |
| **R3, P2:** forwarding evidence belonged to a context, not to a write | §2: `t.Write(ctx)` gives each logical write its own tracker, receiptless verbs included, kept across that write's retries. A refusal after a forwarded attempt is unverified, not "not sent". Each write is reported. |
| **R4, P2:** Retry could activate changed file values under an old confirmation | §5: Retry retries the exact failed candidate, with its file version and terms, or is refused with a fresh plan and its own interlocks. Revert is a plan, and cannot resurrect a superseded activation. |
| **R5, P2:** the credential actions activate through their own route, and `SetAPIKey` adopts a hand-edited egress grant | §5: they activate the running terms. `MoveKeyToKeychain` refuses a divergent file. `ClearAPIKey` stays immediate, even with the file unreadable. Each moves the AI revision under the Applying state. |
| **R6, P2:** a plan's consequences were not bound; a turn started elsewhere could be ended unreviewed | §5: the digest covers what ends and what continues by identity, and a work version. Apply runs under the Applying state and re-plans, refusing on any difference. Save and leave's ends-nothing condition is enforced by the backend, atomically. A replacement plan never inherits a confirmation. |
| **R7, P3:** the outage and baseline wording | §0 and §1 corrected: Stale is usable, so a blip through Stale needs no re-confirmation; notifications' running copy is updated by `SetSettings`; `pre=nil` still has slice 0's write policy; `Check` consumes only on a protected cluster; `CancelDrain` exists but retracts nothing. |

**Also from the review:**
- the candidate cache's bounds;
- the per-section serialization of Applies;
- the file version check on "Use the file's values";
- digest-keyed confirmation state;
- the four negative AST fixtures;
- the coverage table naming the 21 methods.

## 11. ACCEPT (2026-10-25)

Codex accepted the design at revision 1 (51d4daa), after the review of 8f1129b, with no remaining findings. Its implementation checkpoints are acceptance details for the build:
- **One AI activation boundary.** Apply and the credential actions share one serialization and admission boundary, so a concurrent clear is never undone by an older publication. Storing a credential for an inactive provider stays apart from activating the running provider.
- **A pre-write refusal changes nothing.** A re-plan or file-version refusal before the write releases the Applying gate without publishing and without becoming saved-not-active. The comparison uses the reviewed runtime state and does not invalidate itself by entering its own gate.
- **Work tracking follows the affected operations.** Explain, Suggest-fix and A3 count as well as chat turns, and the displayed consequences come from that state.
- **The CLI release fence is final.** The containment and evidence-on-stdin contract stays. A failed release is never followed by another, unfenced handoff, and the transport carrying retries and redirects is the fenced one.
- **One tracker per logical write.** `t.Write` keeps the receipt recorder and allocates a fresh forwarding tracker per logical write, never per retry.

## 12. F1 as built (2026-10-25)

Built in bee1d3f, with c04ee37: the fences, §1-§4.

| Contract | Code |
|---|---|
| §1 the registry | `internal/policy` (Snapshot, Epoch, Seed, Begin, Publish, Hold, Release, ConnUp); seeded in `main` from the running clusters before any connects, and by `AddClusters`; `ConfigService.IsProtected` returns its value for a known cluster |
| §1 the incarnation | `fleet.ClusterConn.moveLocked`, the one way the state changes, telling `Registry.SetOnUsable`'s hook before the new state is visible |
| §2 tickets | `appbridge/protectguard.go`: `Guard`, `Ticket.Valid`, `Ticket.Write`, `Ticket.StartCLI`, `guardFor`; the 21 methods converted |
| §2 per-write tracking | `t.Write`'s tracker; each step of `ReconcileWithSourceSteps` and each document of `CreateResourceYAML` gets its own |
| §2 CLI starts | the drain and the debug terminal check `StartCLI` at the start; the helm runner checks the context's fence before its child |
| §3 A3 | the conversation's pinned epoch in `planFenceLocked` (offers and verification), the review's epoch in `RunStep`, the ticket in `RunStep`'s fence |
| §4 AI | `admitNow` at a conversation's start, `epochProblem` in the turn guard and the member check; `callFence` for Explain and Suggest-fix; `readfence.Transport` in `ai.Harden`; the CLI's release fenced; a write tracker on each send, and `modelError`'s "nothing was sent" or "may have been sent" |

**Where the build differs or adds:**
- **The guard's precedence.** On a protected cluster the grant is consumed first; an unprotected or unknown cluster has none to consume. A cluster that is Applying or Held is then refused as such, before an unarmed or stale grant, because that is the more useful reason.
- **Offers are pinned through their conversation.** An investigation is pinned to one cluster, so its epoch, captured at the start, is every offer's. A review also records its own epoch, which `RunStep` compares. That check is redundant with the plan fence, and is kept as defence in depth: its mutant survives a test only because the plan fence refuses first.
- **Grants are invalidated lazily,** by the epoch they recorded, at `Guard`. Nothing deletes them on a move, so a publication never takes the guard's lock.
- **An unknown cluster** is refused at every admission. `ConfigService.IsProtected` keeps today's false for one, for display only.
- **Receipt-less verbs report a fence refusal's outcome** (c04ee37): `failed` sets not sent or unverified, as a matrix verb's reply does.
- **The production producers in F1** are `Seed` and the incarnation. `Begin`, `Publish`, `Hold` and `Release` are exercised by tests until S2's Apply uses them.
- **A gap in unit coverage:** fleet's per-write trackers sit below the fake dynamic clients, which do not go through the HTTP transport. They are covered by the appbridge fakes, which route each write through the real `readfence.Transport`, and by review.

**Tests:**
- the registry;
- the incarnation;
- the guard:
  - epoch binding;
  - a mismatched arm;
  - Applying, Held and Release;
  - zero tickets, unknown clusters and no guard;
  - a race test;
- a table over the 20 manual guarded methods, each refused and not sent after a move, and each writing without one (except the three that start a child);
- per-write tracking:
  - a lost answer, which is unverified;
  - two writes under one ticket;
  - a receipt-less verb;
- A3:
  - a tightening between the guard and the write;
  - a reconnect withdrawing the offer and its review;
  - a hold, whose release revives nothing;
  - verification;
- AI:
  - a start while applying;
  - a turn ended by a move;
  - a comparison member's move;
  - Explain's revision and epoch;
  - the redirect probe;
  - a refusal before any send;
  - a move during the CLI's preflight;
  - the wording of each;
- the helm runner's start check;
- the AST scan of every guarded method, with four negative fixtures.

Every Go suite and golangci-lint pass. The native smoke passed 211/211 at bee1d3f, night and day at 1440 and 1024; c04ee37 adds only the outcome fields of a fence refusal and tests.

**Mutation pass:** 24 mutants of the registry, the incarnation, the guard and tickets, `RunStep`, the plan fence, the AI start, turn and member checks, `callFence`, the receipt-less outcome, the drain's start check, the AI transport, the CLI release and the helm runner: 24 caught. One mutant was left out on purpose: removing the debug terminal's `StartCLI` would make a test open a real terminal. The AST scan covers that site instead.

## 13. F1 revision 1: Codex's implementation review (2026-10-25)

Codex requested changes on 0563872: six P2. It accepted §12's guard precedence (with the qualification now in §12), the offers' pinning through their conversation, lazy grant invalidation, unknown clusters, and `Seed` and the incarnation as F1's producers. It found the receipt-less outcomes incomplete (F6) and the fake-client coverage insufficient (F2-F4), and its probes through the real clients over a fake round tripper are now regression tests.

| Finding | Change |
|---|---|
| **F1:** Explain decided egress before pinning the epoch, so an old permission could pass under a newer epoch; Suggest-fix the same | `precheck` pins the epoch (`callFence`) before it decides egress, so a protection change after the pin moves the epoch the fence holds. A conversation's start and a comparison member pin before their egress decision too; their turn-time checks already caught the race, and the order now makes the start coherent |
| **F2:** manual discovery dropped the ticket's context | `gvrForKind`, `sourceServedGVR`, the generator's version check and the composer's `resolveGVKIn` read discovery under the call's context (`discoveryFor`), a cached answer included. `servedVersion` and `servedResourceAmong` return a fence refusal as their error and never fall back past it; the composer reports it as itself, never "not served" |
| **F3:** the diagnostic namespace and pod shared one tracker | each create gets its own; a pod not sent after a created namespace says so, and says the namespace was created |
| **F4:** a composite's not-sent step read as unverified | `fleet.Step.NotSent` (with `Refused`), `StepDTO.notSent` and "not sent - …" in the step wording; the source's error wording has a not-sent case; a composite's top-level outcome is left to its steps, so a not-sent target never says "nothing was sent" beside a written source |
| **F5:** the AST scan accepted a constructor taken as a value and an ignored `StartCLI` | a closed set of forms: a constructor is only ever called inside the ticket's `Write` or on an "unfenced:" line; every context a call receives has a tracked provenance (the ticket's `Write`, a `context.With*`, `readfence` or `fenced` wrapper of one, or a variable assigned only those, to a fixpoint), and a context field, a context parameter, or a context-named variable of unknown origin is refused; a child start must directly follow `if err := t.StartCLI(); err != nil { … return }`; across the package, a function returning a context outside three reviewed ones, and a package-level root context, are refused |
| **F6:** known pre-write refusals were classified as possibly sent | `readfence.Check` marks a refusal `ErrNotForwarded` when its context tracks a write and nothing of it was forwarded yet, so a refused GET before a write is not sent; `readfence.Send` checks before a send that is not HTTP (the CLI's prompt handoff, the helm child's start) and records it as forwarded. A refusal after a forwarded attempt stays unverified |

**Tests added:** Codex's eight probes, adopted (`fleet/f1fence_test.go`, `appbridge/ai_fence_test.go`, `appbridge/guardscan_test.go`, `ai/fence_test.go`), and beside them:
- discovery cold and warm, a fence moving between the group read and a version's, the source's discovery after the target's read, and an ArtifactGenerator verb;
- the diagnostic pod after an existing namespace, and its own lost answer, which stays unverified;
- a source stopped before its write, and a composite's top-level outcome;
- the chat's start and a comparison member under a preempted protection read, and an unchanged unprotected Explain and Suggest-fix, which go ahead;
- `Send` and `Check`'s marking, and a read refused after a forwarded write, which is not marked;
- the step wording;
- fourteen negative scan fixtures, each asserting its own finding.

**Mutation pass:** 41 mutants of the fixes - `Check`'s and `Send`'s marking, the helm and CLI sends, every discovery fallback and binding, the step's not-sent outcome through the bridge, the DTO and the wording, the diagnostic pod's trackers, the admission order of Explain, Suggest-fix, a chat's start and a member, and fourteen of the scan's rules - 41 caught. §12's 24, rerun on this revision: 22 still apply and are caught; the CLI release's and the helm runner's are re-specified above.

Every Go suite, the race suites of the touched packages, golangci-lint (with and without the eval tag), tsc and the frontend suite pass. The native smoke passed 211/211, night and day at 1440 and 1024, on this revision before its commit.
