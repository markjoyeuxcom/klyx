package appbridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/readfence"
)

// AI A3 §5.2 and §5.3: a run step's effect is established from this
// request's own witness - its receipt, its token, its pod's UID gone - on a
// fresh read and then a watch of that object, never polled; a state verb's
// effect is the predicate its prerequisite reads (stateEffect). Once a
// plan's last step took its effect, the incident is read from the
// Observer's verdicts. Missing or stale evidence is unverified, never
// established or resolved.
//
// Every read a verification or an incident makes - the cluster's and the
// Observer's - waits under one context: bounded from before the first
// read, and cancelled when the conversation's fence fails, so a call that
// never answers ends with the bound or the conversation (stage 4 review,
// finding 1). A positive result is published only if the fence still
// holds when it is recorded (finding 2).

// Variables, so a test can drive them.
var (
	verifyEventsMax = 10000
	incidentBound   = 10 * time.Minute
	verifyTick      = 2 * time.Second
	// effectBoundFor is the bound a verification waits for
	effectBoundFor = effectBound
)

const (
	incidentPending  = "pending"
	incidentResolved = "resolved"
	incidentNot      = "not resolved"
	incidentUnverif  = "unverified"
)

// objectWatcher is the pinned connection's watch (fleet.ChatWatch).
type objectWatcher interface {
	ChatWatch(ctx context.Context, gvr schema.GroupVersionResource, ns, name, resourceVersion string) (watch.Interface, error)
}

// effectBound is §1's bound for verb on the object as read; with no read,
// the most it can be.
func effectBound(verb string, u *unstructured.Unstructured) time.Duration {
	switch verb {
	case "flux-suspend", "flux-resume", "rollout-pause", "rollout-resume", "cordon", "uncordon":
		return 30 * time.Second
	case "flux-reconcile", "rollout-undo", "delete-pod":
		return 2 * time.Minute
	case "keda-pause", "keda-resume":
		return time.Minute
	case "scale":
		return 5 * time.Minute
	case "rollout-restart":
		if u != nil {
			if d, ok, _ := unstructured.NestedInt64(u.Object, "spec", "progressDeadlineSeconds"); ok && d > 0 && time.Duration(d)*time.Second < 10*time.Minute {
				return time.Duration(d) * time.Second
			}
		}
		return 10 * time.Minute
	}
	return time.Minute
}

// fenced is ctx cancelled once fence fails, checked each tick: a read
// blocked on the cluster or the Observer ends with the conversation.
func fenced(ctx context.Context, fence func() error) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	every := verifyTick // read here, not by the goroutine
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if fence() != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

// witness is what one step's effect is judged by.
type witness struct {
	sn      stepSnap
	written bool   // the request was written: its receipt is its own
	gen     int64  // the receipt's generation, or the first read showing this request's token
	token   string // the attempt's token (restart, reconcile)
	prior   int64  // the armed read's generation (restart)
	// holds is the state verb's effect on a read (stateEffect), its own
	// reads made under ctx
	holds func(ctx context.Context, u *unstructured.Unstructured) (bool, string)
}

const (
	judgePending = iota
	judgeEstablished
	judgeFailed
)

// judge reads one observation of the step's object.
func (w *witness) judge(ctx context.Context, u *unstructured.Unstructured) (int, string) {
	f := w.sn.fact
	if string(u.GetUID()) != f.UID {
		return judgeFailed, "the object was replaced"
	}
	gen := u.GetGeneration()
	observed, _, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	st := w.sn.step
	switch st.Verb {
	case "flux-reconcile":
		handled, _, _ := unstructured.NestedString(u.Object, "status", "lastHandledReconcileAt")
		requested := u.GetAnnotations()["reconcile.fluxcd.io/requestedAt"]
		switch {
		case w.token != "" && handled == w.token:
			return judgeEstablished, ""
		case requested != "" && requested != w.token && handled == requested && (w.written || tokenAfter(requested, w.token)):
			// a token written after this one was handled instead; an older
			// one (this request never landed) is not a supersession
			return judgeFailed, "superseded by another reconcile request"
		}
		return judgePending, "Flux has not handled this request's token"
	case "rollout-restart":
		if w.written && w.gen <= w.prior {
			return judgeFailed, "the restart wrote no new template"
		}
		tok, _, _ := unstructured.NestedString(u.Object, "spec", "template", "metadata", "annotations", "kubectl.kubernetes.io/restartedAt")
		if w.token == "" || tok != w.token {
			return judgePending, "the template does not carry this request's token"
		}
		if w.gen == 0 {
			w.gen = gen // a lost answer: the first read with its token
		}
		if observed < w.gen || gen < w.gen {
			return judgePending, "the controller has not observed the restart's generation"
		}
		if ok, why := rolledOut(f.Kind, u); !ok {
			return judgePending, why
		}
		return judgeEstablished, ""
	}
	if !stateVerb(st.Verb) || w.holds == nil {
		return judgeFailed, "Klyx has no effect for " + st.Verb
	}
	// a written request's effect is read only at its receipt's generation
	// or later (§5.2)
	if w.written && gen < w.gen {
		return judgePending, "the read predates the request's generation"
	}
	if ok, why := w.holds(ctx, u); !ok {
		return judgePending, why
	}
	return judgeEstablished, ""
}

// tokenAfter: both are RFC3339Nano times and a is the later.
func tokenAfter(a, b string) bool {
	ta, err1 := time.Parse(time.RFC3339Nano, a)
	tb, err2 := time.Parse(time.RFC3339Nano, b)
	return err1 == nil && err2 == nil && ta.After(tb)
}

// rolledOut: every pod is of the newest template, as the kind counts it,
// and some pod is desired - an empty set replaced no pod, so a count scaled
// to zero after the restart never establishes it (stage 4 review, finding
// 4).
func rolledOut(kind string, u *unstructured.Unstructured) (bool, string) {
	i := func(path ...string) int64 { n, _, _ := unstructured.NestedInt64(u.Object, path...); return n }
	none := "no pods are desired: Klyx cannot see the restart replace one"
	switch kind {
	case "Deployment":
		want := int64(1)
		if r, ok, _ := unstructured.NestedInt64(u.Object, "spec", "replicas"); ok {
			want = r
		}
		if want <= 0 {
			return false, none
		}
		return i("status", "updatedReplicas") == want && i("status", "replicas") == want, "the old pods are not all replaced"
	case "StatefulSet":
		want := i("spec", "replicas")
		if want <= 0 {
			return false, none
		}
		cur, _, _ := unstructured.NestedString(u.Object, "status", "currentRevision")
		upd, _, _ := unstructured.NestedString(u.Object, "status", "updateRevision")
		return cur != "" && cur == upd && i("status", "updatedReplicas") == want, "the old pods are not all replaced"
	case "DaemonSet":
		want := i("status", "desiredNumberScheduled")
		if want <= 0 {
			return false, none
		}
		return i("status", "updatedNumberScheduled") == want, "the old pods are not all replaced"
	}
	return false, "Klyx has no rollout counters for " + kind
}

// verifyFenceLocked is why a verification and an incident's reading end
// (§2.4): the investigation cleared or ended, its settings or proposals
// changed, or its pinned connection changed or disconnected; "" while it
// holds.
func (s *ChatService) verifyFenceLocked(c *conversation) string {
	if s.convs[c.key] != c {
		return "the investigation was cleared"
	}
	return s.planFenceLocked(c)
}

// verifyFence is verifyFenceLocked as a fence: the run fence's connection
// check first, then the conversation's, under the lock.
func (s *ChatService) verifyFence(c *conversation) func() error {
	return func() error {
		if err := s.runFence(c)(); err != nil {
			return err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if why := s.verifyFenceLocked(c); why != "" {
			return errors.New(why)
		}
		return nil
	}
}

// verifyStepEffect runs a step's verification and settles it (§5.2).
func (s *ChatService) verifyStepEffect(c *conversation, p *actionPlan, n int, sn stepSnap, res ActionResultDTO, ranAt time.Time) {
	fence := s.verifyFence(c)
	// one context for every read: fenced, and bounded by verifyObject and
	// verifyDeletion from before their first read
	ctx, stop := fenced(readfence.With(context.Background(), fence), fence)
	defer stop()
	w := &witness{sn: sn, written: res.Outcome == fleet.OutcomeWritten}
	if res.Receipt != nil {
		w.gen, w.prior, w.token = res.Receipt.Generation, res.Receipt.PriorGeneration, res.Receipt.Token
	}
	if res.Attempt != nil && w.token == "" {
		w.token = res.Attempt.Token
	}
	w.holds = func(ctx context.Context, u *unstructured.Unstructured) (bool, string) {
		return s.stateEffect(ctx, c.conn, sn.step, sn.fact, sn.partner, u, ranAt)
	}
	var effect, why string
	if sn.step.Verb == "delete-pod" {
		effect, why = s.verifyDeletion(ctx, c.conn, sn, fence)
	} else {
		effect, why = s.verifyObject(ctx, c.conn, w, fence)
	}
	if err := fence(); err != nil {
		// a fence that failed during a read: whatever the read returned,
		// the verification ends with the fence's reason (§2.4)
		effect, why = stepEffectUnverified, err.Error()
	}
	if effect == stepEffectEstablished && res.Outcome == fleet.OutcomeUnverified {
		// the request's answer was lost: a state effect may be another
		// writer's, a deletion another actor's (§1)
		if sn.step.Verb == "delete-pod" {
			why = joinWhy("the pod was removed; by this request or another actor", why)
		} else if stateVerb(sn.step.Verb) {
			why = "the requested state, by this request or another writer"
		}
	}
	s.settleEffect(c, p, n, effect, why)
	s.maybeIncident(c, p)
}

func joinWhy(a, b string) string {
	if b == "" {
		return a
	}
	return a + "; " + b
}

// verifyObject is the fresh read and the single-object watch: bounded
// from before the first read by the most the verb's bound can be, then by
// its bound for the object as read - both measured from the same start, so
// a slow read never earns its time back (stage 4 re-review). An effect is
// established only if it was read before the end, and while the fence
// holds.
func (s *ChatService) verifyObject(ctx context.Context, conn ChatConn, w *witness, fence func() error) (string, string) {
	f := w.sn.fact
	gvr := factGVR(f)
	started := time.Now()
	ctx, cancelAll := context.WithDeadline(ctx, started.Add(effectBoundFor(w.sn.step.Verb, nil)))
	defer cancelAll()
	first, err := conn.ChatGet(ctx, gvr, f.Namespace, f.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return stepEffectUnverified, "the object is gone"
		}
		return stepEffectUnverified, notWithin(ctx, effectBoundFor(w.sn.step.Verb, nil), "", "the read failed: "+chatScrub(readError(err), nil))
	}
	bound := effectBoundFor(w.sn.step.Verb, first)
	end := started.Add(bound)
	ctx, cancel := context.WithDeadline(ctx, end)
	defer cancel()
	// established holds only if it was read before the end, and the fence
	// held through the read
	established := func(last string) (string, string) {
		if err := fence(); err != nil {
			return stepEffectUnverified, err.Error()
		}
		if ctx.Err() != nil || !time.Now().Before(end) {
			return stepEffectUnverified, notWithin(ctx, bound, last, "")
		}
		return stepEffectEstablished, ""
	}
	ow, ok := conn.(objectWatcher)
	u, events, last := first, 0, ""
	for {
		switch got, why := w.judge(ctx, u); got {
		case judgeEstablished:
			return established(last)
		case judgeFailed:
			return stepEffectUnverified, why
		default:
			last = why
		}
		if !ok {
			return stepEffectUnverified, "Klyx cannot watch this connection: " + last
		}
		wi, err := ow.ChatWatch(ctx, gvr, f.Namespace, f.Name, u.GetResourceVersion())
		if err != nil {
			return stepEffectUnverified, notWithin(ctx, bound, last, "the watch failed: "+chatScrub(readError(err), nil))
		}
		_, why, done := s.drainObject(ctx, wi, w, u, &events, &last, fence)
		wi.Stop()
		switch {
		case done && why == "":
			return established(last)
		case done:
			return stepEffectUnverified, why
		case ctx.Err() != nil:
			return stepEffectUnverified, notWithin(ctx, bound, last, "")
		}
		// the server closed the watch before the bound: read again, watch
		// again from the fresh read
		fresh, err := conn.ChatGet(ctx, gvr, f.Namespace, f.Name)
		if err != nil {
			return stepEffectUnverified, notWithin(ctx, bound, last, "the read failed: "+chatScrub(readError(err), nil))
		}
		u = fresh
	}
}

// drainObject judges a watch's events until the effect holds, fails, the
// watch ends or the bound does; cur is the object as last seen, and last
// takes each pending judgement's reason, for the bound's words. Each tick
// checks the fence, and a KEDA pause is judged again: its completion is the
// Observer's verdict, an in-memory read, which no event of the object
// announces.
func (s *ChatService) drainObject(ctx context.Context, wi watch.Interface, w *witness, cur *unstructured.Unstructured, events *int, last *string, fence func() error) (next *unstructured.Unstructured, why string, done bool) {
	tick := time.NewTicker(verifyTick)
	defer tick.Stop()
	next = cur
	judge := func(u *unstructured.Unstructured) (string, bool) {
		switch got, why := w.judge(ctx, u); got {
		case judgeEstablished:
			return "", true
		case judgeFailed:
			return why, true
		default:
			*last = why
		}
		return "", false
	}
	for {
		select {
		case <-ctx.Done():
			return next, "", false
		case <-tick.C:
			if err := fence(); err != nil {
				return next, err.Error(), true
			}
			if w.sn.step.Verb == "keda-pause" {
				if why, done := judge(next); done {
					return next, why, true
				}
			}
		case ev, open := <-wi.ResultChan():
			if !open {
				return next, "", false
			}
			if err := fence(); err != nil {
				return next, err.Error(), true
			}
			*events++
			if *events > verifyEventsMax {
				return next, "too many changes to verify", true
			}
			switch ev.Type {
			case watch.Deleted:
				return next, "the object was deleted", true
			case watch.Error:
				return next, "the watch expired: " + statusText(ev.Object), true
			case watch.Added, watch.Modified:
				u, ok := ev.Object.(*unstructured.Unstructured)
				if !ok {
					continue
				}
				next = u
				if why, done := judge(u); done {
					return next, why, true
				}
			}
		}
	}
}

func statusText(o any) string {
	if st, ok := o.(*metav1.Status); ok {
		return chatScrub(st.Message, nil)
	}
	return "an error event"
}

func notWithin(ctx context.Context, bound time.Duration, last, failure string) string {
	if failure != "" && ctx.Err() == nil {
		return failure
	}
	why := fmt.Sprintf("not established within %s", bound)
	if last != "" {
		why += ": " + last
	}
	return why
}

// verifyDeletion is delete-pod's witness (§5.2): the pod's UID gone, and,
// for an owner that keeps a count, a pod of that owner created after the
// step began - from one unfiltered namespace watch from the LIST taken
// before the delete, so the old pod's DELETED and the replacement's events
// come in one sequence (§14). A Job's pod or an ownerless one is
// established by its removal alone, and says what is not established.
func (s *ChatService) verifyDeletion(ctx context.Context, conn ChatConn, sn stepSnap, fence func() error) (string, string) {
	f := sn.fact
	ow, ok := conn.(objectWatcher)
	if !ok || sn.podsRV == "" {
		return stepEffectUnverified, "Klyx cannot watch the pod's namespace"
	}
	bound := effectBoundFor("delete-pod", nil)
	end := time.Now().Add(bound)
	ctx, cancel := context.WithDeadline(ctx, end)
	defer cancel()
	ownerKind, ownerUID := "", ""
	if f.Object != nil {
		ownerKind, ownerUID = f.Object.OwnerKind, f.Object.OwnerUID
	}
	counting := ownerUID != "" && (ownerKind == "ReplicaSet" || ownerKind == "StatefulSet" || ownerKind == "DaemonSet")
	removedOnly := "no controller owns it: it is not recreated"
	if ownerUID != "" {
		removedOnly = fmt.Sprintf("its %s's recreation is not established", ownerKind)
	}
	wi, err := ow.ChatWatch(ctx, podsGVRv1, f.Namespace, "", sn.podsRV)
	if err != nil {
		return stepEffectUnverified, "the watch failed: " + chatScrub(readError(err), nil)
	}
	defer wi.Stop()
	tick := time.NewTicker(verifyTick)
	defer tick.Stop()
	deleted, replacement, events := false, "", 0
	for {
		if deleted && (!counting || replacement != "") {
			if err := fence(); err != nil {
				return stepEffectUnverified, err.Error()
			}
			// an event that won the race against the end is past it
			if ctx.Err() != nil || !time.Now().Before(end) {
				return stepEffectUnverified, fmt.Sprintf("not established within %s", bound)
			}
			if !counting {
				return stepEffectEstablished, removedOnly
			}
			return stepEffectEstablished, ""
		}
		select {
		case <-ctx.Done():
			if !deleted {
				return stepEffectUnverified, fmt.Sprintf("not established within %s: the pod's UID is not gone", bound)
			}
			return stepEffectUnverified, fmt.Sprintf("not established within %s: no replacement of its %s was created", bound, ownerKind)
		case <-tick.C:
			if err := fence(); err != nil {
				return stepEffectUnverified, err.Error()
			}
		case ev, open := <-wi.ResultChan():
			if !open {
				return stepEffectUnverified, "the watch ended before the effect was established"
			}
			if err := fence(); err != nil {
				return stepEffectUnverified, err.Error()
			}
			if events++; events > verifyEventsMax {
				return stepEffectUnverified, "the namespace was too busy to verify"
			}
			if ev.Type == watch.Error {
				return stepEffectUnverified, "the watch expired: " + statusText(ev.Object)
			}
			u, ok := ev.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			uid := string(u.GetUID())
			switch ev.Type {
			case watch.Deleted:
				if uid == f.UID {
					deleted = true
				}
				if uid == replacement {
					replacement = "" // removed again: not present
				}
			case watch.Added:
				// a creation after the LIST; adoption arrives as MODIFIED
				if counting && replacement == "" && uid != f.UID && controllerUID(u) == ownerUID {
					replacement = uid
				}
			}
		}
	}
}

// IncidentSubjectDTO is one of the incident's subjects and its reading
// (§5.3).
type IncidentSubjectDTO struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	Reading   string `json:"reading"`
	Verdict   string `json:"verdict,omitempty"`
	At        string `json:"at,omitempty"`
}

// IncidentDTO is the incident's outcome: pending, resolved, not resolved
// or unverified, with why.
type IncidentDTO struct {
	State    string               `json:"state"`
	Why      string               `json:"why,omitempty"`
	Subjects []IncidentSubjectDTO `json:"subjects"`
}

// incidentSubject is a subject with what its readings need: the step whose
// effect intends it held, with that step's verb and parameters, and the
// delete-pod step that removed it, with whether its answer was lost.
type incidentSubject struct {
	fact        actionFact
	held        int
	holdStep    ActionStepDTO
	removedBy   int
	removedLost bool
}

// maybeIncident starts the incident's reading once every offered step ran
// with its effect established (§5.3).
func (s *ChatService) maybeIncident(c *conversation, p *actionPlan) {
	s.mu.Lock()
	if p.incident != nil || p.running > 0 {
		s.mu.Unlock()
		return
	}
	var last time.Time
	ran := 0
	for i, st := range p.dto.Steps {
		if st.State != stepOffered {
			continue
		}
		r := p.runs[i]
		if r.request == "" || r.effect != stepEffectEstablished {
			s.mu.Unlock()
			return
		}
		ran++
		if r.ranAt.After(last) {
			last = r.ranAt
		}
	}
	if ran == 0 {
		s.mu.Unlock()
		return
	}
	subjects := incidentSubjectsLocked(c, p)
	p.incident = &IncidentDTO{State: incidentPending, Subjects: []IncidentSubjectDTO{}}
	s.retainLocked(c)
	s.version++
	s.mu.Unlock()
	s.emitPlan(c)
	safeGo("investigation-incident", func() { s.readIncident(c, p, subjects, last) })
}

// incidentSubjectsLocked: the anchor, and each step's target Klyx's checks
// found failing in this investigation, by UID.
func incidentSubjectsLocked(c *conversation, p *actionPlan) []incidentSubject {
	failing := map[string]bool{}
	for _, r := range ledgerLocked(c) {
		for _, ch := range r.Checks {
			if ch.Failing && ch.Subject.UID != "" {
				failing[ch.Subject.UID] = true
			}
		}
	}
	var out []incidentSubject
	seen := map[string]bool{}
	add := func(f actionFact) {
		if f.UID == "" || seen[f.UID] {
			return
		}
		seen[f.UID] = true
		sub := incidentSubject{fact: f}
		for i, st := range p.dto.Steps {
			if p.facts[i].UID != f.UID || p.runs[i].request == "" {
				continue
			}
			switch {
			case st.Verb == "delete-pod":
				sub.removedBy, sub.removedLost = i+1, p.runs[i].request == fleet.OutcomeUnverified
			case st.Verb == "flux-suspend", st.Verb == "keda-pause", st.Verb == "cordon", st.Verb == "scale" && st.Replicas != nil && *st.Replicas == 0:
				sub.held, sub.holdStep = i+1, st
			}
		}
		out = append(out, sub)
	}
	if c.anchor != nil {
		if f, ok := c.facts.byUID[c.anchor.UID]; ok {
			add(*f)
		} else {
			for _, f := range p.facts {
				if f.UID == c.anchor.UID {
					add(f)
				}
			}
			if !seen[c.anchor.UID] {
				add(actionFact{Kind: "the investigated object", Name: c.anchor.Name, Namespace: c.anchor.Namespace, UID: c.anchor.UID})
			}
		}
	}
	for _, f := range p.facts {
		if failing[f.UID] {
			add(f)
		}
	}
	return out
}

// holdShown says the fresh captured evidence shows the step's own hold,
// not another pause (stage 4 review, finding 5): a Flux unit suspended, a
// workload scaled to zero, a node cordoned, a KEDA pause completed. A
// rollout pause never reads paused, so it holds nothing here.
func holdShown(st ActionStepDTO, sv subjectVerdict) bool {
	if sv.state != "paused" {
		return false
	}
	switch st.Verb {
	case "flux-suspend":
		return sv.v.Label == "Suspended"
	case "scale":
		return sv.v.Reason == "ScaledToZero"
	case "cordon":
		return sv.v.Reason == "Cordoned"
	case "keda-pause":
		return sv.completed
	}
	return false
}

// readIncident reads the subjects' verdicts from the Observer until the
// incident resolves or its bound ends (§5.3): never a new API read.
func (s *ChatService) readIncident(c *conversation, p *actionPlan, subjects []incidentSubject, lastWrite time.Time) {
	// the bound by the service's clock, which stamped the write; the
	// context's own timeout ends a blocked read by the wall clock
	deadline := lastWrite.Add(incidentBound)
	fence := s.verifyFence(c)
	// a blocked read waits only the time left until the bound
	ctx, cancel := context.WithTimeout(context.Background(), deadline.Sub(s.now()))
	defer cancel()
	ctx, stop := fenced(ctx, fence)
	defer stop()
	tick := time.NewTicker(verifyTick)
	defer tick.Stop()
	for {
		if err := fence(); err != nil {
			s.settleIncident(c, p, incidentUnverif, err.Error(), nil)
			return
		}
		cache := map[string]*chatVerdicts{}
		rows, recovered, open, faulted := readSubjects(func(f actionFact) subjectVerdict { return s.verdictOf(ctx, c.conn, f, cache) }, subjects, lastWrite)
		if err := fence(); err != nil {
			// the fence failed during the reading: its reason, not the bound's
			s.settleIncident(c, p, incidentUnverif, err.Error(), rows)
			return
		}
		// a reading that finished past the bound resolves nothing (stage 4
		// re-review): the bound is decided first
		switch {
		case !s.now().Before(deadline) || ctx.Err() != nil:
			if faulted {
				s.settleIncident(c, p, incidentNot, fmt.Sprintf("a subject is not recovered %s after the last step", incidentBound), rows)
			} else {
				s.settleIncident(c, p, incidentUnverif, "Klyx holds no fresh reading that resolves every subject within the bound", rows)
			}
			return
		case open == 0 && recovered > 0:
			s.settleIncident(c, p, incidentResolved, "every subject recovered or is held as intended", rows)
			return
		}
		s.mu.Lock()
		changed := !reflect.DeepEqual(p.incident.Subjects, rows)
		if changed {
			p.incident.Subjects = rows // a new reading publishes; an unchanged one does not
			s.retainLocked(c)
			s.version++
		}
		s.mu.Unlock()
		if changed {
			s.emitPlan(c)
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
		}
	}
}

// readSubjects is one reading of every subject: how many recovered, how
// many are still open, and whether a fresh reading shows a failure in
// force. A reading, an absence included, is fresh only if its capture was
// evaluated after the last write, by the capture's own clock (stage 4
// review, finding 3).
func readSubjects(verdict func(actionFact) subjectVerdict, subjects []incidentSubject, lastWrite time.Time) ([]IncidentSubjectDTO, int, int, bool) {
	rows := []IncidentSubjectDTO{}
	recovered, open, faulted := 0, 0, false
	for _, sub := range subjects {
		f := sub.fact
		row := IncidentSubjectDTO{Kind: f.Kind, Namespace: f.Namespace, Name: f.Name, UID: f.UID}
		switch {
		case sub.removedBy > 0:
			if sub.removedLost {
				row.Reading = fmt.Sprintf("removed, by step %d's request or another actor: it cannot itself recover; its replacement is shown on its own", sub.removedBy)
			} else {
				row.Reading = fmt.Sprintf("removed by step %d: it cannot itself recover; its replacement is shown on its own", sub.removedBy)
			}
			open++
		case f.Resource == "":
			row.Reading = "unverified: Klyx holds no read of it"
			open++
		default:
			// the verdict's state whichever classifier made it: a verdict
			// part's Kind, a native part's Role (stage 3 re-review 1)
			sv := verdict(f)
			v, state := sv.v, sv.state
			after := !sv.at.IsZero() && sv.at.After(lastWrite)
			fresh := sv.found && v.Coverage == "live" && after
			if sv.found {
				row.Verdict, row.At = state, v.EvaluatedAt
			}
			switch {
			case sv.replaced && after:
				row.Reading = "unverified: replaced"
				open++
			case sv.gone && after:
				row.Reading = "unverified: gone"
				open++
			case !fresh:
				row.Reading = "pending: no fresh reading"
				open++
			case state == "ok":
				row.Reading = "recovered"
				recovered++
			case state == "paused" && sub.held > 0 && holdShown(sub.holdStep, sv):
				row.Reading = fmt.Sprintf("held by step %d, as intended", sub.held)
			case state == "paused":
				row.Reading = "unverified: paused outside this plan"
				open++
			case state == "progressing":
				row.Reading = "pending: still progressing"
				open++
				faulted = true
			case state == "fault", state == "degraded":
				row.Reading = "pending: " + state + ", a failure in force"
				open++
				faulted = true
			default:
				row.Reading = "pending: " + orText(state, "unknown")
				open++
			}
		}
		rows = append(rows, row)
	}
	return rows, recovered, open, faulted
}

// settleIncident records the incident's outcome. A resolution is recorded
// only if the fence still holds (stage 4 review, finding 2).
func (s *ChatService) settleIncident(c *conversation, p *actionPlan, state, why string, rows []IncidentSubjectDTO) {
	defer s.emitPlan(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	if state == incidentResolved {
		if f := s.verifyFenceLocked(c); f != "" {
			state, why = incidentUnverif, f
		}
	}
	p.incident.State, p.incident.Why = state, why
	if rows != nil {
		p.incident.Subjects = rows
	}
	s.retainLocked(c)
	s.version++
}
