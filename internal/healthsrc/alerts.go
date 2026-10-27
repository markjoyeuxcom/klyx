package healthsrc

import (
	"context"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/observe"
)

// The Alerts task (coverage S8a): the cluster's Alertmanager read as an
// Observer snapshot keyed by its source identity, and one part whose counts
// stay apart from every object count. Never ledgered and never notified: an
// alert fingerprint is not a Kubernetes UID.
const (
	SnapAlerts = "alerts"
	TaskAlerts = "alerts"

	AlertsCadence     = 30 * time.Second
	AlertsReadTimeout = 10 * time.Second
	alertsTop         = 5
)

// Subject states (S8a §4, §5): what Klyx can say about the object an alert's
// labels name.
const (
	SubjectFound          = "found"
	SubjectNotFound       = "not found"       // a held kind, read live and complete
	SubjectNotEstablished = "not established" // a held kind not read live and complete
	SubjectNotLookedUp    = "not looked up"   // a kind the Observer does not hold
	// SubjectLastObserved: held, but that kind's read is not live and
	// complete now - the object as last observed, never current evidence.
	SubjectLastObserved = "last observed"
)

// AlertsDeps are what the Alerts snapshot needs: the connection's keyed read.
type AlertsDeps struct {
	Cluster string
	Read    func(ctx context.Context, key string) (alertmanager.Read, error)
	// ReadAt, when set, is Read with the time the alerts were acquired: a
	// share of an installation's read keeps that read's time (Settings S3a
	// §3.2)
	ReadAt func(ctx context.Context, key string) (alertmanager.Read, time.Time, error)
}

// subjectKinds are the held kinds an alert's subject can be looked up in.
var subjectKinds = map[string]string{
	"Pod": KindPods, "Deployment": KindDeployments, "StatefulSet": KindStatefulSets,
	"DaemonSet": KindDaemonSets, "Node": KindNodes, "Namespace": KindNamespaces,
}

// Alerts returns the alerts snapshot and its task.
func Alerts(d AlertsDeps) ([]observe.SnapshotSpec, []observe.DomainSpec) {
	snap := observe.SnapshotSpec{Name: SnapAlerts, Scope: "this cluster's alerts", Cadence: AlertsCadence, Timeout: AlertsReadTimeout,
		ReadKeyed: func(ctx context.Context, key string) ([]any, bool, error) {
			r, err := d.Read(ctx, key)
			if err != nil {
				return nil, false, err
			}
			return []any{r}, r.Truncated, nil
		}}
	if d.ReadAt != nil {
		snap.ReadKeyed, snap.ReadKeyedAt = nil, func(ctx context.Context, key string) ([]any, bool, time.Time, error) {
			r, at, err := d.ReadAt(ctx, key)
			if err != nil {
				return nil, false, time.Time{}, err
			}
			return []any{r}, r.Truncated, at, nil
		}
	}
	domains := []observe.DomainSpec{{ID: TaskAlerts, Parts: []observe.PartSpec{{
		ID: "alerts", Entity: "Alertmanager alerts", Scope: "this cluster's alerts", Classifier: "alerts",
		Counted:  []string{SnapAlerts},
		Deps:     []string{KindPods, KindDeployments, KindStatefulSets, KindDaemonSets, KindNodes, KindNamespaces},
		Evaluate: func(c observe.Capture) observe.PartResult { return evalAlerts(d.Cluster, c) },
	}}}}
	return []observe.SnapshotSpec{snap}, domains
}

// AlertRows are the alerts part's rows: the read's source line and every
// alert with its subject as Klyx can state it.
type AlertRows struct {
	Identity  string
	Source    string
	Match     map[string]string
	Truncated bool
	Alerts    []AlertRow
	// Lineage is the read's provenance (Settings S3a §3.4): an AI read of
	// these rows records it, and every later send checks it
	Lineage alertmanager.Lineage
}

// AlertRow is one alert's display values and its subject's state.
type AlertRow struct {
	Key   string // opaque: source identity and fingerprint (alertmanager.AlertKey)
	Alert alertmanager.DisplayAlert
	// Subject is the raw subject, for in-process correlation only (Explain's
	// name match for a kind Klyx does not hold); the rows DTO never carries it.
	Subject              alertmanager.Subject
	SubjectGVR           string
	SubjectState         string
	SubjectUID           string
	SubjectCreated       *time.Time
	StartedBeforeCreated bool
}

type heldObject struct {
	uid     string
	created time.Time
}

// subjectIndex is the held kinds' objects by namespace/name, and whether
// each kind was read live and complete.
type subjectIndex struct {
	objects  map[string]map[string]heldObject
	complete map[string]bool
}

func indexSubjects(c observe.Capture) subjectIndex {
	idx := subjectIndex{objects: map[string]map[string]heldObject{}, complete: map[string]bool{}}
	for _, kind := range subjectKinds {
		m := map[string]heldObject{}
		for _, o := range c.Objects[kind] {
			acc, err := meta.Accessor(o)
			if err != nil {
				continue
			}
			m[acc.GetNamespace()+"/"+acc.GetName()] = heldObject{uid: string(acc.GetUID()), created: acc.GetCreationTimestamp().Time}
		}
		idx.objects[kind] = m
		idx.complete[kind] = c.Current[kind] && c.Complete[kind]
	}
	return idx
}

// resolve states an alert's subject: found by name, or why not.
func (idx subjectIndex) resolve(s alertmanager.Subject) (state, uid string, created *time.Time) {
	if s.Kind == "" {
		return "", "", nil
	}
	kind, held := subjectKinds[s.Kind]
	if !held {
		return SubjectNotLookedUp, "", nil
	}
	if o, ok := idx.objects[kind][s.Namespace+"/"+s.Name]; ok {
		t := o.created
		if !idx.complete[kind] {
			return SubjectLastObserved, o.uid, &t
		}
		return SubjectFound, o.uid, &t
	}
	if idx.complete[kind] {
		return SubjectNotFound, "", nil
	}
	return SubjectNotEstablished, "", nil
}

// subjectGVR is the served GVR string of a subject's kind.
func subjectGVR(s alertmanager.Subject) string {
	if s.Kind == "" {
		return ""
	}
	version := "v1"
	if s.Group == "autoscaling" {
		version = "v2"
	}
	if s.Group == "" {
		return version + "/" + s.Resource
	}
	return s.Group + "/" + version + "/" + s.Resource
}

func severityRank(sev string) int {
	switch sev {
	case string(alertmanager.SeverityCritical):
		return 0
	case string(alertmanager.SeverityWarning):
		return 1
	}
	return 2
}

// evalAlerts counts the read's alerts (S8a §6), samples the counted ones
// as Top refs - critical first, then the longest firing - and, for the
// lens, states each alert's subject. No read yet: counts of zero under a
// checking, unreadable or absent coverage, which the consumers read first.
func evalAlerts(cluster string, c observe.Capture) observe.PartResult {
	res := observe.PartResult{Alerts: &observe.AlertCounts{}}
	objs := c.Objects[SnapAlerts]
	if len(objs) != 1 {
		return res
	}
	env, ok := objs[0].(alertmanager.Read)
	if !ok {
		return res
	}
	counts := res.Alerts
	counts.Truncated = env.Truncated
	idx := indexSubjects(c)
	var counted []alertmanager.Item
	rows := AlertRows{Identity: env.Identity, Source: env.Source, Match: env.Match, Truncated: env.Truncated, Lineage: env.Lineage}
	for _, it := range env.Alerts {
		switch it.Display.StateClass {
		case alertmanager.StateActive:
			if it.Counted {
				counts.Counted++
				if it.Display.SeverityClass == alertmanager.SeverityCritical {
					counts.Critical++
				} else {
					counts.Warning++
				}
				counted = append(counted, it)
			} else {
				counts.Uncounted++
			}
		case alertmanager.StateSuppressed:
			counts.Suppressed++
		case alertmanager.StateUnprocessed:
			counts.Unprocessed++
		default:
			counts.Unknown++
		}
		if c.Rows {
			state, uid, created := idx.resolve(it.Subject)
			rows.Alerts = append(rows.Alerts, AlertRow{Key: it.Key, Alert: it.Display, Subject: it.Subject, SubjectGVR: subjectGVR(it.Subject), SubjectState: state, SubjectUID: uid,
				SubjectCreated: created, StartedBeforeCreated: created != nil && !it.Display.StartsAt.IsZero() && it.Display.StartsAt.Before(*created)})
		}
	}
	sort.SliceStable(counted, func(i, j int) bool {
		a, b := counted[i].Display, counted[j].Display
		if ra, rb := severityRank(string(a.SeverityClass)), severityRank(string(b.SeverityClass)); ra != rb {
			return ra < rb
		}
		return a.StartsAt.Before(b.StartsAt)
	})
	for i, it := range counted {
		if i == alertsTop {
			break
		}
		_, uid, _ := idx.resolve(it.Subject)
		ref := observe.Ref{Cluster: cluster, GVR: subjectGVR(it.Subject), Namespace: it.Display.Subject.Namespace, Name: it.Display.Subject.Name, UID: uid,
			Role: string(it.Display.SeverityClass), Label: it.Display.Name,
			Reason: it.Display.Summary, Fingerprint: it.Key}
		if !it.Display.StartsAt.IsZero() {
			t := it.Display.StartsAt
			ref.Since = &t
		}
		res.Top = append(res.Top, ref)
	}
	if c.Rows {
		res.Rows = rows
	}
	return res
}

// RowFacts are the read's own facts, beside its rows (the lens's source
// line): display values, and the read time and truncation.
func (r AlertRows) RowFacts() map[string]any {
	f := map[string]any{"alertsSource": r.Source, "alertsSourceKey": alertmanager.SourceKey(r.Identity), "alertsTruncated": r.Truncated}
	if len(r.Match) > 0 {
		f["alertsMatch"] = r.Match
	}
	return f
}
