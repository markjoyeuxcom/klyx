package line

import (
	"fmt"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/gitops/flux"
)

// Object is one row a station's facts come from, as LineService hands it
// over: the object's UID and, when it is failing, when its failure began -
// resolved by the service from a proven rule Since, else the observation
// log's proof (§3.3 "failing since"), with that proof's basis.
type Object[T any] struct {
	UID       string
	FailSince *time.Time
	FailBasis string
	Data      T
}

// StationInput is one station's rows, and each kind's read as the slice 3
// note §2.4 resolves it (ok, absent, checking, stale, error) keyed by kind
// name ("kustomizations", "gitrepositories", ...). A kind with no entry reads
// checking.
type StationInput struct {
	Station
	Kustomizations []Object[flux.Resource]
	HelmReleases   []Object[flux.Resource]
	Sources        []Object[flux.Source]
	Apps           []Object[argo.App]
	KindRead       map[string]string
}

// Kind reads beside the station read states.
const KindAbsent = "absent"

// sourceKinds maps a Flux source kind to its kind name.
var sourceKinds = map[string]string{
	"GitRepository": "gitrepositories", "HelmRepository": "helmrepositories", "OCIRepository": "ocirepositories",
	"Bucket": "buckets", "HelmChart": "helmcharts",
}

// readRank orders reads worst first; the worse of two wins.
var readRank = map[string]int{ReadError: 0, ReadStale: 1, ReadChecking: 2, ReadPartial: 3, ReadOK: 4, KindAbsent: 4}

func (in StationInput) kindRead(kind string) string {
	if r, ok := in.KindRead[kind]; ok {
		return r
	}
	return ReadChecking
}

func worseRead(a, b string) string {
	if readRank[b] < readRank[a] {
		return b
	}
	return a
}

// Key kinds: units compare only within one key, and Git and chart keys
// never mix (§3.2).
const (
	KeyGit   = "git"
	KeyChart = "chart"
)

// Ref kinds beside Flux's own (branch, tag, semver, commit, name).
const (
	RefUnqualified = "unqualified" // an Argo targetRevision that is not fully qualified
	RefDefault     = "default"     // a GitRepository with no spec.ref
)

// Key is a comparison key: (repo, ref kind, ref name) for Git units, (chart
// source, chart name) for chart units.
type Key struct {
	Kind    string
	Repo    string // the normalised repository URL, or the chart source
	RefKind string // Git only
	RefName string // the ref's name, or the chart's name
}

func (k Key) String() string {
	if k.Kind == KeyChart {
		return "chart " + k.Repo + " " + k.RefName
	}
	return k.Repo + " @ " + k.RefKind + " " + k.RefName
}

// Revision is one revision fact.
type Revision struct {
	SHA     string // a Git unit's commit
	Version string // a chart unit's version
	Digest  string // a Flux chart unit's configDigest: "values differ"
	Raw     string
}

func (r Revision) id() string {
	if r.SHA != "" {
		return r.SHA
	}
	return r.Version
}

func sameRev(a, b *Revision) bool { return a != nil && b != nil && a.id() == b.id() }

// Unit is one comparison unit and its three revision facts, never merged
// (§3.2): applied (deployment evidence; nil is "applied unknown", never
// "never applied"), attempted and compared.
type Unit struct {
	Origin          string // Kustomization | HelmRelease | Application
	Namespace, Name string
	SourceIndex     int // an Argo source's index in spec.sources
	UID             string
	Key             Key
	Applied         *Revision
	Attempted       *Revision
	AttemptPhase    string
	Compared        *Revision
	ArtifactTime    time.Time // the source artifact's lastUpdateTime: a disclosed tie-break only
	Failing         bool
	FailSince       *time.Time
	FailBasis       string
	Syncing         bool
	Suspended       bool
	SourceChanged   bool // Argo: the source was reordered or edited since its last deployment
	SourceName      string
	// Read is the worse of its own kind's read and its source kind's (§2.4
	// of the slice 3 note): a unit that is not read ok keeps its station
	// checking.
	Read string
}

// Exclusion is a unit Klyx does not compare, listed per station with its
// reason (§3.2).
type Exclusion struct {
	Origin, Namespace, Name string
	Reason                  string
	Digest                  string // an OCI or Bucket source's revision, shown unordered
}

// Exclusion reasons.
const (
	ExcludedOCI         = "oci source"
	ExcludedBucket      = "bucket source"
	ExcludedUnparsable  = "unparsable revision"
	ExcludedNoSource    = "source not read"
	ExcludedSourceKind  = "unsupported source kind"
	ExcludedValuesOnly  = "values-only source"
	ExcludedNoChartName = "chart name unknown"
)

// Units extracts a station's comparison units and exclusions (§3.2).
func Units(in StationInput) ([]Unit, []Exclusion) {
	var units []Unit
	var excl []Exclusion
	src := map[string]Object[flux.Source]{}
	for _, s := range in.Sources {
		src[string(s.Data.Kind)+"/"+s.Data.Namespace+"/"+s.Data.Name] = s
	}
	lookup := func(kind, ns, name string) (Object[flux.Source], bool) {
		s, ok := src[kind+"/"+ns+"/"+name]
		return s, ok
	}
	for _, o := range in.Kustomizations {
		u, x := kustomizationUnit(o, lookup)
		if u != nil {
			u.Read = worseRead(in.kindRead("kustomizations"), in.kindRead(sourceKinds[o.Data.SourceKind]))
		}
		units, excl = add(units, excl, u, x)
	}
	for _, o := range in.HelmReleases {
		u, x := helmReleaseUnit(o, lookup)
		if u != nil {
			u.Read = worseRead(in.kindRead("helmreleases"), in.kindRead(sourceKinds[o.Data.SourceKind]))
		}
		units, excl = add(units, excl, u, x)
	}
	for _, o := range in.Apps {
		us, xs := appUnits(o)
		for i := range us {
			us[i].Read = in.kindRead("applications")
		}
		units = append(units, us...)
		excl = append(excl, xs...)
	}
	return units, excl
}

func add(units []Unit, excl []Exclusion, u *Unit, x *Exclusion) ([]Unit, []Exclusion) {
	if u != nil {
		units = append(units, *u)
	}
	if x != nil {
		excl = append(excl, *x)
	}
	return units, excl
}

// fluxRef maps a GitRepository's spec.ref onto key kinds. A full ref name
// (refs/heads/x, refs/tags/x) is its branch or tag.
func fluxRef(kind, name string) (string, string) {
	switch {
	case kind == "":
		return RefDefault, ""
	case kind == "name" && strings.HasPrefix(name, "refs/heads/"):
		return "branch", strings.TrimPrefix(name, "refs/heads/")
	case kind == "name" && strings.HasPrefix(name, "refs/tags/"):
		return "tag", strings.TrimPrefix(name, "refs/tags/")
	case kind == "commit":
		return "commit", strings.ToLower(name)
	}
	return kind, name
}

// argoRef maps an Argo targetRevision: unqualified unless it is fully
// qualified or a sha - Argo can resolve a bare "main" to a tag, so it never
// shares a key with a Flux branch (§3.2).
func argoRef(target string) (string, string) {
	t := strings.TrimSpace(target)
	switch {
	case strings.HasPrefix(t, "refs/heads/"):
		return "branch", strings.TrimPrefix(t, "refs/heads/")
	case strings.HasPrefix(t, "refs/tags/"):
		return "tag", strings.TrimPrefix(t, "refs/tags/")
	case IsSHA(t):
		return "commit", strings.ToLower(t)
	case t == "":
		return RefUnqualified, "HEAD"
	}
	return RefUnqualified, t
}

func earliest(ts ...*time.Time) *time.Time {
	var out *time.Time
	for _, t := range ts {
		if t != nil && (out == nil || t.Before(*out)) {
			c := *t
			out = &c
		}
	}
	return out
}

func kustomizationUnit(o Object[flux.Resource], lookup func(kind, ns, name string) (Object[flux.Source], bool)) (*Unit, *Exclusion) {
	r := o.Data
	x := &Exclusion{Origin: "Kustomization", Namespace: r.Namespace, Name: r.Name}
	switch r.SourceKind {
	case "OCIRepository":
		x.Reason, x.Digest = ExcludedOCI, r.Revision
		return nil, x
	case "Bucket":
		x.Reason, x.Digest = ExcludedBucket, r.Revision
		return nil, x
	case "GitRepository":
	default:
		x.Reason = ExcludedSourceKind
		return nil, x
	}
	s, ok := lookup("GitRepository", r.SourceNamespace, r.SourceName)
	if !ok {
		x.Reason = ExcludedNoSource
		return nil, x
	}
	kind, name := fluxRef(s.Data.RefKind, s.Data.RefName)
	u := &Unit{Origin: "Kustomization", Namespace: r.Namespace, Name: r.Name, UID: o.UID, SourceName: s.Data.Name,
		Key: Key{Kind: KeyGit, Repo: NormalizeRepo(s.Data.URL), RefKind: kind, RefName: name}, Suspended: r.Suspended}
	if r.Revision != "" {
		rev, ok := ParseGitRevision(r.Revision)
		if !ok {
			x.Reason = ExcludedUnparsable
			return nil, x
		}
		u.Applied = &Revision{SHA: rev.SHA, Raw: r.Revision}
	}
	if r.LastAttemptedRevision != "" {
		if rev, ok := ParseGitRevision(r.LastAttemptedRevision); ok {
			u.Attempted = &Revision{SHA: rev.SHA, Raw: r.LastAttemptedRevision}
		}
	}
	if rev, ok := ParseGitRevision(s.Data.Revision); ok {
		u.Compared = &Revision{SHA: rev.SHA, Raw: s.Data.Revision}
	}
	u.ArtifactTime = s.Data.ArtifactUpdated
	unitFailing := r.Ready == flux.Failed
	sourceFailing := s.Data.Ready == flux.Failed && !s.Data.Suspended
	u.Failing = !r.Suspended && (unitFailing || sourceFailing)
	if u.Failing {
		var a, b *time.Time
		if unitFailing {
			a = o.FailSince
			u.FailBasis = o.FailBasis
		}
		if sourceFailing {
			b = s.FailSince
			if a == nil || (b != nil && b.Before(*a)) {
				u.FailBasis = s.FailBasis
			}
		}
		u.FailSince = earliest(a, b)
	}
	u.Syncing = !u.Failing && (r.Stale || (u.Attempted != nil && !sameRev(u.Attempted, u.Applied)))
	return u, nil
}

func helmReleaseUnit(o Object[flux.Resource], lookup func(kind, ns, name string) (Object[flux.Source], bool)) (*Unit, *Exclusion) {
	r := o.Data
	x := &Exclusion{Origin: "HelmRelease", Namespace: r.Namespace, Name: r.Name}
	s, ok := lookup(r.SourceKind, r.SourceNamespace, r.SourceName)
	if !ok {
		x.Reason = ExcludedNoSource
		return nil, x
	}
	chart := r.Chart
	for _, h := range r.History {
		if chart == "" {
			chart = h.ChartName
		}
	}
	if chart == "" {
		x.Reason = ExcludedNoChartName
		return nil, x
	}
	u := &Unit{Origin: "HelmRelease", Namespace: r.Namespace, Name: r.Name, UID: o.UID, SourceName: s.Data.Name,
		Key: Key{Kind: KeyChart, Repo: NormalizeRepo(s.Data.URL), RefName: chart}, Suspended: r.Suspended,
		ArtifactTime: s.Data.ArtifactUpdated}
	// Applied: the newest history entry whose status is deployed; attempted:
	// the latest entry when it is not deployed (§3.2).
	for _, h := range r.History {
		if h.Status == "deployed" {
			u.Applied = &Revision{Version: h.ChartVersion, Digest: h.ConfigDigest, Raw: h.ChartVersion}
			break
		}
	}
	if len(r.History) > 0 && r.History[0].Status != "deployed" {
		h := r.History[0]
		u.Attempted = &Revision{Version: h.ChartVersion, Digest: h.ConfigDigest, Raw: h.ChartVersion}
		u.AttemptPhase = h.Status
	}
	u.Failing = !r.Suspended && r.Ready == flux.Failed
	if u.Failing {
		u.FailSince, u.FailBasis = o.FailSince, o.FailBasis
	}
	u.Syncing = !u.Failing && (r.Stale || u.Attempted != nil)
	return u, nil
}

// sourceID is an Argo source's identity for pairing: where it deploys from,
// not its targetRevision.
func sourceID(s argo.Source) string {
	return NormalizeRepo(s.RepoURL) + "|" + s.Path + "|" + s.Chart
}

// pair finds a source's revision in a status block that records its own
// sources (§3.2: never paired with the current spec order). changed reports
// an index or targetRevision that differs from what was recorded.
func pair(rs argo.Revisions, s argo.Source, i int) (rev string, found, changed bool) {
	for j, rec := range rs.Sources {
		if sourceID(rec) != sourceID(s) || j >= len(rs.Revisions) {
			continue
		}
		return rs.Revisions[j], true, j != i || rec.TargetRevision != s.TargetRevision
	}
	return "", false, true
}

func argoRevision(chart bool, raw string) (*Revision, bool) {
	if raw == "" {
		return nil, true
	}
	if chart {
		return &Revision{Version: raw, Raw: raw}, true
	}
	rev, ok := ParseGitRevision(raw)
	if !ok {
		return nil, false
	}
	return &Revision{SHA: rev.SHA, Raw: raw}, true
}

var argoFailedPhases = map[string]bool{"Failed": true, "Error": true}

func appUnits(o Object[argo.App]) ([]Unit, []Exclusion) {
	a := o.Data
	var units []Unit
	var excl []Exclusion
	failing := argoFailedPhases[a.Operation.Phase]
	for _, c := range a.Conditions {
		if strings.HasSuffix(c.Type, "Error") {
			failing = true
		}
	}
	for i, s := range a.Sources {
		x := Exclusion{Origin: "Application", Namespace: a.Namespace, Name: fmt.Sprintf("%s source %d", a.Name, i+1)}
		if s.Ref != "" && s.Path == "" && s.Chart == "" {
			x.Reason = ExcludedValuesOnly
			excl = append(excl, x)
			continue
		}
		chart := s.Chart != ""
		u := Unit{Origin: "Application", Namespace: a.Namespace, Name: a.Name, SourceIndex: i, UID: o.UID, Failing: failing}
		if chart {
			u.Key = Key{Kind: KeyChart, Repo: NormalizeRepo(s.RepoURL), RefName: s.Chart}
		} else {
			kind, name := argoRef(s.TargetRevision)
			u.Key = Key{Kind: KeyGit, Repo: NormalizeRepo(s.RepoURL), RefKind: kind, RefName: name}
		}
		// Applied: the latest history entry, paired with the source(s)
		// recorded in that entry. No history is applied unknown.
		if len(a.History) > 0 {
			raw, found, changed := pair(a.History[0].Revisions, s, i)
			u.SourceChanged = changed
			if found {
				rev, ok := argoRevision(chart, raw)
				if !ok {
					x.Reason = ExcludedUnparsable
					excl = append(excl, x)
					continue
				}
				u.Applied = rev
			}
		}
		if raw, found, _ := pair(a.Compared, s, i); found {
			u.Compared, _ = argoRevision(chart, raw)
		}
		if a.Operation.Phase == "Running" || failing {
			if raw, found, _ := pair(a.Operation.Sync, s, i); found {
				u.Attempted, _ = argoRevision(chart, raw)
				u.AttemptPhase = a.Operation.Phase
			}
		}
		if u.Failing {
			u.FailSince, u.FailBasis = o.FailSince, o.FailBasis
		}
		u.Syncing = !u.Failing && (a.Operation.Phase == "Running" || (u.Attempted != nil && !sameRev(u.Attempted, u.Applied)))
		units = append(units, u)
	}
	return units, excl
}
