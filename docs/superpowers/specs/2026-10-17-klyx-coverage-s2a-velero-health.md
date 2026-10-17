# Coverage S2a: Velero health

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S2a. S2b (the fleet horizon, and the Backup and Restore lists) follows with its own note.

The owner's direction: "velero should be supported also". The roadmap's S2a asks for background evidence independent of any list being open, three faults (an unavailable storage location, a missed schedule, recent failed and partially failed backups, distinct from each other), named schedule states, and Velero's phases kept as they are. Codex's acceptance carried three details into this note (roadmap §6): formalise the overdue opportunity and its reset after a later success; an in-progress run during the grace period, and incomplete retained history; the exact classifier and its fake-clock cases. All three are in §4.

The slice is read-only. It adds no verb (§5).

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
  - **The inspector leads with the schedule's own verdict** (InspectorStatus), its reason and its proof mode: "runs proven by owner reference", or "runs matched by name since this schedule was created" (§3).
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
  - classification inputs: `spec.schedule` kept whole (the parsers cap it at 1024 bytes), `paused`, `skipImmediately`, `useOwnerReferencesInBackup`, `creationTimestamp`, `status.phase`, `lastBackup`, `lastSkipped`;
  - display: `validationErrors` (after §6, 256 bytes each, at most four);
  - the template: `storageLocation`, `ttl`, the snapshot settings and `volumeSnapshotLocations`, and the included and excluded namespaces (at most 20 names, with counts);
  - the hook count, never a hook.
- **BackupStorageLocation:**
  - classification inputs: `status.phase`, `lastValidationTime`, `validationFrequency`, `creationTimestamp`, `default`, `accessMode`;
  - display: `provider`, `bucket`, `prefix`, `caCertRef`'s name, whether `caCert` is set, `credential`'s name and key, `backupSyncPeriod`, `lastSyncedTime`, `message` (after §6, 1 KiB);
  - the config's keys (at most 32), with values only for the allowlist of §6.
- **Backup:**
  - classification inputs: `creationTimestamp`, `status.phase`, `startTimestamp`, `completionTimestamp`, `expiration`;
  - join inputs, kept whole: the `velero.io/schedule-name` label, the ownerReferences whose kind is Schedule (UID and name), `spec.storageLocation`;
  - display: `errors`, `warnings`, the counters, `progress`, `queuePosition`, `failureReason` (after §6, 512 bytes), `validationErrors` (after §6, 256 bytes each, at most four), the `velero.io/gc-failure` label;
  - never the spec's hooks or selectors.

**Versions:** each kind is probed for `v1`. A cluster serving none reads it as absent; one serving another version reads "unsupported Velero API version", never empty.

**Identity: which Backups are a schedule's runs.** The roadmap: "a schedule recreated under the same name is a new schedule (UID), so the schedule-name label alone does not prove association".
- **Local or synced.** A Backup whose `startTimestamp` is more than 5 s before its `creationTimestamp` was processed before it existed here. Backup sync created it from object storage. Klyx counts it as **synced**, and a synced Backup is never a run of a schedule here.

  The converse is not proof. A synced backup from a cluster whose clock runs far ahead can pass as local. That is one part of the limit below.
- **A local Backup is a run of schedule S when** it is in S's namespace, and either:
  - **Proven:** it has an ownerReference with S's UID.
  - **Named:** it has no Schedule ownerReference to another UID, its `velero.io/schedule-name` label equals S's name, and its `creationTimestamp` is at or after S's.

  This excludes the runs of an earlier schedule of the same name and every synced backup.
- **The run Velero last created** is identified by name: `<S>-<lastBackup formatted 20060102150405 in the server's zone>`. That is the controller's own naming of the run it recorded in status.
- **The run time** of a local run is its `creationTimestamp`.
- **The limit,** said in the inspector when the proof mode is "named": a bucket shared with another cluster that runs a same-named schedule can bring that cluster's backups in. Klyx excludes the ones it can tell were synced, and cannot exclude a foreign backup created in this cluster's namespace under the same name. `useOwnerReferencesInBackup: true` makes the runs proven.

**The Backups cap: newest first (a change to the Observer, Stage 1).**
- **The problem:** the budget admits projections in key order (namespace/name, `observe/kind.go` `admit`). For Backups that keeps the alphabetically first schedules' old runs and drops other schedules' newest ones.
- **The change:** `KindSpec.Rank`, a function of a projection that must not change over the object's life. The budget admits, evicts and blocks in rank order instead of key order, and the key breaks ties.
  - Every other kind keeps rank = key, so nothing else moves.
  - For Backups, rank is `creationTimestamp` descending, then the key. A new Backup ranks first, and the oldest are left out.
- **The window:** the capture carries, for a capped kind, the rank of its first left-out object.
  - For Backups that is a creation time `W`: every Backup created after `W` is retained.
  - A question about runs created after `W` has a complete answer; one that reaches before `W` is "not retained".
  - A Backup's projection is bounded by construction (every display string clipped), so none is left out for its size.

**Controllers (C1's controller task).**
- **Finding the servers:** a cluster-wide list of Deployments labelled `component=velero` with a container named `velero` (as Reloader's detection lists cluster-wide). It gives each server's namespace, readiness, the image tag's version, the container's literal `TZ`, and whether `schedule` is among `--disable-controllers`. Klyx reads no other environment variable, argument or value.
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
- per-object maxima: 4 KiB for Schedules and BSLs, 2 KiB for Backups;
- per cluster: Schedules 128 KiB, BSLs 64 KiB, Backups 768 KiB (about 1,500 Backups at a typical 500 bytes), 960 KiB together;
- ten clusters: **9.4 MiB** encoded, about 52 MiB of heap at S1a's calibration of 5.5 heap bytes per encoded byte.

  Past the Backups budget, coverage reads "capped: the newest N retained since <W>".

## 4. Health rules

### 4.1 Storage locations

| State | Condition | Role |
|---|---|---|
| Not processed | the BSL's namespace has no Velero server, and the server list was read | Degraded, "not in a Velero server's namespace: Velero processes only its own" |
| Unavailable | `phase` Unavailable | Fault, "Unavailable: <message>" |
| Available | `phase` Available | OK, "Available, validated <age> ago" |
| Validation overdue | Available, `validationFrequency` declared and above 0, and now ≥ `lastValidationTime` + 2 × frequency + 5m | Degraded, "validation overdue: last validated <time>" |
| Awaiting validation | `phase` empty, created under 5 minutes ago | Progressing, "awaiting Velero's first validation" |
| Not validated | `phase` empty, created 5 minutes ago or more | Degraded, "not validated by Velero" |

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

**Which library, when the version is unknown.** Klyx computes `D` with both.
- **If they agree,** `D` stands. They disagree only for the forms of §1.
- **If only one parses** the spec while Velero says Enabled, that one is Velero's.
- **Otherwise** the schedule is Unknown, "its next run depends on Velero's version, which Klyx could not read".
- **A known version below 1.15** is Unknown, "Velero before 1.15 uses another cron library".

**The grace `G` is 10 minutes.** It covers the controller's one-minute cadence and a server restart. The controller task names a longer outage.

**The verdict, first match wins:**

| # | State | Condition | Role |
|---|---|---|---|
| 1 | Not processed | the schedule's namespace has no Velero server, and the list was read | Degraded, "not in a Velero server's namespace" |
| 2 | Paused | `spec.paused` | Paused. Velero does not validate a paused schedule; if Klyx's parsers reject its cron, "will fail validation when resumed" |
| 3 | FailedValidation | `phase` FailedValidation | Fault, with Velero's validation errors (§6) |
| 4 | Awaiting validation / not processed | `phase` empty: under 5 minutes since creation, or past it | Progressing "awaiting Velero's first validation" / Degraded "not processed by Velero" |
| 5 | Unknown time | `D` cannot be computed (version, zone or parse, above) | Unknown, with the reason |
| 6 | Never matches | `D` is the zero time | Degraded, "this cron never matches a time: Velero starts a run on every check" |
| 7 | Resumed | the resume tracker is Awaiting, within `G` of the capture that last showed it paused | Progressing, "resumed: awaiting Velero's first run" |
| 8 | Runs unreadable | the Backups kind is unreadable | Unknown, "runs not readable" |
| 9 | Missed / deferred | now ≥ `D` + `G`, so Velero has not created the run due at `D` | **deferred**, Degraded, when Velero's own guard holds: a Backup in the namespace labelled with this schedule's name is in "", New or InProgress (the guard reads the label alone, so Klyx does too here): "the run due at D waits for <backup> (Velero defers while one runs)"; otherwise **missed**, Fault, "Velero created no backup for the run due at D", with "the schedule controller is disabled on this server" when so |
| 10 | Last run | otherwise (no run is overdue) | the outcome below |

**The outcome of the last run (row 10).** The runs are ordered by run time, and the newest terminal one decides. A schedule stays lit until a run succeeds.
- **The newest terminal run is Completed:** OK, "last run completed <time>". A newer run in progress adds "a run is in progress".
- **It is Failed or FailedValidation:** Fault, "the last run failed: <failureReason or validation error>". A newer run in progress adds "a new run is in progress since <time>", and the schedule stays lit until that run succeeds.
- **It is PartiallyFailed:** Degraded, "the last run partially failed: N errors". It is distinct from Failed, and lit the same way until a success.
- **No terminal run, some in progress:** Progressing, "first run in progress" or "run in progress".
- **No run at all:**
  - without `lastBackup`: Progressing, "awaiting its first run, due D";
  - with `lastBackup` and no run found: within one minute of `lastBackup`, Progressing, "run created at T" (the run's watch can trail the schedule's); after that, if `lastBackup` is at or before `W`, Unknown "the run created at T is not retained"; otherwise Unknown "the run Velero created at T is not in the cluster".
- **Never counted:** synced Backups and the runs of an earlier schedule of the same name.

**The reset.** A later Completed run ends a failed or partially failed state at once (it becomes the newest terminal run). A missed state ends when Velero creates the owed run, because `lastBackup` passes `D`, and the new run is then judged by the outcome rule. Nothing older than the newest terminal run lights.

A manual `--from-schedule` run is a run of the schedule for the outcome rule, but it never moves `lastBackup`. It does not end a missed state: the controller still owes the run at `D`, and a schedule whose controller does not fire is not working, whatever a person ran by hand. The inspector shows both.

**An in-progress run during the grace period.** Velero creates the owed run at `D`, so `lastBackup` passes `D` and row 9 does not apply. The run is in progress, and row 10 shows "run in progress" over the previous outcome. A run that overruns into the next `D` blocks it (the overlap guard), and row 9 then reads deferred, Degraded, not missed.

**Incomplete retained history.**
- With the newest-first cap, the newest runs are always retained, and row 10 needs only the newest terminal run.
- A schedule whose every retained run is in progress, with older runs past `W`, reads "no terminal run retained since W" (Unknown).
- The rail marks stations before `W` "not retained".

**The resume tracker** (`internal/gitops/resume`, unchanged): the sample is the Schedule's UID. "Disabled" is `spec.paused`, and the fingerprint is the SHA-256 of `phase`, `lastBackup` and `lastSkipped`.
- **Why it is needed:** on unpause, `Next(old lastBackup)` is in the past, so without it row 9 would read missed until Velero acts. Velero acts within seconds of the spec change: it creates the run, or sets `lastSkipped` when unpaused with `--skip-immediately`.
- **What ends Awaiting:** evidence (a changed fingerprint), or `G` after the capture that last showed the schedule paused, after which the rules apply.
- **What the tracker needs:** one accessor for its baseline's capture time.
- **A Klyx that never saw the pause** has no baseline. It applies the rules, as Velero has long since acted.

**Named states, each tested** (the roadmap's list): new (rows 4 and 10), paused (2), FailedValidation (3), `skipImmediately` (`lastSkipped` moves `base`, so no run is owed), a backup in progress (10, and 9 deferred), and no known success (10: never a fault on its own; a failed run or a missed run is).

### 4.3 Unscheduled backups

- **The counted entity:** a local Backup (§3), created in the last 24 hours, that is not a run of a current schedule. That covers manual backups, and runs of a deleted schedule or of an earlier one of the same name. "Recent" is the roadmap's word; 24 hours is Klyx's rule, and the view says so.
- **Roles:**

  | Phase | Role |
  |---|---|
  | Completed | OK |
  | FailedValidation, Failed | Fault, with the failure reason or validation error |
  | PartiallyFailed | Degraded, with the error count |
  | the non-terminal phases | Progressing; the PartiallyFailed variants say "partially failed so far" |
  | Deleting | Progressing |

  A Backup outside a server's namespace (list read) is Degraded, "not processed".
- **Leaving:** a backup leaves the part 24 hours after its creation (a clock, §4.4). Older backups are history, listed in S2b.
- **Never counted here:** synced Backups. They may belong to another cluster, and are listed in S2b.

### 4.4 Clocks

| Deadline | Part | Effect |
|---|---|---|
| `D` + `G` (and the next `D` after a run) | schedules | the missed or deferred state can begin |
| creation + 5m with an empty phase | schedules, locations | "not processed" / "not validated" |
| `lastValidationTime` + 2 × frequency + 5m | locations | validation overdue |
| creation + 24h | backups | the backup leaves the part |
| the resume baseline + `G` | schedules | Awaiting ends |

- **Each deadline is the part's `Next`,** re-evaluated by the clock as ESO's staleness is. A crossed deadline is the role's `Boundary`, never its `Since`.
- **Clock sources:**
  - `D` is computed from Velero's own `lastBackup` (the server's clock) and the cron, then compared with Klyx's clock.
  - Skew between the server and Klyx shifts the deadline by the skew. The 10-minute grace absorbs ordinary skew, and a missed state names both `D` and `lastBackup`.
  - Klyx's time-zone database (Go's, with `time/tzdata` embedded for Windows) can differ from the server's image in rare zone changes. The inspector names the zone it used.

## 5. Verbs (roadmap C4)

None. S2 is read-only (roadmap). Velero's own day-2 verbs, pause and unpause, are not offered. "Backup now" and restore are outside the programme (roadmap §3).

## 6. Sensitive data

- **No credential values.** BSLs name a credential Secret, a CA Secret (`caCertRef`) and, for older installs, an inline `caCert`. Klyx shows the names and "set", never a value, and never reads a Secret.
- **The Velero message rule, `crd.VeleroMessage`,** applies to every Velero free-text field Klyx displays or sends: BSL `status.message`, Backup `failureReason` and `validationErrors`, Schedule `validationErrors`.
  - **First, Velero's own Azure redaction, whatever the server's version:** the ten SAS parameters (§1) become `***`, wherever they appear, since 1.17 does not redact them.
  - **Then the URL rule** (S0b, `urlscrub.Origins`): every URL keeps its origin only, so paths, queries and user information go, including an Azure account URL's container path.
  - **Then the length caps of §3.**
  - **Where it runs:** the inspectors, the rows, AI evidence, the chat get tool and health reasons (`CapReason` input). A needle test covers each (§7).
- **The S0b registry gains the Velero kinds,** so these surfaces are handled on every path (the generic detail, Describe, the guarded editor, cache diffs, chat, AI), not only in this slice's projection:
  - Backup `spec.hooks`, Schedule `spec.template.hooks` and Restore `spec.hooks`: opaque. Hooks carry exec commands, and a Restore's init hooks embed whole containers with environment values.
  - BSL and VolumeSnapshotLocation `spec.config`: free-form values (keys kept, values omitted). Provider config is arbitrary and can embed credentials in a URL.
  - BSL `spec.objectStorage.caCert`: opaque.
- **The BSL config allowlist for display:** `region`, and the origins of `s3Url` and `publicUrl`. Every other key shows by name only.
- **AI facts:**
  - allowlisted: kind, verdict, phases, the cron and zone, times, counts, the storage location's name, provider, bucket and prefix, and messages after the rule above;
  - excluded: config values, Secret names, hooks and selectors.

## 7. Tests, lab and smoke

- **Stage 1, the Observer's rank:**
  - newest-first admission across a list, a watch add (a new Backup evicts the oldest), an update (rank unchanged, size changed), a delete (refill), and ties by key;
  - the window `W` in the capture;
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
    - a run in progress during the grace;
    - the run at `lastBackup` not yet watched (within a minute), gone, and past `W`;
    - Failed then Completed (reset), Completed then PartiallyFailed, Failed with a newer run in progress (still lit);
    - an unpause with the tracker (Awaiting, evidence, the `G` bound) and without a baseline;
    - `skipImmediately` (`lastSkipped` moves `base`);
    - FailedValidation from an unavailable BSL, per tick;
  - **identity:**
    - proven by owner UID, and an owner reference to another UID;
    - named since creation;
    - an earlier schedule's runs, by creation time;
    - a synced backup (start before creation by more than 5 s);
    - a `--from-schedule` manual run;
    - the run named by `lastBackup` in a non-UTC server zone;
  - **BSL rules:** each row, ReadOnly, several defaults, expired backups awaiting deletion;
  - **unscheduled backups:** each phase, the 24-hour boundary, synced excluded, an orphan of a deleted schedule;
  - **detection:**
    - a server ready, not ready, not found while the CRDs are served (unknown), and the list forbidden (unknown);
    - two servers in two namespaces;
    - the version from `velero/velero:v1.18.4`, from a mirror registry, and unknown from a digest-only image;
  - **sensitive data:**
    - a SAS token in a 1.17-style BSL message;
    - a signed S3 URL in a `failureReason`;
    - a needle in a hook command, a Restore init container's environment and a BSL config value;

    each absent from the serialised generic detail, Describe, the chat tools and AI evidence;
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
    - BSLs, one Unavailable with a SAS-token message carrying the needle;
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
