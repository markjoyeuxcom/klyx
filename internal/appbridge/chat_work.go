package appbridge

import (
	"fmt"
	"sort"
	"time"
)

// aiWork is the AI work an Apply of the AI settings affects (Settings slice
// F §5), by identity: each open conversation under the running revision,
// its question being answered, its offered plan, and a running step's
// verification end. A running step's confirmed request continues - the AI
// revision does not fence it (A3 §4.4) - and is named apart, by its phase
// (F2a review, R4). A conversation from an earlier revision is already
// ended for new turns, so it is not named again.
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
		if p := c.stepRunning; p != nil && p.running > 0 && p.running <= len(p.runs) {
			n := p.running
			step := fmt.Sprintf("step %d of the action plan on %s", n, c.cluster)
			if p.runs[n-1].request == "" {
				// its write is not yet recorded: it goes ahead
				continues = append(continues, PlanWorkDTO{ID: fmt.Sprintf("step:%s:%d:request", c.id, n), Kind: "step", Cluster: c.cluster, What: step + ": its confirmed request goes ahead, and may still write"})
			}
			out = append(out, PlanWorkDTO{ID: fmt.Sprintf("step:%s:%d:verify", c.id, n), Kind: "step", Cluster: c.cluster, What: "the verification of " + step + ": it ends unverified"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	sort.Slice(continues, func(i, j int) bool { return continues[i].ID < continues[j].ID })
	return aiWorkList{ends: out, continues: continues}
}

func offersAStep(p *actionPlan) bool {
	for _, st := range p.dto.Steps {
		if st.State == stepOffered {
			return true
		}
	}
	return false
}
