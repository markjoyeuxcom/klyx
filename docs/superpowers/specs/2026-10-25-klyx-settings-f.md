# Klyx Settings F: the activation foundation

Opened 2026-10-25, on `feat/settings-s0`, after S1's implementation was accepted (S1 §15). F is the redesign's foundation slice (redesign §14). It builds the contracts S2, S3 and S4 apply changes through:
- plans from the running state;
- per-cluster policy epochs in the guard and in every write's fence;
- the invalidation of arms, reviews and offers;
- saved-not-active states.

It is built in two stages under this note:
- **F1, the fences:** epochs, tickets, the 21 guarded verbs, A3 and the AI fences. Backend only.
- **F2, the plans:** Apply for the three blocks Settings edits today (AI, notifications, Git), "Use the file's values", and saved-not-active.

This note is for Codex's review before it is built.

## 0. What exists

From a read of the code at 8ad6f36:
- **The guard** (`appbridge/protectguard.go`):
  - one-shot grants keyed by the cluster name alone, for 30 seconds;
  - `Arm` judges protection at arm time and records nothing for an unprotected cluster;
  - `Check` consumes the grant, pass or fail;
  - 21 verbs call `checkProtected` first, before their lookup and before the reads that precede the write.
- **No write is fenced on policy.** A manual verb's write context carries no read fence; only object UID preconditions guard the write. The one run fence is A3's `runFence`, which checks the pinned connection pointer and its usability, and by its own comment not the AI settings.
- **There is no connection incarnation.** A connection is its `*ClusterConn` pointer, which the registry never replaces: Failed→Synced heals in place, so a pointer comparison passes again after an outage.
- **Protection never changes at run time,** except that `AddClusters` adds a cluster. The running configuration is the startup snapshot. `IsProtected` is recomputed on every call, and nothing caches the boolean.
- **A3:**
  - `RunStep` decides protection at entry, and the AI revision under its first lock, and re-checks neither before the write;
  - `ReviewStep` never sees protection;
  - a review lasts 2 minutes, a grant 30 seconds.
- **AI:** the settings revision is global (`AIService.rev`). The chat and investigation turn guard checks it with the connection; Explain and Suggest-fix check protection once and never again before the send.
- **The three editable blocks:**

  | Block | Writer's file fence | Running copy | Drift | Adopting the file |
  |---|---|---|---|---|
  | AI | none (`pre=nil`); its egress interlock compares with the file, not with what runs | `s.applied` | "diverged" | Save writes the draft, which starts from the file |
  | notifications | refuses if the file's block is not the running one | the startup copy | none | a restart |
  | Git | none (`pre=nil`) | none: every use re-reads the file, so a hand edit applies at once | none | not needed |

- **Writes Klyx cannot fence once started:** a drain (a kubectl child), a Helm rollback (a helm child, no deadline), and the debug terminal (a terminal Klyx no longer controls).

## 1. The policy epoch

`internal/policy` holds one epoch per cluster: an opaque value compared only for equality. It has two parts, and moves when either does:
- **Policy:** advanced when the cluster's running policy changes:
  - its effective protection, from any input (§4 of the redesign: the flag, the environment, `tags.env`, the name, the context);
  - in S3, a subscription that feeds it;
  - in S4, its local-only policy.

  In F the only production producer is `AddClusters`: a new cluster starts at policy 1. S2's Apply is the first that moves one. Tests advance it directly.
- **Connection:** a new incarnation counter on `fleet.ClusterConn`. It advances each time the connection becomes usable from not usable: Connecting→Synced and Failed→Synced, and, in S2, a replaced connection.
  - Usable is today's meaning (Synced, Degraded or Stale).
  - Degraded↔Synced and Stale→Synced leave it alone, since the connection never stopped being usable.
  - Starting at the first sync, it makes an arm, a review or an offer made before an outage stale after it, which the pointer comparison cannot.

**Held clusters.** `policy.Hold(cluster, reason)` marks a cluster saved-not-active: no ticket is issued for it, and every ticket for it fails, until `Release`. This is the redesign's "saved, not active blocks new work as well as old" (§17a). Its producer is S2's failed reconnect; F tests it directly.

**Invalidation on a move:** advancing a cluster's epoch deletes its armed grants. Reviews and offers that recorded the old epoch fail their next check (§3).

## 2. Tickets: the guard captures, the write checks

`checkProtected(cluster) error` is replaced by `guard(cluster) (Ticket, error)`:
- **A protected cluster:** it consumes the one-shot grant, pass or fail, as today. It refuses when the grant is missing or expired, or was armed under another epoch.
- **An unprotected cluster:** it passes, and the ticket still captures the epoch. A cluster that becomes protected before the write is then refused at the write: the unprotected pass of §17a.
- **A held cluster:** refused.

`Arm(cluster, typed)` records the epoch with the expiry. One change to today's behaviour: a mismatched typed confirmation now clears an existing grant, as `ArmAIEgress` already does.

**The fence.** `Ticket.Valid()` returns nil while the cluster's epoch is the captured one and the cluster is not held. Otherwise it returns an error naming what moved: "aks-we-prd's protection changed since this was confirmed", or "the connection to aks-we-prd restarted since this was confirmed". Each guarded verb puts the ticket on its write context with `readfence.With(ctx, ticket.Valid)`, so every request it makes checks the epoch: the reads before the write, and the write itself.
- A request refused before it is forwarded is not sent. `WithReceipts` already classifies `ErrNotForwarded` as not sent; a verb without receipts says "not sent: <why>".
- **One ticket for several writes** (`ReconcileWithSource`, a multi-document create, an Argo sync): each request is fenced. A write already forwarded keeps its outcome, and the result names the ones not sent.
- **CLI writes** (drain, Helm rollback, debug terminal): `Valid` is checked immediately before the child starts. A running child is not stopped by a move, since it is already forwarded (§17a: ending verification undoes nothing). S2's plans name a running drain or rollback on the cluster as continuing.
- **Unchanged:** the ungated paths stay ungated (the exec terminal, port-forward, dry-runs, `ReviewStep`, `flux diff`), and a dry-run consumes no grant.

**Coverage.** `checkProtected` is removed, so a verb cannot compile without the ticket. The two ways to get a write context, `withTicket(ctx, t)` and `startCLI(t, ...)`, both apply the fence. A Go AST test fails on:
- a call to `guard` whose ticket is not passed to one of them;
- a guarded service method that writes outside them.

A table test covers every guarded verb against fakes. For each, a move between the guard and the write is refused and not sent, an unprotected pass is refused once protection tightens, and a stale arm after an outage is refused.

## 3. A3: offers, reviews and runs

- **Offers:** a plan records each target's epoch when it is offered. `planFenceLocked` withdraws it when one moves ("aks-we-prd's policy changed"), sticky, like its other fences.
- **Reviews:** a review token records the epoch. `RunStep` refuses a review made under another epoch, so a new review is needed, as for a changed digest.
- **Runs:** `RunStep` takes its ticket at entry; the grant is consumed there, on refused paths too. `runFence` checks `ticket.Valid` with the pinned connection, before and during the write, through the context's read fence. A tightening between the guard and the write is refused at the write, and the epoch check spends no second grant (§17a).
- **Verification:** `verifyFence` checks the epoch as well. A move after the write ends verification as unverified, and the write keeps its outcome.

## 4. AI fences

- **Chat and investigations:** a conversation pins its cluster's epoch at start, beside the settings revision. The turn guard ends the turn when the epoch moves ("aks-we-prd's policy changed"), sticky like a revision change. A fleet comparison's members are checked the same way, each against its own epoch.
- **Explain and Suggest-fix** re-check the settings revision and the epoch immediately before the provider call; a change refuses with its reason, and nothing is sent.

## 5. Plans (stage F2)

**What a plan is:**

```go
type PlanDTO struct {
	Section     string            // ai | notifications | git (S2 adds clusters, S3 sources, S4 profiles)
	Digest      string            // over the section, the running and candidate values, the file version and the revisions
	FileVersion string            // the fleet file's content hash when planned
	Runtime     map[string]string // the revisions it was planned from: the AI revision, each affected cluster's epoch
	Changes     []PlanChange      // per registry id: from and to, as shown; never a secret, only its state
	Ends        []string          // "2 chat turns and 1 investigation on aks-we-prd", "1 offered step", "an armed egress grant"
	Continues   []string          // what a change does not stop (S2: a running drain)
	Interlock   *Interlock        // the phrase and why, when §1.5 of the redesign requires one
	Writes      bool              // false for "Use the file's values": the file already says so
	Refused     string            // a candidate that cannot be applied, and why; nothing is written
}
```

**Planned from what runs.** Each section has a running copy, and the plan compares the candidate with it, never the file with the file:
- **AI:** `s.applied`, as today. The egress interlock compares with the running grant, which fixes §0's issue: "Use the file's values" turning egress on takes the interlock, even though the file already says so.
- **Notifications:** its running copy, as today.
- **Git:** gets a running copy, loaded at start, and every reader uses it. **This is a behaviour change:** a hand edit of `git.repos` no longer applies at once. It shows as drift, and applies with "Use the file's values" or at a restart, like the other blocks. It is proposed for review (§9).

A candidate that cannot be built (an AI provider that does not build) is refused at plan time, and nothing is written, as today.

**Confirming.** The plan is shown; the person applies it. `Apply(digest, typed)` re-plans from what runs now, with the candidate the backend holds for that digest (5 minutes). If the digest differs, because the file, a revision or an epoch moved while the dialog was open, it refuses and returns the new plan. The interlock's typed phrase is consumed by that Apply only.

**Apply in order** (redesign §4):
1. Write the block through slice 0's write policy. The write also checks that the file's content is still the version planned from, so a hand edit made while the plan was open refuses the Apply. AI and Git gain the file fence notifications already has. "Use the file's values" writes nothing.
2. Advance what it changes: the AI revision through an applying state, which ends turns and withdraws offers; the affected clusters' epochs (none in F2).
3. Invalidate what was armed under the old terms: the AI egress grant, and the affected clusters' grants.
4. Activate: build and publish the AI state, set the notification engine, swap Git's running copy.

**When activation fails after the write:** the section is saved, not active, and says both states with Retry and Revert.
- Its work stays fenced. AI is unavailable everywhere with that reason, its revision having moved, and it never resumes under the old or the new terms until Retry activates the file's values or Revert writes the running block back, itself as a plan.
- For AI this is rare, since the candidate built at plan time, but a keychain failure between plan and activation can do it.

**"Use the file's values":** where a section's file block differs from what runs (AI's "diverged"; for notifications and Git, the new drift), the section offers it. It is a plan whose candidate is the file's block and which writes nothing, under the same interlocks. `SaveNotifications`' "restart Klyx to use the file's settings" refusal is replaced by it.

## 6. Frontend (stage F2)

- **AI:** Save becomes "Review and apply". It shows the plan inline (changes, what ends, the interlock where required) with Apply. The existing egress confirmation becomes the plan's interlock.
  - The draft guard's "Save and leave" applies a plan only when it ends nothing and needs no interlock. Otherwise it stays, and shows the plan with that reason.
- **Notifications:** a toggle still applies at once, through a plan that ends nothing, and says so.
- **Git:** approvals and aliases stay explicit actions, each one a plan applied by its own confirmation.
- **Each section:** a file-differs notice with "Use the file's values", and a "saved, not active" banner with Retry and Revert.

## 7. Tests

**F1:**
- **Every guarded verb** (the table of §2): a move between guard and write is refused and not sent; an unprotected pass is refused after a tightening; a stale arm after an outage is refused; a held cluster is refused.
- **Several writes under one ticket:** `ReconcileWithSource` with a move between its two writes. The first is written, the second not sent, and both are said.
- **A CLI write:** a drain refused at start after a move; a started one continuing.
- **The guard:** a mismatched arm clears the grant; an arm under an old epoch is refused; a move deletes the cluster's grants.
- **The connection incarnation:** Connecting→Synced and Failed→Synced advance it; Degraded↔Synced and Stale→Synced do not.
- **A3:**
  - an offer withdrawn on a move;
  - a review under an old epoch refused, a new review accepted;
  - a tightening between `RunStep`'s guard and its write refused at the write, with no second grant spent;
  - verification after a move ends unverified.
- **AI:** a turn ended by a move; Explain refused before the send on a move.
- **The AST coverage test** of §2, with negative fixtures: a ticket not passed to the fence, and a write outside the fenced context.

**F2:**
- the plan from what runs: a hand edit turning egress on, then "Use the file's values", takes the interlock;
- the file changing while a plan is open refuses Apply with a new plan, and so does a revision or an epoch moving;
- activation failing after the write: saved, not active, AI refused everywhere, then Retry and Revert;
- Git's running copy: a hand edit shows as drift and applies only on adoption;
- notifications: the old restart refusal replaced by drift and adoption;
- the guard's Save and leave with a plan that ends work: it stays.

**Native smoke:** the AI section's Review and apply on the lab, with the plan shown and applied.

## 8. Not in F

- Editing clusters (S2), observability sources (S3) and AI profiles (S4), the consumers of these contracts.
- Ending forwards and streams on a reconnect (S2, with the reconnect).
- Gating the exec terminal or port-forward.
- Stopping a CLI write that has started.

## 9. For the review

Three choices to rule on:
1. **The connection incarnation** moves on every not-usable→usable transition, not only on a replaced connection. That refuses arms, reviews and offers across any outage, at the cost of re-confirming after a network blip.
2. **Git's running copy** (§5) changes how a hand edit of approvals applies, for consistency with the other blocks and with the redesign's "file and running are kept apart".
3. **A started CLI write continues** after a move, as a forwarded request does, rather than being cancelled part-way (a half-finished drain).
