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

	changed := false
	for _, d := range o.domains {
		for _, p := range d.parts {
			if keys[p.key] {
				verChanged, _ := o.evaluate(p, now, false)
				changed = changed || verChanged
			}
		}
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
		o.cfg.Sink.Summary(s)
	}
	for _, p := range rowsOut {
		o.cfg.Sink.Rows(o.rowsOf(p, p.lastRows, now))
		p.rowsPub, p.rowsPubVer = true, p.ver
	}
}

func (o *Observer) rowsOf(p *partState, rows any, now time.Time) Rows {
	return Rows{Cluster: o.cfg.Cluster, Incarnation: o.incarnation, Task: p.key.task, EvaluatedAt: p.evaluatedAt, Part: *p.last, Rows: rows}
}

// evaluate captures one part atomically, evaluates it and advances its
// version on any part-visible change (spec §4). Loop goroutine only.
func (o *Observer) evaluate(p *partState, now time.Time, forceRows bool) (verChanged bool, rows any) {
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
	if absent {
		part.Coverage = Absent // every counted source is absent: a live dependency cannot make it observed
	} else {
		part.Counts, part.Native, part.Evidence, part.Top, part.Next = res.Counts, res.Native, res.Evidence, res.Top, res.Next
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
		p.evaluatedAt = now
	}
	if subscribed {
		p.lastRows, p.hasRows = res.Rows, true
	} else {
		p.lastRows, p.hasRows = nil, false
	}
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
func (o *Observer) captureLocked(p *partState, now time.Time) (Capture, []KindCoverage, bool) {
	c := Capture{Now: now, Objects: map[string][]any{}}
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
		}
		kinds = append(kinds, kc)
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
	return c, kinds, absent
}

func (o *Observer) buildSummaryLocked(now time.Time) Summary {
	s := Summary{Cluster: o.cfg.Cluster, Incarnation: o.incarnation, Seq: o.seq, EvaluatedAt: now, Connection: o.conn}
	if ks := o.kinds[o.cfg.NamespacesKind]; ks != nil && ks.gen != nil && ks.gen.fresh.cov == Live {
		n := len(ks.gen.model)
		s.Namespaces = &n // unknown (nil) unless complete and current
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
		if d.active {
			o.deactivateLocked(d)
		}
	}
	if o.cfg.NamespacesKind != "" {
		o.unrefSourceLocked(o.cfg.NamespacesKind)
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
