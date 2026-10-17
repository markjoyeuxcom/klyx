# Coverage S2a: Velero health

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S2a. S2b (the fleet horizon, and the Backup and Restore lists) follows with its own note.

The owner's direction: "velero should be supported also". The roadmap's S2a asks for background evidence independent of any list being open, three faults (an unavailable storage location, a missed schedule, recent failed and partially failed backups, distinct from each other), named schedule states, and Velero's phases kept as they are. Codex's acceptance carried three details into this note (roadmap §6): formalise the overdue opportunity and its reset after a later success; an in-progress run during the grace period, and incomplete retained history; the exact classifier and its fake-clock cases. All three are in §4.

The slice is read-only. It adds no verb (§5). Revision 1 answers Codex's design review of 4e23d5d (§9), revision 2 its re-review of 9f95194 (§10), and revision 3 its re-review of 47c0dff (§11), and revision 4 its re-review of b676a4f (§12), and revision 5 its re-review of 54cb39a (§13).

## 1. The API

Read from Velero's own sources at v1.18.4 (2026-09-24), the latest release, with history for when each behaviour arrived. Velero supports the current and previous minor (`site/content/docs/main/support-process.md`:7), so 1.18 and 1.17 today. Research notes: `scratchpad/s2-research.md`, cited by file and line there.

**Kinds.** Every Velero kind is `velero.io/v1` and namespaced, at one served version (DataUpload and DataDownload are `v2alpha1`). S2a reads three: Schedule, BackupStorageLocation (BSL) and Backup.

**Three properties that shape everything below:**
- **No status subresource** since v1.9 (`velero.io_schedules.yaml`:606, `subresources: {}`). Status is written by merge patches on the object, so status writes bump `metadata.generation`. No Velero status carries `observedGeneration`. Klyx's generation-based staleness model does not apply to these kinds, and the lab writes status with a plain apply.
- **No Kubernetes Events** for Schedules, Backups, Restores or BSLs. The only event recorders are the data mover and pod-volume micro-services. Evidence for these kinds is their status, and their server logs, which Klyx does not read.
- **One server, one namespace.** The server's controller cache is restricted to its own namespace (`pkg/cmd/server/server.go`:272-274). An object in any other namespace is never processed.

**The server.** A Deployment labelled `component=velero` with a container named `velero`. That is the Velero CLI's own selector (`pkg/cmd/cli/plugin/helpers.go`:31-55), and it holds for both the CLI and the Helm installs. `deploy=velero` exists only in CLI installs. Its namespace defaults to `velero`.
- **Version:** only the container image tag shows it read-only. A ServerStatusRequest would need a create.
- **Time zone:** the container's `TZ`, if set; otherwise the pod's local zone, UTC in Velero's image.
- **Controllers can be switched off** by `--disable-controllers` (schedule, gc, backup-sync and others; never the BSL controller), visible only in the server's args.

**Schedule:**
- **spec:** `schedule` (the cron), `template` (a full Backup spec, with its own `metadata.labels`), `useOwnerReferencesInBackup`, `paused`, `skipImmediately`.
- **status:** `phase` (Enabled or FailedValidation; `New` is defined but never written, and the phase is empty before the first reconcile), `lastBackup`, `lastSkipped`, `validationErrors`. No next-run field and no last result.
- **What the controller does** (`pkg/controller/schedule_controller.go`):
  - **Validation is the cron parse only** (179-205): empty, `invalid schedule: <error>`, or a recovered parser panic. It never checks the template's BSL.
  - **The next run** (275-289): `base` is `lastBackup`, else `creationTimestamp`; `lastSkipped` replaces it when later. The next run is `cron.Next(base)`, due when now is strictly after it.
  - **Cadence:** a reconcile every minute plus on create and spec change (44, 82-89), so a run is created within about a minute of being due.
  - **`lastBackup` is the creation time**, not completion (257-270): the controller creates the Backup, then patches `lastBackup` with the same `now`. It says nothing about success.
  - **No catch-up** (258-259). After downtime, one run fires on the next reconcile and the schedule continues from there.
  - **The overlap guard** (161-169, 215-238): a due run is not created while any Backup labelled with this schedule's name has phase "", New or InProgress (since 1.10; "" since 1.18.0). The run is **deferred**, not skipped, and fires when the blocker leaves those phases. Queued, ReadyToStart, WaitingForPluginOperations* and Finalizing* do not block.
  - **`skipImmediately`** (since 1.13): the controller sets `lastSkipped` to now and writes the field back as false. It is a one-shot, used on unpause to avoid the immediate run that `Next(old lastBackup)` would cause. On first reconcile the controller writes `skipImmediately: false` into every schedule. In v1.18.4 the server flag `--schedule-skip-immediately` applies to only one schedule per server lifetime (an aliasing bug, fixed on main).
  - **`paused`:** a predicate drops paused schedules from every event (since 1.16). Nothing is written. Phase, `lastBackup` and `validationErrors` freeze, and a schedule created paused keeps an empty phase and is never validated.
  - **The run it creates** (291-297, `pkg/builder/backup_builder.go`:85-133):
    - the name is `<schedule>-<now as 20060102150405>`, by the controller's clock in the pod's zone;
    - the labels are the template's (else the schedule's own) plus `velero.io/schedule-name=<name>`;
    - an ownerReference to the Schedule's UID is added **only** when `useOwnerReferencesInBackup` is true.

  `velero backup create --from-schedule` copies the template and the label, so a manual run of a schedule carries its name too.

**The cron library changed in a patch release.** v1.15.0 to v1.18.2 parse with `robfig/cron/v3` v3.0.1. v1.18.3 and later parse with `github.com/netresearch/go-cron` v0.15.0 (commit 25402b62, not in the changelog). Both use `ParseStandard`: five fields, descriptors, `@every`, and a `CRON_TZ=`/`TZ=` prefix. They differ:
- **Day of month with day of week:** go-cron requires both to match when both are restricted; robfig accepts either.
- **Day-of-week 7** is Sunday in go-cron and invalid in robfig. go-cron also allows wraparound ranges.
- **`@triggered`, `@manual`, `@none`** are valid in go-cron, and their next time is the zero time. In robfig they fail validation.
- **The spring-forward hour:** go-cron runs a job scheduled inside the skipped hour at the matching minute after it; robfig drops that day's run.
- **The repeated autumn hour:** both return both occurrences, so a schedule there fires twice.
- **Never-matching specs:** in both, a parseable spec that never matches within five years (`0 0 31 2 *`) gives the zero time.

Velero treats a zero next time as always due. Such a schedule creates a run on every one-minute check that the overlap guard allows.

**Backup:**
- **Phases:** New, Queued and ReadyToStart (the 1.18 queue), FailedValidation, InProgress, WaitingForPluginOperations, WaitingForPluginOperationsPartiallyFailed, Finalizing, FinalizingPartiallyFailed, Completed, PartiallyFailed, Failed, Deleting.
- **Terminal phases:** Completed, PartiallyFailed, Failed and FailedValidation.
- **status:** `startTimestamp` (processing start, after any queue time; absent on FailedValidation), `completionTimestamp` (absent on FailedValidation and on some Failed paths), `expiration`, `errors` and `warnings` (counts; the texts are in object storage), `failureReason`, `validationErrors`, `progress`, `queuePosition`, and counters for hooks, snapshots and item operations.
- **A Velero restart** marks every InProgress backup Failed, "found a backup with status "InProgress" during the server starting", so InProgress is bounded by the server's life.
- **Labels:** `velero.io/schedule-name`, and `velero.io/storage-location` (a truncated form for long names). `velero.io/gc-failure` is set on an expired backup the GC could not delete (BSLNotFound, BSLCannotGet, BSLReadOnly).
- **An unavailable or read-only BSL** (since 1.17): the Backup goes straight to FailedValidation, "backup can't be created because BackupStorageLocation <name> is in Unavailable status" or "is currently in read-only mode". The schedule keeps firing, so each tick adds one FailedValidation run.
- **Backup sync** (`pkg/controller/backup_sync_controller.go`):
  - It creates a Backup for every backup in object storage that is not in the cluster, from the stored copy: labels (the schedule name included), phase and timestamps.
  - Its `creationTimestamp` is the sync time. Its ownerReferences are dropped unless a Schedule with that name and UID exists here.
  - Since 1.18.3 only Completed, PartiallyFailed and Failed backups are synced.
  - **A second cluster sharing a bucket, or a rebuilt cluster, therefore sees another cluster's backups under the same schedule name.** Velero's own `velero_backup_last_successful_timestamp` metric and restore-by-schedule count them alike.
- **GC:** an expired backup is deleted through a DeleteBackupRequest, which needs its BSL present, Available and not ReadOnly. Retained history is about one TTL (default 720h), and expired backups accumulate while their BSL is unavailable.

**BackupStorageLocation:**
- **spec:** `provider`, `objectStorage` (`bucket`, `prefix`, `caCert` (deprecated), `caCertRef` since 1.18), `config` (a provider map), `credential` (a Secret key selector), `default`, `accessMode` (ReadOnly or ReadWrite), `backupSyncPeriod`, `validationFrequency`.
- **status:** `phase` (Available or Unavailable; empty before the first validation), `lastValidationTime`, `lastSyncedTime`, `message` (since 1.9).
- **Validation** (`pkg/controller/backup_storage_location_controller.go`, `internal/storage/storagelocation.go`:48-75):
  - **When it runs:** checked every 10 s. A BSL with no `lastValidationTime` validates at once. Otherwise it validates when `validationFrequency` (or the server's `--store-validation-frequency`, default 1m) has passed. `validationFrequency: 0` stops periodic validation, but a spec change or a server start still validates.
  - **What each run writes:** `lastValidationTime`. A success writes Available with an empty message. A failure writes Unavailable with the message `BackupStorageLocation "<name>" is unavailable: <error>`.
  - **What the message carries:** the provider's error, sanitised since 1.18 only for Azure SAS parameters (`sig`, `se`, `st`, `sp`, `spr`, `sv`, `sr`, `sip`, `srt`, `ss`). Account URLs and container names survive. Before 1.18, which is 1.17 today, SAS tokens appeared verbatim. Other providers' errors are not sanitised.
- **Default:** Velero needs none. With several, the controller keeps the newest and writes `default: false` into the others' spec. A Backup with no `storageLocation` uses the default BSL, else the server's `--default-backup-storage-location` (default "default"). If that BSL is missing, the Backup fails validation.

**VolumeSnapshotLocation:** its `status.phase` is written by nothing (also on main). It carries no health, and S2a reads none (roadmap).

## 2. What Klyx shows

A **Velero** group in the cluster's lens index (capability `velero`), with three views: **Schedules**, **Storage locations** and **Unscheduled backups**, plus registry entries for every Velero kind. Console words: "velero", "backup", "schedule", "backup storage location", "bsl", "restore".

- **Schedules, broken-first.**
  - **Columns:** the verdict, the name, the cron with its zone, "last successful run <age>", "next run due <time>", and the storage location.
  - **The inspector leads with the schedule's own verdict** (InspectorStatus), its reason and what decided it: the run Velero last created, or a run proven by owner reference (§3). Runs matched by name only are history, labelled so.
  - **The run rail,** the slice's drawing. The schedule's recent runs as stations on a line, drawn in The Line's shape vocabulary:
    - a Completed run with no mark, a PartiallyFailed run hollow, a Failed or FailedValidation run filled, a run in progress dashed;
    - the run Velero owes, drawn as the next station at its due time;
    - a missed or deferred due run, drawn as a filled station with no run behind it.

    Each station names its run, phase, creation time and, when present, its error and warning counts. A station past the retained history reads "not retained" (§3).
  - **Velero's own words:** "Velero last created a run at <lastBackup>" (a creation, never a success), and "skipped at <lastSkipped>" when set.
  - **The storage location block,** separately labelled with its own verdict: "Storage location default: Available, validated 40s ago". A healthy location never stands in for a failing schedule, and an unavailable one never replaces the schedule's verdict.
  - **The template, as configured now:** included and excluded namespaces, TTL, the snapshot settings (`snapshotVolumes`, `defaultVolumesToFsBackup`, `snapshotMoveData`), the volume snapshot locations, and "N hooks (commands withheld)" (§6).
  - **Facts that qualify the verdict:**
    - "N backups from object storage carry this schedule's name" (synced, never counted, §3);
    - "Velero's schedule controller is disabled on this server" when the server's args say so;
    - "this schedule is not in a Velero server's namespace".
  - **"Velero records no Kubernetes events for schedules"** in place of an events block.
- **Storage locations.**
  - **Columns:** the verdict, the name, the provider, the bucket and prefix, "validated <age>", whether it is the default, and the access mode.
  - **The inspector:**
    - the verdict, with the message under the rules of §6;
    - `lastValidationTime` and `lastSyncedTime`, each with its age, the validation frequency and the sync period;
    - the credential Secret's name and key, never read; "CA certificate set";
    - the config keys, with values only for `region` and the origin of `s3Url`/`publicUrl` (§6);
    - "several locations are marked default; Velero keeps the newest and unsets the others" when so;
    - **the schedules that target it,** each with its verdict;
    - "N expired backups await deletion" from the retained Backups whose expiration has passed, with the GC failure label's reason when set.
- **Unscheduled backups:** the backups created in this cluster in the last 24 hours that are not runs of a current schedule (§4.3), broken-first.
  - **Columns:** the phase, created, completed, errors and warnings, and storage location.
  - **The inspector:** the failure reason and validation errors (§6), progress, and the counters.

  **S2b replaces this view** with the full Backups and Restores lists.

## 3. The source contract (roadmap C1)

| Part | Acquisition | Projection | Counted entity | Health | Notifications |
|---|---|---|---|---|---|
| `schedules` (task `velero`) | observer watch `schedules` (v1); deps `backups`, `backupstoragelocations` | field by field (below) | the Schedule's UID | §4.2 | eligible |
| `locations` (task `velero`) | observer watch `backupstoragelocations` (v1); dep `schedules` | field by field | the BSL's UID | §4.1 | eligible |
| `backups` (task `velero`) | observer watch `backups` (v1), capped and admitted newest first; dep `schedules` | field by field | the Backup's UID, for the recent unscheduled backups of §4.3 only | §4.3 | eligible |

**Projected fields** (classification and join inputs are never clipped; display strings are clipped first):
- **Schedule:**
  - classification inputs: `spec.schedule` kept whole up to 1024 bytes (go-cron's limit; longer reads "the cron is too long to read", and its time is unknown), `paused`, `skipImmediately`, `useOwnerReferencesInBackup`, `creationTimestamp`, `status.phase`, `lastBackup`, `lastSkipped`;
  - display: the classes and Klyx wordings of at most four `validationErrors` (§6);
  - the template: `storageLocation`, `ttl`, the snapshot settings and `volumeSnapshotLocations`, and the included and excluded namespaces (at most 20 names, with counts);
  - the hook count, never a hook.
- **BackupStorageLocation:**
  - classification inputs: `status.phase`, `lastValidationTime`, `validationFrequency`, `creationTimestamp`, `default`, `accessMode`;
  - display: `provider`, `bucket`, `prefix`, `caCertRef`'s name, whether `caCert` is set, `credential`'s name and key, `backupSyncPeriod`, `lastSyncedTime`, and the class and Klyx wording of `message` (§6), never its text;
  - the config's keys (at most 32), with values only for the allowlist of §6.
- **Backup** (revision 1: a checked total, below):
  - classification inputs: `creationTimestamp`, `status.phase`, `startTimestamp`, `completionTimestamp`, `expiration`;
  - join inputs, kept whole once validated (below): the name, the `velero.io/schedule-name` label, at most two ownerReferences whose kind is Schedule (UID and name), and `spec.storageLocation`;
  - display: `errors`, `warnings`, the counters, `progress`, `queuePosition`, and the classes of `failureReason` and of at most four `validationErrors` (§6), plus the `velero.io/gc-failure` label;
  - never the spec's hooks or selectors, and never Velero's text (§6).

  **The total is bounded by Klyx's own validation** (revision 2, Codex's re-review finding 4). Not every input has an API limit: the CRD gives `spec.storageLocation` no `maxLength`, and the API server checks an ownerReference's name and UID only for being non-empty. So Klyx bounds each join input itself, and records what fails instead of keeping it:
  - **Limits the API server enforces:** the object's name (253 bytes), its namespace and every label value (63). These are kept as they are.
  - **`spec.storageLocation`:** kept when it is a valid object name of at most 253 bytes. Otherwise it is replaced by the marker "invalid location name", whose join to a BSL reads "invalid" (no BSL can carry such a name).
  - **A Schedule ownerReference:** kept when its UID is a well-formed UUID and its name a valid object name of at most 253 bytes, two at most. Otherwise the owners are marked **incomplete**, and an incomplete owner set proves nothing either way (§3 identity).
  - **Timestamps, counters and phase:** fixed-size, or an enum.
  - **Each class:** a fixed string of Klyx's (§6).

  The worst case is then under 3 KiB. Each projection is still measured after it is built (`Size`) against `MaxObject` 4 KiB. A projection over it is left out like any other, and the window covers it.

**Versions:** each kind is probed for `v1`. A cluster serving none reads it as absent; one serving another version reads "unsupported Velero API version", never empty.

**Identity: which Backups are a schedule's runs.** The roadmap: "a schedule recreated under the same name is a new schedule (UID), so the schedule-name label alone does not prove association". Revision 2 drops revision 1's clock calibration (Codex's re-review finding 3). A found `N` can be a copy that backup sync re-created long after the run, so `N.creationTimestamp − lastBackup` measures nothing. Timing therefore never decides a verdict on its own; the evidence is graded instead.
- **The run Velero last created**, `N`, is identified by name: `<S>-<lastBackup formatted 20060102150405 in the server's zone>`. That is the controller's own naming of the run it recorded in status.
  - A copy re-created by backup sync carries the run's own stored phase and timestamps, so it is the same run's record.
  - When the server's zone is unresolved (§3 controllers), `N` cannot be identified, and §4.2 says so.
- **Proven runs of S:** `N`, and every Backup in S's namespace with an ownerReference carrying S's UID. Backup sync keeps a Schedule owner only when that same UID exists here (§1), so a proven run is S's own. **Only proven runs decide a schedule's verdict** (§4.2 row 10).
- **Named runs of S:** a Backup in S's namespace with all of the following:
  - a complete owner set (§3 projection) with no Schedule owner to another UID. An incomplete set cannot prove that negative, so its Backup is never named;
  - its `velero.io/schedule-name` label equal to S's name;
  - an execution time (below) no earlier than one minute before S's `creationTimestamp`, which excludes the runs of an earlier schedule of the same name. A re-imported record keeps its old execution time, so revision 3 no longer uses its new `creationTimestamp` here;
  - and it does not look synced (below).

  A named run is evidence for the schedule's history (the rail, S2b's horizon) and for the previous outcome while `N` is in progress (§4.2). It never clears a proven run's failure.
- **Looks synced:** processing that started more than 2 minutes before the object existed here, `startTimestamp < creationTimestamp − 2m`.
  - **Why 2 minutes:** it absorbs plausible skew between the Velero server's clock and the API server's.
  - **Between 5 s and 2 minutes:** the timing is disputed. The Backup counts where it would otherwise count, with "may have been synced from object storage".
  - **Never silent:** a Backup that looks synced is not counted, and the views list it as "not counted: looks synced from object storage", with both timestamps.
  - **Never applied to a proven run.**
- **A run's execution time** orders runs (revision 3, Codex's second re-review finding 2). Backup sync re-creates an older record with a new `creationTimestamp` but its stored run evidence. So the object's lifetime is not the run's, and creation never orders runs:
  - `startTimestamp` when present: the server's clock when processing began, kept through sync;
  - else, for a run that never started (FailedValidation), the timestamp in its name when the name is the controller's form for S (`<S>-<14 digits>`, read in the server's zone);
  - else `lastBackup` for `N`;
  - else **unknown**. Such a run is a fact, never a deciding or previous outcome.

  `N`'s execution time is its `startTimestamp`, else `lastBackup`.
- **A Backup's run time** (revision 4, Codex's third re-review findings 1 and 3) is its `startTimestamp`, else its `creationTimestamp`. One time basis serves:
  - the cap's tier-1 order;
  - the 24-hour membership of §4.3, and its clock;
  - the completeness check of §4.2 row 10.

  It is a function of the projection alone, as a rank must be. For a run that started, it equals the execution time. For a local run that never started, it is the creation, which the controller's name timestamp matches to within the clocks' skew.
- **The limit,** said in the inspector when the schedule has no owner references: a bucket shared with another cluster that runs a same-named schedule can bring that cluster's backups in as named runs. They can shape the rail, never the verdict. `useOwnerReferencesInBackup: true` makes every run proven.

**The Backups cap: newest first (a change to the Observer, Stage 1).**
- **The problem:** the budget admits projections in key order (namespace/name, `observe/kind.go` `admit`). For Backups that keeps the alphabetically first schedules' old runs and drops other schedules' newest ones.
- **The change:** `KindSpec.Rank`, a function of a projection. The budget admits, evicts and blocks in rank order instead of key order, and the key breaks ties.
  - Every other kind keeps rank = key, so nothing else moves.
- **The Backups rank, in two tiers** (revision 2, Codex's re-review finding 2):
  - **Tier 0:** every Backup in a phase Velero's overlap guard reads ("", New, InProgress).
  - **Tier 1:** every other Backup.
  - Within each tier, **run time** descending, then the key (revision 4, Codex's third re-review finding 1).

  So the guard's evidence is retained first and in full: a blocker is never left out while any terminal Backup is retained. A new Backup is created in tier 0 and ranks first. The terminal Backups that ran longest ago are left out.

  **Why run time, not creation:** Velero 1.18's queue lets a later backup that does not conflict pass an earlier blocked one (`backup_queue_controller.go`:161-195, 278-293). An older-created Backup can therefore start after a newer one. Under creation order it could fall out of tier 1 while it is the latest execution. Under run-time order the retained tier-1 set is exactly the latest executions.
- **A rank can change** when a phase moves a Backup between tiers. The watch path re-places the object in rank order:
  - **A retained object that now ranks past the run's end** is left out, and the room it frees can refill.
  - **A left-out object that now ranks inside the run** is admitted, evicting from the end.
  - **The blocker changing rank** relists, as any change a list could admit differently does. **Until that list completes, the recorded boundary stays at the earlier of the blocker's old and new ranks** (revision 3, Codex's second re-review finding 4). Objects omitted past the old boundary are not individually known, and one of them could rank anywhere after it. The refill is asynchronous and rate-limited, and the kind stays live meanwhile. So the window, and with it any tier-1 claim, never advances past the old boundary before a complete list establishes the new one.

  The admission invariant (the retained set is the longest rank-ordered run that fits) holds after each.
- **The window:** the capture carries, for a capped kind, the rank of its first left-out object in rank order, **whatever left it out**: the budget's blocker, or a projection over `MaxObject` (revision 1, Codex's finding 6). Admission records the rank of each oversized omission for this. By construction there are none for Backups (above).
  - **A window in tier 1,** the normal case, has a run time `W`. Every tier-0 Backup is retained, and so is every Backup whose run time is after `W`.
  - **A question about runs that ran after `W`** has a complete answer; one that reaches before `W` is "not retained".
  - **Velero's guard, a negative over all history** (every Backup carrying the label, old-UID runs included), is answered completely by tier 0, whatever `W` is.
  - **A window in tier 0** means more blocking Backups than the budget holds. The guard is then unresolved, and §4.2 row 9 says so.
  - **A shrunk oversized object** is admitted on the next refill, as today, and the window moves with it.

**Controllers (C1's controller task).**
- **Finding the servers:** a cluster-wide list of Deployments labelled `component=velero` with a container named `velero` (as Reloader's detection lists cluster-wide). It gives each server's namespace, readiness, the image tag's version, the container's literal `TZ`, and whether `schedule` is among `--disable-controllers`. Klyx reads no other environment variable, argument or value, and never resolves a `valueFrom`.
- **Configuration is not the running server's until it has rolled out** (revision 1). The version, zone and disabled-controller facts come from a Deployment only when:
  - `status.observedGeneration` equals its generation;
  - and its updated, available and desired replicas are equal.

  During a rollout they are "unresolved (rollout in progress)". So are two servers in one namespace whose facts disagree, and a `TZ` set by `valueFrom`. An unresolved fact makes only the states that need it unknown (§4.2), never a fault.
- **The task `velero`:**
  - not ready when a found server is not ready;
  - unknown when the list is unreadable, or when the CRDs are served and no server is found. A retained CRD is not an active install (C2);
  - healthy otherwise.
- **The facts** (server namespaces, versions, zones, the disabled schedule controller, and whether the list was read) reach the parts through the Observer's facts, as `FactFluxPresent` does today.

**Dependency completeness.**
- **The schedule's verdict depends on its runs.** That is the point of the part. Backups unreadable make the run-based states unknown (§4.2), never healthy.
- **No verdict depends on a BSL.** The storage location block is shown and never folded in.
- **A BSL's schedules,** a schedule's location and the expired backups are joins from the same capture. Missing reads "not found", unreadable "not readable", and past the cap "not retained". None is ever read as healthy.

**Clocks:** five deadlines (§4.4), each published as the part's `Next`.

**The budget** (S1a §3's mechanism):
- three watches;
- per-object maximum: 4 KiB, checked, for all three kinds;
- per cluster: Schedules 128 KiB, BSLs 64 KiB, Backups 768 KiB (about 1,500 Backups at a typical 500 bytes), 960 KiB together;
- ten clusters: **9.4 MiB** encoded, about 52 MiB of heap at S1a's calibration of 5.5 heap bytes per encoded byte.

  Past the Backups budget, coverage reads "capped: the newest N retained since <W>".

## 4. Health rules

### 4.1 Storage locations

First match wins (revision 1: the overdue rule precedes Available explicitly).

| # | State | Condition | Role |
|---|---|---|---|
| 1 | Not processed | the BSL's namespace has no Velero server, and the server list was read | Degraded, "not in a Velero server's namespace: Velero processes only its own" |
| 2 | Unavailable | `phase` Unavailable | Fault, "Unavailable: <the message's class>" (§6) |
| 3 | Validation overdue | `phase` Available, `validationFrequency` declared and above 0, and now ≥ `lastValidationTime` + 2 × frequency + 5m | Degraded, "validation overdue: last validated <time>" |
| 4 | Available | `phase` Available | OK, "Available, validated <age> ago" |
| 5 | Awaiting validation | `phase` empty, created under 5 minutes ago | Progressing, "awaiting Velero's first validation" |
| 6 | Not validated | `phase` empty, created 5 minutes ago or more | Degraded, "not validated by Velero" |

- **The overdue rule applies only to a declared frequency.** The server's default comes from a flag Klyx does not read, so an Available BSL without one keeps its age as a fact.
- **Facts, never roles:** ReadOnly mode (a restore-only location is intended), several defaults, and expired backups awaiting deletion.

### 4.2 Schedules: the classifier

**Inputs:**
- the schedule: UID, creation, cron, `paused`, `phase`, `validationErrors`, `lastBackup`, `lastSkipped`;
- its runs (§3) and the Backups window `W`;
- the server facts for its namespace: zone, version, disabled controller, and whether the list was read;
- the resume tracker's state (below);
- now, from the injected clock.

**Velero's own due time.** `base` = `lastBackup`, else `creationTimestamp`, replaced by `lastSkipped` when later. `D` = `Next(base)`, computed:
- **with the library Velero uses:** robfig for 1.15 to 1.18.2, go-cron for 1.18.3 and later;
- **in the zone Velero uses:** the `CRON_TZ=`/`TZ=` prefix, else the server's `TZ`, else UTC.

`D` is the run Velero owes. Velero creates it within about a minute of `D` unless it is blocked or down.

**Which library, when the version is unresolved.** Klyx computes `D` with both.
- **If both parse and agree,** `D` stands. They disagree only for the forms of §1.
- **Otherwise,** including when only one of them parses, the schedule is Unknown: "its next run depends on Velero's version, which Klyx could not resolve".
  - Revision 1 drops the inference that a phase of Enabled names the library (Codex's finding 5).
  - The phase carries no observed-spec identity. A spec edited while the server was down keeps an Enabled phase from the old one, and Velero itself revalidates every reconcile rather than trust it (`schedule_controller.go`:120-132).
- **A known version below 1.15** is Unknown, "Velero before 1.15 uses another cron library".

**The grace `G` is 10 minutes.** It covers the controller's one-minute cadence and a server restart. The controller task names a longer outage.

**The verdict, first match wins:**

| # | State | Condition | Role |
|---|---|---|---|
| 1 | Not processed | the schedule's namespace has no Velero server, and the list was read | Degraded, "not in a Velero server's namespace" |
| 2 | Paused | `spec.paused` | Paused. Velero does not validate a paused schedule; if Klyx's parsers reject its cron, "will fail validation when resumed" |
| 3 | FailedValidation | `phase` FailedValidation | Fault, with the validation's class: the empty cron, or Klyx's own parse error (§6) |
| 4 | Awaiting validation / not processed | `phase` empty: under 5 minutes since creation, or past it | Progressing "awaiting Velero's first validation" / Degraded "not processed by Velero" |
| 5 | Unknown time | `D` cannot be computed (version, zone or parse, above) | Unknown, with the reason |
| 6 | Never matches | `D` is the zero time | Degraded, "this cron never matches a time: Velero starts a run on every check" |
| 7 | Resumed | the resume tracker is Awaiting or Unproven, within `G` of the first capture that showed this suspension's schedule enabled | Progressing, "resumed: awaiting Velero's first run" |
| 8 | Runs unreadable | the Backups kind is unreadable | Unknown, "runs not readable" |
| 9 | Missed / deferred | now ≥ `D` + `G`, so Velero has not created the run due at `D` | see below |
| 10 | Last run | otherwise (no run is overdue) | the outcome below |

**Row 9, missed or deferred.** Velero's own guard decides which: a Backup in the namespace labelled with this schedule's name in "", New or InProgress. The guard reads the label alone, old-UID runs included, so Klyx does too here.
- **Klyx's evidence for the guard is complete** unless the Backups window is in tier 0 (§3). Revision 2 keeps every guard-phase Backup first (Codex's re-review finding 2).
- **A retained blocker:** deferred, Degraded, "the run due at D waits for <backup> (Velero defers while one runs)".
- **No blocker, the guard's evidence complete:** missed, Fault, "Velero created no backup for the run due at D". Add "the schedule controller is disabled on this server" when so.
- **No retained blocker, the guard unresolved** (more Backups in "", New or InProgress than the budget holds): Degraded, "overdue: Velero created no backup for the run due at D, and whether it is deferring it behind a run in progress cannot be told: more runs are in progress than Klyx holds".
  - Degraded is the floor of the two outcomes the complete evidence could give (deferred, Degraded; missed, Fault).
  - So less evidence never raises the severity, and never lowers it below what is certain: the run is overdue either way.

**Row 10, the outcome of the last run.** It is anchored on `N`, the run Velero last created (revision 1). Only proven runs decide (revision 2). Revision 3 orders runs by their own execution time (§3), lets a proven run decide before the first automatic run, and gives a proven failure precedence over any unproven success (Codex's second re-review, findings 1 to 3).
- **`N` first:**
  - **Without `lastBackup`:** the outcome rule below runs over the proven runs alone, manual ones with owner references included. A proven manual run that failed before the first scheduled opportunity lights the schedule. With no proven run, Progressing, "awaiting its first run, due D".
  - **`N` found:** the outcome rule below.
  - **`N` not found, within one minute of `lastBackup`:** Progressing, "run created at T, not yet seen". The run's watch can trail the schedule's.
  - **`N` not found, after that:**
    - **"the run Velero created at T is not retained"** when the Backups window could have left `N` out:
      - the window is in tier 0, so no terminal Backup is retained;
      - or it is in tier 1 and `lastBackup` is no later than `W` plus one minute (`lastBackup` is the server's clock, `W` the API server's).
    - **"the run Velero created at T cannot be identified (the server's zone is unresolved)"** when the zone is unresolved;
    - **otherwise "the run Velero created at T is not in the cluster (deleted or expired)".** The window proves `N` would be retained in any tier.

    Each is Unknown. A retained older run is shown beside it as a fact, never as the latest outcome. This holds whether or not older runs are retained: a surviving older success never stands for a missing newer run, and a deleted newer success never resurrects an older failure.
- **The outcome rule.**
  - **The deciding runs:** `N`, and every proven run whose execution time is later than `N`'s. Without `N`, every proven run. The newest terminal one, by execution time, decides.
  - **Complete only when the cap cannot hide a later execution** (revision 4). With Backups capped, that needs:
    - a window in tier 1;
    - and `N`'s execution time (without `N`, the time the question starts from: the schedule's creation) later than `W` plus one minute. The minute absorbs the skew between the server's clock (an execution time) and the run time a rank was built from.

    Otherwise a later execution could be left out, and the outcome is Unknown, "a run after <time> may not be retained", with the retained outcome shown as a fact.
  - **The previous outcome:** when every deciding run is still in progress, it decides with "a new run is in progress":
    - the newest proven terminal run before `N`, by execution time. A retained one is the newest there is: anything the cap left out ran before `W`, and so before it;
    - **only when none is retained and the Backups are complete** (not capped), the newest named terminal run before `N`, with "matched by name" (and its timing qualifier, §3);
    - **when none is retained and the Backups are capped**, Unknown: "the run before <N's time> may not be retained" (revision 5, Codex's fourth re-review finding 1). A proven run before `W` could be the true previous outcome, so a named record is never promoted into its place, and retention alone never changes the verdict uncapped evidence would give.

    A proven failure is therefore never cleared by a named success, newer or not. With `N` in progress after a proven failure at 10:00 and a named success at 10:30, the 10:00 failure decides.
  - **Order unknown:** a run whose execution time cannot be established (§3) never decides. It is shown as a fact, "order unknown".
  - **Stays lit:** a schedule stays lit until a proven run succeeds, including while a newer `N` runs.

  | Deciding run | Role and wording |
  |---|---|
  | Completed | OK, "last run completed <time>" |
  | Failed or FailedValidation | Fault, "the last run failed: <its class>" (§6) |
  | PartiallyFailed | Degraded, "the last run partially failed: N errors"; distinct from Failed, and lit the same way until a success |
  | none terminal, some in progress, no previous outcome | Progressing, "first run in progress" or "run in progress" |
  | none terminal retained before an in-progress `N`, Backups capped | Unknown, "no finished run retained since W" |

- **Named runs never decide** while a proven run can. That covers a manual `--from-schedule` run without owner references, and a foreign backup with the schedule's name. Those newer than `N` are shown as facts, "a backup named for this schedule, run at T, Completed (not counted: not proven to be this schedule's run)", and counted in §4.3 if they failed.
- **Never counted:** Backups that look synced (listed as such, §3), and the runs of an earlier schedule of the same name.

**The reset.** A later proven Completed run, by execution time, ends a failed or partially failed state at once: it becomes the deciding run. A re-imported older record keeps its old execution time, so it never reorders the history (§3). A missed state ends when Velero creates the owed run, because `lastBackup` passes `D`, and the new run is then judged by the outcome rule. Nothing older than the deciding run lights.

A manual `--from-schedule` run never moves `lastBackup`, so it never ends a missed state. The controller still owes the run at `D`, and a schedule whose controller does not fire is not working, whatever a person ran by hand. The inspector shows both.

**An in-progress run during the grace period.** Velero creates the owed run at `D`, so `lastBackup` passes `D` and row 9 does not apply. The run is in progress, and row 10 shows "run in progress" over the previous outcome. A run that overruns into the next `D` blocks it (the overlap guard), and row 9 then reads deferred, Degraded, not missed.

**Incomplete retained history.**
- With the newest-first cap, the newest runs are always retained. Row 10 checks `N` itself, and then needs only the newest terminal run.
- What the cap can hide is said where it matters: the guard when tier 0 overflows (row 9), `N` before `W`, and a terminal run before `W` (row 10).
- The rail marks stations before `W` "not retained".

**The resume tracker** (`internal/gitops/resume`): the sample is the Schedule's UID. "Disabled" is `spec.paused`, and the fingerprint is the SHA-256 of `phase`, `lastBackup` and `lastSkipped`.
- **Why it is needed:** on unpause, `Next(old lastBackup)` is in the past, so without it row 9 would read missed until Velero acts. Velero acts within seconds of the spec change: it creates the run, or sets `lastSkipped` when unpaused with `--skip-immediately`.
- **The grace starts at the resume, not the pause** (revision 1, Codex's finding 4).
  - A paused schedule can sit unchanged for hours, so its last paused capture says nothing about when it was resumed.
  - The tracker records, per baseline, the first capture that showed the object enabled since that suspension (`ResumedAt`), and keeps it across later captures until evidence ends the resume.
  - Row 7 holds for `G` from `ResumedAt`.
- **Unproven** (a baseline the tracker let go of while the object was out of its reads) is handled as Awaiting, with its own `ResumedAt`: the first capture that read it Unproven. A resume Klyx cannot rule out gets the same grace, never a fault in the first `G`.
- **What ends it:** evidence (a changed fingerprint), or `G` after `ResumedAt`. Then the rules apply.
- **A Klyx that never saw the pause** has no baseline. It applies the rules, as Velero has long since acted.
- **The tracker's other users** (the Flux Operator's kinds, ArtifactGenerators) gain only the accessor, and their behaviour is unchanged.

**Named states, each tested** (the roadmap's list): new (rows 4 and 10), paused (2), FailedValidation (3), `skipImmediately` (`lastSkipped` moves `base`, so no run is owed), a backup in progress (10, and 9 deferred), and no known success (10: never a fault on its own; a failed run or a missed run is).

### 4.3 Unscheduled backups

- **The counted entity:** a Backup that does not look synced (§3), with a **run time** (§3: `startTimestamp`, else `creationTimestamp`) in the last 24 hours, that is one of:
  - neither a proven run of a current schedule nor a named run whose execution time is at or before that schedule's `N` (superseded by the controller's own run);
  - **a proven run whose execution time is unknown** (revision 4, Codex's third re-review finding 2). For example, a custom-named `--from-schedule` run that failed validation, with no start and no controller timestamp, before the schedule's first automatic run. It cannot decide the schedule's verdict (§4.2), so it is counted here until it is superseded, or its 24 hours pass. **Only a terminal run supersedes it** (revision 5, Codex's fourth re-review finding 2): a proven run that has finished, with a known execution time later than its creation plus one minute. A run that has only started leaves it counted, so a known failure always lights one of the two parts until a finished run replaces it. So a named run newer than its schedule's `N`, or of a schedule with no `N`, is counted here until a newer `N` supersedes it, and an older proven failure never lights here after a later run. That covers manual backups, runs of a deleted schedule or of an earlier one of the same name, and a Backup whose owner set is incomplete ("not attributed: its owner references are not well-formed"). One with disputed timing counts, with "may have been synced from object storage". "Recent" is the roadmap's word; 24 hours is Klyx's rule, and the view says so.
- **Roles:**

  | Phase | Role |
  |---|---|
  | Completed | OK |
  | FailedValidation, Failed | Fault, with the failure reason or validation error |
  | PartiallyFailed | Degraded, with the error count |
  | the non-terminal phases | Progressing; the PartiallyFailed variants say "partially failed so far" |
  | Deleting | Progressing |

  A Backup outside a server's namespace (list read) is Degraded, "not processed".
- **Leaving:** a backup leaves the part 24 hours after its run time, the same basis as its membership (revision 4, Codex's third re-review finding 3). That is a clock (§4.4). Older backups are history, listed in S2b.
- **Never counted here:** Backups that look synced. They may belong to another cluster. The view lists the last day's as "not counted: looks synced", with the timestamps that decided it, and S2b lists them all.

### 4.4 Clocks

| Deadline | Part | Effect |
|---|---|---|
| `D` + `G` (and the next `D` after a run) | schedules | the missed or deferred state can begin |
| creation + 5m with an empty phase | schedules, locations | "not processed" / "not validated" |
| `lastValidationTime` + 2 × frequency + 5m | locations | validation overdue |
| run time + 24h (`startTimestamp`, else `creationTimestamp`) | backups | the backup leaves the part |
| `ResumedAt` + `G` | schedules | the resume grace ends |
| `lastBackup` + 1m | schedules | an unseen `N` reads unknown |

- **Each deadline is the part's `Next`,** re-evaluated by the clock as ESO's staleness is. A crossed deadline is the role's `Boundary`, never its `Since`.
- **Clock sources:**
  - `D` is computed from Velero's own `lastBackup` (the server's clock) and the cron, then compared with Klyx's clock.
  - Skew between the server and Klyx shifts the deadline by the skew. The 10-minute grace absorbs ordinary skew, and a missed state names both `D` and `lastBackup`.
  - Klyx's time-zone database (Go's, with `time/tzdata` embedded for Windows) can differ from the server's image in rare zone changes. The inspector names the zone it used.

## 5. Verbs (roadmap C4)

None. S2 is read-only (roadmap). Velero's own day-2 verbs, pause and unpause, are not offered. "Backup now" and restore are outside the programme (roadmap §3).

## 6. Sensitive data

- **No credential values.** BSLs name a credential Secret, a CA Secret (`caCertRef`) and, for older installs, an inline `caCert`. Klyx shows the names and "set", never a value, and never reads a Secret.
- **The Velero message rule, `crd.VeleroMessage`, fails closed** (revision 1, Codex's finding 1).
  - **Why:** a redaction pipeline cannot bound a provider plugin's error text. Velero passes object-store `Init` and `ListCommonPrefixes` errors through unchanged (`pkg/persistence/object_store.go`:202-203, 219-222; `pkg/plugin/framework/object_store_server.go`:67-68). Codex showed "invalid configuration value: <secret>" and "Authorization: Bearer <secret>" surviving SAS redaction and the URL rule.
  - **The rule:** Velero's text is never shown or sent. Each message is classified, and only the class and Klyx's own wording for it cross, as S1b's automation messages do.
  - **Which fields:** BSL `status.message`; Backup `status.failureReason` and `status.validationErrors[]`; Schedule `status.validationErrors[]`; and Restore's `failureReason` and `validationErrors[]` (read in S2b, registered now).
- **The classes,** each matched against the whole message by a pinned regular expression. The forms are enumerated from v1.18.4's sources at build time and pinned by tests.
  - **Nothing is re-emitted from a match** (revision 2, Codex's re-review finding 1). A provider can return text that forges a whole fixed form, such as "unable to locate ObjectStore plugin named <secret>", and Velero adds the real prefix. A captured name has no provenance, whatever its grammar.
  - **So a class is a fixed string of Klyx's.** A name it needs comes from an object field Klyx reads independently, never from the message: the BSL's `spec.provider`, its `objectStorage.bucket`, the Backup's `spec.storageLocation` and `spec.volumeSnapshotLocations`.
  - **What a forged form can do:** mislead the class (a false diagnosis), never disclose. Every class says it is "as Velero reported".
  - **BSL,** after the fixed prefix `BackupStorageLocation "<name>" is unavailable: `:

    | Velero's form | Klyx's class (fixed) | Field shown beside it |
    |---|---|---|
    | `backup storage location does not use object storage` | "no object storage configured" | none |
    | `object storage provider name must not be empty` | "no provider configured" | none |
    | `backup storage location's bucket name "…" must not contain a '/' …` | "the bucket name contains '/' (use the prefix)" | `objectStorage.bucket` |
    | `cannot specify both caCert and caCertRef in objectStorage` | "both caCert and caCertRef are set" | none |
    | `error getting CA certificate from secret: …` | "the CA certificate's Secret could not be read" | `caCertRef`'s name |
    | `unable to get credentials: …` | "the credentials could not be read" | `credential`'s name and key |
    | `Backup store contains … invalid top-level directories: …` | "the bucket holds top-level directories Velero does not own" | `objectStorage.bucket` and `prefix` |
    | `unable to locate ObjectStore plugin named …` (`pkg/plugin/clientmgmt/process/registry.go`:252) | "the provider's plugin is not installed" | `spec.provider` |

  - **Backup `validationErrors`,** each a fixed class with the Backup's own fields beside it:
    - the read-only and Unavailable BSL forms (§1): "the storage location is read-only" and "the storage location is Unavailable", with `spec.storageLocation`;
    - the missing default and server-default BSL forms: "no storage location was named and the default does not exist";
    - `error getting backup storage location: …`: "the storage location could not be read";
    - the three VolumeSnapshotLocation forms: "a volume snapshot location is missing", "could not be read" and "more than one per provider", with `spec.volumeSnapshotLocations`;
    - `invalid resource policies: …`: "the resource policy is invalid";
    - the old-filter-parameters notice: "old and new resource filters are mixed".
  - **Backup `failureReason`:**
    - the server-start sweep: "the server restarted while this backup ran";
    - `backup execution failed: …`: "the backup failed while running".
  - **Schedule `validationErrors`:**
    - the empty-cron form: "the cron is empty";
    - `invalid schedule: …`: Klyx shows **its own** parse error from the library the server uses (both libraries' errors when it is unresolved), never Velero's tail. Klyx's parse error is computed from `spec.schedule`, a field it reads and shows anyway.
- **Unclassified text is withheld whole:** "Velero's message is withheld: provider errors can carry credentials". The message's presence and its field are kept, so the verdict still says what failed: the BSL is Unavailable, or the run failed or failed validation.
- **A diagnosis from a fixed vocabulary, never from the text.** A withheld message, or a withheld tail, is scanned for whole tokens from a fixed list, and only the matched list entry crosses: "the provider reported AccessDenied", "network: connection refused".
  - **Provider error codes:**
    - S3: `AccessDenied`, `NoSuchBucket`, `InvalidAccessKeyId`, `SignatureDoesNotMatch`, `ExpiredToken`, `InvalidToken`, `RequestTimeTooSkewed`, `PermanentRedirect`, `AuthorizationHeaderMalformed`, `NoCredentialProviders`;
    - Azure: `AuthenticationFailed`, `AuthorizationFailure`, `AuthorizationPermissionMismatch`, `ContainerNotFound`, `AccountIsDisabled`, `InvalidAuthenticationInfo`;
    - GCS: `storage: bucket doesn't exist`.
  - **HTTP status codes** in the forms `StatusCode: <3 digits>` and `status code: <3 digits>`.
  - **Network classes:** `no such host`, `connection refused`, `i/o timeout`, `context deadline exceeded`, `x509: certificate signed by unknown authority`, `x509: certificate has expired`.

  Nothing else from the text is emitted, so a secret on either side of a token never crosses.
- **Where it runs: every path that carries these fields.**
  - **The new lens:** its rows, inspectors and rails, and the health reasons (`CapReason` input).
  - **Health and AI:** chat (get, list and events tools), and AI evidence and facts.
  - **The generic paths, through the S0b registry below:**
    - the generic detail's Manifest and Fields;
    - the list's local cells. A column whose JSONPath reaches one of the fields reads the class;
    - Describe, **withheld whenever one of these fields is non-empty**. Kubectl's text has no field boundaries Klyx could trust, so a payload-free BSL with a message is held too;
    - the guarded editor (refused while a value is present, as for S0b payloads);
    - cache diffs (the class, never the text).
  - **Events:** any Event whose subject is a Velero kind is withheld whole by the event classifier (S1c's `EventMessage`). Velero itself records none for these kinds, so such an event comes from another tool and its text is unbounded.
- **The S0b registry gains the Velero kinds,** so these surfaces are handled on every path (the generic detail, Describe, the guarded editor, cache diffs, chat, AI), not only in this slice's projection:
  - the message fields above: classified, as a new registry shape, "Velero message";
  - Backup `spec.hooks`, Schedule `spec.template.hooks` and Restore `spec.hooks`: opaque. Hooks carry exec commands, and a Restore's init hooks embed whole containers with environment values.
  - BSL and VolumeSnapshotLocation `spec.config`: free-form values (keys kept, values omitted). Provider config is arbitrary and can embed credentials in a URL.
  - BSL `spec.objectStorage.caCert`: opaque.
- **The BSL config allowlist for display:** `region`, and the origins of `s3Url` and `publicUrl`. Every other key shows by name only.
- **AI facts:**
  - allowlisted: kind, verdict, phases, the cron and zone, times, counts, the storage location's name, provider, bucket and prefix, and the messages' classes and Klyx wordings;
  - excluded: config values, Secret names, hooks and selectors.

## 7. Tests, lab and smoke

- **Stage 1, the Observer's rank:**
  - newest-first admission across a list, a watch add (a new Backup evicts the oldest), an update (rank unchanged, size changed), a delete (refill), and ties by key;
  - the window `W` in the capture;
  - an oversized newest Backup: it is left out, the window covers it, and it is admitted again after it shrinks (revision 1);
  - **a rank that changes** (revision 2): a retained object moving past the run's end leaves and refills, a left-out object moving into the run is admitted and evicts from the end, and the blocker's change relists. A list after each admits the same set;
  - the Backups' two tiers: an InProgress Backup older than every retained terminal one stays retained, and leaves its tier when it finishes;
  - **a tier-changing blocker under a delayed refill** (revision 3): A and B retained in tier 0, C (the blocker) and D omitted in tier 0. C finishes and moves to tier 1 while the refill waits out `RefillEvery`. Every capture in between keeps the window at C's old tier-0 rank, so the guard reads unresolved, and the list then establishes the new window;
  - every existing budget test unchanged (rank = key).
- **Unit:**
  - **parsing:** each kind at v1.18.4's shapes and at 1.17's (no Queued phases, no `caCertRef`); a schedule created paused (empty phase);
  - **the due time,** with both libraries:
    - five fields; `@daily`; `@every 90m` anchored to `lastBackup`; `CRON_TZ=Europe/Amsterdam`;
    - the spring-forward hour (03:30 under go-cron, the next day under robfig) and the repeated autumn hour;
    - `0 3 1 * 0` (they disagree: version known, unknown);
    - `* * * * 7` (only go-cron parses);
    - `@manual` and `0 0 31 2 *` (zero time);
    - a pod `TZ`, a `TZ` from `valueFrom` (unknown), and a version below 1.15;
  - **the classifier, every row with an injected clock:**
    - `D` + `G` crossing, and `Next`;
    - missed, and missed with the schedule controller disabled;
    - deferred behind an InProgress run, and not deferred behind a Queued one;
    - **a 70-minute InProgress blocker of an hourly schedule while other schedules fill the cap** (revision 2): retained by its tier, so deferred, Degraded, exactly as uncapped;
    - **the guard unresolved** (tier 0 overflowing the budget, no retained blocker): the Degraded floor, never a fault;
    - a run in progress during the grace;
    - **`N`** (revision 1):
      - not yet watched (within a minute);
      - gone, with an older retained success and, separately, with an older retained failure (Unknown each time, never the older outcome);
      - past `W`, and with the window in tier 0, whatever `lastBackup` is (revision 3);
      - unidentifiable with the zone unresolved;
    - Failed then Completed (reset), Completed then PartiallyFailed, Failed with a newer run in progress (still lit);
    - **an unpause:**
      - after a long quiet pause: the last paused capture hours old, then a fresh unpause, with the grace from `ResumedAt`, held across repeated evaluations (revision 1);
      - evidence;
      - the `G` bound;
      - an Unproven baseline;
      - no baseline;
    - `skipImmediately` (`lastSkipped` moves `base`);
    - FailedValidation from an unavailable BSL, per tick;
    - **only one library parses** (`* * * * 7`) with the version unresolved and an Enabled phase: Unknown, never inferred (revision 1);
    - a rollout in progress: version and zone unresolved;
  - **identity:**
    - proven by owner UID, and an owner reference to another UID;
    - named since creation;
    - an earlier schedule's runs, by creation time;
    - **looks synced** (more than 2 minutes), disputed (5 s to 2 minutes: counted with its qualifier), and local;
    - **a locally owned Failed Backup processed by a server 30 s behind the API server**: proven by owner, never tested, so it decides;
    - **a re-synced `N`** (revision 2): `N` deleted and re-created by sync an hour later under its name, with a foreign Backup started in between. `N`'s stored outcome decides, the foreign Backup is at most a named fact, and it never clears `N`'s failure;
    - **a named Completed Backup newer than a failed `N`** (a manual run without owners, or a foreign one): shown as a fact, the schedule still lit, and counted in §4.3 only if it failed;
    - **the previous outcome while `N` runs** (revision 3): a proven failure at 10:00, a named success at 10:30 and `N` in progress at 11:00, where the 10:00 failure decides. Without any proven terminal run, uncapped, the named one decides with its qualifier;
    - **its capped counterpart** (revision 5): the same runs with a tier-1 window `W` at 10:20, so the 10:00 failure is left out. Unknown, "the run before 11:00 may not be retained", never the named success;
    - **re-imported records** (revision 3): an older owner-proven Completed run re-created by sync after `N` failed, which keeps its old execution time, so `N` still decides. And the reverse, an old failure re-imported after a later success;
    - **a proven manual run before the first automatic one** (revision 3): `useOwnerReferencesInBackup: true`, no `lastBackup`, and a `--from-schedule` run that Failed, and separately one that PartiallyFailed. Each lights the schedule, Fault and Degraded;
    - **execution time:** a FailedValidation run ordered by its name's timestamp in a non-UTC zone, and a run with neither a start nor the controller's name form (order unknown, a fact);
    - **an older-created, later-started run under a cap** (revision 4): a proven Backup created before `N` but started after it (the 1.18 queue), finishing Failed while `N` is Completed and other schedules fill the cap. It is retained by its run time and decides, so the schedule is lit. And the reverse: it finishes Completed after a failed `N`, and the schedule is OK;
    - **the completeness check:** `N`'s execution time within a minute of `W`, which reads "a run after <time> may not be retained" (Unknown);
    - **a custom-named `--from-schedule` run** with owner references, FailedValidation with no start, before the schedule's first automatic run (revision 4). Counted in §4.3 with the schedule's verdict unchanged (revision 5):
      - still counted while a replacement is only InProgress;
      - superseded when the replacement completes, and also when it fails, which then lights the schedule;
    - **validation of join inputs** (revision 2): a 5,000-byte `spec.storageLocation` (marked invalid, the projection within `MaxObject`), a Schedule ownerReference with a malformed UID or an over-long name (owners incomplete, never named, never proven), and a third Schedule owner;
    - a `--from-schedule` manual run;
    - `N` in a non-UTC server zone;
    - a third Schedule owner (owners incomplete);
  - **BSL rules:** each row, ReadOnly, several defaults, expired backups awaiting deletion;
  - **unscheduled backups:** each phase, synced excluded, an orphan of a deleted schedule;
    - **the 24-hour boundary on one time basis** (revision 4): a Backup created at 10:00 and started at 12:00. A quiet fake clock, with no object or list change, crosses 10:00 the next day (still counted) and 12:00 (leaves). `Next` is published at 12:00;
  - **detection:**
    - a server ready, not ready, not found while the CRDs are served (unknown), and the list forbidden (unknown);
    - two servers in two namespaces;
    - the version from `velero/velero:v1.18.4`, from a mirror registry, and unknown from a digest-only image;
  - **sensitive data:**
    - **every classified form,** at v1.18.4's text, passing as its fixed class with the field beside it, and a near-miss of each (an extra suffix) withheld;
    - **a forged whole form** (revision 2): `unable to locate ObjectStore plugin named <needle>` and `backup can't be created because BackupStorageLocation <needle> is in Unavailable status.`, each classified with the needle absent, the name shown coming from the object's field;
    - **needles** (revision 1):
      - a SAS token in a 1.17-style BSL message;
      - a signed S3 URL in a `failureReason`;
      - a URL-free needle, `invalid configuration value: <needle>`;
      - `Authorization: Bearer <needle>`;
      - a needle before the first URL;
      - a needle beside a vocabulary token (`AccessDenied <needle>`), of which only the token crosses;
      - a needle in a Schedule's `invalid schedule:` tail;
      - a needle in a hook command, a Restore init container's environment and a BSL config value;

      each absent from:
      - the serialised generic detail and the local cells;
      - Describe, including a payload-free BSL with only a message (held);
      - the editor's refusal;
      - a cache diff;
      - an Event about a Velero object;
      - the chat tools and AI evidence;
  - **C1 beyond parity:**
    - projection equality for every rule field;
    - baseline, continuous, after-gap and loss cases;
    - UID replacement: a schedule deleted and recreated under its name, whose old runs stop counting;
    - the cap: newest first, `W`, "not retained";
    - an unsupported version;
    - 1.17 and 1.18 fixtures.
- **Parity:**
  - the three CRDs on `kind-klyx` and in `klyx-parity.sh`;
  - a stand-in server Deployment (pause image, `component=velero`, a container named `velero`);
  - time-free fixtures only:
    - a paused schedule;
    - a FailedValidation schedule;
    - a yearly schedule created by the fixture (no run owed for months; the fixture says so);
    - an Available and an Unavailable BSL;
    - a failed unscheduled backup;
  - the oracle's three parts and the controller task, and the frontend `ROW_PARTS`.
- **The lab (`kind-klyx` only):**
  - **The fixtures:**
    - Velero v1.18.4's CRDs, read before they are applied;
    - the stand-in server;
    - BSLs: one Unavailable with a SAS-token message carrying the needle, and one with a URL-free provider message carrying it beside `AccessDenied`;
    - an hourly schedule with a fresh Completed run created by each apply;
    - a schedule whose last run Failed;
    - a schedule created by the first apply with no runs, which reads missed from 10 minutes after its first due time;
    - a schedule deferred behind an InProgress run;
    - a `0 3 1 * 0` schedule (Unknown with the stand-in's unversioned image);
    - a synced backup;
    - an unscheduled failed backup;
    - hooks and config carrying the needle.
  - **How status is written:** with a plain apply, since there is no status subresource. `creationTimestamp` cannot be backdated, so the missed fixture needs the first apply at least 15 minutes before the smoke. The script says so and is idempotent.
- **Native smoke:**
  - "Velero schedules" opens broken-first;
  - the missed schedule's inspector shows its rail with the missed station and its storage location block;
  - the Unavailable BSL reads its message without the needle;
  - the needle is absent from every view the smoke opens.
- **Live captures:** the Schedules view, a schedule inspector with the rail, the storage locations view, and a BSL inspector, in night and day at 1440 and 1024.

## 8. Not in S2a

- **S2b:** the fleet horizon (the latest retained successful backup per schedule), and the Backup and Restore lists, synced backups included.
- **Velero's pause and unpause verbs:** S2 is read-only.
- **A configured maximum success age per schedule** (roadmap: "may replace" the grace). It needs a configuration surface. Proposed for S2b if the owner wants it.
- **The node-agent, PodVolumeBackups, DataUploads and BackupRepositories.** BackupRepository has its own Ready/NotReady phase and maintenance time, and is a candidate for a later slice.
- **Velero's Prometheus metrics** (`velero_backup_last_successful_timestamp` and others): S8's external-evidence contract.
- **Server logs** and the backup logs in object storage (a DownloadRequest is a write).

## 9. Revision 1: Codex's design review of 4e23d5d

| # | Finding | Where answered |
|---|---|---|
| P1 | SAS redaction and the URL rule cannot bound a plugin's error text: a URL-free secret or a bearer header survives | §6: Velero's text never crosses. Each message is matched whole against pinned v1.18.4 forms and re-emitted as Klyx's wording. Unclassified text is withheld, with only fixed-vocabulary tokens (provider codes, HTTP status, network classes) emitted. The rule covers every path, generic ones included (cells, Describe held, editor, diffs, events about Velero objects). §7 adds URL-free, bearer, before-the-URL and beside-a-token needles |
| 2 | A capped suffix cannot prove the overlap guard clear | §3 (a negative over all history is never proven while capped); §4.2 row 9: the qualified overdue fault, with why it stays a fault; §7 |
| 3 | A missing newest run let an older retained outcome stand | §4.2 row 10: anchored on `N`, checked whether or not older runs exist, with the watch grace, and Unknown with the older outcome as a fact only; §7 covers an older success and an older failure |
| 4 | The resume grace started at the pause | §4.2: `ResumedAt`, the first enabled capture of the suspension, kept until evidence; Unproven handled as Awaiting with its own `ResumedAt`; §7 |
| 5 | Enabled does not name the library that parsed the current spec | §4.2: the inference is dropped; one-library parses with an unresolved version are Unknown |
| 6 | No total bound on a Backup projection; oversized omissions could leave a hole before the window | §3: bounded inputs and a checked 4 KiB total that sheds display classes only. The window is the first omission of any cause, oversized included; §7 |
| 7 | Cross-clock skew read as proof of sync | §3: the server's clock offset measured from `N`; proven runs (owner UID, `N`) are never tested; "looks synced" is listed, never silent, and "probably" while the offset is unmeasured; §7 |
| - | Deployment configuration during a rollout | §3: facts only from a rolled-out Deployment, else unresolved |
| - | The overdue rule's precedence | §4.1: numbered, first match wins, overdue before Available |

## 10. Revision 2: Codex's re-review of 9f95194

| # | Finding | Where answered |
|---|---|---|
| P1 | A whole-form match still re-emitted captured names, which a provider can forge | §6: every class is a fixed string; a name beside it comes from an object field Klyx reads, never from the message; a forged form can mislead the class, never disclose. §7 adds forged whole forms |
| 2 | Retained-window length is not a run-duration limit, so a capped "no blocker" cannot justify a fault | §3: Backups rank in two tiers, the guard's phases first, so the guard's evidence is complete whatever `W` is; ranks may change, and the watch path re-places them. §4.2 row 9: only a tier-0 overflow leaves it unresolved, at the Degraded floor. §7: the 70-minute blocker under a cap |
| 3 | `N.creationTimestamp − lastBackup` is no clock measurement: sync can re-create `N` | §3: no calibration. Only proven runs (`N`, owner UID) decide a verdict (§4.2 row 10); named runs are history and the previous outcome; looks-synced is a fixed 2-minute rule, disputed timing counted with a qualifier. §7: the re-synced `N`, and a named success newer than a failed `N` |
| 4 | The size proof assumed API limits that do not exist | §3: Klyx validates `spec.storageLocation` and Schedule ownerReferences itself (invalid marker; incomplete owners prove nothing either way); an oversized projection is left out and covered by the window. §7: a 5,000-byte location and malformed owners |

## 11. Revision 3: Codex's re-review of 47c0dff

| # | Finding | Where answered |
|---|---|---|
| 1 | The named previous-outcome fallback could clear a proven failure while a newer `N` runs | §4.2 row 10: the previous outcome is the newest proven terminal run; a named one only when none is retained. A proven failure is never cleared by a named success. §7: the 10:00 / 10:30 / 11:00 case |
| 2 | Creation time let re-imported records reverse the order of runs | §3: runs are ordered by their own execution time (`startTimestamp`, else the controller's name timestamp, else `lastBackup` for `N`, else unknown, never deciding). The earlier-incarnation rule uses it too. §7: re-imported proven success after a failed `N`, and the reverse |
| 3 | A proven manual failure before the first automatic run lit nothing | §4.2 row 10: without `lastBackup`, proven runs decide alone. §4.3: counted are the backups not proven for a current schedule and not superseded by its `N`. §7: Failed and PartiallyFailed |
| 4 | A tier-changing blocker's relist does not establish the new window at once | §3: until a complete list, the boundary stays at the earlier of the old and new ranks; §4.2: a missing `N` with the window in tier 0 is "not retained". §7: the delayed-refill capture test |

## 12. Revision 4: Codex's re-review of b676a4f

| # | Finding | Where answered |
|---|---|---|
| 1 | Admission by creation did not keep the latest executions: the 1.18 queue can start an older-created Backup after `N` | §3: tier 1 ranks by run time (`startTimestamp`, else `creationTimestamp`), so the retained tier-1 set is the latest executions; §4.2 row 10: the outcome is complete only when `N`'s execution time is later than `W` plus a minute, else "a run after <time> may not be retained". §7: both directions under a cap |
| 2 | A proven run of unknown order that failed lit neither part | §4.3: counted until a proven run of known later execution supersedes it, or 24 hours pass. §7: the custom-named FailedValidation before the first automatic run |
| 3 | The 24-hour membership and its clock used different times | §3 and §4.3: one basis, the run time, for membership, leaving and `Next` (§4.4). §7: the quiet fake-clock crossing |

## 13. Revision 5: Codex's re-review of 54cb39a

| # | Finding | Where answered |
|---|---|---|
| 1 | The completeness check missed the previous-outcome query: a capped proven failure let a named success decide | §4.2 row 10: a retained proven previous run is provably the newest; with none retained, the named fallback applies only to complete Backups, and capped it is Unknown. §7: the capped 10:00 / 10:30 / 11:00 case |
| 2 | An unknown-order failure left §4.3 as soon as a replacement started | §4.3: only a finished proven run supersedes it. §7: still counted while the replacement runs, superseded when it completes or fails |
