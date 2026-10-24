package observe

import (
	"context"
	"reflect"
	"sort"
	"time"

	"k8s.io/utils/clock"
)

type rowsReq struct {
	task, part string
	reply      chan rowsReply
}

type rowsReply struct {
	rows Rows
	ok   bool
}

// loop is the only goroutine that evaluates and publishes, so two captures
// can never publish out of order under one part version.
func (o *Observer) loop() {
	var lastPublish time.Time
	pending := false
	var publishAt time.Time
	schedule := func(now time.Time) {
		if pending {
			return
		}
		pending = true
		publishAt = now
		if next := lastPublish.Add(o.cfg.Throttle); !lastPublish.IsZero() && next.After(now) {
			publishAt = next
		}
	}
	for {
		now := o.clk.Now()
		var wakeAt time.Time
		if pending {
			wakeAt = publishAt
		}
		if b, ok := o.nextBoundary(); ok && (wakeAt.IsZero() || b.Before(wakeAt)) {
			wakeAt = b
		}
		var t clock.Timer
		var tc <-chan time.Time
		if !wakeAt.IsZero() {
			t = o.clk.NewTimer(max(wakeAt.Sub(now), 0))
			tc = t.C()
		}
		select {
		case <-o.ctx.Done():
			if t != nil {
				t.Stop()
			}
			o.final()
			return
		case <-o.wake:
			schedule(o.clk.Now())
		case <-tc:
		case req := <-o.reqs:
			if o.serveRows(req) {
				schedule(o.clk.Now())
			}
		case req := <-o.roleReqs:
			if o.serveRoles(req) {
				schedule(o.clk.Now())
			}
		}
		if t != nil {
			t.Stop()
		}
		now = o.clk.Now()
		if o.applyBoundaries(now) {
			schedule(now)
		}
		if pending && !now.Before(publishAt) {
			o.publish(now)
			lastPublish, pending = now, false
		}
	}
}

// nextBoundary is the earliest time-driven change: a part's Next or a live
// snapshot going overdue.
func (o *Observer) nextBoundary() (time.Time, bool) {
	var best time.Time
	found := false
	consider := func(t time.Time) {
		if !found || t.Before(best) {
			best, found = t, true
		}
	}
	for _, d := range o.domains {
		for _, p := range d.parts {
			if p.last != nil && p.last.Next != nil && !p.boundaryFired {
				consider(*p.last.Next)
			}
		}
	}
	o.mu.Lock()
	for _, ss := range o.snaps {
		if g := ss.gen; g != nil && g.fresh.cov == Live && g.fresh.readAt != nil {
			consider(g.fresh.readAt.Add(2 * ss.spec.Cadence))
		}
	}
	o.mu.Unlock()
	return best, found
}

// applyBoundaries marks parts whose time boundary passed, and turns overdue
// snapshots stale. No API call: re-evaluation reads the models.
func (o *Observer) applyBoundaries(now time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	any := false
	for _, d := range o.domains {
		for _, p := range d.parts {
			if p.last != nil && p.last.Next != nil && !p.boundaryFired && !now.Before(*p.last.Next) {
				p.boundaryFired = true
				o.dirty[p.key] = true
				any = true
			}
		}
	}
	for _, ss := range o.snaps {
		if g := ss.gen; g != nil && g.overdue(now) {
			o.markSnapLocked(ss)
			any = true
		}
	}
	return any
}

// publish evaluates every dirty part and emits the summary (new Seq) plus
// rows for subscribed parts whose version advanced or that gained rows.
func (o *Observer) publish(now time.Time) {
	o.mu.Lock()
	keys := o.dirty
	o.dirty = map[partKey]bool{}
	summaryDirty := o.summaryDirty
	o.summaryDirty = false
	o.mu.Unlock()

	changed := false
	start := time.Now() // wall time, not the injected clock: this is cost
	for _, d := range o.domains {
		for _, p := range d.parts {
			if keys[p.key] {
				verChanged, _ := o.evaluate(p, now, false)
				changed = changed || verChanged
			}
		}
	}
	if len(keys) > 0 {
		spent := time.Since(start)
		o.statEvalNanos.Add(int64(spent))
		o.statLastEval.Store(int64(spent))
	}
	// Rows owed to subscribers: any part whose version they have not been
	// sent - including a version a RowsNow bootstrap advanced, which never
	// consumes the broadcast.
	var rowsOut []*partState
	o.mu.Lock()
	for _, d := range o.domains {
		for _, p := range d.parts {
			if len(p.subs) > 0 && p.hasRows && (!p.rowsPub || p.rowsPubVer != p.ver) {
				rowsOut = append(rowsOut, p)
			}
		}
	}
	o.mu.Unlock()
	if !changed && !summaryDirty && len(rowsOut) == 0 {
		return
	}
	if changed || summaryDirty {
		o.mu.Lock()
		o.seq++
		s := o.buildSummaryLocked(now)
		o.latest = &s
		o.mu.Unlock()
		o.statPublishes.Add(1)
		o.cfg.Sink.Summary(s)
	}
	for _, p := range rowsOut {
		o.cfg.Sink.Rows(o.rowsOf(p, p.lastRows, now))
		p.rowsPub, p.rowsPubVer = true, p.ver
	}
}

func (o *Observer) rowsOf(p *partState, rows any, now time.Time) Rows {
	return Rows{Cluster: o.cfg.Cluster, Incarnation: o.incarnation, Task: p.key.task, EvaluatedAt: p.evaluatedAt, CapturedAt: p.capturedAt, Part: *p.last, Rows: rows}
}

// evaluate captures one part atomically, evaluates it and advances its
// version on any part-visible change (spec §4). Loop goroutine only.
func (o *Observer) evaluate(p *partState, now time.Time, forceRows bool) (verChanged bool, rows any) {
	o.mu.Lock()
	subscribed := len(p.subs) > 0
	wantRows := subscribed || forceRows
	c, kinds, absent, cp := o.captureLocked(p, now)
	obs := p.obs
	o.mu.Unlock()

	var res PartResult
	if !absent && p.spec.Evaluate != nil {
		c.Rows = wantRows
		if p.ledger != nil && o.tsink != nil {
			c.RoleCap = o.cfg.RoleCap
		}
		res = p.spec.Evaluate(c)
	}
	covs := make([]Coverage, len(kinds))
	var asOf *time.Time
	for i, k := range kinds {
		covs[i] = k.Coverage
		if k.AsOf != nil && (asOf == nil || k.AsOf.Before(*asOf)) {
			t := *k.AsOf
			asOf = &t
		}
	}
	part := Part{ID: p.spec.ID, Entity: p.spec.Entity, Scope: p.spec.Scope, Classifier: p.spec.Classifier,
		Coverage: rollup(covs), Kinds: kinds, Obs: obs, AsOf: asOf}
	if absent {
		part.Coverage = Absent // every counted source is absent: a live dependency cannot make it observed
	} else {
		part.Counts, part.Native, part.Evidence, part.Top, part.Next = res.Counts, res.Native, res.Evidence, res.Top, res.Next
		part.Alerts = res.Alerts
	}

	prev := p.last
	verChanged = prev == nil || prev.Obs != part.Obs || !samePart(*prev, part) ||
		(subscribed && p.hasRows && !reflect.DeepEqual(p.lastRows, res.Rows))
	if subscribed && !p.hasRows {
		p.rowsPub = false // a new subscription period: its first rows are owed
	}
	if verChanged {
		p.ver++
	}
	part.Ver = p.ver
	p.last = &part
	p.boundaryFired = false
	if verChanged || p.evaluatedAt.IsZero() {
		p.evaluatedAt = c.Now // the capture's own clock read (slice 2b review)
	}
	p.capturedAt = c.Now // every evaluation, changed or not
	if subscribed {
		p.lastRows, p.hasRows = res.Rows, true
	} else {
		p.lastRows, p.hasRows = nil, false
	}
	// Commit and emit together (slice 2b §5.1): this evaluation's batches go
	// out now, before any summary or rows of the cycle.
	o.commitLedger(p, cp, c, res, p.ver)
	return verChanged, res.Rows
}

// samePart compares everything a consumer can see, ignoring the version.
func samePart(a, b Part) bool {
	a.Ver, b.Ver = 0, 0
	return reflect.DeepEqual(a, b)
}

// captureLocked takes one part's objects and coverage under the Observer
// lock: model contents and the part's watermark are read together. A part is
// absent only when every counted source is absent; an absent dependency or a
// missing optional kind leaves the rest of the part observed.
func (o *Observer) captureLocked(p *partState, now time.Time) (Capture, []KindCoverage, bool, captured) {
	// One clock read under the lock is the capture's time: the rules
	// evaluate at it, and its batches and rows are stamped with it - never an
	// earlier cycle time, which a slow evaluation before it would make stale.
	at := o.clk.Now()
	c := Capture{Now: at, Objects: map[string][]any{}, Complete: map[string]bool{}, Current: map[string]bool{}, AsOf: map[string]time.Time{}, Capped: map[string]bool{}}
	cp := captured{at: at, connEpoch: o.connEpoch, connUsable: o.connUsable}
	if p.ledger != nil {
		cp.ids, cp.readAt, cp.truncated = map[string]SourceIdentity{}, map[string]*time.Time{}, map[string]bool{}
	}
	if len(p.spec.Facts) > 0 {
		c.Facts = make(map[string]any, len(p.spec.Facts))
		for _, f := range p.spec.Facts {
			if v, ok := o.facts[f]; ok {
				c.Facts[f] = v
			}
		}
	}
	var kinds []KindCoverage
	add := func(name, role string) {
		kc := KindCoverage{Kind: name, Role: role}
		if ks := o.kinds[name]; ks != nil {
			kc.Scope = ks.spec.Scope
			if ks.gen == nil {
				kc.Coverage, kc.Class, kc.Error = o.kindCoverageLocked(ks)
				// a kind confirmed absent has no objects: a complete read
				c.Complete[name] = kc.Coverage == Absent
				if kc.Coverage == Stale && !ks.probedAt.IsZero() {
					kc.AsOf, kc.LastGoodAt = copyTime(&ks.probedAt), copyTime(&ks.probedAt)
				}
			} else {
				kc.Discovery = ks.probeErr
				f := ks.gen.fresh
				kc.Coverage, kc.Class, kc.Error = f.cov, f.class, f.err
				if f.cov == Stale {
					kc.AsOf, kc.LastGoodAt = copyTime(f.lastGoodAt), copyTime(f.lastGoodAt)
				}
				if f.cov == Live || f.cov == Stale {
					c.Objects[name] = sortedValues(ks.gen.model)
					kc.Objects = len(ks.gen.model)
					kc.Truncated, kc.NotRetained = ks.gen.capped, ks.gen.notRetained
					c.Complete[name] = f.cov == Live && !ks.gen.capped
					c.Capped[name] = ks.gen.capped
					if w, ok := ks.gen.windowLocked(); ok {
						if c.Windows == nil {
							c.Windows = map[string]string{}
						}
						c.Windows[name] = w
					}
					if cp.truncated != nil {
						// a budgeted kind's left-out objects are not deletions:
						// the ledger treats the read as capped (S1a §3)
						cp.truncated[name] = ks.gen.capped
					}
				}
			}
		} else {
			ss := o.snaps[name]
			kc.Scope = ss.spec.Scope
			kc.CadenceSec = int64(ss.spec.Cadence / time.Second)
			if ss.gen == nil {
				kc.Coverage = Checking // not observed by any active domain
			} else {
				f := ss.gen.fresh
				kc.Coverage, kc.Class, kc.Error, kc.Truncated = f.cov, f.class, f.err, f.truncated
				if cp.readAt != nil {
					cp.readAt[name], cp.truncated[name] = copyTime(f.readAt), f.truncated
				}
				switch f.cov {
				case Live:
					kc.AsOf = copyTime(f.readAt)
				case Stale:
					kc.AsOf, kc.LastGoodAt = copyTime(f.lastGoodAt), copyTime(f.lastGoodAt)
				}
				if f.cov == Live || f.cov == Stale {
					c.Objects[name] = append([]any(nil), ss.gen.items...)
					c.Complete[name] = f.cov == Live && !f.truncated
					c.Capped[name] = f.truncated
					kc.Objects = len(ss.gen.items)
				}
			}
		}
		viewed := false // the part's consumer view replaced the kind (C3)
		if ks := o.kinds[name]; ks != nil && len(ks.spec.Consumers) > 0 {
			kc.Consumers = demandVector(ks.demand)
			// the part's own consumer decides what of the shared kind is its
			// evidence (C3): objects, completeness, coverage and the ledger's
			// source identity alike
			switch d, probed := ks.consumerView(p.spec.Consumer); {
			case d == DemandYes:
			case d == DemandNo:
				viewed = true
				delete(c.Objects, name)
				delete(c.Windows, name)
				delete(c.Capped, name)
				c.Complete[name] = true
				kc = KindCoverage{Kind: name, Role: role, Scope: kc.Scope, Coverage: Absent, Consumers: kc.Consumers}
				if cp.truncated != nil {
					delete(cp.truncated, name)
				}
			default:
				viewed = true
				delete(c.Objects, name)
				delete(c.Windows, name)
				delete(c.Capped, name)
				c.Complete[name] = false
				kc = KindCoverage{Kind: name, Role: role, Scope: kc.Scope, Coverage: Checking, Consumers: kc.Consumers}
				if probed {
					kc.Coverage, kc.Class, kc.Error = Unreadable, ClassDemand, "whether "+p.spec.Consumer+" needs this kind is not known: "+ks.probeErr
				}
				if cp.truncated != nil {
					delete(cp.truncated, name)
				}
			}
		}
		c.Current[name] = kc.Coverage == Live
		if kc.Coverage == Stale && kc.LastGoodAt != nil {
			c.AsOf[name] = *kc.LastGoodAt
		}
		kinds = append(kinds, kc)
		if cp.ids != nil {
			if viewed {
				// the part's own absence or gap: a stable identity that
				// borrows nothing of the other consumers' generation, so
				// their relists are never this part's loss (Codex's S3a
				// implementation review, finding 1)
				cp.ids[name] = SourceIdentity{Source: name, Coverage: kc.Coverage}
			} else {
				cp.ids[name] = o.sourceIdentityLocked(name, kc.Coverage)
			}
		}
	}
	for _, n := range p.spec.Counted {
		add(n, "counted")
	}
	for _, n := range p.spec.Deps {
		add(n, "dependency")
	}
	absent := len(p.spec.Counted) > 0
	for _, k := range kinds[:len(p.spec.Counted)] {
		absent = absent && k.Coverage == Absent
	}
	return c, kinds, absent, cp
}

// demandVector is a kind's demand, sorted by consumer.
func demandVector(m map[string]Demand) []ConsumerDemand {
	if len(m) == 0 {
		return nil
	}
	out := make([]ConsumerDemand, 0, len(m))
	for n, d := range m {
		out = append(out, ConsumerDemand{Name: n, Demand: d})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (o *Observer) buildSummaryLocked(now time.Time) Summary {
	s := Summary{Cluster: o.cfg.Cluster, Incarnation: o.incarnation, Seq: o.seq, EvaluatedAt: now, Connection: o.conn}
	if ks := o.kinds[o.cfg.NamespacesKind]; ks != nil && ks.gen != nil && ks.gen.fresh.cov == Live {
		// the count stays exact past a budget: the objects left out are
		// counted (coverage S7 §8)
		n := len(ks.gen.model) + ks.gen.notRetained
		s.Namespaces = &n // unknown (nil) unless current
	}
	for _, d := range o.domains {
		t := Task{ID: d.spec.ID}
		if d.controller != nil {
			c := *d.controller
			t.Controller = &c
		}
		var covs []Coverage
		for _, p := range d.parts {
			part := Part{ID: p.spec.ID, Entity: p.spec.Entity, Scope: p.spec.Scope, Classifier: p.spec.Classifier, Coverage: Checking}
			if p.last != nil {
				part = *p.last
			}
			t.Parts = append(t.Parts, part)
			covs = append(covs, part.Coverage)
			if part.AsOf != nil && (t.AsOf == nil || part.AsOf.Before(*t.AsOf)) {
				a := *part.AsOf
				t.AsOf = &a
			}
		}
		t.Coverage = rollup(covs)
		s.Tasks = append(s.Tasks, t)
	}
	return s
}

// serveRows answers a rows bootstrap from a fresh capture. It reports
// whether the part's version advanced (the summary must follow).
func (o *Observer) serveRows(req rowsReq) bool {
	o.mu.Lock()
	p := o.partLocked(req.task, req.part)
	if p != nil {
		delete(o.dirty, p.key) // this capture covers every mutation so far
	}
	o.mu.Unlock()
	if p == nil {
		req.reply <- rowsReply{}
		return false
	}
	now := o.clk.Now()
	verChanged, rows := o.evaluate(p, now, true)
	req.reply <- rowsReply{rows: o.rowsOf(p, rows, now), ok: true}
	if verChanged {
		o.mu.Lock()
		o.summaryDirty = true
		o.mu.Unlock()
	}
	o.mu.Lock()
	owed := len(p.subs) > 0 && p.hasRows && p.rowsPubVer != p.ver
	o.mu.Unlock()
	return verChanged || owed
}

// RowsNow returns a part's rows and counts from one fresh capture - the
// bootstrap after subscribing (spec §4). ok is false once the Observer ended.
func (o *Observer) RowsNow(task, part string) (Rows, bool) {
	return o.RowsNowCtx(context.Background(), task, part)
}

// RowsNowCtx is RowsNow bounded by ctx: a caller that stops waiting (an
// unmounted lens, a bootstrap deadline) gets ok=false at once.
func (o *Observer) RowsNowCtx(ctx context.Context, task, part string) (Rows, bool) {
	o.mu.Lock()
	started := o.started
	o.mu.Unlock()
	if !started {
		return Rows{}, false
	}
	reply := make(chan rowsReply, 1) // buffered: the loop never blocks on a gone caller
	select {
	case o.reqs <- rowsReq{task: task, part: part, reply: reply}:
	case <-o.ctx.Done():
		return Rows{}, false
	case <-ctx.Done():
		return Rows{}, false
	}
	select {
	case r := <-reply:
		return r.rows, r.ok
	case <-ctx.Done():
		return Rows{}, false
	case <-o.ctx.Done():
		return Rows{}, false
	}
}

// final publishes the terminal summary once and stops every generation.
func (o *Observer) final() {
	o.mu.Lock()
	for _, d := range o.domains {
		if d.active {
			o.deactivateLocked(d)
		}
	}
	if o.cfg.NamespacesKind != "" {
		o.unrefSourceLocked(o.cfg.NamespacesKind)
	}
	o.seq++
	now := o.clk.Now()
	s := Summary{Cluster: o.cfg.Cluster, Incarnation: o.incarnation, Seq: o.seq, EvaluatedAt: now, Connection: o.conn, Final: true}
	o.latest = &s
	o.mu.Unlock()
	// Every batch was emitted at its commit, so nothing is pending: the end
	// batch closes the stream before the Final summary (slice 2b §3).
	if o.tsink != nil {
		o.emit(nil, TransitionBatch{Kind: BatchEnd, At: now})
	}
	o.cfg.Sink.Summary(s)
}

func sortedValues(m map[string]any) []any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]any, len(keys))
	for i, k := range keys {
		out[i] = m[k]
	}
	return out
}

func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}
