package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/hpa"
	"github.com/moomora/klyx/internal/keda"
)

func kyes() *bool { b := true; return &b }

// kedaFixture builds a connection whose KEDA operator runs image, with a
// target Deployment running as ServiceAccount wl, and the given KEDA
// objects and Secret metadata.
func kedaFixture(t *testing.T, image string, env []corev1.EnvVar, objs []runtime.Object, secrets ...string) *ClusterConn {
	t.Helper()
	typed := typedfake.NewSimpleClientset(
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "keda", Name: "keda-operator-1", UID: "rs-1",
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "keda-operator", UID: "dep-1", Controller: kyes()}}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "keda", Name: "keda-operator-1-a", UID: "pod-1",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "keda-operator-1", UID: "rs-1", Controller: kyes()}}},
			Spec:   corev1.PodSpec{ServiceAccountName: "keda-operator", Containers: []corev1.Container{{Name: "keda-operator", Image: image, Env: env}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "web"},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{ServiceAccountName: "wl"}}}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "keda", Name: "keda-operator"}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "wl", Annotations: map[string]string{"eks.amazonaws.com/role-arn": "arn:aws:iam::111122223333:role/r"}}},
	)
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	var metas []runtime.Object
	for _, s := range secrets {
		ns, name, _ := strings.Cut(s, "/")
		metas = append(metas, &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}})
	}
	meta := metafake.NewSimpleMetadataClient(scheme, metas...)
	listKinds := map[schema.GroupVersionResource]string{kedaGVR("scaledobjects"): "ScaledObjectList", kedaGVR("scaledjobs"): "ScaledJobList",
		kedaGVR("triggerauthentications"): "TriggerAuthenticationList", kedaGVR("clustertriggerauthentications"): "ClusterTriggerAuthenticationList"}
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
	c := NewClusterConn("x", typed, meta, dyn, nil, clock.Real{}, config.MetricsConfig{})
	c.ctx = context.Background()
	c.caps.KEDA = capability.KEDACapability{Present: true, OperatorsRead: true, Operators: []capability.KEDAOperator{{Namespace: "keda", Name: "keda-operator", UID: "dep-1"}}}
	return c
}

func kedaObj(kind, ns, name, uid string, spec map[string]any, ann map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": kind,
		"metadata": map[string]any{"name": name, "uid": uid}, "spec": spec}}
	if ns != "" {
		u.SetNamespace(ns)
	}
	if ann != nil {
		u.SetAnnotations(ann)
	}
	return u
}

func authRef(name, kind string) map[string]any {
	r := map[string]any{"name": name}
	if kind != "" {
		r["kind"] = kind
	}
	return map[string]any{"type": "prometheus", "authenticationRef": r}
}

// Coverage S9 §6: the handling operator from its running pod, each
// authentication's existence and visibility, the referenced Secrets'
// existence at KEDA's namespace, and the subject.
func TestKEDAInspect(t *testing.T) {
	so := kedaObj("ScaledObject", "team", "web", "so-1", map[string]any{"scaleTargetRef": map[string]any{"name": "web"},
		"triggers": []any{authRef("ta", ""), authRef("ghost", ""), authRef("aws", ""), authRef("shared", "ClusterTriggerAuthentication")}}, nil)
	ta := kedaObj("TriggerAuthentication", "team", "ta", "ta-1", map[string]any{"secretTargetRef": []any{
		map[string]any{"parameter": "p", "name": "tok", "key": "k"}, map[string]any{"parameter": "q", "name": "gone", "key": "k"}}}, nil)
	aws := kedaObj("TriggerAuthentication", "team", "aws", "ta-2", map[string]any{"podIdentity": map[string]any{"provider": "aws", "identityOwner": "workload"}}, nil)
	cta := kedaObj("ClusterTriggerAuthentication", "", "shared", "cta-1", map[string]any{"secretTargetRef": []any{map[string]any{"parameter": "p", "name": "lab", "key": "k"}}}, nil)
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", []corev1.EnvVar{{Name: "WATCH_NAMESPACE", Value: ""}}, []runtime.Object{so, ta, aws, cta}, "team/tok", "keda/lab")
	in, err := c.KEDAInspect(context.Background(), "scaledobjects", "team", "web", "so-1")
	if err != nil {
		t.Fatal(err)
	}
	if in.Handler != "keda/keda-operator" || in.Version != "2.21.0" {
		t.Fatalf("handler: %+v", in)
	}
	got := map[string]KEDAAuthCheck{}
	for _, a := range in.Auths {
		got[a.Name] = a
	}
	if got["ghost"].State != "not found" || got["ta"].State != "exists" || got["ta"].Visible["keda/keda-operator"] != "visible" {
		t.Fatalf("auths: %+v", in.Auths)
	}
	refs := map[string]string{}
	for _, r := range got["ta"].Refs {
		refs[r.Name] = r.State + "@" + r.Namespace
	}
	if refs["tok"] != "exists@team" || refs["gone"] != "not found@team" {
		t.Fatalf("refs: %v", refs)
	}
	if r := got["shared"].Refs; len(r) != 1 || r[0].Namespace != "keda" || r[0].State != "exists" {
		t.Fatalf("a ClusterTriggerAuthentication's Secret is read in the operator's namespace: %+v", r)
	}
	if s := got["aws"].Subject; s == nil || s.SA == nil || s.SA.Name != "keda-operator" || s.RoleFrom == nil || s.RoleFrom.Name != "wl" || len(s.Refusals) != 0 {
		t.Fatalf("aws workload subject: %+v", s)
	}
	if _, err := c.KEDAInspect(context.Background(), "scaledobjects", "team", "web", "other-uid"); err == nil {
		t.Fatal("a replaced object was inspected")
	}
	// envFrom on the operator: the scope is unresolved, so the handler is
	// unverified and nothing is read at a guessed namespace
	c2 := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, []runtime.Object{so, ta}, "team/tok")
	pods, _ := c2.typed.CoreV1().Pods("keda").List(context.Background(), metav1.ListOptions{})
	p := pods.Items[0]
	p.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "env"}}}}
	if _, err := c2.typed.CoreV1().Pods("keda").Update(context.Background(), &p, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	in, _ = c2.KEDAInspect(context.Background(), "scaledobjects", "team", "web", "so-1")
	if in.Handler != "" || len(in.Candidates) != 1 {
		t.Fatalf("envFrom: %+v", in)
	}
	for _, a := range in.Auths {
		for _, r := range a.Refs {
			if r.State != "unverified" {
				t.Fatalf("a reference read without a resolved handler: %+v", r)
			}
		}
	}
}

// S9 §8: Pause through autoscaling.keda.sh/paused only, offered only on a
// resolved operator whose version applies it; Resume only when paused is
// the only pause annotation; UID-guarded.
func TestKEDASetPaused(t *testing.T) {
	ctx := context.Background()
	newSO := func(ann map[string]string) *unstructured.Unstructured {
		return kedaObj("ScaledObject", "team", "web", "so-1", map[string]any{"scaleTargetRef": map[string]any{"name": "web"}}, ann)
	}
	annOf := func(c *ClusterConn) map[string]string {
		u, err := c.dyn.Resource(kedaGVR("scaledobjects")).Namespace("team").Get(ctx, "web", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return u.GetAnnotations()
	}
	c := kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, []runtime.Object{newSO(nil)})
	if err := c.KEDASetPaused(ctx, "scaledobjects", "team", "web", "so-1", true); err != nil {
		t.Fatal(err)
	}
	if annOf(c)[keda.AnnPaused] != "true" {
		t.Fatalf("pause: %v", annOf(c))
	}
	if err := c.KEDASetPaused(ctx, "scaledobjects", "team", "web", "so-1", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := annOf(c)[keda.AnnPaused]; ok {
		t.Fatalf("resume left the annotation: %v", annOf(c))
	}
	// resume refused when another pause annotation holds it
	c = kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, []runtime.Object{newSO(map[string]string{keda.AnnPaused: "true", keda.AnnPausedReplicas: "1"})})
	if err := c.KEDASetPaused(ctx, "scaledobjects", "team", "web", "so-1", false); err == nil || !strings.Contains(err.Error(), "paused-replicas") {
		t.Fatalf("resume with paused-replicas: %v", err)
	}
	// 2.11 does not apply paused on a ScaledObject; a custom image's version is unknown
	for image, want := range map[string]string{"ghcr.io/kedacore/keda:2.11.2": "does not apply", "registry.example/keda:2.21.0": "unverified"} {
		c = kedaFixture(t, image, nil, []runtime.Object{newSO(nil)})
		if err := c.KEDASetPaused(ctx, "scaledobjects", "team", "web", "so-1", true); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", image, err)
		}
		if _, ok := annOf(c)[keda.AnnPaused]; ok {
			t.Fatalf("%s: a refused pause wrote", image)
		}
	}
	c = kedaFixture(t, "ghcr.io/kedacore/keda:2.21.0", nil, []runtime.Object{newSO(nil)})
	if err := c.KEDASetPaused(ctx, "scaledobjects", "team", "web", "replaced", true); !errors.Is(err, ErrReplaced) {
		t.Fatalf("a replaced object: %v", err)
	}
}

// S9 §6.4: the Jobs read pages, keeps only the ScaledJob's own Jobs by
// controller UID, and claims the newest only when it completes.
func TestKEDAJobsOf(t *testing.T) {
	job := func(name string, owner types.UID, created time.Time) *batchv1.Job {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: name, CreationTimestamp: metav1.NewTime(created),
			Labels:          map[string]string{"scaledjob.keda.sh/name": "batch"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ScaledJob", Name: "batch", UID: owner, Controller: kyes()}}}}
	}
	t0 := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	typed := typedfake.NewSimpleClientset(job("old", "sj-1", t0), job("new", "sj-1", t0.Add(time.Hour)), job("foreign", "other", t0.Add(2*time.Hour)))
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	js, err := c.KEDAJobsOf(context.Background(), "team", "batch", "sj-1")
	if err != nil || !js.Complete || js.Owned != 2 || js.Read != 3 || js.Jobs[0].Name != "new" {
		t.Fatalf("%+v %v", js, err)
	}
	// a continuation that never ends: not complete, never a global claim
	typed.PrependReactor("list", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &batchv1.JobList{ListMeta: metav1.ListMeta{Continue: "more"}, Items: []batchv1.Job{*job("x", "other", t0)}}, nil
	})
	js, err = c.KEDAJobsOf(context.Background(), "team", "batch", "sj-1")
	if err != nil || js.Complete || js.Read != kedaJobsPages || js.Owned != 0 {
		t.Fatalf("paged: %+v %v", js, err)
	}
}

// S9 §7: owned, proven unowned or unverified per axis; labels never proof.
func TestReplicaOwnership(t *testing.T) {
	plain := &hpa.HPA{Namespace: "team", Name: "plain", Target: hpa.Target{APIVersion: "apps/v1", Kind: "Deployment", Name: "web"}}
	kedaHPA := &hpa.HPA{Namespace: "team", Name: "keda-hpa-api", Target: hpa.Target{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"},
		Controller: &hpa.Owner{APIVersion: "keda.sh/v1alpha1", Kind: "ScaledObject", Name: "api", UID: "so-api"}}
	p := "true"
	pausedSO := &keda.ScaledObject{Scaler: keda.Scaler{Meta: keda.Meta{Namespace: "team", Name: "batchy", UID: "so-b"}, Pause: keda.Pause{Paused: &p}}, TargetName: "batchy"}
	api := &keda.ScaledObject{Scaler: keda.Scaler{Meta: keda.Meta{Namespace: "team", Name: "api", UID: "so-api"}}, TargetName: "api"}
	v := keda.Version{Known: true, Major: 2, Minor: 21}
	if o := replicaOwners("Deployment", "team", "web", []*hpa.HPA{plain}, nil, v); len(o) != 1 || o[0] != "HPA plain" {
		t.Fatalf("plain: %v", o)
	}
	if o := replicaOwners("Deployment", "team", "api", []*hpa.HPA{kedaHPA}, []*keda.ScaledObject{api}, v); len(o) != 1 || !strings.Contains(o[0], "KEDA ScaledObject api, through HPA keda-hpa-api") {
		t.Fatalf("keda: %v", o)
	}
	if o := replicaOwners("Deployment", "team", "batchy", nil, []*keda.ScaledObject{pausedSO}, v); len(o) != 1 || !strings.Contains(o[0], "paused and will resume control") {
		t.Fatalf("paused: %v", o)
	}
	inv := GitOpsOwnership{OwnerKind: "Application", OwnerNamespace: "argocd", OwnerName: "shop", Basis: "argo-resources"}
	if r := classifyOwnership(nil, true, true, inv); r.Autoscaler != "unowned" || r.GitOps != "owned" || r.GitOpsOwner != "Application argocd/shop" {
		t.Fatalf("argo by resources: %+v", r)
	}
	if r := classifyOwnership(nil, false, true, GitOpsOwnership{}); r.Autoscaler != "unverified" || r.GitOps != "unowned" {
		t.Fatalf("a capped HPA read: %+v", r)
	}
	if r := classifyOwnership(nil, true, true, GitOpsOwnership{Warnings: []string{"kustomizations could not be listed"}}); r.GitOps != "unverified" {
		t.Fatalf("a failing GitOps check: %+v", r)
	}
}
