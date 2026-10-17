package composition

import (
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/gitops/resume"
	"github.com/moomora/klyx/internal/health"
)

// Readiness is the Ready condition as the rules read it.
type Readiness string

const (
	Ready       Readiness = "Ready"
	Failed      Readiness = "Failed"
	Reconciling Readiness = "Reconciling"
	Unknown     Readiness = "Unknown"
	NoReady     Readiness = ""
)

// Common is what both kinds carry.
type Common struct {
	Kind, Namespace, Name, UID string
	Generation                 int64
	Created                    time.Time
	Ready                      Readiness
	Reason, Message            string
	ReadySince                 time.Time
	ReadyGeneration            int64
	Stalled                    bool
	Clipped, Incomplete        []string
}

func common(o *Object) Common {
	u := o.Unstructured
	c := Common{Kind: u.GetKind(), Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID()),
		Generation: u.GetGeneration(), Created: u.GetCreationTimestamp().Time, Ready: NoReady, Clipped: o.Clipped, Incomplete: o.Incomplete}
	reconciling := false
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, x := range conds {
		cm, _ := x.(map[string]any)
		st, _ := cm["status"].(string)
		switch cm["type"] {
		case "Ready":
			switch st {
			case "True":
				c.Ready = Ready
			case "False":
				c.Ready = Failed
			default:
				c.Ready = Unknown
			}
			c.Reason, _ = cm["reason"].(string)
			c.Message, _ = cm["message"].(string)
			if t, ok := cm["lastTransitionTime"].(string); ok {
				c.ReadySince, _ = time.Parse(time.RFC3339, t)
			}
			c.ReadyGeneration, _ = cm["observedGeneration"].(int64)
		case "Reconciling":
			reconciling = st == "True"
		case "Stalled":
			c.Stalled = st == "True"
		}
	}
	if reconciling && c.Ready != Failed {
		c.Ready = Reconciling
	}
	return c
}

func (c Common) incomplete(path string) bool {
	for _, p := range c.Incomplete {
		if p == path {
			return true
		}
	}
	return false
}

// Ref names an object.
type Ref struct {
	APIVersion, Kind, Namespace, Name string
}

// GeneratorSource is one declared source of a generator.
type GeneratorSource struct {
	Alias string
	Ref
}

// Output is one artifact a generator declares.
type Output struct {
	Name, Revision string
	Copy           []string
}

// Generator is an ArtifactGenerator.
type Generator struct {
	Common
	// Disabled: suspended by its own annotation (case-insensitive).
	Disabled                 bool
	RequestedAt, LastHandled string
	Sources                  []GeneratorSource
	SourcesIncomplete        bool
	Outputs                  []Output
	// Inventory are the ExternalArtifacts it generated.
	Inventory           []Ref
	InventoryIncomplete bool
}

// ParseGenerator reads a projected ArtifactGenerator.
func ParseGenerator(o *Object) Generator {
	u := o.Unstructured
	g := Generator{Common: common(o)}
	ann := u.GetAnnotations()
	g.Disabled = strings.ToLower(ann[AnnReconcile]) == "disabled"
	g.RequestedAt = ann[AnnRequestedAt]
	g.LastHandled, _, _ = unstructured.NestedString(u.Object, "status", "lastHandledReconcileAt")
	srcs, _, _ := unstructured.NestedSlice(u.Object, "spec", "sources")
	for _, x := range srcs {
		m, _ := x.(map[string]any)
		s := GeneratorSource{Alias: str(m, "alias"), Ref: Ref{Kind: str(m, "kind"), Name: str(m, "name"), Namespace: str(m, "namespace")}}
		if s.Namespace == "" {
			s.Namespace = g.Namespace
		}
		g.Sources = append(g.Sources, s)
	}
	g.SourcesIncomplete = g.incomplete("spec.sources")
	arts, _, _ := unstructured.NestedSlice(u.Object, "spec", "artifacts")
	for _, x := range arts {
		m, _ := x.(map[string]any)
		out := Output{Name: str(m, "name"), Revision: str(m, "revision")}
		cp, _ := m["copy"].([]any)
		for _, c := range cp {
			if s, ok := c.(string); ok {
				out.Copy = append(out.Copy, s)
			}
		}
		g.Outputs = append(g.Outputs, out)
	}
	inv, _, _ := unstructured.NestedSlice(u.Object, "status", "inventory")
	for _, x := range inv {
		m, _ := x.(map[string]any)
		g.Inventory = append(g.Inventory, Ref{Kind: KindArtifact, Name: str(m, "name"), Namespace: str(m, "namespace")})
	}
	g.InventoryIncomplete = g.incomplete("status.inventory")
	return g
}

// Sample is the generator's capture for the shared resume tracker (S1c §4:
// S1a §4's evidence, unchanged). ReconciliationDisabled is never evidence;
// a generator has no schedule.
func (g Generator) Sample(o *Object) resume.Sample {
	fp, has := o.Fingerprint()
	return resume.Sample{Kind: KindGenerator, UID: g.UID, Disabled: g.Disabled, Fingerprint: fp, HasReady: has,
		NotEvidence: g.Reason == ReasonDisabled,
		InProgress:  g.Ready == Reconciling || (g.Ready == Unknown && g.Reason == ReasonProgressing)}
}

// Artifact is an ExternalArtifact.
type Artifact struct {
	Common
	HasArtifact                              bool
	Revision, Digest, Origin, OriginRevision string
	LastUpdate                               time.Time
	Size                                     int64
	// SourceRef is spec.sourceRef as declared (its namespace defaulted to
	// the artifact's); GeneratorUID the generator label: two pieces of
	// producer evidence (S1c §2, revision 1).
	SourceRef    *Ref
	GeneratorUID string
	ManagedBy    string
}

// ParseArtifact reads a projected ExternalArtifact.
func ParseArtifact(o *Object) Artifact {
	u := o.Unstructured
	a := Artifact{Common: common(o)}
	if ref, ok, _ := unstructured.NestedMap(u.Object, "spec", "sourceRef"); ok {
		r := Ref{APIVersion: str(ref, "apiVersion"), Kind: str(ref, "kind"), Name: str(ref, "name"), Namespace: str(ref, "namespace")}
		if r.Namespace == "" {
			r.Namespace = a.Namespace
		}
		a.SourceRef = &r
	}
	a.GeneratorUID = u.GetLabels()[LabelGenerator]
	a.ManagedBy = u.GetLabels()[LabelManagedBy]
	if art, ok, _ := unstructured.NestedMap(u.Object, "status", "artifact"); ok {
		a.HasArtifact = true
		a.Revision, a.Digest, a.Origin, a.OriginRevision = str(art, "revision"), str(art, "digest"), str(art, "origin"), str(art, "originRevision")
		a.LastUpdate, _ = time.Parse(time.RFC3339, str(art, "lastUpdateTime"))
		a.Size, _ = art["size"].(int64)
	}
	return a
}

// Labels the rules use.
const (
	LabelSuspended  = "Suspended"
	LabelAwaiting   = "Resumed · awaiting evaluation"
	LabelUnproven   = "Awaiting evaluation"
	LabelEvaluating = "Resumed · evaluating"
	LabelSyncing    = "Syncing…"
	LabelNoArtifact = "No artifact"
)

// AwaitingNoDrift is why a resumed generator may stay awaiting: a run that
// finds nothing changed writes no condition (S1c §4).
const AwaitingNoDrift = "nothing has changed since suspension, and the generator records nothing when nothing drifts"

// GeneratorRule is an ArtifactGenerator's verdict (S1c §4): suspension
// first; a resumed generator without evidence is not counted;
// ReconciliationDisabled is never a verdict; a spec Ready has not answered
// for is Syncing (keyed on Ready's own generation: there is no top-level
// observedGeneration); otherwise Ready decides.
func GeneratorRule(g Generator, r resume.Resume) health.NativeEval {
	switch {
	case g.Disabled:
		return health.NativeEval{Role: health.Paused, Label: LabelSuspended}
	case r == resume.Awaiting:
		return health.NativeEval{Role: health.Unknown, Label: LabelAwaiting, Reason: health.CapReason("", AwaitingNoDrift)}
	case r == resume.Unproven, g.Reason == ReasonDisabled:
		return health.NativeEval{Role: health.Unknown, Label: LabelUnproven}
	case r == resume.Evaluating:
		return health.NativeEval{Included: true, Role: health.Progressing, Label: LabelEvaluating}
	case g.ReadyGeneration > 0 && g.Generation > g.ReadyGeneration:
		return health.NativeEval{Included: true, Role: health.Progressing, Label: LabelSyncing}
	}
	return readyRule(g.Common)
}

func readyRule(c Common) health.NativeEval {
	e := health.NativeEval{Included: c.Ready != Ready}
	switch c.Ready {
	case Ready:
		e.Role, e.Label = health.OK, "Ready"
		e.Cond = health.CondAt("Ready", "True", c.ReadySince)
	case Failed:
		e.Role, e.Label = health.Fault, "Failed"
		if c.Stalled {
			e.Label = "Stalled"
		}
		e.Cond = health.CondAt("Ready", "False", c.ReadySince)
		e.Reason = health.CapReason(c.Reason, c.Message)
	case Reconciling:
		e.Role, e.Label = health.Progressing, "Reconciling"
	case Unknown:
		if c.Reason == ReasonProgressing || c.Reason == "ProgressingWithRetry" {
			e.Role, e.Label = health.Progressing, "Progressing"
		} else {
			e.Role, e.Label = health.Unknown, "Unknown"
		}
	default:
		e.Role, e.Label = health.Unknown, "Not reported"
	}
	return e
}

// ArtifactRule is an ExternalArtifact's verdict (S1c §4): from the artifact
// and Ready, never from Ready alone - source-watcher writes only
// Ready=True, so a failing generator leaves its artifacts Ready. Ready
// False (a third-party producer that writes it) is a fault; no artifact is
// a fault; an artifact present is ok. It has no suspension and no
// generation lag.
func ArtifactRule(a Artifact) health.NativeEval {
	switch {
	case a.Ready == Failed:
		return readyRule(a.Common)
	case !a.HasArtifact:
		return health.NativeEval{Included: true, Role: health.Fault, Label: LabelNoArtifact,
			Reason: health.CapReason("", "no artifact yet: its consumers cannot apply")}
	}
	e := health.NativeEval{Role: health.OK, Label: "Ready"}
	if a.Ready == Ready {
		e.Cond = health.CondAt("Ready", "True", a.ReadySince)
	}
	return e
}
