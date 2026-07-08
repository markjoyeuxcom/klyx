package fleet

import (
	"context"

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
