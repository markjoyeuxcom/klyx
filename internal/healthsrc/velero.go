package healthsrc

import (
	"sort"
	"time"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/gitops/resume"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/velero"
)

// Velero's kinds, task and fact (coverage S2a §3). The kinds are named by
// their resources, as every CRD kind is.
const (
	KindVeleroSchedules = velero.ResourceSchedules
	KindVeleroLocations = velero.ResourceLocations
	KindVeleroBackups   = velero.ResourceBackups

	TaskVelero = "velero"

	// FactVelero carries the detector's Velero servers (velero.Servers).
	FactVelero = "veleroServers"
)

// The budget per cluster (S2a §3): 960 KiB together, Backups admitted in
// two tiers, newest run first.
const (
	budgetVeleroSchedules = 128 << 10
	budgetVeleroLocations = 64 << 10
	budgetVeleroBackups   = 768 << 10
)

// Velero returns its kinds and its task (read-only; S2a).
func Velero(d NativeDeps) ([]observe.KindSpec, []observe.DomainSpec) {
	kind := func(resource string, budget int) observe.KindSpec {
		return oneVersionKind(d, resource, velero.Group, velero.APIVersion, budget, velero.Project(resource), velero.MaxObject, velero.SizeOf)
	}
	backups := kind(KindVeleroBackups, budgetVeleroBackups)
	backups.Rank = velero.Rank
	kinds := []observe.KindSpec{kind(KindVeleroSchedules, budgetVeleroSchedules), kind(KindVeleroLocations, budgetVeleroLocations), backups}
	c := d.Cluster
	facts := []string{FactVelero}
	// one tracker: the locations part shows each schedule's verdict with the
	// same resume state as the schedules part
	tr := resume.NewTracker()
	domains := []observe.DomainSpec{{ID: TaskVelero, Parts: []observe.PartSpec{
		{ID: "schedules", Entity: "Schedules", Scope: "all namespaces", Classifier: "native",
			Counted: []string{KindVeleroSchedules}, Deps: []string{KindVeleroBackups, KindVeleroLocations}, Facts: facts,
			Evaluate: evalVeleroSchedules(c, tr), Ledger: true},
		{ID: "locations", Entity: "Backup storage locations", Scope: "all namespaces", Classifier: "native",
			Counted: []string{KindVeleroLocations}, Deps: []string{KindVeleroSchedules, KindVeleroBackups}, Facts: facts,
			Evaluate: evalVeleroLocations(c, tr), Ledger: true},
		{ID: "backups", Entity: "Recent unscheduled backups", Scope: "all namespaces", Classifier: "native",
			Counted: []string{KindVeleroBackups}, Deps: []string{KindVeleroSchedules}, Facts: facts,
			Evaluate: evalVeleroBackups(c), Ledger: true},
	}}}
	return kinds, domains
}

// VeleroServers maps the detector's servers onto the facts the rules read
// (S2a §3): a server's configuration counts only once it has rolled out,
// and servers in one namespace that disagree leave it unresolved.
func VeleroServers(v capability.VeleroCapability) velero.Servers {
	out := velero.Servers{Read: v.ServersRead, ByNamespace: map[string]velero.ServerFacts{}}
	byNS := map[string][]capability.VeleroServer{}
	for _, s := range v.Servers {
		byNS[s.Namespace] = append(byNS[s.Namespace], s)
	}
	for ns, servers := range byNS {
		var facts []velero.ServerFacts
		ready := true
		for _, s := range servers {
			f := velero.ServerFacts{Ready: s.Ready}
			ready = ready && s.Ready
			if s.RolledOut {
				if ver, ok := velero.VersionOfImage(s.Image); ok {
					f.Version, f.VersionResolved = ver, true
				}
				switch {
				case s.TZFromRef:
				case s.TZSet:
					f.Zone, f.ZoneResolved = s.TZ, true
				default:
					f.Zone, f.ZoneResolved = "UTC", true
				}
				f.ScheduleDisabled = s.ScheduleDisabled
			}
			facts = append(facts, f)
		}
		merged := facts[0]
		for _, f := range facts[1:] {
			if f.Version != merged.Version || f.VersionResolved != merged.VersionResolved {
				merged.Version, merged.VersionResolved = velero.Version{}, false
			}
			if f.Zone != merged.Zone || f.ZoneResolved != merged.ZoneResolved {
				merged.Zone, merged.ZoneResolved = "", false
			}
			merged.ScheduleDisabled = merged.ScheduleDisabled || f.ScheduleDisabled
		}
		merged.Ready = ready
		out.ByNamespace[ns] = merged
	}
	return out
}

func veleroServersOf(c observe.Capture) velero.Servers {
	s, _ := c.Facts[FactVelero].(velero.Servers)
	return s
}

func veleroSchedules(c observe.Capture) []*velero.Schedule {
	var out []*velero.Schedule
	for _, x := range c.Objects[KindVeleroSchedules] {
		if s, ok := x.(*velero.Schedule); ok {
			out = append(out, s)
		}
	}
	return out
}

func veleroLocations(c observe.Capture) []*velero.Location {
	var out []*velero.Location
	for _, x := range c.Objects[KindVeleroLocations] {
		if l, ok := x.(*velero.Location); ok {
			out = append(out, l)
		}
	}
	return out
}

// veleroHistory is the Backups capture as the rules read it.
func veleroHistory(c observe.Capture) velero.History {
	objs, readable := c.Objects[KindVeleroBackups]
	h := velero.History{Readable: readable || c.Complete[KindVeleroBackups], Complete: c.Complete[KindVeleroBackups]}
	for _, x := range objs {
		if b, ok := x.(*velero.Backup); ok {
			h.Backups = append(h.Backups, b)
		}
	}
	if w, ok := c.Windows[KindVeleroBackups]; ok {
		if win, ok := velero.WindowOf(w); ok {
			h.Window = &win
		}
	}
	return h
}

func veleroRef(m velero.Meta, resource string) refInput {
	return refInput{gvr: m.APIVersion + "/" + resource, ns: m.Namespace, name: m.Name, uid: m.UID, generation: m.Generation, source: resource}
}

// VeleroLocationLink is a schedule's storage location as its own block
// (S2a §2): never the schedule's verdict.
type VeleroLocationLink struct {
	// State: "found", "not found", "not readable", "invalid" (an invalid
	// name), or "server default" (no location named and none marked default:
	// the server's flag, which Klyx does not read).
	State       string
	Name        string
	ByDefault   bool
	Label, Role string
	Validated   time.Time
}

// VeleroScheduleRow is one schedule with what decided its verdict.
type VeleroScheduleRow struct {
	Schedule *velero.Schedule
	Verdict  velero.ScheduleVerdict
	Location VeleroLocationLink
	Resumed  bool
}

// VeleroLocationRow is one storage location with the schedules that target
// it and the expired backups awaiting deletion.
type VeleroLocationRow struct {
	Location       *velero.Location
	Schedules      []VeleroScheduleRef
	SchedulesState string
	Expired        int
	ExpiredState   string
	GCFailures     map[string]int
	// SeveralDefaults: more than one location in its namespace is marked
	// default; Velero keeps the newest and unsets the others.
	SeveralDefaults bool
}

// VeleroScheduleRef names a schedule and its verdict.
type VeleroScheduleRef struct {
	Namespace, Name, Label, Role string
}

// VeleroBackupRow is one recent unscheduled backup and why it is here.
type VeleroBackupRow struct {
	Backup     *velero.Backup
	Membership velero.Membership
}

// defaultLocation is the namespace's default storage location: the newest
// marked default (Velero keeps the newest, S2a §1).
func defaultLocation(ns string, locs []*velero.Location) *velero.Location {
	var out *velero.Location
	for _, l := range locs {
		if l.Namespace == ns && l.Default && (out == nil || l.Created.After(out.Created)) {
			out = l
		}
	}
	return out
}

func coverageWord(c observe.Capture, kind string) string {
	_, has := c.Objects[kind]
	switch {
	case c.Complete[kind]:
		return "found"
	case has:
		return "incomplete"
	}
	return "not readable"
}

// locationLink resolves a schedule's storage location block.
func locationLink(s *velero.Schedule, locs []*velero.Location, readable string, servers velero.Servers, now time.Time) VeleroLocationLink {
	t := s.Template
	if t.StorageLocationInvalid {
		return VeleroLocationLink{State: "invalid"}
	}
	name, byDefault := t.StorageLocation, false
	if name == "" {
		// the default is the newest marked default: only a complete read of
		// the candidates can name it (Codex's S2a implementation review,
		// finding 6); a partial one names its candidate, unverified
		d := defaultLocation(s.Namespace, locs)
		switch {
		case readable != "found" && d != nil:
			return VeleroLocationLink{State: readable, Name: d.Name, ByDefault: true}
		case readable != "found":
			return VeleroLocationLink{State: readable, ByDefault: true}
		case d == nil:
			return VeleroLocationLink{State: "server default", ByDefault: true}
		}
		name, byDefault = d.Name, true
	}
	for _, l := range locs {
		if l.Namespace == s.Namespace && l.Name == name {
			ev := velero.LocationRule(l, servers, now)
			return VeleroLocationLink{State: "found", Name: name, ByDefault: byDefault, Label: ev.Label, Role: ev.Role.String(), Validated: l.LastValidation}
		}
	}
	if readable == "found" {
		return VeleroLocationLink{State: "not found", Name: name, ByDefault: byDefault}
	}
	return VeleroLocationLink{State: readable, Name: name, ByDefault: byDefault}
}

// scheduleSample is the resume tracker's view of a schedule (S2a §4.2): the
// fingerprint is Velero's own evidence of a run or a skip.
func scheduleSample(s *velero.Schedule) resume.Sample {
	return resume.Sample{Kind: KindVeleroSchedules, UID: s.UID, Disabled: s.Paused, HasReady: true,
		Fingerprint: resume.ReadyFingerprint(s.Phase, s.LastBackup.UTC().Format(time.RFC3339), s.LastSkipped.UTC().Format(time.RFC3339), "")}
}

// scheduleVerdict judges one schedule with its resume state.
func scheduleVerdict(s *velero.Schedule, hist velero.History, servers velero.Servers, now time.Time, tr *resume.Tracker) (velero.ScheduleVerdict, bool) {
	in := velero.ScheduleInput{S: s, History: hist, Servers: servers, Now: now}
	if r := tr.Observe(now, scheduleSample(s)); r == resume.Awaiting || r == resume.Unproven {
		at, ok := tr.ResumedAt(s.UID)
		if !ok {
			at = now
		}
		in.Resume = velero.Resume{Awaiting: true, ResumedAt: at}
	}
	return velero.ScheduleRule(in), in.Resume.Awaiting
}

func evalVeleroSchedules(cluster string, tr *resume.Tracker) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		servers, hist, locs := veleroServersOf(c), veleroHistory(c), veleroLocations(c)
		locState := coverageWord(c, KindVeleroLocations)
		scheds := veleroSchedules(c)
		var items []nativeItem
		var rows []Row[VeleroScheduleRow]
		uids := map[string]bool{}
		for _, s := range scheds {
			uids[s.UID] = true
			v, resumed := scheduleVerdict(s, hist, servers, c.Now, tr)
			it := nativeItem{v.Eval, veleroRef(s.Meta, KindVeleroSchedules)}
			items = append(items, it)
			if c.Rows {
				rows = append(rows, nativeRow(it, VeleroScheduleRow{Schedule: s, Verdict: v, Resumed: resumed,
					Location: locationLink(s, locs, locState, servers, c.Now)}))
			}
		}
		tr.Prune(KindVeleroSchedules, uids, c.Complete[KindVeleroSchedules])
		res := nativeResult(c.RoleCap, cluster, TaskVelero, "missed, failed or not running", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}

func evalVeleroLocations(cluster string, tr *resume.Tracker) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		servers, locs, scheds, hist := veleroServersOf(c), veleroLocations(c), veleroSchedules(c), veleroHistory(c)
		schedState := coverageWord(c, KindVeleroSchedules)
		locsComplete := coverageWord(c, KindVeleroLocations) == "found"
		backupState := coverageWord(c, KindVeleroBackups)
		defaults := map[string]int{}
		for _, l := range locs {
			if l.Default {
				defaults[l.Namespace]++
			}
		}
		var items []nativeItem
		var rows []Row[VeleroLocationRow]
		for _, l := range locs {
			it := nativeItem{velero.LocationRule(l, servers, c.Now), veleroRef(l.Meta, KindVeleroLocations)}
			items = append(items, it)
			if !c.Rows {
				continue
			}
			row := VeleroLocationRow{Location: l, SchedulesState: schedState, ExpiredState: backupState, SeveralDefaults: defaults[l.Namespace] > 1}
			for _, s := range scheds {
				if s.Namespace != l.Namespace {
					continue
				}
				target := s.Template.StorageLocation
				if target == "" && !s.Template.StorageLocationInvalid {
					if !locsComplete {
						// which location is the default is not proven by a
						// partial read: this list cannot be complete
						if row.SchedulesState == "found" {
							row.SchedulesState = "incomplete"
						}
						continue
					}
					if d := defaultLocation(s.Namespace, locs); d != nil {
						target = d.Name
					}
				}
				if target != l.Name {
					continue
				}
				// the schedule's own verdict, from the same capture
				v, _ := scheduleVerdict(s, hist, servers, c.Now, tr)
				row.Schedules = append(row.Schedules, VeleroScheduleRef{Namespace: s.Namespace, Name: s.Name, Label: v.Eval.Label, Role: v.Eval.Role.String()})
			}
			for _, b := range hist.Backups {
				if b.Namespace != l.Namespace || b.StorageLocation != l.Name || b.Expiration.IsZero() || b.Expiration.After(c.Now) {
					continue
				}
				row.Expired++
				if b.GCFailure != "" {
					if row.GCFailures == nil {
						row.GCFailures = map[string]int{}
					}
					row.GCFailures[b.GCFailure]++
				}
			}
			sort.Slice(row.Schedules, func(i, j int) bool { return row.Schedules[i].Name < row.Schedules[j].Name })
			rows = append(rows, nativeRow(it, row))
		}
		res := nativeResult(c.RoleCap, cluster, TaskVelero, "unavailable or not validated", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}

func evalVeleroBackups(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		servers, hist, scheds := veleroServersOf(c), veleroHistory(c), veleroSchedules(c)
		members := velero.Unscheduled(hist.Backups, scheds, servers, c.Now)
		var items []nativeItem
		var rows []Row[VeleroBackupRow]
		for _, b := range hist.Backups {
			m := members[b]
			if !m.In {
				// a backup becomes a member only by being created or changed,
				// never by time alone, so only leaving needs a clock
				continue
			}
			it := nativeItem{velero.BackupRule(b, m, servers, c.Now), veleroRef(b.Meta, KindVeleroBackups)}
			items = append(items, it)
			if c.Rows {
				rows = append(rows, nativeRow(it, VeleroBackupRow{Backup: b, Membership: m}))
			}
		}
		res := nativeResult(c.RoleCap, cluster, TaskVelero, "failed or partially failed", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}
