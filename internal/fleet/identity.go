package fleet

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	"github.com/moomora/klyx/internal/keda"
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
	out.KEDA, out.KEDARead = c.kedaAuthsFor(ctx, obs, ns, name)
	return out, nil
}

// kedaAuthsFor lists the KEDA authentications that authenticate as one
// ServiceAccount (coverage S9 §6.3): for a KEDA operator's own
// ServiceAccount, the podIdentity authentications its operator uses; for a
// workload's, the aws identityOwner workload authentications whose
// ScaledObjects target a Deployment or StatefulSet running as it. Either
// link holds only with the forward join's proof - a using scaler whose
// handler is proven and sees the authentication - and anything short of
// that, an unread operator inventory, a cut trigger list or an incomplete
// model says partial (implementation reviews 1 and 2). From the models,
// with at most 16 target reads.
func (c *ClusterConn) kedaAuthsFor(ctx context.Context, obs identityObserver, ns, name string) ([]identity.KEDALink, string) {
	out := []identity.KEDALink{}
	if !c.kedaServed() {
		return out, identity.ReadComplete
	}
	if obs == nil {
		return out, identity.ReadNone
	}
	read := identity.ReadComplete
	var auths []*keda.Auth
	for _, kind := range []string{healthsrc.KindTriggerAuths, healthsrc.KindClusterTriggerAuths} {
		st, complete, ok := obs.VisitKind(kind, func(obj any) bool {
			if a, ok := obj.(*keda.Auth); ok && a.PodIdentity != nil {
				auths = append(auths, a)
			}
			return true
		})
		if !ok || !complete || st.Coverage != observe.Live || st.Truncated {
			read = identity.ReadPartial
		}
	}
	h := c.kedaHandling(ctx)
	// the operator's own ServiceAccount: only a read inventory rules out an
	// operator in this namespace
	switch c.kedaOperatorIn(ns) {
	case keda.Yes:
		links, complete := c.kedaOperatorUses(obs, ns, name, auths, h)
		out = append(out, links...)
		if !complete {
			read = identity.ReadPartial
		}
	case keda.Unknown:
		read = identity.ReadPartial
	}
	// a workload's ServiceAccount lending its role
	links, complete := c.kedaWorkloadUses(ctx, obs, ns, name, auths, h)
	out = append(out, links...)
	if !complete {
		read = identity.ReadPartial
	}
	return out, read
}

// kedaHandling is the operator candidates read once for one inverse join.
type kedaHandling struct {
	configs []keda.Config
	cut     bool
	ok      bool
}

func (c *ClusterConn) kedaHandling(ctx context.Context) kedaHandling {
	configs, cut, err := c.KEDAOperatorConfigs(ctx)
	return kedaHandling{configs: configs, cut: cut, ok: err == nil}
}

// sees is the forward join's proof for one scaler and one authentication:
// the proven handler and its visibility of the authentication - Yes, No
// (no operator handles it, or its selector excludes the authentication),
// or Unknown.
func (h kedaHandling) sees(s *keda.Scaler, a *keda.Auth) (*keda.Config, keda.Tri) {
	if !h.ok {
		return nil, keda.Unknown
	}
	hd := keda.HandlerOf(h.configs, h.cut, s.Namespace, s.Labels, s.LabelsCut)
	switch {
	case hd.Handler != nil:
		return hd.Handler, hd.Handler.SeesAuth(a.Labels, a.LabelsCut)
	case hd.None:
		return nil, keda.No
	}
	return nil, keda.Unknown
}

// kedaOperatorIn says whether a KEDA operator Deployment runs in ns:
// unknown when the operators could not be read.
func (c *ClusterConn) kedaOperatorIn(ns string) keda.Tri {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.caps.KEDA.OperatorsRead {
		return keda.Unknown
	}
	for _, o := range c.caps.KEDA.Operators {
		if o.Namespace == ns {
			return keda.Yes
		}
	}
	return keda.No
}

// authKey is a scaler reference's key among the authentications.
func authKey(s *keda.Scaler, ref string) string {
	kind, aname := splitRef(ref)
	if kind == keda.KindClusterTriggerAuth {
		return kind + "//" + aname
	}
	return kind + "/" + s.Namespace + "/" + aname
}

// kedaWorkloadUses lists the aws identityOwner workload authentications
// whose using ScaledObjects in ns, with a proven handler that sees them,
// target a workload running as ServiceAccount ns/name: the operator
// assumes that ServiceAccount's role.
func (c *ClusterConn) kedaWorkloadUses(ctx context.Context, obs identityObserver, ns, name string, auths []*keda.Auth, h kedaHandling) ([]identity.KEDALink, bool) {
	byKey := map[string]*keda.Auth{}
	for _, a := range auths {
		if a.PodIdentity.Provider == "aws" && a.PodIdentity.IdentityOwner == "workload" {
			byKey[authKind(a)+"/"+a.Namespace+"/"+a.Name] = a
		}
	}
	var out []identity.KEDALink
	if len(byKey) == 0 {
		return out, true
	}
	complete := true
	reads := 0
	seen := map[string]bool{}
	st, done, ok := obs.VisitKind(healthsrc.KindScaledObjects, func(obj any) bool {
		so, ok := obj.(*keda.ScaledObject)
		if !ok || so.Namespace != ns {
			return true
		}
		if so.TriggersCut {
			complete = false // an omitted trigger may name a workload authentication
		}
		for _, ref := range so.AuthRefs() {
			key := authKey(&so.Scaler, ref)
			a := byKey[key]
			if a == nil || seen[key] {
				continue
			}
			switch _, vis := h.sees(&so.Scaler, a); vis {
			case keda.No:
				continue // no operator handles it, or KEDA cannot read the authentication
			case keda.Unknown:
				complete = false
				continue
			}
			if reads == 16 {
				complete = false
				return false
			}
			reads++
			if sa := c.targetServiceAccount(ctx, so); sa == nil {
				complete = false
			} else if sa.Name == name {
				seen[key] = true
				out = append(out, identity.KEDALink{Kind: authKind(a), Namespace: a.Namespace, Name: a.Name,
					Via: "the KEDA operator assumes this ServiceAccount's role for ScaledObject " + so.Namespace + "/" + so.Name})
			}
		}
		return true
	})
	if !ok || !done || st.Coverage != observe.Live || st.Truncated {
		complete = false
	}
	return out, complete
}

// kedaOperatorUses lists the podIdentity authentications (not a workload's
// role) whose using scalers' handling operator runs as ServiceAccount
// ns/name and sees them; complete is false when any of that is unproven.
func (c *ClusterConn) kedaOperatorUses(obs identityObserver, ns, name string, auths []*keda.Auth, h kedaHandling) ([]identity.KEDALink, bool) {
	byKey := map[string]*keda.Auth{}
	for _, a := range auths {
		if a.PodIdentity.Provider == "aws" && a.PodIdentity.IdentityOwner == "workload" {
			continue // the operator assumes a workload's role: listed on that ServiceAccount
		}
		byKey[authKind(a)+"/"+a.Namespace+"/"+a.Name] = a
	}
	complete := h.ok
	type use struct {
		a        *keda.Auth
		operator string
		users    []string
	}
	uses := map[string]*use{}
	visit := func(kind string, s *keda.Scaler) {
		if s.TriggersCut {
			complete = false // an omitted trigger may name an authentication
		}
		for _, ref := range s.AuthRefs() {
			key := authKey(s, ref)
			a := byKey[key]
			if a == nil {
				continue
			}
			hd, vis := h.sees(s, a)
			switch {
			case hd == nil && vis == keda.No:
				continue // no operator handles it
			case hd == nil:
				complete = false // the handler is unverified: it may run as this ServiceAccount
				continue
			case hd.Namespace != ns:
				continue
			case hd.ServiceAccount == "":
				complete = false // its pods disagree on the ServiceAccount
				continue
			case hd.ServiceAccount != name:
				continue
			}
			switch vis {
			case keda.Yes:
				u := uses[key]
				if u == nil {
					u = &use{a: a, operator: hd.Namespace + "/" + hd.Name}
					uses[key] = u
				}
				u.users = append(u.users, kind+" "+s.Namespace+"/"+s.Name)
			case keda.Unknown:
				complete = false
			}
		}
	}
	for _, kind := range []string{healthsrc.KindScaledObjects, healthsrc.KindScaledJobs} {
		st, done, ok := obs.VisitKind(kind, func(obj any) bool {
			switch o := obj.(type) {
			case *keda.ScaledObject:
				visit(keda.KindScaledObject, &o.Scaler)
			case *keda.ScaledJob:
				visit(keda.KindScaledJob, &o.Scaler)
			}
			return true
		})
		if !ok || !done || st.Coverage != observe.Live || st.Truncated {
			complete = false
		}
	}
	keys := make([]string, 0, len(uses))
	for k := range uses {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []identity.KEDALink
	for _, k := range keys {
		u := uses[k]
		sort.Strings(u.users)
		via := "the KEDA operator " + u.operator + " runs as this ServiceAccount and handles " + u.users[0]
		if len(u.users) > 1 {
			via += fmt.Sprintf(" and %d more", len(u.users)-1)
		}
		out = append(out, identity.KEDALink{Kind: authKind(u.a), Namespace: u.a.Namespace, Name: u.a.Name, Via: via})
	}
	return out, complete
}

func authKind(a *keda.Auth) string {
	if a.Cluster {
		return keda.KindClusterTriggerAuth
	}
	return keda.KindTriggerAuth
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
