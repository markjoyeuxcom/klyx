package fleet

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/moomora/klyx/internal/crd"
)

// reverseLookupCap bounds the pods/bindings a reverse "used-by / granted-by"
// lookup scans, so opening an inspector on a huge cluster can never issue an
// unbounded list. The refs are drill-ins, not an audit; the cap keeps the
// detail read cheap.
const reverseLookupCap = 500

// pvcConsumerRefs finds the pods in ns that mount the named PVC - the "what is
// using this volume" question a PVC inspector should answer. Namespace-scoped,
// capped list; nil on any error (the detail still renders).
func (c *ClusterConn) pvcConsumerRefs(ctx context.Context, ns, name string) []crd.RelatedRef {
	if c.typed == nil {
		return nil
	}
	pods, err := c.typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: reverseLookupCap})
	if err != nil || pods == nil {
		return nil
	}
	var out []crd.RelatedRef
	for i := range pods.Items {
		p := &pods.Items[i]
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == name {
				out = append(out, resourceRef("Pod", ns, p.Name, "", "v1", "pods", "Namespaced", "used by"))
				break
			}
		}
	}
	return out
}

// ownerPodRefs finds pods in ns owned (via ownerReferences) by the object with
// the given UID - the pods a Job or ReplicaSet inspector should list. Empty UID
// or missing typed client yields nil.
func (c *ClusterConn) ownerPodRefs(ctx context.Context, ns, ownerUID, relation string) []crd.RelatedRef {
	if c.typed == nil || ownerUID == "" {
		return nil
	}
	pods, err := c.typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: reverseLookupCap})
	if err != nil || pods == nil {
		return nil
	}
	var out []crd.RelatedRef
	for i := range pods.Items {
		p := &pods.Items[i]
		for _, o := range p.OwnerReferences {
			if string(o.UID) == ownerUID {
				out = append(out, resourceRef("Pod", ns, p.Name, "", "v1", "pods", "Namespaced", relation))
				break
			}
		}
	}
	return out
}

// pdbGuardedRefs finds the pods a PodDisruptionBudget guards via
// spec.selector.matchLabels. matchExpressions are not evaluated (matchLabels
// covers the common case); an EMPTY selector guards nothing here - it is never
// expanded to "all pods in the namespace", which would be misleading in a
// drill-in list.
func (c *ClusterConn) pdbGuardedRefs(ctx context.Context, ns string, matchLabels map[string]string) []crd.RelatedRef {
	if c.typed == nil || len(matchLabels) == 0 {
		return nil
	}
	pods, err := c.typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(matchLabels).String(),
		Limit:         reverseLookupCap,
	})
	if err != nil || pods == nil {
		return nil
	}
	out := make([]crd.RelatedRef, 0, len(pods.Items))
	for i := range pods.Items {
		out = append(out, resourceRef("Pod", ns, pods.Items[i].Name, "", "v1", "pods", "Namespaced", "guarded pod"))
	}
	return out
}

// rbacDanglingFacts returns a danger fact when a (Cluster)RoleBinding's roleRef
// points at a Role/ClusterRole that does not exist - a real broken-permission
// state: the binding grants nothing. One Get; the fact is emitted ONLY on a
// definitive NotFound, never on an RBAC-denied or transient list error (which
// must not be misreported as "missing").
func (c *ClusterConn) rbacDanglingFacts(ctx context.Context, group, plural, ns string, u *unstructured.Unstructured) []crd.Fact {
	if c.typed == nil || group != "rbac.authorization.k8s.io" {
		return nil
	}
	if plural != "rolebindings" && plural != "clusterrolebindings" {
		return nil
	}
	roleName := nestedString(u.Object, "roleRef", "name")
	if roleName == "" {
		return nil
	}
	roleKind := nestedString(u.Object, "roleRef", "kind")
	var err error
	if roleKind == "ClusterRole" || plural == "clusterrolebindings" {
		_, err = c.typed.RbacV1().ClusterRoles().Get(ctx, roleName, metav1.GetOptions{})
		roleKind = "ClusterRole"
	} else {
		_, err = c.typed.RbacV1().Roles(ns).Get(ctx, roleName, metav1.GetOptions{})
		if roleKind == "" {
			roleKind = "Role"
		}
	}
	if apierrors.IsNotFound(err) {
		return []crd.Fact{{Group: "", Label: "roleRef", Value: "missing: " + roleKind + "/" + roleName, Tone: "danger", Kind: "row"}}
	}
	return nil
}

// serviceAccountBindingRefs finds the RoleBindings + ClusterRoleBindings whose
// subjects include this ServiceAccount - the "what grants this SA its
// permissions" question. A RoleBinding subject with no namespace defaults to the
// binding's own namespace; a ClusterRoleBinding subject must carry the namespace.
func (c *ClusterConn) serviceAccountBindingRefs(ctx context.Context, ns, name string) []crd.RelatedRef {
	if c.typed == nil {
		return nil
	}
	saSubject := func(subjects []rbacv1.Subject, requireNS bool) bool {
		for _, s := range subjects {
			if s.Kind != "ServiceAccount" || s.Name != name {
				continue
			}
			if requireNS {
				if s.Namespace == ns {
					return true
				}
			} else if s.Namespace == "" || s.Namespace == ns {
				return true
			}
		}
		return false
	}
	var out []crd.RelatedRef
	if rbs, err := c.typed.RbacV1().RoleBindings(ns).List(ctx, metav1.ListOptions{Limit: reverseLookupCap}); err == nil && rbs != nil {
		for i := range rbs.Items {
			if saSubject(rbs.Items[i].Subjects, false) {
				out = append(out, resourceRef("RoleBinding", ns, rbs.Items[i].Name, "rbac.authorization.k8s.io", "v1", "rolebindings", "Namespaced", "granted by"))
			}
		}
	}
	if crbs, err := c.typed.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{Limit: reverseLookupCap}); err == nil && crbs != nil {
		for i := range crbs.Items {
			if saSubject(crbs.Items[i].Subjects, true) {
				out = append(out, resourceRef("ClusterRoleBinding", "", crbs.Items[i].Name, "rbac.authorization.k8s.io", "v1", "clusterrolebindings", "Cluster", "granted by"))
			}
		}
	}
	return out
}

// reloaderConsumerRefs answers "if I change this ConfigMap/Secret, what restarts"
// for clusters running Stakater Reloader: it finds the Deployments, StatefulSets,
// and DaemonSets in ns that reload on this object. Two Reloader modes are honored:
//   - explicit: the workload's configmap|secret.reloader.stakater.com/reload
//     annotation lists this object's name (comma-separated).
//   - auto: reloader.stakater.com/auto="true" reloads on ANY mounted CM/Secret, so
//     the workload matches when its pod template mounts this object (reloaderPodMounts).
//
// kind is "ConfigMap" or "Secret". Capped, namespace-scoped lists; nil on error.
func (c *ClusterConn) reloaderConsumerRefs(ctx context.Context, ns, name, kind string) []crd.RelatedRef {
	if c.typed == nil {
		return nil
	}
	explicitKey := "configmap.reloader.stakater.com/reload"
	if kind == "Secret" {
		explicitKey = "secret.reloader.stakater.com/reload"
	}
	reloadsOn := func(ann map[string]string, tmpl corev1.PodSpec) bool {
		if v := ann[explicitKey]; v != "" {
			for _, n := range strings.Split(v, ",") {
				if strings.TrimSpace(n) == name {
					return true
				}
			}
		}
		return ann["reloader.stakater.com/auto"] == "true" && reloaderPodMounts(tmpl, kind, name)
	}
	var out []crd.RelatedRef
	if deps, err := c.typed.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{Limit: reverseLookupCap}); err == nil && deps != nil {
		for i := range deps.Items {
			if reloadsOn(deps.Items[i].Annotations, deps.Items[i].Spec.Template.Spec) {
				out = append(out, resourceRef("Deployment", ns, deps.Items[i].Name, "apps", "v1", "deployments", "Namespaced", "reloads"))
			}
		}
	}
	if sts, err := c.typed.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{Limit: reverseLookupCap}); err == nil && sts != nil {
		for i := range sts.Items {
			if reloadsOn(sts.Items[i].Annotations, sts.Items[i].Spec.Template.Spec) {
				out = append(out, resourceRef("StatefulSet", ns, sts.Items[i].Name, "apps", "v1", "statefulsets", "Namespaced", "reloads"))
			}
		}
	}
	if ds, err := c.typed.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{Limit: reverseLookupCap}); err == nil && ds != nil {
		for i := range ds.Items {
			if reloadsOn(ds.Items[i].Annotations, ds.Items[i].Spec.Template.Spec) {
				out = append(out, resourceRef("DaemonSet", ns, ds.Items[i].Name, "apps", "v1", "daemonsets", "Namespaced", "reloads"))
			}
		}
	}
	return out
}

// reloaderPodMounts reports whether a pod template references the named
// ConfigMap/Secret through the surfaces Reloader's auto mode watches: a volume
// (incl. a projected source), an envFrom, or a valueFrom key ref.
func reloaderPodMounts(spec corev1.PodSpec, kind, name string) bool {
	for _, v := range spec.Volumes {
		if kind == "ConfigMap" && v.ConfigMap != nil && v.ConfigMap.Name == name {
			return true
		}
		if kind == "Secret" && v.Secret != nil && v.Secret.SecretName == name {
			return true
		}
		if v.Projected != nil {
			for _, s := range v.Projected.Sources {
				if kind == "ConfigMap" && s.ConfigMap != nil && s.ConfigMap.Name == name {
					return true
				}
				if kind == "Secret" && s.Secret != nil && s.Secret.Name == name {
					return true
				}
			}
		}
	}
	containers := append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...)
	for i := range containers {
		ct := &containers[i]
		for _, ef := range ct.EnvFrom {
			if kind == "ConfigMap" && ef.ConfigMapRef != nil && ef.ConfigMapRef.Name == name {
				return true
			}
			if kind == "Secret" && ef.SecretRef != nil && ef.SecretRef.Name == name {
				return true
			}
		}
		for _, e := range ct.Env {
			if e.ValueFrom == nil {
				continue
			}
			if kind == "ConfigMap" && e.ValueFrom.ConfigMapKeyRef != nil && e.ValueFrom.ConfigMapKeyRef.Name == name {
				return true
			}
			if kind == "Secret" && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == name {
				return true
			}
		}
	}
	return false
}
