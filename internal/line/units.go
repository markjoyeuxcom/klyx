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

// Key kinds: units compare only within one key, and kinds never mix
// (§3.2).
const (
	KeyGit   = "git"
	KeyChart = "chart" // an Argo chart source: (repository, chart), recorded per deployment
	// KeyChartName is a Flux HelmRelease's chart by its RECORDED name only.
	// Flux's history records the chart name and version a Helm action
	// deployed, never the repository or path it came from, so the key
	// claims no repository (Repo is empty) and a match means the same
	// recorded name and version - not the same artifact, repository or
	// desired deployment. Same-name charts share a key: a disclosed limit.
	// Selector-only, like every chart key.
	KeyChartName = "chart-name"
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
	switch k.Kind {
	case KeyChart:
		return "chart " + k.Repo + " " + k.RefName
	case KeyChartName:
		return "Flux chart " + k.RefName + " - by recorded name; repository not recorded"
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
	// ConfiguredChartDiffers: a Flux release's configured chart name (a
	// HelmRepository source) differs from its recorded deployed chart -
	// current context, never a provenance claim.
	ConfiguredChartDiffers bool
	// Read is the read of what the unit's compared facts come from (§2.4 of
	// the slice 3 note): for a Git unit, the worse of its own kind's and its
	// source kind's; for a recorded-name chart unit, its own kind's alone (its
	// history). A unit that is not read ok keeps its station checking.
	Read string
	// ContextRead is a recorded-name chart unit's current source kind read:
	// context only, disclosed separately, never gating the comparison.
	ContextRead string
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
	ExcludedNoIdentity  = "source identity unknown" // no repository URL to compare on
	ExcludedNoChartID   = "chart identity unknown"  // no history record names a chart
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
			u.Read = in.kindRead("helmreleases")
			u.ContextRead = in.kindRead(sourceKinds[o.Data.SourceKind])
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

// OverrideKey is the Git key a fleet.yaml line.source names, normalised as
// a Flux ref is.
func OverrideKey(url, refKind, refName string) Key {
	kind, name := fluxRef(refKind, refName)
	return Key{Kind: KeyGit, Repo: NormalizeRepo(url), RefKind: kind, RefName: name}
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
	repoID := NormalizeRepo(s.Data.URL)
	if repoID == "" {
		x.Reason = ExcludedNoIdentity
		return nil, x
	}
	u := &Unit{Origin: "Kustomization", Namespace: r.Namespace, Name: r.Name, UID: o.UID, SourceName: s.Data.Name,
		Key: Key{Kind: KeyGit, Repo: repoID, RefKind: kind, RefName: name}, Suspended: r.Suspended}
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

// helmReleaseUnit reads a Flux HelmRelease as a recorded-name chart unit
// (KeyChartName): every fact compared is one Flux's history records - the
// chart name and version a Helm action deployed. Nothing is attached to the
// current spec or source: Flux records neither the repository nor the path a
// release came from, and its history changes only with Helm actions, so a
// spec edit, a source edit or recreation, or a suspension leaves the recorded
// chart under its old name-key until history itself changes.
func helmReleaseUnit(o Object[flux.Resource], lookup func(kind, ns, name string) (Object[flux.Source], bool)) (*Unit, *Exclusion) {
	r := o.Data
	x := &Exclusion{Origin: "HelmRelease", Namespace: r.Namespace, Name: r.Name}
	// The newest deployed record sets both the key's chart name and the
	// applied version. With no deployed record, a newest record naming a
	// chart gives the key with applied unknown and a historical attempt.
	// No fallback to the spec, or to an older and convenient deployment.
	var deployed *flux.HelmHistory
	for i := range r.History {
		if r.History[i].Status == "deployed" {
			deployed = &r.History[i]
			break
		}
	}
	name := ""
	switch {
	case deployed != nil:
		name = deployed.ChartName
	case len(r.History) > 0:
		name = r.History[0].ChartName
	}
	if name == "" {
		x.Reason = ExcludedNoChartID
		return nil, x
	}
	u := &Unit{Origin: "HelmRelease", Namespace: r.Namespace, Name: r.Name, UID: o.UID,
		Key: Key{Kind: KeyChartName, RefName: name}, Suspended: r.Suspended}
	if deployed != nil && deployed.ChartVersion != "" {
		u.Applied = &Revision{Version: deployed.ChartVersion, Digest: deployed.ConfigDigest, Raw: deployed.ChartVersion}
	}
	// A historical attempt: the newest record when it is not deployed and
	// names this key's chart. A failed attempt of another chart stays raw
	// history, never an attempt for this key.
	if h := r.History; len(h) > 0 && h[0].Status != "deployed" && h[0].ChartName == name && h[0].ChartVersion != "" {
		u.Attempted = &Revision{Version: h[0].ChartVersion, Digest: h[0].ConfigDigest, Raw: h[0].ChartVersion}
		u.AttemptPhase = h[0].Status
	}
	// Current context, optional: the configured chart name (a HelmRepository
	// source names it) and the release's and its bound source's failure.
	if r.SourceKind == "HelmRepository" && r.Chart != "" && r.Chart != name {
		u.ConfiguredChartDiffers = true
	}
	var a, b *time.Time
	unitFailing := r.Ready == flux.Failed
	sourceFailing := false
	if s, ok := lookup(r.SourceKind, r.SourceNamespace, r.SourceName); ok {
		u.SourceName = s.Data.Name
		sourceFailing = s.Data.Ready == flux.Failed && !s.Data.Suspended
		if sourceFailing {
			b = s.FailSince
		}
	}
	u.Failing = !r.Suspended && (unitFailing || sourceFailing)
	if u.Failing {
		if unitFailing {
			a, u.FailBasis = o.FailSince, o.FailBasis
		}
		if sourceFailing && (a == nil || (b != nil && b.Before(*a))) {
			if s, ok := lookup(r.SourceKind, r.SourceNamespace, r.SourceName); ok {
				u.FailBasis = s.FailBasis
			}
		}
		u.FailSince = earliest(a, b)
	}
	// Syncing is current evidence only - an unobserved spec - never a
	// historical attempt, and never for a suspended release.
	u.Syncing = !u.Failing && !r.Suspended && r.Stale
	return u, nil
}

// sourceID is an Argo source's identity for pairing: where it deploys from,
// not its targetRevision.
func sourceID(s argo.Source) string {
	return NormalizeRepo(s.RepoURL) + "|" + s.Path + "|" + s.Chart
}

// pairing is one source's revision in a status block.
type pairing struct {
	rev            string
	found, changed bool
}

// pairAll pairs every source with the revisions a status block records for
// its own sources (§3.2: never with the current spec order), one to one.
// An exact match (same repository, path, chart, ref and targetRevision) is
// preferred; otherwise a unique identity match, flagged as changed. An
// ambiguous match is refused - applied unknown, never a guess. changed also
// flags a reorder.
func pairAll(rs argo.Revisions, srcs []argo.Source) []pairing {
	out := make([]pairing, len(srcs))
	used := make([]bool, len(rs.Sources))
	exact := func(a, b argo.Source) bool {
		return sourceID(a) == sourceID(b) && a.TargetRevision == b.TargetRevision && a.Ref == b.Ref
	}
	assign := func(i, j int) {
		used[j] = true
		out[i] = pairing{rev: rs.Revisions[j], found: true, changed: j != i || !exact(rs.Sources[j], srcs[i])}
	}
	matches := func(pass int, rec, s argo.Source) bool {
		if pass == 0 {
			return exact(rec, s)
		}
		return sourceID(rec) == sourceID(s)
	}
	for pass := 0; pass < 2; pass++ {
		// Count each unused record's claimants among the unpaired sources:
		// a record two sources could both be paired with is contested, and
		// left to neither (one to one on both sides).
		claimants := make([]int, len(rs.Sources))
		for i, s := range srcs {
			if out[i].found {
				continue
			}
			for j, rec := range rs.Sources {
				if !used[j] && j < len(rs.Revisions) && matches(pass, rec, s) {
					claimants[j]++
				}
			}
		}
		for i, s := range srcs {
			if out[i].found {
				continue
			}
			match, n := -1, 0
			for j, rec := range rs.Sources {
				if !used[j] && j < len(rs.Revisions) && matches(pass, rec, s) {
					match = j
					n++
				}
			}
			if n == 1 && claimants[match] == 1 {
				assign(i, match)
			}
		}
	}
	for i := range out {
		if !out[i].found {
			out[i].changed = true
		}
	}
	return out
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
	// Suspended is an intended pause: never a failure (parent §3.3; the
	// native Argo rule's paused state).
	suspended := a.HealthStatus == "Suspended"
	opFailed := argoFailedPhases[a.Operation.Phase]
	failing := opFailed
	for _, c := range a.Conditions {
		if strings.HasSuffix(c.Type, "Error") {
			failing = true
		}
	}
	failing = failing && !suspended
	var applied, compared, attempted []pairing
	if len(a.History) > 0 {
		applied = pairAll(a.History[0].Revisions, a.Sources)
	}
	compared = pairAll(a.Compared, a.Sources)
	// Attempted evidence is the operation's own: a running or failed
	// operation, never a succeeded one because another condition fails.
	if a.Operation.Phase == "Running" || opFailed {
		attempted = pairAll(a.Operation.Sync, a.Sources)
	}
	for i, s := range a.Sources {
		x := Exclusion{Origin: "Application", Namespace: a.Namespace, Name: fmt.Sprintf("%s source %d", a.Name, i+1)}
		if s.Ref != "" && s.Path == "" && s.Chart == "" {
			x.Reason = ExcludedValuesOnly
			excl = append(excl, x)
			continue
		}
		chart := s.Chart != ""
		repoID := NormalizeRepo(s.RepoURL)
		if repoID == "" {
			x.Reason = ExcludedNoIdentity
			excl = append(excl, x)
			continue
		}
		u := Unit{Origin: "Application", Namespace: a.Namespace, Name: a.Name, SourceIndex: i, UID: o.UID, Failing: failing, Suspended: suspended}
		if chart {
			u.Key = Key{Kind: KeyChart, Repo: repoID, RefName: s.Chart}
		} else {
			kind, name := argoRef(s.TargetRevision)
			u.Key = Key{Kind: KeyGit, Repo: repoID, RefKind: kind, RefName: name}
		}
		// Applied: the latest history entry, paired with the source(s)
		// recorded in that entry. No history is applied unknown.
		if applied != nil {
			p := applied[i]
			u.SourceChanged = p.changed
			if p.found {
				rev, ok := argoRevision(chart, p.rev)
				if !ok {
					x.Reason = ExcludedUnparsable
					excl = append(excl, x)
					continue
				}
				u.Applied = rev
			}
		}
		if p := compared[i]; p.found {
			u.Compared, _ = argoRevision(chart, p.rev)
		}
		if attempted != nil {
			if p := attempted[i]; p.found {
				u.Attempted, _ = argoRevision(chart, p.rev)
				u.AttemptPhase = a.Operation.Phase
			}
		}
		if u.Failing {
			u.FailSince, u.FailBasis = o.FailSince, o.FailBasis
		}
		u.Syncing = !u.Failing && !suspended && (a.Operation.Phase == "Running" || (u.Attempted != nil && !sameRev(u.Attempted, u.Applied)))
		units = append(units, u)
	}
	return units, excl
}
