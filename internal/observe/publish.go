package observe

import (
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

	var rowsOut []*partState
	changed := false
	for _, d := range o.domains {
		for _, p := range d.parts {
			if !keys[p.key] {
				continue
			}
			verChanged, rowsNew, _ := o.evaluate(p, now, false)
			changed = changed || verChanged
			if (verChanged || rowsNew) && p.hasRows {
				rowsOut = append(rowsOut, p)
			}
		}
	}
	if !changed && !summaryDirty && len(rowsOut) == 0 {
		return
	}
	if changed || summaryDirty {
		o.mu.Lock()
		o.seq++
		s := o.buildSummaryLocked(now)
		o.latest = &s
		o.mu.Unlock()
		o.cfg.Sink.Summary(s)
	}
	for _, p := range rowsOut {
		o.cfg.Sink.Rows(o.rowsOf(p, p.lastRows, now))
	}
}

func (o *Observer) rowsOf(p *partState, rows any, now time.Time) Rows {
	return Rows{Cluster: o.cfg.Cluster, Incarnation: o.incarnation, Task: p.key.task, EvaluatedAt: p.evaluatedAt, Part: *p.last, Rows: rows}
}

// evaluate captures one part atomically, evaluates it and advances its
// version on any part-visible change (spec §4). Loop goroutine only.
func (o *Observer) evaluate(p *partState, now time.Time, forceRows bool) (verChanged, rowsNew bool, rows any) {
	o.mu.Lock()
	subscribed := len(p.subs) > 0
	wantRows := subscribed || forceRows
	c, kinds, absent := o.captureLocked(p, now)
	obs := p.obs
	o.mu.Unlock()

	var res PartResult
	if !absent && p.spec.Evaluate != nil {
		c.Rows = wantRows
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
	if !absent {
		part.Counts, part.Native, part.Evidence, part.Top, part.Next = res.Counts, res.Native, res.Evidence, res.Top, res.Next
	}

	prev := p.last
	verChanged = prev == nil || prev.Obs != part.Obs || !samePart(*prev, part) ||
		(subscribed && p.hasRows && !reflect.DeepEqual(p.lastRows, res.Rows))
	rowsNew = subscribed && !p.hasRows
	if verChanged {
		p.ver++
	}
	part.Ver = p.ver
	p.last = &part
	p.boundaryFired = false
	if verChanged || p.evaluatedAt.IsZero() {
		p.evaluatedAt = now
	}
	if subscribed {
		p.lastRows, p.hasRows = res.Rows, true
	} else {
		p.lastRows, p.hasRows = nil, false
	}
	return verChanged, rowsNew, res.Rows
}

// samePart compares everything a consumer can see, ignoring the version.
func samePart(a, b Part) bool {
	a.Ver, b.Ver = 0, 0
	return reflect.DeepEqual(a, b)
}

// captureLocked takes one part's objects and coverage under the Observer
// lock: model contents and the part's watermark are read together.
func (o *Observer) captureLocked(p *partState, now time.Time) (Capture, []KindCoverage, bool) {
	d := p.dom
	g := d.gen
	absent := g != nil && g.probed && !g.served
	c := Capture{Now: now, Objects: map[string][]any{}}
	var kinds []KindCoverage
	add := func(name, role string) {
		kc := KindCoverage{Kind: name, Role: role}
		switch {
		case o.kinds[name] != nil:
			ks := o.kinds[name]
			kc.Scope = ks.spec.Scope
			if absent || g == nil || !g.acquired || ks.gen == nil {
				kc.Coverage, kc.Class, kc.Error = o.domainCoverageLocked(d)
				break
			}
			f := ks.gen.fresh
			kc.Coverage, kc.Class, kc.Error = f.cov, f.class, f.err
			if f.cov == Stale {
				kc.AsOf, kc.LastGoodAt = copyTime(f.lastGoodAt), copyTime(f.lastGoodAt)
			}
			if f.cov != Unreadable && f.cov != Checking {
				c.Objects[name] = sortedValues(ks.gen.model)
				kc.Objects = len(ks.gen.model)
			}
		case o.snaps[name] != nil:
			ss := o.snaps[name]
			kc.Scope = ss.spec.Scope
			kc.CadenceSec = int64(ss.spec.Cadence / time.Second)
			if absent || g == nil || !g.acquired || ss.gen == nil {
				kc.Coverage, kc.Class, kc.Error = o.domainCoverageLocked(d)
				break
			}
			f := ss.gen.fresh
			kc.Coverage, kc.Class, kc.Error, kc.Truncated = f.cov, f.class, f.err, f.truncated
			switch f.cov {
			case Live:
				kc.AsOf = copyTime(f.readAt)
			case Stale:
				kc.AsOf, kc.LastGoodAt = copyTime(f.lastGoodAt), copyTime(f.lastGoodAt)
			}
			if f.cov == Live || f.cov == Stale {
				c.Objects[name] = append([]any(nil), ss.gen.items...)
				kc.Objects = len(ss.gen.items)
			}
		}
		kinds = append(kinds, kc)
	}
	for _, n := range p.spec.Counted {
		add(n, "counted")
	}
	for _, n := range p.spec.Deps {
		add(n, "dependency")
	}
	return c, kinds, absent
}

// domainCoverageLocked is a kind's coverage when its domain decides it: not
// installed, still probing, or a probe that never succeeded.
func (o *Observer) domainCoverageLocked(d *domainState) (Coverage, string, string) {
	g := d.gen
	switch {
	case g == nil:
		return Checking, "", ""
	case g.probed && !g.served:
		return Absent, "", ""
	case !g.probed && g.probeErr != "":
		return Unreadable, ClassDiscovery, g.probeErr
	default:
		return Checking, "", ""
	}
}

func (o *Observer) buildSummaryLocked(now time.Time) Summary {
	s := Summary{Cluster: o.cfg.Cluster, Incarnation: o.incarnation, Seq: o.seq, EvaluatedAt: now, Connection: o.conn}
	for _, d := range o.domains {
		t := Task{ID: d.spec.ID}
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
	verChanged, _, rows := o.evaluate(p, now, true)
	req.reply <- rowsReply{rows: o.rowsOf(p, rows, now), ok: true}
	if verChanged {
		o.mu.Lock()
		o.summaryDirty = true
		o.mu.Unlock()
	}
	return verChanged
}

// RowsNow returns a part's rows and counts from one fresh capture - the
// bootstrap after subscribing (spec §4). ok is false once the Observer ended.
func (o *Observer) RowsNow(task, part string) (Rows, bool) {
	o.mu.Lock()
	started := o.started
	o.mu.Unlock()
	if !started {
		return Rows{}, false
	}
	reply := make(chan rowsReply, 1)
	select {
	case o.reqs <- rowsReq{task: task, part: part, reply: reply}:
	case <-o.ctx.Done():
		return Rows{}, false
	}
	r := <-reply
	return r.rows, r.ok
}

// final publishes the terminal summary once and stops every generation.
func (o *Observer) final() {
	o.mu.Lock()
	for _, d := range o.domains {
		if d.gen != nil {
			o.stopDomainLocked(d)
		}
	}
	o.seq++
	s := Summary{Cluster: o.cfg.Cluster, Incarnation: o.incarnation, Seq: o.seq, EvaluatedAt: o.clk.Now(), Connection: o.conn, Final: true}
	o.latest = &s
	o.mu.Unlock()
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
