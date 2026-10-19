package appbridge

import (
	"context"
	"encoding/json"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/velero"
)

// Coverage S2b: the Velero lens's on-demand reads. The Restores are read
// when their tab opens and retained by nothing after the reply; the fleet
// horizon reads each cluster's schedules rows from its Observer. Neither
// adds a watch, a verdict or a write.

// VeleroConn is what VeleroService needs of one cluster.
type VeleroConn interface {
	ListVeleroRestores(ctx context.Context) (fleet.VeleroRestores, error)
	HealthRowsNow(ctx context.Context, task, part string) (observe.Rows, bool)
}

// VeleroService is bound to JS.
type VeleroService struct {
	lookup func(string) (VeleroConn, bool)
}

func NewVeleroService(lookup func(string) (VeleroConn, bool)) *VeleroService {
	return &VeleroService{lookup: lookup}
}

// VeleroMapDTO is one namespaceMapping entry.
type VeleroMapDTO struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// VeleroRestoreSourceDTO is a Restore's source backup joined to the
// retained Backups under the Backups kind's own coverage (S2b §3). State:
//   - found: in the current read (AsOf set when that read is stale);
//   - not-found: absent from a complete current read;
//   - not-retained: absent from a capped read, which never proves absence;
//   - not-in-last-read: absent from a stale read;
//   - unreadable: the Backups could not be read (Class says why);
//   - none: no backup is named yet; invalid: its name is not an object name.
type VeleroRestoreSourceDTO struct {
	State       string        `json:"state"`
	Text        string        `json:"text"`
	Backup      *VeleroRunDTO `json:"backup,omitempty"`
	Schedule    string        `json:"schedule,omitempty"`
	Attribution string        `json:"attribution,omitempty"`
	// AttributionCoverage is the Schedules read the attribution comes from.
	AttributionCoverage string     `json:"attributionCoverage,omitempty"`
	AsOf                *time.Time `json:"asOf,omitempty"`
	Class               string     `json:"class,omitempty"`
}

// VeleroRestoreDTO is one Restore: a fact, with no verdict (S2b §2).
type VeleroRestoreDTO struct {
	UID                    string                 `json:"uid"`
	Namespace              string                 `json:"namespace"`
	Name                   string                 `json:"name"`
	Created                *time.Time             `json:"created,omitempty"`
	BackupName             string                 `json:"backupName,omitempty"`
	BackupNameInvalid      bool                   `json:"backupNameInvalid,omitempty"`
	ScheduleName           string                 `json:"scheduleName,omitempty"`
	ScheduleNameInvalid    bool                   `json:"scheduleNameInvalid,omitempty"`
	IncludedNamespaces     []string               `json:"includedNamespaces"`
	IncludedCount          int                    `json:"includedCount"`
	ExcludedNamespaces     []string               `json:"excludedNamespaces"`
	ExcludedCount          int                    `json:"excludedCount"`
	Mapping                []VeleroMapDTO         `json:"mapping"`
	MappingCount           int                    `json:"mappingCount"`
	ResourcesIncluded      int                    `json:"resourcesIncluded,omitempty"`
	ResourcesExcluded      int                    `json:"resourcesExcluded,omitempty"`
	LabelSelector          bool                   `json:"labelSelector,omitempty"`
	OrLabelSelectors       int                    `json:"orLabelSelectors,omitempty"`
	RestorePVs             *bool                  `json:"restorePVs,omitempty"`
	ExistingResourcePolicy string                 `json:"existingResourcePolicy,omitempty"`
	ItemOperationTimeout   string                 `json:"itemOperationTimeout,omitempty"`
	Hooks                  int                    `json:"hooks"` // counted; commands withheld
	ResourceModifier       string                 `json:"resourceModifier,omitempty"`
	ResourcePolicy         string                 `json:"resourcePolicy,omitempty"`
	Phase                  string                 `json:"phase"`
	Started                *time.Time             `json:"started,omitempty"`
	Completed              *time.Time             `json:"completed,omitempty"`
	Errors                 int                    `json:"errors"`
	Warnings               int                    `json:"warnings"`
	ItemsTotal             int                    `json:"itemsTotal,omitempty"`
	ItemsRestored          int                    `json:"itemsRestored,omitempty"`
	OperationsAttempted    int                    `json:"operationsAttempted,omitempty"`
	OperationsCompleted    int                    `json:"operationsCompleted,omitempty"`
	OperationsFailed       int                    `json:"operationsFailed,omitempty"`
	HooksAttempted         int                    `json:"hooksAttempted,omitempty"`
	HooksFailed            int                    `json:"hooksFailed,omitempty"`
	Failure                string                 `json:"failure,omitempty"` // Klyx's class, never Velero's text
	FailureWithheld        bool                   `json:"failureWithheld,omitempty"`
	Validation             []string               `json:"validation"`
	ValidationCount        int                    `json:"validationCount"`
	Cut                    bool                   `json:"cut,omitempty"`
	Source                 VeleroRestoreSourceDTO `json:"source"`
}

// VeleroRestoresDTO is one read. Cluster and ReadID fence it: the view
// drops a reply for another cluster or an older read. State is read,
// not-served, forbidden or error.
type VeleroRestoresDTO struct {
	Cluster   string             `json:"cluster"`
	ReadID    string             `json:"readId"`
	State     string             `json:"state"`
	Error     string             `json:"error,omitempty"`
	Restores  []VeleroRestoreDTO `json:"restores"`
	Cap       int                `json:"cap"`
	More      bool               `json:"more,omitempty"`
	Remaining *int64             `json:"remaining,omitempty"` // the server's count, only when it supplied one
	ReadAt    *time.Time         `json:"readAt,omitempty"`
	// Backups is the Backups kind's coverage the source join read.
	Backups string `json:"backups"`
}

// veleroBackups is the Backups kind's coverage and its retained objects,
// from one capture of the backups part.
type veleroBackups struct {
	state  string // live | capped | stale | unreadable
	class  string
	asOf   *time.Time
	byName map[string]healthsrc.VeleroBackupRow
}

func readVeleroBackups(ctx context.Context, conn VeleroConn) veleroBackups {
	rows, ok := conn.HealthRowsNow(ctx, healthsrc.TaskVelero, "backups")
	if !ok {
		return veleroBackups{state: "unreadable", class: "Klyx has no live view of this cluster's health"}
	}
	out := veleroBackups{state: "unreadable", byName: map[string]healthsrc.VeleroBackupRow{}}
	for _, k := range rows.Part.Kinds {
		if k.Kind != healthsrc.KindVeleroBackups { // the Observer names a kind by its resource
			continue
		}
		switch {
		case k.Coverage == observe.Live && k.Truncated:
			out.state = "capped"
		case k.Coverage == observe.Live:
			out.state = "live"
		case k.Coverage == observe.Stale:
			out.state, out.asOf = "stale", k.LastGoodAt
		default:
			out.class = string(k.Coverage)
			if k.Class != "" {
				out.class = k.Class
			}
		}
	}
	if set, ok := rows.Rows.(healthsrc.RowSet[healthsrc.VeleroBackupRow]); ok && out.state != "unreadable" {
		for _, r := range set.Rows {
			out.byName[r.Data.Backup.Namespace+"/"+r.Data.Backup.Name] = r.Data
		}
	}
	return out
}

// join resolves one Restore's source backup (S2b §3).
func (bk veleroBackups) join(r *velero.Restore) VeleroRestoreSourceDTO {
	switch {
	case r.BackupNameInvalid:
		return VeleroRestoreSourceDTO{State: "invalid", Text: "the source backup's name is not a valid object name"}
	case r.BackupName == "":
		return VeleroRestoreSourceDTO{State: "none", Text: "no backup is named yet"}
	case bk.state == "unreadable":
		return VeleroRestoreSourceDTO{State: "unreadable", Text: "the Backups could not be read", Class: bk.class}
	}
	if row, ok := bk.byName[r.Namespace+"/"+r.BackupName]; ok {
		b := row.Backup
		d := VeleroRestoreSourceDTO{State: "found", Text: "retained", Backup: veleroRunDTO(b, b.RunTime(), row.Attribution.How == "proven"),
			Schedule: row.Attribution.Schedule, Attribution: row.Attribution.How, AttributionCoverage: row.Attribution.Coverage}
		if bk.state == "stale" {
			d.Text, d.AsOf = "retained as of the last good read", bk.asOf
		}
		if row.Expired {
			d.Text += "; expired, awaiting deletion"
		}
		return d
	}
	switch bk.state {
	case "capped":
		return VeleroRestoreSourceDTO{State: "not-retained", Text: "not in the retained window: a capped read never proves absence"}
	case "stale":
		return VeleroRestoreSourceDTO{State: "not-in-last-read", Text: "not in the last good read", AsOf: bk.asOf}
	}
	return VeleroRestoreSourceDTO{State: "not-found", Text: "not found in the cluster"}
}

func veleroRestoreDTO(r *velero.Restore, bk veleroBackups) VeleroRestoreDTO {
	d := VeleroRestoreDTO{UID: r.UID, Namespace: r.Namespace, Name: r.Name, Created: timePtrOf(r.Created), BackupName: r.BackupName,
		BackupNameInvalid: r.BackupNameInvalid, ScheduleName: r.ScheduleName, ScheduleNameInvalid: r.ScheduleNameInvalid,
		IncludedNamespaces: nonNil(r.Included), IncludedCount: r.IncludedCount, ExcludedNamespaces: nonNil(r.Excluded), ExcludedCount: r.ExcludedCount,
		Mapping: []VeleroMapDTO{}, MappingCount: r.MappingCount, ResourcesIncluded: r.ResourcesIncluded, ResourcesExcluded: r.ResourcesExcluded,
		LabelSelector: r.LabelSelector, OrLabelSelectors: r.OrLabelSelectors, RestorePVs: r.RestorePVs, ExistingResourcePolicy: r.ExistingResourcePolicy,
		ItemOperationTimeout: r.ItemOperationTimeout, Hooks: r.Hooks, ResourceModifier: r.ResourceModifier, ResourcePolicy: r.ResourcePolicy,
		Phase: r.Phase, Started: timePtrOf(r.Start), Completed: timePtrOf(r.Completion), Errors: r.Errors, Warnings: r.Warnings,
		ItemsTotal: r.ItemsTotal, ItemsRestored: r.ItemsRestored, OperationsAttempted: r.OperationsAttempted, OperationsCompleted: r.OperationsCompleted,
		OperationsFailed: r.OperationsFailed, HooksAttempted: r.HooksAttempted, HooksFailed: r.HooksFailed,
		Validation: classTexts(r.Validation), ValidationCount: r.ValidationCount, Cut: r.Cut, Source: bk.join(r)}
	for _, m := range r.Mapping {
		d.Mapping = append(d.Mapping, VeleroMapDTO{From: m.From, To: m.To})
	}
	if r.Failure != nil {
		d.Failure, d.FailureWithheld = velero.TextOf(*r.Failure), r.Failure.Withheld
	}
	fitRestoreDTO(&d)
	return d
}

// fitRestoreDTO holds the serialised Restore, source join included, within
// velero.MaxObject (S2b §3): the bridge spells out field names and Klyx's
// class texts, so the projection's own fit is not enough (Codex's S2b
// implementation review, finding 5). It cuts from the tails of the mapping,
// then the excluded and included namespaces, then the validation classes,
// then the joined backup's details; every count stays whole and Cut says
// so.
func fitRestoreDTO(d *VeleroRestoreDTO) {
	for size := restoreDTOSize(d); size > velero.MaxObject; {
		switch {
		case len(d.Mapping) > 0:
			d.Mapping = d.Mapping[:len(d.Mapping)-1]
		case len(d.ExcludedNamespaces) > 0:
			d.ExcludedNamespaces = d.ExcludedNamespaces[:len(d.ExcludedNamespaces)-1]
		case len(d.IncludedNamespaces) > 0:
			d.IncludedNamespaces = d.IncludedNamespaces[:len(d.IncludedNamespaces)-1]
		case len(d.Validation) > 0:
			d.Validation = d.Validation[:len(d.Validation)-1]
		case d.Source.Backup != nil:
			d.Source.Backup = nil
		default:
			return // nothing left to cut: every other field is bounded on its own
		}
		d.Cut = true
		size = restoreDTOSize(d)
	}
}

func restoreDTOSize(d *VeleroRestoreDTO) int {
	b, err := json.Marshal(d)
	if err != nil {
		return velero.MaxObject + 1
	}
	return len(b)
}

// GetVeleroRestores reads one cluster's Restores on demand, capped at
// fleet.VeleroRestoresCap, each joined to the retained Backups. readID is
// echoed for the view's fence.
func (s *VeleroService) GetVeleroRestores(cluster, readID string) VeleroRestoresDTO {
	out := VeleroRestoresDTO{Cluster: cluster, ReadID: readID, Restores: []VeleroRestoreDTO{}, Cap: fleet.VeleroRestoresCap}
	conn, ok := s.lookup(cluster)
	if !ok {
		out.State, out.Error = "error", "cluster not connected"
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	res, err := conn.ListVeleroRestores(ctx)
	if err != nil {
		out.State, out.Error = "error", err.Error()
		return out
	}
	out.ReadAt = timePtrOf(res.ReadAt)
	switch {
	case !res.Served:
		out.State = "not-served"
		return out
	case res.Forbidden:
		out.State = "forbidden"
		return out
	}
	out.State, out.More, out.Remaining = "read", res.More, res.Remaining
	bk := readVeleroBackups(ctx, conn)
	out.Backups = bk.state
	for _, r := range res.Restores {
		out.Restores = append(out.Restores, veleroRestoreDTO(r, bk))
	}
	return out
}

// VeleroFleetHorizonDTO is one cluster's schedules for the fleet Backups
// view (S2b §2): its rows from one capture, as of that capture, with the
// part's coverage. State is read, no-observer or no-velero.
type VeleroFleetHorizonDTO struct {
	Cluster   string                 `json:"cluster"`
	State     string                 `json:"state"`
	AsOf      *time.Time             `json:"asOf,omitempty"`
	Coverage  string                 `json:"coverage,omitempty"`
	Kinds     []observe.KindCoverage `json:"kinds"`
	Schedules []VeleroScheduleDTO    `json:"schedules"`
}

// GetBackupHorizon reads one cluster's schedules rows (the S2a verdict and
// the S2b horizon) from its Observer, once.
func (s *VeleroService) GetBackupHorizon(cluster string) VeleroFleetHorizonDTO {
	out := VeleroFleetHorizonDTO{Cluster: cluster, Kinds: []observe.KindCoverage{}, Schedules: []VeleroScheduleDTO{}}
	conn, ok := s.lookup(cluster)
	if !ok {
		out.State = "no-observer"
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	rows, ok := conn.HealthRowsNow(ctx, healthsrc.TaskVelero, "schedules")
	if !ok {
		out.State = "no-observer"
		return out
	}
	out.AsOf, out.Coverage, out.Kinds = timePtrOf(rows.EvaluatedAt), string(rows.Part.Coverage), append(out.Kinds, rows.Part.Kinds...)
	if rows.Part.Coverage == observe.Absent {
		out.State = "no-velero"
		return out
	}
	out.State = "read"
	if set, ok := rows.Rows.(healthsrc.RowSet[healthsrc.VeleroScheduleRow]); ok {
		for _, r := range set.Rows {
			out.Schedules = append(out.Schedules, veleroScheduleDTO(r))
		}
	}
	return out
}
