package workloads

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestReloadTargets_None(t *testing.T) {
	if rt := reloadTargets(nil, corev1.PodSpec{}); rt != nil {
		t.Fatalf("no annotation must yield nil, got %+v", rt)
	}
	// A search-only annotation is out of scope for the forward view (targets
	// depend on per-object match labels), so it yields nil rather than overstating.
	ann := map[string]string{"reloader.stakater.com/search": "true"}
	if rt := reloadTargets(ann, corev1.PodSpec{Volumes: []corev1.Volume{{
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cm"}}},
	}}}); rt != nil {
		t.Fatalf("search-only must yield nil (not overstate), got %+v", rt)
	}
}

func TestReloadTargets_Explicit(t *testing.T) {
	ann := map[string]string{
		"configmap.reloader.stakater.com/reload": "b-cm, a-cm",
		"secret.reloader.stakater.com/reload":    "s1",
	}
	// Explicit mode ignores mounts entirely - only the named objects.
	rt := reloadTargets(ann, corev1.PodSpec{Volumes: []corev1.Volume{{
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "mounted-only"}}},
	}}})
	if rt == nil || rt.Auto {
		t.Fatalf("want non-nil explicit (Auto=false), got %+v", rt)
	}
	if !reflect.DeepEqual(rt.ConfigMaps, []string{"a-cm", "b-cm"}) { // sorted, trimmed
		t.Fatalf("configmaps: want [a-cm b-cm], got %v", rt.ConfigMaps)
	}
	if !reflect.DeepEqual(rt.Secrets, []string{"s1"}) {
		t.Fatalf("secrets: want [s1], got %v", rt.Secrets)
	}
}

func TestReloadTargets_AutoEnumeratesMounts(t *testing.T) {
	ann := map[string]string{"reloader.stakater.com/auto": "true"}
	spec := corev1.PodSpec{
		Volumes: []corev1.Volume{
			{VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "vol-cm"}}}},
			{VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{
				{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "proj-sec"}}},
			}}}},
		},
		Containers: []corev1.Container{{
			EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "env-cm"}}}},
			Env: []corev1.EnvVar{{ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "key-sec"}},
			}}},
		}},
	}
	rt := reloadTargets(ann, spec)
	if rt == nil || !rt.Auto {
		t.Fatalf("want Auto=true, got %+v", rt)
	}
	if !reflect.DeepEqual(rt.ConfigMaps, []string{"env-cm", "vol-cm"}) {
		t.Fatalf("auto configmaps: want [env-cm vol-cm], got %v", rt.ConfigMaps)
	}
	if !reflect.DeepEqual(rt.Secrets, []string{"key-sec", "proj-sec"}) {
		t.Fatalf("auto secrets: want [key-sec proj-sec], got %v", rt.Secrets)
	}
}

func TestReloadTargets_ExplicitPlusAutoMerges(t *testing.T) {
	ann := map[string]string{
		"reloader.stakater.com/auto":             "true",
		"configmap.reloader.stakater.com/reload": "named-cm",
	}
	spec := corev1.PodSpec{Volumes: []corev1.Volume{{
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "named-cm"}}},
	}}}
	rt := reloadTargets(ann, spec)
	// The explicitly-named CM that is also mounted must appear once (de-duped).
	if !reflect.DeepEqual(rt.ConfigMaps, []string{"named-cm"}) {
		t.Fatalf("want de-duped [named-cm], got %v", rt.ConfigMaps)
	}
}
