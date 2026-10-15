package appbridge

import (
	"strconv"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/obslog"
)

// Rule history (The Line slice 7b, parent §8.5 "attributed history"): each
// HTTPRoute rule's fingerprint, recorded as predicate batches in the
// observation log under task "routes", part "rules" - one key per route UID
// and rule index, its value the fingerprint. A rule's metric series is
// attributable only inside a proven interval: the same route UID and the
// same fingerprint, observed continuously.
//
// The writer is a health-sink listener: it runs on each Observer's loop, so
// it only diffs and appends to the log under its own lock. The route rows
// flow because LineService subscribes them on every station's Observer.

const (
	routesTask      = "routes"
	routesPartRules = "rules"
	routesMode      = "routes"
	// ruleRateWindow is the rate window of the per-rule queries: points
	// within it after an interval's start still mix the previous rule's
	// samples, so attribution starts after it.
	ruleRateWindow = time.Minute
)

// routesOwner holds the gateway domain's lease and route rows for the rule
// history.
var routesOwner = observe.Owner{Kind: "routes", ID: "rules"}

func ruleKey(uid string, index int) string { return "route/" + uid + "#" + strconv.Itoa(index) }

type ruleCluster struct {
	inc    uint64
	prev   map[string]string
	live   bool
	seen   bool
	ended  bool // the incarnation's Observer ended: only the fence remains
	lastAt time.Time
}

type routeHistory struct {
	mu  sync.Mutex
	log *obslog.Log
	st  map[string]*ruleCluster
}

func newRouteHistory(log *obslog.Log) *routeHistory {
	return &routeHistory{log: log, st: map[string]*ruleCluster{}}
}

// RouteRuleHistory registers the rule history on the health sink and
// returns it: a package-level accessor, so nothing is bound to the webview.
func RouteRuleHistory(sink *HealthSink) *routeHistory {
	h := newRouteHistory(sink.ObservationLog())
	sink.AddListener(h)
	return h
}

func (h *routeHistory) OnSummary(observe.Summary) {}

// OnRows commits the routes part's rows: a baseline at first sight, an
// after-gap once readable again, then only real changes.
func (h *routeHistory) OnRows(r observe.Rows) {
	if r.Task != healthsrc.TaskGateway || r.Part.ID != "routes" {
		return
	}
	rs, ok := r.Rows.(healthsrc.RowSet[observe.Attached])
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.st[r.Cluster]
	if c == nil || r.Incarnation > c.inc {
		c = &ruleCluster{inc: r.Incarnation}
		h.st[r.Cluster] = c
	} else if r.Incarnation < c.inc || c.ended {
		return
	}
	at := r.EvaluatedAt
	if at.Before(c.lastAt) {
		at = c.lastAt // evidence times only move forward
	}
	readable := ok && routesReadable(r.Part)
	b := observe.TransitionBatch{Cluster: r.Cluster, Incarnation: r.Incarnation, Task: routesTask, Part: routesPartRules,
		Source: routesPartRules, Mode: routesMode, At: at}
	if !readable {
		if c.live {
			loss := b
			loss.Kind, loss.Loss = observe.BatchLoss, &observe.Loss{Reason: observe.LossCoverage}
			prev := c.lastAt
			loss.PrevAt = &prev
			h.apply(loss)
			c.live = false
		}
		return
	}
	cur := map[string]string{}
	for _, row := range rs.Rows {
		for i, fp := range row.Data.Rules {
			cur[ruleKey(row.Data.UID, i)] = fp
		}
	}
	switch {
	case !c.seen:
		b.Kind, b.Items = observe.BatchBaseline, diffPreds(nil, cur)
	case !c.live:
		b.Kind, b.Items = observe.BatchAfterGap, diffPreds(c.prev, cur)
	default:
		b.Kind, b.Items = observe.BatchChanges, diffPreds(c.prev, cur)
		prev := c.lastAt
		b.PrevAt = &prev
		if len(b.Items) == 0 {
			return // nothing changed: the evidence watermark stays
		}
	}
	h.apply(b)
	c.prev, c.live, c.seen, c.lastAt = cur, true, true, at
}

// OnTransitions: a loss of the Observer's own routes partition is a break in
// the rules' continuity too; the next readable rows re-baseline after it. The
// Observer's end drops the cluster's fingerprints (the log ends the partition
// itself) and keeps only the incarnation fence, so a removed cluster leaves
// nothing behind but that.
func (h *routeHistory) OnTransitions(b observe.TransitionBatch) {
	if b.Kind == observe.BatchEnd {
		h.mu.Lock()
		defer h.mu.Unlock()
		if c := h.st[b.Cluster]; c != nil && b.Incarnation == c.inc {
			h.st[b.Cluster] = &ruleCluster{inc: c.inc, ended: true}
		}
		return
	}
	if b.Task != healthsrc.TaskGateway || b.Part != "routes" || b.Kind != observe.BatchLoss {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.st[b.Cluster]
	if c == nil || b.Incarnation != c.inc || !c.live {
		return
	}
	at := b.At
	if at.Before(c.lastAt) {
		at = c.lastAt
	}
	prev := c.lastAt
	h.apply(observe.TransitionBatch{Cluster: b.Cluster, Incarnation: b.Incarnation, Task: routesTask, Part: routesPartRules,
		Source: routesPartRules, Mode: routesMode, Kind: observe.BatchLoss, At: at, PrevAt: &prev,
		Loss: &observe.Loss{Reason: observe.LossCoverage}})
	c.live = false
}

// routesReadable: both the routes and the Gateways they attach to are live;
// otherwise a vanished row could be a coverage gap, not a gone rule.
func routesReadable(p observe.Part) bool {
	live := map[string]bool{}
	for _, k := range p.Kinds {
		live[k.Kind] = k.Coverage == observe.Live
	}
	return live[healthsrc.KindHTTPRoutes] && live[healthsrc.KindGateways]
}

func (h *routeHistory) apply(b observe.TransitionBatch) {
	if h.log != nil {
		h.log.Apply(b)
	}
}

// RuleInterval is a rule's current proven interval: its start (the proof)
// and the attribution cut-off, one rate window later. Two times, kept apart
// (slice 7 note §4).
type RuleInterval struct {
	Start  time.Time
	Cutoff time.Time
	Basis  string // how Start is known: observed (a fingerprint change) or unknown (first sighting, or after a gap)
}

// Interval reads a rule's proven interval back from the log: the latest of
// its last fingerprint change, its first sighting and the start of the
// partition's current live span. None while the partition is lost, the
// rule gone, never seen, or trimmed, or when the log's fingerprint is not
// the one the caller read (the log has not caught up with that rule).
func (h *routeHistory) Interval(cluster, uid string, index int, fingerprint string) (RuleInterval, bool) {
	if h.log == nil {
		return RuleInterval{}, false
	}
	var live *obslog.PartitionState
	for _, ps := range h.log.Partitions(cluster, routesTask, routesPartRules) {
		if ps.Source == routesPartRules {
			p := ps
			live = &p
		}
	}
	if live == nil || !live.Live {
		return RuleInterval{}, false
	}
	p, ok, _ := h.log.ProofOf(cluster, ruleKey(uid, index))
	if !ok || p.Suspended || p.Role != fingerprint {
		return RuleInterval{}, false
	}
	start, basis := p.FirstSeen, obslog.ProofUnknown
	if p.Basis == obslog.ProofObserved && p.Since != nil && !p.Since.Before(start) {
		start, basis = *p.Since, obslog.ProofObserved
	}
	if live.Since.After(start) {
		start, basis = live.Since, obslog.ProofUnknown
	}
	return RuleInterval{Start: start, Cutoff: start.Add(ruleRateWindow), Basis: basis}, true
}
