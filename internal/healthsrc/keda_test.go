package healthsrc

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/hpa"
	"github.com/moomora/klyx/internal/keda"
	"github.com/moomora/klyx/internal/observe"
)

var kNow = time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)

func kHPA(name, ownerUID string, conds ...hpa.Cond) *hpa.HPA {
	h := &hpa.HPA{Namespace: "team", Name: name, UID: "h-" + name, Generation: 1, Created: kNow.Add(-time.Hour), Max: 3, Current: 2, Desired: 2,
		Target: hpa.Target{APIVersion: "apps/v1", Kind: "Deployment", Name: "web"}, Conds: conds}
	if ownerUID != "" {
		h.Controller = &hpa.Owner{APIVersion: "keda.sh/v1alpha1", Kind: "ScaledObject", Name: "web", UID: ownerUID}
	}
	return h
}

var (
	hpaHealthy = []hpa.Cond{{Type: "AbleToScale", Status: "True"}, {Type: "ScalingActive", Status: "True", Reason: "ValidMetricFound"}}
	hpaAmbig   = []hpa.Cond{{Type: "AbleToScale", Status: "True"}, {Type: "ScalingActive", Status: "False", Reason: "AmbiguousSelector", Message: "more than one HPA"}}
)

func kSO(name, uid string, auth string, conds ...keda.Cond) *keda.ScaledObject {
	so := &keda.ScaledObject{Scaler: keda.Scaler{Meta: keda.Meta{Namespace: "team", Name: name, UID: uid, Created: kNow.Add(-time.Hour)}, Conds: conds}, TargetName: "web"}
	if auth != "" {
		so.Triggers = []keda.Trigger{{Type: "prometheus", AuthName: auth, AuthKind: keda.KindTriggerAuth}}
	}
	return so
}

var ready = keda.Cond{Type: "Ready", Status: "True"}

func kCapture(objs map[string][]any, complete map[string]bool) observe.Capture {
	current := map[string]bool{}
	for k, v := range complete {
		current[k] = v
	}
	return observe.Capture{Now: kNow, Objects: objs, Complete: complete, Current: current, AsOf: map[string]time.Time{}, Rows: true,
		Facts: map[string]any{FactKEDA: KEDAFacts{Version: keda.Version{Known: true, Major: 2, Minor: 21}}}}
}

// S9 §4: the standalone HPA part counts HPAs no ScaledObject controls; a
// KEDA-owned HPA is a fact row, never counted twice; AmbiguousSelector
// names the other autoscalers with their owners.
func TestEvalHPAs(t *testing.T) {
	plain := kHPA("plain", "", hpaAmbig...)
	owned := kHPA("keda-hpa-web", "so-1", hpaAmbig...)
	c := kCapture(map[string][]any{KindHPAs: {plain, owned}}, map[string]bool{KindHPAs: true})
	res := evalHPAs("x")(c)
	if res.Native.Total != 1 || res.Native.Roles.Fault != 1 {
		t.Fatalf("counts: %+v", res.Native)
	}
	rows := res.Rows.(RowSet[HPARow]).Rows
	if len(rows) != 2 {
		t.Fatalf("rows: %d", len(rows))
	}
	for _, r := range rows {
		switch r.Data.HPA.Name {
		case "keda-hpa-web":
			if !r.Data.FactOnly() || r.Eval.Role != health.Fault && r.Eval.Label != "" {
				t.Fatalf("a KEDA-owned row is a fact: %+v", r)
			}
			if r.Eval.Included {
				t.Fatal("a KEDA-owned row counted")
			}
		case "plain":
			if r.Eval.Role != health.Fault || r.Data.Others[0] != "team/keda-hpa-web (KEDA ScaledObject web)" {
				t.Fatalf("plain: %v %v", r.Eval.Role, r.Data.Others)
			}
		}
	}
}

// S9 §3: the ScaledObject part joins its HPA by controller UID, proves a
// missing authentication only from a complete read, and dates stale HPA
// evidence.
func TestEvalScaledObjects(t *testing.T) {
	failing := keda.Cond{Type: "Ready", Status: "False", Reason: "ScaledObjectCheckFailed", Message: "bearer token=<empty> is required"}
	objs := map[string][]any{
		KindScaledObjects: {kSO("web", "so-1", "", ready), kSO("ghost", "so-2", "no-such-auth", failing)},
		KindHPAs:          {kHPA("keda-hpa-web", "so-1", hpaAmbig...)},
		KindTriggerAuths:  {}, KindClusterTriggerAuths: {},
		KindDeployments: {&ProjectedDeployment{Deployment: appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "web"}, Status: appsv1.DeploymentStatus{Replicas: 2}}}},
	}
	complete := map[string]bool{KindScaledObjects: true, KindHPAs: true, KindTriggerAuths: true, KindClusterTriggerAuths: true, KindDeployments: true}
	res := evalScaledObjects("x")(kCapture(objs, complete))
	rows := map[string]Row[ScaledObjectRow]{}
	for _, r := range res.Rows.(RowSet[ScaledObjectRow]).Rows {
		rows[r.Data.ScaledObject.Name] = r
	}
	if r := rows["web"]; r.Eval.Role != health.Fault || r.Data.HPA == nil || r.Data.HPA.Name != "keda-hpa-web" {
		t.Fatalf("web: %v %+v", r.Eval.Role, r.Data.HPA)
	}
	if r := rows["ghost"]; r.Eval.Label != "TriggerAuthentication no-such-auth does not exist" {
		t.Fatalf("ghost: %q", r.Eval.Label)
	}
	// an incomplete authentication read proves nothing missing
	complete[KindTriggerAuths] = false
	res = evalScaledObjects("x")(kCapture(objs, complete))
	for _, r := range res.Rows.(RowSet[ScaledObjectRow]).Rows {
		if r.Data.ScaledObject.Name == "ghost" && r.Eval.Label != "ScaledObjectCheckFailed" {
			t.Fatalf("an incomplete read proved an absence: %q", r.Eval.Label)
		}
	}
	// a stale HPA: dated, never current
	healthy := map[string][]any{KindScaledObjects: {kSO("web", "so-1", "", ready, keda.Cond{Type: "HPAActive", Status: "False", Reason: "FailedGetExternalMetric"})},
		KindHPAs: {kHPA("keda-hpa-web", "so-1", hpaHealthy...)}}
	c := kCapture(healthy, map[string]bool{KindScaledObjects: true})
	c.AsOf[KindHPAs] = kNow.Add(-5 * time.Minute)
	r := evalScaledObjects("x")(c).Rows.(RowSet[ScaledObjectRow]).Rows[0]
	if r.Eval.Role != health.Unknown || r.Eval.Label != "HPA health not established" {
		t.Fatalf("stale positive against KEDA's False: %v %q (%s)", r.Eval.Role, r.Eval.Label, r.Eval.Reason)
	}
}

// S9 §6.2: the authentications are fact rows; their users come from the
// scalers' specs, and a cut trigger list is never "unused".
func TestEvalKEDAAuths(t *testing.T) {
	ta := &keda.Auth{Meta: keda.Meta{Namespace: "team", Name: "ta", UID: "ta-1"}}
	cta := &keda.Auth{Meta: keda.Meta{Name: "shared", UID: "cta-1"}, Cluster: true}
	user := kSO("web", "so-1", "ta")
	user.Triggers = append(user.Triggers, keda.Trigger{Type: "prometheus", AuthName: "shared", AuthKind: keda.KindClusterTriggerAuth})
	cut := kSO("big", "so-2", "")
	cut.TriggersCut = true
	objs := map[string][]any{KindTriggerAuths: {ta}, KindClusterTriggerAuths: {cta}, KindScaledObjects: {user, cut}, KindScaledJobs: {}}
	res := evalKEDAAuths(kCapture(objs, map[string]bool{KindTriggerAuths: true, KindClusterTriggerAuths: true, KindScaledObjects: true, KindScaledJobs: true}))
	if *res.Evidence != 2 {
		t.Fatalf("evidence: %d", *res.Evidence)
	}
	for _, r := range res.Rows.(RowSet[AuthRow]).Rows {
		if !r.Data.FactOnly() || len(r.Data.Users) != 1 || r.Data.Users[0] != "ScaledObject team/web" || r.Data.UsersComplete {
			t.Fatalf("%s: %+v", r.Data.Auth.Name, r.Data)
		}
	}
}

// C3: the HPA kind's two consumers; the standalone part is always on.
func TestSharedHPAKindConsumers(t *testing.T) {
	k := SharedHPAKind(NativeDeps{})
	if k.Name != KindHPAs || len(k.Consumers) != 2 || k.Consumers[0].Name != ConsumerHPAs || k.Consumers[1].Name != ConsumerKEDA || k.Budget != hpa.Budget {
		t.Fatalf("%+v", k)
	}
}
