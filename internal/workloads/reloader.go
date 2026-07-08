package workloads

import (
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// ReloadTargets is the set of ConfigMaps/Secrets a workload reloads on when they
// change, declared via Stakater Reloader annotations on the workload itself. Nil
// when the workload carries no reload annotation (the vast majority). This is the
// FORWARD complement of the reverse "who reloads on this ConfigMap" drill-in; it
// declares intent only - it does not prove Reloader is installed (capability
// detection answers that separately).
type ReloadTargets struct {
	Auto       bool     // reloader.stakater.com/auto: reload on ANY mounted CM/Secret
	ConfigMaps []string // sorted, de-duped
	Secrets    []string // sorted, de-duped
}

const (
	reloaderAutoKey      = "reloader.stakater.com/auto"
	reloaderConfigMapKey = "configmap.reloader.stakater.com/reload"
	reloaderSecretKey    = "secret.reloader.stakater.com/reload"
)

// reloadTargets derives a workload's reload targets from its own annotations and
// pod template. Explicit mode uses the comma-separated name lists; auto mode adds
// every ConfigMap/Secret the pod template mounts (volumes incl. projected
// sources, envFrom, valueFrom) - the same surfaces the reverse lookup checks.
// Search mode is deliberately out of scope: its targets depend on per-object
// match labels that cannot be resolved from the workload alone, so claiming them
// here would overstate. Pure; returns nil when no reload annotation is present.
func reloadTargets(ann map[string]string, spec corev1.PodSpec) *ReloadTargets {
	auto := ann[reloaderAutoKey] == "true"
	cmSet := toSet(splitNames(ann[reloaderConfigMapKey]))
	secSet := toSet(splitNames(ann[reloaderSecretKey]))
	if !auto && len(cmSet) == 0 && len(secSet) == 0 {
		return nil
	}
	if auto {
		for _, n := range mountedNames(spec, "ConfigMap") {
			cmSet[n] = struct{}{}
		}
		for _, n := range mountedNames(spec, "Secret") {
			secSet[n] = struct{}{}
		}
	}
	return &ReloadTargets{Auto: auto, ConfigMaps: sortedKeys(cmSet), Secrets: sortedKeys(secSet)}
}

func splitNames(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, n := range strings.Split(v, ",") {
		if t := strings.TrimSpace(n); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func toSet(xs []string) map[string]struct{} {
	s := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		s[x] = struct{}{}
	}
	return s
}

func sortedKeys(s map[string]struct{}) []string {
	delete(s, "")
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mountedNames returns the names of the given kind ("ConfigMap"|"Secret") the pod
// template references through a volume (incl. a projected source), an envFrom, or
// a valueFrom key ref.
func mountedNames(spec corev1.PodSpec, kind string) []string {
	set := map[string]struct{}{}
	for _, v := range spec.Volumes {
		if kind == "ConfigMap" && v.ConfigMap != nil {
			set[v.ConfigMap.Name] = struct{}{}
		}
		if kind == "Secret" && v.Secret != nil {
			set[v.Secret.SecretName] = struct{}{}
		}
		if v.Projected != nil {
			for _, src := range v.Projected.Sources {
				if kind == "ConfigMap" && src.ConfigMap != nil {
					set[src.ConfigMap.Name] = struct{}{}
				}
				if kind == "Secret" && src.Secret != nil {
					set[src.Secret.Name] = struct{}{}
				}
			}
		}
	}
	containers := append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...)
	for i := range containers {
		ct := &containers[i]
		for _, ef := range ct.EnvFrom {
			if kind == "ConfigMap" && ef.ConfigMapRef != nil {
				set[ef.ConfigMapRef.Name] = struct{}{}
			}
			if kind == "Secret" && ef.SecretRef != nil {
				set[ef.SecretRef.Name] = struct{}{}
			}
		}
		for _, e := range ct.Env {
			if e.ValueFrom == nil {
				continue
			}
			if kind == "ConfigMap" && e.ValueFrom.ConfigMapKeyRef != nil {
				set[e.ValueFrom.ConfigMapKeyRef.Name] = struct{}{}
			}
			if kind == "Secret" && e.ValueFrom.SecretKeyRef != nil {
				set[e.ValueFrom.SecretKeyRef.Name] = struct{}{}
			}
		}
	}
	return sortedKeys(set)
}
