# Coverage S2b: the Velero horizon and lists

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S2b. It builds on S2a (`2026-10-17-klyx-coverage-s2a-velero-health.md`): design accepted at ebcd07d, implementation at bc3be19. Revision 1 answers Codex's design review of 1d11926 (§8), revision 2 its re-review of 8b6129d (§9). Every S2a contract stands, notably:
- the Backups watch: capped, in two tiers, and its window `W`;
- run identity: proven, named or synced, with execution and run times;
- the message classes and placeholders;
- read-only: no verbs.

The roadmap's S2b:
- **The fleet horizon:** "a fleet-wide 'latest retained successful backup per schedule': the newest Completed Backup by `completionTimestamp`, with PartiallyFailed kept distinct".
- **Lists:** the Backups and Restores lists ("Restores are read; only *running* one is out of scope").
- **Identity:** a schedule recreated under its name is a new schedule; expiration and deletion take entries out of the history; the list says when it is partial or truncated.
- **Honest wording:** "latest retained successful backup", never "restorable". Completed is not a restore test.

**A gap from S2a, closed here:** S2a §2 listed a "last successful run" column for the Schedules list, and S2a did not build it. It is this slice's horizon datum, so it lands here, in the column, in the inspector and fleet-wide.

## 1. The API, beyond S2a

Read from Velero v1.18.4, as S2a.

**Restore** (`velero.io/v1`):
- **spec:**
  - the source: `backupName`, or `scheduleName` (the newest Completed backup of that schedule, by `startTimestamp`);
  - `includedNamespaces`, `excludedNamespaces`, `namespaceMapping`, `restorePVs`, `existingResourcePolicy`, `itemOperationTimeout`;
  - `labelSelector` and `orLabelSelectors`;
  - `hooks`: registered as opaque in S2a;
  - `resourceModifier`: a ConfigMap reference.
- **status:**
  - `phase`: New, FailedValidation, InProgress, WaitingForPluginOperations, WaitingForPluginOperationsPartiallyFailed, Finalizing, FinalizingPartiallyFailed, Completed, PartiallyFailed, Failed;
  - `validationErrors`, `failureReason`, `warnings`, `errors`;
  - `startTimestamp`, `completionTimestamp`, `progress{totalItems, itemsRestored}`, the item-operation counters and `hookStatus`.
- **What the controller does:**
  - with `backupName`, it fills `scheduleName` from the Backup's label;
  - with `scheduleName`, it picks among every Backup carrying the label, synced foreign ones included (`restore_controller.go` 370-389);
  - a Restore is deleted with its Backup.
- **Restore `validationErrors` forms** (`restore_controller.go` 320-435), each a fixed class (§5):
  - non-restorable resources;
  - invalid included or excluded resource or namespace lists;
  - both label selectors;
  - both or neither source;
  - invalid hooks and invalid init containers;
  - an invalid ExistingResourcePolicy;
  - the schedule's backups unlistable, none found, or none completed;
  - the backup unreadable;
  - the BSL unavailable;
  - invalid resource policies;
  - the resource-modifier ConfigMap unreadable, unparseable or invalid.

**History is what the cluster still holds:**
- Velero's GC deletes each Backup when it expires (`spec.ttl`, default 720h), and its Restores with it.
- Backup sync can bring records back from object storage (S2a §1).
- So every history Klyx shows is "retained", bounded by the TTL and by S2a's Backups cap.

## 2. What Klyx shows

**In the Velero lens:**
- **Schedules:**
  - **A new column:** "last successful backup" with its age, or "none retained" / "none since W" (§4).
  - **The inspector:** a horizon block: the latest retained successful backup, by whom it is proven, and a newer PartiallyFailed run when there is one. It reads "latest retained successful backup", never "restorable".
- **A Backups tab:** every retained Backup.
  - **Columns:** phase, schedule (with proven, named, synced or none), started, completed, expires, errors and warnings, storage location.
  - **Filters:** by schedule and by phase group (failed, partially failed, in progress, completed).
  - **Notices:** a notice says the list is what the cluster retains, bounded by each backup's TTL. When capped, it says the list holds the runs that started since `W`, and that it holds every run in progress only when the window is in tier 1 (S2a §3).
  - **Expired records:** shown as "expired, awaiting deletion", with the GC failure reason.
  - **Its counts:** its own counts of the records it shows, never the unscheduled part's health total.
  - **The inspector:** S2a's backup inspector, with its attribution:
    - "proven run of daily";
    - "matched by name to daily";
    - "looks synced from object storage";
    - "not a run of a current schedule".
- **The Unscheduled backups tab stays** as S2a built it: the counted, recent ones.
- **A Restores tab, read when it opens:**
  - **The read:** capped at 500, with "+N more" said, and an "as of" time with Read again. Nothing is retained after the view closes.
  - **Columns:** phase, source (backup or schedule), started, completed, errors and warnings, and the restore's class when it failed.
  - **The inspector:**
    - the source backup, joined to the retained Backups with the Backups kind's own coverage (§3);
    - the namespaces and mapping;
    - the policies, with the selectors as counts and the resource modifier's ConfigMap by name;
    - hooks counted, their commands withheld;
    - progress, the counters and the classes.
  - **No verdict, no count, no notification:** a Restore is a fact in S2b. Its phases keep Velero's words and The Line's marks.

**On the fleet board:** a **Backups** fleet view (a fleet route view beside Expiry), the horizon:
- **Rows:** one per schedule across every connected cluster with Velero:
  - the cluster and schedule;
  - the schedule's S2a verdict;
  - the latest retained successful backup and its age;
  - a newer PartiallyFailed run;
  - the run due;
  - each cluster's coverage: capped, stale, unreadable, or no Velero.
- **Order:** broken first, then the oldest latest success first, so the schedule longest without a success leads.
- **Each cluster is read once when the view opens,** from that cluster's Observer (§3), as the Expiry view reads its scans, with Refresh. A cluster that is not connected, or has no Velero, is named, never silently missing.

## 3. The source contract (roadmap C1)

| Source | Acquisition | Counted | Health | Notifications |
|---|---|---|---|---|
| the Backups list | S2a's `backups` part rows, extended to every retained Backup (below) | unchanged: the recent unscheduled backups only | unchanged | unchanged |
| the horizon datum | S2a's `schedules` part rows: `ScheduleVerdict` gains the latest retained success (§4) | unchanged | unchanged | unchanged |
| Restores | on-demand list of `restores` (v1) when the tab opens, capped at 500 | none | none: facts | none |
| the fleet view | per cluster, on demand: that cluster's `schedules` rows (`HealthRowsNow`), no new watch | none | the S2a verdicts | none |

**The Backups list rows:**
- The `backups` part's rows now carry every retained Backup.
- Each row says whether it is **counted**: S2a §4.3 membership, never `Rule.Included`. The part's counted items, Native totals and roles, ledger, Top and notifications come from the counted rows alone, unchanged.
- A row that is not counted carries a **display phase** (Completed none, PartiallyFailed look-at, Failed fault, in progress syncing), kept separate from any verdict, with its attribution and timing.
- **Verdict eligibility is gated centrally:** the shared anchor that AI Explain and the chat verdict map read (`anchorVerdict`, `verdictsFor`) takes only counted rows as Klyx's verdict. A list row is a fact. A test checks that the counted rows alone make the Native totals and roles.
- **The cost:**
  - Rows are published only while the lens is subscribed.
  - At S2a's cap of about 1,500 Backups, a publication is about 900 KB of DTOs, comparable to a large Pods list.
  - The rows are built from the capture the part already takes, so there is no new watch and no new retained model.

**The Restores read:**
- `restores.velero.io` v1, through the cluster's dynamic client, with a 500-item limit. Past it, "+N more" uses the list's `remainingItemCount` when the server supplies it. A continue token alone says "more", never a count.
- **Projected field by field into a bounded DTO** (the CRD bounds neither the map nor its strings):
  - names validated as object names, else marked invalid;
  - `namespaceMapping` at most 50 entries, with the count;
  - included and excluded namespaces at most 20 each, with counts;
  - every display string under the URL rule and clipped (256 bytes);
  - Velero's text classified (§5);
  - one Restore's DTO at most 4 KiB, measured.
- **Fences:** a reply carries its cluster and read id, and a stale reply (another cluster, an older read) is dropped. The inspector is UID-pinned.
- An absent API reads "not served", a forbidden one "not readable", never empty.
- **The source join** (Codex's finding 4) reads the Backups kind's own coverage from the `backups` part's capture, never the Schedules dependency's:
  - **Live and complete:** found, or "not found in the cluster".
  - **Live and capped:** found, or "not in the retained window": a capped read never proves absence.
  - **Stale:** found "as of <last good>", or "not in the last good read".
  - **Checking or unreadable:** "the Backups could not be read", with its class.

**The fleet read:**
- `VeleroService.GetBackupHorizon(cluster)` returns one cluster's schedules rows as horizon rows with that capture's coverage, read through `HealthRowsNow`.
- The frontend fans out over the fleet, as `FleetExpiry` does.
- A cluster without an Observer or without Velero says so.

**Budget:** no new watch and no new retained model. The Restores read retains nothing past the view, and the fleet read retains one reply per cluster while the view is open.

## 4. The latest retained successful backup

Revision 1 answers Codex's design review of 1d11926 (§8): completion order is never certified by `W`, missing completion times are said, expiry is an eligibility rule with its own clock, and the confidence of a name match stays visible everywhere.

**The candidates, per schedule S:**
- **The runs:** S's runs by S2a's `Attribute`, whose precedence stands. `N` and a valid owner proof win over the looks-synced heuristic, so a proven record re-imported by backup sync is a candidate. "Never synced" means the unproven synced category only.
- **The phase:** Completed.
- **Not expired at the capture time:** `status.expiration` is after the capture's now. Velero's GC can leave an expired record in the API indefinitely when its storage location is missing, unreadable, unavailable or read-only (`gc_controller.go` 145-178). Such a record is no horizon candidate. The Backups list shows it as "expired, awaiting deletion", with the GC failure label's reason when set.

**The selection:**
- **Proven first:** among the proven candidates, the newest by `completionTimestamp`.
- **Named only when no proven candidate is retained:** the newest named one.
- **Its confidence is part of every cell, row and AI field:**
  - "latest proven retained success <age> ago";
  - or "a Completed record matched by name <age> ago (not proven to be this schedule's)".

  A name match never appears as a confirmed success of S.
- **A newer PartiallyFailed run:** the newest proven, else named, PartiallyFailed run that completed after the selected success. It is labelled the same way: "a newer run partially failed", or "a newer record matched by name partially failed".

**A missing completion time is said, never filled in** (Codex's finding 2). Completed runs normally carry `completionTimestamp` (set by the backup and finalizer controllers), but a record can lack it.
- **The order rule:** run A is certainly after run B only when both completion times are recorded and A's is later, or when A started after B's recorded completion. Any other pair's order is unknown, and is said.
- **Applied to the selection:**
  - **A candidate without one, started after the newest timed candidate's completion,** is certainly newer. It is selected, shown as "started <time>; its completion time is not recorded". When several are, the latest start is shown and the order among them is said to be unknown.
  - **One started before it** may or may not be newer, so the selection is qualified: "latest by completion time; a success without a recorded completion time may be newer".
- **Applied to the newer-PartiallyFailed comparison:** a PartiallyFailed run is "newer" only when it is certainly after the selected success; one whose order is unknown is "a PartiallyFailed run of unknown order".
- **Uncapped history too:** the rule does not depend on the cap.

**Completeness under the cap: `W` does not certify completion order** (Codex's finding 1).
- **Why:** S2a admits terminal Backups by run time, so an omitted run that started before `W` can have completed after the retained candidate.
- **When the selection is complete:**
  - **Uncapped:** the selection is complete, subject to the missing-time rule.
  - **Capped:** it is complete only when S's whole history is held, that is when S's creation minus a minute is after `W` plus a minute (S2a §15's "own history"), so that every run of S is retained.
- **Otherwise it is always qualified:** "latest retained success; a run that started before <W> and completed later may not be retained". With no candidate: "none since W retained".
- **The window in tier 0** (S2a §3) certifies no terminal coverage, and it does not prove that no finished run is retained (revision 2): the Observer keeps a tier-0 floor until a complete refill, while a Backup it retains can finish meanwhile.
  - The selection is made from the retained candidates, as in tier 1, and is always qualified: "latest retained success; the retained history may be missing finished runs".
  - "No finished runs are retained" is said only when the retained set itself holds none, and then as a fact about the retained set.
  - The Backups list's notice, "every run in progress is held", is said only when the window is in tier 1.

**Clock:**
- **The deadline:** the earliest expiration among S's candidates is a presentation deadline.
- **Merged into the schedules part's `Next`:** the part is re-evaluated at that time, and the horizon changes without an API event. The roles are unchanged, so S2a's health, ledger and notifications are untouched: an evaluation with the same roles records no transition.
- **Fleet snapshots** carry their capture's time and name it ("as of").

**Where it is computed:** in `internal/velero` (pure), from the same capture as the schedule's verdict. It is carried in `ScheduleVerdict` and its DTO. It is never part of the verdict: the verdict stays S2a's.

**Identity:**
- **A schedule recreated under its name:** S2a's creation rule leaves the old schedule's runs out, so its latest success is never the new schedule's.
- **History that leaves:** expiry and deletion remove Backups from the cluster. A schedule whose every success has expired reads "none retained", which is a fact about the retained history, never "never succeeded".

## 5. Sensitive data

- **Restore messages:** S2a's `FieldRestoreValidation` gains the enumerated forms of §1 as fixed classes. Everything else is withheld whole, with the fixed vocabulary, as S2a §6. `FieldRestoreFailure` already has its two forms.
- **The Restores DTO:**
  - never hooks: the count only;
  - never selector values: counts only;
  - the resource modifier's ConfigMap by name, never read;
  - namespaces and their mapping, which are object names;
  - display strings under the URL rule.
- **AI facts:**
  - the backups list rows' allowlist adds the attribution and `counted`;
  - Restores are not offered to AI Explain in S2b, as they have no verdict;
  - the horizon fields join the schedules allowlist.

## 6. Tests, lab and smoke

- **Unit (`internal/velero`):**
  - **the latest retained success:**
    - proven over named;
    - by completion time, including a run that completed out of order;
    - a newer PartiallyFailed;
    - a synced success never counted;
    - an earlier schedule's success excluded;
    - capped with the margin, with "since W", and with none retained;
    - expiry taking the only success away ("none retained");
  - **revision 1:**
    - an omitted run that started before `W` and completed later, which qualifies the capped selection; the schedule's own history held, which does not;
    - a Completed candidate without `completionTimestamp`, uncapped and capped, starting before and after the newest timed completion, and the same for the newer-PartiallyFailed comparison;
    - an expired success left in the API by a GC-blocked location: not a candidate, listed as awaiting deletion, and its expiration as the presentation `Next`, crossed with no watch update;
    - a proven success re-imported by sync, which is a candidate, and an unproven synced one, which is not;
    - a name-matched success and a name-matched PartiallyFailed, each labelled;
    - the tier-0 window's notice;
    - revision 2: a tier-0 window with a retained Completed candidate, which is selected and qualified, never suppressed; and, in `internal/observe`, the delayed refill that leaves a deleted blocker's tier-0 floor while a retained Backup turns terminal (Codex's probe, kept);
  - **the Restore classes:** every enumerated form, and their near-misses withheld;
  - needles.
- **healthsrc:** the backups rows carry every retained Backup with `counted` and attribution, while the counted items, roles and Top are unchanged, so S2a's tests still pass.
- **appbridge:**
  - `GetVeleroRestores`:
    - the cap, with `remainingItemCount` and a token alone;
    - absent and forbidden;
    - the source join under each Backups coverage (complete, capped, stale, checking, unreadable), separately from a failure to list Restores;
    - the DTO bounds;
    - needles in hooks, selectors, the mapping, a failure reason and the validation errors;
  - `GetBackupHorizon`: a cluster without an Observer, without Velero, capped, and stale;
  - chat and AI read only counted rows as verdicts;
  - the counted rows alone make the Native totals and roles.
- **Frontend:**
  - the column, the horizon block, the Backups tab's filters and notices, and the Restores tab's read, cap and inspector;
  - the fleet Backups view: order, coverage notes, Refresh, and a missing cluster named.
- **Parity:** unchanged. No new part or counted entity. The healthy baseline's yearly schedule reads "none retained" in its new column.
- **The lab (`lab-s2a/apply.sh`, extended):**
  - Restores: a completed one, a failed one with a needle in its failure reason, and one failed validation;
  - a schedule with a proven older success and a newer PartiallyFailed run;
  - a schedule whose only success is synced, which reads "none retained", with the synced one listed.
- **Native smoke:**
  - the Backups tab lists the retained Backups with their attribution;
  - the Restores tab reads;
  - the fleet Backups view leads with the schedule longest without a success;
  - no needle on screen.
- **Live captures:** the Schedules column, the Backups and Restores tabs, a Restore inspector, and the fleet Backups view, in night and day at 1440 and 1024.

## 7. Not in S2b

- **Running a restore, "backup now", pause or unpause:** the roadmap's §3.
- **A configured maximum success age:** S2a §8 offered it here, but it needs a configuration surface the owner has not asked for. It stays a candidate.
- **The backup logs and restore logs in object storage:** a DownloadRequest is a write.
- **BackupRepository and the node-agent's objects.**

## 8. Revision 1: Codex's design review of 1d11926

| # | Finding | Where answered |
|---|---|---|
| 1 | `W` is by run time, so it cannot certify a completion-ordered horizon; the tier-0 notice | §4: capped, the selection is complete only when the schedule's whole history is held, else always qualified; the tier-0 notice is qualified. §6 |
| 2 | A missing `completionTimestamp` became a completion time | §4: never filled in. A later-started one is selected and said; an earlier-started one qualifies the selection, uncapped too, and the newer-PartiallyFailed comparison likewise. §6 |
| 3 | Expiry assumed deletion; the GC can leave expired records | §4: `status.expiration` at capture time decides eligibility; expired records are listed as awaiting deletion; the earliest expiration is a presentation deadline merged into `Next` without touching health. §6 |
| 4 | The Restore source join needed the Backups kind's own coverage | §3: found or not under each coverage, never a capped or stale absence read as "not in the cluster". §6 |
| - | Guidance | §4: confidence in every cell, row and AI field; S2a's `Attribute` precedence. §3: `counted` is membership, gated centrally; display phase separate; the Restores DTO's bounds, fences and `remainingItemCount` |

## 9. Revision 2: Codex's design re-review of 8b6129d

| # | Finding | Where answered |
|---|---|---|
| 1 | A tier-0 window does not prove that no finished Backup is retained: the pending floor survives a delayed refill while a retained Backup finishes | §4: tier 0 certifies no terminal coverage; the selection is made from the retained candidates and qualified; "no finished runs are retained" only when the retained set holds none. S2a's code was already conservative (`covers` and `guardComplete` certify nothing in tier 0); only `Window`'s doc comment overclaimed, and is corrected. §6 |

## 10. As built (2a76827, 1d3a485)

**`internal/velero` (pure):**
- **Restore classes** (`messages.go`): the seventeen `validationErrors` forms of `restore_controller.go` 307-435 (v1.18.4), anchored, nothing re-emitted. v1.17 wrote the hook and init-container errors bare, so they stay withheld whole; v1.17 has no resource-policy form.
- **The Restore projection** (`restore.go`): the §3 caps (mapping 50, included and excluded namespaces 20 each, every string under the URL rule), then cut from the tails (mapping, then excluded, then included) until it measures within 4 KiB. Counts stay whole and `Cut` says a list was cut. The resource modifier and policy are named only when their kind is ConfigMap.
- **The horizon** (`horizon.go`), carried in `ScheduleVerdict.Horizon`:
  - `certainlyAfter` is §4's order rule, and `newest` picks by it;
  - a PartiallyFailed run must also be unexpired to be the newer partial run;
  - with no success, the newest partial run is the fact ("a run partially failed");
  - `Incomplete` is `!covers(creation - 1m)`, so tier 0 and an unknown window certify nothing;
  - `Horizon.Text` and `PartialText` are the wording every surface carries.
- **The clock:** `ScheduleRule` wraps the S2a classifier (now `scheduleRule`), adds the horizon, and merges `NextExpiry` into `Eval.Next`: the Observer re-evaluates the part then, and the roles are the classifier's alone.
- **List attributions** (`Attributions`): by `Attribute`'s precedence. "synced" is only an unowned Backup carrying a current schedule's name that looks synced, as S2a's rail counts them.

**healthsrc and appbridge:**
- **The backups part's rows** carry every retained Backup:
  - a counted row is S2a's member, with its evaluation;
  - a row not counted has a zero `Eval`, which never reaches a DTO;
  - `healthsrc.FactRow` marks the row; the Backups window travels as row facts (`veleroWindow`, `veleroTier0`).
- **The one gate** is `anchorVerdict`: a fact row returns `errFactRow`. AI Explain then explains the object with no verdict and a qualification, and the chat verdict map leaves it out. A zero `health.Kind` reads as fault, which is why the gate is central and the converter emits no rule for a fact row.
- **`VeleroService`:**
  - `GetVeleroRestores(cluster, readID)`: a cluster-wide list, limit 500. NotFound reads not served, Forbidden reads forbidden. `remaining` is set only from `remainingItemCount`.
  - **The source join** reads the `backups` part's own coverage. Its states are found, not-found, not-retained, not-in-last-read, unreadable, none and invalid.
  - `GetBackupHorizon(cluster)`: one capture of the schedules part, as of its evaluation.

**Frontend:**
- **The Velero lens:**
  - Schedules gain "Last success" (`horizonCell`). An age a newer, not retained success could make younger reads "≤", and a name match says "by name". The cron column now drops first at narrow widths.
  - The inspector's horizon block.
  - A Backups tab (facts, phase-group and schedule filters, its own counts, the retained and window notices).
  - A Restores tab (read on open, fenced by cluster and read id, Read again, the cap).
  - The fact and Restore inspectors.
- **The fleet Backups view** (`FleetBackups`, fleet route `backups`, lens index, palette), over `useFleetBatch`.

**Found by the live captures and fixed in 1d3a485:**
- **Kind names:** the source join and the fleet notes looked for the kinds by kind name, while the Observer names a kind by its resource (`backups`, `schedules`). Every source read "the Backups could not be read". The unit test had faked the wrong name.
- **Smaller fixes:**
  - the fleet "Run due" showed a bare clock for another day;
  - the Restore inspector repeated "retained".

**Lab:** `lab-s2b/apply.sh` (scratchpad), over lab-s2a on kind-klyx:
- a schedule with a proven older success (an owner reference) and a newer PartiallyFailed N;
- a schedule whose only success looks synced;
- an expired success left by a GC-blocked location;
- three Restores (completed and joined, failed with the needle, failed validation with the needle).

**Gates:** filled in below as they complete.

## 11. Implementation review 1: Codex's review of 5a2e811

| # | Finding | Fix |
|---|---|---|
| 1 | Switching clusters committed the last cluster's Restores for a frame, until an effect cleared them | `useRestores` shows a read only under the cluster it was made for, decided in render; the reply fence stays |
| 2 | A Backup with no match in an incomplete or unreadable Schedules read was "not a run of a current schedule" | `Attributions` takes the Schedules read's coverage: no match there is "unknown"; a match is kept with its coverage, said in the row, inspector, Restore join and AI fields; "no current schedule" filters only what a complete read proves |
| 3 | A success with neither time read "none retained" and printed a zero date | `Horizon.Text` says both times are not recorded; `horizonCell` reads "retained, time not recorded" (qualified when incomplete) |
| 4 | The fleet view counted unknown history as "no retained success" | Three counts: known none, none in the history Klyx holds (capped), runs unknown |
| 5 | The 4 KiB bound held only for the abbreviated projection, not the serialised DTO with its source join | `fitRestoreDTO` measures the final DTO and cuts from the mapping, the namespaces, the validation classes, then the joined backup's details; counts stay whole; a worst-case test serialises the bridge result |

Codex's probes are kept as `review_s2b_r1_test.go` in velero, healthsrc and appbridge, and as the "review:" cases in `VeleroView.test.tsx` and `FleetBackups.test.tsx`.
