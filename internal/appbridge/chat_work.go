package appbridge

import (
	"fmt"
	"sort"
	"time"
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

// AlertWork is the AI work holding alerts read on clusters (Settings S3a
// §5), by identity: each open conversation that recorded an alerts
// lineage there, which ends at its next send once an Observability Apply
// moves the terms its alerts were read under, and its offered plan, which
// is withdrawn then (§4.3).
func (s *ChatService) AlertWork(clusters []string) []PlanWorkDTO {
	in := map[string]bool{}
	for _, c := range clusters {
		in[c] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []PlanWorkDTO{}
	for _, c := range s.convs {
		if c.ended != "" || len(c.lineages) == 0 || !in[c.cluster] {
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
		out = append(out, PlanWorkDTO{ID: "conv:" + c.id, Kind: c.kind, Cluster: c.cluster, Started: started, What: what + " holding alerts read under the old terms: it ends at its next send"})
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
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
