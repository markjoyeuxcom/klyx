package healthsrc

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/moomora/klyx/internal/extres"
	"github.com/moomora/klyx/internal/observe"
)

// TaskExtended is coverage S11a's task: what pods reserve of each node's
// extended resources, and the accelerator totals. It has no area of its
// own (no navigation entry, no Needs you line): the Nodes lens joins its
// rows. It is a task apart from Nodes because the nodes part is a ledger
// part whose continuity would follow a pods dependency, and a task's
// coverage is the worst of its parts' (S11a §2).
const TaskExtended = "extended"

// extendedDomain is the extended task: two evidence parts over the nodes
// (counted) and the pods (a dependency). Each part's Evidence is its full
// count before any cut (nodes with extended resources; accelerator names),
// so a reader knows how many rows a bound left out.
func extendedDomain() observe.DomainSpec {
	return observe.DomainSpec{ID: TaskExtended, Parts: []observe.PartSpec{
		{ID: "reservations", Entity: "extended resource reservations", Scope: "cluster-scoped", Classifier: "evidence",
			Counted: []string{KindNodes}, Deps: []string{KindPods}, Evaluate: evalReservations},
		{ID: "totals", Entity: "accelerator totals", Scope: "cluster-scoped", Classifier: "evidence",
			Counted: []string{KindNodes}, Deps: []string{KindPods}, Evaluate: evalExtendedTotals},
	}}
}

// FactOnly: a reservations or totals row is a fact, never a verdict.
func (ReservationRow) FactOnly() bool { return true }

// FactOnly: a totals row is a fact, never a verdict.
func (TotalRow) FactOnly() bool { return true }

// ReservationRow is one node's reservations (extres.NodeRow).
type ReservationRow struct{ extres.NodeRow }

// TotalRow is one accelerator name's cluster total (extres.Total).
type TotalRow struct{ extres.Total }

func reserveCapture(c observe.Capture) extres.Result {
	var nodes []*corev1.Node
	for _, x := range c.Objects[KindNodes] {
		if n, ok := x.(*corev1.Node); ok {
			nodes = append(nodes, n)
		}
	}
	var pods []*corev1.Pod
	for _, x := range c.Objects[KindPods] {
		if p, ok := x.(*corev1.Pod); ok {
			pods = append(pods, p)
		}
	}
	return extres.Reserve(nodes, pods)
}

func evalReservations(c observe.Capture) observe.PartResult {
	res := reserveCapture(c)
	n := len(res.Rows) + res.RowsDropped
	out := observe.PartResult{Evidence: &n}
	if c.Rows {
		rows := make([]Row[ReservationRow], 0, len(res.Rows))
		for _, r := range res.Rows {
			rows = append(rows, Row[ReservationRow]{Ref: RowRef{UID: string(r.UID), GVR: "v1/nodes", Name: r.Node}, Data: ReservationRow{r}})
		}
		out.Rows = rowSet(rows)
	}
	return out
}

func evalExtendedTotals(c observe.Capture) observe.PartResult {
	res := reserveCapture(c)
	n := len(res.Totals) + res.TotalsOmitted
	out := observe.PartResult{Evidence: &n}
	if c.Rows {
		rows := make([]Row[TotalRow], 0, len(res.Totals))
		for _, t := range res.Totals {
			rows = append(rows, Row[TotalRow]{Ref: RowRef{Name: t.Name}, Data: TotalRow{t}})
		}
		out.Rows = rowSet(rows)
	}
	return out
}
