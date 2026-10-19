package keda

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/hpa"
)

var now = time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)

// ---- fixtures -----------------------------------------------------------

func labItems(t *testing.T, kind string) []*unstructured.Unstructured {
	t.Helper()
	b, err := os.ReadFile("testdata/lab-" + kind + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var list struct{ Items []map[string]any }
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	var out []*unstructured.Unstructured
	for _, o := range list.Items {
		out = append(out, &unstructured.Unstructured{Object: o})
	}
	return out
}

func labSO(t *testing.T, name string) *ScaledObject {
	t.Helper()
	for _, u := range labItems(t, "scaledobjects") {
		if u.GetName() == name {
			return ProjectScaledObject(u)
		}
	}
	t.Fatalf("no lab ScaledObject %s", name)
	return nil
}

func labHPA(t *testing.T, name string) *hpa.HPA {
	t.Helper()
	b, _ := os.ReadFile("testdata/lab-hpa.json")
	var list autoscalingv2.HorizontalPodAutoscalerList
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	for i := range list.Items {
		if list.Items[i].Name == name {
			v, err := hpa.Project(&list.Items[i])
			if err != nil {
				t.Fatal(err)
			}
			return v.(*hpa.HPA)
		}
	}
	t.Fatalf("no lab HPA %s", name)
	return nil
}

// so builds a ScaledObject: annotations, conditions as "Type=Status:Reason:Message".
func so(ann map[string]string, conds ...string) *ScaledObject {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "keda.sh/v1alpha1", "kind": "ScaledObject",
		"metadata": map[string]any{"namespace": "team", "name": "web", "uid": "so-1", "resourceVersion": "7",
			"creationTimestamp": now.Add(-time.Hour).Format(time.RFC3339)},
		"spec": map[string]any{"scaleTargetRef": map[string]any{"name": "web"}, "minReplicaCount": int64(1), "maxReplicaCount": int64(3),
			"triggers": []any{map[string]any{"type": "prometheus", "metadata": map[string]any{"serverAddress": "http://prom", "query": "SENTINEL-METADATA"}}}},
	}}
	if len(ann) > 0 {
		u.SetAnnotations(ann)
	}
	var cs []any
	for _, c := range conds {
		typ, rest, _ := strings.Cut(c, "=")
		parts := strings.SplitN(rest, ":", 3)
		for len(parts) < 3 {
			parts = append(parts, "")
		}
		cs = append(cs, map[string]any{"type": typ, "status": parts[0], "reason": parts[1], "message": parts[2]})
	}
	if cs != nil {
		_ = unstructured.SetNestedSlice(u.Object, cs, "status", "conditions")
	}
	return ProjectScaledObject(u)
}

func withPausedCount(s *ScaledObject, n int32) *ScaledObject {
	s.PausedCount = &n
	return s
}

func hpaOf(name string, conds ...string) *hpa.HPA {
	h := &hpa.HPA{Namespace: "team", Name: name, UID: "h-" + name, Generation: 1, Max: 3, Current: 2, Desired: 2,
		Controller: &hpa.Owner{APIVersion: "keda.sh/v1alpha1", Kind: "ScaledObject", Name: "web", UID: "so-1"},
		Target:     hpa.Target{APIVersion: "apps/v1", Kind: "Deployment", Name: "web"}}
	for _, c := range conds {
		typ, rest, _ := strings.Cut(c, "=")
		parts := strings.SplitN(rest, ":", 3)
		for len(parts) < 3 {
			parts = append(parts, "")
		}
		h.Conds = append(h.Conds, hpa.Cond{Type: typ, Status: parts[0], Reason: parts[1], Message: parts[2], At: now.Add(-time.Minute)})
	}
	return h
}

var (
	v221     = Version{Known: true, Major: 2, Minor: 21}
	ready    = "Ready=True:ScaledObjectReady:ScaledObject is defined correctly and is ready for scaling"
	active   = "Active=True:ScalerActive:Scaling is performed because triggers are active"
	idle     = "Active=False:ScalerNotActive:Scaling is not performed because triggers are not active"
	noFb     = "Fallback=False:NoFallbackFound:No fallbacks are active on this scaled object"
	notPause = "Paused=False::"
	paused   = "Paused=True:ScaledObjectPaused:ScaledObject is paused"
	hpaOK    = []string{"AbleToScale=True:ReadyForNewScale", "ScalingActive=True:ValidMetricFound", "ScalingLimited=False:DesiredWithinRange"}
)

func current(h *hpa.HPA) HPAEvidence { return HPAEvidence{Joined: h, Current: true, Complete: true} }
func absent() HPAEvidence            { return HPAEvidence{Current: true, Complete: true} }
func capped(h *hpa.HPA) HPAEvidence  { return HPAEvidence{Joined: h, Current: true} }
func stale(h *hpa.HPA) HPAEvidence   { return HPAEvidence{Joined: h, AsOf: now.Add(-5 * time.Minute)} }
func unreadable() HPAEvidence        { return HPAEvidence{} }
func eps(k EpisodeKind, age time.Duration, observed bool) map[EpisodeKind]Episode {
	return map[EpisodeKind]Episode{k: {Start: now.Add(-age), Observed: observed}}
}

// ---- projection ----------------------------------------------------------

// The lab's objects (lab-s9 t5) project within MaxObject, with the fields
// the rules read.
func TestProjectLab(t *testing.T) {
	for _, u := range labItems(t, "scaledobjects") {
		s := ProjectScaledObject(u)
		if s.Size <= 0 || s.Size > MaxObject || s.UID == "" || len(s.Conds) == 0 {
			t.Fatalf("%s: size %d uid %q conds %d", s.Name, s.Size, s.UID, len(s.Conds))
		}
	}
	if f := labSO(t, "failing"); f.Fallback == nil || f.Fallback.FailureThreshold != 2 || f.Fallback.Replicas != 2 ||
		f.Health["s0-prometheus"].Status != "Failing" || !f.CondIs(CondFallback, "True") {
		t.Fatalf("failing: %+v %+v", f.Fallback, f.Health)
	}
	if c := labSO(t, "custom-name"); c.HPANameSpec != "web-autoscaler" || c.HPAName != "web-autoscaler" {
		t.Fatalf("custom-name: %q %q", c.HPANameSpec, c.HPAName)
	}
	if g, k, n := labSO(t, "sts").Target(); g != "apps" || k != "StatefulSet" || n != "sts" {
		t.Fatalf("sts target: %s %s %s", g, k, n)
	}
	if p := labSO(t, "paused-at-one"); p.PausedCount == nil || *p.PausedCount != 1 || p.Pause.PausedReplicas == nil {
		t.Fatalf("paused-at-one: %+v", p.Pause)
	}
	if o := labSO(t, "one-way"); o.Pause.ScaleIn == nil || ScaledObjectIntent(o.Pause, v221).Kind != PauseDirectional {
		t.Fatalf("one-way: %+v", o.Pause)
	}
	if a := labSO(t, "adopted"); !a.Transfer {
		t.Fatal("adopted: the transfer annotation")
	}
	if b := labSO(t, "bad-auth"); len(b.AuthRefs()) != 1 || b.AuthRefs()[0] != "TriggerAuthentication/missing-secret" {
		t.Fatalf("bad-auth refs: %v", b.AuthRefs())
	}
	if c := labSO(t, "cluster-auth"); c.AuthRefs()[0] != "ClusterTriggerAuthentication/klyx-s9-lab" {
		t.Fatalf("cluster-auth refs: %v", c.AuthRefs())
	}
	for _, u := range labItems(t, "scaledjobs") {
		if j := ProjectScaledJob(u); j.Size > MaxObject || !j.CondIs(CondReady, "True") {
			t.Fatalf("scaledjob %s", j.Name)
		}
	}
	for _, kind := range []string{"triggerauthentications", "clustertriggerauthentications"} {
		for _, u := range labItems(t, kind) {
			a := ProjectAuth(u, kind == "clustertriggerauthentications")
			if a.Size > MaxAuthObject || (len(a.Sources) == 0 && a.PodIdentity == nil) {
				t.Fatalf("%s %s: %+v", kind, a.Name, a)
			}
		}
	}
}

// §10: trigger metadata and the Vault token never enter a projection;
// caps are flagged, labels are never clipped.
func TestProjectNeverRetains(t *testing.T) {
	s := so(nil, ready)
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "SENTINEL-METADATA") || strings.Contains(string(b), "http://prom") {
		t.Fatalf("trigger metadata retained: %s", b)
	}
	ta := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "vault", "namespace": "team", "uid": "ta-1"},
		"spec": map[string]any{
			"hashiCorpVault":  map[string]any{"address": "https://vault:8200", "authentication": "token", "credential": map[string]any{"token": "SENTINEL-TOKEN"}, "secrets": []any{map[string]any{"parameter": "p", "key": "k", "path": "secret/x"}}},
			"secretTargetRef": []any{map[string]any{"parameter": "bearerToken", "name": "tok", "key": "token"}},
			"oauth2":          map[string]any{"type": "clientCredentials", "clientId": "c", "tokenUrl": "https://idp/token", "clientSecret": map[string]any{"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "oauth", "key": "secret"}}}},
			"podIdentity":     map[string]any{"provider": "aws", "identityOwner": "workload"},
		}}}
	a := ProjectAuth(ta, false)
	b, _ = json.Marshal(a)
	if strings.Contains(string(b), "SENTINEL-TOKEN") {
		t.Fatalf("the Vault token was retained: %s", b)
	}
	var vault, oauth *Source
	for i := range a.Sources {
		switch a.Sources[i].Kind {
		case "hashiCorpVault":
			vault = &a.Sources[i]
		case "oauth2":
			oauth = &a.Sources[i]
		}
	}
	if vault == nil || vault.Method != "token" || vault.Detail != "https://vault:8200" || vault.RefN != 1 {
		t.Fatalf("vault: %+v", vault)
	}
	if oauth == nil || len(oauth.Refs) != 1 || oauth.Refs[0].Name != "oauth" || oauth.Refs[0].Kind != "Secret" {
		t.Fatalf("oauth2 secret reference: %+v", oauth)
	}
	if a.PodIdentity == nil || a.PodIdentity.Provider != "aws" || a.PodIdentity.IdentityOwner != "workload" {
		t.Fatalf("podIdentity: %+v", a.PodIdentity)
	}
	// caps: 17 triggers, 9 conditions, a long label
	u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "big", "namespace": "team", "uid": "u",
		"labels": map[string]any{"ok": "v", "long": strings.Repeat("x", 64)}}, "spec": map[string]any{}}}
	var trig, conds []any
	for i := 0; i < 17; i++ {
		trig = append(trig, map[string]any{"type": "cron", "authenticationRef": map[string]any{"name": fmt.Sprint("a", i)}})
	}
	for i := 0; i < 9; i++ {
		conds = append(conds, map[string]any{"type": fmt.Sprint("C", i), "status": "True", "message": strings.Repeat("m", 2000)})
	}
	_ = unstructured.SetNestedSlice(u.Object, trig, "spec", "triggers")
	_ = unstructured.SetNestedSlice(u.Object, conds, "status", "conditions")
	big := ProjectScaledObject(u)
	if !big.TriggersCut || big.TriggerN != 17 || len(big.Triggers) != 16 || !big.CondsCut || len(big.Conds) != 8 || !big.LabelsCut || big.Labels["long"] != "" || big.Labels["ok"] != "v" {
		t.Fatalf("caps: triggers %d/%d cut %v, conds %d cut %v, labels %v cut %v", len(big.Triggers), big.TriggerN, big.TriggersCut, len(big.Conds), big.CondsCut, big.Labels, big.LabelsCut)
	}
	if big.Size > MaxObject {
		t.Fatalf("a failing object with every message at its cap is %d bytes, over MaxObject", big.Size)
	}
}

// ---- version and pause ---------------------------------------------------

func TestVersionOfImage(t *testing.T) {
	for img, want := range map[string]string{
		"ghcr.io/kedacore/keda:2.21.0":                   "2.21.0",
		"kedacore/keda:2.17.3@sha256:abc":                "2.17.3",
		"docker.io/kedacore/keda:v2.12.1":                "2.12.1",
		"registry.example/kedacore/keda:2.21.0":          "unknown", // not KEDA's own repository
		"ghcr.io/kedacore/keda-metrics-apiserver:2.21.0": "unknown", // not the operator
		"ghcr.io/kedacore/keda:main":                     "unknown",
	} {
		if got := VersionOfImage(img).String(); got != want {
			t.Errorf("%s: %s, want %s", img, got, want)
		}
	}
	if !IsMetricsServerImage("ghcr.io/kedacore/keda-metrics-apiserver:2.21.0") || IsMetricsServerImage("example/metrics:1") {
		t.Fatal("metrics server image identity")
	}
}

// §1.3: what the annotations request, per version.
func TestIntent(t *testing.T) {
	s := func(v string) *string { return &v }
	ver := func(m int) Version { return Version{Known: true, Major: 2, Minor: m} }
	for _, tc := range []struct {
		name       string
		p          Pause
		v          Version
		kind       PauseKind
		ann        string
		notApplied string
		unverified string
	}{
		{"paused-replicas wins over paused=false", Pause{PausedReplicas: s("3"), Paused: s("false")}, v221, PauseFull, AnnPausedReplicas, "", ""},
		{"paused on 2.11 has no effect", Pause{Paused: s("true")}, ver(11), PauseNone, "", AnnPaused, ""},
		{"paused=false on 2.12 still pauses", Pause{Paused: s("false")}, ver(12), PauseFull, AnnPaused, "", ""},
		{"paused=false on 2.13 does not", Pause{Paused: s("false")}, ver(13), PauseNone, "", "", ""},
		{"an unparseable paused is true", Pause{Paused: s("yes please")}, v221, PauseFull, AnnPaused, "", ""},
		{"paused, version unknown", Pause{Paused: s("true")}, Version{}, PauseFull, AnnPaused, "", AnnPaused},
		{"paused=false, version unknown", Pause{Paused: s("false")}, Version{}, PauseNone, "", "", AnnPaused},
		{"directional on 2.18", Pause{ScaleIn: s("true")}, ver(18), PauseDirectional, "", "", ""},
		{"directional on 2.17 has no effect", Pause{ScaleOut: s("true")}, ver(17), PauseNone, "", AnnPausedScaleOut, ""},
		{"directional, version unknown", Pause{ScaleIn: s("true")}, Version{}, PauseDirectional, "", "", AnnPausedScaleIn},
		{"a full pause wins over a directional one", Pause{Paused: s("true"), ScaleIn: s("true")}, v221, PauseFull, AnnPaused, "", ""},
	} {
		in := ScaledObjectIntent(tc.p, tc.v)
		if in.Kind != tc.kind || in.Annotation != tc.ann || in.NotApplied != tc.notApplied || in.Unverified != tc.unverified {
			t.Errorf("%s: %+v", tc.name, in)
		}
	}
	if in := ScaledObjectIntent(Pause{PausedReplicas: s("three")}, v221); !in.Malformed || in.Requested != nil {
		t.Errorf("malformed paused-replicas: %+v", in)
	}
	if in := ScaledJobIntent(Pause{Paused: s("true")}, ver(10)); in.Kind != PauseNone || in.NotApplied != AnnPaused {
		t.Errorf("ScaledJob pause on 2.10: %+v", in)
	}
	if in := ScaledJobIntent(Pause{Paused: s("false")}, ver(11)); in.Kind != PauseFull {
		t.Errorf("ScaledJob paused=false on 2.11 still pauses: %+v", in)
	}
}

// §1.3's completion evidence per version: never Paused=True alone.
func TestPauseCompleted(t *testing.T) {
	ann := func(k, v string) map[string]string { return map[string]string{k: v} }
	ver := func(m int) Version { return Version{Known: true, Major: 2, Minor: m} }
	for _, tc := range []struct {
		name      string
		s         *ScaledObject
		v         Version
		hpaAbsent bool
		want      bool
	}{
		{"2.10: paused-replicas with pausedReplicaCount set", withPausedCount(so(ann(AnnPausedReplicas, "2"), ready), 2), ver(10), true, true},
		{"2.10: a non-nil zero is set", withPausedCount(so(ann(AnnPausedReplicas, "0"), ready), 0), ver(10), true, true},
		{"2.10: no pausedReplicaCount yet", so(ann(AnnPausedReplicas, "2"), ready), ver(10), true, false},
		{"2.11: paused-replicas, Paused=True, HPA gone", withPausedCount(so(ann(AnnPausedReplicas, "1"), ready, paused), 1), ver(11), true, true},
		{"2.21: paused, Paused=True, HPA gone", so(ann(AnnPaused, "true"), ready, paused), v221, true, true},
		{"the HPA still exists", so(ann(AnnPaused, "true"), ready, paused), v221, false, false},
		{"Ready=False after the HPA's deletion", so(ann(AnnPausedReplicas, "1"), "Ready=False:ScaledObjectCheckFailed:failed to scale target to paused replica count: forbidden", paused), v221, true, false},
		{"a directional pause is never a completed full pause", so(ann(AnnPausedScaleIn, "true"), ready, paused), v221, true, false},
	} {
		if got := PauseCompleted(tc.s, ScaledObjectIntent(tc.s.Pause, tc.v), tc.hpaAbsent); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

// ---- the ScaledObject rule (§3, §3.2) -------------------------------------

func rule(s *ScaledObject, in SOInputs) Verdict {
	if in.Now.IsZero() {
		in.Now = now
	}
	if !in.Version.Known && in.Version.Major == 0 {
		in.Version = v221
	}
	return ScaledObjectRule(s, in)
}

func expect(t *testing.T, name string, v Verdict, role health.Kind, label string) {
	t.Helper()
	if v.Eval.Role != role || (label != "" && !strings.Contains(v.Eval.Label+" | "+v.Eval.Reason, label)) {
		var fs []string
		for _, f := range v.Findings {
			fs = append(fs, fmt.Sprintf("%v %q %q", f.Role, f.Label, f.Text))
		}
		t.Fatalf("%s: %v %q (%s); findings %v", name, v.Eval.Role, v.Eval.Label, v.Eval.Reason, fs)
	}
	if v.Eval.Included != role.Attention() {
		t.Fatalf("%s: included %v for %v", name, v.Eval.Included, role)
	}
}

// §3's HPA result table, every row.
func TestHPAResultTable(t *testing.T) {
	base := func() *ScaledObject { return so(nil, ready, active, noFb, notPause) }
	ambiguous := hpaOf("keda-hpa-web", "AbleToScale=True:SucceededGetScale", "ScalingActive=False:AmbiguousSelector:pods by selector app=web are controlled by more than one HPA")
	ok := hpaOf("keda-hpa-web", hpaOK...)
	mirrorFalse := func() *ScaledObject {
		return so(nil, ready, active, noFb, notPause, "HPAActive=False:FailedGetExternalMetric:HPA is not actively scaling")
	}
	mirrorTrue := func() *ScaledObject {
		return so(nil, ready, active, noFb, notPause, "HPAActive=True:HPAActive:HPA is actively scaling")
	}
	for _, tc := range []struct {
		name  string
		s     *ScaledObject
		ev    HPAEvidence
		ep    map[EpisodeKind]Episode
		role  health.Kind
		label string
	}{
		{"current, failed", base(), HPAEvidence{Joined: ambiguous, Current: true, Complete: true, Others: []string{"team/plain (HPA)"}}, nil, health.Fault, "also scaling this target: team/plain (HPA)"},
		{"current, failed under a cap", base(), capped(ambiguous), nil, health.Fault, "HPA cannot compute replicas"},
		{"AbleToScale=False with ScalingActive=True", base(), current(hpaOf("keda-hpa-web", "AbleToScale=False:FailedUpdateScale:conflict", "ScalingActive=True:ValidMetricFound")), nil, health.Fault, "HPA cannot scale"},
		{"current healthy, KEDA's copy False", mirrorFalse(), current(ok), nil, health.OK, "Active"},
		{"current healthy under a cap, KEDA's copy False", mirrorFalse(), capped(ok), nil, health.OK, "Active"},
		{"current healthy, KEDA's copy True", mirrorTrue(), current(ok), nil, health.OK, "Active (2 replicas, wants 2)"},
		{"stale, failed", base(), stale(ambiguous), nil, health.Fault, "as of"},
		{"stale healthy, KEDA's copy False", mirrorFalse(), stale(ok), nil, health.Unknown, "HPA health not established"},
		{"stale healthy, KEDA's copy True", mirrorTrue(), stale(ok), nil, health.Unknown, "HPA health not current"},
		{"absence proven, within the episode", base(), absent(), eps(EpAwaitingHPA, time.Minute, true), health.Progressing, "Awaiting HPA"},
		{"absence proven, past the episode", base(), absent(), eps(EpAwaitingHPA, 3*time.Minute, true), health.Fault, "no HPA controlled by this ScaledObject"},
		{"absence not proven, KEDA's copy False", mirrorFalse(), unreadable(), nil, health.Fault, "as KEDA last saw it"},
		{"absence not proven, KEDA's copy True proves nothing", mirrorTrue(), unreadable(), nil, health.Unknown, "HPA join not established"},
		{"a 2.17-shaped object with a capped read is never ok", base(), HPAEvidence{Current: true}, nil, health.Unknown, "HPA join not established"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, tc.name, rule(tc.s, SOInputs{HPA: tc.ev, Episodes: tc.ep}), tc.role, tc.label)
		})
	}
	// the current-healthy disagreement is said, never attention
	v := rule(mirrorFalse(), SOInputs{HPA: current(ok)})
	found := false
	for _, f := range v.Findings {
		found = found || strings.Contains(f.Text, "KEDA last reported HPAActive=False (FailedGetExternalMetric); the HPA itself is healthy now")
	}
	if !found {
		t.Fatal("the disagreement is not said")
	}
	// 2.20.0's Ready reason is read as KEDA's copy
	v200 := so(nil, "Ready=False:HPAMetricsUnavailable:the HPA cannot fetch metrics", active, noFb, notPause)
	expect(t, "2.20.0 Ready reason, unreadable join", rule(v200, SOInputs{HPA: unreadable()}), health.Fault, "as KEDA last saw it")
	expect(t, "2.20.0 Ready reason, current healthy HPA", rule(v200, SOInputs{HPA: current(ok)}), health.OK, "")
}

// §3.2: independent findings, the worst the verdict; proven failures and
// absence before degradation; intent never clears a failure.
func TestScaledObjectFindings(t *testing.T) {
	ann := func(k, v string) map[string]string { return map[string]string{k: v} }
	ok := hpaOf("keda-hpa-web", hpaOK...)
	ambiguous := hpaOf("keda-hpa-web", "AbleToScale=True:SucceededGetScale", "ScalingActive=False:AmbiguousSelector:more than one HPA")
	fallback := "Fallback=True:FallbackExists:At least one trigger is falling back on this scaled object"
	failingHealth := func(s *ScaledObject) *ScaledObject {
		s.Health = map[string]HealthEntry{"s0-prometheus": {Failures: 5, Status: "Failing"}}
		s.Fallback = &Fallback{FailureThreshold: 2, Replicas: 2}
		return s
	}
	for _, tc := range []struct {
		name  string
		s     *ScaledObject
		in    SOInputs
		role  health.Kind
		label string
	}{
		{"no conditions within the grace", so(nil), SOInputs{HPA: absent()}, health.Unknown, "Not reconciled"},
		{"idle is healthy", so(nil, ready, idle, noFb, notPause), SOInputs{HPA: current(ok)}, health.OK, "Idle"},
		{"cooling down", so(nil, ready, "Active=False:ScalerCooldown:cooling", noFb, notPause), SOInputs{HPA: current(ok)}, health.OK, "Cooling down"},
		{"fallback: never a held count", failingHealth(so(nil, ready, active, fallback, notPause)), SOInputs{HPA: current(ok)}, health.Degraded, "fallback active (static): s0-prometheus failed 5 times (threshold 2)"},
		{"fallback with an AmbiguousSelector HPA: the fault", failingHealth(so(nil, ready, active, fallback, notPause)), SOInputs{HPA: current(ambiguous)}, health.Fault, "HPA cannot compute replicas"},
		{"fallback with a proven missing HPA past the episode", failingHealth(so(nil, ready, active, fallback, notPause)), SOInputs{HPA: absent(), Episodes: eps(EpAwaitingHPA, 5*time.Minute, false)}, health.Fault, "no HPA controlled by this ScaledObject"},
		{"partial trigger error", so(nil, "Ready=Unknown:PartialTriggerError:Some triggers are not working", active, noFb, notPause), SOInputs{HPA: current(ok)}, health.Degraded, "Some triggers failing"},
		{"a missing TriggerAuthentication heads the fault", so(nil, "Ready=False:ScaledObjectCheckFailed:error parsing prometheus metadata: bearer token=<empty> is required", "Active=Unknown:UnknownState:ScaledObject check failed", noFb, notPause),
			SOInputs{HPA: absent(), MissingAuths: []string{"TriggerAuthentication/no-such-auth"}}, health.Fault, "TriggerAuthentication no-such-auth does not exist"},
		{"hpaName mismatch", func() *ScaledObject { s := so(nil, ready, active, noFb, notPause); s.HPAName = "old"; return s }(), SOInputs{HPA: current(ok)}, health.Degraded, "status names HPA old; the HPA KEDA controls is keda-hpa-web"},
		// pauses
		{"a completed pause stands alone", so(ann(AnnPaused, "true"), ready, paused), SOInputs{HPA: absent()}, health.Paused, "frozen at the current replicas"},
		{"held at n: the target proven at n", withPausedCount(so(ann(AnnPausedReplicas, "1"), ready, paused), 1), SOInputs{HPA: absent(), TargetReplicas: ptr32(1)}, health.Paused, "held at 1 replicas"},
		{"a changed request: 3 to 5 while status says 3", withPausedCount(so(ann(AnnPausedReplicas, "5"), ready, paused), 3), SOInputs{HPA: absent(), TargetReplicas: ptr32(3)}, health.Paused, "Paused at 3; 5 requested"},
		{"a failed paused-count write after the HPA's deletion", so(ann(AnnPausedReplicas, "1"), "Ready=False:ScaledObjectCheckFailed:failed to scale target to paused replica count: forbidden", paused), SOInputs{HPA: absent()}, health.Fault, "pause failed: failed to scale target to paused replica count"},
		{"a malformed paused-replicas", so(ann(AnnPausedReplicas, "three"), "Ready=False:ScaledObjectCheckFailed:invalid paused replicas", noFb, notPause), SOInputs{HPA: current(ok)}, health.Fault, "invalid paused replicas (while a pause is requested)"},
		{"Ready=False before a new pause is not 'pause failed'", so(ann(AnnPaused, "true"), "Ready=False:TriggerError:Triggers defined in ScaledObject are not working correctly", noFb, notPause), SOInputs{HPA: current(ok)}, health.Fault, "(while a pause is requested)"},
		{"pause requested within the episode", so(ann(AnnPaused, "true"), ready, active, noFb, notPause), SOInputs{HPA: current(ok), Episodes: eps(EpPausing, 30*time.Second, true)}, health.Progressing, "Pause requested"},
		{"pause requested past the episode, the HPA still there", so(ann(AnnPaused, "true"), ready, active, noFb, notPause), SOInputs{HPA: current(ok), Episodes: eps(EpPausing, 3*time.Minute, true)}, health.Unknown, "KEDA still has its HPA keda-hpa-web"},
		{"a capped read during a pause: not established, never 'not applied'", so(ann(AnnPaused, "true"), ready, paused), SOInputs{HPA: HPAEvidence{Current: true}, Episodes: eps(EpPausing, 3*time.Minute, true)}, health.Unknown, "completion not established (the HPA read is incomplete)"},
		{"KEDA's transient 'being scaled' within the episode", so(ann(AnnPausedReplicas, "1"), "Ready=False:ScaledObjectCheckFailed:ScaledObject paused replicas are being scaled", paused), SOInputs{HPA: absent(), Episodes: eps(EpPausing, 20*time.Second, true)}, health.Progressing, "pausing: scaling to 1"},
		{"the transient past the episode", so(ann(AnnPausedReplicas, "1"), "Ready=False:ScaledObjectCheckFailed:ScaledObject paused replicas are being scaled", paused), SOInputs{HPA: absent(), Episodes: eps(EpPausing, 3*time.Minute, true)}, health.Fault, "while a pause is requested"},
		{"the transient beside an independent HPA failure", so(ann(AnnPausedReplicas, "1"), "Ready=False:ScaledObjectCheckFailed:ScaledObject paused replicas are being scaled", paused), SOInputs{HPA: current(ambiguous), Episodes: eps(EpPausing, 20*time.Second, true)}, health.Fault, "HPA cannot compute replicas"},
		{"a full pause requested while the HPA reads AmbiguousSelector", so(ann(AnnPaused, "true"), ready, active, noFb, notPause), SOInputs{HPA: current(ambiguous), Episodes: eps(EpPausing, 10*time.Second, true)}, health.Fault, "HPA cannot compute replicas"},
		{"a directional pause already Paused=True, then a full pause requested", so(map[string]string{AnnPausedScaleIn: "true", AnnPaused: "true"}, ready, active, noFb, paused), SOInputs{HPA: current(ok), Episodes: eps(EpPausing, 10*time.Second, true)}, health.Progressing, "Pause requested"},
		{"resuming within the episode", so(nil, ready, active, noFb, paused), SOInputs{HPA: current(ok), Episodes: eps(EpResuming, 30*time.Second, true)}, health.Progressing, "Resuming"},
		{"resuming with an HPA fault", so(nil, ready, active, noFb, paused), SOInputs{HPA: current(ambiguous), Episodes: eps(EpResuming, 30*time.Second, true)}, health.Fault, "HPA cannot compute replicas"},
		{"resume not established", so(nil, ready, active, noFb, paused), SOInputs{HPA: current(ok), Episodes: eps(EpResuming, 3*time.Minute, false)}, health.Unknown, "resume not established (at least since"},
		{"a directional pause is a note", so(ann(AnnPausedScaleIn, "true"), ready, active, noFb, paused), SOInputs{HPA: current(ok)}, health.OK, "scale-in paused"},
		{"paused on 2.11 is not applied", so(ann(AnnPaused, "true"), ready, active, noFb, notPause), SOInputs{HPA: current(ok), Version: Version{Known: true, Major: 2, Minor: 11}}, health.Unknown, "KEDA 2.11.0 does not apply autoscaling.keda.sh/paused"},
		{"version unknown: pause unverified past the episode", so(ann(AnnPaused, "true"), ready, active, noFb, notPause), SOInputs{HPA: current(ok), Version: Version{Major: -1}, Episodes: eps(EpPausing, 3*time.Minute, true)}, health.Unknown, "is unverified (version unknown)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "no conditions within the grace" {
				s := tc.s
				s.Created = now.Add(-30 * time.Second)
				expect(t, tc.name, rule(s, tc.in), health.Progressing, "Waiting for KEDA")
				s.Created = now.Add(-3 * time.Minute)
			}
			expect(t, tc.name, rule(tc.s, tc.in), tc.role, tc.label)
		})
	}
}

func ptr32(n int32) *int32 { return &n }

// A pending deadline is Next: a clock-only crossing is re-evaluated.
func TestNextAndBoundary(t *testing.T) {
	v := rule(so(map[string]string{AnnPaused: "true"}, ready, active, noFb, notPause), SOInputs{HPA: current(hpaOf("h", hpaOK...)), Episodes: eps(EpPausing, 30*time.Second, true)})
	if v.Eval.Next == nil || !v.Eval.Next.Equal(now.Add(90*time.Second)) {
		t.Fatalf("next: %v", v.Eval.Next)
	}
	v = rule(so(nil, ready, active, noFb, notPause), SOInputs{HPA: absent(), Episodes: eps(EpAwaitingHPA, 3*time.Minute, true)})
	if v.Eval.Boundary == nil || !v.Eval.Boundary.Equal(now.Add(-time.Minute)) {
		t.Fatalf("boundary: %v", v.Eval.Boundary)
	}
}

// ---- the episode tracker (§3.1) --------------------------------------------

func TestTracker(t *testing.T) {
	var tr Tracker
	c := Continuity{Generation: 1, Epoch: 1, HPAGeneration: 1, HPAEpoch: 1}
	t0 := now
	// first seen already pausing: a lower bound
	got := tr.Observe(c, t0, map[string]Obs{"a": {States: EpPausing, RV: "1"}, "b": {RV: "1"}}, true)
	if ep := got["a"][EpPausing]; ep.Observed || !ep.Start.Equal(t0) {
		t.Fatalf("first seen: %+v", ep)
	}
	// an old object newly paused: an observed start
	got = tr.Observe(c, t0.Add(time.Minute), map[string]Obs{"a": {States: EpPausing, RV: "1"}, "b": {States: EpPausing, RV: "2"}}, true)
	if ep := got["b"][EpPausing]; !ep.Observed || !ep.Start.Equal(t0.Add(time.Minute)) {
		t.Fatalf("newly paused: %+v", ep)
	}
	if ep := got["a"][EpPausing]; !ep.Start.Equal(t0) {
		t.Fatalf("unchanged object carries on: %+v", ep)
	}
	// written between non-contiguous snapshots: restart as a lower bound
	got = tr.Observe(c, t0.Add(2*time.Minute), map[string]Obs{"a": {States: EpPausing, RV: "9"}, "b": {States: EpPausing, RV: "2"}}, false)
	if ep := got["a"][EpPausing]; ep.Observed || !ep.Start.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("written, not contiguous: %+v", ep)
	}
	if ep := got["b"][EpPausing]; !ep.Start.Equal(t0.Add(time.Minute)) {
		t.Fatalf("unwritten carries on: %+v", ep)
	}
	// a gap (the HPA kind relisted): every episode restarts as a lower bound
	gap := c
	gap.HPAEpoch++
	got = tr.Observe(gap, t0.Add(3*time.Minute), map[string]Obs{"a": {States: EpPausing, RV: "9"}, "b": {States: EpPausing, RV: "2"}}, true)
	if ep := got["b"][EpPausing]; ep.Observed || !ep.Start.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("after a gap: %+v", ep)
	}
	// a UID replacement is a new object; clearing drops the episode
	got = tr.Observe(gap, t0.Add(4*time.Minute), map[string]Obs{"a2": {States: EpPausing, RV: "1"}, "b": {RV: "3"}}, true)
	if _, ok := got["b"]; ok {
		t.Fatal("a cleared episode stayed")
	}
	if ep := got["a2"][EpPausing]; ep.Observed {
		t.Fatalf("a replacement first seen pausing is a lower bound: %+v", ep)
	}
	tr.Break()
	got = tr.Observe(gap, t0.Add(5*time.Minute), map[string]Obs{"a2": {States: EpPausing, RV: "1"}}, true)
	if ep := got["a2"][EpPausing]; !ep.Start.Equal(t0.Add(5 * time.Minute)) {
		t.Fatalf("after Break: %+v", ep)
	}
}

// States: what the tracker is fed.
func TestStates(t *testing.T) {
	if st := States(so(map[string]string{AnnPaused: "true"}, ready, notPause), v221, false); st != EpPausing {
		t.Fatalf("pausing: %b", st)
	}
	if st := States(so(nil, ready, paused), v221, true); st != EpResuming|EpAwaitingHPA {
		t.Fatalf("resuming and awaiting: %b", st)
	}
	if st := States(so(map[string]string{AnnPaused: "true"}, ready, paused), v221, true); st != 0 {
		t.Fatalf("a completed pause has no episode: %b", st)
	}
}

// ---- the ScaledJob rule (§5) ------------------------------------------------

func TestScaledJobRule(t *testing.T) {
	job := func(ann map[string]string, conds ...string) *ScaledJob {
		s := so(ann, conds...)
		j := &ScaledJob{Scaler: s.Scaler}
		j.Pause = Pause{Paused: s.Pause.Paused}
		return j
	}
	for _, tc := range []struct {
		name  string
		j     *ScaledJob
		in    JobInputs
		role  health.Kind
		label string
	}{
		{"active", job(nil, "Ready=True:ScaledJobReady:ok", "Active=True:ScalerActive:a", notPause), JobInputs{}, health.OK, "Active"},
		{"idle", job(nil, "Ready=True:ScaledJobReady:ok", "Active=False:ScalerNotActive:a", notPause), JobInputs{}, health.OK, "Idle"},
		{"a completed pause", job(map[string]string{AnnPaused: "true"}, "Ready=True:ScaledJobReady:ScaledJob is paused, skipping reconcile loop", "Paused=True:ScaledJobPaused:ScaledJob is paused"), JobInputs{}, health.Paused, "no new Jobs; running Jobs continue"},
		{"pausing", job(map[string]string{AnnPaused: "true"}, "Ready=True:ScaledJobReady:ok", notPause), JobInputs{Episodes: eps(EpPausing, 20*time.Second, true)}, health.Progressing, "Pause requested"},
		{"pausing past the episode", job(map[string]string{AnnPaused: "true"}, "Ready=True:ScaledJobReady:ok", notPause), JobInputs{Episodes: eps(EpPausing, 3*time.Minute, true)}, health.Unknown, "pause requested; completion not established ("},
		{"a job create failure", job(nil, "Ready=False:KEDAJobCreateFailed:quota exceeded", notPause), JobInputs{}, health.Fault, "quota exceeded"},
		{"partial", job(nil, "Ready=Unknown:PartialTriggerError:some", notPause), JobInputs{}, health.Degraded, "Some triggers failing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.in.Now.IsZero() {
				tc.in.Now = now
			}
			if !tc.in.Version.Known {
				tc.in.Version = v221
			}
			v := ScaledJobRule(tc.j, tc.in)
			expect(t, tc.name, v, tc.role, tc.label)
			if strings.Contains(v.Eval.Reason, "HPA") {
				t.Fatalf("a ScaledJob reason cites an HPA: %s", v.Eval.Reason)
			}
		})
	}
}

// The lab's real objects (lab-s9 t5): each ScaledObject joins its HPA by
// controller UID - a custom name and an adopted HPA included - and the
// rule reads the lab's states in KEDA's vocabulary.
func TestLabJoinsAndVerdicts(t *testing.T) {
	joined := map[string]string{"custom-name": "web-autoscaler", "adopted": "adopted-hpa", "failing": "keda-hpa-failing", "shared": "keda-hpa-shared"}
	for so, want := range joined {
		s, h := labSO(t, so), labHPA(t, want)
		if o, ok := h.KEDAOwner(); !ok || o.UID != s.UID {
			t.Fatalf("%s: the HPA %s is not controlled by its UID", so, want)
		}
	}
	verdict := func(name, hpaName string) Verdict {
		s := labSO(t, name)
		ev := absent()
		if hpaName != "" {
			ev = current(labHPA(t, hpaName))
		}
		return ScaledObjectRule(s, SOInputs{Now: now, Version: v221, HPA: ev})
	}
	expect(t, "failing", verdict("failing", "keda-hpa-failing"), health.Degraded, "fallback active (static): s0-prometheus failed")
	expect(t, "shared", verdict("shared", "keda-hpa-shared"), health.Fault, "AmbiguousSelector")
	expect(t, "idle-api", verdict("idle-api", "keda-hpa-idle-api"), health.OK, "Idle")
	expect(t, "paused", verdict("paused", ""), health.Paused, "frozen at the current replicas")
	expect(t, "bad-auth", verdict("bad-auth", ""), health.Fault, "ScaledObjectCheckFailed")
	expect(t, "no-target", verdict("no-target", ""), health.Fault, "no-such-deployment")
	expect(t, "wl-identity", verdict("wl-identity", "keda-hpa-wl-identity"), health.Fault, "")
	expect(t, "one-way", verdict("one-way", "keda-hpa-one-way"), health.OK, "scale-in paused")
}
