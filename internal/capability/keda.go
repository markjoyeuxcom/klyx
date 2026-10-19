package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/keda"
)

// KEDA detection (coverage S9 §2). The CRDs are the capability; the
// operators are Deployments labelled as KEDA's, in whatever namespace holds
// them; the external metrics APIService is KEDA's only on positive proof.
const (
	kedaGroup           = "keda.sh"
	kedaAPIService      = "v1beta1.external.metrics.k8s.io"
	kedaMaxBackingPods  = 16
	kedaNoOperator      = "KEDA CRDs installed but no KEDA operator found (no Deployment labelled app=keda-operator or app.kubernetes.io/name=keda-operator)"
	kedaUnread          = "KEDA CRDs installed but the KEDA operators could not be read"
	kedaNotReady        = "KEDA operator %s/%s is not ready"
	kedaWebhookNotReady = "KEDA admission webhooks %s/%s are not ready: two autoscalers on one target are not refused"
	kedaNoAPIService    = "no external metrics API (" + kedaAPIService + "): KEDA's external triggers (every type except cpu and memory) cannot scale"
	kedaAPIUnavailable  = "the external metrics API is not available (%s): KEDA's external triggers cannot scale"
	kedaAPIForeign      = "the external metrics API is held by %s, not KEDA's metrics server: KEDA's external triggers cannot scale"
)

// The operator and webhook Deployment labels: the chart's and the release
// manifest's (S9 §1.7).
var (
	kedaOperatorSelectors = []string{"app=keda-operator", "app.kubernetes.io/name=keda-operator"}
	kedaWebhookSelectors  = []string{"app=keda-admission-webhooks", "app.kubernetes.io/name=keda-admission-webhooks", "app=keda-admission"}
)

// KEDAOperator is one operator Deployment and its running pods' version.
type KEDAOperator struct {
	Namespace, Name, UID string
	Ready                bool
	// Version is read from the running pods' images, fenced by owner UID
	// (the Deployment's ReplicaSets, then their pods); unknown when the
	// pods disagree, are unreadable, or run an image not KEDA's own.
	Version keda.Version
}

// KEDAAPIService is what Klyx proved about the external metrics APIService.
type KEDAAPIService struct {
	Read      bool   // the APIService was read (or proven absent)
	Present   bool   //
	Available string // "True", "False", or "" when unreported
	Reason    string
	Service   string // namespace/name
	// Owner: "keda" (every ready backing pod runs KEDA's metrics server
	// image), "foreign" (the backing pods are proven and none does), or
	// "unverified" (anything short of that).
	Owner       string
	OwnerDetail string
}

// KEDACapability is KEDA's detection: the CRDs, the operators and their
// running versions, the webhooks, and the external metrics APIService.
type KEDACapability struct {
	Base
	Present       bool // scaledobjects served
	Healthy       bool
	Verified      bool // an operator was read, so Healthy is a claim
	OperatorsRead bool
	Operators     []KEDAOperator
	WebhooksRead  bool
	Webhooks      []KEDAOperator
	APIService    KEDAAPIService
}

// Version is the version every running operator agrees on (S9 §1.8):
// one operator, or several on one version; otherwise unknown.
func (k KEDACapability) Version() keda.Version {
	var v keda.Version
	for i, o := range k.Operators {
		if !o.Version.Known || (i > 0 && o.Version != v) {
			return keda.Version{}
		}
		v = o.Version
	}
	return v
}

func (d *Detector) detectKEDA(ctx context.Context, served map[string]bool) KEDACapability {
	out := KEDACapability{Present: served[kedaGroup]}
	if !out.Present {
		out.Base = Base{Tier: Absent, Reason: "no KEDA CRDs installed"}
		return out
	}
	ops, err := d.kedaDeployments(ctx, kedaOperatorSelectors)
	if err != nil {
		out.Base = Base{Tier: Classify(true, false), Reason: kedaUnread + ": " + err.Error()}
	} else {
		out.OperatorsRead = true
		for i := range ops {
			out.Operators = append(out.Operators, KEDAOperator{Namespace: ops[i].Namespace, Name: ops[i].Name, UID: string(ops[i].UID),
				Ready: DeploymentReady(&ops[i]), Version: d.kedaRunningVersion(ctx, &ops[i])})
		}
	}
	if hooks, err := d.kedaDeployments(ctx, kedaWebhookSelectors); err == nil {
		out.WebhooksRead = true
		for i := range hooks {
			out.Webhooks = append(out.Webhooks, KEDAOperator{Namespace: hooks[i].Namespace, Name: hooks[i].Name, UID: string(hooks[i].UID), Ready: DeploymentReady(&hooks[i])})
		}
	}
	out.APIService = d.kedaAPIService(ctx)
	if !out.OperatorsRead {
		return out
	}
	var problems []string
	for _, o := range out.Operators {
		if !o.Ready {
			problems = append(problems, fmt.Sprintf(kedaNotReady, o.Namespace, o.Name))
		}
	}
	for _, w := range out.Webhooks {
		if !w.Ready {
			problems = append(problems, fmt.Sprintf(kedaWebhookNotReady, w.Namespace, w.Name))
		}
	}
	switch a := out.APIService; {
	case !a.Read:
	case !a.Present:
		problems = append(problems, kedaNoAPIService)
	case a.Available == "False":
		problems = append(problems, fmt.Sprintf(kedaAPIUnavailable, a.Reason))
	case a.Owner == "foreign":
		problems = append(problems, fmt.Sprintf(kedaAPIForeign, a.Service))
	}
	switch {
	case len(out.Operators) == 0:
		out.Base.Reason = kedaNoOperator
	case len(problems) > 0:
		out.Verified = true
		out.Base.Reason = strings.Join(problems, "; ")
	default:
		out.Verified, out.Healthy = true, true
	}
	out.Base.Tier = Classify(true, out.Healthy)
	return out
}

// kedaDeployments lists the Deployments matching any selector, once each.
func (d *Detector) kedaDeployments(ctx context.Context, selectors []string) ([]appsv1.Deployment, error) {
	seen := map[string]bool{}
	var out []appsv1.Deployment
	for _, sel := range selectors {
		list, err := d.cs.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			return nil, err
		}
		for _, dep := range list.Items {
			if !seen[string(dep.UID)] {
				seen[string(dep.UID)] = true
				out = append(out, dep)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// kedaRunningVersion reads the version the operator's running pods run:
// its ReplicaSets by controller UID, their ready pods by controller UID,
// each pod's KEDA container image. Pods that disagree, an unreadable
// chain, or an image not KEDA's own leave it unknown (S9 §1.8).
func (d *Detector) kedaRunningVersion(ctx context.Context, dep *appsv1.Deployment) keda.Version {
	rss, err := d.cs.AppsV1().ReplicaSets(dep.Namespace).List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		return keda.Version{}
	}
	owned := map[string]bool{}
	for i := range rss.Items {
		if controlledBy(rss.Items[i].OwnerReferences, string(dep.UID)) {
			owned[string(rss.Items[i].UID)] = true
		}
	}
	pods, err := d.cs.CoreV1().Pods(dep.Namespace).List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		return keda.Version{}
	}
	var v keda.Version
	n := 0
	for i := range pods.Items {
		p := &pods.Items[i]
		if !podReady(p) || !ownedByAny(p.OwnerReferences, owned) {
			continue
		}
		pv := keda.Version{}
		for _, c := range p.Spec.Containers {
			if keda.IsOperatorImage(c.Image) {
				pv = keda.VersionOfImage(c.Image)
			}
		}
		if !pv.Known || (n > 0 && pv != v) {
			return keda.Version{}
		}
		v, n = pv, n+1
	}
	return v
}

func controlledBy(refs []metav1.OwnerReference, uid string) bool {
	for _, r := range refs {
		if r.Controller != nil && *r.Controller && string(r.UID) == uid {
			return true
		}
	}
	return false
}

func ownedByAny(refs []metav1.OwnerReference, uids map[string]bool) bool {
	for _, r := range refs {
		if r.Controller != nil && *r.Controller && uids[string(r.UID)] {
			return true
		}
	}
	return false
}

func podReady(p *corev1.Pod) bool {
	if p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// apiService is the slice of an APIService Klyx reads.
type apiService struct {
	Metadata struct {
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Service *struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"service"`
	} `json:"spec"`
	Status struct {
		Conditions []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"conditions"`
	} `json:"status"`
}

// kedaAPIService reads the external metrics APIService and proves its
// owner (S9 §2): every ready backing pod, traced Service to EndpointSlices
// to pods with matching UIDs, runs KEDA's metrics server image. Anything
// short of that is unverified, never KEDA's and never foreign.
func (d *Detector) kedaAPIService(ctx context.Context) KEDAAPIService {
	var out KEDAAPIService
	get := d.rawGet
	if get == nil {
		rc := d.cs.Discovery().RESTClient()
		if rc == nil {
			return out
		}
		get = func(ctx context.Context, path string) ([]byte, error) { return rc.Get().AbsPath(path).DoRaw(ctx) }
	}
	raw, err := get(ctx, "/apis/apiregistration.k8s.io/v1/apiservices/"+kedaAPIService)
	if err != nil {
		if apierrors.IsNotFound(err) {
			out.Read = true
		}
		return out
	}
	var a apiService
	if json.Unmarshal(raw, &a) != nil {
		return out
	}
	out.Read, out.Present = true, true
	for _, c := range a.Status.Conditions {
		if c.Type == "Available" {
			out.Available, out.Reason = c.Status, c.Reason
		}
	}
	if a.Spec.Service == nil {
		out.Owner, out.OwnerDetail = "foreign", "served locally, not by a Service"
		out.Service = "(local)"
		return out
	}
	out.Service = a.Spec.Service.Namespace + "/" + a.Spec.Service.Name
	out.Owner, out.OwnerDetail = d.kedaAdapterOwner(ctx, a.Spec.Service.Namespace, a.Spec.Service.Name)
	return out
}

func (d *Detector) kedaAdapterOwner(ctx context.Context, ns, name string) (string, string) {
	slices, err := d.cs.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + name})
	if err != nil {
		return "unverified", "the Service's endpoints could not be read"
	}
	type target struct{ name, uid string }
	var ready []target
	for _, s := range slices.Items {
		for _, e := range s.Endpoints {
			if e.Conditions.Ready != nil && !*e.Conditions.Ready {
				continue
			}
			if e.TargetRef == nil || e.TargetRef.Kind != "Pod" {
				return "unverified", "an endpoint does not name a pod"
			}
			ready = append(ready, target{e.TargetRef.Name, string(e.TargetRef.UID)})
		}
	}
	if len(ready) == 0 {
		return "unverified", "no ready endpoints back the Service"
	}
	if len(ready) > kedaMaxBackingPods {
		return "unverified", fmt.Sprintf("more than %d backing pods", kedaMaxBackingPods)
	}
	ours, other := 0, 0
	for _, t := range ready {
		p, err := d.cs.CoreV1().Pods(ns).Get(ctx, t.name, metav1.GetOptions{})
		if err != nil || string(p.UID) != t.uid {
			return "unverified", "a backing pod could not be read, or was replaced"
		}
		runs := false
		for _, c := range p.Status.ContainerStatuses {
			runs = runs || isMetricsImage(c.Image)
		}
		for _, c := range p.Spec.Containers {
			runs = runs || isMetricsImage(c.Image)
		}
		if runs {
			ours++
		} else {
			other++
		}
	}
	switch {
	case other == 0:
		return "keda", "every ready backing pod runs KEDA's metrics server"
	case ours == 0:
		return "foreign", "no backing pod runs KEDA's metrics server"
	}
	return "unverified", "the backing pods are mixed"
}

func isMetricsImage(image string) bool { return keda.IsMetricsServerImage(image) }
