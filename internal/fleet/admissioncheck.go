package fleet

import (
	"context"
	"errors"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/admission"
)

// Param-check states (coverage S3a §6).
const (
	ParamCheckNone             = "none"              // no paramRef: evaluates with no params
	ParamCheckRequestNamespace = "request-namespace" // depends on the admitted request's namespace: not checked
	ParamCheckMisconfigured    = "misconfigured"     // a namespace for a cluster-scoped paramKind
	ParamCheckFound            = "found"
	ParamCheckNotFound         = "not-found"
	ParamCheckUnreadable       = "unreadable" // forbidden or an error: never "not found"
)

// maxParamChecks bounds one policy's on-demand reads.
const maxParamChecks = 20

const paramCheckTimeout = 15 * time.Second

// AdmissionParamCheck is one binding's param existence, read on demand:
// metadata only, never a param's values or a selector's.
type AdmissionParamCheck struct {
	Binding        string
	BindingUID     string
	State          string
	NotFoundAction string
	Class          string // why it is unreadable
}

// AdmissionParams is one on-demand param check for a policy.
type AdmissionParams struct {
	Checks []AdmissionParamCheck
	More   int // bindings past the cap, not checked
	ReadAt time.Time
}

// CheckAdmissionParams reads, for each binding that names the policy,
// whether its param exists (S3a §6). The bindings are read fresh, so the
// selector is the binding's current one; it is used for the read and never
// returned. paramGVR and namespaced come from the param-kind fact.
func (c *ClusterConn) CheckAdmissionParams(ctx context.Context, policyKind, policyName string, paramGVR schema.GroupVersionResource, namespaced bool) (AdmissionParams, error) {
	if c.dyn == nil || c.meta == nil {
		return AdmissionParams{}, errors.New("cluster not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, paramCheckTimeout)
	defer cancel()
	res := admission.ResourceValidatingBindings
	if policyKind == admission.Mutating {
		res = admission.ResourceMutatingBindings
	}
	list, err := c.dyn.Resource(schema.GroupVersionResource{Group: admission.Group, Version: admission.APIVersion, Resource: res}).List(ctx, metav1.ListOptions{})
	if err != nil {
		return AdmissionParams{}, err
	}
	var bindings []unstructured.Unstructured
	for _, b := range list.Items {
		if p, _, _ := unstructured.NestedString(b.Object, "spec", "policyName"); p == policyName {
			bindings = append(bindings, b)
		}
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].GetName() < bindings[j].GetName() })
	out := AdmissionParams{ReadAt: c.clk.Now()}
	if len(bindings) > maxParamChecks {
		out.More, bindings = len(bindings)-maxParamChecks, bindings[:maxParamChecks]
	}
	for _, b := range bindings {
		out.Checks = append(out.Checks, c.checkParam(ctx, &b, paramGVR, namespaced))
	}
	return out, nil
}

func (c *ClusterConn) checkParam(ctx context.Context, b *unstructured.Unstructured, gvr schema.GroupVersionResource, namespaced bool) AdmissionParamCheck {
	chk := AdmissionParamCheck{Binding: b.GetName(), BindingUID: string(b.GetUID())}
	ref, ok, _ := unstructured.NestedMap(b.Object, "spec", "paramRef")
	if !ok || ref == nil {
		chk.State = ParamCheckNone
		return chk
	}
	name, _, _ := unstructured.NestedString(ref, "name")
	ns, _, _ := unstructured.NestedString(ref, "namespace")
	chk.NotFoundAction, _, _ = unstructured.NestedString(ref, "parameterNotFoundAction")
	switch {
	case namespaced && ns == "":
		chk.State = ParamCheckRequestNamespace
		return chk
	case !namespaced && ns != "":
		chk.State = ParamCheckMisconfigured
		return chk
	}
	ri := c.meta.Resource(gvr)
	read := func(err error, found bool) {
		switch {
		case err == nil && found:
			chk.State = ParamCheckFound
		case err == nil || apierrors.IsNotFound(err):
			chk.State = ParamCheckNotFound
		case apierrors.IsForbidden(err):
			chk.State, chk.Class = ParamCheckUnreadable, "forbidden"
		default:
			chk.State, chk.Class = ParamCheckUnreadable, "error"
		}
	}
	if name != "" {
		var err error
		if namespaced {
			_, err = ri.Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		} else {
			_, err = ri.Get(ctx, name, metav1.GetOptions{})
		}
		read(err, err == nil)
		return chk
	}
	sel, ok, _ := unstructured.NestedMap(ref, "selector")
	if !ok {
		chk.State = ParamCheckNone // neither a name nor a selector: rejected by the API
		return chk
	}
	ls := &metav1.LabelSelector{}
	if err := convertSelector(sel, ls); err != nil {
		chk.State, chk.Class = ParamCheckUnreadable, "selector"
		return chk
	}
	selector, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil {
		chk.State, chk.Class = ParamCheckUnreadable, "selector"
		return chk
	}
	opts := metav1.ListOptions{LabelSelector: selector.String(), Limit: 1}
	if namespaced {
		l, err := ri.Namespace(ns).List(ctx, opts)
		read(err, err == nil && len(l.Items) > 0)
	} else {
		l, err := ri.List(ctx, opts)
		read(err, err == nil && len(l.Items) > 0)
	}
	return chk
}

// convertSelector decodes an unstructured label selector.
func convertSelector(m map[string]any, out *metav1.LabelSelector) error {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(m, out)
}
