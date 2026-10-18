package appbridge

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/admission"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

// AdmissionConn is what AdmissionService needs of one cluster.
type AdmissionConn interface {
	HealthRowsNow(ctx context.Context, task, part string) (observe.Rows, bool)
	CheckAdmissionParams(ctx context.Context, policyKind, policyName string, paramGVR schema.GroupVersionResource, namespaced bool) (fleet.AdmissionParams, error)
}

// AdmissionService is bound to JS: the policy inspector's param check
// (coverage S3a §6), read when it opens. Existence only, never a param's
// values; nothing is retained after the reply.
type AdmissionService struct {
	lookup func(string) (AdmissionConn, bool)
}

func NewAdmissionService(lookup func(string) (AdmissionConn, bool)) *AdmissionService {
	return &AdmissionService{lookup: lookup}
}

// AdmissionParamCheckDTO is one binding's param, as read.
type AdmissionParamCheckDTO struct {
	Binding string `json:"binding"`
	State   string `json:"state"` // none | request-namespace | misconfigured | found | not-found | unreadable
	Text    string `json:"text"`
}

// AdmissionParamsDTO is one check for a policy. Cluster and PolicyUID fence
// it: the inspector drops a reply for another cluster or policy.
type AdmissionParamsDTO struct {
	Cluster   string                   `json:"cluster"`
	PolicyUID string                   `json:"policyUid"`
	State     string                   `json:"state"` // read | not-applicable | error
	Text      string                   `json:"text,omitempty"`
	Checks    []AdmissionParamCheckDTO `json:"checks"`
	More      int                      `json:"more,omitempty"`
	ReadAt    *time.Time               `json:"readAt,omitempty"`
}

// paramText words one check (S3a §6): a missing param under Deny is a
// configuration error, so the policy's failure policy decides; under
// Allow (or unset) the binding is skipped.
func paramText(c fleet.AdmissionParamCheck, failClosed bool) string {
	switch c.State {
	case fleet.ParamCheckNone:
		return "no paramRef: the binding evaluates with no params"
	case fleet.ParamCheckRequestNamespace:
		return "depends on the request's namespace: not checked"
	case fleet.ParamCheckMisconfigured:
		return "the paramRef sets a namespace for a cluster-scoped paramKind"
	case fleet.ParamCheckFound:
		return "found"
	case fleet.ParamCheckUnreadable:
		return "not readable (" + c.Class + "): whether it exists is not known"
	}
	switch {
	case c.NotFoundAction != "Deny":
		return "not found: this binding is skipped"
	case failClosed:
		return "not found: a request this binding matches is denied (no params found)"
	}
	return "not found: this binding is skipped (no params found; the policy ignores configuration errors)"
}

// CheckParams reads the params of the policy with this UID.
func (s *AdmissionService) CheckParams(cluster, policyUID string) AdmissionParamsDTO {
	out := AdmissionParamsDTO{Cluster: cluster, PolicyUID: policyUID, Checks: []AdmissionParamCheckDTO{}}
	conn, ok := s.lookup(cluster)
	if !ok {
		out.State, out.Text = "error", "cluster not connected"
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	rows, ok := conn.HealthRowsNow(ctx, healthsrc.TaskAdmission, "policies")
	if !ok {
		out.State, out.Text = "error", "Klyx has no live view of this cluster's health"
		return out
	}
	row, found := findRow(rows.Rows, policyUID)
	data, isRow := row.data.(healthsrc.AdmissionPolicyRow)
	if !found || !isRow || data.Policy == nil {
		out.State, out.Text = "error", "the policy is no longer in Klyx's read"
		return out
	}
	p := data.Policy
	switch {
	case p.ParamKind == nil:
		out.State, out.Text = "not-applicable", "no paramKind: there are no params to check"
		return out
	case data.ParamKind == nil || data.ParamKind.State != admission.ParamServed || data.ParamKind.Resource == "":
		out.State, out.Text = "not-applicable", "the paramKind is not confirmed served: params are not checked"
		return out
	}
	g, v, _, _ := p.ParamKind.GVK()
	res, err := conn.CheckAdmissionParams(ctx, p.Kind, p.Name, schema.GroupVersionResource{Group: g, Version: v, Resource: data.ParamKind.Resource}, data.ParamKind.Namespaced)
	if err != nil {
		out.State, out.Text = "error", err.Error()
		return out
	}
	out.State, out.More, out.ReadAt = "read", res.More, timePtrOf(res.ReadAt)
	for _, c := range res.Checks {
		out.Checks = append(out.Checks, AdmissionParamCheckDTO{Binding: c.Binding, State: c.State, Text: paramText(c, p.FailClosed())})
	}
	return out
}
