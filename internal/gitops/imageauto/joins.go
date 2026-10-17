package imageauto

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/moomora/klyx/internal/crd"
)

// The joins (S1b §2-3). No verdict depends on another object: each kind is
// judged by its own status, and a join is shown beside it. A join input
// that is missing reads "not found", unreadable "not readable", and past
// the budget or not kept whole "incomplete" - never healthy, never a claim.

// Join states.
const (
	JoinFound      = "found"
	JoinNotFound   = "not found"
	JoinUnreadable = "not readable"
	JoinIncomplete = "incomplete"
)

// Coverage is how much of a kind a join could read.
type Coverage int

const (
	CoverageComplete   Coverage = iota // every object
	CoveragePartial                    // capped: a lower bound
	CoverageUnreadable                 // nothing readable
)

// CoverageState is a whole join's state from its read: found (complete),
// incomplete (capped) or not readable. A join drawn from an incomplete or
// unreadable read never reads as a proven empty one.
func CoverageState(cov Coverage) string {
	switch cov {
	case CoverageUnreadable:
		return JoinUnreadable
	case CoveragePartial:
		return JoinIncomplete
	}
	return JoinFound
}

// missing is the state of an object a join did not find.
func missing(cov Coverage) string {
	switch cov {
	case CoverageUnreadable:
		return JoinUnreadable
	case CoveragePartial:
		return JoinIncomplete
	}
	return JoinNotFound
}

// RepositoryLink is a policy's repository, as a separate block (the policy's
// own verdict comes first).
type RepositoryLink struct {
	State      string
	Repository *Repository
}

// PolicyRepository finds a policy's repository by its reference.
func PolicyRepository(p Policy, repos []Repository, cov Coverage) RepositoryLink {
	for i := range repos {
		if repos[i].Namespace == p.RepositoryNamespace && repos[i].Name == p.RepositoryName {
			return RepositoryLink{State: JoinFound, Repository: &repos[i]}
		}
	}
	return RepositoryLink{State: missing(cov)}
}

// RepositoryPolicies are the policies naming r, across namespaces.
func RepositoryPolicies(r Repository, policies []Policy) []Policy {
	var out []Policy
	for _, p := range policies {
		if p.RepositoryNamespace == r.Namespace && p.RepositoryName == r.Name {
			out = append(out, p)
		}
	}
	return out
}

// Use states: whether an automation's last successful run used a policy's
// resolved image.
const (
	UseUsed       = "used"
	UsePending    = "pending"
	UseIncomplete = "incomplete"
)

// PolicyUse is one policy an automation uses now.
type PolicyUse struct {
	Namespace, Name string
	Resolved        ImageRef
	Observed        *ImageRef
	State           string
}

// selectorOf is the automation's selector; ok false when it cannot be
// evaluated (not kept whole, or invalid).
func selectorOf(a Automation) (labels.Selector, bool) {
	switch {
	case a.SelectorIncomplete:
		return nil, false
	case !a.HasSelector:
		return labels.Everything(), true
	case a.Selector == nil:
		return nil, false
	}
	s, err := metav1.LabelSelectorAsSelector(a.Selector)
	return s, err == nil
}

// uses reports whether a uses p now: the same namespace, the selector
// matching p's labels, and a resolved image - whether or not p is Ready.
// ok false when it cannot be told.
func uses(a Automation, sel labels.Selector, p Policy) (bool, bool) {
	if p.Namespace != a.Namespace || p.Latest == nil {
		return false, true
	}
	if p.LabelsIncomplete {
		return false, false
	}
	return sel.Matches(labels.Set(p.Labels)), true
}

func useOf(a Automation, p Policy) PolicyUse {
	u := PolicyUse{Namespace: p.Namespace, Name: p.Name, Resolved: *p.Latest}
	if !a.ObservedComplete {
		u.State = UseIncomplete
		return u
	}
	if o, ok := a.ObservedPolicies[p.Name]; ok {
		u.Observed = &o
	}
	if u.Observed != nil && *u.Observed == u.Resolved {
		u.State = UseUsed
	} else {
		u.State = UsePending
	}
	return u
}

// AutomationPolicies are the policies a uses now, each with whether its
// last successful run used the resolved image. state is JoinFound, or
// JoinIncomplete when the selector, a policy's labels or the policies'
// read could not be used whole.
func AutomationPolicies(a Automation, policies []Policy, cov Coverage) (out []PolicyUse, state string) {
	if cov == CoverageUnreadable {
		return nil, JoinUnreadable
	}
	sel, ok := selectorOf(a)
	if !ok {
		return nil, JoinIncomplete
	}
	state = JoinFound
	if cov == CoveragePartial {
		state = JoinIncomplete
	}
	for _, p := range policies {
		match, known := uses(a, sel, p)
		if !known {
			state = JoinIncomplete
			continue
		}
		if match {
			out = append(out, useOf(a, p))
		}
	}
	return out, state
}

// AutomationUse is an automation that uses a policy.
type AutomationUse struct {
	Namespace, Name string
	State           string // UseUsed, UsePending or UseIncomplete
}

// PolicyAutomations are the automations that use p now.
func PolicyAutomations(p Policy, automations []Automation) []AutomationUse {
	var out []AutomationUse
	for _, a := range automations {
		sel, ok := selectorOf(a)
		if !ok {
			if a.Namespace == p.Namespace && p.Latest != nil {
				out = append(out, AutomationUse{Namespace: a.Namespace, Name: a.Name, State: UseIncomplete})
			}
			continue
		}
		match, known := uses(a, sel, p)
		switch {
		case !known:
			out = append(out, AutomationUse{Namespace: a.Namespace, Name: a.Name, State: UseIncomplete})
		case match:
			out = append(out, AutomationUse{Namespace: a.Namespace, Name: a.Name, State: useOf(a, p).State})
		}
	}
	return out
}

// Checkout origins.
const (
	CheckoutAutomation = "automation" // git.checkout.ref, as a whole
	CheckoutSource     = "source"     // the GitRepository's spec.ref
	CheckoutDefault    = "default"    // neither: Git's default branch
)

// PushTarget is where the next run would push, as configured now: never
// proof of where the last push went (S1b §2).
type PushTarget struct {
	CheckoutFrom string
	Checkout     *CheckoutRef
	// Branch is the push branch, or Error why it cannot be inferred.
	Branch, Error string
	Refspec       string
	OptionCount   int
}

// Target infers the push target as image-automation-controller v1.2.5 does
// (internal/source/git.go configurePush). An explicit checkout replaces the
// source's ref as a whole, so a checkout by tag with no push branch is an
// inference failure whatever branch the GitRepository names.
func Target(a Automation, sourceRef *CheckoutRef) PushTarget {
	t := PushTarget{Refspec: a.PushRefspec, OptionCount: a.PushOptionCount}
	switch {
	case a.Checkout != nil:
		t.CheckoutFrom, t.Checkout = CheckoutAutomation, a.Checkout
	case sourceRef != nil:
		t.CheckoutFrom, t.Checkout = CheckoutSource, sourceRef
	default:
		t.CheckoutFrom = CheckoutDefault
	}
	switch {
	case strings.HasPrefix(a.PushRefspec, ":"):
		t.Error = "the push refspec specifies a deletion, which the controller refuses"
		return t
	case strings.HasPrefix(a.PushRefspec, "+"):
		t.Error = "the push refspec specifies a force update, which the controller refuses"
		return t
	}
	switch {
	case a.PushBranch != "":
		t.Branch = a.PushBranch
	case t.Checkout != nil && t.Checkout.Branch != "":
		t.Branch = t.Checkout.Branch
	default:
		t.Error = "no push branch, and none can be inferred from the checkout ref"
	}
	return t
}

// Branch evidence states.
const (
	BranchBound     = "bound"     // exactly one event names the branch
	BranchExpired   = "expired"   // no bound event: events age out
	BranchAmbiguous = "ambiguous" // bound events disagree
	BranchNoPush    = "no push"   // no push recorded
)

// PushEvent is an event's involved object UID and message.
type PushEvent struct {
	UID, Message string
}

// BranchEvidence is the last push's branch, from the controller's events.
type BranchEvidence struct {
	State, Branch string
}

// BoundBranch reads the last push's branch from events bound to it: the
// automation's UID, a summary whose seven-character hash prefixes
// lastPushCommit, and a first line that parses. Only the first line is
// read. Status never records the branch.
func BoundBranch(a Automation, events []PushEvent) BranchEvidence {
	if a.LastPushCommit == "" {
		return BranchEvidence{State: BranchNoPush}
	}
	branches := map[string]bool{}
	for _, e := range events {
		if e.UID != a.UID {
			continue
		}
		first, _, _ := strings.Cut(e.Message, "\n")
		s, ok := crd.ParsePushSummary(first)
		if !ok || !strings.HasPrefix(a.LastPushCommit, s.Commit) {
			continue
		}
		branches[s.Branch] = true
	}
	switch len(branches) {
	case 0:
		return BranchEvidence{State: BranchExpired}
	case 1:
		for b := range branches {
			return BranchEvidence{State: BranchBound, Branch: b}
		}
	}
	return BranchEvidence{State: BranchAmbiguous}
}

// SourceInput is the bound GitRepository as a join reads it.
type SourceInput struct {
	State string // a Join state
	// Revision is its artifact revision; Branch the branch it tracks.
	Revision, Branch string
}

// Source line kinds.
const (
	SourceFetched = "fetched" // its artifact is the pushed commit
	SourceOther   = "other"   // at another revision: inclusion not known
	SourceMissing = "missing" // not found, not readable or incomplete
	SourceNoPush  = "no push"
)

// SourceLine compares the last push with the source's artifact today: a
// comparison, never proof of the push's destination.
type SourceLine struct {
	Kind     string
	Revision string
	// BranchNote is set only when provable (S1b §2).
	BranchNote string
}

// Source is the push-versus-source line.
func Source(a Automation, src SourceInput, bound BranchEvidence) SourceLine {
	switch {
	case a.LastPushCommit == "":
		return SourceLine{Kind: SourceNoPush}
	case src.State != JoinFound:
		return SourceLine{Kind: SourceMissing}
	}
	l := SourceLine{Kind: SourceOther, Revision: src.Revision}
	if sha := ShortSHA(src.Revision); sha != "" && sha == a.LastPushCommit {
		l.Kind = SourceFetched
	}
	switch {
	case a.PushRefspec != "":
		l.BranchNote = "a refspec is configured, so the push may also have updated other branches"
	case bound.State == BranchBound && src.Branch != "" && bound.Branch != src.Branch:
		l.BranchNote = "pushed to " + bound.Branch + "; this source tracks " + src.Branch
	}
	return l
}

// AppliedInput is a Kustomization bound to the automation's GitRepository.
type AppliedInput struct {
	Namespace, Name, Revision string
}

// AppliedLine is one Kustomization's last applied revision against the
// push: "applied this commit" only on an equal SHA. Workloads are never
// inferred, and nothing is called deployed.
type AppliedLine struct {
	Namespace, Name, Revision string
	Applied                   bool
}

// Applied compares each bound Kustomization with the last push.
func Applied(a Automation, ks []AppliedInput) []AppliedLine {
	if a.LastPushCommit == "" {
		return nil
	}
	out := make([]AppliedLine, 0, len(ks))
	for _, k := range ks {
		sha := ShortSHA(k.Revision)
		out = append(out, AppliedLine{Namespace: k.Namespace, Name: k.Name, Revision: k.Revision, Applied: sha != "" && sha == a.LastPushCommit})
	}
	return out
}
