package velero

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/moomora/klyx/internal/urlscrub"
)

// The API group and version S2a reads (S2a §1).
const (
	Group      = "velero.io"
	APIVersion = "v1"
)

// Kinds and resources.
const (
	KindSchedule = "Schedule"
	KindLocation = "BackupStorageLocation"
	KindBackup   = "Backup"

	ResourceSchedules = "schedules"
	ResourceLocations = "backupstoragelocations"
	ResourceBackups   = "backups"
)

// Labels Velero sets on Backups.
const (
	LabelSchedule  = "velero.io/schedule-name"
	LabelGCFailure = "velero.io/gc-failure"
)

// Projection limits (S2a §3): one projection is measured after it is
// built against MaxObject; display lists and strings are capped.
const (
	MaxObject      = 4 << 10
	maxName        = 253
	maxNamespaces  = 20
	maxSnapshotLoc = 2
	maxClasses     = 4
	maxVocabulary  = 3
	maxConfigKeys  = 32
	maxConfigKey   = 64
	maxShort       = 64
	maxPrefix      = 256
	maxOwners      = 2
)

// Meta is a projected object's identity.
type Meta struct {
	APIVersion string    `json:"av"`
	Namespace  string    `json:"ns"`
	Name       string    `json:"n"`
	UID        string    `json:"u"`
	Generation int64     `json:"g,omitempty"`
	Created    time.Time `json:"c"`
}

// Template is what a Schedule's backup template says, as configured now.
type Template struct {
	StorageLocation         string   `json:"sl,omitempty"`
	StorageLocationInvalid  bool     `json:"sli,omitempty"`
	TTL                     string   `json:"ttl,omitempty"`
	SnapshotVolumes         *bool    `json:"sv,omitempty"`
	DefaultVolumesToFs      *bool    `json:"fs,omitempty"`
	SnapshotMoveData        *bool    `json:"md,omitempty"`
	VolumeSnapshotLocations []string `json:"vsl,omitempty"`
	VolumeSnapshotCount     int      `json:"vslN,omitempty"`
	Included                []string `json:"in,omitempty"`
	IncludedCount           int      `json:"inN,omitempty"`
	Excluded                []string `json:"ex,omitempty"`
	ExcludedCount           int      `json:"exN,omitempty"`
	Hooks                   int      `json:"hooks,omitempty"`
}

// Schedule is one projected Schedule.
type Schedule struct {
	Meta
	Cron               string    `json:"cron"`
	CronTooLong        bool      `json:"cronLong,omitempty"`
	Paused             bool      `json:"paused,omitempty"`
	SkipImmediately    *bool     `json:"skip,omitempty"`
	UseOwnerReferences bool      `json:"owners,omitempty"`
	Phase              string    `json:"phase,omitempty"`
	LastBackup         time.Time `json:"lb"`
	LastSkipped        time.Time `json:"ls"`
	Validation         []Class   `json:"ve,omitempty"`
	ValidationCount    int       `json:"veN,omitempty"`
	Template           Template  `json:"t"`
	Size               int       `json:"-"`
}

// Location is one projected BackupStorageLocation.
type Location struct {
	Meta
	Provider            string        `json:"p,omitempty"`
	Bucket              string        `json:"b,omitempty"`
	Prefix              string        `json:"pre,omitempty"`
	CACertSet           bool          `json:"ca,omitempty"`
	CACertRef           string        `json:"caRef,omitempty"`
	Credential          string        `json:"cred,omitempty"`
	CredentialKey       string        `json:"credKey,omitempty"`
	Default             bool          `json:"def,omitempty"`
	AccessMode          string        `json:"am,omitempty"`
	SyncPeriod          time.Duration `json:"sync,omitempty"`
	SyncPeriodSet       bool          `json:"syncSet,omitempty"`
	ValidationFrequency time.Duration `json:"vf,omitempty"`
	ValidationSet       bool          `json:"vfSet,omitempty"`
	Phase               string        `json:"phase,omitempty"`
	LastValidation      time.Time     `json:"lv"`
	LastSynced          time.Time     `json:"lsy"`
	Message             *Class        `json:"msg,omitempty"`
	ConfigKeys          []string      `json:"cfg,omitempty"`
	ConfigCount         int           `json:"cfgN,omitempty"`
	Region              string        `json:"region,omitempty"`
	S3URL               string        `json:"s3,omitempty"`
	PublicURL           string        `json:"pub,omitempty"`
	Size                int           `json:"-"`
}

// Owner is one Schedule ownerReference of a Backup.
type Owner struct {
	UID  string `json:"u"`
	Name string `json:"n"`
}

// Backup is one projected Backup.
type Backup struct {
	Meta
	ScheduleLabel          string    `json:"sch,omitempty"`
	Owners                 []Owner   `json:"own,omitempty"`
	OwnersIncomplete       bool      `json:"ownX,omitempty"`
	StorageLocation        string    `json:"sl,omitempty"`
	StorageLocationInvalid bool      `json:"sli,omitempty"`
	SnapshotLocations      []string  `json:"vsl,omitempty"`
	SnapshotLocationCount  int       `json:"vslN,omitempty"`
	Phase                  string    `json:"phase,omitempty"`
	Start                  time.Time `json:"st"`
	Completion             time.Time `json:"co"`
	Expiration             time.Time `json:"ex"`
	Errors                 int       `json:"err,omitempty"`
	Warnings               int       `json:"warn,omitempty"`
	ItemsTotal             int       `json:"it,omitempty"`
	ItemsBackedUp          int       `json:"ib,omitempty"`
	QueuePosition          int       `json:"q,omitempty"`
	Failure                *Class    `json:"fail,omitempty"`
	Validation             []Class   `json:"ve,omitempty"`
	ValidationCount        int       `json:"veN,omitempty"`
	GCFailure              string    `json:"gc,omitempty"`
	HooksAttempted         int       `json:"ha,omitempty"`
	HooksFailed            int       `json:"hf,omitempty"`
	SnapshotsAttempted     int       `json:"sa,omitempty"`
	SnapshotsCompleted     int       `json:"sc,omitempty"`
	CSIAttempted           int       `json:"ca,omitempty"`
	CSICompleted           int       `json:"cc,omitempty"`
	OperationsAttempted    int       `json:"oa,omitempty"`
	OperationsCompleted    int       `json:"oc,omitempty"`
	OperationsFailed       int       `json:"of,omitempty"`
	Size                   int       `json:"-"`
}

// RunTime is a Backup's run time (S2a §3, revision 4): its start, else its
// creation. It orders the cap's tier 1 and bounds the 24-hour window.
func (b *Backup) RunTime() time.Time {
	if !b.Start.IsZero() {
		return b.Start
	}
	return b.Created
}

// Blocking reports a phase Velero's overlap guard reads (S2a §1).
func Blocking(phase string) bool { return phase == "" || phase == "New" || phase == "InProgress" }

// Terminal reports a Backup phase that ends a run.
func Terminal(phase string) bool {
	switch phase {
	case "Completed", "PartiallyFailed", "Failed", "FailedValidation":
		return true
	}
	return false
}

// SizeOf is the budget's Size function for a projection.
func SizeOf(v any) int {
	switch o := v.(type) {
	case *Schedule:
		return o.Size
	case *Location:
		return o.Size
	case *Backup:
		return o.Size
	}
	return MaxObject + 1
}

// Rank is the Backups kind's admission order (S2a §3): tier 0 for the
// overlap guard's phases, then tier 1; within a tier the run time
// descending, then the key. It changes when a phase changes tier, and the
// Observer re-places the object.
func Rank(key string, v any) string {
	b, ok := v.(*Backup)
	if !ok {
		return "9" + key
	}
	tier := "1"
	if Blocking(b.Phase) {
		tier = "0"
	}
	var secs int64
	if t := b.RunTime(); !t.IsZero() && t.Unix() > 0 {
		secs = t.Unix()
	}
	return fmt.Sprintf("%s%019d/%s", tier, math.MaxInt64-secs, key)
}

// Window is a capped Backups kind's boundary, decoded from its rank.
type Window struct {
	// Tier0: the window is in tier 0, so no terminal Backup is retained and
	// Velero's guard is unresolved.
	Tier0 bool
	// W is the run time of the first left-out tier-1 Backup: every Backup
	// whose run time is after it is retained.
	W time.Time
}

// WindowOf decodes a window rank; ok is false for a rank Rank did not make.
func WindowOf(rank string) (Window, bool) {
	if len(rank) < 20 || (rank[0] != '0' && rank[0] != '1') {
		return Window{}, false
	}
	n, err := strconv.ParseInt(rank[1:20], 10, 64)
	if err != nil {
		return Window{}, false
	}
	return Window{Tier0: rank[0] == '0', W: time.Unix(math.MaxInt64-n, 0).UTC()}, true
}

// ---- building ------------------------------------------------------------

func validName(s string) bool {
	return s != "" && len(s) <= maxName && len(validation.IsDNS1123Subdomain(s)) == 0
}

func validNamespace(s string) bool {
	return s == "*" || (s != "" && len(validation.IsDNS1123Label(s)) == 0)
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// disp is a display string: the URL rule (S0b) first - a free string can
// carry a URL with user info - then the clip (Codex's S2a implementation
// review, finding 2).
func disp(s string, n int) string { return clip(urlscrub.Field(s).Text, n) }

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

func str(u *unstructured.Unstructured, path ...string) string {
	v, _, _ := unstructured.NestedString(u.Object, path...)
	return v
}

func boolp(u *unstructured.Unstructured, path ...string) *bool {
	v, ok, err := unstructured.NestedBool(u.Object, path...)
	if !ok || err != nil {
		return nil
	}
	return &v
}

func num(u *unstructured.Unstructured, path ...string) int {
	v, ok, err := unstructured.NestedFieldNoCopy(u.Object, path...)
	if !ok || err != nil {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func ts(u *unstructured.Unstructured, path ...string) time.Time {
	s := str(u, path...)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func strs(u *unstructured.Unstructured, path ...string) []string {
	v, _, _ := unstructured.NestedStringSlice(u.Object, path...)
	return v
}

func dur(u *unstructured.Unstructured, path ...string) (time.Duration, bool) {
	s := str(u, path...)
	if s == "" {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, false
	}
	return d, true
}

func metaOf(u *unstructured.Unstructured) Meta {
	return Meta{APIVersion: clip(u.GetAPIVersion(), maxShort), Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID()),
		Generation: u.GetGeneration(), Created: u.GetCreationTimestamp().UTC()}
}

// names keeps at most n valid names (by valid), with the total count.
func names(in []string, n int, valid func(string) bool) ([]string, int) {
	var out []string
	for _, s := range in {
		if len(out) == n {
			break
		}
		if valid(s) {
			out = append(out, s)
		} else {
			out = append(out, "(invalid name)")
		}
	}
	return out, len(in)
}

// classes classifies a field's messages, at most maxClasses, each with at
// most maxVocabulary diagnoses.
func classes(field string, msgs []string) ([]Class, int) {
	var out []Class
	for _, m := range msgs {
		if len(out) == maxClasses {
			break
		}
		if c, ok := Classify(field, m); ok {
			out = append(out, bounded(c))
		}
	}
	return out, len(msgs)
}

func bounded(c Class) Class {
	if len(c.Vocabulary) > maxVocabulary {
		c.Vocabulary = c.Vocabulary[:maxVocabulary]
	}
	return c
}

func size(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return MaxObject + 1
	}
	return len(b)
}

// ProjectSchedule builds a Schedule's projection. It never mutates u.
func ProjectSchedule(u *unstructured.Unstructured) *Schedule {
	s := &Schedule{Meta: metaOf(u), Phase: disp(str(u, "status", "phase"), maxShort),
		Paused: boolp(u, "spec", "paused") != nil && *boolp(u, "spec", "paused"), SkipImmediately: boolp(u, "spec", "skipImmediately"),
		LastBackup: ts(u, "status", "lastBackup"), LastSkipped: ts(u, "status", "lastSkipped")}
	if p := boolp(u, "spec", "useOwnerReferencesInBackup"); p != nil {
		s.UseOwnerReferences = *p
	}
	if c := str(u, "spec", "schedule"); len(c) > maxCron {
		s.CronTooLong = true
	} else {
		s.Cron = c
	}
	s.Validation, s.ValidationCount = classes(FieldScheduleValidation, strs(u, "status", "validationErrors"))
	t := &s.Template
	if sl := str(u, "spec", "template", "storageLocation"); sl != "" {
		if validName(sl) {
			t.StorageLocation = sl
		} else {
			t.StorageLocationInvalid = true
		}
	}
	t.TTL = disp(str(u, "spec", "template", "ttl"), maxShort)
	t.SnapshotVolumes = boolp(u, "spec", "template", "snapshotVolumes")
	t.DefaultVolumesToFs = boolp(u, "spec", "template", "defaultVolumesToFsBackup")
	t.SnapshotMoveData = boolp(u, "spec", "template", "snapshotMoveData")
	t.VolumeSnapshotLocations, t.VolumeSnapshotCount = names(strs(u, "spec", "template", "volumeSnapshotLocations"), maxSnapshotLoc, validName)
	t.Included, t.IncludedCount = names(strs(u, "spec", "template", "includedNamespaces"), maxNamespaces, validNamespace)
	t.Excluded, t.ExcludedCount = names(strs(u, "spec", "template", "excludedNamespaces"), maxNamespaces, validNamespace)
	if hooks, ok, _ := unstructured.NestedSlice(u.Object, "spec", "template", "hooks", "resources"); ok {
		t.Hooks = len(hooks)
	}
	s.Size = size(s)
	return s
}

// The BSL config keys whose values Klyx shows (S2a §6): the region, and
// the origin of the endpoint URLs. Every other key shows by name only.
const (
	cfgRegion    = "region"
	cfgS3URL     = "s3Url"
	cfgPublicURL = "publicUrl"
)

// ProjectLocation builds a BackupStorageLocation's projection.
func ProjectLocation(u *unstructured.Unstructured) *Location {
	l := &Location{Meta: metaOf(u), Provider: disp(str(u, "spec", "provider"), maxName),
		Bucket: disp(str(u, "spec", "objectStorage", "bucket"), maxName), Prefix: disp(str(u, "spec", "objectStorage", "prefix"), maxPrefix),
		CACertSet: str(u, "spec", "objectStorage", "caCert") != "", AccessMode: disp(str(u, "spec", "accessMode"), maxShort),
		Phase: disp(str(u, "status", "phase"), maxShort), LastValidation: ts(u, "status", "lastValidationTime"), LastSynced: ts(u, "status", "lastSyncedTime")}
	if p := boolp(u, "spec", "default"); p != nil {
		l.Default = *p
	}
	if ref := str(u, "spec", "objectStorage", "caCertRef", "name"); ref != "" {
		l.CACertRef = clipName(ref)
	}
	l.Credential, l.CredentialKey = clipName(str(u, "spec", "credential", "name")), disp(str(u, "spec", "credential", "key"), maxName)
	l.SyncPeriod, l.SyncPeriodSet = dur(u, "spec", "backupSyncPeriod")
	l.ValidationFrequency, l.ValidationSet = dur(u, "spec", "validationFrequency")
	if m := str(u, "status", "message"); m != "" {
		c, _ := Classify(FieldLocationMessage, m)
		c = bounded(c)
		l.Message = &c
	}
	if cfg, ok, _ := unstructured.NestedStringMap(u.Object, "spec", "config"); ok {
		keys := make([]string, 0, len(cfg))
		for k := range cfg {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		l.ConfigCount = len(keys)
		for _, k := range keys {
			if len(l.ConfigKeys) == maxConfigKeys {
				break
			}
			l.ConfigKeys = append(l.ConfigKeys, disp(k, maxConfigKey))
		}
		l.Region = disp(cfg[cfgRegion], maxShort)
		if v := cfg[cfgS3URL]; v != "" {
			l.S3URL = clip(urlscrub.OriginOf(v), maxName)
		}
		if v := cfg[cfgPublicURL]; v != "" {
			l.PublicURL = clip(urlscrub.OriginOf(v), maxName)
		}
	}
	l.Size = size(l)
	return l
}

// clipName keeps a valid object name, else the invalid marker.
func clipName(s string) string {
	if s == "" || validName(s) {
		return s
	}
	return "(invalid name)"
}

// gcFailures are the GC's label values (gc_controller.go 45-48).
var gcFailures = map[string]bool{"BSLNotFound": true, "BSLCannotGet": true, "BSLReadOnly": true}

// ProjectBackup builds a Backup's projection, validating every join input
// itself (S2a §3, revision 2): no API limit bounds them all.
func ProjectBackup(u *unstructured.Unstructured) *Backup {
	b := &Backup{Meta: metaOf(u), ScheduleLabel: u.GetLabels()[LabelSchedule], Phase: disp(str(u, "status", "phase"), maxShort),
		Start: ts(u, "status", "startTimestamp"), Completion: ts(u, "status", "completionTimestamp"), Expiration: ts(u, "status", "expiration"),
		Errors: num(u, "status", "errors"), Warnings: num(u, "status", "warnings"),
		ItemsTotal: num(u, "status", "progress", "totalItems"), ItemsBackedUp: num(u, "status", "progress", "itemsBackedUp"),
		QueuePosition:  num(u, "status", "queuePosition"),
		HooksAttempted: num(u, "status", "hookStatus", "hooksAttempted"), HooksFailed: num(u, "status", "hookStatus", "hooksFailed"),
		SnapshotsAttempted: num(u, "status", "volumeSnapshotsAttempted"), SnapshotsCompleted: num(u, "status", "volumeSnapshotsCompleted"),
		CSIAttempted: num(u, "status", "csiVolumeSnapshotsAttempted"), CSICompleted: num(u, "status", "csiVolumeSnapshotsCompleted"),
		OperationsAttempted: num(u, "status", "backupItemOperationsAttempted"), OperationsCompleted: num(u, "status", "backupItemOperationsCompleted"),
		OperationsFailed: num(u, "status", "backupItemOperationsFailed")}
	if g := u.GetLabels()[LabelGCFailure]; g != "" {
		if gcFailures[g] {
			b.GCFailure = g
		} else {
			b.GCFailure = "other"
		}
	}
	for _, o := range u.GetOwnerReferences() {
		if o.Kind != KindSchedule {
			continue
		}
		if len(b.Owners) == maxOwners || !uuidRe.MatchString(string(o.UID)) || !validName(o.Name) {
			b.OwnersIncomplete = true
			continue
		}
		b.Owners = append(b.Owners, Owner{UID: string(o.UID), Name: o.Name})
	}
	if sl := str(u, "spec", "storageLocation"); sl != "" {
		if validName(sl) {
			b.StorageLocation = sl
		} else {
			b.StorageLocationInvalid = true
		}
	}
	b.SnapshotLocations, b.SnapshotLocationCount = names(strs(u, "spec", "volumeSnapshotLocations"), maxSnapshotLoc, validName)
	if m := str(u, "status", "failureReason"); m != "" {
		c, _ := Classify(FieldBackupFailure, m)
		c = bounded(c)
		b.Failure = &c
	}
	b.Validation, b.ValidationCount = classes(FieldBackupValidation, strs(u, "status", "validationErrors"))
	b.Size = size(b)
	return b
}

// Project is the KindSpec projection for one Velero resource.
func Project(resource string) func(obj any) (any, error) {
	return func(obj any) (any, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("velero: want *Unstructured, got %T", obj)
		}
		switch resource {
		case ResourceSchedules:
			return ProjectSchedule(u), nil
		case ResourceLocations:
			return ProjectLocation(u), nil
		case ResourceBackups:
			return ProjectBackup(u), nil
		}
		return nil, fmt.Errorf("velero: no projection for %s", resource)
	}
}
