package fleet

import (
	"context"
	"errors"
	"fmt"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/rbac"
)

// AccessCanI asks the API server whether a subject can perform the checked
// action - the authoritative answer, covering every configured authorizer.
// An empty saName checks as the kubeconfig identity (SelfSubjectAccessReview);
// otherwise the review carries the ServiceAccount's username AND its groups
// (a SubjectAccessReview without the groups mis-evaluates group-bound RBAC).
// Both reviews are virtual POSTs that persist nothing. On-demand; no watch.
func (c *ClusterConn) AccessCanI(ctx context.Context, spec rbac.CheckSpec, saNamespace, saName string) (rbac.Verdict, error) {
	if c.typed == nil {
		return rbac.Verdict{}, errors.New("cluster client not available")
	}
	attrs := &authorizationv1.ResourceAttributes{
		Namespace:   spec.Namespace,
		Verb:        spec.Verb,
		Group:       spec.Group,
		Resource:    spec.Resource,
		Subresource: spec.Subresource,
		Name:        spec.Name,
	}
	var status authorizationv1.SubjectAccessReviewStatus
	if saName == "" {
		r, err := c.typed.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx,
			&authorizationv1.SelfSubjectAccessReview{
				Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: attrs},
			}, metav1.CreateOptions{})
		if err != nil {
			return rbac.Verdict{}, fmt.Errorf("selfsubjectaccessreview: %w", err)
		}
		status = r.Status
	} else {
		r, err := c.typed.AuthorizationV1().SubjectAccessReviews().Create(ctx,
			&authorizationv1.SubjectAccessReview{
				Spec: authorizationv1.SubjectAccessReviewSpec{
					ResourceAttributes: attrs,
					User:               rbac.ServiceAccountUsername(saNamespace, saName),
					Groups:             rbac.ServiceAccountGroups(saNamespace),
				},
			}, metav1.CreateOptions{})
		if err != nil {
			return rbac.Verdict{}, fmt.Errorf("subjectaccessreview: %w", err)
		}
		status = r.Status
	}
	return rbac.Verdict{
		Allowed:         status.Allowed,
		Denied:          status.Denied,
		Reason:          status.Reason,
		EvaluationError: status.EvaluationError,
	}, nil
}

// AccessWhoCan enumerates the RBAC grants for the checked action. Uncapped
// lists, deliberately unlike reverseLookupCap: a capped who-can silently
// under-reports, which for an access audit is worse than a slow read - and
// RBAC objects are small. Any list failure errors out honestly; a partial
// enumeration is never returned as complete. On-demand; no watch.
func (c *ClusterConn) AccessWhoCan(ctx context.Context, spec rbac.CheckSpec) (rbac.Enumeration, error) {
	if c.typed == nil {
		return rbac.Enumeration{}, errors.New("cluster client not available")
	}
	crs, err := c.typed.RbacV1().ClusterRoles().List(ctx, metav1.ListOptions{})
	if err != nil {
		return rbac.Enumeration{}, fmt.Errorf("list clusterroles: %w", err)
	}
	crbs, err := c.typed.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		return rbac.Enumeration{}, fmt.Errorf("list clusterrolebindings: %w", err)
	}
	var roles []rbacv1.Role
	var rbs []rbacv1.RoleBinding
	if spec.Namespace != "" {
		rl, err := c.typed.RbacV1().Roles(spec.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return rbac.Enumeration{}, fmt.Errorf("list roles in %s: %w", spec.Namespace, err)
		}
		roles = rl.Items
		rbl, err := c.typed.RbacV1().RoleBindings(spec.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return rbac.Enumeration{}, fmt.Errorf("list rolebindings in %s: %w", spec.Namespace, err)
		}
		rbs = rbl.Items
	}
	return rbac.Enumerate(spec, roles, crs.Items, rbs, crbs.Items), nil
}
