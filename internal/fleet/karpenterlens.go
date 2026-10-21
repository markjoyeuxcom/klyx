package fleet

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/urlscrub"
	"github.com/moomora/klyx/internal/workloads"
)

// karpenterEventCap bounds the disruption feed: it is recent activity, not an
// audit (the full event stream lives in the Events lens).
const karpenterEventCap = 30

// KarpenterSnapshot is the Karpenter lens's on-demand read: recent
// NodePool/NodeClaim events - the "why did my node get replaced" feed - as
// of AsOf. The NodePools, NodeClaims and NodeClasses are the health
// Observer's shared rows (1c slice 3 §5.5), and the controllers are the
// task's own reading (coverage S10 §5: an attributed inventory and platform
// evidence), so the lens no longer probes one Deployment by name.
type KarpenterSnapshot struct {
	Detected bool
	AsOf     time.Time
	Events   []workloads.EventSummary
	// EventsError names event reads that failed (the feed is then partial or
	// unreadable, never quiet); EventsTruncated: more events exist than shown.
	EventsError     string
	EventsTruncated bool
}

// KarpenterState returns the Karpenter lens's disruption feed.
// Capability-gated (honest "not detected", never an empty lens).
func (c *ClusterConn) KarpenterState(ctx context.Context) (KarpenterSnapshot, error) {
	c.mu.RLock()
	present := c.caps.Karpenter.Present
	c.mu.RUnlock()
	if !present {
		return KarpenterSnapshot{Detected: false}, nil
	}
	if c.typed == nil {
		return KarpenterSnapshot{}, fmt.Errorf("cluster client not available")
	}
	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	out := KarpenterSnapshot{Detected: true, AsOf: clk.Now()}

	// Disruption feed: events about NodeClaims and NodePools (launch failures,
	// disruption/consolidation decisions, drift replacements). Optional: a
	// failing event read never blocks the triage above, but it is reported -
	// the feed is then partial or unreadable, never "quiet".
	var evErrs []string
	for _, kind := range []string{"NodeClaim", "NodePool"} {
		evs, eerr := c.typed.CoreV1().Events("").List(ctx, metav1.ListOptions{
			FieldSelector: "involvedObject.kind=" + kind,
			Limit:         karpenterEventCap,
		})
		if eerr != nil || evs == nil {
			msg := "no response"
			if eerr != nil {
				msg = eerr.Error()
			}
			evErrs = append(evErrs, kind+" events: "+msg)
			continue
		}
		if evs.Continue != "" || len(evs.Items) >= karpenterEventCap {
			out.EventsTruncated = true
		}
		for i := range evs.Items {
			e := &evs.Items[i]
			// Kind from the event itself, and a client-side guard: a server
			// (or fake) that ignores the field selector must not duplicate
			// rows across the two queries.
			if e.InvolvedObject.Kind != kind {
				continue
			}
			last := e.LastTimestamp.Time
			if last.IsZero() {
				last = e.CreationTimestamp.Time
			}
			out.Events = append(out.Events, workloads.EventSummary{
				Type: e.Type, Reason: e.Reason, Message: urlscrub.Text(workloads.EventMessage(e)).Text, Count: e.Count,
				Namespace: e.Namespace, Kind: e.InvolvedObject.Kind, Name: e.InvolvedObject.Name,
				LastSeenUnix: last.Unix(),
			})
		}
	}
	sort.Slice(out.Events, func(i, j int) bool { return out.Events[i].LastSeenUnix > out.Events[j].LastSeenUnix })
	if len(out.Events) > karpenterEventCap {
		out.Events = out.Events[:karpenterEventCap]
		out.EventsTruncated = true
	}
	out.EventsError = strings.Join(evErrs, "; ")
	return out, nil
}
