package dra

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/moomora/klyx/internal/health"
)

// Grace bounds two readings (S11b §3.1, §3.2): a claim being deleted with
// no consumer, and an incomplete pool.
const Grace = 5 * time.Minute

// ---- claims ---------------------------------------------------------------

// ConsumerPod is one non-terminal pod that references a pending claim,
// with what is known of its other claims.
type ConsumerPod struct {
	Pod *corev1.Pod
	// Claims are the pod's DRA claims ("<ns>/<name>"), and Classes each
	// claim's device classes. Complete: every claim of the pod resolved
	// and read, under a complete and current claims read (row b's proof).
	Claims   []string
	Classes  map[string][]string
	Complete bool
}

// ConsumerReading is one consumer's row of the decision table.
type ConsumerReading struct {
	Pod     string
	Row     string // a to g
	Kind    health.Kind
	Label   string
	Reason  string // the fixed reason's key, when one matched
	Message string // the scheduler's message, local only
}

// Key is a claim's "<ns>/<name>", as the scheduler names it.
func (c Claim) Key() string { return c.Namespace + "/" + c.Name }

func scheduled(p *corev1.Pod) *corev1.PodCondition {
	for i := range p.Status.Conditions {
		if p.Status.Conditions[i].Type == corev1.PodScheduled {
			return &p.Status.Conditions[i]
		}
	}
	return nil
}

// ClaimVerdict reads one claim (S11b §3.1). consumers are its pending
// consumer pods; consumersKnown says the pods read is complete and current,
// so no consumer means none.
func ClaimVerdict(c Claim, consumers []ConsumerPod, consumersKnown bool, now time.Time) (health.Verdict, []ConsumerReading) {
	native := claimState(c)
	switch {
	case c.Deleting && c.ReservedCount > 0:
		return health.Verdict{Kind: health.Progressing, Label: "Being deleted", Reason: "Deleting", Native: native, Rule: "claim.deleting",
			Message: fmt.Sprintf("waiting for %d consumer%s", c.ReservedCount, plural(c.ReservedCount))}, nil
	case c.Deleting:
		if now.Sub(c.DeletionTime) < Grace {
			at := c.DeletionTime.Add(Grace)
			return health.Verdict{Kind: health.Progressing, Label: "Being deleted", Reason: "Deleting", Native: native, Rule: "claim.deleting", ReclassifyAt: &at}, nil
		}
		return health.Verdict{Kind: health.Degraded, Label: "Deletion not finishing", Reason: "DeletionStuck", Native: native, Rule: "claim.deletion-stuck",
			Message: "deleted " + short(now.Sub(c.DeletionTime)) + " ago with no consumer; finalizers: " + strings.Join(c.Finalizers, ", ")}, nil
	}
	if d, cond, ok := notReady(c); ok {
		return health.Verdict{Kind: health.Degraded, Label: "Device not ready", Reason: orToken(cond.Reason, "NotReady"), Native: native, Rule: "claim.device-not-ready",
			Message: d.Driver + "/" + d.Pool + "/" + d.Device + ": Ready False" + withReason(cond.Reason)}, nil
	}
	if c.Allocated {
		if c.ReservedCount > 0 {
			return health.Verdict{Kind: health.OK, Label: "Allocated", Reason: "InUse", Native: native, Rule: "claim.allocated",
				Message: fmt.Sprintf("in use by %d", c.ReservedCount)}, nil
		}
		return health.Verdict{Kind: health.OK, Label: "Allocated", Reason: "NotInUse", Native: native, Rule: "claim.allocated", Message: "not in use"}, nil
	}
	if len(consumers) == 0 {
		if consumersKnown {
			return health.Verdict{Kind: health.OK, Label: "Pending", Reason: "Unused", Native: native, Rule: "claim.unused", Message: "not used by any pod"}, nil
		}
		return health.Verdict{Kind: health.Unknown, Label: "Pending", Reason: "ConsumersUnknown", Native: native, Rule: "claim.consumers-unknown",
			Message: "its consumers are not established: the pods read is not complete and current"}, nil
	}
	return pendingVerdict(c, consumers, native)
}

// pendingVerdict is the decision table (S11b §3.1, revision 2).
func pendingVerdict(c Claim, consumers []ConsumerPod, native string) (health.Verdict, []ConsumerReading) {
	key := c.Key()
	// row a decides first, for the claim as a whole
	for _, cp := range consumers {
		if cond := scheduled(cp.Pod); cond != nil {
			for _, r := range Reasons(cond.Message) {
				if r.Scope == InProgress && r.Claim == key {
					return health.Verdict{Kind: health.Progressing, Label: "Being allocated", Reason: "Allocating", Native: native, Rule: "claim.allocating"},
						[]ConsumerReading{{Pod: cp.Pod.Name, Row: "a", Kind: health.Progressing, Label: "being allocated", Reason: r.Key}}
				}
			}
		}
	}
	var rows []ConsumerReading
	for _, cp := range consumers {
		rows = append(rows, consumerRow(c, cp))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].Pod < rows[j].Pod
	})
	w := rows[0]
	v := health.Verdict{Kind: w.Kind, Label: w.Label, Native: native, Message: w.Message}
	switch w.Row {
	case "b":
		v.Reason, v.Rule = "CannotAllocate", "claim.cannot-allocate"
		v.Message = joinNonEmpty("pod "+w.Pod+": "+Meaning(w.Reason), w.Message)
	case "c":
		v.Reason, v.Rule = "PodCannotAllocate", "claim.pod-cannot-allocate"
		v.Message = joinNonEmpty(Meaning(w.Reason), w.Message)
	case "d":
		v.Reason, v.Rule = "SchedulerError", "claim.waiting-scheduler-error"
	case "e":
		v.Reason, v.Rule = "PodUnschedulable", "claim.waiting-unschedulable"
	case "f":
		v.Reason, v.Rule = "WaitingForScheduler", "claim.waiting-scheduler"
	case "g":
		v.Reason, v.Rule = "PodScheduledUnallocated", "claim.scheduled-unallocated"
	}
	if len(rows) > 1 {
		v.Note = fmt.Sprintf("%d consumer pods; the worst reading shown", len(rows))
	}
	return v, rows
}

func consumerRow(c Claim, cp ConsumerPod) ConsumerReading {
	p := cp.Pod
	cond := scheduled(p)
	row := ConsumerReading{Pod: p.Name}
	if p.Spec.NodeName != "" || (cond != nil && cond.Status == corev1.ConditionTrue) {
		row.Row, row.Kind, row.Label = "g", health.Unknown, "its pod is on a node, but the claim is not allocated"
		return row
	}
	if cond == nil || cond.Status != corev1.ConditionFalse {
		row.Row, row.Kind, row.Label = "f", health.Progressing, "waiting for the scheduler"
		return row
	}
	row.Message = cond.Message
	if cond.Reason == corev1.PodReasonUnschedulable || cond.Reason == corev1.PodReasonSchedulerError {
		for _, r := range Reasons(cond.Message) {
			if r.Scope == InProgress {
				continue // another claim's allocation in progress: not this claim's fault
			}
			row.Reason = r.Key
			if claimSpecific(c, cp, r) {
				row.Row, row.Kind, row.Label = "b", health.Degraded, "the scheduler cannot allocate it"
			} else {
				row.Row, row.Kind, row.Label = "c", health.Degraded,
					"its pod "+p.Name+" cannot have its claims allocated; which of its "+strconv.Itoa(max(len(cp.Claims), 1))+" claims is the cause is not established"
			}
			return row
		}
	}
	switch cond.Reason {
	case corev1.PodReasonSchedulerError:
		row.Row, row.Kind, row.Label = "d", health.Progressing, "waiting for its pod: the scheduler reported an error, and its relation to this claim is not established"
	case corev1.PodReasonUnschedulable:
		row.Row, row.Kind, row.Label = "e", health.Progressing, "waiting for its pod: it is unschedulable, and the message names no DRA reason Klyx recognises"
	default:
		row.Row, row.Kind, row.Label = "f", health.Progressing, "waiting for the scheduler ("+cond.Reason+")"
	}
	return row
}

// claimSpecific is row b's proof (S11b §3.1): the reason names this claim;
// or names a class only this claim's requests use among the pod's claims;
// or this is the pod's only DRA claim. Uniqueness needs complete evidence.
func claimSpecific(c Claim, cp ConsumerPod, r Reason) bool {
	if r.Scope == NamesClaim {
		return r.Claim == c.Key()
	}
	if !cp.Complete {
		return false
	}
	if len(cp.Claims) == 1 && cp.Claims[0] == c.Key() {
		return true
	}
	if r.Scope != NamesClass || !contains(c.Classes(), r.Class) {
		return false
	}
	for _, other := range cp.Claims {
		if other != c.Key() && contains(cp.Classes[other], r.Class) {
			return false
		}
	}
	return true
}

// notReady finds an allocated device whose driver reports Ready False.
func notReady(c Claim) (DeviceStatus, Cond, bool) {
	for _, d := range c.Devices {
		for _, cond := range d.Conditions {
			if cond.Type == "Ready" && cond.Status == "False" {
				return d, cond, true
			}
		}
	}
	return DeviceStatus{}, Cond{}, false
}

// claimState is kubectl's state column (S11b §1.3).
func claimState(c Claim) string {
	var s []string
	if c.Deleting {
		s = append(s, "deleted")
	}
	if !c.Allocated {
		s = append(s, "pending")
	} else {
		s = append(s, "allocated")
		if c.ReservedCount > 0 {
			s = append(s, "reserved")
		}
	}
	return strings.Join(s, ",")
}

// ---- pools ----------------------------------------------------------------

// VariantOutcome is one scheduler's outcome for one view.
type VariantOutcome struct {
	Variant Variant
	Outcome Outcome
}

// View is one node's view of a pool ("" is a node no slice names), read
// under every allowed scheduler.
type View struct {
	Node     string
	Outcomes []VariantOutcome
	// Agreed: every variant reached the same outcome, or every one an
	// outcome that reads OK. Kind is then that outcome ("ok" when they
	// agree only in severity).
	Agreed bool
	Kind   string
}

// PoolRead is a pool read under every scheduler the cluster may run.
type PoolRead struct {
	Driver, Pool string
	Generation   int64
	Slices       int
	Views        []View
	Variants     []Variant
	// Obsolete names nodes on which every variant finds the pool obsolete.
	Obsolete []string
}

// Variants are the schedulers a server minor allows (S11b §3.2): the minor
// and one older; every modelled family for an unknown minor (0); the
// partitionable-devices feature both ways where the pool uses it.
func Variants(serverMinor int, slices []Slice) []Variant {
	var minors []int
	switch {
	case serverMinor == 0:
		minors = []int{33, 34, 35}
	case serverMinor <= 33:
		minors = []int{33}
	default:
		minors = []int{serverMinor - 1, serverMinor}
	}
	gated := false
	for _, s := range slices {
		gated = gated || s.PerDevice || s.usesCounters()
	}
	var out []Variant
	for _, m := range minors {
		out = append(out, Variant{Minor: m})
		if gated {
			out = append(out, Variant{Minor: m, Partitionable: true})
		}
	}
	return out
}

// okOutcome: an outcome that reads OK.
func okOutcome(k string) bool { return k == OutComplete || k == OutNone || k == OutObsolete }

// ReadPool reads one pool's views under the allowed schedulers.
func ReadPool(slices []Slice, serverMinor int) PoolRead {
	p := PoolRead{Slices: len(slices), Variants: Variants(serverMinor, slices)}
	if len(slices) > 0 {
		p.Driver, p.Pool = slices[0].Driver, slices[0].Pool
	}
	nodes := map[string]bool{}
	for _, s := range slices {
		if s.Generation > p.Generation {
			p.Generation = s.Generation
		}
		if s.NodeName != "" {
			nodes[s.NodeName] = true
		}
		for _, d := range s.Devices {
			if d.NodeName != "" {
				nodes[d.NodeName] = true
			}
		}
	}
	names := make([]string, 0, len(nodes)+1)
	for n := range nodes {
		names = append(names, n)
	}
	sort.Strings(names)
	names = append(names, "")
	for _, n := range names {
		v := View{Node: n}
		obsolete := true
		for _, va := range p.Variants {
			o := ViewOutcome(slices, n, va)
			v.Outcomes = append(v.Outcomes, VariantOutcome{Variant: va, Outcome: o})
			obsolete = obsolete && o.Kind == OutObsolete
		}
		v.Agreed, v.Kind = agree(v.Outcomes)
		if obsolete && n != "" {
			p.Obsolete = append(p.Obsolete, n)
		}
		p.Views = append(p.Views, v)
	}
	return p
}

func agree(os []VariantOutcome) (bool, string) {
	first := os[0].Outcome.Kind
	same, allOK := true, true
	for _, o := range os {
		same = same && o.Outcome.Kind == first
		allOK = allOK && okOutcome(o.Outcome.Kind)
	}
	switch {
	case same && first != OutUnestablished:
		return true, first
	case allOK:
		return true, "ok"
	}
	return false, ""
}

// UsesGate: a variant varies the partitionable-devices feature.
func (p PoolRead) UsesGate() bool {
	for _, v := range p.Variants {
		if v.Partitionable {
			return true
		}
	}
	return false
}

// Assumed says which schedulers the reading allowed for (S11b §3.2).
func (p PoolRead) Assumed() string {
	seen := map[int]bool{}
	var minors []string
	for _, v := range p.Variants {
		if !seen[v.Minor] {
			seen[v.Minor] = true
			minors = append(minors, "1."+strconv.Itoa(v.Minor))
		}
	}
	s := "read for a " + strings.Join(minors, " or ") + " scheduler"
	if p.UsesGate() {
		s += ", with the partitionable-devices feature on or off"
	}
	return s
}

func viewName(n string) string {
	if n == "" {
		return "a node no slice names (if there is one)"
	}
	return n
}

// Verdict reads the pool (S11b §3.2): its worst view. incompleteSince is
// the start of the pool's incomplete episode (zero when unknown).
func (p PoolRead) Verdict(incompleteSince, now time.Time) health.Verdict {
	type vv struct {
		v    health.Verdict
		node string
	}
	var all []vv
	for _, view := range p.Views {
		all = append(all, vv{p.viewVerdict(view, incompleteSince, now), view.Node})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].v.Kind < all[j].v.Kind })
	v := all[0].v
	v.Native = fmt.Sprintf("%d slice%s, generation %d", p.Slices, plural(p.Slices), p.Generation)
	v.Note = p.Assumed()
	return v
}

func (p PoolRead) viewVerdict(view View, since, now time.Time) health.Verdict {
	where := viewName(view.Node)
	if !view.Agreed {
		var parts []string
		for _, o := range view.Outcomes {
			k := o.Outcome.Kind
			if k == OutUnestablished {
				k = "not established (" + o.Outcome.Rule + ")"
			}
			parts = append(parts, o.Variant.String()+": "+k)
		}
		return health.Verdict{Kind: health.Unknown, Label: "Scheduler health not established", Reason: "VariantsDisagree", Rule: "pool.not-established",
			Message: "on " + where + ", it depends on the scheduler's version or feature gate, which Klyx cannot read: " + strings.Join(parts, "; ")}
	}
	o := view.Outcomes[0].Outcome
	switch view.Kind {
	case OutUpdating:
		msg := fmt.Sprintf("on %s (%d of %d slices)", where, o.Have, o.Want)
		if since.IsZero() {
			return health.Verdict{Kind: health.Progressing, Label: "Being updated", Reason: "Incomplete", Rule: "pool.updating", Message: msg}
		}
		if now.Sub(since) < Grace {
			at := since.Add(Grace)
			return health.Verdict{Kind: health.Progressing, Label: "Being updated", Reason: "Incomplete", Rule: "pool.updating", Message: msg, ReclassifyAt: &at}
		}
		return health.Verdict{Kind: health.Degraded, Label: "Incomplete", Reason: "IncompleteTooLong", Rule: "pool.incomplete",
			Message: "the pool has been incomplete for over 5 minutes: the scheduler allocates nothing from it " + msg}
	case OutInvalid:
		return health.Verdict{Kind: health.Degraded, Label: "Invalid", Reason: "Invalid", Rule: "pool.invalid",
			Message: "on " + where + ": the scheduler allocates nothing from it there (" + o.Rule + ")"}
	case OutUnestablished:
		return health.Verdict{Kind: health.Unknown, Label: "Not established", Reason: "NotEstablished", Rule: "pool.not-established", Message: "on " + where + ": " + o.Rule}
	}
	return health.Verdict{Kind: health.OK, Label: "Complete", Reason: "Complete", Rule: "pool.complete"}
}

// ---- helpers --------------------------------------------------------------

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

func withReason(r string) string {
	if r == "" {
		return ""
	}
	return " (" + r + ")"
}

// orToken keeps a reason only when it is a machine token.
func orToken(r, def string) string {
	if r == "" || len(r) > 64 || strings.ContainsAny(r, " \t\n") {
		return def
	}
	return r
}

func short(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	}
	return strconv.Itoa(int(d.Hours())) + "h"
}

// EpisodeKey keys an incomplete pool's episode: the pool at one generation.
func EpisodeKey(driver, pool string, generation int64) string {
	return driver + "/" + pool + "@" + strconv.FormatInt(generation, 10)
}

// AnyUpdating: some allowed scheduler finds the pool being updated on some
// view (the episode tracker's test).
func (p PoolRead) AnyUpdating() bool {
	for _, v := range p.Views {
		for _, o := range v.Outcomes {
			if o.Outcome.Kind == OutUpdating {
				return true
			}
		}
	}
	return false
}
