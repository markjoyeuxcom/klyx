package appbridge

import (
	"context"
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
	Now       func() time.Time

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

type stationPartition struct {
	state string
	ver   uint64
}

type lineStation struct {
	cfg        config.ClusterConfig
	order      int
	att        *lineAttachment
	inc        uint64
	ended      bool // Final: terminal for inc
	usable     bool // the connection word allows observation
	conn       string
	rows       map[string]observe.Rows // per part key, current incarnation
	booted     map[string]bool
	parts      map[partitionRef]*stationPartition
	histGap    bool // continuity broke since the line history last committed
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
	dirty    bool
	lastRun  time.Time
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
		st.lastOKAt, st.histGap = nil, true
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.nextAtt++
	att := &lineAttachment{id: s.nextAtt, obs: obs, inc: inc, ctx: ctx, cancel: cancel, inflight: map[string]bool{}}
	att.releases = append(att.releases, obs.Acquire(healthsrc.TaskFlux, lineOwner), obs.Acquire(healthsrc.TaskArgo, lineOwner))
	for _, p := range lineParts {
		att.releases = append(att.releases, obs.SubscribeRows(p.task, p.part, lineOwner))
	}
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
		s.applyRows(st, r.part.key(), r.rows, false)
		s.seed(r.cluster, st, r.rows)
		s.dirty = true
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
		if sum := d.summary; sum != nil {
			switch {
			case sum.Incarnation > st.inc:
				s.scan() // a newer Observer: attach it
			case sum.Incarnation == st.inc:
				if usable := lineConnectionUsable(sum.Connection); usable != st.usable {
					st.usable = usable
					if !usable {
						st.histGap = true
					}
				}
				st.conn = sum.Connection
				if sum.Final {
					st.ended, st.histGap = true, true
					s.detach(st)
				}
			}
		}
		if st.ended {
			continue
		}
		for k, r := range d.rows {
			if r.Incarnation != st.inc {
				continue
			}
			s.applyRows(st, k, r, d.overwrite[k])
		}
		for pr, p := range d.partitions {
			if p.inc != st.inc {
				continue
			}
			if p.state != partitionUnknown {
				st.parts[pr] = &stationPartition{state: p.state, ver: p.ver}
			}
			if p.gap {
				st.histGap = true
			}
		}
		s.dirty = true
	}
}

// applyRows keeps a part's rows when they are newer than the ones held.
func (s *LineService) applyRows(st *lineStation, key string, r observe.Rows, overwritten bool) {
	if overwritten {
		st.histGap = true // a discarded row set may have shown a flip (note §5)
	}
	if held, ok := st.rows[key]; ok && held.Part.Ver >= r.Part.Ver {
		return
	}
	st.rows[key] = r
	st.booted[key] = true
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
			st.parts[pr] = &stationPartition{state: partitionAbsent}
		default:
			st.parts[pr] = &stationPartition{state: partitionLost}
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
			if cur == nil || cur.state != partitionAvailable {
				st.parts[pr] = &stationPartition{state: partitionAvailable, ver: ps.PartVer}
			}
		case ps.Loss != nil && ps.Loss.Reason == "absent":
			st.parts[pr] = &stationPartition{state: partitionAbsent}
		default:
			st.parts[pr] = &stationPartition{state: partitionLost}
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
				case sp.state == partitionLost:
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
	for _, n := range names {
		st := s.stations[n]
		si, read, why := s.input(st)
		in.Stations = append(in.Stations, si)
		in.Read[n], reasons[n] = read, why
		if read == line.ReadOK {
			t := now
			st.lastOKAt = &t
		}
	}
	l := line.Build(in)
	proofs := s.hist.commit(l, s.stations, now)
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
		for _, n := range s.mesh.graph.Nodes {
			if n.ReadNote != "" {
				dto.Mesh.Unknown = append(dto.Mesh.Unknown, n.Cluster)
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
	if s.d.Emitter != nil {
		s.d.Emitter.Emit(LineEvent, dto)
	}
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
