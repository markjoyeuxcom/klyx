package fluxop

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/health"
)

const needle = "FLUXOPneedle1A"

func obj(t *testing.T, doc string) *unstructured.Unstructured {
	t.Helper()
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON([]byte(doc)); err != nil {
		t.Fatal(err)
	}
	return u
}

func project(t *testing.T, kind string, u *unstructured.Unstructured) *Object {
	t.Helper()
	v, err := Project(kind)(u)
	if err != nil {
		t.Fatal(err)
	}
	return v.(*Object)
}

func ready(status, reason, msg, at string, gen int) string {
	return `{"type":"Ready","status":"` + status + `","reason":"` + reason + `","message":"` + msg + `","lastTransitionTime":"` + at + `","observedGeneration":` + itoa(gen) + `}`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func provider(ann, conds, extra string) string {
	return `{"apiVersion":"fluxcd.controlplane.io/v1","kind":"ResourceSetInputProvider","metadata":{"name":"p","namespace":"team","uid":"u1","generation":1` + ann + `},
		"spec":{"type":"GitHubBranch","url":"https://bob:` + needle + `@github.com/org/repo","defaultValues":{"secret":"` + needle + `"},"schedule":[{"cron":"0 * * * *","window":"5m"}]},
		"status":{"conditions":[` + conds + `]` + extra + `,"exportedInputs":[{"id":"` + needle + `","branch":"` + needle + `"}]}}`
}

// S1a §3 and §6: nothing a payload surface holds is projected (exported
// input values, the id included, default values, templates, inline inputs,
// patches), URLs pass the URL rule, and the fingerprint never serialises.
func TestProjectionCarriesNoPayload(t *testing.T) {
	docs := map[string]string{
		KindProvider: provider("", ready("True", "ReconciliationSucceeded", "ok", "2026-10-17T10:00:00Z", 1), `,"lastExportedRevision":"sha256:abc"`),
		KindSet: `{"apiVersion":"fluxcd.controlplane.io/v1","kind":"ResourceSet","metadata":{"name":"rs","namespace":"team","uid":"u2","generation":3},
			"spec":{"inputs":[{"token":"` + needle + `"}],"resources":[{"apiVersion":"v1","kind":"Secret","stringData":{"k":"` + needle + `"}}],"resourcesTemplate":"` + needle + `",
			"steps":[{"name":"crds","resourcesTemplate":"` + needle + `"},{"name":"apps"}],
			"dependsOn":[{"apiVersion":"v1","kind":"ConfigMap","name":"cfg","ready":true,"readyExpr":"` + needle + `"}],
			"inputsFrom":[{"kind":"ResourceSetInputProvider","selector":{"matchLabels":{"team":"a"}}}]},
			"status":{"conditions":[` + ready("True", "ReconciliationSucceeded", "ok", "2026-10-17T10:00:00Z", 3) + `],"inventory":{"entries":[{"id":"x","v":"v1"}]}}}`,
		KindInstance: `{"apiVersion":"fluxcd.controlplane.io/v1","kind":"FluxInstance","metadata":{"name":"flux","namespace":"flux-system","uid":"u3","generation":2},
			"spec":{"distribution":{"version":"2.x","registry":"ghcr.io/fluxcd"},"components":["source-controller","kustomize-controller"],
			"kustomize":{"patches":[{"patch":"` + needle + `"}]},"sync":{"kind":"GitRepository","url":"https://x-access-token:` + needle + `@github.com/org/fleet","ref":"refs/heads/main","path":"clusters/prd"}},
			"status":{"conditions":[` + ready("True", "ReconciliationSucceeded", "ok", "2026-10-17T10:00:00Z", 2) + `],"components":[{"name":"source-controller","repository":"ghcr.io/fluxcd/source-controller","tag":"v1.9.6"}]}}`,
	}
	for kind, doc := range docs {
		o := project(t, kind, obj(t, doc))
		b, err := json.Marshal(o)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), needle) {
			t.Errorf("%s: a payload or credential crossed: %s", kind, b)
		}
		raw, _ := json.Marshal(o.Unstructured)
		if string(b) != string(raw) {
			t.Errorf("%s: the projection serialises more than its object", kind)
		}
		if fp, ok := o.Fingerprint(); !ok || fp == ([32]byte{}) {
			t.Errorf("%s: no fingerprint", kind)
		}
	}
	p := ParseProvider(project(t, KindProvider, obj(t, docs[KindProvider])))
	if p.ExportedInputCount != 1 || p.URL != "https://github.com/org/repo" || !p.Scheduled() || p.LastExportedRevision != "sha256:abc" {
		t.Errorf("provider: %+v", p)
	}
	s := ParseSet(project(t, KindSet, obj(t, docs[KindSet])))
	if s.InputCount != 1 || s.ResourceCount != 1 || !s.HasTemplate || s.StepCount != 2 || len(s.Steps) != 2 || s.InventoryCount != 1 ||
		len(s.DependsOn) != 1 || !s.DependsOn[0].HasReadyExpr || s.InputsFrom[0].Selector != "team=a" {
		t.Errorf("set: %+v", s)
	}
	in := ParseInstance(project(t, KindInstance, obj(t, docs[KindInstance])))
	if in.PatchCount != 1 || in.Sync == nil || in.Sync.URL != "https://github.com/org/fleet" || in.SyncName() != "flux-system" || len(in.Running) != 1 {
		t.Errorf("instance: %+v", in)
	}
}

// S1a §3: a projection over 8 KiB is rebuilt with every display string at
// 256 bytes and its lists shortened, each clip recorded; the classification
// facts and the fingerprint do not change with the clip.
func TestProjectionClipsDisplayNeverFacts(t *testing.T) {
	long := strings.Repeat("m", 3000)
	deps := ""
	for i := 0; i < 60; i++ {
		if i > 0 {
			deps += ","
		}
		deps += `{"apiVersion":"v1","kind":"ConfigMap","name":"` + strings.Repeat("d", 200) + itoa(i) + `"}`
	}
	doc := `{"apiVersion":"fluxcd.controlplane.io/v1","kind":"ResourceSet","metadata":{"name":"rs","namespace":"team","uid":"u2","generation":3},
		"spec":{"dependsOn":[` + deps + `]},"status":{"conditions":[` + ready("False", "DependencyNotReady", long, "2026-10-17T10:00:00Z", 3) + `]}}`
	o := project(t, KindSet, obj(t, doc))
	if o.Size > MaxObject || len(o.Clipped) == 0 {
		t.Fatalf("size %d, clipped %v", o.Size, o.Clipped)
	}
	c := ParseSet(o).Common
	if c.Ready != Failed || c.Reason != "DependencyNotReady" || c.ReadyGeneration != 3 || len(c.Message) > tightClip+4 {
		t.Fatalf("facts: %+v", c)
	}
	// the fingerprint is of the raw condition, whatever the clip
	small := project(t, KindSet, obj(t, `{"apiVersion":"fluxcd.controlplane.io/v1","kind":"ResourceSet","metadata":{"name":"rs","namespace":"team","uid":"u2","generation":3},
		"status":{"conditions":[`+ready("False", "DependencyNotReady", long, "2026-10-17T10:00:00Z", 3)+`]}}`))
	a, _ := o.Fingerprint()
	b, _ := small.Fingerprint()
	if a != b {
		t.Fatal("an unchanged condition fingerprints differently under a tighter clip")
	}
	// two messages that differ only beyond any clip differ in fingerprint
	other := project(t, KindSet, obj(t, `{"apiVersion":"fluxcd.controlplane.io/v1","kind":"ResourceSet","metadata":{"name":"rs","namespace":"team","uid":"u2","generation":3},
		"status":{"conditions":[`+ready("False", "DependencyNotReady", long+"X", "2026-10-17T10:00:00Z", 3)+`]}}`))
	if c, _ := other.Fingerprint(); c == b {
		t.Fatal("a change beyond the clip is invisible to the fingerprint")
	}
}

var t0 = time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

// state builds a provider at a reconcile annotation and Ready.
func state(t *testing.T, disabled bool, readyCond, exported string) (*Object, Common) {
	ann := ""
	if disabled {
		ann = `,"annotations":{"fluxcd.controlplane.io/reconcile":"disabled"}`
	}
	extra := ""
	if exported != "" {
		extra = `,"lastExportedRevision":"` + exported + `"`
	}
	o := project(t, KindProvider, obj(t, provider(ann, readyCond, extra)))
	return o, ParseProvider(o).Common
}

// S1a §4 (revisions 3 and 4): the resume cases of §7, against the tracker.
func TestResumeEvidence(t *testing.T) {
	frozen := ready("False", "ReconciliationFailed", "fetch failed", "2026-10-16T09:00:00Z", 1)
	inProgress := ready("Unknown", "Progressing", "Reconciliation in progress", "2026-10-17T10:00:05Z", 1)
	succeeded := ready("True", "ReconciliationSucceeded", "Reconciliation finished in 2s", "2026-10-17T10:00:07Z", 1)
	cases := []struct {
		name  string
		steps []func(t *testing.T) (*Object, Common, string)
		want  []Resume
	}{
		{"a skipped scheduled run (Ready unchanged, tokens move)", seq(
			st(true, frozen, "rev1"), st(false, frozen, "rev1")), []Resume{Current, Awaiting}},
		{"SkippedDueToSchedule over a stale in-progress state", seq(
			st(true, inProgress, "rev1"), st(false, ready("True", ReasonSkippedSchedule, "Reconciliation skipped", "2026-10-17T10:01:00Z", 1), "rev1")),
			[]Resume{Current, Awaiting}},
		{"a forced fetch: evaluating, then evaluated", seq(
			st(true, frozen, "rev1"), st(false, inProgress, "rev1"), st(false, succeeded, "rev1"), st(false, succeeded, "rev1")),
			[]Resume{Current, Evaluating, Current, Current}},
		{"a new failure, no history", seq(
			st(true, frozen, "rev1"), st(false, ready("False", "ReconciliationFailed", "fetch failed: 401", "2026-10-17T10:00:09Z", 1), "rev1")),
			[]Resume{Current, Current}},
		{"a changed export is evidence", seq(st(true, frozen, "rev1"), st(false, frozen, "rev2")), []Resume{Current, Current}},
		{"a watch gap across which the run completed", seq(st(true, frozen, "rev1"), st(false, succeeded, "rev1")), []Resume{Current, Current}},
		{"a resume never observed", seq(st(false, frozen, "rev1")), []Resume{Current}},
		{"initialized while disabled, enabled with no run", seq(
			st(true, "", ""), st(true, ready("True", ReasonDisabled, "Initialized with reconciliation suspended", "2026-10-17T10:00:00Z", 1), ""),
			st(false, ready("True", ReasonDisabled, "Initialized with reconciliation suspended", "2026-10-17T10:00:00Z", 1), "")),
			[]Resume{Current, Current, Awaiting}},
		{"a resume, then a delayed initialization patch, then a run", seq(
			st(true, "", ""), st(false, "", ""), st(false, ready("True", ReasonDisabled, "Initialized with reconciliation suspended", "2026-10-17T10:00:00Z", 1), ""),
			st(false, inProgress, ""), st(false, succeeded, "")),
			[]Resume{Current, Awaiting, Awaiting, Evaluating, Current}},
	}
	for _, c := range cases {
		tr := NewTracker()
		for i, step := range c.steps {
			o, cm, exp := step(t)
			if got := tr.Observe(t0.Add(time.Duration(i)*time.Second), o, cm, exp); got != c.want[i] {
				t.Errorf("%s, step %d: got %v, want %v", c.name, i, got, c.want[i])
			}
		}
	}
}

func seq(f ...func(t *testing.T) (*Object, Common, string)) []func(t *testing.T) (*Object, Common, string) {
	return f
}

func st(disabled bool, cond, exported string) func(t *testing.T) (*Object, Common, string) {
	return func(t *testing.T) (*Object, Common, string) {
		o, c := state(t, disabled, cond, exported)
		return o, c, exported
	}
}

// S1a §4: the rule.
func TestRule(t *testing.T) {
	base := Common{Generation: 3, ReadyGeneration: 3, Ready: Ready, ReadySince: t0}
	cases := []struct {
		name string
		c    func(c Common) Common
		r    Resume
		role health.Kind
		lbl  string
		incl bool
	}{
		{"ready", func(c Common) Common { return c }, Current, health.OK, "Ready", false},
		{"suspended first", func(c Common) Common { c.Disabled, c.Ready = true, Failed; return c }, Awaiting, health.Paused, LabelSuspended, false},
		{"awaiting is checking, not counted", func(c Common) Common { c.Ready = Failed; return c }, Awaiting, health.Unknown, LabelAwaiting, false},
		{"ReconciliationDisabled is never a verdict", func(c Common) Common { c.Reason = ReasonDisabled; return c }, Current, health.Unknown, LabelUnproven, false},
		{"evaluating", func(c Common) Common { c.Ready = Reconciling; return c }, Evaluating, health.Progressing, LabelEvaluating, true},
		{"an older Ready is Syncing", func(c Common) Common { c.Generation = 4; c.Ready = Failed; return c }, Current, health.Progressing, LabelSyncing, true},
		{"a current failure is a fault", func(c Common) Common { c.Ready, c.Reason, c.Message = Failed, "BuildFailed", "boom"; return c }, Current, health.Fault, "Failed", true},
		{"stalled", func(c Common) Common { c.Ready, c.Stalled = Failed, true; return c }, Current, health.Fault, "Stalled", true},
		{"progressing", func(c Common) Common { c.Ready, c.Reason = Unknown, "Progressing"; return c }, Current, health.Progressing, "Progressing", true},
		{"not reported", func(c Common) Common { c.Ready = NoReady; return c }, Current, health.Unknown, "Not reported", true},
	}
	for _, c := range cases {
		e := Rule(c.c(base), c.r)
		if e.Role != c.role || e.Label != c.lbl || e.Included != c.incl {
			t.Errorf("%s: %+v", c.name, e)
		}
	}
	p := Provider{Common: base, Schedule: []Schedule{{Cron: "0 * * * *"}}}
	if e := ProviderRule(p, Awaiting); e.Label != LabelSchedule {
		t.Errorf("a scheduled provider awaiting: %+v", e)
	}
}
