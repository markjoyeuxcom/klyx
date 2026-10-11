package healthsrc

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

// The Events task (1c spec §1.1): never live, evidence only - Events never
// contribute to a verdict. A bounded read of Warning events across all
// namespaces every EventsCadence; a capped read is marked truncated and is
// shown as "≥500" with its as-of time, never as a quiet count.
const (
	SnapEvents = "events"
	TaskEvents = "events"

	EventsCadence     = 60 * time.Second
	EventsReadTimeout = 30 * time.Second
	EventsLimit       = 500
)

// EventsDeps are what the Events snapshot needs.
type EventsDeps struct {
	Cluster string
	Typed   kubernetes.Interface
}

// Events returns the Warning-events snapshot and its evidence task.
func Events(d EventsDeps) ([]observe.SnapshotSpec, []observe.DomainSpec) {
	snap := observe.SnapshotSpec{Name: SnapEvents, Scope: "all namespaces", Cadence: EventsCadence, Timeout: EventsReadTimeout,
		Read: func(ctx context.Context) ([]any, bool, error) {
			list, err := d.Typed.CoreV1().Events("").List(ctx, metav1.ListOptions{FieldSelector: "type=Warning", Limit: EventsLimit})
			if err != nil {
				return nil, false, err
			}
			// The cap returns the first 500 in etcd key order: a subset, so
			// the count is a floor and says so.
			truncated := list.Continue != "" || (list.RemainingItemCount != nil && *list.RemainingItemCount > 0)
			sums := workloads.SummarizeEvents(list.Items)
			items := make([]any, 0, len(sums))
			for _, e := range sums {
				if e.Type == "Warning" { // the field selector is the server's; hold it here too
					items = append(items, e)
				}
			}
			return items, truncated, nil
		}}
	domains := []observe.DomainSpec{{ID: TaskEvents, Parts: []observe.PartSpec{
		{ID: "warnings", Entity: "Warning events", Scope: "all namespaces", Classifier: "evidence",
			Counted: []string{SnapEvents}, Evaluate: evalWarnings},
	}}}
	return []observe.SnapshotSpec{snap}, domains
}

// evalWarnings counts Warning event objects (not occurrences). Evidence
// only: no verdict, no Top refs.
func evalWarnings(c observe.Capture) observe.PartResult {
	n := len(c.Objects[SnapEvents])
	res := observe.PartResult{Evidence: &n}
	if c.Rows {
		rows := make([]workloads.EventSummary, 0, n)
		for _, o := range c.Objects[SnapEvents] {
			rows = append(rows, o.(workloads.EventSummary))
		}
		res.Rows = rows
	}
	return res
}
