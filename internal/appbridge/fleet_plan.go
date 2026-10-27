package appbridge

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/policy"
)

// Settings S2 §4: fleet plans. Each plan is for one cluster, from what runs
// to a candidate, in F's engine: the person confirms the plan's digest, and
// Apply re-plans under the fleet Apply lock and the cluster's reservation,
// refusing with the new plan when anything moved. A plan compares
// effective entries (the context defaulted, the derived tags) to class its
// change, and writes the file's raw entry: only the fields the person
// changed.

// Fleet plan kinds, beyond F's save, file and revert.
const (
	planConnect = "connect" // a cluster in the file and not running
	planRemove  = "remove"
	planRetry   = "retry" // a fresh connection for the saved terms
)

// Fleet plan classes (S2 §4.2): what the Apply does.
const (
	classPlacement  = "placement"  // relabel; the epoch stays
	classProtection = "protection" // the epoch moves once
	classReconnect  = "reconnect"  // held until the new connection is usable
	classRemoval    = "removal"
	classAddition   = "addition"
)

// ClusterDraftDTO is the editor's draft of one cluster's S2 fields, as
// fleet.yaml would say them: raw, before any default. Region is
// tags.region.
type ClusterDraftDTO struct {
	Context     string            `json:"context"`
	Kubeconfig  string            `json:"kubeconfig"`
	Group       string            `json:"group"`
	Environment string            `json:"environment"`
	Tags        map[string]string `json:"tags"`
	Protected   bool              `json:"protected"`
}

// PlanRecordDTO is one keychain record a removal offers to delete (S2
// §4.4): present, by kind, bound to its credential revision; never its
// secret.
type PlanRecordDTO struct {
	Kind     string `json:"kind"`
	Revision string `json:"revision"`
	Chosen   bool   `json:"chosen"`
}

// FleetPlanDeps is what fleet plans need beyond the configuration service.
type FleetPlanDeps struct {
	Ctx     context.Context // the app's: a connection's lifetime
	Life    *Lifecycle
	Prepare func(config.ClusterConfig) (*fleet.Prepared, error)
	Install func(context.Context, *fleet.Prepared) error
	// Resolve checks at plan time that a context resolves: the REST
	// configuration builds and the context exists (S2 §4.4)
	Resolve func(config.ClusterConfig) error
	// Records are a cluster's keychain records present now; CredLock
	// serializes their deletion with the credential actions, and Delete
	// deletes one
	Records  func(cluster string) []PlanRecordDTO
	CredLock func() func()
	Delete   func(cluster, kind string) error
	// Hosts are the installations naming the cluster as their host: a
	// removal is refused while there are any (S2 §4.4; S3a fills it)
	Hosts func(cluster string) []string
	// EndEpochWork ends a cluster's conversations explicitly at a committed
	// protection change (S2 §8, ruling 1)
	EndEpochWork func(cluster, why string)
	// Changed is told after an Apply commits: the line rescans
	Changed func()
}

// fleetPlans is the configuration service's plan state.
type fleetPlans struct {
	d     FleetPlanDeps
	cache *planCache
	// beforeDecide is a test seam: it runs after the re-plan and the
	// interlock, before the class's decision. Nil in production.
	beforeDecide func()

	mu sync.Mutex
	// raw S2 terms, kept with the connections that actually use them
	// (Codex's review of S2b, R5): terms, by connection identity; current,
	// each cluster's current connection's or pending attempt's; connected,
	// the last terms whose connection really connected - what Revert writes
	// back. The file's entry never stands in for them.
	terms     map[fleet.ConnID]attemptTerms
	current   map[string]s2Fields
	connected map[string]s2Fields
	// connectedBy is the identity whose publication recorded connected: an
	// edit committed on that identity keeps them coherent
	connectedBy map[string]fleet.ConnID
}

type attemptTerms struct {
	cluster string
	terms   s2Fields
}

// WireFleetPlans gives the configuration service its fleet plans (S2 §4).
// The startup connections' raw terms are read from fleet.yaml as Klyx
// started; a connection's terms become its cluster's last connected terms
// when its usability is published - when it really connected.
func WireFleetPlans(cs *ConfigService, d FleetPlanDeps) {
	fp := &fleetPlans{d: d, cache: newPlanCache(time.Now), terms: map[fleet.ConnID]attemptTerms{}, current: map[string]s2Fields{},
		connected: map[string]s2Fields{}, connectedBy: map[string]fleet.ConnID{}}
	// a startup connection's raw terms are its file entry - only while that
	// entry still says what started, never a hand edit made since
	type started struct {
		name string
		conn fleet.Conn
		id   fleet.ConnID
	}
	var conns []started
	if eff, raw, _, err := config.LoadRaw(cs.path); err == nil {
		inFile := map[string]config.ClusterConfig{}
		for _, c := range eff.Clusters {
			inFile[c.Name] = c
		}
		for _, cc := range cs.fleetClusters() {
			entry, ok := raw[cc.Name]
			fileEff, still := inFile[cc.Name]
			if !ok || !still || !sameS2(fileEff, cc) {
				continue
			}
			fp.current[cc.Name] = s2Of(entry)
			if c, ok := d.Life.reg.Conn(cc.Name); ok {
				if id, ok := connID(c); ok {
					fp.terms[id] = attemptTerms{cluster: cc.Name, terms: s2Of(entry)}
					conns = append(conns, started{cc.Name, c, id})
				}
			}
		}
	}
	cs.fp = fp
	d.Life.OnPublished(fp.published)
	d.Life.OnForget(fp.forget)
	// subscribed: a startup connection whose usability was published before
	// now connected all the same (Codex's re-review of S2b, RR2); one
	// published since is recorded twice, the same
	for _, st := range conns {
		if st.conn.Snapshot().State.Usable() {
			fp.published(st.name, st.id)
		}
	}
}

// published records that connection id of cluster connected: its terms are
// the last that connected.
func (fp *fleetPlans) published(cluster string, id fleet.ConnID) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if a, ok := fp.terms[id]; ok && a.cluster == cluster {
		fp.connected[cluster], fp.connectedBy[cluster] = a.terms, id
	}
}

// edited records an S2 change committed on connection id, which keeps its
// identity (a placement or protection change): its terms, the cluster's
// current terms and - when id is the connection that connected - its last
// connected terms all say the change (Codex's re-review of S2b, RR3).
func (fp *fleetPlans) edited(cluster string, id fleet.ConnID, terms s2Fields) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.terms[id] = attemptTerms{cluster: cluster, terms: terms}
	fp.current[cluster] = terms
	if by, ok := fp.connectedBy[cluster]; ok && by == id {
		fp.connected[cluster] = terms
	}
}

// editedUnbuilt records an S2 change committed on a cluster with no
// connection: its current terms say the change; no identity connected,
// so no history does.
func (fp *fleetPlans) editedUnbuilt(cluster string, terms s2Fields) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.current[cluster] = terms
}

func (fp *fleetPlans) forget(cluster string) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	delete(fp.connected, cluster)
	delete(fp.connectedBy, cluster)
	delete(fp.current, cluster)
	for id, a := range fp.terms {
		if a.cluster == cluster {
			delete(fp.terms, id)
		}
	}
}

func (fp *fleetPlans) lastConnected(cluster string) (s2Fields, bool) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	t, ok := fp.connected[cluster]
	return t, ok
}

// currentTerms are the raw terms of the cluster's current connection or
// pending attempt: what Retry connects with.
func (fp *fleetPlans) currentTerms(cluster string) (s2Fields, bool) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	t, ok := fp.current[cluster]
	return t, ok
}

// s2Fields are the fields S2 edits (§3.1): never a name or an endpoint.
type s2Fields struct {
	Context     string            `json:"context,omitempty"`
	Kubeconfig  string            `json:"kubeconfig,omitempty"`
	Group       string            `json:"group,omitempty"`
	Environment string            `json:"environment,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	Protected   bool              `json:"protected,omitempty"`
}

func s2Of(c config.ClusterConfig) s2Fields {
	return s2Fields{Context: c.Context, Kubeconfig: c.Kubeconfig, Group: c.Group, Environment: c.Environment, Tags: copyTags(c.Tags), Protected: c.Protected}
}

func draftFields(d ClusterDraftDTO) s2Fields {
	tags := map[string]string{}
	for k, v := range d.Tags {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k != "" && v != "" {
			tags[k] = v
		}
	}
	if len(tags) == 0 {
		tags = nil
	}
	return s2Fields{Context: strings.TrimSpace(d.Context), Kubeconfig: strings.TrimSpace(d.Kubeconfig), Group: strings.TrimSpace(d.Group),
		Environment: strings.TrimSpace(d.Environment), Tags: tags, Protected: d.Protected}
}

// onto is c with f's S2 fields, as written: its name and endpoints stay.
func (f s2Fields) onto(c config.ClusterConfig) config.ClusterConfig {
	c.Context, c.Kubeconfig, c.Group, c.Environment, c.Tags, c.Protected = f.Context, f.Kubeconfig, f.Group, f.Environment, copyTags(f.Tags), f.Protected
	return c
}

func copyTags(t map[string]string) map[string]string {
	if len(t) == 0 {
		return nil
	}
	out := make(map[string]string, len(t))
	for k, v := range t {
		out[k] = v
	}
	return out
}

// editBetween is the writer's edit from the file's raw fields to to: only
// what differs, a field cleared when to leaves it empty.
func editBetween(from, to s2Fields) (config.ClusterEdit, bool) {
	var e config.ClusterEdit
	changed := false
	str := func(a, b string) *string {
		if a == b {
			return nil
		}
		changed = true
		return &b
	}
	e.Context, e.Kubeconfig, e.Group, e.Environment = str(from.Context, to.Context), str(from.Kubeconfig, to.Kubeconfig), str(from.Group, to.Group), str(from.Environment, to.Environment)
	if from.Protected != to.Protected {
		p := to.Protected
		e.Protected, changed = &p, true
	}
	if !reflect.DeepEqual(copyTags(from.Tags), copyTags(to.Tags)) {
		t := copyTags(to.Tags)
		if t == nil {
			t = map[string]string{}
		}
		e.Tags, changed = &t, true
	}
	return e, changed
}

// connTerms are what a connection is built from: a change is a reconnect.
func connTerms(c config.ClusterConfig) [2]string { return [2]string{c.Context, c.Kubeconfig} }

// sameS2 reports whether two effective entries agree on every S2 field.
func sameS2(a, b config.ClusterConfig) bool {
	return reflect.DeepEqual(s2Of(a), s2Of(b))
}

// fleetCand is what a plan's digest holds in the cache: never a secret.
type fleetCand struct {
	Kind    string    `json:"kind"`
	Cluster string    `json:"cluster"`
	Draft   *s2Fields `json:"draft,omitempty"`   // save: the raw draft
	Records []string  `json:"records,omitempty"` // remove: the chosen kinds
}

// fleetPlanned is a plan's working state, for its Apply.
type fleetPlanned struct {
	plan    PlanDTO
	class   string
	bound   Bound
	version string
	cand    config.ClusterConfig // effective
	terms   s2Fields             // raw: what the candidate's attempt connects with
	before  s2Fields             // raw: the file's entry before the write
	edit    *config.ClusterEdit  // the entry's edit; nil writes no edit
	remove  bool                 // the Apply removes the entry from the file
}

// PlanClusterEdit plans the editor's draft for a running cluster (kind save).
func (s *ConfigService) PlanClusterEdit(name string, d ClusterDraftDTO) PlanDTO {
	f := draftFields(d)
	return s.planCluster(fleetCand{Kind: planSave, Cluster: name, Draft: &f}).plan
}

// PlanClusterFromFile plans "Use the file's values" (S2 §4.6): a running
// cluster's entry as the file says it, a cluster in the file and not
// running connected, or one gone from the file removed - writing nothing.
func (s *ConfigService) PlanClusterFromFile(name string) PlanDTO {
	kind := planFile
	if _, running := s.runningEntry(name); !running {
		kind = planConnect
	}
	return s.planCluster(fleetCand{Kind: kind, Cluster: name}).plan
}

// PlanClusterRemove plans removing a cluster, with the keychain records
// chosen for deletion (S2 §4.4).
func (s *ConfigService) PlanClusterRemove(name string, records []string) PlanDTO {
	return s.planCluster(fleetCand{Kind: planRemove, Cluster: name, Records: sortedUnique(records)}).plan
}

// PlanClusterRevert plans a held cluster back to the last terms that
// connected (S2 §4.5).
func (s *ConfigService) PlanClusterRevert(name string) PlanDTO {
	return s.planCluster(fleetCand{Kind: planRevert, Cluster: name}).plan
}

// PlanClusterRetry plans a fresh connection for a held cluster's saved
// terms (S2 §4.5).
func (s *ConfigService) PlanClusterRetry(name string) PlanDTO {
	return s.planCluster(fleetCand{Kind: planRetry, Cluster: name}).plan
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// runningEntry is the running fleet's entry for name.
func (s *ConfigService) runningEntry(name string) (config.ClusterConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.running {
		if c.Name == name {
			return c, true
		}
	}
	return config.ClusterConfig{}, false
}

func fleetRefused(c fleetCand, why string) PlanDTO {
	return PlanDTO{Section: "fleet", Kind: c.Kind, Cluster: c.Cluster, Refused: why, Runtime: map[string]string{}, Changes: []PlanChangeDTO{}, Ends: []PlanWorkDTO{}, Continues: []PlanWorkDTO{}}
}

// planCluster plans c from what runs and the file as it is now.
func (s *ConfigService) planCluster(c fleetCand) fleetPlanned {
	x := fleetPlanned{}
	fp := s.fp
	if fp == nil {
		x.plan = fleetRefused(c, "fleet changes are not available")
		return x
	}
	refuse := func(why string) fleetPlanned {
		x.plan.Refused = why
		x.plan.Changes, x.plan.Ends, x.plan.Continues, x.plan.Interlock, x.plan.Records = []PlanChangeDTO{}, []PlanWorkDTO{}, []PlanWorkDTO{}, nil, nil
		bind, _ := json.Marshal(c)
		seal(&x.plan, string(bind))
		return x
	}
	x.plan = fleetRefused(c, "")
	eff, raw, version, err := config.LoadRaw(s.path)
	x.version, x.plan.FileVersion = version, version
	x.plan.Runtime["fleet"] = strconv.FormatUint(RunningRevision(s), 10)
	if err != nil {
		return refuse("fleet.yaml does not load, so nothing can be planned: " + config.DisplayError(err))
	}
	name := c.Cluster
	run, running := s.runningEntry(name)
	var fileEff config.ClusterConfig
	inFile := false
	for _, cl := range eff.Clusters {
		if cl.Name == name {
			fileEff, inFile = cl, true
		}
	}
	fileRaw := raw[name]
	x.bound, _ = fp.d.Life.BoundOf(name)
	x.plan.Runtime["epoch"] = x.bound.Epoch.String()
	x.plan.Runtime["attempt"] = strconv.FormatUint(x.bound.Attempt, 10)
	held := x.bound.Attempt != 0 || s.heldNow(name)
	x.before = s2Of(fileRaw)

	switch c.Kind {
	case planSave:
		switch {
		case !running:
			return refuse(name + " is not running; use Connect")
		case !inFile:
			return refuse(name + " is not in fleet.yaml; use the file's values")
		case !sameS2(fileEff, run):
			return refuse("fleet.yaml's entry for " + name + " differs from the one in use, so nothing was planned: use the file's values first, or undo that edit")
		case c.Draft == nil:
			return refuse("no draft")
		}
		e, changed := editBetween(x.before, *c.Draft)
		if !changed {
			return refuse("the draft changes nothing")
		}
		x.edit, x.terms = &e, *c.Draft
		x.cand = c.Draft.onto(withName(fileRaw, name)).Effective()
		x.cand = withEndpoints(x.cand, run)
	case planFile:
		switch {
		case running && inFile:
			x.terms = x.before
			x.cand = withEndpoints(fileEff, run)
			if sameS2(x.cand, run) {
				return refuse("fleet.yaml already says what " + name + " runs with")
			}
		case running:
			x.remove = true // gone from the file: removed, writing nothing
		default:
			return refuse(name + " is not running and not in fleet.yaml")
		}
	case planConnect:
		switch {
		case running:
			return refuse(name + " is already running")
		case !inFile:
			return refuse(name + " is not in fleet.yaml")
		}
		x.terms, x.cand = x.before, fileEff
	case planRemove:
		if !running {
			return refuse(name + " is not running")
		}
		x.remove = true
	case planRevert:
		last, ok := fp.lastConnected(name)
		switch {
		case !running:
			return refuse(name + " is not running")
		case !held:
			return refuse(name + " is connected: there is nothing to revert")
		case !ok:
			return refuse(name + " has not connected under other terms this session: nothing to revert to; use Retry")
		}
		x.terms = last
		x.cand = withEndpoints(last.onto(withName(fileRaw, name)).Effective(), run)
		if inFile {
			if e, changed := editBetween(x.before, last); changed {
				x.edit = &e
			}
		}
	case planRetry:
		saved, ok := fp.currentTerms(name)
		switch {
		case !running:
			return refuse(name + " is not running")
		case !held:
			return refuse(name + " is not waiting to connect: there is nothing to retry")
		case !ok:
			return refuse("Klyx does not know the terms " + name + " was saved with; use the file's values")
		}
		// the saved terms that run, never a hand edit since: Retry makes no
		// history the connection did not have (S2b review, R5)
		x.terms, x.cand = saved, run
	default:
		return refuse("unknown plan")
	}

	// protection's baseline is the published value - a held reconnect's
	// pending terms are not in force yet (Codex's review of S2b, R4)
	published := running && s.IsProtected(name)

	// the class, from what changes (S2 §4.2)
	switch {
	case x.remove:
		x.class = classRemoval
	case !running:
		x.class = classAddition
	case c.Kind == planRevert || c.Kind == planRetry || connTerms(run) != connTerms(x.cand):
		x.class = classReconnect
	case published != x.cand.IsProtected():
		x.class = classProtection
	default:
		x.class = classPlacement
	}
	x.plan.Class = x.class
	if held && x.class != classReconnect && x.class != classRemoval &&
		(x.cand.IsProtected() != published || x.cand.IsProtected() != run.IsProtected()) {
		// the hold publishes its pending attempt's protection when it
		// connects: a change of either the published or the pending value
		// now is refused before anything is written (S2b review, R3; its
		// re-review, RR1)
		return refuse(name + " is saved, not connected yet: change its protection once it connects, or Revert or Retry first")
	}

	// the checks made at plan time (S2 §4.4)
	switch x.class {
	case classRemoval:
		if len(s.fleetClusters()) <= 1 {
			return refuse("Klyx needs at least one cluster in fleet.yaml, so " + name + " cannot be removed")
		}
		if fp.d.Hosts != nil {
			if hosts := fp.d.Hosts(name); len(hosts) > 0 {
				return refuse(name + " hosts " + strings.Join(hosts, ", ") + ": remove those first")
			}
		}
	case classReconnect, classAddition:
		if fp.d.Resolve != nil {
			if err := fp.d.Resolve(x.cand); err != nil {
				return refuse("Klyx cannot connect with these terms, so nothing was planned: " + config.DisplayError(err))
			}
		}
	}

	// what it shows
	if x.class == classRemoval {
		x.plan.Changes = []PlanChangeDTO{{ID: "fleet.cluster", Label: "Cluster", From: name, To: "removed"}}
		if c.Kind == planRemove && inFile {
			x.plan.Writes = true
		}
		recs, why := fp.records(name, c.Records)
		if why != "" {
			return refuse(why)
		}
		x.plan.Records = recs
		var revs []string
		for _, r := range recs {
			revs = append(revs, r.Kind+"="+r.Revision)
		}
		x.plan.Runtime["records"] = strings.Join(revs, ",")
		if published {
			x.plan.Interlock = &InterlockDTO{Phrase: name, Why: "Klyx disconnects the protected cluster " + name + " and stops showing it."}
		}
	} else {
		from := run
		if !running {
			from = config.ClusterConfig{Name: name}
		}
		x.plan.Changes = clusterChanges(from, x.cand, running, published)
		x.plan.Writes = x.edit != nil
		if published && !x.cand.IsProtected() {
			x.plan.Interlock = &InterlockDTO{Phrase: name, Why: "Destructive actions on " + name + " will no longer ask for its name to be typed, and AI egress stops treating it as protected."}
		}
	}
	x.plan.Activates = true
	x.plan.Ends, x.plan.Continues = s.planWork(name, x.class)
	x.plan.Runtime["work"] = workVersion(append(append([]PlanWorkDTO{}, x.plan.Ends...), x.plan.Continues...))
	bind, _ := json.Marshal(c)
	seal(&x.plan, string(bind))
	fp.cache.put(x.plan.Digest, c)
	return x
}

func withName(c config.ClusterConfig, name string) config.ClusterConfig {
	c.Name = name
	return c
}

// withEndpoints is cand with run's endpoint terms: the active ones (S2
// §2.6, §4.6), never the file's.
func withEndpoints(cand, run config.ClusterConfig) config.ClusterConfig {
	cand.Metrics, cand.Alertmanager, cand.Loki = run.Metrics, run.Alertmanager, run.Loki
	return cand
}

// heldNow reports whether the cluster is Held: saved, not active.
func (s *ConfigService) heldNow(name string) bool {
	if s.policy == nil {
		return false
	}
	snap, ok := s.policy.Snapshot(name)
	return ok && snap.State == policy.Held
}

// planWork is what the class's Apply ends and continues (S2 §4.2): a
// reconnect or a removal ends the connection's work, and continuing work
// runs on; a protection change ends the work pinned to the epoch - the
// conversations and the AI calls; a placement ends nothing.
func (s *ConfigService) planWork(name, class string) (ends, continues []PlanWorkDTO) {
	ends, continues = []PlanWorkDTO{}, []PlanWorkDTO{}
	switch class {
	case classReconnect, classRemoval:
		return s.fp.d.Life.ClusterWork(name)
	case classProtection:
		e, cont := s.fp.d.Life.ClusterWork(name)
		for _, w := range e {
			switch w.Kind {
			case kindChat, kindInvestigation, "explain", "suggest-fix":
				ends = append(ends, w)
			}
		}
		return ends, cont
	}
	return ends, continues
}

// records are the cluster's keychain records present now, each marked
// chosen when it is in chosen; a chosen kind not present refuses.
func (fp *fleetPlans) records(name string, chosen []string) ([]PlanRecordDTO, string) {
	var present []PlanRecordDTO
	if fp.d.Records != nil {
		present = fp.d.Records(name)
	}
	byKind := map[string]bool{}
	for i := range present {
		byKind[present[i].Kind] = true
		for _, k := range chosen {
			if k == present[i].Kind {
				present[i].Chosen = true
			}
		}
	}
	for _, k := range chosen {
		if !byKind[k] {
			return nil, "the keychain holds no " + k + " credential for " + name
		}
	}
	sort.Slice(present, func(i, j int) bool { return present[i].Kind < present[j].Kind })
	if present == nil {
		present = []PlanRecordDTO{}
	}
	return present, ""
}

// clusterChanges are the effective changes from run to cand, with
// protection's effect as its own line naming what decides it (S2 §4.2):
// from the published value, which a held reconnect's pending terms do not
// set yet. A cluster connected from the file names the endpoint kinds it
// brings (S2 §4.6), never their secrets.
func clusterChanges(run, cand config.ClusterConfig, running, published bool) []PlanChangeDTO {
	out := []PlanChangeDTO{}
	add := func(id, label, from, to string) {
		if from != to {
			out = append(out, PlanChangeDTO{ID: id, Label: label, From: from, To: to})
		}
	}
	if !running {
		add("fleet.cluster", "Cluster", "not running", "connected")
	}
	add("fleet.context", "Context", run.Context, cand.Context)
	add("fleet.kubeconfig", "Kubeconfig", orDefault(run.Kubeconfig), orDefault(cand.Kubeconfig))
	add("fleet.group", "Group", run.Group, cand.Group)
	add("fleet.environment", "Environment", run.Environment, cand.Environment)
	add("fleet.region", "Region", run.Tags["region"], cand.Tags["region"])
	add("fleet.tags", "Tags", tagsText(run.Tags), tagsText(cand.Tags))
	from := protectionText(run)
	if running && run.IsProtected() != published {
		from = map[bool]string{true: "on", false: "off"}[published] + ", until the pending connection is usable"
	}
	add("fleet.protection", "Protection", from, protectionText(cand))
	if !running {
		var kinds []string
		for _, e := range cand.Endpoints() {
			kinds = append(kinds, e.Kind)
		}
		if len(kinds) > 0 {
			add("fleet.endpoints", "Endpoints", "none", strings.Join(kinds, ", "))
		}
	}
	return out
}

func orDefault(path string) string {
	if path == "" {
		return "the default kubeconfig"
	}
	return path
}

// tagsText is the tags other than region, as one line.
func tagsText(t map[string]string) string {
	var keys []string
	for k := range t {
		if k != "region" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+t[k])
	}
	return strings.Join(parts, ", ")
}

// protectionText is protection's effect in words, with the input that
// decides it.
func protectionText(c config.ClusterConfig) string {
	by := c.ProtectedBy()
	switch {
	case by == "":
		return "off"
	case by == "protected: true":
		return "on, set in fleet.yaml"
	}
	return "on, inferred from " + by
}
