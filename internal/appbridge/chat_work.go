package appbridge

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/config"
)

// aiWork is the AI work an Apply of the AI settings affects (Settings slice
// F §5), by identity: each open conversation under the running revision,
// its question being answered, its offered plan, and a running step's
// verification end. A confirmed step request continues - the AI revision
// does not fence it (A3 §4.4) - and is named apart until it returns,
// whatever its conversation's revision (F2a review, R4; re-review, RR2).
// A step is named by its conversation, the exchange that proposed its
// plan, its number and its phase (RR1). A conversation from an earlier
// revision is already ended for new turns, so it is not named again.
func (s *ChatService) aiWork() aiWorkList {
	rev := s.ai.load().Rev
	s.mu.Lock()
	defer s.mu.Unlock()
	var out, continues []PlanWorkDTO
	for _, c := range s.convs {
		if c.ended != "" || c.rev != rev {
			continue
		}
		started := ""
		if !c.started.IsZero() {
			started = c.started.UTC().Format(time.RFC3339)
		}
		what := "a chat on " + c.cluster
		if c.kind == kindInvestigation {
			what = "an investigation on " + c.cluster
		}
		out = append(out, PlanWorkDTO{ID: "conv:" + c.id, Kind: c.kind, Cluster: c.cluster, Started: started, What: what})
		if c.active != nil {
			out = append(out, PlanWorkDTO{ID: "turn:" + c.active.id, Kind: "turn", Cluster: c.cluster, What: "a question being answered on " + c.cluster})
		}
		// the current proposal only: a later one supersedes the earlier
		// (planLocked)
		for i := len(c.exchanges) - 1; i >= 0; i-- {
			e := c.exchanges[i]
			if e.plan == nil || len(e.plan.dto.Steps) == 0 {
				continue
			}
			if p := e.plan; p.withdrawn == "" && !p.declined && p.settled && offersAStep(p) {
				out = append(out, PlanWorkDTO{ID: "offer:" + c.id + ":" + e.id, Kind: "offer", Cluster: c.cluster, What: "an action plan offered on " + c.cluster})
			}
			break
		}
		if p := c.stepRunning; p != nil && p.running > 0 {
			if ex := planExchangeLocked(c, p); ex != "" {
				out = append(out, PlanWorkDTO{ID: stepWorkID(c, ex, p.running) + ":verify", Kind: "step", Cluster: c.cluster, What: "the verification of " + stepWhatLocked(c, ex, p.running) + ": it ends unverified"})
			}
		}
	}
	for _, w := range s.requests {
		continues = append(continues, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	sort.Slice(continues, func(i, j int) bool { return continues[i].ID < continues[j].ID })
	return aiWorkList{ends: out, continues: continues}
}

// stepWorkID names step n of the plan exchange proposed in c.
func stepWorkID(c *conversation, exchange string, n int) string {
	return fmt.Sprintf("step:%s:%s:%d", c.id, exchange, n)
}

// stepWhatLocked says which step it is: its number, the turn that proposed
// its plan, and the cluster.
func stepWhatLocked(c *conversation, exchange string, n int) string {
	for _, e := range c.exchanges {
		if e.id == exchange {
			return fmt.Sprintf("step %d of the action plan proposed in turn %d on %s", n, e.turn, c.cluster)
		}
	}
	return fmt.Sprintf("step %d of an action plan on %s", n, c.cluster)
}

// planExchangeLocked is the exchange that proposed p in c.
func planExchangeLocked(c *conversation, p *actionPlan) string {
	for _, e := range c.exchanges {
		if e.plan == p {
			return e.id
		}
	}
	return ""
}

// endRequest ends a confirmed request's work, once it returned or was
// refused.
func (s *ChatService) endRequest(id string) {
	s.mu.Lock()
	delete(s.requests, id)
	s.mu.Unlock()
}

func offersAStep(p *actionPlan) bool {
	for _, st := range p.dto.Steps {
		if st.State == stepOffered {
			return true
		}
	}
	return false
}

// SourceWork is the AI work holding data read under terms that move
// (Settings S3a §5, S3b §5), by identity: each open conversation that
// recorded a lineage of a moved kind on a cluster whose terms of that
// kind move, which ends at its next send once an Observability Apply
// moves them, and its offered plan, which is withdrawn then (§4.3). A
// conversation holding only other kinds' data, or another cluster's, is
// not listed (§13). moved is, for each kind, its clusters.
func (s *ChatService) SourceWork(moved map[string][]string) []PlanWorkDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []PlanWorkDTO{}
	for _, c := range s.convs {
		held := heldMoved(c.lineages, c.cluster, moved)
		if c.ended != "" || len(held) == 0 {
			continue
		}
		started := ""
		if !c.started.IsZero() {
			started = c.started.UTC().Format(time.RFC3339)
		}
		what := "a chat on " + c.cluster
		if c.kind == kindInvestigation {
			what = "an investigation on " + c.cluster
		}
		out = append(out, PlanWorkDTO{ID: "conv:" + c.id, Kind: c.kind, Cluster: c.cluster, Started: started, What: what + " holding " + heldNouns(held) + " read under the old terms: it ends at its next send"})
		for i := len(c.exchanges) - 1; i >= 0; i-- {
			e := c.exchanges[i]
			if e.plan == nil || len(e.plan.dto.Steps) == 0 {
				continue
			}
			if p := e.plan; p.withdrawn == "" && !p.declined && p.settled && offersAStep(p) {
				out = append(out, PlanWorkDTO{ID: "offer:" + c.id + ":" + e.id, Kind: "offer", Cluster: c.cluster, What: "an action plan offered on " + c.cluster + ": it is withdrawn"})
			}
			break
		}
	}
	out = append(out, explainWork(moved)...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// heldMoved are the lineages a call holding ls on cluster holds of a kind
// whose terms move there; an alerts read's kind is empty.
func heldMoved(ls []alertmanager.Lineage, cluster string, moved map[string][]string) []alertmanager.Lineage {
	var out []alertmanager.Lineage
	for _, l := range ls {
		kind := l.Kind
		if kind == "" {
			kind = config.EndpointAlertmanager
		}
		if slices.Contains(moved[kind], cluster) {
			out = append(out, l)
		}
	}
	return out
}

// heldNouns is what a conversation's lineages read, in a plan's words:
// "alerts", "alerts and logs", "alerts, logs and metrics" (Settings S3b §4).
func heldNouns(ls []alertmanager.Lineage) string {
	held := map[string]bool{}
	for _, l := range ls {
		held[l.Kind] = true
	}
	var nouns []string
	for _, k := range []struct{ kind, noun string }{{"", "alerts"}, {config.EndpointLoki, "logs"}, {config.EndpointMetrics, "metrics"}} {
		if held[k.kind] {
			nouns = append(nouns, k.noun)
		}
	}
	switch len(nouns) {
	case 0:
		return "data"
	case 1:
		return nouns[0]
	}
	return strings.Join(nouns[:len(nouns)-1], ", ") + " and " + nouns[len(nouns)-1]
}
