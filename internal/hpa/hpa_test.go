package hpa

import (
	"strings"
	"testing"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/health"
)

var now = time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func raw(name string, conds ...autoscalingv2.HorizontalPodAutoscalerCondition) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: name, UID: types.UID("u-" + name), Generation: 2,
			CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{MinReplicas: ptr(int32(1)), MaxReplicas: 3,
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "web"},
			Metrics:        []autoscalingv2.MetricSpec{{Type: autoscalingv2.ResourceMetricSourceType}}},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 2, DesiredReplicas: 2, Conditions: conds},
	}
}

func cond(t, st, reason, msg string) autoscalingv2.HorizontalPodAutoscalerCondition {
	return autoscalingv2.HorizontalPodAutoscalerCondition{Type: autoscalingv2.HorizontalPodAutoscalerConditionType(t),
		Status: corev1.ConditionStatus(st), Reason: reason, Message: msg, LastTransitionTime: metav1.NewTime(now.Add(-time.Minute))}
}

func project(t *testing.T, h *autoscalingv2.HorizontalPodAutoscaler) *HPA {
	t.Helper()
	v, err := Project(h)
	if err != nil {
		t.Fatal(err)
	}
	return v.(*HPA)
}

var healthy = []autoscalingv2.HorizontalPodAutoscalerCondition{
	cond(CondAbleToScale, "True", "ReadyForNewScale", ""),
	cond(CondScalingActive, "True", "ValidMetricFound", ""),
	cond(CondScalingLimited, "False", "DesiredWithinRange", ""),
}

// §4: each rule, first match wins (lab finding 14 for the reasons).
func TestRule(t *testing.T) {
	for _, tc := range []struct {
		name   string
		h      func() *autoscalingv2.HorizontalPodAutoscaler
		others []string
		role   health.Kind
		in     bool
		label  string
	}{
		{"no conditions within the grace", func() *autoscalingv2.HorizontalPodAutoscaler {
			h := raw("new")
			h.CreationTimestamp = metav1.NewTime(now.Add(-10 * time.Second))
			return h
		}, nil, health.Progressing, false, "Waiting for the HPA controller"},
		{"no conditions past the grace", func() *autoscalingv2.HorizontalPodAutoscaler { return raw("quiet") }, nil, health.Unknown, false, "Not reported"},
		{"a missing target", func() *autoscalingv2.HorizontalPodAutoscaler {
			return raw("gone", cond(CondAbleToScale, "False", "FailedGetScale", `deployments.apps "web" not found`))
		}, nil, health.Fault, true, "Cannot scale"},
		{"no metrics source", func() *autoscalingv2.HorizontalPodAutoscaler {
			return raw("cpu", cond(CondAbleToScale, "True", "SucceededGetScale", ""), cond(CondScalingActive, "False", "FailedGetResourceMetric", "unable to fetch metrics"))
		}, nil, health.Fault, true, "Cannot compute replicas"},
		{"two autoscalers", func() *autoscalingv2.HorizontalPodAutoscaler {
			return raw("twice", cond(CondAbleToScale, "True", "SucceededGetScale", ""), cond(CondScalingActive, "False", ReasonAmbiguous, "pods by selector app=web are controlled by more than one HPA"))
		}, []string{"team/other (HPA)"}, health.Fault, true, "Cannot compute replicas"},
		{"scaled to zero", func() *autoscalingv2.HorizontalPodAutoscaler {
			return raw("zero", cond(CondAbleToScale, "True", "SucceededGetScale", ""), cond(CondScalingActive, "False", ReasonScalingOff, "scaling is disabled since the replica count of the target is zero"))
		}, nil, health.OK, false, "Scaling disabled: target at 0"},
		{"a reported lag", func() *autoscalingv2.HorizontalPodAutoscaler {
			h := raw("lag", healthy...)
			h.Status.ObservedGeneration = ptr(int64(1))
			return h
		}, nil, health.Progressing, false, "Syncing…"},
		{"a fault stands through a lag", func() *autoscalingv2.HorizontalPodAutoscaler {
			h := raw("lagfail", cond(CondAbleToScale, "False", "FailedGetScale", ""))
			h.Status.ObservedGeneration = ptr(int64(1))
			return h
		}, nil, health.Fault, true, "Cannot scale"},
		{"no observedGeneration (an older controller) is never Syncing", func() *autoscalingv2.HorizontalPodAutoscaler { return raw("old", healthy...) }, nil, health.OK, false, "2 replicas (min 1, max 3, wants 2)"},
		{"an explicit zero is never Syncing", func() *autoscalingv2.HorizontalPodAutoscaler {
			h := raw("zero-gen", healthy...)
			h.Status.ObservedGeneration = ptr(int64(0))
			return h
		}, nil, health.OK, false, "2 replicas (min 1, max 3, wants 2)"},
		{"at maximum: a capacity fact, never attention", func() *autoscalingv2.HorizontalPodAutoscaler {
			h := raw("max", cond(CondAbleToScale, "True", "ReadyForNewScale", ""), cond(CondScalingActive, "True", "ValidMetricFound", ""), cond(CondScalingLimited, "True", ReasonTooMany, "the desired replica count is more than the maximum replica count"))
			h.Status.DesiredReplicas = 5
			return h
		}, nil, health.OK, false, "2 replicas (min 1, max 3, wants 5); at maximum, wants 5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := Rule(project(t, tc.h()), now, tc.others)
			if ev.Role != tc.role || ev.Included != tc.in || ev.Label != tc.label {
				t.Fatalf("got %v included %v %q (%s)", ev.Role, ev.Included, ev.Label, ev.Reason)
			}
			if tc.others != nil && !strings.Contains(ev.Reason, "also scaling this target: team/other (HPA)") {
				t.Fatalf("the other autoscaler is not named: %q", ev.Reason)
			}
			if ev.Role == health.Fault && ev.Cond == nil {
				t.Fatal("a condition-decided fault cites no condition time")
			}
		})
	}
}

// §10: the projection keeps presence, the controller reference, and caps
// with flags; messages go through the URL rule before the clip.
func TestProject(t *testing.T) {
	h := raw("p")
	h.OwnerReferences = []metav1.OwnerReference{
		{APIVersion: "v1", Kind: "ConfigMap", Name: "not-controller", UID: "x"},
		{APIVersion: "keda.sh/v1alpha1", Kind: "ScaledObject", Name: "web", UID: "so-1", Controller: ptr(true)},
	}
	for i := 0; i < 10; i++ {
		h.Status.Conditions = append(h.Status.Conditions, cond("C"+string(rune('a'+i)), "True", "R", "see https://user:secret@example.com/x "+strings.Repeat("m", 600)))
	}
	p := project(t, h)
	if o, ok := p.KEDAOwner(); !ok || o.UID != "so-1" || o.Name != "web" {
		t.Fatalf("controller reference: %+v", p.Controller)
	}
	if len(p.Conds) != maxConds || !p.CondsCut || len(p.Conds[0].Message) > maxMessage+len("…") || strings.Contains(p.Conds[0].Message, "secret") {
		t.Fatalf("conditions: %d cut %v %q", len(p.Conds), p.CondsCut, p.Conds[0].Message[:60])
	}
	if p.ObservedGeneration != nil {
		t.Fatal("an absent observedGeneration became a value")
	}
	// the worst shape fits MaxObject: every field at its cap
	worst := raw(strings.Repeat("n", 253))
	worst.Namespace = strings.Repeat("s", 63)
	worst.OwnerReferences = []metav1.OwnerReference{{APIVersion: "keda.sh/v1alpha1", Kind: "ScaledObject", Name: strings.Repeat("o", 253), UID: "0123456789abcdef0123456789abcdef0123", Controller: ptr(true)}}
	worst.Spec.ScaleTargetRef = autoscalingv2.CrossVersionObjectReference{APIVersion: strings.Repeat("g", 253) + "/v1", Kind: strings.Repeat("K", 63), Name: strings.Repeat("t", 253)}
	for i := 0; i < 12; i++ {
		worst.Spec.Metrics = append(worst.Spec.Metrics, autoscalingv2.MetricSpec{Type: autoscalingv2.ContainerResourceMetricSourceType})
		worst.Status.Conditions = append(worst.Status.Conditions, cond(strings.Repeat("T", 316), "Unknown", strings.Repeat("R", 1024), strings.Repeat("\x00<", 600)))
	}
	if w := project(t, worst); w.Size > MaxObject {
		t.Fatalf("the worst projection is %d bytes, over MaxObject %d", w.Size, MaxObject)
	} else {
		t.Logf("worst projection %d bytes; this one %d", w.Size, p.Size)
	}
	if !p.Scales("team", "apps", "Deployment", "web") || p.Scales("team", "argoproj.io", "Deployment", "web") || p.Scales("other", "apps", "Deployment", "web") {
		t.Fatal("the target join is not by namespace, group, kind and name")
	}
	if _, ok := project(t, raw("plain")).KEDAOwner(); ok {
		t.Fatal("a plain HPA read as KEDA's")
	}
}
