package appbridge

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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
type subjectVerdict struct {
	v     chatVerdict
	found bool
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
	return subjectVerdict{v: v, found: ok}
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
		if !o.kedaPausedTrue() {
			return false, "its pause annotation is not true"
		}
		sv := s.verdictOf(conn, f, nil)
		if !sv.found || sv.v.Coverage != "live" {
			return false, "Klyx holds no live verdict that its pause completed"
		}
		if sv.v.Kind != "paused" {
			return false, fmt.Sprintf("KEDA's pause has not completed (Klyx reads it %s)", orText(sv.v.Kind, "unknown"))
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
// ReplicaSet is the partner's, by UID - the partner, read by name, carries
// the Deployment's revision. Another kind's undo has none.
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
	if string(rs.GetUID()) != partner.ObjectUID {
		return false, "the partner ReplicaSet was replaced"
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
