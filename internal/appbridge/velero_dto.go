package appbridge

import (
	"sort"
	"time"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/velero"
)

// Coverage S2a: the Velero lens's rows (the Observer's shared schedules,
// locations and backups parts). Velero's text never crosses: messages are
// Klyx's fixed classes (velero.TextOf), and hooks, config values and
// Secrets are never carried.

// VeleroRunDTO is one Backup as a schedule's run or a fact beside it.
type VeleroRunDTO struct {
	UID       string     `json:"uid"`
	Name      string     `json:"name"`
	Phase     string     `json:"phase"`
	At        *time.Time `json:"at,omitempty"` // its execution time; absent when unknown
	Created   *time.Time `json:"created,omitempty"`
	Started   *time.Time `json:"started,omitempty"`
	Completed *time.Time `json:"completed,omitempty"`
	Proven    bool       `json:"proven"`
	Timing    string     `json:"timing"` // local | disputed | synced
	Errors    int        `json:"errors,omitempty"`
	Warnings  int        `json:"warnings,omitempty"`
	Class     string     `json:"class,omitempty"` // a failed run's class
}

// VeleroTemplateDTO is a schedule's backup template, as configured now.
type VeleroTemplateDTO struct {
	StorageLocation         string   `json:"storageLocation,omitempty"`
	StorageLocationInvalid  bool     `json:"storageLocationInvalid,omitempty"`
	TTL                     string   `json:"ttl,omitempty"`
	SnapshotVolumes         *bool    `json:"snapshotVolumes,omitempty"`
	DefaultVolumesToFs      *bool    `json:"defaultVolumesToFsBackup,omitempty"`
	SnapshotMoveData        *bool    `json:"snapshotMoveData,omitempty"`
	VolumeSnapshotLocations []string `json:"volumeSnapshotLocations"`
	IncludedNamespaces      []string `json:"includedNamespaces"`
	IncludedCount           int      `json:"includedCount"`
	ExcludedNamespaces      []string `json:"excludedNamespaces"`
	ExcludedCount           int      `json:"excludedCount"`
	Hooks                   int      `json:"hooks"` // counted; commands withheld
}

// VeleroLocationLinkDTO is a schedule's storage location as its own block.
type VeleroLocationLinkDTO struct {
	State     string     `json:"state"`
	Name      string     `json:"name,omitempty"`
	ByDefault bool       `json:"byDefault,omitempty"`
	Label     string     `json:"label,omitempty"`
	Role      string     `json:"role,omitempty"`
	Validated *time.Time `json:"validated,omitempty"`
}

// VeleroScheduleDTO is a Schedule row with what decided its verdict.
type VeleroScheduleDTO struct {
	UID                string                `json:"uid"`
	GVR                string                `json:"gvr"`
	Namespace          string                `json:"namespace"`
	Name               string                `json:"name"`
	Generation         int64                 `json:"generation"`
	Created            *time.Time            `json:"created,omitempty"`
	Cron               string                `json:"cron"`
	CronTooLong        bool                  `json:"cronTooLong,omitempty"`
	Zone               string                `json:"zone,omitempty"`
	Library            string                `json:"library,omitempty"`
	Paused             bool                  `json:"paused"`
	Phase              string                `json:"phase"`
	UseOwnerReferences bool                  `json:"useOwnerReferences"`
	LastBackup         *time.Time            `json:"lastBackup,omitempty"`
	LastSkipped        *time.Time            `json:"lastSkipped,omitempty"`
	Due                *time.Time            `json:"due,omitempty"`
	DueZero            bool                  `json:"dueZero,omitempty"`
	DueUnknown         string                `json:"dueUnknown,omitempty"`
	Validation         []string              `json:"validation"`
	NName              string                `json:"nName,omitempty"`
	NState             string                `json:"nState,omitempty"`
	N                  *VeleroRunDTO         `json:"n,omitempty"`
	Deciding           *VeleroRunDTO         `json:"deciding,omitempty"`
	Previous           *VeleroRunDTO         `json:"previous,omitempty"`
	PreviousNamed      bool                  `json:"previousNamed,omitempty"`
	Blocker            *VeleroRunDTO         `json:"blocker,omitempty"`
	Fact               *VeleroRunDTO         `json:"fact,omitempty"`
	Runs               []VeleroRunDTO        `json:"runs"`
	Newer              []VeleroRunDTO        `json:"newer"`
	Synced             int                   `json:"synced"`
	Template           VeleroTemplateDTO     `json:"template"`
	Location           VeleroLocationLinkDTO `json:"location"`
	Resumed            bool                  `json:"resumed,omitempty"`
	Reason             string                `json:"reason,omitempty"`
	Rule               RuleDTO               `json:"rule"`
}

// VeleroScheduleRefDTO names a schedule and its verdict.
type VeleroScheduleRefDTO struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Label     string `json:"label"`
	Role      string `json:"role"`
}

// VeleroLocationDTO is a BackupStorageLocation row: never a config value
// beyond the allowlist, a Secret's content or Velero's text.
type VeleroLocationDTO struct {
	UID                 string                 `json:"uid"`
	GVR                 string                 `json:"gvr"`
	Namespace           string                 `json:"namespace"`
	Name                string                 `json:"name"`
	Created             *time.Time             `json:"created,omitempty"`
	Provider            string                 `json:"provider,omitempty"`
	Bucket              string                 `json:"bucket,omitempty"`
	Prefix              string                 `json:"prefix,omitempty"`
	CACertSet           bool                   `json:"caCertSet,omitempty"`
	CACertRef           string                 `json:"caCertRef,omitempty"`
	Credential          string                 `json:"credential,omitempty"`
	CredentialKey       string                 `json:"credentialKey,omitempty"`
	Default             bool                   `json:"default"`
	AccessMode          string                 `json:"accessMode,omitempty"`
	SyncPeriod          string                 `json:"syncPeriod,omitempty"`
	ValidationFrequency string                 `json:"validationFrequency,omitempty"`
	Phase               string                 `json:"phase"`
	LastValidation      *time.Time             `json:"lastValidation,omitempty"`
	LastSynced          *time.Time             `json:"lastSynced,omitempty"`
	Message             string                 `json:"message,omitempty"` // Klyx's class, never Velero's text
	MessageWithheld     bool                   `json:"messageWithheld,omitempty"`
	ConfigKeys          []string               `json:"configKeys"`
	ConfigCount         int                    `json:"configCount"`
	Region              string                 `json:"region,omitempty"`
	S3URL               string                 `json:"s3Url,omitempty"`     // origin only
	PublicURL           string                 `json:"publicUrl,omitempty"` // origin only
	Schedules           []VeleroScheduleRefDTO `json:"schedules"`
	SchedulesState      string                 `json:"schedulesState"`
	Expired             int                    `json:"expired"`
	ExpiredState        string                 `json:"expiredState"`
	GCFailures          map[string]int         `json:"gcFailures,omitempty"`
	SeveralDefaults     bool                   `json:"severalDefaults,omitempty"`
	Reason              string                 `json:"reason,omitempty"`
	Rule                RuleDTO                `json:"rule"`
}

// VeleroBackupDTO is a recent unscheduled Backup row.
type VeleroBackupDTO struct {
	UID                    string     `json:"uid"`
	GVR                    string     `json:"gvr"`
	Namespace              string     `json:"namespace"`
	Name                   string     `json:"name"`
	Created                *time.Time `json:"created,omitempty"`
	Started                *time.Time `json:"started,omitempty"`
	Completed              *time.Time `json:"completed,omitempty"`
	Expires                *time.Time `json:"expires,omitempty"`
	Phase                  string     `json:"phase"`
	ScheduleLabel          string     `json:"scheduleLabel,omitempty"`
	StorageLocation        string     `json:"storageLocation,omitempty"`
	StorageLocationInvalid bool       `json:"storageLocationInvalid,omitempty"`
	Errors                 int        `json:"errors"`
	Warnings               int        `json:"warnings"`
	ItemsTotal             int        `json:"itemsTotal,omitempty"`
	ItemsBackedUp          int        `json:"itemsBackedUp,omitempty"`
	QueuePosition          int        `json:"queuePosition,omitempty"`
	Failure                string     `json:"failure,omitempty"`
	Validation             []string   `json:"validation"`
	ValidationCount        int        `json:"validationCount"`
	HooksAttempted         int        `json:"hooksAttempted,omitempty"`
	HooksFailed            int        `json:"hooksFailed,omitempty"`
	OperationsFailed       int        `json:"operationsFailed,omitempty"`
	Membership             string     `json:"membership"`
	MembershipSchedule     string     `json:"membershipSchedule,omitempty"`
	Disputed               bool       `json:"disputed,omitempty"`
	Reason                 string     `json:"reason,omitempty"`
	Rule                   RuleDTO    `json:"rule"`
}

func timingWord(t velero.Timing) string {
	switch t {
	case velero.TimingDisputed:
		return "disputed"
	case velero.TimingSynced:
		return "synced"
	}
	return "local"
}

func failedClass(b *velero.Backup) string {
	switch {
	case b.Failure != nil:
		return velero.TextOf(*b.Failure)
	case len(b.Validation) > 0:
		return velero.TextOf(b.Validation[0])
	}
	return ""
}

func veleroRunDTO(b *velero.Backup, at time.Time, proven bool) *VeleroRunDTO {
	if b == nil {
		return nil
	}
	d := &VeleroRunDTO{UID: b.UID, Name: b.Name, Phase: b.Phase, Created: timePtrOf(b.Created), Started: timePtrOf(b.Start),
		Completed: timePtrOf(b.Completion), Proven: proven, Timing: timingWord(b.Timing()), Errors: b.Errors, Warnings: b.Warnings, Class: failedClass(b)}
	if !at.IsZero() {
		d.At = timePtrOf(at)
	}
	return d
}

func classTexts(cs []velero.Class) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, velero.TextOf(c))
	}
	return out
}

func durString(d time.Duration, set bool) string {
	if !set {
		return ""
	}
	return d.String()
}

func veleroScheduleDTO(r healthsrc.Row[healthsrc.VeleroScheduleRow]) VeleroScheduleDTO {
	s, v := r.Data.Schedule, r.Data.Verdict
	t := s.Template
	d := VeleroScheduleDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Namespace: s.Namespace, Name: s.Name, Generation: s.Generation, Created: timePtrOf(s.Created),
		Cron: s.Cron, CronTooLong: s.CronTooLong, Zone: v.Due.Zone, Library: v.Due.Library, Paused: s.Paused, Phase: s.Phase,
		UseOwnerReferences: s.UseOwnerReferences, LastBackup: timePtrOf(s.LastBackup), LastSkipped: timePtrOf(s.LastSkipped),
		DueZero: v.Due.Zero, DueUnknown: v.Due.Unknown, Validation: classTexts(s.Validation), NName: v.NName, NState: v.NState,
		PreviousNamed: v.PreviousNamed, Runs: []VeleroRunDTO{}, Newer: []VeleroRunDTO{}, Synced: v.Synced, Resumed: r.Data.Resumed,
		Reason: r.Eval.Reason, Rule: toRuleDTO(r.Eval),
		Template: VeleroTemplateDTO{StorageLocation: t.StorageLocation, StorageLocationInvalid: t.StorageLocationInvalid, TTL: t.TTL,
			SnapshotVolumes: t.SnapshotVolumes, DefaultVolumesToFs: t.DefaultVolumesToFs, SnapshotMoveData: t.SnapshotMoveData,
			VolumeSnapshotLocations: nonNil(t.VolumeSnapshotLocations), IncludedNamespaces: nonNil(t.Included), IncludedCount: t.IncludedCount,
			ExcludedNamespaces: nonNil(t.Excluded), ExcludedCount: t.ExcludedCount, Hooks: t.Hooks},
		Location: VeleroLocationLinkDTO{State: r.Data.Location.State, Name: r.Data.Location.Name, ByDefault: r.Data.Location.ByDefault,
			Label: r.Data.Location.Label, Role: r.Data.Location.Role, Validated: timePtrOf(r.Data.Location.Validated)}}
	if !v.Due.D.IsZero() {
		d.Due = timePtrOf(v.Due.D)
	}
	at := map[*velero.Backup]velero.Run{}
	for _, run := range v.Runs {
		at[run.Backup] = run
		d.Runs = append(d.Runs, *veleroRunDTO(run.Backup, run.At, run.Proven))
	}
	ref := func(b *velero.Backup) *VeleroRunDTO {
		if b == nil {
			return nil
		}
		run, ok := at[b]
		return veleroRunDTO(b, run.At, !ok || run.Proven)
	}
	d.N, d.Deciding, d.Previous, d.Blocker, d.Fact = ref(v.N), ref(v.Deciding), ref(v.Previous), ref(v.Blocker), ref(v.Fact)
	if d.Previous != nil && v.PreviousNamed {
		d.Previous.Proven = false
	}
	for _, b := range v.Newer {
		d.Newer = append(d.Newer, *veleroRunDTO(b, b.RunTime(), false))
	}
	return d
}

func veleroLocationDTO(r healthsrc.Row[healthsrc.VeleroLocationRow]) VeleroLocationDTO {
	l := r.Data.Location
	d := VeleroLocationDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Namespace: l.Namespace, Name: l.Name, Created: timePtrOf(l.Created), Provider: l.Provider,
		Bucket: l.Bucket, Prefix: l.Prefix, CACertSet: l.CACertSet, CACertRef: l.CACertRef, Credential: l.Credential, CredentialKey: l.CredentialKey,
		Default: l.Default, AccessMode: l.AccessMode, SyncPeriod: durString(l.SyncPeriod, l.SyncPeriodSet),
		ValidationFrequency: durString(l.ValidationFrequency, l.ValidationSet), Phase: l.Phase, LastValidation: timePtrOf(l.LastValidation),
		LastSynced: timePtrOf(l.LastSynced), ConfigKeys: nonNil(l.ConfigKeys), ConfigCount: l.ConfigCount, Region: l.Region, S3URL: l.S3URL,
		PublicURL: l.PublicURL, Schedules: []VeleroScheduleRefDTO{}, SchedulesState: r.Data.SchedulesState, Expired: r.Data.Expired,
		ExpiredState: r.Data.ExpiredState, GCFailures: r.Data.GCFailures, SeveralDefaults: r.Data.SeveralDefaults, Reason: r.Eval.Reason, Rule: toRuleDTO(r.Eval)}
	if m := l.Message; m != nil {
		d.Message, d.MessageWithheld = velero.TextOf(*m), m.Withheld
	}
	for _, s := range r.Data.Schedules {
		d.Schedules = append(d.Schedules, VeleroScheduleRefDTO{Namespace: s.Namespace, Name: s.Name, Label: s.Label, Role: s.Role})
	}
	return d
}

func veleroBackupDTO(r healthsrc.Row[healthsrc.VeleroBackupRow]) VeleroBackupDTO {
	b, m := r.Data.Backup, r.Data.Membership
	d := VeleroBackupDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Namespace: b.Namespace, Name: b.Name, Created: timePtrOf(b.Created), Started: timePtrOf(b.Start),
		Completed: timePtrOf(b.Completion), Expires: timePtrOf(b.Expiration), Phase: b.Phase, ScheduleLabel: b.ScheduleLabel,
		StorageLocation: b.StorageLocation, StorageLocationInvalid: b.StorageLocationInvalid, Errors: b.Errors, Warnings: b.Warnings,
		ItemsTotal: b.ItemsTotal, ItemsBackedUp: b.ItemsBackedUp, QueuePosition: b.QueuePosition, Validation: classTexts(b.Validation),
		ValidationCount: b.ValidationCount, HooksAttempted: b.HooksAttempted, HooksFailed: b.HooksFailed, OperationsFailed: b.OperationsFailed,
		Membership: m.Why, MembershipSchedule: m.Schedule, Disputed: m.Disputed, Reason: r.Eval.Reason, Rule: toRuleDTO(r.Eval)}
	if b.Failure != nil {
		d.Failure = velero.TextOf(*b.Failure)
	}
	return d
}

func init() {
	rowsConverters[healthsrc.TaskVelero+"/schedules"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.VeleroScheduleRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]VeleroScheduleDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, veleroScheduleDTO(r))
		}
		return out, nil
	}
	rowsConverters[healthsrc.TaskVelero+"/locations"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.VeleroLocationRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]VeleroLocationDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, veleroLocationDTO(r))
		}
		return out, nil
	}
	rowsConverters[healthsrc.TaskVelero+"/backups"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.VeleroBackupRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]VeleroBackupDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, veleroBackupDTO(r))
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out, nil
	}
}
