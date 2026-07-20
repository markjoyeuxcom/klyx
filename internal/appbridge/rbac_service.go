package appbridge

import (
	"context"

	"github.com/moomora/klyx/internal/rbac"
)

// RBACConn is the per-cluster surface RBACService needs: the two on-demand
// access-review verbs. Both are read-only (SubjectAccessReview is a virtual
// POST; who-can only lists), so no protected-cluster gating applies.
type RBACConn interface {
	AccessCanI(ctx context.Context, spec rbac.CheckSpec, saNamespace, saName string) (rbac.Verdict, error)
	AccessWhoCan(ctx context.Context, spec rbac.CheckSpec) (rbac.Enumeration, error)
}

// RBACService is bound to JS: the access-check lens's backend.
type RBACService struct {
	lookup func(string) (RBACConn, bool)
}

func NewRBACService(lookup func(string) (RBACConn, bool)) *RBACService {
	return &RBACService{lookup: lookup}
}

// validateQuery enforces the fields every check needs. The SA namespace is
// required WITH the SA name: defaulting it (e.g. to the query namespace)
// would silently check a different identity than the user meant.
func validateQuery(q AccessQueryDTO) string {
	if q.Verb == "" {
		return "verb is required"
	}
	if q.Resource == "" {
		return "resource is required"
	}
	if q.SAName != "" && q.SANamespace == "" {
		return "service account namespace is required"
	}
	if q.SAName == "" && q.SANamespace != "" {
		return "service account name is required"
	}
	return ""
}

func toCheckSpec(q AccessQueryDTO) rbac.CheckSpec {
	return rbac.CheckSpec{
		Verb: q.Verb, Group: q.Group, Resource: q.Resource,
		Subresource: q.Subresource, Name: q.Name, Namespace: q.Namespace,
	}
}

// CanI runs the SubjectAccessReview simulation. The DTO always names the
// subject that was checked.
func (s *RBACService) CanI(cluster string, q AccessQueryDTO) AccessVerdictDTO {
	subject := "kubeconfig user (self)"
	if q.SAName != "" {
		subject = rbac.ServiceAccountUsername(q.SANamespace, q.SAName)
	}
	if msg := validateQuery(q); msg != "" {
		return AccessVerdictDTO{Subject: subject, Error: msg}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return AccessVerdictDTO{Subject: subject, Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	v, err := conn.AccessCanI(ctx, toCheckSpec(q), q.SANamespace, q.SAName)
	if err != nil {
		return AccessVerdictDTO{Subject: subject, Error: err.Error()}
	}
	return AccessVerdictDTO{
		Allowed: v.Allowed, Denied: v.Denied, Reason: v.Reason,
		EvaluationError: v.EvaluationError, Subject: subject,
	}
}

// WhoCan runs the static RBAC enumeration. Subject fields are ignored - the
// question is "who", not "can this subject".
func (s *RBACService) WhoCan(cluster string, q AccessQueryDTO) AccessWhoCanDTO {
	q.SAName, q.SANamespace = "", ""
	if msg := validateQuery(q); msg != "" {
		return AccessWhoCanDTO{Grants: []AccessGrantDTO{}, Error: msg}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return AccessWhoCanDTO{Grants: []AccessGrantDTO{}, Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	e, err := conn.AccessWhoCan(ctx, toCheckSpec(q))
	if err != nil {
		return AccessWhoCanDTO{Grants: []AccessGrantDTO{}, Error: err.Error()}
	}
	grants := make([]AccessGrantDTO, 0, len(e.Grants))
	for _, g := range e.Grants {
		grants = append(grants, AccessGrantDTO{
			SubjectKind: g.SubjectKind, SubjectName: g.SubjectName, SubjectNamespace: g.SubjectNamespace,
			BindingKind: g.BindingKind, BindingNamespace: g.BindingNamespace, BindingName: g.BindingName,
			RoleKind: g.RoleKind, RoleName: g.RoleName,
		})
	}
	return AccessWhoCanDTO{Grants: grants, ClusterWideOnly: e.ClusterWideOnly}
}
