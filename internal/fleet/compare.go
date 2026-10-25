package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/readfence"
	"github.com/moomora/klyx/internal/workloads"
)

// Fleet compare: the per-cluster half of "why does PRD behave differently than
// STG". One call per cluster returns the drift-relevant facts of a workload -
// images, replicas, per-container resources, the GitOps owner and its RESOLVED
// revision, plus a normalized spec hash so "something else differs" is never
// silent. The cross-cluster aggregation happens in the frontend, which already
// holds the fleet list with env/region tags.

// CompareContainer is one container's drift-relevant identity.
type CompareContainer struct {
	Name      string
	Image     string
	Resources string // compact "req cpu/mem · lim cpu/mem"; "" when unset
}

// WorkloadCompareInfo is one cluster's view of a workload for the fleet compare.
// Found=false is an honest state (the cluster does not run it), not an error.
type WorkloadCompareInfo struct {
	Found           bool
	Kind            string
	Namespace       string
	Name            string
	DesiredReplicas int32
	ReadyReplicas   int32
	Containers      []CompareContainer
	// SpecHash is a short digest of the POD SPEC (template metadata is
	// excluded: kubectl rollout restart and Reloader stamp annotations there,
	// which are not configuration drift). Equal hashes mean the pod specs are
	// identical; differing hashes with identical images and resources mean
	// OTHER pod-spec drift (env, volumes, probes, ...) exists.
	SpecHash string
	// The GitOps owner from the workload's labels, and - for Flux owners - the
	// owning object's lastAppliedRevision, resolved live (best-effort; "" when
	// unresolvable). An Argo owner reports identity only.
	OwnerKind      string
	OwnerNamespace string
	OwnerName      string
	OwnerRevision  string
	// OwnerRevisionNote says why OwnerRevision is empty for an owned
	// workload (Argo owner, Flux API not served, read failed, not applied
	// yet) - an unresolved revision is never shown as an equal one.
	OwnerRevisionNote string
}

// WorkloadCompare fetches one workload's compare facts. kind is Deployment,
// StatefulSet, or DaemonSet. NotFound returns Found=false with a nil error;
// real failures (RBAC, disconnect) return the error so a degraded column is
// never mistaken for "not present".
func (c *ClusterConn) WorkloadCompare(ctx context.Context, kind, ns, name string) (WorkloadCompareInfo, error) {
	if c.typed == nil {
		return WorkloadCompareInfo{}, fmt.Errorf("cluster connection unavailable")
	}
	info := WorkloadCompareInfo{Kind: kind, Namespace: ns, Name: name}

	var labels map[string]string
	var tmpl corev1.PodTemplateSpec
	switch kind {
	case "Deployment":
		d, err := c.typed.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return info, nil
		}
		if err != nil {
			return WorkloadCompareInfo{}, err
		}
		labels, tmpl = d.Labels, d.Spec.Template
		info.DesiredReplicas = desiredReplicasOf(d.Spec.Replicas)
		info.ReadyReplicas = d.Status.ReadyReplicas
	case "StatefulSet":
		s, err := c.typed.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return info, nil
		}
		if err != nil {
			return WorkloadCompareInfo{}, err
		}
		labels, tmpl = s.Labels, s.Spec.Template
		info.DesiredReplicas = desiredReplicasOf(s.Spec.Replicas)
		info.ReadyReplicas = s.Status.ReadyReplicas
	case "DaemonSet":
		d, err := c.typed.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return info, nil
		}
		if err != nil {
			return WorkloadCompareInfo{}, err
		}
		labels, tmpl = d.Labels, d.Spec.Template
		info.DesiredReplicas = d.Status.DesiredNumberScheduled
		info.ReadyReplicas = d.Status.NumberReady
	default:
		return WorkloadCompareInfo{}, fmt.Errorf("unsupported kind %q", kind)
	}

	info.Found = true
	for _, ct := range append(append([]corev1.Container{}, tmpl.Spec.InitContainers...), tmpl.Spec.Containers...) {
		info.Containers = append(info.Containers, CompareContainer{
			Name:      ct.Name,
			Image:     ct.Image,
			Resources: compactResources(ct.Resources),
		})
	}
	info.SpecHash = podTemplateHash(tmpl)

	if o := workloads.GitOpsOwnerFromLabels(labels); o != nil {
		info.OwnerKind, info.OwnerNamespace, info.OwnerName = o.Kind, o.Namespace, o.Name
		info.OwnerRevision, info.OwnerRevisionNote = c.fluxOwnerRevision(ctx, o)
	}
	return info, nil
}

func desiredReplicasOf(r *int32) int32 {
	if r != nil {
		return *r
	}
	return 1
}

// podTemplateHash digests the pod spec (short sha256). Typed-struct JSON
// marshaling has a fixed field order, so equal specs hash equal. Template
// metadata is deliberately excluded (restartedAt, Reloader annotations).
func podTemplateHash(tmpl corev1.PodTemplateSpec) string {
	b, err := json.Marshal(tmpl.Spec)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// compactResources renders requests/limits as "req 100m/128Mi · lim 500m/256Mi".
// Missing halves render "-"; fully unset returns "".
func compactResources(r corev1.ResourceRequirements) string {
	part := func(l corev1.ResourceList) string {
		if len(l) == 0 {
			return ""
		}
		cpu, mem := "-", "-"
		if q, ok := l[corev1.ResourceCPU]; ok {
			cpu = q.String()
		}
		if q, ok := l[corev1.ResourceMemory]; ok {
			mem = q.String()
		}
		return cpu + "/" + mem
	}
	req, lim := part(r.Requests), part(r.Limits)
	switch {
	case req == "" && lim == "":
		return ""
	case lim == "":
		return "req " + req
	case req == "":
		return "lim " + lim
	default:
		return "req " + req + " · lim " + lim
	}
}

// fluxOwnerRevision resolves the owning Flux object's lastAppliedRevision with
// one live Get. When it cannot, the note says why (the compare still shows the
// owner identity, and never treats two unresolved revisions as equal).
func (c *ClusterConn) fluxOwnerRevision(ctx context.Context, o *workloads.Owner) (rev, note string) {
	if o.Kind != "Kustomization" && o.Kind != "HelmRelease" {
		return "", o.Kind + " owner - revision not read"
	}
	if c.dyn == nil {
		return "", "revision not read - no dynamic client"
	}
	gvr, err := gvrForKindIn(c.discoveryFor(ctx), flux.Kind(o.Kind))
	if readfence.Failed(err) {
		return "", "revision not read - " + err.Error()
	}
	if err != nil {
		return "", "Flux " + o.Kind + " API not served"
	}
	ns := o.Namespace
	if ns == "" {
		ns = "flux-system"
	}
	u, err := c.dyn.Resource(gvr).Namespace(ns).Get(ctx, o.Name, metav1.GetOptions{})
	if err != nil || u == nil {
		if err == nil {
			err = fmt.Errorf("empty response")
		}
		return "", "revision unreadable: " + err.Error()
	}
	if r := fluxAppliedRevision(u); r != "" {
		return r, ""
	}
	return "", "no revision applied yet"
}

func fluxAppliedRevision(u *unstructured.Unstructured) string {
	rev, _, _ := unstructured.NestedString(u.Object, "status", "lastAppliedRevision")
	return rev
}
