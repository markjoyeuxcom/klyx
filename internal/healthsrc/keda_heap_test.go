package healthsrc

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/hpa"
	"github.com/moomora/klyx/internal/keda"
)

// Coverage S9 §10's measurement: each KEDA kind and the HPA kind filled to
// its budget with projections - a typical shape and one at every cap - and
// the heap they hold reported beside the encoded bytes the budget counts.
// Run with
//
//	go test ./internal/healthsrc -run TestKEDAHeapCalibration -v
//
// and record the logged figures in the S9 note's As built.

func kedaScalerObject(kind string, i int, worst bool) *unstructured.Unstructured {
	labels := map[string]any{"app": "web", "kustomize.toolkit.fluxcd.io/name": "apps", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}
	triggers := []any{
		map[string]any{"type": "prometheus", "metadata": map[string]any{"serverAddress": "http://prometheus.monitoring.svc:9090", "query": "sum(rate(http_requests_total[1m]))", "threshold": "10"},
			"authenticationRef": map[string]any{"name": "prom-auth"}},
		map[string]any{"type": "cpu", "metricType": "Utilization", "metadata": map[string]any{"value": "80"}},
	}
	conds := []any{
		map[string]any{"type": "Ready", "status": "True", "reason": "ScaledObjectReady", "message": "ScaledObject is defined correctly and is ready for scaling"},
		map[string]any{"type": "Active", "status": "True", "reason": "ScalerActive", "message": "Scaling is performed because triggers are active"},
		map[string]any{"type": "Fallback", "status": "False", "reason": "NoFallbackFound", "message": "No fallbacks are active on this scaled object"},
		map[string]any{"type": "Paused", "status": "False"},
		map[string]any{"type": "HPAActive", "status": "True", "reason": "HPAActive", "message": "HPA is actively scaling"},
	}
	health := map[string]any{"s0-prometheus": map[string]any{"numberOfFailures": int64(0), "status": "Happy"}}
	activity := map[string]any{"s0-prometheus": map[string]any{"isActive": true}, "s1-cpu": map[string]any{"isActive": false}}
	if worst {
		for j := 0; j < 16; j++ {
			labels[fmt.Sprintf("team.example.com/label-%02d-%s", j, strings.Repeat("k", 30))] = strings.Repeat("v", 63)
		}
		triggers = nil
		for j := 0; j < 20; j++ {
			triggers = append(triggers, map[string]any{"type": "prometheus" + strings.Repeat("x", 118), "name": strings.Repeat("n", 128), "metricType": "AverageValue" + strings.Repeat("m", 116),
				"metadata":          map[string]any{"query": strings.Repeat("q", 4096)},
				"authenticationRef": map[string]any{"name": strings.Repeat("a", 253), "kind": "ClusterTriggerAuthentication"}})
		}
		conds = nil
		for j := 0; j < 10; j++ {
			conds = append(conds, map[string]any{"type": fmt.Sprintf("Cond%02d", j) + strings.Repeat("t", 120), "status": "Unknown",
				"reason": strings.Repeat("r", 128), "message": strings.Repeat("m", 2048)})
		}
		health, activity = map[string]any{}, map[string]any{}
		for j := 0; j < 20; j++ {
			key := fmt.Sprintf("s%d-%s", j, strings.Repeat("k", 124))
			health[key] = map[string]any{"numberOfFailures": int64(j), "status": "Failing" + strings.Repeat("s", 121)}
			activity[key] = map[string]any{"isActive": true}
		}
	}
	spec := map[string]any{"scaleTargetRef": map[string]any{"name": fmt.Sprintf("web-%06d", i)}, "minReplicaCount": int64(1), "maxReplicaCount": int64(10),
		"fallback": map[string]any{"failureThreshold": int64(3), "replicas": int64(2)}, "triggers": triggers}
	status := map[string]any{"conditions": conds, "hpaName": fmt.Sprintf("keda-hpa-web-%06d", i), "health": health, "triggersActivity": activity,
		"scaleTargetGVKR": map[string]any{"group": "apps", "version": "v1", "kind": "Deployment", "resource": "deployments"}, "lastActiveTime": "2026-10-19T10:00:00Z"}
	if kind == "ScaledJob" {
		spec = map[string]any{"jobTargetRef": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "job", "image": "busybox"}}}}},
			"successfulJobsHistoryLimit": int64(5), "failedJobsHistoryLimit": int64(5), "triggers": triggers}
		status = map[string]any{"conditions": conds, "lastActiveTime": "2026-10-19T10:00:00Z"}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "keda.sh/v1alpha1", "kind": kind,
		"metadata": map[string]any{"name": fmt.Sprintf("web-%06d", i), "namespace": fmt.Sprintf("team-%03d", i%200), "uid": fmt.Sprintf("uid-%06d", i),
			"generation": int64(2), "creationTimestamp": "2026-10-19T10:00:00Z", "resourceVersion": "123456", "labels": labels,
			"annotations": map[string]any{keda.AnnPaused: "false", "kubectl.kubernetes.io/last-applied-configuration": strings.Repeat("x", 2048)}},
		"spec": spec, "status": status,
	}}
}

// kedaAuthObject: a typical authentication, a large one (16 references of
// realistic length per source, Vault and a pod identity), or one at every
// cap (refs at full length).
func kedaAuthObject(i int, cluster bool, level int) *unstructured.Unstructured {
	spec := map[string]any{"secretTargetRef": []any{map[string]any{"parameter": "bearerToken", "name": "prom-token", "key": "token"}}}
	if level > 0 {
		name, key, param := 40, 24, 24
		if level > 1 {
			name, key, param = 253, 253, 128
		}
		refs := func(nameKey string) []any {
			var out []any
			for j := 0; j < 20; j++ {
				out = append(out, map[string]any{"parameter": strings.Repeat("p", param), nameKey: fmt.Sprintf("%02d%s", j, strings.Repeat("n", name-2)), "key": strings.Repeat("k", key)})
			}
			return out
		}
		spec = map[string]any{"secretTargetRef": refs("name"), "configMapTargetRef": refs("name"), "env": refs("name"), "boundServiceAccountToken": refs("serviceAccountName"),
			"hashiCorpVault": map[string]any{"address": "https://vault.example.com:8200/" + strings.Repeat("v", 512), "authentication": "token",
				"credential": map[string]any{"token": strings.Repeat("T", 512)}},
			"podIdentity": map[string]any{"provider": "aws", "identityOwner": "workload", "roleArn": strings.Repeat("r", 512)}}
	}
	meta := map[string]any{"name": fmt.Sprintf("auth-%06d", i), "uid": fmt.Sprintf("uid-%06d", i), "generation": int64(1), "creationTimestamp": "2026-10-19T10:00:00Z"}
	if !cluster {
		meta["namespace"] = fmt.Sprintf("team-%03d", i%200)
	}
	kind := "TriggerAuthentication"
	if cluster {
		kind = "ClusterTriggerAuthentication"
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": kind, "metadata": meta, "spec": spec}}
}

func calibrationHPA(i int, worst bool) *autoscalingv2.HorizontalPodAutoscaler {
	ctrl := true
	min, og := int32(1), int64(2)
	at := metav1.NewTime(time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC))
	h := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("keda-hpa-web-%06d", i), Namespace: fmt.Sprintf("team-%03d", i%200), UID: types.UID(fmt.Sprintf("uid-%06d", i)), Generation: 2,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "keda.sh/v1alpha1", Kind: "ScaledObject", Name: fmt.Sprintf("web-%06d", i), UID: types.UID(fmt.Sprintf("so-%06d", i)), Controller: &ctrl}}},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: fmt.Sprintf("web-%06d", i)},
			MinReplicas: &min, MaxReplicas: 10, Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ExternalMetricSourceType}}},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{ObservedGeneration: &og, CurrentReplicas: 3, DesiredReplicas: 3, LastScaleTime: &at,
			Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
				{Type: autoscalingv2.AbleToScale, Status: corev1.ConditionTrue, Reason: "ReadyForNewScale", Message: "recommended size matches current size", LastTransitionTime: at},
				{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionTrue, Reason: "ValidMetricFound", Message: "the HPA was able to successfully calculate a replica count from external metric s0-prometheus", LastTransitionTime: at},
				{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionFalse, Reason: "DesiredWithinRange", Message: "the desired count is within the acceptable range", LastTransitionTime: at},
			}},
	}
	if worst {
		h.Spec.ScaleTargetRef = autoscalingv2.CrossVersionObjectReference{APIVersion: strings.Repeat("g", 200) + "/v1", Kind: strings.Repeat("K", 63), Name: strings.Repeat("n", 253)}
		h.Spec.Metrics = nil
		for j := 0; j < 12; j++ {
			h.Spec.Metrics = append(h.Spec.Metrics, autoscalingv2.MetricSpec{Type: autoscalingv2.ExternalMetricSourceType})
		}
		h.Status.Conditions = nil
		for j := 0; j < 8; j++ {
			h.Status.Conditions = append(h.Status.Conditions, autoscalingv2.HorizontalPodAutoscalerCondition{Type: autoscalingv2.HorizontalPodAutoscalerConditionType(fmt.Sprintf("C%d", j) + strings.Repeat("t", 126)),
				Status: corev1.ConditionUnknown, Reason: strings.Repeat("r", 128), Message: strings.Repeat("m", 2048), LastTransitionTime: at})
		}
	}
	return h
}

func TestKEDAHeapCalibration(t *testing.T) {
	if testing.Short() {
		t.Skip("calibration")
	}
	// level 0 is typical; the others are named by shapes. bounded: MaxObject
	// bounds the kind's cap product, so every shape must fit (the exact
	// products are pinned in internal/keda and internal/hpa); an
	// authentication's every-cap shape is counted, not retained.
	type kindCase struct {
		name           string
		budget, maxObj int
		bounded        bool
		project        func(obj any) (any, error)
		size           func(any) int
		make           func(i, level int) any
		shapes         []string
	}
	cases := []kindCase{
		{"scaledobjects", keda.BudgetScaledObjects, keda.MaxObject, true, keda.Project(keda.ResourceScaledObjects), keda.SizeOf,
			func(i, l int) any { return kedaScalerObject("ScaledObject", i, l > 0) }, []string{"typical", "at the caps"}},
		{"scaledjobs", keda.BudgetScaledJobs, keda.MaxObject, true, keda.Project(keda.ResourceScaledJobs), keda.SizeOf,
			func(i, l int) any { return kedaScalerObject("ScaledJob", i, l > 0) }, []string{"typical", "at the caps"}},
		{"triggerauthentications", keda.BudgetTriggerAuths, keda.MaxAuthObject, false, keda.Project(keda.ResourceTriggerAuths), keda.SizeOf,
			func(i, l int) any { return kedaAuthObject(i, false, l) }, []string{"typical", "large", "every cap"}},
		{"clustertriggerauthentications", keda.BudgetClusterAuths, keda.MaxAuthObject, false, keda.Project(keda.ResourceClusterTriggerAuths), keda.SizeOf,
			func(i, l int) any { return kedaAuthObject(i, true, l) }, []string{"typical", "large", "every cap"}},
		{"horizontalpodautoscalers", hpa.Budget, hpa.MaxObject, true, hpa.Project, hpa.SizeOf,
			func(i, l int) any { return calibrationHPA(i, l > 0) }, []string{"typical", "at the caps"}},
	}
	total := 0
	for _, kc := range cases {
		total += kc.budget
		for level, shape := range kc.shapes {
			// the sources are built before the baseline, so only the
			// projections are measured
			first, err := kc.project(kc.make(0, level))
			if err != nil {
				t.Fatal(err)
			}
			if n := kc.size(first); n > kc.maxObj {
				if kc.bounded {
					t.Fatalf("%s %s: one object is %d bytes, over MaxObject %d", kc.name, shape, n, kc.maxObj)
				}
				t.Logf("%-30s %-9s one object is %d bytes, over MaxObject %d: counted, not retained", kc.name, shape, n, kc.maxObj)
				continue
			}
			// each source is built and dropped inside the measurement: what
			// the projection shares with it is retained, the rest is garbage
			before := heapNow()
			var held []any
			encoded, per := 0, 0
			for i := 0; ; i++ {
				v, err := kc.project(kc.make(i, level))
				if err != nil {
					t.Fatal(err)
				}
				n := kc.size(v)
				if encoded+n > kc.budget {
					break
				}
				held, encoded, per = append(held, v), encoded+n, n
			}
			retained := heapNow() - before
			ratio := float64(retained) / float64(encoded)
			t.Logf("%-30s %-9s %5d objects, %5d encoded bytes each, %7d encoded of %7d budget, heap %8d bytes, %.2f heap per encoded byte",
				kc.name, shape, len(held), per, encoded, kc.budget, retained, ratio)
			if ratio <= 0 || ratio > 10 {
				t.Errorf("%s %s: an implausible ratio %.2f", kc.name, shape, ratio)
			}
			runtime.KeepAlive(held)
		}
	}
	t.Logf("encoded admission budget per cluster: %d bytes (%.2f MiB); the HPAs' %d on every cluster", total, float64(total)/(1<<20), hpa.Budget)
}
