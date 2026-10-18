package fleet

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"

	"github.com/moomora/klyx/internal/admission"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/identity"
	"github.com/moomora/klyx/internal/observe"
)

// Coverage S5 §6: workload identity, read on demand when an inspector
// opens; never watched. Every read is fenced by the UID the inspector holds
// and bounded.
const (
	identityTimeout         = 20 * time.Second
	maxIdentityPods         = 50
	maxControllerCandidates = 3
	maxControllerPods       = 10
	agentNamespace          = "kube-system"
	agentDaemonSet          = "eks-pod-identity-agent"
	metadataPodPrefix       = "gke-metadata-server"
	esoNamespace            = "external-secrets"
	esoName                 = "external-secrets"
	esoLabel                = "app.kubernetes.io/name=external-secrets"
)

// The identity webhooks' configuration names.
var identityWebhooks = []string{"pod-identity-webhook", "azure-wi-webhook-mutating-webhook-configuration"}

// identityObserver is what the reads need of the Observer.
type identityObserver interface {
	VisitKind(kind string, visit func(obj any) bool) (st observe.KindState, complete, ok bool)
}

func readState(err error) string {
	if apierrors.IsNotFound(err) {
		return identity.StateNotFound
	}
	return identity.StateUnreadable
}

func errText(err error) string {
	s := err.Error()
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// visitLive visits a kind's model and reports whether the visit was of a
// live, complete model.
func visitLive(h identityObserver, kind string, visit func(any)) bool {
	if h == nil {
		return false
	}
	st, complete, ok := h.VisitKind(kind, func(o any) bool { visit(o); return true })
	return ok && readLive(st, complete)
}

// readLive: a kind read completely and live, or confirmed absent. VisitKind
// visits nothing for an absent kind and so reports it incomplete: absence
// is a complete, empty source. Stale, checking and unreadable are not.
func readLive(st observe.KindState, complete bool) bool {
	return st.Coverage == observe.Absent || (complete && st.Coverage == observe.Live)
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// identityEnv reads the cluster around a workload: nodes, the agent and
// metadata-server pods and the webhooks from the Observer's models; the
// agent DaemonSet (GET) only when an AWS mechanism is in play.
func (c *ClusterConn) identityEnv(ctx context.Context, h identityObserver, needAgent bool) identity.Env {
	e := identity.Env{Nodes: map[string]*identity.NodeInfo{}}
	e.NodesRead = visitLive(h, healthsrc.KindNodes, func(o any) {
		if n, ok := o.(*corev1.Node); ok {
			e.Nodes[n.Name] = &identity.NodeInfo{Name: n.Name, Labels: n.Labels, ProviderID: n.Spec.ProviderID, OS: n.Labels["kubernetes.io/os"]}
		}
	})
	var agentUID string
	agentFound := false
	if needAgent && c.typed != nil {
		ds, err := c.typed.AppsV1().DaemonSets(agentNamespace).Get(ctx, agentDaemonSet, metav1.GetOptions{})
		switch {
		case err == nil:
			agentUID, agentFound = string(ds.UID), true
		case apierrors.IsNotFound(err):
			agentFound = true // read: there is none
		}
	}
	podsRead := visitLive(h, healthsrc.KindPods, func(o any) {
		p, ok := o.(*corev1.Pod)
		if !ok || p.Namespace != agentNamespace {
			return
		}
		switch {
		case agentUID != "" && ownerUIDIs(p.OwnerReferences, agentUID):
			e.AgentPods = append(e.AgentPods, identity.ComponentPod{Name: p.Name, Node: p.Spec.NodeName, Ready: podReady(p)})
		case strings.HasPrefix(p.Name, metadataPodPrefix):
			e.MetadataPods = append(e.MetadataPods, identity.ComponentPod{Name: p.Name, Node: p.Spec.NodeName, Ready: podReady(p)})
		}
	})
	e.AgentRead = podsRead && agentFound
	e.MetadataRead = podsRead
	found := map[string]identity.Webhook{}
	read := visitLive(h, healthsrc.KindMutatingWebhooks, func(o any) {
		cfg, ok := o.(*admission.Config)
		if !ok {
			return
		}
		for _, name := range identityWebhooks {
			if cfg.Name != name {
				continue
			}
			w := identity.Webhook{Name: name, Present: true}
			for _, wh := range cfg.Webhooks {
				w.URL = w.URL || wh.URL
				if wh.Service != nil {
					w.Service = wh.Service.Namespace + "/" + wh.Service.Name
				}
				w.FailClosed = w.FailClosed || wh.FailClosed()
				w.Selectors = w.Selectors || wh.NamespaceSelector || wh.ObjectSelector
			}
			found[name] = w
		}
	})
	for _, name := range identityWebhooks {
		w, ok := found[name]
		if !ok {
			w = identity.Webhook{Name: name}
		}
		w.Read = read
		e.Webhooks = append(e.Webhooks, w)
	}
	return e
}

func ownerUIDIs(refs []metav1.OwnerReference, uid string) bool {
	for _, r := range refs {
		if string(r.UID) == uid {
			return true
		}
	}
	return false
}

func webhookSides(e identity.Env, relevant func(name string) bool) []identity.Side {
	var out []identity.Side
	for _, w := range e.Webhooks {
		if relevant(w.Name) {
			out = append(out, identity.WebhookSide(w))
		}
	}
	return out
}

// usesAWS and usesAzure say which webhooks a reading concerns.
func usesAWS(d identity.Declared, pods []identity.Pod) bool {
	if d.RoleARN != nil {
		return true
	}
	for _, p := range pods {
		for _, c := range p.Containers {
			for _, m := range c.Mechanisms {
				if m.Kind == identity.MechIRSA || m.Kind == identity.MechPodIdentity {
					return true
				}
			}
		}
	}
	return false
}

func usesAzure(d identity.Declared, pods []identity.Pod) bool {
	if d.AzureClient != nil {
		return true
	}
	for _, p := range pods {
		if p.AzureOptIn {
			return true
		}
	}
	return false
}

func webhookRelevance(aws, azure bool) func(string) bool {
	return func(name string) bool {
		return (name == identityWebhooks[0] && aws) || (name == identityWebhooks[1] && azure)
	}
}

// ServiceAccountIdentity reads a ServiceAccount's Identity block (S5 §5).
func (c *ClusterConn) ServiceAccountIdentity(ctx context.Context, ns, name, uid string) (identity.SAReport, error) {
	if c.typed == nil {
		return identity.SAReport{}, errors.New("cluster not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, identityTimeout)
	defer cancel()
	out := identity.SAReport{ReadAt: time.Now().UTC(), Limits: identity.Limits, PodsRead: identity.ReadNone, StoresRead: identity.ReadNone, Pods: []identity.PodReport{}, Stores: []identity.StoreLink{}}
	sa, err := c.typed.CoreV1().ServiceAccounts(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		out.State, out.Error = readState(err), errText(err)
		return out, nil
	}
	if uid != "" && string(sa.UID) != uid {
		out.State = identity.StateReplaced
		return out, nil
	}
	out.State = identity.StateRead
	d := identity.DeclaredOf(sa)
	out.Declared = &d
	var pods []identity.Pod
	list, err := c.typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("spec.serviceAccountName", name).String(), Limit: maxIdentityPods})
	switch {
	case err != nil:
		out.PodsRead = identity.StateUnreadable
	default:
		out.PodsRead = identity.ReadComplete
		if list.Continue != "" {
			out.PodsRead = identity.ReadMore // the count is unknown: no remainingItemCount for selected lists
		}
		for i := range list.Items {
			pods = append(pods, identity.PodOf(&list.Items[i]))
		}
	}
	identity.SortPods(pods)
	h := c.Health()
	var obs identityObserver
	if h != nil {
		obs = h
	}
	aws := usesAWS(d, pods)
	e := c.identityEnv(ctx, obs, aws)
	for _, p := range pods {
		out.Pods = append(out.Pods, identity.PodReportOf(d, p, e))
	}
	out.Webhooks = webhookSides(e, webhookRelevance(aws, usesAzure(d, pods)))
	out.Stores, out.StoresRead, out.StoresGap = c.storesFor(ctx, obs, ns, name)
	return out, nil
}

// storesFor lists the stores that authenticate through a ServiceAccount:
// by serviceAccountRef, and (no auth) through the controller whose pods run
// as it (S5 §4.3).
func (c *ClusterConn) storesFor(ctx context.Context, h identityObserver, ns, name string) ([]identity.StoreLink, string, string) {
	stores, read := esoStores(h)
	if !read {
		return []identity.StoreLink{}, identity.ReadNone, "the External Secrets stores are not read live and completely"
	}
	out := []identity.StoreLink{}
	var unjoined []string // stores without auth whose handler is not resolved
	var cands []identity.Candidate
	candsComplete, candsRead := false, false
	for _, st := range stores {
		if link, ok := identity.StoreRefersTo(st, ns, name); ok {
			out = append(out, link)
			continue
		}
		sub := identity.StoreSubject(st, nil)
		if !sub.Controller {
			continue
		}
		if !candsRead {
			cands, candsComplete = c.esoCandidates(ctx, h)
			candsRead = true
		}
		hs := identity.HandlersOf(identity.StoreClass(st), cands, candsComplete)
		if hs.Unresolved != "" {
			unjoined = append(unjoined, storeName(st)+" ("+hs.Unresolved+")")
		}
		for _, g := range hs.Groups {
			if g.Namespace == ns && g.ServiceAccount == name {
				out = append(out, identity.StoreLink{Kind: st.GetKind(), Namespace: st.GetNamespace(), Name: st.GetName(), UID: string(st.GetUID()),
					Provider: sub.Provider, Mode: sub.Mode, Via: "controller"})
				break
			}
		}
	}
	state, gap := identity.ReadComplete, ""
	if len(unjoined) > 0 {
		// a store whose handler is not resolved may run as this ServiceAccount
		state = identity.ReadPartial
		gap = "Not known for " + strconv.Itoa(len(unjoined)) + " store(s) without auth, whose handling controller is not resolved: " + unjoined[0]
		if len(unjoined) > 1 {
			gap += ", and " + strconv.Itoa(len(unjoined)-1) + " more"
		}
	}
	if len(out) > identity.MaxStores {
		out, state = out[:identity.MaxStores], "cut"
	}
	return out, state, gap
}

func storeName(st *unstructured.Unstructured) string {
	if st.GetNamespace() == "" {
		return st.GetKind() + " " + st.GetName()
	}
	return st.GetKind() + " " + st.GetNamespace() + "/" + st.GetName()
}

// esoStores reads both store kinds from the Observer's ESO models. Only a
// live, complete read yields stores: a stale or partial one yields none
// and false, so no old auth configuration becomes a current subject.
func esoStores(h identityObserver) ([]*unstructured.Unstructured, bool) {
	var out []*unstructured.Unstructured
	complete := true
	served := false
	for _, kind := range []string{healthsrc.KindSecretStores, healthsrc.KindClusterSecretStores} {
		if h == nil {
			return nil, false
		}
		st, all, ok := h.VisitKind(kind, func(o any) bool {
			if u, ok := o.(*unstructured.Unstructured); ok {
				out = append(out, u)
			}
			return true
		})
		if !ok {
			complete = false
			continue
		}
		served = served || st.Coverage != observe.Absent
		complete = complete && readLive(st, all)
	}
	if !complete {
		return nil, false
	}
	if !served {
		return nil, true
	}
	return out, true
}

// esoCandidates reads the candidate controllers and their running pods
// (S5 §4.2): Deployments by the chart's label, plus the conventional one;
// each one's pods by its selector, fenced to it through their ReplicaSet
// owner's UID (from the Observer's ReplicaSets model).
func (c *ClusterConn) esoCandidates(ctx context.Context, h identityObserver) ([]identity.Candidate, bool) {
	if c.typed == nil {
		return nil, false
	}
	complete := true
	deps := map[string]*appsv1.Deployment{}
	list, err := c.typed.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: esoLabel, Limit: maxControllerCandidates})
	if err != nil {
		complete = false
	} else {
		if list.Continue != "" {
			complete = false
		}
		for i := range list.Items {
			deps[string(list.Items[i].UID)] = &list.Items[i]
		}
	}
	if d, err := c.typed.AppsV1().Deployments(esoNamespace).Get(ctx, esoName, metav1.GetOptions{}); err == nil {
		deps[string(d.UID)] = d
	} else if !apierrors.IsNotFound(err) {
		complete = false
	}
	rsOwner := map[string]string{} // ReplicaSet UID -> Deployment UID
	rsRead := visitLive(h, healthsrc.KindReplicaSets, func(o any) {
		if m, ok := o.(*metav1.PartialObjectMetadata); ok {
			for _, r := range m.OwnerReferences {
				if r.Controller != nil && *r.Controller {
					rsOwner[string(m.UID)] = string(r.UID)
				}
			}
		}
	})
	var out []identity.Candidate
	for _, d := range deps {
		cand := identity.Candidate{Namespace: d.Namespace, Name: d.Name, UID: string(d.UID), PodsComplete: rsRead}
		sel, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
		if err != nil {
			cand.PodsComplete = false
			out = append(out, cand)
			continue
		}
		pods, err := c.typed.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String(), Limit: maxControllerPods})
		if err != nil || pods.Continue != "" {
			cand.PodsComplete = false
		}
		if err == nil {
			for i := range pods.Items {
				p := &pods.Items[i]
				cp := identity.ControllerPod{Pod: identity.PodOf(p), Phase: string(p.Status.Phase)}
				for _, r := range p.OwnerReferences {
					if r.Controller != nil && *r.Controller && rsOwner[string(r.UID)] == string(d.UID) {
						cp.Owned = true
					}
				}
				if ctr := controllerContainer(p.Spec.Containers); ctr != nil {
					class, ok := identity.ClassOf(*ctr)
					cp.Class, cp.ClassUnresolved = class, !ok
				}
				cand.Pods = append(cand.Pods, cp)
			}
		}
		out = append(out, cand)
	}
	return out, complete
}

// controllerContainer is the ESO controller's container: the one carrying
// --controller-class, else the first.
func controllerContainer(cs []corev1.Container) *corev1.Container {
	for i := range cs {
		for _, a := range append(append([]string(nil), cs[i].Command...), cs[i].Args...) {
			if strings.HasPrefix(a, "--controller-class") {
				return &cs[i]
			}
		}
	}
	if len(cs) > 0 {
		return &cs[0]
	}
	return nil
}

// PodIdentity reads a pod's Identity block (S5 §5).
func (c *ClusterConn) PodIdentity(ctx context.Context, ns, name, uid string) (identity.PodInspectorReport, error) {
	if c.typed == nil {
		return identity.PodInspectorReport{}, errors.New("cluster not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, identityTimeout)
	defer cancel()
	out := identity.PodInspectorReport{ReadAt: time.Now().UTC(), Limits: identity.Limits, SAState: identity.ReadNone}
	p, err := c.typed.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		out.State, out.Error = readState(err), errText(err)
		return out, nil
	}
	if uid != "" && string(p.UID) != uid {
		out.State = identity.StateReplaced
		return out, nil
	}
	out.State = identity.StateRead
	pod := identity.PodOf(p)
	var d identity.Declared
	if sa, err := c.typed.CoreV1().ServiceAccounts(ns).Get(ctx, pod.ServiceAccount, metav1.GetOptions{}); err == nil {
		d = identity.DeclaredOf(sa)
		out.Declared, out.SAState = &d, identity.StateRead
	} else {
		out.SAState = readState(err)
		d = identity.Declared{Namespace: ns, Name: pod.ServiceAccount}
	}
	h := c.Health()
	var obs identityObserver
	if h != nil {
		obs = h
	}
	pods := []identity.Pod{pod}
	aws := usesAWS(d, pods)
	e := c.identityEnv(ctx, obs, aws)
	r := identity.PodReportOf(d, pod, e)
	if out.Declared == nil {
		r.Readings, r.Compared = nil, false // no comparison without the ServiceAccount
	}
	out.Report = &r
	out.Webhooks = webhookSides(e, webhookRelevance(aws, usesAzure(d, pods)))
	return out, nil
}

// StoreIdentity reads an ESO store's "Authenticates as" block (S5 §4, §5),
// fenced by the store's UID in the Observer's model.
func (c *ClusterConn) StoreIdentity(ctx context.Context, kind, ns, name, uid string) (identity.StoreReport, error) {
	if c.typed == nil {
		return identity.StoreReport{}, errors.New("cluster not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, identityTimeout)
	defer cancel()
	out := identity.StoreReport{ReadAt: time.Now().UTC(), Limits: identity.StoreLimits}
	h := c.Health()
	var obs identityObserver
	if h != nil {
		obs = h
	}
	stores, read := esoStores(obs)
	var st *unstructured.Unstructured
	for _, s := range stores {
		if s.GetKind() == kind && s.GetNamespace() == ns && s.GetName() == name {
			st = s
		}
	}
	switch {
	case st == nil && read:
		out.State = identity.StateNotFound
		return out, nil
	case st == nil:
		// a store from a stale or partial read is never a current subject
		out.State = identity.StateUnreadable
		out.Error = "the stores are not read live and completely"
		return out, nil
	case uid != "" && string(st.GetUID()) != uid:
		out.State = identity.StateReplaced
		return out, nil
	}
	out.State = identity.StateRead
	get := func(sns, sname string) (*corev1.ServiceAccount, identity.Read) {
		sa, err := c.typed.CoreV1().ServiceAccounts(sns).Get(ctx, sname, metav1.GetOptions{})
		switch {
		case err == nil:
			return sa, identity.ReadFound
		case apierrors.IsNotFound(err):
			return nil, identity.ReadMissing
		}
		return nil, identity.ReadUnreadable
	}
	out.Subject = identity.StoreSubject(st, get)
	if out.Subject.Controller {
		cands, complete := c.esoCandidates(ctx, obs)
		hs := identity.HandlersOf(identity.StoreClass(st), cands, complete)
		out.Handlers = &hs
		for _, g := range hs.Groups {
			if out.Subject.ControllerEnv {
				out.EnvGaps = append(out.EnvGaps, identity.ControllerEnvGaps(g)...)
			}
		}
	}
	return out, nil
}
