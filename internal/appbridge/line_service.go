package appbridge

import (
	"context"
	"github.com/moomora/klyx/internal/urlscrub"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/clustermesh"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/line"
	"github.com/moomora/klyx/internal/observe"
)

// LineService (The Line slice 3, docs/superpowers/specs/2026-10-15-klyx-the-
// line-slice3-line-model.md) builds the line from rows the Observers already
// hold - it never watches or lists anything itself - and pushes a versioned
// LineDTO. One worker goroutine owns every piece of state; the health sink
// tap only records, bootstrap reads run as cancellable jobs, and the mesh is
// a sampled feed.

// LineDeps wires the service.
type LineDeps struct {
	Emitter   Emitter
	Sink      *HealthSink
	Clusters  func() []config.ClusterConfig // the live fleet, in fleet.yaml order
	Snapshots func() []fleet.Snapshot       // connection states, for clusters with no Observer
	Observer  func(cluster string) *observe.Observer
	Mesh      *MeshService // nil: no mesh
	Override  *line.Key    // fleet.yaml line.source
	// OnLine receives every LineDTO with the clusters' Git source URLs it
	// was built from (GitLineSink); it must not block the worker.
	OnLine func(LineDTO, []LineGitSource)
	Now    func() time.Time

	// Cadences; zero takes the note's defaults.
	ScanEvery        time.Duration // 5s
	MeshEvery        time.Duration // 60s
	MeshTimeout      time.Duration // 15s
	Coalesce         time.Duration // 250ms
	BootstrapTimeout time.Duration // 10s
	MaxBootstraps    int           // 8
}

// lineOwner holds the line's domain leases and row subscriptions.
var lineOwner = observe.Owner{Kind: "line", ID: "line"}

type lineAttachment struct {
	id       uint64
	obs      *observe.Observer
	inc      uint64
	ctx      context.Context
	cancel   context.CancelFunc
	releases []func()
	inflight map[string]bool // part key -> a bootstrap job is running
}

// stationPartition is a source partition's continuity with the part
// version that set it: a recovery's, a loss's, or the bootstrap rows'.
type stationPartition struct {
	state string
	ver   uint64
}

type lineStation struct {
	cfg    config.ClusterConfig
	order  int
	att    *lineAttachment
	inc    uint64
	ended  bool // Final: terminal for inc
	usable bool // the connection word allows observation
	conn   string
	rows   map[string]observe.Rows // per part key, current incarnation
	booted map[string]bool
	parts  map[partitionRef]*stationPartition
	// History breaks since the last commit (note §5): allGap breaks every
	// predicate partition (connection, Final, a new incarnation); gapKinds
	// are the kinds whose continuity broke (a partition loss, overwritten
	// rows), applied only to the partitions whose inputs need them.
	allGap   bool
	gapKinds map[string]bool
	// pending are parts whose rows were overwritten unseen and whose newer
	// rows this drain has not installed yet: their retained rows predate the
	// gap, so the partitions reading them stay unread until then.
	pending    map[string]bool
	histDirty  bool // an accepted change awaits its history commit
	lastOKAt   *time.Time
	snapState  fleet.ConnState
	snapReason string
}

type bootResult struct {
	att     uint64
	cluster string
	part    partRef
	rows    observe.Rows
	ok      bool
}

type meshResult struct {
	graph clustermesh.Graph
	err   string
}

type meshState struct {
	graph    *clustermesh.Graph
	readAt   *time.Time
	err      string
	inflight bool
}

// LineService is the line's single acquisition point.
type LineService struct {
	d        LineDeps
	tap      *lineTap
	ctx      context.Context
	results  chan any
	requests chan func()
	sem      chan struct{}

	dtoMu sync.RWMutex
	dto   LineDTO

	// Worker-owned.
	stations map[string]*lineStation
	selector *line.Key
	version  uint64
	nextAtt  uint64
	mesh     meshState
	hist     *lineHistory
	// The reference of the last build: held is evaluated against it on every
	// accepted change.
	lastKey *line.Key
	lastR   string
	dirty   bool
	lastRun time.Time
}

// NewLineService registers the tap, then starts the worker, which scans the
// current fleet at once (listeners first, then the scan: note §2.2). It runs
// until ctx ends.
func NewLineService(ctx context.Context, d LineDeps) *LineService {
	if d.Now == nil {
		d.Now = time.Now
	}
	def := func(v *time.Duration, x time.Duration) {
		if *v <= 0 {
			*v = x
		}
	}
	def(&d.ScanEvery, 5*time.Second)
	def(&d.MeshEvery, 60*time.Second)
	def(&d.MeshTimeout, 15*time.Second)
	def(&d.Coalesce, 250*time.Millisecond)
	def(&d.BootstrapTimeout, 10*time.Second)
	if d.MaxBootstraps <= 0 {
		d.MaxBootstraps = 8
	}
	s := &LineService{d: d, tap: newLineTap(), ctx: ctx, results: make(chan any, 64), requests: make(chan func(), 16),
		sem: make(chan struct{}, d.MaxBootstraps), stations: map[string]*lineStation{}}
	s.hist = newLineHistory(d.Sink.ObservationLog())
	s.dto = LineDTO{Keys: []LineKeyCountDTO{}, Tracks: []LineTrackDTO{}, Stations: []LineStationDTO{}, Siding: []LineStationDTO{}}
	d.Sink.AddListener(s.tap)
	go s.run()
	return s
}

// Line returns the current line, for bootstrapping (subscribe to LineEvent,
// then read).
func (s *LineService) Line() LineDTO {
	s.dtoMu.RLock()
	defer s.dtoMu.RUnlock()
	return s.dto
}

// SetSource switches the board's comparison key for this session (the
// parent's selector); it is never persisted.
func (s *LineService) SetSource(k LineKeyDTO) {
	key := line.Key{Kind: k.Kind, Repo: k.Repo, RefKind: k.RefKind, RefName: k.RefName}
	s.request(func() { s.selector = &key; s.dirty = true })
}

// ClearSource returns the board to the default key.
func (s *LineService) ClearSource() { s.request(func() { s.selector = nil; s.dirty = true }) }

// Rescan asks the worker to reconcile the stations with the fleet now (a
// config change, note §2.2); the worker also rescans every ScanEvery.
func (s *LineService) Rescan() { s.request(s.scan) }

func (s *LineService) request(f func()) {
	select {
	case s.requests <- f:
	case <-s.ctx.Done():
	}
}

func (s *LineService) run() {
	scan := time.NewTicker(s.d.ScanEvery)
	defer scan.Stop()
	meshT := time.NewTicker(s.d.MeshEvery)
	defer meshT.Stop()
	s.scan()
	s.sampleMesh()
	var timer *time.Timer
	var fire <-chan time.Time
	for {
		select {
		case <-s.ctx.Done():
			for _, st := range s.stations {
				s.detach(st)
			}
			if timer != nil {
				timer.Stop()
			}
			return
		case <-s.tap.wake:
			s.applyDrain()
		case <-scan.C:
			s.scan()
		case <-meshT.C:
			s.sampleMesh()
		case r := <-s.results:
			s.applyResult(r)
		case f := <-s.requests:
			f()
		case <-fire:
			fire, timer = nil, nil
			s.recompute()
		}
		if s.dirty && fire == nil {
			wait := s.d.Coalesce - time.Since(s.lastRun)
			if wait <= 0 {
				s.recompute()
			} else {
				timer = time.NewTimer(wait)
				fire = timer.C
			}
		}
	}
}

// ---- reconciliation ----------------------------------------------------------

// scan reconciles the stations with the live fleet and its Observers.
func (s *LineService) scan() {
	snaps := map[string]fleet.Snapshot{}
	if s.d.Snapshots != nil {
		for _, sn := range s.d.Snapshots() {
			snaps[sn.Name] = sn
		}
	}
	seen := map[string]bool{}
	for i, cc := range s.d.Clusters() {
		seen[cc.Name] = true
		st := s.stations[cc.Name]
		if st == nil {
			st = &lineStation{rows: map[string]observe.Rows{}, booted: map[string]bool{}, parts: map[partitionRef]*stationPartition{}}
			s.stations[cc.Name] = st
		}
		st.cfg, st.order = cc, i
		if sn, ok := snaps[cc.Name]; ok {
			st.snapState, st.snapReason = sn.State, sn.Reason
		}
		obs := s.d.Observer(cc.Name)
		switch {
		case obs == nil:
			s.detach(st)
		case st.ended && obs.Incarnation() == st.inc:
			// Final was terminal for this incarnation: wait for the next.
		case st.att == nil || st.att.obs != obs:
			s.detach(st)
			s.attach(cc.Name, st, obs)
		default:
			s.retryBootstraps(cc.Name, st)
		}
	}
	for name, st := range s.stations {
		if !seen[name] {
			s.detach(st)
			delete(s.stations, name)
		}
	}
	s.dirty = true
}

func (s *LineService) attach(cluster string, st *lineStation, obs *observe.Observer) {
	inc := obs.Incarnation()
	if inc != st.inc {
		// A new incarnation drops the station's facts (parent §3.6).
		st.inc, st.ended, st.usable, st.conn = inc, false, true, ""
		st.rows, st.booted = map[string]observe.Rows{}, map[string]bool{}
		st.parts = map[partitionRef]*stationPartition{}
		st.lastOKAt, st.allGap, st.gapKinds, st.histDirty = nil, true, map[string]bool{}, true
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.nextAtt++
	att := &lineAttachment{id: s.nextAtt, obs: obs, inc: inc, ctx: ctx, cancel: cancel, inflight: map[string]bool{}}
	att.releases = append(att.releases, obs.Acquire(healthsrc.TaskFlux, lineOwner), obs.Acquire(healthsrc.TaskArgo, lineOwner))
	for _, p := range lineParts {
		att.releases = append(att.releases, obs.SubscribeRows(p.task, p.part, lineOwner))
	}
	// The rule history (slice 7b) rides the same attachment: the route rows
	// flow on every station's Observer for as long as the station is attached.
	att.releases = append(att.releases, obs.Acquire(healthsrc.TaskGateway, routesOwner),
		obs.SubscribeRows(healthsrc.TaskGateway, "routes", routesOwner))
	st.att = att
	s.retryBootstraps(cluster, st)
}

// detach cancels an attachment: its jobs end at once, its subscriptions and
// leases are released.
func (s *LineService) detach(st *lineStation) {
	if st.att == nil {
		return
	}
	st.att.cancel()
	for _, r := range st.att.releases {
		r()
	}
	st.att = nil
}

// retryBootstraps starts a bootstrap job for every part not yet booted and
// not already in flight.
func (s *LineService) retryBootstraps(cluster string, st *lineStation) {
	if st.att == nil || st.ended {
		return
	}
	for _, p := range lineParts {
		if st.booted[p.key()] || st.att.inflight[p.key()] {
			continue
		}
		st.att.inflight[p.key()] = true
		s.startBootstrap(st.att, cluster, p)
	}
}

// startBootstrap runs one part's RowsNowCtx as a job (note §2.2): under the
// attachment's context and a deadline, at most MaxBootstraps at once, its
// result delivered to the worker tagged with the attachment.
func (s *LineService) startBootstrap(att *lineAttachment, cluster string, p partRef) {
	go func() {
		select {
		case s.sem <- struct{}{}:
		case <-att.ctx.Done():
			return
		}
		defer func() { <-s.sem }()
		ctx, cancel := context.WithTimeout(att.ctx, s.d.BootstrapTimeout)
		rows, ok := att.obs.RowsNowCtx(ctx, p.task, p.part)
		cancel()
		select {
		case s.results <- bootResult{att: att.id, cluster: cluster, part: p, rows: rows, ok: ok}:
		case <-s.ctx.Done():
		}
	}()
}

func (s *LineService) applyResult(r any) {
	switch r := r.(type) {
	case bootResult:
		st := s.stations[r.cluster]
		if st == nil || st.att == nil || st.att.id != r.att {
			return // a cancelled or replaced attachment's result
		}
		delete(st.att.inflight, r.part.key())
		if !r.ok || r.rows.Incarnation != st.inc || st.ended {
			return // retried on the next scan
		}
		applied := s.applyRows(st, r.part.key(), r.rows)
		s.seed(r.cluster, st, r.rows)
		s.dirty = true
		if applied {
			s.commitStation(r.cluster, st, &rowTrigger{part: r.part.key(), at: r.rows.EvaluatedAt})
		} else {
			st.histDirty = true // seeding may have changed continuity
		}
		s.commitDirty()
	case meshResult:
		s.applyMesh(r)
	}
}

// applyDrain applies everything the tap recorded since the last drain.
func (s *LineService) applyDrain() {
	for cluster, d := range s.tap.drain() {
		st := s.stations[cluster]
		if st == nil {
			continue
		}
		// Only what the line reads makes it recompute: summaries arrive on
		// every publish of any part.
		if sum := d.summary; sum != nil {
			switch {
			case sum.Incarnation > st.inc:
				s.scan() // a newer Observer: attach it
			case sum.Incarnation == st.inc:
				if usable := lineConnectionUsable(sum.Connection); usable != st.usable {
					st.usable = usable
					if !usable {
						st.allGap = true
					}
					st.histDirty, s.dirty = true, true
				}
				if sum.Connection != st.conn {
					st.conn = sum.Connection
					s.dirty = true
				}
				if sum.Final && !st.ended {
					st.ended, st.allGap = true, true
					s.detach(st)
					st.histDirty, s.dirty = true, true
				}
			}
		}
		if st.ended {
			continue
		}
		// Partition states and every gap first: a gap or overwritten rows
		// may predate any row set in this drain, so no commit below may
		// claim continuity across them.
		for pr, p := range d.partitions {
			if p.inc != st.inc {
				continue
			}
			if p.state != partitionUnknown {
				if cur := st.parts[pr]; cur == nil || cur.state != p.state || cur.ver != p.ver {
					st.parts[pr] = &stationPartition{state: p.state, ver: p.ver}
					st.histDirty, s.dirty = true, true
				}
			}
			if p.gap {
				st.gapKind(pr.source)
				st.histDirty, s.dirty = true, true
			}
		}
		var keys []string
		for k, r := range d.rows {
			if r.Incarnation != st.inc {
				continue
			}
			keys = append(keys, k)
			if d.overwrite[k] {
				for _, kind := range partKinds[k] {
					st.gapKind(kind) // a discarded row set may have shown a flip (note §5)
				}
				if st.pending == nil {
					st.pending = map[string]bool{}
				}
				st.pending[k] = true
				st.histDirty, s.dirty = true, true
			}
		}
		// Each accepted row set is installed and committed on its own, in
		// capture order: distinct parts published inside one drain cannot
		// hide the composite state between them.
		sort.Slice(keys, func(i, j int) bool {
			a, b := d.rows[keys[i]].EvaluatedAt, d.rows[keys[j]].EvaluatedAt
			if !a.Equal(b) {
				return a.Before(b)
			}
			return keys[i] < keys[j]
		})
		for _, k := range keys {
			r := d.rows[k]
			applied := s.applyRows(st, k, r)
			delete(st.pending, k)
			if applied {
				s.dirty = true
				s.commitStation(cluster, st, &rowTrigger{part: k, at: r.EvaluatedAt})
			}
		}
		// A part whose newer rows were not accepted (the station already
		// holds newer ones) leaves its gap for the next commit to break.
		if len(st.pending) > 0 {
			st.pending, st.histDirty = nil, true
		}
	}
	s.commitDirty()
}

// partKinds are the counted kinds of each line part: overwritten rows of a
// part break the continuity of all of them.
var partKinds = map[string][]string{
	partRef{healthsrc.TaskFlux, "reconciliations"}.key(): {"kustomizations", "helmreleases"},
	partRef{healthsrc.TaskFlux, "sources"}.key():         {"gitrepositories", "helmrepositories", "ocirepositories", "buckets", "helmcharts"},
	partRef{healthsrc.TaskArgo, "applications"}.key():    {"applications"},
}

func (st *lineStation) gapKind(kind string) {
	if st.gapKinds == nil {
		st.gapKinds = map[string]bool{}
	}
	st.gapKinds[kind] = true
}

// applyRows keeps a part's rows when they are newer than the ones held, and
// reports whether it did.
func (s *LineService) applyRows(st *lineStation, key string, r observe.Rows) bool {
	if held, ok := st.rows[key]; ok && held.Part.Ver >= r.Part.Ver {
		return false
	}
	st.rows[key] = r
	st.booted[key] = true
	return true
}

// seed sets each source partition's continuity at bootstrap: losses before
// the tap was registered are not replayed (note §2.3). The bootstrap's
// per-kind coverage, then the observation log's fenced snapshot, fill only
// what the tap has not set since.
func (s *LineService) seed(cluster string, st *lineStation, r observe.Rows) {
	for _, kc := range r.Part.Kinds {
		if kc.Role != "counted" {
			continue
		}
		pr := partitionRef{r.Task, r.Part.ID, kc.Kind}
		if st.parts[pr] != nil {
			continue
		}
		switch kc.Coverage {
		case observe.Live:
			st.parts[pr] = &stationPartition{state: partitionAvailable, ver: r.Part.Ver}
		case observe.Absent:
			st.parts[pr] = &stationPartition{state: partitionAbsent, ver: r.Part.Ver}
		default:
			st.parts[pr] = &stationPartition{state: partitionLost, ver: r.Part.Ver}
		}
	}
	inc, states, ok := s.d.Sink.ObservationLog().PartitionSnapshot(cluster)
	if !ok || inc != st.inc {
		return
	}
	for _, ps := range states {
		pr := partitionRef{ps.Task, ps.Part, ps.Source}
		if pr.task != r.Task || pr.part != r.Part.ID {
			continue
		}
		cur := st.parts[pr]
		if cur != nil && cur.state == partitionAvailable && cur.ver > ps.PartVer {
			continue // the tap saw a later recovery
		}
		switch {
		case ps.Live:
			// A newer recovery than the bootstrap's own rows: keep its
			// version, so rows that precede it stay checking.
			if cur == nil || cur.state != partitionAvailable || ps.PartVer > cur.ver {
				st.parts[pr] = &stationPartition{state: partitionAvailable, ver: ps.PartVer}
			}
		case ps.Loss != nil && ps.Loss.Reason == observe.LossAbsent:
			st.parts[pr] = &stationPartition{state: partitionAbsent, ver: ps.PartVer}
		default:
			st.parts[pr] = &stationPartition{state: partitionLost, ver: ps.PartVer}
		}
	}
}

func lineConnectionUsable(word string) bool {
	return word == "" || word == "connected" || strings.HasPrefix(word, "connected · ")
}

// ---- the mesh feed (note §6) -------------------------------------------------------

func (s *LineService) sampleMesh() {
	if s.d.Mesh == nil || s.mesh.inflight {
		return
	}
	s.mesh.inflight = true
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, s.d.MeshTimeout)
		defer cancel()
		g := clustermesh.BuildGraph(s.d.Mesh.listMembers(ctx))
		r := meshResult{graph: g}
		if err := ctx.Err(); err != nil {
			r.err = "mesh read did not finish: " + err.Error()
		}
		select {
		case s.results <- r:
		case <-s.ctx.Done():
		}
	}()
}

// applyMesh records a sample. Every outcome changes what the DTO shows (the
// status, node facts, or a refreshed readAt), so it always republishes; the
// last good graph is kept on a failure.
func (s *LineService) applyMesh(r meshResult) {
	s.mesh.inflight = false
	if r.err != "" {
		s.mesh.err = r.err
	} else {
		g := r.graph
		now := s.d.Now()
		s.mesh.graph, s.mesh.readAt, s.mesh.err = &g, &now, ""
	}
	s.dirty = true
}

// ---- recompute and push ------------------------------------------------------------

// input builds a station's line input from its rows, and resolves its read.
func (s *LineService) input(st *lineStation) (line.StationInput, string, []string) {
	env := st.cfg.Environment
	if env == "" {
		env = st.cfg.Tags["env"]
	}
	in := line.StationInput{Station: line.Station{Cluster: st.cfg.Name, Env: env, Region: st.cfg.Tags["region"], Group: st.cfg.Group, Order: st.order}}
	if st.att == nil && !st.ended {
		read := line.ReadChecking
		if st.snapState == fleet.Failed {
			read = line.ReadError
		}
		return in, read, nil
	}
	if rs, ok := st.rows[partRef{healthsrc.TaskFlux, "reconciliations"}.key()].Rows.(healthsrc.RowSet[flux.Resource]); ok {
		for _, r := range rs.Rows {
			o := line.Object[flux.Resource]{UID: r.Ref.UID, Data: r.Data}
			if r.Data.Kind == flux.HelmReleaseKind {
				in.HelmReleases = append(in.HelmReleases, o)
			} else {
				in.Kustomizations = append(in.Kustomizations, o)
			}
		}
	}
	if rs, ok := st.rows[partRef{healthsrc.TaskFlux, "sources"}.key()].Rows.(healthsrc.RowSet[flux.Source]); ok {
		for _, r := range rs.Rows {
			in.Sources = append(in.Sources, line.Object[flux.Source]{UID: r.Ref.UID, Data: r.Data})
		}
	}
	if rs, ok := st.rows[partRef{healthsrc.TaskArgo, "applications"}.key()].Rows.(healthsrc.RowSet[argo.App]); ok {
		for _, r := range rs.Rows {
			in.Apps = append(in.Apps, line.Object[argo.App]{UID: r.Ref.UID, Data: r.Data})
		}
	}
	// Per-kind coverage from the parts' own kinds, gated by each source
	// partition's continuity: a lost partition, or an available one whose
	// rows predate its recovery, reads unread (note §2.3-2.4).
	coverage := map[string]string{}
	attaching := false
	for _, p := range lineParts {
		r, ok := st.rows[p.key()]
		if !ok {
			attaching = true
			continue
		}
		for _, kc := range r.Part.Kinds {
			if kc.Role != "counted" {
				continue
			}
			cov := string(kc.Coverage)
			if sp := st.parts[partitionRef{p.task, p.part, kc.Kind}]; sp != nil {
				switch {
				case sp.state == partitionLost && kc.Coverage == observe.Absent && r.Part.Ver >= sp.ver:
					// A source absent from the start commits no batch (slice
					// 2b): rows at or after the loss carry the capability
					// word, or a bootstrap caught mid-probe stays checking.
					cov = "absent"
				case sp.state == partitionLost:
					cov = "checking"
				case sp.state == partitionAbsent && r.Part.Ver < sp.ver:
					// Rows from before the absence still hold the kind's old
					// objects: never known-empty until rows at or after it.
					cov = "checking"
				case sp.state == partitionAbsent:
					cov = "absent"
				case sp.state == partitionAvailable && r.Part.Ver < sp.ver:
					cov = "checking"
				}
			}
			coverage[kc.Kind] = cov
		}
	}
	read := line.ResolveRead(&in, coverage, line.Availability{Usable: st.usable && !st.ended, Attaching: attaching})
	var reasons []string
	for k, r := range in.KindRead {
		if r != line.ReadOK && r != line.KindAbsent {
			reasons = append(reasons, k+": "+r)
		}
	}
	sort.Strings(reasons)
	if !st.usable && st.conn != "" {
		reasons = append([]string{"connection: " + st.conn}, reasons...)
	}
	return in, read, reasons
}

func (s *LineService) recompute() {
	s.dirty, s.lastRun = false, time.Now()
	now := s.d.Now()
	names := make([]string, 0, len(s.stations))
	for n := range s.stations {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return s.stations[names[i]].order < s.stations[names[j]].order })
	in := line.Input{Read: map[string]string{}, Override: s.d.Override, Selector: s.selector}
	reasons := map[string][]string{}
	var git []LineGitSource
	for _, n := range names {
		st := s.stations[n]
		si, read, why := s.input(st)
		git = append(git, gitSources(si)...)
		in.Stations = append(in.Stations, si)
		in.Read[n], reasons[n] = read, why
		if read == line.ReadOK {
			t := now
			st.lastOKAt = &t
		}
	}
	l := line.Build(in)
	refChanged := lineRef(l.Key, l.R) != lineRef(s.lastKey, s.lastR)
	s.lastKey, s.lastR = l.Key, l.R
	proofs := map[string]*stationProofs{}
	for _, sls := range [][]line.StationLine{l.Stations, l.Siding} {
		for _, sl := range sls {
			st := s.stations[sl.Cluster]
			if st.inc == 0 {
				continue
			}
			// A new reference re-settles every station's held and state
			// (ending their proofs); unchanged ones were committed with
			// their accepted changes.
			if refChanged || st.histDirty || !s.hist.committed(sl.Cluster, st.inc) {
				s.commitStation(sl.Cluster, st, nil)
			}
			proofs[sl.Cluster] = s.hist.proofsOf(l, sl)
		}
	}
	dto := lineDTO(l)
	fill := func(ds []LineStationDTO) {
		for i := range ds {
			st := s.stations[ds[i].Cluster]
			ds[i].Incarnation, ds[i].ReadReasons = st.inc, reasons[ds[i].Cluster]
			ds[i].ReadAt = oldestEvaluated(st)
			if ds[i].Read != line.ReadOK && st.lastOKAt != nil {
				t := *st.lastOKAt
				ds[i].AsOf = &t
			}
			if p := proofs[ds[i].Cluster]; p != nil {
				ds[i].FailingSince, ds[i].HeldSince = p.failing, p.held
			}
		}
	}
	fill(dto.Stations)
	fill(dto.Siding)
	if s.mesh.graph != nil {
		dto.Mesh = meshDTO(line.Mesh(l.Placement, *s.mesh.graph))
		// A configured station the sample has no read for - its reader
		// skipped it (not connected) or could not read it - has unknown
		// peerings, never "no peers".
		read := map[string]bool{}
		for _, n := range s.mesh.graph.Nodes {
			if n.Present && n.ReadNote == "" {
				read[n.Cluster] = true
			}
		}
		for _, name := range names {
			if !read[name] {
				dto.Mesh.Unknown = append(dto.Mesh.Unknown, name)
			}
		}
	} else {
		dto.Mesh = LineMeshDTO{Drawn: []LineMeshLinkDTO{}, Card: map[string][]LineMeshLinkDTO{}, OffFleet: []LineMeshLinkDTO{}}
	}
	dto.Mesh.ReadAt, dto.Mesh.Error = s.mesh.readAt, s.mesh.err
	s.version++
	dto.Version = s.version
	s.dtoMu.Lock()
	s.dto = dto
	s.dtoMu.Unlock()
	if s.d.OnLine != nil {
		s.d.OnLine(dto, git)
	}
	if s.d.Emitter != nil {
		s.d.Emitter.Emit(LineEvent, dto)
	}
}

// commitDirty commits every station with an accepted change since its last
// history commit (note §5: history follows every accepted change, not the
// presentation's coalesced recompute).
func (s *LineService) commitDirty() {
	for name, st := range s.stations {
		if st.histDirty && st.inc != 0 {
			s.commitStation(name, st, nil)
		}
	}
}

// attached: the station's current incarnation is attached, usable and has
// rows for every line part.
func (s *LineService) attached(st *lineStation) bool {
	if st.att == nil || st.ended || !st.usable {
		return false
	}
	for _, p := range lineParts {
		if _, ok := st.rows[p.key()]; !ok {
			return false
		}
	}
	return true
}

// rowTrigger is the accepted row set a history commit follows.
type rowTrigger struct {
	part string    // its part key
	at   time.Time // its capture time
}

var (
	partFluxRecon   = partRef{healthsrc.TaskFlux, "reconciliations"}.key()
	partFluxSources = partRef{healthsrc.TaskFlux, "sources"}.key()
	partArgoApps    = partRef{healthsrc.TaskArgo, "applications"}.key()
)

// histInputs are the parts each history partition reads: its evidence time
// is the newest capture among them, never an unrelated part's.
var histInputs = map[string][]string{
	unitsKustomizations: {partFluxRecon, partFluxSources},
	unitsHelmReleases:   {partFluxRecon, partFluxSources},
	unitsApplications:   {partArgoApps},
	linePartHeld:        {partFluxRecon, partFluxSources, partArgoApps},
	linePartState:       {partFluxRecon, partFluxSources, partArgoApps},
}

// commitStation commits a station's history partitions (note §5): each unit
// kind's failing predicates, then its held predicate and state under the
// last build's reference. Each partition is dated by its own inputs'
// newest capture and broken only by the gaps its own inputs need; trig is
// the accepted row set this commit follows (nil for a lifecycle change).
func (s *LineService) commitStation(cluster string, st *lineStation, trig *rowTrigger) {
	in, read, _ := s.input(st)
	attached := s.attached(st)
	needed := neededByPartition(in)
	// A pending part's retained rows predate its gap: its kinds are unread
	// until its newer rows are installed.
	pendingKind := map[string]bool{}
	for p := range st.pending {
		for _, k := range partKinds[p] {
			pendingKind[k] = true
		}
	}
	kindsOK := func(kinds []string) bool {
		for _, k := range kinds {
			if r := in.KindRead[k]; pendingKind[k] || (r != line.ReadOK && r != line.KindAbsent) {
				return false
			}
		}
		return true
	}
	gapped := func(kinds []string) bool {
		for _, k := range kinds {
			if st.gapKinds[k] {
				return true
			}
		}
		return false
	}
	evidence := func(part string, kinds []string, readable bool) histCommit {
		c := histCommit{readable: readable, broken: st.allGap || gapped(kinds), at: evidenceAt(st, histInputs[part], s.d.Now())}
		if trig != nil {
			for _, p := range histInputs[part] {
				if p == trig.part {
					t := trig.at
					c.trigger, c.part = &t, trig.part
				}
			}
		}
		return c
	}
	sl := line.Settle(in, read, s.lastKey, s.lastR)
	preds := map[string]map[string]string{}
	for _, p := range unitPartitions {
		preds[p] = map[string]string{}
	}
	for _, u := range sl.Units {
		v := predOK
		if u.Failing {
			v = predFault
		}
		preds[unitPartitionOf(u)][unitPredKey(u)] = v
	}
	var all []string
	for _, p := range unitPartitions {
		s.hist.commit(cluster, st.inc, linePartUnits, p, preds[p], evidence(p, needed[p], attached && kindsOK(needed[p])))
		all = append(all, needed[p]...)
	}
	// Held and state are relative to the last build's key and R.
	s.hist.reference(cluster, st.inc, lineRef(s.lastKey, s.lastR), evidenceAt(st, histInputs[linePartState], s.d.Now()))
	whole := attached && read == line.ReadOK
	for _, k := range all {
		whole = whole && !pendingKind[k]
	}
	held := map[string]string{keyHeld: predOK}
	if sl.Held {
		held[keyHeld] = predHeld
	}
	s.hist.commit(cluster, st.inc, linePartHeld, lineTask, held, evidence(linePartHeld, all, whole))
	s.hist.commit(cluster, st.inc, linePartState, lineTask, map[string]string{keyState: sl.State}, evidence(linePartState, all, whole))
	// A gap lasts until the commit after its part's newer rows: a pending
	// part's kinds keep theirs.
	kept := map[string]bool{}
	for k := range st.gapKinds {
		if pendingKind[k] {
			kept[k] = true
		}
	}
	st.allGap, st.gapKinds, st.histDirty = false, kept, false
}

// neededByPartition is each unit partition's input kinds (note §2.4, §5): a
// unit kind needs its own kind and the source kinds its failing predicate
// reads - a Kustomization's or a HelmRelease's bound source, every source
// kind it could reference while its kind is unread. A recorded-name chart
// comparison never needs the source, but its failure does. Argo
// applications need only their own kind.
func neededByPartition(in line.StationInput) map[string][]string {
	needs := func(kind string, could []string, refs []string) []string {
		out := []string{kind}
		seen := map[string]bool{kind: true}
		add := func(k string) {
			if k != "" && !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
		if r := in.KindRead[kind]; r != line.ReadOK && r != line.KindAbsent {
			for _, k := range could {
				add(k)
			}
		}
		for _, k := range refs {
			add(k)
		}
		return out
	}
	srcKinds := map[string]string{"GitRepository": "gitrepositories", "OCIRepository": "ocirepositories", "Bucket": "buckets",
		"HelmRepository": "helmrepositories", "HelmChart": "helmcharts"}
	var ksRefs, hrRefs []string
	for _, o := range in.Kustomizations {
		ksRefs = append(ksRefs, srcKinds[o.Data.SourceKind])
	}
	for _, o := range in.HelmReleases {
		hrRefs = append(hrRefs, srcKinds[o.Data.SourceKind])
	}
	return map[string][]string{
		unitsKustomizations: needs("kustomizations", []string{"gitrepositories", "ocirepositories", "buckets"}, ksRefs),
		unitsHelmReleases:   needs("helmreleases", []string{"helmrepositories", "gitrepositories", "buckets", "ocirepositories", "helmcharts"}, hrRefs),
		unitsApplications:   {"applications"},
	}
}

// evidenceAt is the newest capture among the given parts: a partition's
// evidence time (the worker's clock only while it holds none of them).
func evidenceAt(st *lineStation, parts []string, fallback time.Time) time.Time {
	var out time.Time
	for _, p := range parts {
		if r, ok := st.rows[p]; ok && r.EvaluatedAt.After(out) {
			out = r.EvaluatedAt
		}
	}
	if out.IsZero() {
		return fallback
	}
	return out
}

func lineRef(k *line.Key, r string) string {
	if k == nil {
		return ""
	}
	return k.String() + "@" + r
}

// oldestEvaluated is the oldest evaluation time among a station's line parts.
func oldestEvaluated(st *lineStation) *time.Time {
	var out *time.Time
	for _, r := range st.rows {
		if t := r.EvaluatedAt; !t.IsZero() && (out == nil || t.Before(*out)) {
			tc := t
			out = &tc
		}
	}
	return out
}

// LineGitSource is one Git repository URL a station's rows name, exactly as
// the cluster holds it: a GitRepository's spec.url or an Argo Git source's
// repoURL. Settings > Git only matches it to an approved entry and shows it
// scrubbed; it is never executed (parent §7).
type LineGitSource struct {
	Cluster, Origin, Namespace, Name, URL string
}

func gitSources(in line.StationInput) []LineGitSource {
	var out []LineGitSource
	for _, o := range in.Sources {
		if o.Data.Kind == flux.GitRepositoryKind && o.Data.URL != "" {
			out = append(out, LineGitSource{Cluster: in.Cluster, Origin: string(o.Data.Kind), Namespace: o.Data.Namespace,
				Name: o.Data.Name, URL: o.Data.URL})
		}
	}
	for _, o := range in.Apps {
		for _, src := range o.Data.Sources {
			if src.Chart == "" && src.RepoURL != "" {
				out = append(out, LineGitSource{Cluster: in.Cluster, Origin: "Application", Namespace: o.Data.Namespace,
					Name: o.Data.Name, URL: urlscrub.Field(src.RepoURL).Text})
			}
		}
	}
	return out
}
