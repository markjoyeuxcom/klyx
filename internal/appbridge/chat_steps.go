package appbridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/readfence"
	"github.com/moomora/klyx/internal/workloads"
)

// AI A3 §4: a step of the current plan runs only when the operator reviews
// it, then confirms it, through the manual verb's own code on the
// conversation's pinned connection. ReviewStep reads the target, an undo's
// partner and every standing prerequisite again, and words the change, the
// consequence and the expected result from those reads; RunStep consumes
// the protected grant first, reads the same again, refuses a material
// change, and dispatches to the verb's inner function. Klyx never retries
// and never compensates.

const (
	reviewTokenTTL        = 2 * time.Minute
	stepEffectPending     = "pending"
	stepEffectEstablished = "established"
	stepEffectUnverified  = "unverified"
	planStatusSuperseded  = "superseded"
	planStatusWithdrawn   = "withdrawn"
	planStatusDeclined    = "declined"
	planStatusHeld        = "held"
	// a step's record serializes to at most 1 KiB (§2.4): its texts are
	// capped by their JSON encoding, and stepRunBytes is what retention
	// counts for the rest of it
	stepErrMax   = 512
	stepWhyMax   = 352
	stepRunBytes = 128
)

// stepRun is one step's review and run, kept by its plan.
type stepRun struct {
	review    *stepReview
	request   string // written | refused | unverified; "" until it runs
	notSent   bool
	errText   string
	effect    string // pending | established | unverified; "" until it runs
	effectWhy string
	ranAt     time.Time
}

// stepReview is the latest review: its token, the digest of the facts its
// words rest on, and when it expires.
type stepReview struct {
	token   string
	digest  string
	expires time.Time
}

// StepRunStateDTO is a step's run as the card shows it.
type StepRunStateDTO struct {
	Request   string `json:"request,omitempty"`
	NotSent   bool   `json:"notSent,omitempty"`
	Error     string `json:"error,omitempty"`
	Effect    string `json:"effect,omitempty"`
	EffectWhy string `json:"effectWhy,omitempty"`
	RanAt     string `json:"ranAt,omitempty"`
}

// StepObservedDTO is one fact the review rests on, with when it was read.
type StepObservedDTO struct {
	What string `json:"what"`
	At   string `json:"at"`
}

// StepReviewDTO is what the ConfirmDialog shows for one step (§4.1).
type StepReviewDTO struct {
	Offered     bool              `json:"offered"`
	Reason      string            `json:"reason,omitempty"`
	Token       string            `json:"token,omitempty"`
	ExpiresAt   string            `json:"expiresAt,omitempty"`
	N           int               `json:"n"`
	Verb        string            `json:"verb"`
	Target      ChatCheckSubject  `json:"target"`
	Change      string            `json:"change,omitempty"`
	Consequence []string          `json:"consequence,omitempty"`
	Expected    string            `json:"expected,omitempty"`
	Danger      bool              `json:"danger"`
	Observed    []StepObservedDTO `json:"observed,omitempty"`
}

// StepRunDTO is RunStep's answer: why nothing ran, or the request's result,
// and the plan as it now stands.
type StepRunDTO struct {
	Ran    bool             `json:"ran"`
	Reason string           `json:"reason,omitempty"`
	Result *ActionResultDTO `json:"result,omitempty"`
	Plan   *ActionPlanDTO   `json:"plan,omitempty"`
}

// stepSnap is what a review or run needs of its plan, copied under the lock.
type stepSnap struct {
	// podsRV is delete-pod's collection resourceVersion, read before the
	// delete (§5.2)
	podsRV  string
	n       int
	step    ActionStepDTO
	fact    actionFact
	partner *fleet.RevisionExpect
	anchor  string // the investigated object's UID
	earlier []stepSnapPrior
}

// stepSnapPrior is an earlier step whose state effect stands (§4.3).
type stepSnapPrior struct {
	n       int
	verb    string
	fact    actionFact
	step    ActionStepDTO
	partner *fleet.RevisionExpect // an undo's pinned revision
}

// matrixBounds are §1's bounds, in the card's words.
var matrixBounds = map[string]string{
	"flux-suspend": "30 s", "flux-resume": "30 s", "flux-reconcile": "2 min", "rollout-restart": "the rollout's progress deadline, at most 10 min",
	"scale": "5 min", "rollout-undo": "2 min", "rollout-pause": "30 s", "rollout-resume": "30 s", "delete-pod": "2 min",
	"cordon": "30 s", "uncordon": "30 s", "keda-pause": "1 min", "keda-resume": "1 min",
}

// planLocked finds an investigation's plan by its exchange, and whether it
// is the conversation's current one.
func (s *ChatService) planLocked(cluster, conversationID, exchangeID string) (*conversation, *actionPlan, string) {
	c := s.convs[invKey(cluster)]
	if c == nil || c.id != conversationID || c.kind != kindInvestigation {
		return nil, nil, "the investigation is gone: start a new one"
	}
	var plan *actionPlan
	current := true
	for _, ex := range c.exchanges {
		if ex.id == exchangeID {
			plan, current = ex.plan, true
			continue
		}
		if plan != nil && ex.plan != nil && len(ex.plan.dto.Steps) > 0 {
			current = false // a later proposal supersedes it (§2.4)
		}
	}
	if plan == nil {
		return c, nil, "that turn proposed no plan"
	}
	if !current {
		return c, plan, "a later proposal superseded this plan"
	}
	return c, plan, ""
}

// offeredLocked says why step n cannot be reviewed or run now, or "": a
// fenced investigation's unrun steps are withdrawn (stage 2 review,
// finding 1) before anything else is decided.
func (s *ChatService) offeredLocked(c *conversation, p *actionPlan, n int) string {
	s.withdrawFencedLocked(c)
	if p.withdrawn != "" {
		return "withdrawn: " + p.withdrawn
	}
	if !p.settled {
		return "the turn that proposed the plan is still running"
	}
	switch {
	case p.declined:
		return "the plan was dismissed"
	case n < 1 || n > len(p.dto.Steps):
		return fmt.Sprintf("the plan has no step %d", n)
	case p.dto.Steps[n-1].State != stepOffered:
		return fmt.Sprintf("step %d is %s: %s", n, p.dto.Steps[n-1].State, p.dto.Steps[n-1].Reason)
	case c.stepRunning == p && p.running > 0:
		return fmt.Sprintf("step %d is running", p.running)
	case c.stepRunning != nil:
		// one running step per conversation, whichever plan holds it
		// (stage 3 review, finding 2)
		return fmt.Sprintf("step %d of an earlier plan is running", c.stepRunning.running)
	case p.runs[n-1].request != "":
		return fmt.Sprintf("step %d has run", n)
	}
	for j := 0; j < n-1; j++ {
		r := p.runs[j]
		switch {
		case r.request == "":
			return fmt.Sprintf("step %d has not run", j+1)
		case r.effect != stepEffectEstablished:
			return fmt.Sprintf("step %d's effect is not established (%s)", j+1, orText(r.effectWhy, r.effect))
		}
	}
	return ""
}

func orText(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// snapLocked copies what step n's review or run reads.
func snapLocked(c *conversation, p *actionPlan, n int) stepSnap {
	sn := stepSnap{n: n, step: p.dto.Steps[n-1], fact: p.facts[n-1], partner: p.partner[n-1]}
	if c.anchor != nil {
		sn.anchor = c.anchor.UID
	}
	for j := 0; j < n-1; j++ {
		st := p.dto.Steps[j]
		if !stateVerb(st.Verb) {
			continue
		}
		sn.earlier = append(sn.earlier, stepSnapPrior{n: j + 1, verb: st.Verb, fact: p.facts[j], step: st, partner: p.partner[j]})
	}
	return sn
}

// stateVerb: its effect is a requested state that later steps stand on
// (§4.3); an event verb's effect is history.
func stateVerb(verb string) bool {
	switch verb {
	case "flux-reconcile", "rollout-restart", "delete-pod":
		return false
	}
	return true
}

// runFence is RunStep's fence (§4.4): the conversation's pinned connection
// is still the registry's and usable. AI settings do not fence the write.
func (s *ChatService) runFence(c *conversation) func() error {
	return func() error {
		conn, usable := s.state(c.cluster)
		if conn == nil || conn != c.conn || !usable {
			return errors.New("the cluster's connection changed or disconnected; nothing more is sent on it")
		}
		return nil
	}
}

// observation is what a review read: the words the card shows, and the
// digest of the facts those words rest on.
type observation struct {
	change      string
	consequence []string
	expected    string
	observed    []StepObservedDTO
	digest      string
}

func factGVR(f actionFact) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: f.Group, Version: f.Version, Resource: f.Resource}
}

// observe reads a step's target, an undo's partner and its standing
// prerequisites again, through the fenced pinned connection, and words the
// step from those reads. A reason means the step is not offered.
func (s *ChatService) observe(ctx context.Context, conn ChatConn, sn stepSnap) (observation, string) {
	var ob observation
	now := s.now().UTC().Format(time.RFC3339)
	f := sn.fact
	who := fmt.Sprintf("%s %s", f.Kind, strings.TrimPrefix(f.Namespace+"/"+f.Name, "/"))
	u, err := conn.ChatGet(ctx, factGVR(f), f.Namespace, f.Name)
	if err != nil {
		return ob, fmt.Sprintf("Klyx could not read %s again: %s", who, chatScrub(readError(err), nil))
	}
	if string(u.GetUID()) != f.UID {
		return ob, fmt.Sprintf("%s was replaced (another object holds the name now): nothing was sent", who)
	}
	fresh := factOf(factGVR(f), u, "review", now, acquisition())
	fresh.History = f.History
	store := newActionFacts()
	store.add(fresh)
	in := stepInputOf(sn.step)
	if _, _, why := eligible(in, sn.step, store); why != "" {
		return ob, why
	}
	type digestFact struct {
		UID        string       `json:"uid"`
		Generation int64        `json:"generation"`
		Object     *objectFacts `json:"object"`
		Partner    string       `json:"partner,omitempty"`
	}
	strip := func(o *objectFacts) *objectFacts { c := *o; c.ReadID, c.ReadAt = "", ""; return &c }
	target := digestFact{UID: fresh.UID, Generation: u.GetGeneration(), Object: strip(fresh.Object)}
	ob.observed = append(ob.observed, StepObservedDTO{What: fmt.Sprintf("%s at generation %d", who, u.GetGeneration()), At: now})

	if sn.step.Verb == "rollout-undo" {
		pd, why := s.observePartner(ctx, conn, f, sn.partner)
		if why != "" {
			return ob, why
		}
		target.Partner = pd
		ob.observed = append(ob.observed, StepObservedDTO{What: fmt.Sprintf("revision %d's %s %s, its template unchanged", f.History.PartnerRevision, partnerKind(f.Kind), f.History.PartnerObject), At: now})
	}
	digest := []any{target}
	for _, pr := range sn.earlier {
		pf, why := s.observePrior(ctx, conn, pr)
		if why != "" {
			return ob, why
		}
		digest = append(digest, pf)
		ob.observed = append(ob.observed, StepObservedDTO{What: fmt.Sprintf("step %d's effect still holds on %s %s", pr.n, pr.fact.Kind, pr.fact.Name), At: now})
	}
	ob.change = changeWords(sn, fresh)
	ob.consequence = s.consequenceWords(ctx, conn, sn, fresh)
	ob.expected = expectedWords(sn) + ", within " + matrixBounds[sn.step.Verb]
	// the words the operator confirms are digested with the facts: an owner
	// that changed, a suspension that ended or an ownership read that is no
	// longer complete words another consequence, so it needs a new review
	// (stage 3 review, finding 1)
	digest = append(digest, map[string]any{"change": ob.change, "consequence": ob.consequence})
	b, _ := json.Marshal(digest)
	sum := sha256.Sum256(b)
	ob.digest = hex.EncodeToString(sum[:])
	return ob, ""
}

func stepInputOf(st ActionStepDTO) actionStepInput {
	var in actionStepInput
	in.Verb = st.Verb
	in.Target.Kind, in.Target.Namespace, in.Target.Name = st.Target.Kind, st.Target.Namespace, st.Target.Name
	if st.Replicas != nil || st.Revision != nil {
		in.Params = &struct {
			Replicas *int   `json:"replicas"`
			Revision *int64 `json:"revision"`
		}{st.Replicas, st.Revision}
	}
	return in
}

func partnerKind(workload string) string {
	if workload == "Deployment" {
		return "ReplicaSet"
	}
	return "ControllerRevision"
}

// observePartner reads an undo's partner revision object again: the same
// object, with the same template (§4.1).
func (s *ChatService) observePartner(ctx context.Context, conn ChatConn, f actionFact, want *fleet.RevisionExpect) (string, string) {
	h := f.History
	if h == nil || want == nil || h.PartnerObject == "" {
		return "", "Klyx holds no partner revision to read again"
	}
	gvr, path := replicaSetsGVR, []string{"spec", "template"}
	if f.Kind != "Deployment" {
		gvr, path = controllerRevisionsGVR, []string{"data", "spec", "template"}
	}
	u, err := conn.ChatGet(ctx, gvr, f.Namespace, h.PartnerObject)
	if err != nil {
		return "", fmt.Sprintf("revision %d (%s %s) could not be read again: %s", h.PartnerRevision, partnerKind(f.Kind), h.PartnerObject, chatScrub(readError(err), nil))
	}
	tm, ok, _ := unstructured.NestedMap(u.Object, path...)
	d := ""
	if ok {
		d, _ = workloads.TemplateDigest(tm, f.Kind == "Deployment")
	}
	if string(u.GetUID()) != want.ObjectUID || d == "" || d != want.TemplateDigest {
		return "", fmt.Sprintf("revision %d is gone or changed since rollout_history read it: nothing was sent", h.PartnerRevision)
	}
	return d, ""
}

// observePrior reads an earlier step's target again and checks its state
// effect still holds (§4.3).
func (s *ChatService) observePrior(ctx context.Context, conn ChatConn, pr stepSnapPrior) (any, string) {
	f := pr.fact
	who := fmt.Sprintf("%s %s", f.Kind, strings.TrimPrefix(f.Namespace+"/"+f.Name, "/"))
	u, err := conn.ChatGet(ctx, factGVR(f), f.Namespace, f.Name)
	if err != nil {
		return nil, fmt.Sprintf("step %d's effect could not be read again (%s): %s", pr.n, who, chatScrub(readError(err), nil))
	}
	if string(u.GetUID()) != f.UID {
		return nil, fmt.Sprintf("step %d's %s was replaced since it ran", pr.n, who)
	}
	// the matrix's whole effect, as verification decides it (stage 3
	// review, finding 3)
	st := pr.step
	st.Verb = pr.verb
	holds, what := s.stateEffect(ctx, conn, st, f, pr.partner, u, time.Time{})
	if !holds {
		return nil, fmt.Sprintf("step %d's effect no longer holds on %s: %s (read %s)", pr.n, who, what, s.now().UTC().Format(time.RFC3339))
	}
	return map[string]any{"step": pr.n, "uid": f.UID, "generation": u.GetGeneration(), "holds": true}, ""
}

// changeWords is the step's change in Klyx's words, from Klyx's reads.
func changeWords(sn stepSnap, fresh actionFact) string {
	o := fresh.Object
	switch sn.step.Verb {
	case "flux-suspend":
		return "spec.suspend: false → true"
	case "flux-resume":
		return "spec.suspend: true → false"
	case "flux-reconcile":
		return "a reconcile request: reconcile.fluxcd.io/requestedAt set to a new token"
	case "rollout-restart":
		return "a rolling restart: kubectl.kubernetes.io/restartedAt set on the pod template, so every pod is replaced"
	case "scale":
		if o.Replicas != nil {
			return fmt.Sprintf("replicas %d → %d", *o.Replicas, *sn.step.Replicas)
		}
		return fmt.Sprintf("replicas → %d", *sn.step.Replicas)
	case "rollout-undo":
		h := fresh.History
		return fmt.Sprintf("revision %d → %d: the pod template of %s %s", h.DesiredRevision, h.PartnerRevision, partnerKind(fresh.Kind), h.PartnerObject)
	case "rollout-pause":
		return "spec.paused: false → true"
	case "rollout-resume":
		return "spec.paused: true → false"
	case "delete-pod":
		return fmt.Sprintf("pod %s/%s deleted", fresh.Namespace, fresh.Name)
	case "cordon":
		return "spec.unschedulable: false → true"
	case "uncordon":
		return "spec.unschedulable: true → false"
	case "keda-pause":
		return "the autoscaling.keda.sh/paused annotation set"
	case "keda-resume":
		return "the autoscaling.keda.sh/paused annotation removed"
	}
	return sn.step.Verb
}

// expectedWords is the effect Klyx will look for (§1).
func expectedWords(sn stepSnap) string {
	switch sn.step.Verb {
	case "flux-suspend":
		return "effect: spec.suspend true"
	case "flux-resume":
		return "effect: spec.suspend false"
	case "flux-reconcile":
		return "effect: Flux handles this request's token (status.lastHandledReconcileAt)"
	case "rollout-restart":
		return "effect: the template carries this request's token, then every pod is of the new template"
	case "scale":
		return fmt.Sprintf("effect: spec.replicas and status.replicas %d", *sn.step.Replicas)
	case "rollout-undo":
		return fmt.Sprintf("effect: revision %d's template is the workload's, observed by its controller", *sn.step.Revision)
	case "rollout-pause":
		return "effect: spec.paused true, observed"
	case "rollout-resume":
		return "effect: spec.paused false, observed"
	case "delete-pod":
		return "effect: the pod's UID is gone"
	case "cordon":
		return "effect: spec.unschedulable true"
	case "uncordon":
		return "effect: spec.unschedulable false"
	case "keda-pause":
		return "effect: KEDA's completed pause"
	case "keda-resume":
		return "effect: the pause annotation gone and Ready not False"
	}
	return ""
}

// ownershipReader and replicaOwnerReader are the authoritative reads the
// consequence comes from (§3): the manual YAML editor's and scale's.
type ownershipReader interface {
	GitOpsOwnership(ctx context.Context, group, kind, ns, name string) fleet.GitOpsOwnership
}

type replicaOwnerReader interface {
	ReplicaOwner(ctx context.Context, kind, ns, name string) fleet.ReplicaOwnership
}

// consequenceWords is the step's GitOps consequence, qualified by what the
// ownership read shows: ownership is evidence of management, not a promise
// that this change is reverted at the next tick (§3).
func (s *ChatService) consequenceWords(ctx context.Context, conn ChatConn, sn stepSnap, fresh actionFact) []string {
	var out []string
	if r, ok := conn.(ownershipReader); ok {
		o := r.GitOpsOwnership(ctx, fresh.Group, fresh.Kind, fresh.Namespace, fresh.Name)
		who := strings.TrimSpace(o.OwnerKind + " " + strings.TrimPrefix(o.OwnerNamespace+"/"+o.OwnerName, "/"))
		switch {
		case o.OwnerKind == "":
		case o.Basis == "argo-resources":
			out = append(out, fmt.Sprintf("Argo CD's %s manages it (its Argo resources): it may revert this change when it syncs; fields it ignores are not reverted", who))
		case o.OwnerSuspended:
			out = append(out, fmt.Sprintf("%s manages it (its Flux inventory) and is suspended: Flux applies nothing until it is resumed", who))
		default:
			line := fmt.Sprintf("%s manages it (its Flux inventory). While it is not suspended, Flux applies its desired state at each reconcile, which may revert this change", who)
			for _, pr := range sn.earlier {
				if pr.verb == "flux-suspend" && pr.fact.Kind == o.OwnerKind && pr.fact.Namespace == o.OwnerNamespace && pr.fact.Name == o.OwnerName {
					line += fmt.Sprintf("; step %d suspends it", pr.n)
				}
			}
			out = append(out, line)
		}
		for _, w := range o.Warnings {
			out = append(out, "ownership was not fully read: "+chatScrub(w, nil))
		}
		if o.OwnerKind == "" && len(o.Warnings) == 0 {
			out = append(out, "Klyx found no GitOps unit managing it")
		}
	} else {
		out = append(out, "Klyx could not read which GitOps unit manages it")
	}
	if sn.step.Verb == "scale" {
		if r, ok := conn.(replicaOwnerReader); ok {
			ro := r.ReplicaOwner(ctx, fresh.Kind, fresh.Namespace, fresh.Name)
			for _, owner := range ro.Owners {
				if strings.HasPrefix(owner, "KEDA") {
					out = append(out, owner+" sets the count: pause the ScaledObject instead to hold one")
				} else {
					out = append(out, owner+" sets the count back within its sync period")
				}
			}
			if ro.Autoscaler == "unverified" {
				out = append(out, "Klyx could not prove no autoscaler sets this count: "+chatScrub(orText(ro.AutoscalerWhy, "the read is incomplete"), nil))
			}
		} else {
			out = append(out, "Klyx could not read whether an autoscaler sets this count")
		}
	}
	if sn.step.Verb == "delete-pod" {
		o := fresh.Object
		switch o.OwnerKind {
		case "ReplicaSet", "StatefulSet", "DaemonSet":
			out = append(out, fmt.Sprintf("its %s %s keeps a count, so it creates a replacement", o.OwnerKind, o.OwnerName))
		case "":
			out = append(out, "no controller owns it: it is NOT recreated")
		default:
			out = append(out, fmt.Sprintf("its %s %s may not recreate it: recreation is not established", o.OwnerKind, o.OwnerName))
		}
		if fresh.UID == sn.anchor {
			out = append(out, "deleting the investigated pod ends what Klyx can verify about it; its replacement is shown on its own")
		}
	}
	return out
}

func reviewToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ReviewStep reads step n of the plan exchangeID proposed again, and words
// it for the ConfirmDialog with a token RunStep requires (§4.1).
func (s *ChatService) ReviewStep(cluster, conversationID, exchangeID string, n int) StepReviewDTO {
	s.mu.Lock()
	c, p, why := s.planLocked(cluster, conversationID, exchangeID)
	if why == "" {
		why = s.offeredLocked(c, p, n)
	}
	if why != "" {
		s.mu.Unlock()
		return StepReviewDTO{N: n, Reason: why}
	}
	sn := snapLocked(c, p, n)
	conn := c.conn
	s.mu.Unlock()

	fence := s.runFence(c)
	ctx, cancel := context.WithTimeout(readfence.With(context.Background(), fence), actionTimeout)
	defer cancel()
	out := StepReviewDTO{N: n, Verb: sn.step.Verb, Target: sn.step.Target, Danger: sn.step.Danger}
	if err := fence(); err != nil {
		out.Reason = err.Error()
		return out
	}
	ob, why := s.observe(ctx, conn, sn)
	if why != "" {
		out.Reason = why
		return out
	}
	token := reviewToken()
	expires := s.now().Add(reviewTokenTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	if c2, p2, why := s.planLocked(cluster, conversationID, exchangeID); why != "" || c2 != c || p2 != p {
		out.Reason = orText(why, "the plan changed while it was reviewed")
		return out
	} else if why := s.offeredLocked(c, p, n); why != "" {
		out.Reason = why
		return out
	}
	p.runs[n-1].review = &stepReview{token: token, digest: ob.digest, expires: expires}
	out.Offered, out.Token, out.ExpiresAt = true, token, expires.UTC().Format(time.RFC3339)
	out.Change, out.Consequence, out.Expected, out.Observed = ob.change, ob.consequence, ob.expected, ob.observed
	return out
}

// RunStep runs step n of the plan exchangeID proposed, as reviewed with
// token (§4.2). It consumes the protected grant first, so a refusal after
// the dialog armed one never leaves it armed.
func (s *ChatService) RunStep(cluster, conversationID, exchangeID string, n int, token string) StepRunDTO {
	if err := checkProtected(cluster); err != nil {
		r := refusedNotSent(err.Error())
		return StepRunDTO{Reason: err.Error(), Result: &r}
	}
	s.mu.Lock()
	c, p, why := s.planLocked(cluster, conversationID, exchangeID)
	if why == "" {
		why = s.offeredLocked(c, p, n)
	}
	if why == "" {
		rv := p.runs[n-1].review
		switch {
		case rv == nil || rv.token == "" || rv.token != token:
			why = "review the step first"
		case !s.now().Before(rv.expires):
			why = "the review expired: review the step again"
		}
	}
	if why != "" {
		s.mu.Unlock()
		return StepRunDTO{Reason: why}
	}
	reviewed := p.runs[n-1].review.digest
	p.running, c.stepRunning = n, p // reserved with the check, under one lock
	sn := snapLocked(c, p, n)
	conn := c.conn
	s.mu.Unlock()

	release := func(reason string) StepRunDTO {
		s.mu.Lock()
		defer s.mu.Unlock()
		if p.running == n {
			p.running = 0
		}
		if c.stepRunning == p {
			c.stepRunning = nil
		}
		p.runs[n-1].review = nil
		s.version++
		return StepRunDTO{Reason: reason}
	}
	fence := s.runFence(c)
	ctx, cancel := context.WithTimeout(readfence.With(context.Background(), fence), actionTimeout)
	defer cancel()
	if err := fence(); err != nil {
		return release(err.Error())
	}
	ob, why := s.observe(ctx, conn, sn)
	switch {
	case why != "":
		return release(why)
	case ob.digest != reviewed:
		return release("it changed since you reviewed it: review it again")
	}
	// delete-pod's witness needs the namespace's collection resourceVersion
	// from before the delete (§5.2): without it, nothing is sent
	if sn.step.Verb == "delete-pod" {
		l, err := conn.ChatList(ctx, podsGVRv1, sn.fact.Namespace, "", 1)
		if err != nil {
			return release("Klyx could not prepare the deletion's verification: " + chatScrub(readError(err), nil))
		}
		sn.podsRV = l.GetResourceVersion()
	}
	// the fence again before the write: a client without the transport
	// fence (a stand-in) never writes on a connection that changed
	if err := fence(); err != nil {
		return release(err.Error())
	}
	res := runVerb(ctx, conn, sn)

	s.mu.Lock()
	r := p.runs[n-1]
	r.review = nil
	r.request, r.notSent, r.errText, r.ranAt = res.Outcome, res.NotSent, capRecord(res.Error, stepErrMax), s.now()
	if res.Outcome == "" { // a verb that did not classify: never written
		r.request = fleet.OutcomeUnverified
	}
	if r.request == fleet.OutcomeRefused {
		r.effect, r.effectWhy = "", "the request was refused"
	} else {
		r.effect = stepEffectPending
	}
	p.running, c.stepRunning = 0, nil
	if r.effect == stepEffectPending {
		// the step runs until its effect is decided (§5.2)
		p.running, c.stepRunning = n, p
	}
	s.retainLocked(c)
	s.version++
	ranAt := r.ranAt
	dto := planDTOLocked(c, p, true)
	s.mu.Unlock()
	if r.effect == stepEffectPending {
		s.verifyStep(c, p, n, sn, res, ranAt)
	}
	return StepRunDTO{Ran: true, Result: &res, Plan: &dto}
}

// verifyStepHook replaces the verification in a test.
var verifyStepHook func(s *ChatService, c *conversation, p *actionPlan, n int, sn stepSnap, res ActionResultDTO)

// verifyStep decides a run step's effect in the background (§5.2): RunStep
// answers with the request's outcome, and the plan's state carries the
// effect when it settles.
func (s *ChatService) verifyStep(c *conversation, p *actionPlan, n int, sn stepSnap, res ActionResultDTO, ranAt time.Time) {
	if verifyStepHook != nil {
		verifyStepHook(s, c, p, n, sn, res)
		return
	}
	safeGo("investigation-verify", func() { s.verifyStepEffect(c, p, n, sn, res, ranAt) })
}

// settleEffect records a step's effect and ends its run, releasing the
// conversation's running step.
func (s *ChatService) settleEffect(c *conversation, p *actionPlan, n int, effect, why string) {
	defer s.emitPlan(c)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := p.runs[n-1]
	if effect == stepEffectEstablished {
		// recorded only while the fence holds (stage 4 review, finding 2)
		if f := s.verifyFenceLocked(c); f != "" {
			effect, why = stepEffectUnverified, f
		}
	}
	r.effect, r.effectWhy = effect, capRecord(why, stepWhyMax)
	if p.running == n {
		p.running = 0
		if c.stepRunning == p {
			c.stepRunning = nil
		}
	}
	s.retainLocked(c)
	s.version++
}

// emitPlan tells the drawer that a plan's run or incident changed in the
// background (AI A3 stage 5): it reads the conversation back, ordered by
// version as ever. Called without the service lock.
func (s *ChatService) emitPlan(c *conversation) {
	if s.em != nil {
		s.em.Emit("chat-plan:"+c.id, struct{}{})
	}
}

// capRecord caps a record's text so its JSON encoding, escapes included,
// is at most max bytes (stage 3 re-review, finding 3), cut on a rune
// boundary.
func capRecord(s string, max int) string {
	if jsonTextLen(s) <= max {
		return s
	}
	budget, n := max-len("…"), 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		w := escapedLen(r, size)
		if n+w > budget {
			return s[:i] + "…"
		}
		n += w
		i += size
	}
	return s
}

// jsonTextLen is an upper bound of s's length inside a JSON string, as
// encoding/json writes it (HTML-safe escapes included).
func jsonTextLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		n += escapedLen(r, size)
		i += size
	}
	return n
}

func escapedLen(r rune, size int) int {
	switch {
	case r == utf8.RuneError && size == 1: // invalid UTF-8: \ufffd
		return 6
	case r == '\n', r == '\r', r == '\t', r == '"', r == '\\':
		return 2
	case r < 0x20, r == '<', r == '>', r == '&', r == '\u2028', r == '\u2029':
		return 6
	}
	return size
}

// DismissPlan ends a plan as declined (§4.6): its unrun steps are not run.
func (s *ChatService) DismissPlan(cluster, conversationID, exchangeID string) StepRunDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, p, why := s.planLocked(cluster, conversationID, exchangeID)
	if p == nil {
		return StepRunDTO{Reason: why}
	}
	if p.running > 0 {
		return StepRunDTO{Reason: fmt.Sprintf("step %d is running", p.running)}
	}
	p.declined = true
	s.version++
	dto := planDTOLocked(c, p, why == "")
	return StepRunDTO{Plan: &dto}
}

// runVerb is the dispatch table (§4.4): each matrix verb to the inner
// function its manual bridge method calls, with the pinned connection.
func runVerb(ctx context.Context, conn ChatConn, sn stepSnap) ActionResultDTO {
	f, st := sn.fact, sn.step
	no := refusedNotSent("the connection cannot run " + st.Verb)
	switch st.Verb {
	case "flux-suspend", "flux-resume":
		if c, ok := conn.(suspender); ok {
			return doSetSuspend(ctx, c, f.Kind, f.Namespace, f.Name, f.UID, st.Verb == "flux-suspend")
		}
	case "flux-reconcile":
		if c, ok := conn.(reconciler); ok {
			return doReconcile(ctx, c, f.Kind, f.Namespace, f.Name, f.UID)
		}
	case "rollout-restart":
		if c, ok := conn.(restarter); ok {
			return doRolloutRestart(ctx, c, f.Kind, f.Namespace, f.Name, f.UID)
		}
	case "scale":
		if c, ok := conn.(scaler); ok {
			return doScaleWorkload(ctx, c, f.Kind, f.Namespace, f.Name, f.UID, *st.Replicas)
		}
	case "rollout-undo":
		if c, ok := conn.(undoer); ok && sn.partner != nil {
			return doRolloutUndo(ctx, c, f.Kind, f.Namespace, f.Name, f.UID, int(*st.Revision), sn.partner)
		}
	case "rollout-pause", "rollout-resume":
		if c, ok := conn.(pauser); ok {
			return doRolloutPause(ctx, c, f.Namespace, f.Name, f.UID, st.Verb == "rollout-pause")
		}
	case "delete-pod":
		if c, ok := conn.(podDeleter); ok {
			return doDeletePod(ctx, c, f.Namespace, f.Name, f.UID)
		}
	case "cordon", "uncordon":
		if c, ok := conn.(cordoner); ok {
			return doCordon(ctx, c, f.Name, f.UID, st.Verb == "cordon")
		}
	case "keda-pause", "keda-resume":
		if c, ok := conn.(kedaPauser); ok {
			return doKEDASetPaused(ctx, c, f.Resource, f.Namespace, f.Name, f.UID, st.Verb == "keda-pause")
		}
	}
	return no
}

// planDTOLocked is a plan with each step's run and the plan's status.
func planDTOLocked(c *conversation, p *actionPlan, current bool) ActionPlanDTO {
	dto := p.dto
	dto.Steps = append([]ActionStepDTO(nil), p.dto.Steps...)
	held := 0
	for i := range dto.Steps {
		r := p.runs[i]
		if r.request == "" {
			continue
		}
		dto.Steps[i].Run = &StepRunStateDTO{Request: r.request, NotSent: r.notSent, Error: r.errText, Effect: r.effect, EffectWhy: r.effectWhy, RanAt: r.ranAt.UTC().Format(time.RFC3339)}
		if held == 0 && (r.request != fleet.OutcomeWritten && r.request != fleet.OutcomeUnverified || r.effect == stepEffectUnverified) {
			held = i + 1
		}
	}
	if p.incident != nil {
		inc := *p.incident
		inc.Subjects = append([]IncidentSubjectDTO(nil), p.incident.Subjects...)
		dto.Incident = &inc
	}
	switch {
	case p.declined:
		dto.Status, dto.StatusWhy = planStatusDeclined, "the plan was dismissed"
	case !current:
		dto.Status, dto.StatusWhy = planStatusSuperseded, "a later proposal superseded its unrun steps"
	case c.ended != "":
		dto.Status, dto.StatusWhy = planStatusWithdrawn, "the investigation ended ("+c.ended+"): its unrun steps are withdrawn"
	case held > 0:
		dto.Status, dto.StatusWhy = planStatusHeld, fmt.Sprintf("step %d held the plan: %s", held, holdWhy(p.runs[held-1]))
	}
	return dto
}

func holdWhy(r *stepRun) string {
	switch {
	case r.request == fleet.OutcomeRefused:
		return "its request was refused"
	case r.effect == stepEffectUnverified:
		return "its effect is unverified (" + r.effectWhy + ")"
	}
	return "its request is unverified"
}

// planRecordLocked is Klyx's record of the current plan's runs for the next
// question (§5.4): each run step's verb, its target as the ledger names it,
// and the outcomes in these words. No reason, UID or read is sent: the
// model reads what it needs through its tools, fenced as ever.
func planRecordLocked(c *conversation) string {
	var lines []string
	for _, e := range c.exchanges {
		p := e.plan
		if p == nil {
			continue
		}
		turn := e.turn
		ran := false
		for j, st := range p.dto.Steps {
			r := p.runs[j]
			if r.request == "" {
				continue
			}
			ran = true
			who := st.Target.Kind + " " + strings.TrimPrefix(st.Target.Namespace+"/"+st.Target.Name, "/")
			line := fmt.Sprintf("turn %d's plan, step %d, %s %s: request %s", turn, st.N, st.Verb, who, r.request)
			if r.effect != "" {
				line += "; effect " + r.effect
			}
			lines = append(lines, line)
		}
		if ran && p.incident != nil {
			lines = append(lines, fmt.Sprintf("turn %d's plan, incident: %s", turn, p.incident.State))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "Klyx's record of the steps the operator ran (you ran nothing):\n" + strings.Join(lines, "\n")
}
