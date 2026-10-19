package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/hpa"
	"github.com/moomora/klyx/internal/identity"
	"github.com/moomora/klyx/internal/keda"
	"github.com/moomora/klyx/internal/observe"
)

// Coverage S9 §6, §7, §8: KEDA's on-demand reads and its verb. Every read
// is bounded and fenced: the inspected object's UID, the per-read timeout,
// the caps below; a cut is said and leaves the rest unverified.
const (
	kedaMaxCandidates   = 4
	kedaMaxOperatorPods = 8
	kedaMaxExistence    = 32
	kedaReadTimeout     = 5 * time.Second
	kedaTotalTimeout    = 15 * time.Second
	kedaJobsPage        = 200
	kedaJobsPages       = 5
	kedaJobsShown       = 20
)

func kedaGVR(resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: keda.Group, Version: keda.APIVersion, Resource: resource}
}

// KEDAOperatorConfigs reads each candidate operator's configuration from
// its running pods (S9 §6.1): at most 4 candidates and 8 pods each, fenced
// by owner UID through the Deployment's ReplicaSets; only the allowlisted
// env names are read.
func (c *ClusterConn) KEDAOperatorConfigs(ctx context.Context) (configs []keda.Config, cut bool, err error) {
	c.mu.RLock()
	ops := append([]capKEDAOperator(nil), operatorsOf(c)...)
	read := c.caps.KEDA.OperatorsRead
	c.mu.RUnlock()
	if !read {
		return nil, false, errors.New("the KEDA operators could not be read")
	}
	if len(ops) > kedaMaxCandidates {
		ops, cut = ops[:kedaMaxCandidates], true
	}
	for _, op := range ops {
		rctx, cancel := context.WithTimeout(ctx, kedaReadTimeout)
		pods := c.kedaOperatorPods(rctx, op)
		cancel()
		configs = append(configs, keda.ConfigOf(op.Name, op.Namespace, pods))
	}
	return configs, cut, nil
}

type capKEDAOperator struct{ Namespace, Name, UID string }

func operatorsOf(c *ClusterConn) []capKEDAOperator {
	var out []capKEDAOperator
	for _, o := range c.caps.KEDA.Operators {
		out = append(out, capKEDAOperator{o.Namespace, o.Name, o.UID})
	}
	return out
}

func (c *ClusterConn) kedaOperatorPods(ctx context.Context, op capKEDAOperator) []keda.OperatorPod {
	rss, err := c.typed.AppsV1().ReplicaSets(op.Namespace).List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		return []keda.OperatorPod{{Name: op.Name, Namespace: op.Namespace}}
	}
	owned := map[string]bool{}
	for i := range rss.Items {
		if controlledByUID(rss.Items[i].OwnerReferences, op.UID) {
			owned[string(rss.Items[i].UID)] = true
		}
	}
	pods, err := c.typed.CoreV1().Pods(op.Namespace).List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		return []keda.OperatorPod{{Name: op.Name, Namespace: op.Namespace}}
	}
	var out []keda.OperatorPod
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil || !ownedByAnyUID(p.OwnerReferences, owned) {
			continue
		}
		if len(out) == kedaMaxOperatorPods {
			break
		}
		out = append(out, operatorPodOf(p))
	}
	return out
}

// operatorPodOf reads one pod's KEDA container: its image, its
// ServiceAccount, the allowlisted env names as declared, and whether it
// has envFrom (§6.1). No other env is read.
func operatorPodOf(p *corev1.Pod) keda.OperatorPod {
	op := keda.OperatorPod{Name: p.Name, Namespace: p.Namespace, ServiceAccount: p.Spec.ServiceAccountName, Readable: true, Env: map[string]keda.EnvEntry{}}
	if op.ServiceAccount == "" {
		op.ServiceAccount = "default"
	}
	var ctr *corev1.Container
	for i := range p.Spec.Containers {
		if keda.IsOperatorImage(p.Spec.Containers[i].Image) || ctr == nil {
			ctr = &p.Spec.Containers[i]
		}
	}
	if ctr == nil {
		op.Readable = false
		return op
	}
	op.Image = ctr.Image
	op.EnvFrom = len(ctr.EnvFrom) > 0
	allowed := map[string]bool{}
	for _, n := range keda.OperatorEnvNames {
		allowed[n] = true
	}
	for _, e := range ctr.Env {
		if !allowed[e.Name] {
			continue
		}
		if e.ValueFrom != nil {
			op.Env[e.Name] = keda.EnvEntry{ValueFrom: true}
		} else {
			op.Env[e.Name] = keda.EnvEntry{Literal: true, Value: e.Value}
		}
	}
	return op
}

func controlledByUID(refs []metav1.OwnerReference, uid string) bool {
	for _, r := range refs {
		if r.Controller != nil && *r.Controller && string(r.UID) == uid {
			return true
		}
	}
	return false
}

func ownedByAnyUID(refs []metav1.OwnerReference, uids map[string]bool) bool {
	for _, r := range refs {
		if r.Controller != nil && *r.Controller && uids[string(r.UID)] {
			return true
		}
	}
	return false
}

// KEDARef is one referenced object's existence (§6.2).
type KEDARef struct {
	Source    string `json:"source"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Key       string `json:"key,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	// State: "exists", "not found", or "unverified" with Why.
	State string `json:"state"`
	Why   string `json:"why,omitempty"`
}

// KEDAAuthCheck is one authentication a scaler references.
type KEDAAuthCheck struct {
	Kind, Name string
	// State: "exists", "not found", "unverified".
	State string
	Why   string
	// Visible is per handling operator: "visible", "excluded" or
	// "unverified" (§6.1).
	Visible map[string]string
	Refs    []KEDARef
	RefsCut bool
	Subject *identity.Subject
}

// KEDAInspection is the inspector's on-demand evidence for one scaler.
type KEDAInspection struct {
	UID           string
	Handler       string   // namespace/name of the handling operator
	None          bool     // every candidate resolved and none watches it
	Candidates    []string // when unverified
	CandidatesCut bool
	Version       string
	Disagree      []string
	Auths         []KEDAAuthCheck
}

// KEDAInspect reads one scaler's on-demand evidence (S9 §6): the handling
// operator, each referenced authentication with its visibility, the
// objects it references (metadata only, at the namespace KEDA reads them
// from) and the subject. The scaler is fenced by uid.
func (c *ClusterConn) KEDAInspect(ctx context.Context, resource, ns, name, uid string) (KEDAInspection, error) {
	ctx, cancel := context.WithTimeout(ctx, kedaTotalTimeout)
	defer cancel()
	out := KEDAInspection{UID: uid}
	if c.dyn == nil || c.typed == nil {
		return out, errors.New("the cluster connection cannot read KEDA")
	}
	if resource != keda.ResourceScaledObjects && resource != keda.ResourceScaledJobs {
		return out, fmt.Errorf("not a KEDA scaler: %s", resource)
	}
	u, err := c.dyn.Resource(kedaGVR(resource)).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return out, err
	}
	if string(u.GetUID()) != uid {
		return out, fmt.Errorf("%s/%s was replaced since it was shown", ns, name)
	}
	var sc keda.Scaler
	var workloadSA *identity.SARef
	if resource == keda.ResourceScaledObjects {
		so := keda.ProjectScaledObject(u)
		sc = so.Scaler
		workloadSA = c.targetServiceAccount(ctx, so)
	} else {
		sc = keda.ProjectScaledJob(u).Scaler
		saName, _, _ := unstructured.NestedString(u.Object, "spec", "jobTargetRef", "template", "spec", "serviceAccountName")
		if saName == "" {
			saName = "default"
		}
		workloadSA = &identity.SARef{Namespace: ns, Name: saName}
	}
	configs, cut, err := c.KEDAOperatorConfigs(ctx)
	out.CandidatesCut = cut
	var handler *keda.Config
	if err == nil {
		h := keda.HandlerOf(configs, ns, sc.Labels, sc.LabelsCut)
		switch {
		case h.Handler != nil:
			handler = h.Handler
			out.Handler, out.Version, out.Disagree = h.Handler.Namespace+"/"+h.Handler.Name, h.Handler.Version.String(), h.Handler.Disagree
		case h.None && !cut:
			out.None = true
		default:
			out.Candidates = h.Candidates
		}
	}
	get := c.saLookup(ctx)
	budget := kedaMaxExistence
	for _, ref := range sc.AuthRefs() {
		kind, aname := splitRef(ref)
		chk := KEDAAuthCheck{Kind: kind, Name: aname, Visible: map[string]string{}}
		resourceName, ans := keda.ResourceTriggerAuths, ns
		if kind == keda.KindClusterTriggerAuth {
			resourceName, ans = keda.ResourceClusterTriggerAuths, ""
		}
		ri := c.dyn.Resource(kedaGVR(resourceName))
		var au *unstructured.Unstructured
		rctx, rcancel := context.WithTimeout(ctx, kedaReadTimeout)
		if ans == "" {
			au, err = ri.Get(rctx, aname, metav1.GetOptions{})
		} else {
			au, err = ri.Namespace(ans).Get(rctx, aname, metav1.GetOptions{})
		}
		rcancel()
		switch {
		case apierrors.IsNotFound(err):
			chk.State = "not found"
		case err != nil:
			chk.State, chk.Why = "unverified", "the authentication could not be read"
		default:
			chk.State = "exists"
			a := keda.ProjectAuth(au, kind == keda.KindClusterTriggerAuth)
			if handler != nil {
				chk.Visible[out.Handler] = visibleWord(handler.SeesAuth(a.Labels, a.LabelsCut))
			} else {
				for i := range configs {
					chk.Visible[configs[i].Namespace+"/"+configs[i].Name] = visibleWord(configs[i].SeesAuth(a.Labels, a.LabelsCut))
				}
			}
			chk.Refs, chk.RefsCut, budget = c.kedaRefs(ctx, handler, a, budget)
			if a.PodIdentity != nil {
				var op *identity.SARef
				if handler != nil && handler.ServiceAccount != "" {
					op = &identity.SARef{Namespace: handler.Namespace, Name: handler.ServiceAccount}
				}
				s := identity.TriggerAuthSubject(a.PodIdentity, op, workloadSA, get)
				chk.Subject = &s
			}
		}
		out.Auths = append(out.Auths, chk)
	}
	return out, nil
}

func visibleWord(t keda.Tri) string {
	switch t {
	case keda.Yes:
		return "visible"
	case keda.No:
		return "excluded"
	}
	return "unverified"
}

func splitRef(ref string) (kind, name string) {
	for i := 0; i < len(ref); i++ {
		if ref[i] == '/' {
			return ref[:i], ref[i+1:]
		}
	}
	return ref, ""
}

// kedaRefs checks each referenced Secret and ConfigMap with a metadata-only
// read, at the namespace the handler reads it from (§6.2's table); never a
// value. Past the budget the rest are unverified, and the cut is said.
func (c *ClusterConn) kedaRefs(ctx context.Context, handler *keda.Config, a *keda.Auth, budget int) ([]KEDARef, bool, int) {
	var out []KEDARef
	cut := false
	for _, src := range a.Sources {
		for _, r := range src.Refs {
			if r.Kind != "Secret" && r.Kind != "ConfigMap" {
				continue
			}
			ref := KEDARef{Source: src.Kind, Kind: r.Kind, Name: r.Name, Key: r.Key}
			if handler == nil {
				ref.State, ref.Why = "unverified", "the handling KEDA operator is unverified, so the namespace KEDA reads it from is too"
				out = append(out, ref)
				continue
			}
			rns, ok := handler.ReadNamespace(a, r)
			if !ok {
				ref.State, ref.Why = "unverified", "the operator's namespace settings could not be resolved"
				out = append(out, ref)
				continue
			}
			ref.Namespace = rns
			if budget == 0 {
				cut = true
				ref.State, ref.Why = "unverified", "past the read limit"
				out = append(out, ref)
				continue
			}
			budget--
			rctx, cancel := context.WithTimeout(ctx, kedaReadTimeout)
			var err error
			if r.Kind == "Secret" {
				_, err = c.meta.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace(rns).Get(rctx, r.Name, metav1.GetOptions{})
			} else {
				_, err = c.meta.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace(rns).Get(rctx, r.Name, metav1.GetOptions{})
			}
			cancel()
			switch {
			case err == nil:
				ref.State = "exists"
			case apierrors.IsNotFound(err):
				ref.State = "not found"
			case apierrors.IsForbidden(err):
				ref.State, ref.Why = "unverified", "forbidden"
			default:
				ref.State, ref.Why = "unverified", "the read failed"
			}
			out = append(out, ref)
		}
	}
	return out, cut, budget
}

// targetServiceAccount is a ScaledObject's target's pod template
// ServiceAccount (else "default"), for Deployments and StatefulSets; nil
// when it cannot be read.
func (c *ClusterConn) targetServiceAccount(ctx context.Context, so *keda.ScaledObject) *identity.SARef {
	group, kind, name := so.Target()
	if group != "apps" {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, kedaReadTimeout)
	defer cancel()
	var sa string
	switch kind {
	case "Deployment":
		d, err := c.typed.AppsV1().Deployments(so.Namespace).Get(rctx, name, metav1.GetOptions{})
		if err != nil {
			return nil
		}
		sa = d.Spec.Template.Spec.ServiceAccountName
	case "StatefulSet":
		s, err := c.typed.AppsV1().StatefulSets(so.Namespace).Get(rctx, name, metav1.GetOptions{})
		if err != nil {
			return nil
		}
		sa = s.Spec.Template.Spec.ServiceAccountName
	default:
		return nil
	}
	if sa == "" {
		sa = "default"
	}
	return &identity.SARef{Namespace: so.Namespace, Name: sa}
}

func (c *ClusterConn) saLookup(ctx context.Context) identity.SALookup {
	return func(ns, name string) (*corev1.ServiceAccount, identity.Read) {
		rctx, cancel := context.WithTimeout(ctx, kedaReadTimeout)
		defer cancel()
		sa, err := c.typed.CoreV1().ServiceAccounts(ns).Get(rctx, name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			return nil, identity.ReadMissing
		case err != nil:
			return nil, identity.ReadUnreadable
		}
		return sa, identity.ReadFound
	}
}

// KEDAJob is one Job a ScaledJob created.
type KEDAJob struct {
	Name      string
	Created   time.Time
	Succeeded int32
	Failed    int32
	Active    int32
	Complete  bool
	FailedOut bool
}

// KEDAJobs is the ScaledJob's Jobs (S9 §6.4).
type KEDAJobs struct {
	Jobs []KEDAJob
	// Complete: the paginated read finished, so "the newest" is global;
	// otherwise they are the newest among those read.
	Complete  bool
	Read      int    // Jobs read (every page)
	Owned     int    // kept by the owner-UID filter
	Remaining *int64 // the server's remainingItemCount, when given
}

// KEDAJobsOf reads a ScaledJob's Jobs: label-selected pages of 200, at
// most 5, kept by controller UID (the label alone is not proof).
func (c *ClusterConn) KEDAJobsOf(ctx context.Context, ns, name, uid string) (KEDAJobs, error) {
	var out KEDAJobs
	if c.typed == nil {
		return out, errors.New("the cluster connection cannot read Jobs")
	}
	cont := ""
	var all []batchv1.Job
	for page := 0; page < kedaJobsPages; page++ {
		rctx, cancel := context.WithTimeout(ctx, kedaReadTimeout)
		list, err := c.typed.BatchV1().Jobs(ns).List(rctx, metav1.ListOptions{LabelSelector: "scaledjob.keda.sh/name=" + name, Limit: kedaJobsPage, Continue: cont})
		cancel()
		if err != nil {
			return out, err
		}
		out.Read += len(list.Items)
		for _, j := range list.Items {
			if controlledByUID(j.OwnerReferences, uid) {
				all = append(all, j)
			}
		}
		cont = list.Continue
		out.Remaining = list.RemainingItemCount
		if cont == "" {
			out.Complete = true
			break
		}
	}
	out.Owned = len(all)
	sort.Slice(all, func(i, j int) bool { return all[i].CreationTimestamp.After(all[j].CreationTimestamp.Time) })
	for i := range all {
		if i == kedaJobsShown {
			break
		}
		j := all[i]
		kj := KEDAJob{Name: j.Name, Created: j.CreationTimestamp.Time, Succeeded: j.Status.Succeeded, Failed: j.Status.Failed, Active: j.Status.Active}
		for _, c := range j.Status.Conditions {
			if c.Status == corev1.ConditionTrue {
				kj.Complete = kj.Complete || c.Type == batchv1.JobComplete
				kj.FailedOut = kj.FailedOut || c.Type == batchv1.JobFailed
			}
		}
		out.Jobs = append(out.Jobs, kj)
	}
	return out, nil
}

// ReplicaOwnership is who owns a workload's replica count (S9 §7), per
// axis: "owned", "unowned" (proven), or "unverified".
type ReplicaOwnership struct {
	Autoscaler    string
	AutoscalerWhy string
	Owners        []string
	GitOps        string
	GitOpsOwner   string
	GitOpsWhy     string
}

// ReplicaOwner joins the HPA and ScaledObject models, as of their last
// update, and the authoritative GitOps check.
func (c *ClusterConn) ReplicaOwner(ctx context.Context, kind, ns, name string) ReplicaOwnership {
	var hs []*hpa.HPA
	var sos []*keda.ScaledObject
	hpaProven, soProven := false, false
	if h := c.Health(); h != nil {
		hst, hComplete, hOK := h.VisitKind(healthsrc.KindHPAs, func(obj any) bool {
			if x, ok := obj.(*hpa.HPA); ok {
				hs = append(hs, x)
			}
			return true
		})
		sst, sComplete, sOK := h.VisitKind(healthsrc.KindScaledObjects, func(obj any) bool {
			if so, ok := obj.(*keda.ScaledObject); ok {
				sos = append(sos, so)
			}
			return true
		})
		hpaProven = hOK && hComplete && hst.Coverage == observe.Live && !hst.Truncated
		// KEDA's API not served: no ScaledObject can exist
		soProven = !c.kedaServed() || (sOK && sComplete && sst.Coverage == observe.Live && !sst.Truncated)
	}
	owners := replicaOwners(kind, ns, name, hs, sos, c.kedaVersion())
	return classifyOwnership(owners, hpaProven, soProven, c.GitOpsOwnership(ctx, "apps", kind, ns, name))
}

// replicaOwners names every autoscaler of one workload: an HPA scaling it
// (a KEDA-owned one with its ScaledObject), and a ScaledObject targeting
// it that no HPA names (a paused one has none).
func replicaOwners(kind, ns, name string, hs []*hpa.HPA, sos []*keda.ScaledObject, v keda.Version) []string {
	var owners []string
	viaHPA := map[string]bool{}
	for _, x := range hs {
		if !x.Scales(ns, "apps", kind, name) {
			continue
		}
		if so, ok := x.KEDAOwner(); ok {
			owners = append(owners, "KEDA ScaledObject "+so.Name+", through HPA "+x.Name)
			viaHPA[so.UID] = true
		} else {
			owners = append(owners, "HPA "+x.Name)
		}
	}
	for _, so := range sos {
		g, k, n := so.Target()
		if so.Namespace != ns || g != "apps" || k != kind || n != name || viaHPA[so.UID] {
			continue
		}
		label := "KEDA ScaledObject " + so.Name
		if keda.ScaledObjectIntent(so.Pause, v).Kind == keda.PauseFull {
			label += " is paused and will resume control"
		}
		owners = append(owners, label)
	}
	sort.Strings(owners)
	return owners
}

// classifyOwnership reads each axis as owned, proven unowned or
// unverified (S9 §7): an absence is proven only by complete, current
// reads, and GitOps only by the authoritative check without warnings.
// Labels are never proof either way.
func classifyOwnership(owners []string, hpaProven, soProven bool, g GitOpsOwnership) ReplicaOwnership {
	out := ReplicaOwnership{Autoscaler: "unverified", GitOps: "unverified"}
	switch {
	case len(owners) > 0:
		out.Autoscaler, out.Owners = "owned", owners
	case hpaProven && soProven:
		out.Autoscaler = "unowned"
	default:
		out.AutoscalerWhy = "the HPA or ScaledObject read is incomplete"
	}
	switch {
	case g.Basis != "":
		out.GitOps, out.GitOpsOwner = "owned", g.OwnerKind+" "+g.OwnerNamespace+"/"+g.OwnerName
	case len(g.Warnings) > 0:
		out.GitOpsWhy = g.Warnings[0]
	default:
		out.GitOps = "unowned"
	}
	return out
}

func (c *ClusterConn) kedaServed() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.caps.KEDA.Present
}

// KEDASetPaused pauses or resumes a scaler through autoscaling.keda.sh/
// paused only (S9 §8), UID-guarded. It is refused unless the handling
// operator is resolved and its version applies the annotation (2.12 for a
// ScaledObject, 2.11 for a ScaledJob); Resume only when paused is the
// scaler's only pause annotation.
func (c *ClusterConn) KEDASetPaused(ctx context.Context, resource, ns, name, uid string, pause bool) error {
	if c.dyn == nil {
		return errors.New("dynamic client unavailable")
	}
	kind := map[string]string{keda.ResourceScaledObjects: keda.KindScaledObject, keda.ResourceScaledJobs: keda.KindScaledJob}[resource]
	if kind == "" {
		return fmt.Errorf("not a KEDA scaler: %s", resource)
	}
	verb := "resume"
	if pause {
		verb = "pause"
	}
	gvr := kedaGVR(resource)
	u, err := c.checkArmed(ctx, gvr, kind, ns, name, uid)
	if err != nil {
		return fmt.Errorf("%s %w", verb, err)
	}
	configs, _, err := c.KEDAOperatorConfigs(ctx)
	if err != nil {
		return fmt.Errorf("%s is not offered: %v - nothing was written", verb, err)
	}
	h := keda.HandlerOf(configs, ns, labelsOf(u), false)
	if h.Handler == nil || !h.Handler.Version.Known {
		return fmt.Errorf("%s is not offered: the handling KEDA operator or its version is unverified, so Klyx cannot prove it applies %s - nothing was written", verb, keda.AnnPaused)
	}
	min := 12
	if resource == keda.ResourceScaledJobs {
		min = 11
	}
	if !h.Handler.Version.AtLeast(2, min) {
		return fmt.Errorf("%s is not offered: KEDA %s does not apply %s - nothing was written", verb, h.Handler.Version, keda.AnnPaused)
	}
	ann := u.GetAnnotations()
	var value any = "true"
	if !pause {
		for _, other := range []string{keda.AnnPausedReplicas, keda.AnnPausedScaleIn, keda.AnnPausedScaleOut} {
			if _, ok := ann[other]; ok {
				return fmt.Errorf("resume is not offered: %s is paused by %s, set outside Klyx - nothing was written", name, other)
			}
		}
		if _, ok := ann[keda.AnnPaused]; !ok {
			return fmt.Errorf("resume is not offered: %s has no %s annotation - nothing was written", name, keda.AnnPaused)
		}
		value = nil // a merge patch's null removes the key
	}
	body, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]any{keda.AnnPaused: value}}})
	if err != nil {
		return err
	}
	if err := c.patchArmed(ctx, gvr, kind, ns, name, uid, body); err != nil {
		return fmt.Errorf("%s %w", verb, err)
	}
	c.signalRefresh()
	return nil
}

func labelsOf(u *unstructured.Unstructured) map[string]string { return u.GetLabels() }
