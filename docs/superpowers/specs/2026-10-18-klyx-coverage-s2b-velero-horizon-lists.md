# Coverage S2b: the Velero horizon and lists

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S2b. It builds on S2a (`2026-10-17-klyx-coverage-s2a-velero-health.md`): design accepted at ebcd07d, implementation at bc3be19. Every S2a contract stands, notably:
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
  - **Notices:** a notice says the list is what the cluster retains, bounded by each backup's TTL, and, when capped, that it holds the newest runs since `W` and every run in progress.
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
    - the source backup, joined to the retained Backups: found, not retained, or not in the cluster;
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
- Each row says whether it is counted (S2a §4.3 membership), and the part's counted items, ledger, Top and notifications are unchanged.
- A row that is not counted carries a display role for its phase (Completed none, PartiallyFailed look-at, Failed fault, in progress syncing), its attribution and its timing, and is never Klyx's verdict.
  - The chat verdict map and AI Explain read only counted rows as verdicts. A list row is a fact.
- **The cost:**
  - Rows are published only while the lens is subscribed.
  - At S2a's cap of about 1,500 Backups, a publication is about 900 KB of DTOs, comparable to a large Pods list.
  - The rows are built from the capture the part already takes, so there is no new watch and no new retained model.

**The Restores read:**
- `restores.velero.io` v1, through the cluster's dynamic client, with a 500-item limit and a continue token to count what is past it ("+N more", or "more" when the count is unknown).
- Projected field by field into a bounded DTO, with Velero's text classified (§5) and display strings under the URL rule (S2a's review fix).
- An absent API reads "not served", a forbidden one "not readable", never empty.

**The fleet read:**
- `VeleroService.GetBackupHorizon(cluster)` returns one cluster's schedules rows as horizon rows with that capture's coverage, read through `HealthRowsNow`.
- The frontend fans out over the fleet, as `FleetExpiry` does.
- A cluster without an Observer or without Velero says so.

**Budget:** no new watch and no new retained model. The Restores read retains nothing past the view, and the fleet read retains one reply per cluster while the view is open.

## 4. The latest retained successful backup

**Definition, per schedule S:**
- **The candidates:** S's runs (S2a §3: proven or named, never synced) whose phase is Completed.
- **Proven first:**
  - the newest proven Completed run by `completionTimestamp` (falling back to its run time when absent);
  - only when no proven Completed run is retained, the newest named one, labelled "matched by name".

  A named run can be a foreign backup from a shared bucket (S2a §3's limit), so it never stands in for a proven one.
- **A newer PartiallyFailed run:** the newest proven, else named, PartiallyFailed run completed after that success is shown beside it, "a newer run partially failed", and never counted as a success.
- **Completeness:**
  - **Uncapped:** "none retained" when there is no candidate.
  - **Capped:**
    - a success found whose run time is later than `W` plus a minute is the latest there is (S2a §4.2's margin);
    - otherwise it is labelled "latest retained since W; an older or concurrent one may not be retained";
    - and with none found, "none retained since W".
- **Wording:** "latest retained successful backup <age> ago", never "last backup", "restorable" or "protected". The inspector adds "Completed is Velero's phase; it is not a restore test."

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
  - **the Restore classes:** every enumerated form, and their near-misses withheld;
  - needles.
- **healthsrc:** the backups rows carry every retained Backup with `counted` and attribution, while the counted items, roles and Top are unchanged, so S2a's tests still pass.
- **appbridge:**
  - `GetVeleroRestores`: the cap, "+N more", absent, forbidden, the join to Backups, and needles in hooks, selectors, a failure reason and the validation errors;
  - `GetBackupHorizon`: a cluster without an Observer, without Velero, capped, and stale;
  - chat and AI read only counted rows as verdicts.
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
