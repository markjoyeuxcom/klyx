package gwapi

import (
	"testing"

	"github.com/moomora/klyx/internal/health"
)

// Codex's S7b re-review 2, its probe adopted: a current ListenerSet's own
// fresh refusal is a Fault whatever its parent reference names.

func TestListenerSetCurrentDoesNotDependOnItsParentKind(t *testing.T) {
	for _, parent := range []ParentRef{
		{Group: "", Kind: "Service", Namespace: "infra", Name: "backend"},
		{Group: "example.test", Kind: "Gateway", Namespace: "infra", Name: "eg"},
	} {
		t.Run(parent.Group+"/"+parent.Kind, func(t *testing.T) {
			ref := refTo("extra", "kind", "ListenerSet")
			ls := ListenerSet{Namespace: "infra", Name: "extra", Parent: parent, Generation: 1, AcceptedCond: Cond{Status: "False", Reason: "Invalid", ObservedGeneration: 1}}
			own := ListenerSetRule(ls, ParentLookup{State: ParentUnsupported})
			if own.Role != health.Fault {
				t.Fatalf("fixture's own fresh refusal was not a fault: %+v", own)
			}
			ix := indexWith(gwRef("eg"))
			ix.ListenerSets = []ListenerSet{ls}
			r := ParseRoute(KindHTTPRoute, routeObj(KindHTTPRoute, "shop", "web", 1, map[string]any{"parentRefs": []any{ref}, "rules": backendSpec("web", 80)["rules"]}, parentEntry(ref, egCtl, "True", "True", 1)))
			okBackends(&r)
			a := r.Attachments(ix)[0]
			if !a.Parent.ListenerSetCurrent || a.Eval.Role != health.Fault || a.Eval.Label != "parent ListenerSet not accepted" {
				t.Fatalf("current ListenerSet read with its own fresh refusal was hidden because its parent kind is unsupported: LSCurrent=%v eval=%+v", a.Parent.ListenerSetCurrent, a.Eval)
			}
			// The same retained ListenerSet from a stale read must still be unproven.
			ix.ListenerSetsRead = ParentRead{}
			if stale := r.Attachments(ix)[0]; stale.Eval.Role != health.Unknown {
				t.Fatalf("stale refusal became proof: %+v", stale.Eval)
			}
		})
	}
}
