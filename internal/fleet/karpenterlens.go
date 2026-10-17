package fleet

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/urlscrub"
	"github.com/moomora/klyx/internal/workloads"
)

// karpenterEventCap bounds the disruption feed: it is recent activity, not an
// audit (the full event stream lives in the Events lens).
const karpenterEventCap = 30

// KarpenterSnapshot is the Karpenter lens's on-demand read: controller
// health re-probed live (the capability snapshot is connect-time; the lens
// headline must not go stale) and recent NodePool/NodeClaim events - the "why
// did my node get replaced" feed - as of AsOf. The NodePools and NodeClaims
// themselves are the health Observer's shared rows (1c slice 3 §5.5).
type KarpenterSnapshot struct {
	Detected bool
	AsOf     time.Time
	// ControllerVerified: a karpenter Deployment was actually read. When
	// false, ControllerHealthy carries no claim and ControllerError says why
	// (forbidden, not found under the expected name, ...).
	ControllerVerified bool
	ControllerHealthy  bool
	ControllerError    string
	Events             []workloads.EventSummary
	// EventsError names event reads that failed (the feed is then partial or
	// unreadable, never quiet); EventsTruncated: more events exist than shown.
	EventsError     string
	EventsTruncated bool
}

// KarpenterState returns the Karpenter lens's controller probe and
// disruption feed. Capability-gated (honest "not detected", never an empty
// lens).
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

	// Controller health, re-probed live (mirrors the detector's namespace
	// pair). Only a Deployment actually read is evidence: a forbidden read or
	// a controller installed under another name is unverified, never a
	// confirmed outage.
	var ctrlErrs []string
	for _, ns := range []string{"kube-system", "karpenter"} {
		dep, derr := c.typed.AppsV1().Deployments(ns).Get(ctx, "karpenter", metav1.GetOptions{})
		if derr == nil && dep != nil {
			out.ControllerVerified = true
			out.ControllerHealthy = dep.Status.ReadyReplicas > 0
			break
		}
		if derr != nil && !apierrors.IsNotFound(derr) {
			ctrlErrs = append(ctrlErrs, ns+": "+derr.Error())
		}
	}
	if !out.ControllerVerified {
		if len(ctrlErrs) > 0 {
			out.ControllerError = "controller Deployment unreadable (" + strings.Join(ctrlErrs, "; ") + ")"
		} else {
			out.ControllerError = "no Deployment named karpenter in kube-system or karpenter"
		}
	}

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
			msg, _ := crd.MessageRule(e.InvolvedObject.Kind, e.Reason, e.Message) // S1b §6
			out.Events = append(out.Events, workloads.EventSummary{
				Type: e.Type, Reason: e.Reason, Message: urlscrub.Text(msg).Text, Count: e.Count,
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
