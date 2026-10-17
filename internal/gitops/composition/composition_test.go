package composition

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/gitops/resume"
	"github.com/moomora/klyx/internal/health"
)

const needle = "SIGNEDneedle8C"

var t0 = time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

func obj(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("%v: %s", err, doc)
	}
	var norm func(v any) any
	norm = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				x[k] = norm(e)
			}
		case []any:
			for i, e := range x {
				x[i] = norm(e)
			}
		case float64:
			return int64(x)
		}
		return v
	}
	norm(m)
	return &unstructured.Unstructured{Object: m}
}

func project(t *testing.T, kind string, u *unstructured.Unstructured) *Object {
	t.Helper()
	v, err := Project(kind)(u)
	if err != nil {
		t.Fatal(err)
	}
	return v.(*Object)
}

func generatorDoc(ann, ready string, gen int) string {
	return `{"apiVersion":"source.extensions.fluxcd.io/v1beta1","kind":"ArtifactGenerator",
		"metadata":{"name":"apps","namespace":"flux-system","uid":"u-gen","generation":` + itoa(gen) + ann + `},
		"spec":{"sources":[{"alias":"repo","kind":"GitRepository","name":"fleet"}],
			"artifacts":[{"name":"apps","revision":"@repo","copy":[{"from":"@repo/apps/**","to":"@artifact/"}]}]},
		"status":{"inventory":[{"name":"apps","namespace":"flux-system","digest":"sha256:a","filename":"a.tgz"}],"conditions":[` + ready + `]}}`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func ready(status, reason, msg, ltt string, gen int) string {
	return `{"type":"Ready","status":"` + status + `","reason":"` + reason + `","message":"` + msg + `","lastTransitionTime":"` + ltt + `","observedGeneration":` + itoa(gen) + `}`
}

func TestGeneratorProjectionAndRule(t *testing.T) {
	failing := ready("False", "SourceFetchFailed", "failed to fetch https://store.example/a.tgz?sig="+needle, "2026-10-17T09:00:00Z", 1)
	o := project(t, KindGenerator, obj(t, generatorDoc("", failing, 1)))
	b, _ := json.Marshal(o.Object)
	if strings.Contains(string(b), needle) {
		t.Fatalf("a signed URL crossed into the projection: %s", b)
	}
	g := ParseGenerator(o)
	if len(g.Sources) != 1 || g.Sources[0].Namespace != "flux-system" || len(g.Inventory) != 1 || g.Outputs[0].Copy[0] != "@repo/apps/** → @artifact/" {
		t.Fatalf("parse: %+v", g)
	}
	if e := GeneratorRule(g, resume.Current); e.Role != health.Fault || !strings.HasPrefix(e.Reason, "SourceFetchFailed") || strings.Contains(e.Reason, needle) {
		t.Fatalf("failing: %+v", e)
	}
	// the fingerprint is taken from the raw Ready, before Origins
	o2 := project(t, KindGenerator, obj(t, generatorDoc("", ready("False", "SourceFetchFailed", "failed to fetch https://store.example/a.tgz?sig=other", "2026-10-17T09:00:00Z", 1), 1)))
	if f1, _ := o.Fingerprint(); func() bool { f2, _ := o2.Fingerprint(); return f1 == f2 }() {
		t.Fatal("two raw messages that differ only past the origin share a fingerprint")
	}
	// suspension is case-insensitive, and a suspended generator is paused
	susp := ParseGenerator(project(t, KindGenerator, obj(t, generatorDoc(`,"annotations":{"source.extensions.fluxcd.io/reconcile":"Disabled"}`, failing, 1))))
	if !susp.Disabled || GeneratorRule(susp, resume.Current).Role != health.Paused {
		t.Fatalf("suspended: %+v", susp)
	}
	// ReconciliationDisabled is never a verdict, and never evidence
	init := ParseGenerator(project(t, KindGenerator, obj(t, generatorDoc("", ready("True", ReasonDisabled, "Initialized with reconciliation suspended", "2026-10-17T09:00:00Z", 1), 1))))
	if e := GeneratorRule(init, resume.Current); e.Label != LabelUnproven || e.Included {
		t.Fatalf("initialized while disabled: %+v", e)
	}
	if s := init.Sample(o); !s.NotEvidence {
		t.Fatal("ReconciliationDisabled is never evidence")
	}
	// a spec Ready has not answered for is Syncing
	pending := ParseGenerator(project(t, KindGenerator, obj(t, generatorDoc("", ready("True", "Succeeded", "ok", "2026-10-17T09:00:00Z", 1), 2))))
	if GeneratorRule(pending, resume.Current).Label != LabelSyncing {
		t.Fatal("pending")
	}
	if e := GeneratorRule(pending, resume.Awaiting); e.Label != LabelAwaiting || e.Included {
		t.Fatalf("awaiting: %+v", e)
	}
}

// S1c §4: resume evidence is S1a's, through the shared tracker; the
// no-drift case stays awaiting.
func TestGeneratorResume(t *testing.T) {
	frozen := ready("False", "SourceFetchFailed", "fetch failed", "2026-10-16T09:00:00Z", 1)
	tr := resume.NewTracker()
	disabled := project(t, KindGenerator, obj(t, generatorDoc(`,"annotations":{"source.extensions.fluxcd.io/reconcile":"disabled"}`, frozen, 1)))
	tr.Observe(t0, ParseGenerator(disabled).Sample(disabled))
	resumed := project(t, KindGenerator, obj(t, generatorDoc("", frozen, 1)))
	if r := tr.Observe(t0.Add(time.Minute), ParseGenerator(resumed).Sample(resumed)); r != resume.Awaiting {
		t.Fatalf("no drift: %v", r)
	}
	ran := project(t, KindGenerator, obj(t, generatorDoc("", ready("True", "Succeeded", "reconciliation succeeded, generated 1 artifact(s)", "2026-10-17T10:01:00Z", 1), 1)))
	if r := tr.Observe(t0.Add(2*time.Minute), ParseGenerator(ran).Sample(ran)); r != resume.Current {
		t.Fatalf("evidence: %v", r)
	}
}

func artifactDoc(labels, spec, status string) string {
	return `{"apiVersion":"source.toolkit.fluxcd.io/v1","kind":"ExternalArtifact",
		"metadata":{"name":"apps","namespace":"flux-system","uid":"u-ea","generation":1` + labels + `},"spec":` + spec + `,"status":` + status + `}`
}

const goodArtifact = `{"artifact":{"revision":"latest@sha256:abc","digest":"sha256:abc","url":"https://store.example/t/` + needle + `/a.tgz?sig=` + needle + `",
	"path":"a.tgz","lastUpdateTime":"2026-10-17T09:00:00Z","metadata":{"org.opencontainers.image.revision":"main@sha1:4f2c9e1","note":"` + needle + `"}},
	"conditions":[{"type":"Ready","status":"True","reason":"Succeeded","message":"Artifact is ready","lastTransitionTime":"2026-10-17T09:00:00Z"}]}`

func TestArtifactProjectionAndRule(t *testing.T) {
	o := project(t, KindArtifact, obj(t, artifactDoc("", `{"sourceRef":{"apiVersion":"source.extensions.fluxcd.io/v1beta1","kind":"ArtifactGenerator","name":"apps"}}`, goodArtifact)))
	b, _ := json.Marshal(o.Object)
	if strings.Contains(string(b), needle) {
		t.Fatalf("the artifact's URL or metadata crossed: %s", b)
	}
	a := ParseArtifact(o)
	if !a.HasArtifact || a.Origin != "https://store.example <the rest of the message is withheld: it follows a URL>" || a.OriginRevision != "main@sha1:4f2c9e1" || a.SourceRef.Namespace != "flux-system" {
		t.Fatalf("parse: %+v", a)
	}
	if e := ArtifactRule(a); e.Role != health.OK {
		t.Fatalf("an artifact present is ok: %+v", e)
	}
	none := ParseArtifact(project(t, KindArtifact, obj(t, artifactDoc("", `{}`, `{"conditions":[]}`))))
	if e := ArtifactRule(none); e.Role != health.Fault || e.Label != LabelNoArtifact {
		t.Fatalf("no artifact: %+v", e)
	}
	third := ParseArtifact(project(t, KindArtifact, obj(t, artifactDoc("", `{}`,
		`{"conditions":[{"type":"Ready","status":"False","reason":"BuildFailed","message":"x","lastTransitionTime":"2026-10-17T09:00:00Z"}]}`))))
	if e := ArtifactRule(third); e.Role != health.Fault {
		t.Fatalf("a third-party producer's Ready False: %+v", e)
	}
}

// S1c §2: who the producer is, from the label (an identity) and sourceRef
// (a name, of any kind).
func TestProducerOf(t *testing.T) {
	gen := func(ns, name, uid string) Generator {
		return Generator{Common: Common{Namespace: ns, Name: name, UID: uid}}
	}
	genRef := &Ref{APIVersion: "source.extensions.fluxcd.io/v1beta1", Kind: KindGenerator, Namespace: "flux-system", Name: "apps"}
	gens := []Generator{gen("flux-system", "apps", "u-1"), gen("flux-system", "other", "u-2")}
	cases := []struct {
		name  string
		a     Artifact
		gens  []Generator
		cov   Coverage
		state string
	}{
		{"both agree", Artifact{SourceRef: genRef, GeneratorUID: "u-1"}, gens, CoverageComplete, ProducerVerified},
		{"recreated under the same name, the recorded one gone", Artifact{SourceRef: genRef, GeneratorUID: "u-old"}, gens, CoverageComplete, ProducerDisagree},
		{"the recorded producer live under another name", Artifact{SourceRef: genRef, GeneratorUID: "u-2"}, gens, CoverageComplete, ProducerDisagree},
		{"the label alone, found", Artifact{GeneratorUID: "u-2"}, gens, CoverageComplete, ProducerByIdentity},
		{"the label alone, gone", Artifact{GeneratorUID: "u-x"}, gens, CoverageComplete, ProducerGone},
		{"the label alone, capped", Artifact{GeneratorUID: "u-x"}, gens, CoveragePartial, ProducerIncomplete},
		{"a generator sourceRef alone", Artifact{SourceRef: genRef}, gens, CoverageComplete, ProducerNamed},
		{"a sourceRef without a namespace resolves in the artifact's", Artifact{SourceRef: &Ref{Kind: KindGenerator, Namespace: "flux-system", Name: "other"}}, gens, CoverageComplete, ProducerNamed},
		{"a third-party kind is never a same-named generator", Artifact{SourceRef: &Ref{APIVersion: "example.com/v1", Kind: "ArtifactGenerator", Namespace: "flux-system", Name: "apps"}}, gens, CoverageComplete, ProducerDeclared},
		{"another kind", Artifact{SourceRef: &Ref{Kind: "Producer", Namespace: "flux-system", Name: "apps"}}, gens, CoverageComplete, ProducerDeclared},
		{"unreadable generators", Artifact{SourceRef: genRef, GeneratorUID: "u-1"}, nil, CoverageUnreadable, ProducerUnreadable},
		{"none", Artifact{}, gens, CoverageComplete, ProducerNone},
	}
	for _, c := range cases {
		if p := ProducerOf(c.a, c.gens, c.cov); p.State != c.state {
			t.Errorf("%s: %+v", c.name, p)
		}
	}
	p := ProducerOf(Artifact{SourceRef: genRef, GeneratorUID: "u-old"}, gens, CoverageComplete)
	if !p.RecordedGone || p.Generator != nil {
		t.Fatalf("a disagreeing producer attributes no health: %+v", p)
	}
	p = ProducerOf(Artifact{SourceRef: genRef, GeneratorUID: "u-2"}, gens, CoverageComplete)
	if p.Recorded == nil || p.Recorded.Name != "other" || p.RecordedGone || p.Generator != nil {
		t.Fatalf("the recorded producer is named, with no health: %+v", p)
	}
}
