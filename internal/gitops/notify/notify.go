// Package notify reads Flux's notification kinds (coverage S1c, design
// accepted at 04d638b): the Receiver (notification.toolkit.fluxcd.io/v1),
// which has a verdict, and the Provider and Alert (v1beta3), which are
// static facts with no status to judge.
//
// A Receiver's webhook path is its bearer credential, and its Ready message
// names it: neither is ever projected. A Provider's address and proxy are
// withheld; only whether an address is set is read.
package notify

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/urlscrub"
)

// Group, versions and kinds.
const (
	Group           = "notification.toolkit.fluxcd.io"
	ReceiverVersion = "v1"
	// FactsVersion is the version Providers and Alerts are read at.
	FactsVersion    = "v1beta3"
	StaticVersion   = "v1beta3" // Provider and Alert
	KindReceiver    = "Receiver"
	KindProvider    = "Provider"
	KindAlert       = "Alert"
	AnnRequestedAt  = "reconcile.fluxcd.io/requestedAt"
	ReasonTokenNF   = "TokenNotFound"
	TypeGenericOIDC = "generic-oidc"
)

// Projection limits.
const (
	MaxObject = 4 << 10
	tightClip = 256
	maxRefs   = 50
)

// Object is one projected Receiver.
type Object struct {
	*unstructured.Unstructured
	Clipped []string
	Size    int
}

// SizeOf is the budget's Size function.
func SizeOf(v any) int {
	if o, ok := v.(*Object); ok {
		return o.Size
	}
	return MaxObject + 1
}

func clip(s string, path string, clipped *[]string) string {
	if len(s) <= tightClip {
		return s
	}
	*clipped = append(*clipped, path)
	cut := tightClip
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// ProjectReceiver builds a Receiver's projection (S1c §3): classification
// facts, the spec's shape (CEL filters as counts), never webhookPath and
// never a condition message.
func ProjectReceiver(obj any) (any, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("notify: want *Unstructured, got %T", obj)
	}
	var clipped []string
	md := map[string]any{"name": u.GetName(), "uid": string(u.GetUID()), "generation": u.GetGeneration(),
		"creationTimestamp": u.GetCreationTimestamp().UTC().Format(time.RFC3339)}
	if ns := u.GetNamespace(); ns != "" {
		md["namespace"] = ns
	}
	if rv := u.GetResourceVersion(); rv != "" {
		md["resourceVersion"] = rv
	}
	if v, ok := u.GetAnnotations()[AnnRequestedAt]; ok {
		md["annotations"] = map[string]any{AnnRequestedAt: clip(v, "metadata.annotations", &clipped)}
	}
	for _, k := range []string{"kustomize.toolkit.fluxcd.io/name", "kustomize.toolkit.fluxcd.io/namespace", "helm.toolkit.fluxcd.io/name", "helm.toolkit.fluxcd.io/namespace"} {
		if v, ok := u.GetLabels()[k]; ok {
			labels, _ := md["labels"].(map[string]any)
			if labels == nil {
				labels = map[string]any{}
				md["labels"] = labels
			}
			labels[k] = clip(v, "metadata.labels", &clipped)
		}
	}
	spec := map[string]any{}
	o := u.Object
	spec["type"], _, _ = unstructured.NestedString(o, "spec", "type")
	if s, ok, _ := unstructured.NestedString(o, "spec", "interval"); ok {
		spec["interval"] = clip(s, "spec.interval", &clipped)
	}
	if v, ok, _ := unstructured.NestedBool(o, "spec", "suspend"); ok {
		spec["suspend"] = v
	}
	if evs, ok, _ := unstructured.NestedStringSlice(o, "spec", "events"); ok {
		var out []any
		for _, e := range evs {
			out = append(out, clip(e, "spec.events", &clipped))
		}
		spec["events"] = out
	}
	if res, ok, _ := unstructured.NestedSlice(o, "spec", "resources"); ok {
		var out []any
		filters := 0
		for i, x := range res {
			m, _ := x.(map[string]any)
			if str(m, "filter") != "" {
				filters++
			}
			if i >= maxRefs {
				continue
			}
			r := map[string]any{"kind": clip(str(m, "kind"), "spec.resources", &clipped), "name": clip(str(m, "name"), "spec.resources", &clipped)}
			if ns := str(m, "namespace"); ns != "" {
				r["namespace"] = clip(ns, "spec.resources", &clipped)
			}
			if ml, ok := m["matchLabels"].(map[string]any); ok && len(ml) > 0 {
				r["matchLabels"] = int64(len(ml))
			}
			out = append(out, r)
		}
		if len(res) > maxRefs {
			clipped = append(clipped, "spec.resources")
		}
		spec["resources"] = out
		spec["resourceCount"] = int64(len(res))
		spec["filterCount"] = int64(filters)
	}
	if s, _, _ := unstructured.NestedString(o, "spec", "resourceFilter"); s != "" {
		spec["resourceFilter"] = true
	}
	if n, _, _ := unstructured.NestedString(o, "spec", "secretRef", "name"); n != "" {
		spec["secretRef"] = clip(n, "spec.secretRef", &clipped)
	}
	if p, ok, _ := unstructured.NestedSlice(o, "spec", "oidcProviders"); ok {
		spec["oidcProviders"] = int64(len(p))
	}
	status := map[string]any{}
	if g, ok, _ := unstructured.NestedInt64(o, "status", "observedGeneration"); ok {
		status["observedGeneration"] = g
	}
	conds, _, _ := unstructured.NestedSlice(o, "status", "conditions")
	var keep []any
	for _, x := range conds {
		cm, _ := x.(map[string]any)
		t := str(cm, "type")
		if t != "Ready" && t != "Reconciling" && t != "Stalled" {
			continue
		}
		c := map[string]any{"type": t, "status": str(cm, "status"), "reason": clip(str(cm, "reason"), "reason", &clipped),
			"lastTransitionTime": str(cm, "lastTransitionTime")}
		switch g := cm["observedGeneration"].(type) {
		case int64:
			c["observedGeneration"] = g
		case float64:
			c["observedGeneration"] = int64(g)
		}
		keep = append(keep, c) // never the message: a Receiver's names its path
	}
	if len(keep) > 0 {
		status["conditions"] = keep
	}
	m := map[string]any{"apiVersion": u.GetAPIVersion(), "kind": u.GetKind(), "metadata": md, "spec": spec}
	if len(status) > 0 {
		m["status"] = status
	}
	sort.Strings(clipped)
	out := &Object{Unstructured: &unstructured.Unstructured{Object: m}, Clipped: clipped}
	b, err := json.Marshal(m)
	if err != nil {
		out.Size = MaxObject + 1
		return out, nil
	}
	out.Size = len(b)
	return out, nil
}

// ReceiverResource is one resource a Receiver reconciles on a webhook.
type ReceiverResource struct {
	Kind, Name, Namespace string
	MatchLabels           int
}

// Receiver is a projected Receiver.
type Receiver struct {
	Kind, Namespace, Name, UID           string
	Generation                           int64
	ObservedGeneration                   int64
	ObservedSet                          bool
	Created                              time.Time
	Type, Interval, SecretRef            string
	Events                               []string
	Resources                            []ReceiverResource
	ResourceCount, FilterCount           int
	ResourceFilter, Suspended            bool
	OIDCProviders                        int
	Ready                                string // "True", "False", "Unknown" or ""
	Reason                               string
	ReadySince                           time.Time
	ReadyGeneration                      int64
	Reconciling, Stalled                 bool
	StalledReason                        string
	RequestedAt                          string
	OwnerKind, OwnerNamespace, OwnerName string
	Clipped                              []string
}

// ParseReceiver reads a projected Receiver.
func ParseReceiver(o *Object) Receiver {
	u := o.Unstructured
	m := u.Object
	r := Receiver{Kind: u.GetKind(), Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID()),
		Generation: u.GetGeneration(), Created: u.GetCreationTimestamp().Time, Clipped: o.Clipped}
	r.ObservedGeneration, r.ObservedSet, _ = unstructured.NestedInt64(m, "status", "observedGeneration")
	r.Type, _, _ = unstructured.NestedString(m, "spec", "type")
	r.Interval, _, _ = unstructured.NestedString(m, "spec", "interval")
	r.SecretRef, _, _ = unstructured.NestedString(m, "spec", "secretRef")
	r.Suspended, _, _ = unstructured.NestedBool(m, "spec", "suspend")
	r.ResourceFilter, _, _ = unstructured.NestedBool(m, "spec", "resourceFilter")
	r.Events, _, _ = unstructured.NestedStringSlice(m, "spec", "events")
	if n, ok, _ := unstructured.NestedInt64(m, "spec", "oidcProviders"); ok {
		r.OIDCProviders = int(n)
	}
	if n, ok, _ := unstructured.NestedInt64(m, "spec", "resourceCount"); ok {
		r.ResourceCount = int(n)
	}
	if n, ok, _ := unstructured.NestedInt64(m, "spec", "filterCount"); ok {
		r.FilterCount = int(n)
	}
	res, _, _ := unstructured.NestedSlice(m, "spec", "resources")
	for _, x := range res {
		rm, _ := x.(map[string]any)
		rr := ReceiverResource{Kind: str(rm, "kind"), Name: str(rm, "name"), Namespace: str(rm, "namespace")}
		if n, ok := rm["matchLabels"].(int64); ok {
			rr.MatchLabels = int(n)
		}
		r.Resources = append(r.Resources, rr)
	}
	r.RequestedAt = u.GetAnnotations()[AnnRequestedAt]
	if l := u.GetLabels(); l["kustomize.toolkit.fluxcd.io/name"] != "" {
		r.OwnerKind, r.OwnerNamespace, r.OwnerName = "Kustomization", l["kustomize.toolkit.fluxcd.io/namespace"], l["kustomize.toolkit.fluxcd.io/name"]
	} else if l["helm.toolkit.fluxcd.io/name"] != "" {
		r.OwnerKind, r.OwnerNamespace, r.OwnerName = "HelmRelease", l["helm.toolkit.fluxcd.io/namespace"], l["helm.toolkit.fluxcd.io/name"]
	}
	conds, _, _ := unstructured.NestedSlice(m, "status", "conditions")
	for _, x := range conds {
		cm, _ := x.(map[string]any)
		switch str(cm, "type") {
		case "Ready":
			r.Ready, r.Reason = str(cm, "status"), str(cm, "reason")
			r.ReadySince, _ = time.Parse(time.RFC3339, str(cm, "lastTransitionTime"))
			r.ReadyGeneration, _ = cm["observedGeneration"].(int64)
		case "Reconciling":
			r.Reconciling = str(cm, "status") == "True"
		case "Stalled":
			if str(cm, "status") == "True" {
				r.Stalled, r.StalledReason = true, str(cm, "reason")
			}
		}
	}
	return r
}

// Labels and wordings (S1c §4).
const (
	LabelSuspended = "Suspended"
	LabelSyncing   = "Syncing…"
	// TokenUnavailable is Klyx's own wording for TokenNotFound: the reason
	// covers a missing key and a failed Secret read alike, so the cause is
	// never claimed beyond that.
	TokenUnavailable = "token unavailable: its Secret could not be read or has no token; webhooks are refused"
	// Webhooks leave no trace on the Receiver.
	NoDeliveryEvidence = "Klyx cannot see whether webhooks arrive: deliveries leave no trace on the Receiver"
)

// pending is S1b §4.1's rule: Ready answers for an older generation, or it
// is not a failure and status.observedGeneration (which advances only on
// Ready or a terminal error) lags.
func (r Receiver) pending() bool {
	lag := r.ObservedSet && r.Generation > 0 && r.Generation > r.ObservedGeneration
	switch {
	case r.ReadyGeneration == 0 || r.Generation == 0:
		return lag
	case r.Generation > r.ReadyGeneration:
		return true
	case r.Ready == "False":
		return false
	}
	return lag
}

// ReceiverRule is a Receiver's verdict (S1c §4): Ready means the token was
// found and the spec is valid - never that webhooks arrive.
func ReceiverRule(r Receiver) health.NativeEval {
	switch {
	case r.Suspended:
		return health.NativeEval{Role: health.Paused, Label: LabelSuspended}
	case r.pending():
		return health.NativeEval{Included: true, Role: health.Progressing, Label: LabelSyncing}
	}
	e := health.NativeEval{Included: r.Ready != "True"}
	switch {
	case r.Ready == "True":
		e.Role, e.Label = health.OK, "Ready"
		e.Cond = health.CondAt("Ready", "True", r.ReadySince)
	case r.Ready == "False" && r.Reason == ReasonTokenNF:
		e.Role, e.Label = health.Fault, "Failed"
		e.Cond = health.CondAt("Ready", "False", r.ReadySince)
		e.Reason = health.CapReason(r.Reason, TokenUnavailable)
	case r.Ready == "False":
		e.Role, e.Label = health.Fault, "Failed"
		if r.Stalled {
			e.Label = "Stalled"
		}
		e.Cond = health.CondAt("Ready", "False", r.ReadySince)
		e.Reason = health.CapReason(r.Reason, "")
	case r.Stalled:
		e.Role, e.Label = health.Fault, "Stalled"
		e.Reason = health.CapReason(r.StalledReason, "")
	case r.Reconciling || r.Reason == "Progressing":
		e.Role, e.Label = health.Progressing, "Progressing"
	default:
		e.Role, e.Label = health.Unknown, "Unknown"
	}
	return e
}

// ReadyMeaning is what a Ready Receiver means, worded by type (S1c §4,
// revision 1): generic-oidc needs no token Secret.
func ReadyMeaning(r Receiver) string {
	if r.Type == TypeGenericOIDC && r.SecretRef == "" {
		return "configuration accepted (OIDC, no token Secret)"
	}
	return "configuration accepted, token read"
}

// Provider is a Provider's facts (S1c §2): never its address or proxy.
type Provider struct {
	Namespace, Name, UID                     string
	Type, Channel, Username                  string
	HasAddress, HasProxy                     bool
	SecretRef, CertSecretRef, ProxySecretRef string
	ServiceAccountName, Interval, Timeout    string
	Suspended                                bool
	OwnerKind, OwnerNamespace, OwnerName     string
}

// ParseProvider reads a Provider's facts from the object.
func ParseProvider(u *unstructured.Unstructured) Provider {
	m := u.Object
	p := Provider{Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID())}
	p.Type, _, _ = unstructured.NestedString(m, "spec", "type")
	p.Channel, _, _ = unstructured.NestedString(m, "spec", "channel")
	p.Username, _, _ = unstructured.NestedString(m, "spec", "username")
	addr, _, _ := unstructured.NestedString(m, "spec", "address")
	proxy, _, _ := unstructured.NestedString(m, "spec", "proxy")
	p.HasAddress, p.HasProxy = addr != "", proxy != ""
	p.SecretRef, _, _ = unstructured.NestedString(m, "spec", "secretRef", "name")
	p.CertSecretRef, _, _ = unstructured.NestedString(m, "spec", "certSecretRef", "name")
	p.ProxySecretRef, _, _ = unstructured.NestedString(m, "spec", "proxySecretRef", "name")
	p.ServiceAccountName, _, _ = unstructured.NestedString(m, "spec", "serviceAccountName")
	p.Interval, _, _ = unstructured.NestedString(m, "spec", "interval")
	p.Timeout, _, _ = unstructured.NestedString(m, "spec", "timeout")
	p.Suspended, _, _ = unstructured.NestedBool(m, "spec", "suspend")
	p.Channel = urlscrub.Text(p.Channel).Text
	if l := u.GetLabels(); l["kustomize.toolkit.fluxcd.io/name"] != "" {
		p.OwnerKind, p.OwnerNamespace, p.OwnerName = "Kustomization", l["kustomize.toolkit.fluxcd.io/namespace"], l["kustomize.toolkit.fluxcd.io/name"]
	} else if l["helm.toolkit.fluxcd.io/name"] != "" {
		p.OwnerKind, p.OwnerNamespace, p.OwnerName = "HelmRelease", l["helm.toolkit.fluxcd.io/namespace"], l["helm.toolkit.fluxcd.io/name"]
	}
	return p
}

// EventSource is one source an Alert listens to.
type EventSource struct {
	Kind, Name, Namespace string
	MatchLabels           int
}

// Alert is an Alert's facts.
type Alert struct {
	Namespace, Name, UID                 string
	ProviderName, EventSeverity          string
	EventSources                         []EventSource
	InclusionList, ExclusionList         []string
	MetadataKeys                         []string
	Suspended                            bool
	OwnerKind, OwnerNamespace, OwnerName string
}

// ParseAlert reads an Alert's facts. Event metadata values are free text:
// only their keys are read.
func ParseAlert(u *unstructured.Unstructured) Alert {
	m := u.Object
	a := Alert{Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID())}
	a.ProviderName, _, _ = unstructured.NestedString(m, "spec", "providerRef", "name")
	a.EventSeverity, _, _ = unstructured.NestedString(m, "spec", "eventSeverity")
	srcs, _, _ := unstructured.NestedSlice(m, "spec", "eventSources")
	for _, x := range srcs {
		sm, _ := x.(map[string]any)
		s := EventSource{Kind: str(sm, "kind"), Name: str(sm, "name"), Namespace: str(sm, "namespace")}
		if ml, ok := sm["matchLabels"].(map[string]any); ok {
			s.MatchLabels = len(ml)
		}
		a.EventSources = append(a.EventSources, s)
	}
	a.InclusionList, _, _ = unstructured.NestedStringSlice(m, "spec", "inclusionList")
	a.ExclusionList, _, _ = unstructured.NestedStringSlice(m, "spec", "exclusionList")
	if md, ok, _ := unstructured.NestedMap(m, "spec", "eventMetadata"); ok {
		for k := range md {
			a.MetadataKeys = append(a.MetadataKeys, k)
		}
		sort.Strings(a.MetadataKeys)
	}
	a.Suspended, _, _ = unstructured.NestedBool(m, "spec", "suspend")
	if l := u.GetLabels(); l["kustomize.toolkit.fluxcd.io/name"] != "" {
		a.OwnerKind, a.OwnerNamespace, a.OwnerName = "Kustomization", l["kustomize.toolkit.fluxcd.io/namespace"], l["kustomize.toolkit.fluxcd.io/name"]
	} else if l["helm.toolkit.fluxcd.io/name"] != "" {
		a.OwnerKind, a.OwnerNamespace, a.OwnerName = "HelmRelease", l["helm.toolkit.fluxcd.io/namespace"], l["helm.toolkit.fluxcd.io/name"]
	}
	return a
}

// Provider link states.
const (
	ProviderFound      = "found"
	ProviderMissing    = "not found"
	ProviderIncomplete = "incomplete"
)

// ProviderLink is an Alert's provider: whether it exists, from a read that
// may be capped, and whether it is suspended.
type ProviderLink struct {
	State     string
	Suspended bool
}

// AlertProvider joins an Alert to its Provider in its own namespace.
func AlertProvider(a Alert, providers []Provider, complete bool) ProviderLink {
	for _, p := range providers {
		if p.Namespace == a.Namespace && p.Name == a.ProviderName {
			return ProviderLink{State: ProviderFound, Suspended: p.Suspended}
		}
	}
	if !complete {
		return ProviderLink{State: ProviderIncomplete}
	}
	return ProviderLink{State: ProviderMissing}
}

// The reasons an Alert's events carry about dispatch (S1c §2).
var DispatchReasons = []string{"NotificationDispatchFailed", "InvalidConfig", "SourceFetchFailed", "MetadataAppendFailed"}

// Dispatch is an Alert's dispatch evidence: counts by reason, from its
// events, which expire after about an hour. Never a verdict, never a
// message.
func Dispatch(reasons []string) map[string]int {
	out := map[string]int{}
	for _, r := range reasons {
		for _, d := range DispatchReasons {
			if r == d {
				out[r]++
			}
		}
	}
	return out
}

// SummaryOf words dispatch evidence.
func SummaryOf(counts map[string]int) string {
	var parts []string
	for _, d := range DispatchReasons {
		if n := counts[d]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, d))
		}
	}
	if len(parts) == 0 {
		return "no dispatch problems in the recent events"
	}
	return strings.Join(parts, ", ") + " (from the controller's events, which expire after about an hour)"
}

// Read is the Notifications view's on-demand read (S1c §3): Providers and
// Alerts, each capped, and the reasons of the Alerts' recent events. It
// retains nothing after the view closes.
type Read struct {
	Providers, Alerts List
	// AlertReasons are the Alerts' recent event reasons by "namespace/name":
	// never their messages (§6).
	AlertReasons    map[string][]string
	EventsTruncated bool
	EventsError     string
}

// List is one capped read of a kind.
type List struct {
	Items []*unstructured.Unstructured
	// Absent: the cluster does not serve the kind. Error: the read failed,
	// or the kind is served at a version Klyx does not read.
	Absent bool
	Error  string
	// More is how many are past the cap: the server's count, -1 when it
	// does not say, 0 when nothing is.
	More int64
}

// Complete: every object of the kind is in the list.
func (l List) Complete() bool { return l.Error == "" && l.More == 0 }

// ListCap bounds each on-demand list (S1c §3).
const ListCap = 500
