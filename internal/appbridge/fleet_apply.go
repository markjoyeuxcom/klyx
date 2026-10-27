package appbridge

import (
	"errors"
	"strings"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
)

// ApplyClusterPlan applies the fleet plan with digest (Settings S2 §4.5),
// every route the same way: under the fleet Apply lock and the cluster's
// reservation it re-plans, refusing with the new plan when anything moved,
// and checks the interlock - typed is its phrase; it prepares any
// connection outside the commit gate; then it decides under the gate by
// class. A refusal before the write unreserves and changes nothing.
func (s *ConfigService) ApplyClusterPlan(digest, typed string) ApplyResultDTO {
	fp := s.fp
	if fp == nil || s.policy == nil {
		return ApplyResultDTO{Error: "fleet changes are not available"}
	}
	v, ok := fp.cache.get(digest)
	if !ok {
		return ApplyResultDTO{Error: "This plan expired or was already applied: review the changes again."}
	}
	c := v.(fleetCand)
	// a removal deleting keychain records holds the credential lock first:
	// endpoints, then the fleet Apply lock, in that order only (S2 §2.6)
	if c.Kind == planRemove && len(c.Records) > 0 && fp.d.CredLock != nil {
		defer fp.d.CredLock()()
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	tok, err := s.policy.Reserve(c.Cluster)
	if err != nil {
		return ApplyResultDTO{Error: c.Cluster + " is applying another change; try again in a moment"}
	}
	defer s.policy.Unreserve(tok) // last; a removal has forgotten it already
	x := s.planCluster(c)
	switch {
	case x.plan.Digest != digest:
		return ApplyResultDTO{Error: "Something changed since this was planned: review the new plan.", Plan: &x.plan}
	case x.plan.Refused != "":
		return ApplyResultDTO{Error: x.plan.Refused}
	case x.plan.Interlock != nil && strings.TrimSpace(typed) != x.plan.Interlock.Phrase:
		return ApplyResultDTO{Error: "Type \"" + x.plan.Interlock.Phrase + "\" to confirm; nothing was applied."}
	}
	if fp.beforeDecide != nil {
		fp.beforeDecide()
	}
	name, life := c.Cluster, fp.d.Life
	// write runs under the commit gate: the edit or the removal against the
	// plan's file version - or, writing nothing, that version checked all
	// the same, so a file changed since the re-plan is never adopted
	// (Codex's review of S2b, R1)
	write := func() error {
		switch {
		case x.edit != nil:
			_, err := config.EditClusterAt(s.path, x.version, name, *x.edit)
			return err
		case x.class == classRemoval && x.plan.Writes:
			_, err := config.RemoveClusterAt(s.path, x.version, name)
			return err
		}
		return s.fileIs(x.version)
	}
	note := ""
	switch x.class {
	case classPlacement:
		err = life.Decide(name, x.bound, func() error {
			if err := write(); err != nil {
				return err
			}
			s.swapRunning(x.cand)
			fp.editedNow(life, name, x.terms)
			return nil
		})
	case classProtection:
		err = life.Decide(name, x.bound, func() error {
			if err := write(); err != nil {
				return err
			}
			s.swapRunning(x.cand)
			if _, err := s.policy.Advance(tok, x.cand.IsProtected()); err != nil {
				return err
			}
			fp.editedNow(life, name, x.terms)
			return nil
		})
		if err == nil && fp.d.EndEpochWork != nil {
			fp.d.EndEpochWork(name, name+"'s protection changed")
		}
	case classReconnect:
		p, perr := fp.d.Prepare(x.cand)
		if perr != nil {
			return ApplyResultDTO{Error: "Klyx cannot connect with these terms, so nothing was applied: " + config.DisplayError(perr)}
		}
		err = life.Switch(fp.d.Ctx, tok, x.bound, p, "saved, not connected yet: connecting with context "+x.cand.Context, name+"'s connection settings changed", x.cand.IsProtected(), func() error {
			if err := write(); err != nil {
				return err
			}
			s.swapRunning(x.cand)
			fp.attempt(name, p.ID(), x.terms)
			return nil
		})
	case classRemoval:
		err = life.Unseat(x.bound, name, name+" was removed", write)
		if err == nil {
			note = fp.deleteRecords(name, x.plan.Records)
		}
	case classAddition:
		p, perr := fp.d.Prepare(x.cand)
		if perr != nil {
			return ApplyResultDTO{Error: "Klyx cannot connect with these terms, so nothing was applied: " + config.DisplayError(perr)}
		}
		err = life.Decide(name, Bound{}, func() error {
			if err := s.fileIs(x.version); err != nil {
				return err
			}
			s.appendRunning(x.cand)
			s.policy.Seed(name, x.cand.IsProtected()) // over its placeholder: still reserved
			fp.attempt(name, p.ID(), x.terms)
			return nil
		})
		if err != nil {
			p.Discard()
		} else if ierr := fp.d.Install(fp.d.Ctx, p); ierr != nil {
			p.Discard()
			s.dropRunning(name)
			s.policy.Forget(name)
			err = ierr
		}
	}
	switch {
	case errors.Is(err, config.ErrFileChanged), errors.Is(err, config.ErrFleetChangedOnDisk):
		re := s.planCluster(c)
		return ApplyResultDTO{Error: "fleet.yaml changed while this was applied, so nothing was written: review the new plan.", Plan: &re.plan}
	case errors.Is(err, ErrMoved):
		re := s.planCluster(c)
		return ApplyResultDTO{Error: ErrMoved.Error(), Plan: &re.plan}
	case err != nil:
		return ApplyResultDTO{Error: config.DisplayError(err)}
	}
	fp.cache.consume(digest)
	if fp.d.Changed != nil {
		fp.d.Changed()
	}
	return ApplyResultDTO{OK: true, Note: note}
}

// attempt records a new connection's raw terms under the gate: they are
// the cluster's current terms, and become its last connected ones when
// that connection's usability is published.
func (fp *fleetPlans) attempt(cluster string, id fleet.ConnID, terms s2Fields) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.terms[id] = attemptTerms{cluster: cluster, terms: terms}
	fp.current[cluster] = terms
}

// editedNow records a change committed on the cluster, under its gate: on
// its connection when it has one; a cluster whose first build failed has
// none, and its current terms alone say the change - what Retry builds
// (Codex's second re-review of S2b, R2-1).
func (fp *fleetPlans) editedNow(life *Lifecycle, cluster string, terms s2Fields) {
	if c, ok := life.reg.Conn(cluster); ok {
		if id, ok := connID(c); ok {
			fp.edited(cluster, id, terms)
			return
		}
	}
	fp.editedUnbuilt(cluster, terms)
}

// fileIs checks fleet.yaml is still the version a plan was made from.
func (s *ConfigService) fileIs(version string) error {
	v, err := config.FileVersion(s.path)
	if err != nil {
		return err
	}
	if v != version {
		return config.ErrFileChanged
	}
	return nil
}

// deleteRecords deletes a removed cluster's chosen keychain records, each
// its own choice; a deletion that fails is said, record by record, and the
// cluster stays removed (S2 §4.4).
func (fp *fleetPlans) deleteRecords(cluster string, recs []PlanRecordDTO) string {
	var failed []string
	for _, r := range recs {
		if !r.Chosen || fp.d.Delete == nil {
			continue
		}
		if err := fp.d.Delete(cluster, r.Kind); err != nil {
			failed = append(failed, "the keychain still holds the "+r.Kind+" credential ("+config.DisplayError(err)+")")
		}
	}
	if len(failed) == 0 {
		return ""
	}
	return cluster + " was removed, but " + strings.Join(failed, "; ")
}

// swapRunning replaces the running entry of cc's name with cc (S2 §2.6).
func (s *ConfigService) swapRunning(cc config.ClusterConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.running {
		if s.running[i].Name == cc.Name {
			s.running[i] = cc
			s.runningRev++
			return
		}
	}
}

// appendRunning adds cc at the end of the running fleet.
func (s *ConfigService) appendRunning(cc config.ClusterConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = append(s.running, cc)
	s.runningRev++
}
