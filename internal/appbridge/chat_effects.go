package appbridge

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/workloads"
)

// AI A3 §1 and §4.3: a state verb's effect is one predicate, the matrix's
// whole effect, read the same way when an earlier step stands as a later
// one's prerequisite and when verification decides it (stage 3 review,
// finding 3). What the object alone does not show is read too: a
// Deployment undo's current ReplicaSet, and KEDA's completed pause from the
// Observer's verdict.

// verdictOfHook replaces the Observer's verdict lookup in a test.
var verdictOfHook func(f actionFact) subjectVerdict

// subjectVerdict is one subject's verdict by UID in the Observer's capture.
// state is its verdict's state whichever classifier made it: a verdict
// part's Kind, a native part's Role (stage 3 re-review, finding 1);
// completed is a KEDA scaler row's own evidence of a completed pause.
type subjectVerdict struct {
	v         chatVerdict
	found     bool
	state     string
	completed bool
}

// verdictOf is Klyx's verdict for one subject by UID, from the Observer's
// capture: an in-memory read, never an API call. cache keeps one reading's
// parts, so several subjects of one part classify it once.
func (s *ChatService) verdictOf(conn ChatConn, f actionFact, cache map[string]*chatVerdicts) subjectVerdict {
	if verdictOfHook != nil {
		return verdictOfHook(f)
	}
	gvr := factGVR(f)
	vs, ok := cache[gvr.String()]
	if !ok {
		n := 0
		vs = newChatTools(conn, s.now).verdictsFor(context.Background(), gvr, &n)
		if cache != nil {
			cache[gvr.String()] = vs
		}
	}
	if vs == nil || vs.byUID == nil {
		return subjectVerdict{}
	}
	v, ok := vs.byUID[f.UID]
	return subjectVerdict{v: v, found: ok, state: orText(v.Kind, v.Role), completed: vs.completed[f.UID]}
}

// stateEffect says whether a state verb's effect holds on u, its target as
// just read (the UID already checked), and why not. partner is an undo's
// pinned revision.
func (s *ChatService) stateEffect(ctx context.Context, conn ChatConn, st ActionStepDTO, f actionFact, partner *fleet.RevisionExpect, u *unstructured.Unstructured) (bool, string) {
	o := factOf(factGVR(f), u, "", "", 0).Object
	gen := u.GetGeneration()
	observed, _, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	unobserved := fmt.Sprintf("its controller has not observed generation %d (observed %d)", gen, observed)
	switch st.Verb {
	case "flux-suspend":
		return o.Suspended, "it is not suspended (spec.suspend false)"
	case "flux-resume":
		return !o.Suspended, "it is suspended (spec.suspend true)"
	case "scale":
		want := int64(*st.Replicas)
		have, _, _ := unstructured.NestedInt64(u.Object, "status", "replicas")
		switch {
		case o.Replicas == nil || *o.Replicas != want:
			return false, fmt.Sprintf("spec.replicas is not %d", want)
		case observed < gen:
			return false, unobserved
		case have != want:
			return false, fmt.Sprintf("status.replicas is %d, not %d", have, want)
		}
		return true, ""
	case "rollout-undo":
		tm, _, _ := unstructured.NestedMap(u.Object, "spec", "template")
		d, _ := workloads.TemplateDigest(tm, f.Kind == "Deployment")
		switch {
		case partner == nil || d == "" || d != partner.TemplateDigest:
			return false, "its template is not the partner revision's"
		case observed < gen:
			return false, unobserved
		}
		return s.currentPartner(ctx, conn, f, partner, u)
	case "rollout-pause", "rollout-resume":
		if o.Paused != (st.Verb == "rollout-pause") {
			return false, fmt.Sprintf("spec.paused is %t", o.Paused)
		}
		if observed < gen {
			return false, unobserved
		}
		return true, ""
	case "cordon":
		return o.Unschedulable, "it is not cordoned (spec.unschedulable false)"
	case "uncordon":
		return !o.Unschedulable, "it is cordoned (spec.unschedulable true)"
	case "keda-pause":
		// the fresh read's annotation and conditions, joined to the
		// capture's own completion evidence (S9's PauseCompleted or
		// JobPauseCompleted, with its HPA evidence) at the same generation
		switch {
		case !o.kedaPausedTrue():
			return false, "its pause annotation is not true"
		case condStatus(u, "Paused") != "True":
			return false, "its Paused condition is not True"
		case condStatus(u, "Ready") == "False":
			return false, "Ready is False"
		}
		sv := s.verdictOf(conn, f, nil)
		switch {
		case !sv.found || sv.v.Coverage != "live":
			return false, "Klyx holds no live verdict that its pause completed"
		case sv.v.Generation != u.GetGeneration():
			return false, fmt.Sprintf("Klyx's verdict describes generation %d, not the read's %d", sv.v.Generation, u.GetGeneration())
		case !sv.completed:
			return false, fmt.Sprintf("KEDA's pause has not completed (Klyx reads it %s)", orText(sv.state, "unknown"))
		}
		return true, ""
	case "keda-resume":
		switch {
		case o.KEDAPauseSet:
			return false, "its pause annotation is set"
		case o.KEDAOtherPause != "":
			return false, "it carries " + o.KEDAOtherPause
		case condStatus(u, "Ready") == "False":
			return false, "Ready is False"
		}
		return true, ""
	}
	return false, "Klyx has no state effect for " + st.Verb
}

// currentPartner is a Deployment undo's further witness (§1): its current
// ReplicaSet is the partner's, by A1.1's evidence rule (stage 3 re-review,
// finding 2) - the partner, read by name, is the pinned UID, controlled by
// this Deployment's UID, with the pinned template, and matches its
// selector; the complete LIST of the Deployment's ReplicaSets holds it,
// as the controller's own candidate set does (re-review 2: a ReplicaSet
// that stops matching is released, whatever its controller reference
// says), and no other of them has that template (an ambiguity is
// unestablished, and so is a partial read); and, corroborating, it
// carries the Deployment's revision. Another kind's undo has none.
func (s *ChatService) currentPartner(ctx context.Context, conn ChatConn, f actionFact, partner *fleet.RevisionExpect, d *unstructured.Unstructured) (bool, string) {
	if f.Kind != "Deployment" {
		return true, ""
	}
	if f.History == nil || f.History.PartnerObject == "" || partner == nil {
		return false, "Klyx holds no partner ReplicaSet to read"
	}
	rs, err := conn.ChatGet(ctx, replicaSetsGVR, f.Namespace, f.History.PartnerObject)
	if err != nil {
		return false, "the partner ReplicaSet could not be read: " + chatScrub(readError(err), nil)
	}
	digest := func(o *unstructured.Unstructured) string {
		tm, ok, _ := unstructured.NestedMap(o.Object, "spec", "template")
		if !ok {
			return ""
		}
		dg, _ := workloads.TemplateDigest(tm, true)
		return dg
	}
	switch {
	case string(rs.GetUID()) != partner.ObjectUID:
		return false, "the partner ReplicaSet was replaced"
	case controllerUID(rs) != f.UID:
		return false, "the partner ReplicaSet is no longer controlled by this Deployment"
	case digest(rs) != partner.TemplateDigest:
		return false, "the partner ReplicaSet's template changed"
	}
	sel, err := selectorOf(d)
	if err != nil {
		return false, err.Error()
	}
	if !sel.Matches(labels.Set(rs.GetLabels())) {
		return false, "the partner ReplicaSet no longer matches the Deployment's selector"
	}
	list, err := conn.ChatList(ctx, replicaSetsGVR, f.Namespace, sel.String(), chatReplicaSetsMax)
	if err != nil {
		return false, "the Deployment's ReplicaSets could not be read: " + chatScrub(readError(err), nil)
	}
	if len(list.Items) >= chatReplicaSetsMax || list.GetContinue() != "" {
		return false, "the Deployment's ReplicaSets were read only in part: Klyx cannot rule out another with the partner's template"
	}
	member := false
	for i := range list.Items {
		o := &list.Items[i]
		switch {
		case string(o.GetUID()) == partner.ObjectUID:
			member = controllerUID(o) == f.UID && digest(o) == partner.TemplateDigest
		case controllerUID(o) == f.UID && digest(o) == partner.TemplateDigest:
			return false, "ambiguous: another ReplicaSet of this Deployment has the partner's template"
		}
	}
	if !member {
		return false, "the partner ReplicaSet is not among the Deployment's ReplicaSets, as their LIST reads them"
	}
	const rev = "deployment.kubernetes.io/revision"
	if have := rs.GetAnnotations()[rev]; have == "" || have != d.GetAnnotations()[rev] {
		return false, "the Deployment's current ReplicaSet is not the partner's"
	}
	return true, ""
}

func condStatus(u *unstructured.Unstructured, typ string) string {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if ok && m["type"] == typ {
			st, _ := m["status"].(string)
			return st
		}
	}
	return ""
}
