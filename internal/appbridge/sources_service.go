package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/safehttp"
	"github.com/moomora/klyx/internal/secretstore"
)

// Settings S3a §5, §8; S3b §5: Observability's sources of every kind -
// Alertmanager, Loki and metrics: the installations in force and each
// cluster's subscription - planned from what runs and applied as one
// transaction under the fleet Apply lock. A plan is of one kind, a panel's,
// and has two candidates: the file with the plan's edits, which is
// written; and the running fleet with the installations in force and
// exactly the terms the plan approves, which is resolved for every kind
// and published. Whatever else differs in the file is neither adopted nor
// published.

// SourcesDeps are what the sources service reads and publishes through.
type SourcesDeps struct {
	// Active are the installations and subscriptions in force
	Active func() *fleet.AlertSources
	// Publish puts installations and the running fleet's subscriptions in
	// force (the registry's PublishSources)
	Publish func(sources []config.SourceConfig, running []config.ClusterConfig) error
	// Work is the AI work holding data read under the terms that move: for
	// each kind, the clusters whose terms of that kind move
	Work func(moved map[string][]string) []PlanWorkDTO
	// Read is an installation's latest read
	Read func(id string) (fleet.InstallationRead, bool)
	// Store and Revs say an installation's credential state
	Store secretstore.Store
	Revs  *endpointcred.Registry
	// Detect runs Detect in one cluster (the registry's DetectAlertmanager)
	Detect func(ctx context.Context, cluster string, fleetNames []string) fleet.DetectSample
}

// SourcesService is Observability's installations and subscriptions.
type SourcesService struct {
	path  string
	cs    *ConfigService
	d     SourcesDeps
	cache *planCache
}

// NewSourcesService is the sources service over the fleet file at path and
// the running fleet cs holds.
func NewSourcesService(path string, cs *ConfigService, d SourcesDeps) *SourcesService {
	return &SourcesService{path: path, cs: cs, d: d, cache: newPlanCache(time.Now)}
}

// ServiceRefDTO is a Service an installation is reached at, through its
// host's API server.
type ServiceRefDTO struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Port      string `json:"port"`
	Scheme    string `json:"scheme,omitempty"`
	Path      string `json:"path,omitempty"`
}

// SourceDraftDTO is an installation's fields, as edited (S3a §2.1). It
// carries no secret: an endpoint's credential is the keychain's.
type SourceDraftDTO struct {
	ID             string         `json:"id"`
	Name           string         `json:"name,omitempty"`
	Host           string         `json:"host,omitempty"`
	ServiceRef     *ServiceRefDTO `json:"serviceRef,omitempty"`
	Discover       bool           `json:"discover,omitempty"`
	Endpoint       string         `json:"endpoint,omitempty"`
	TLSSkipVerify  bool           `json:"tlsSkipVerify,omitempty"`
	AuthType       string         `json:"authType,omitempty"`
	AuthUsername   string         `json:"authUsername,omitempty"`
	Tenant         string         `json:"tenant,omitempty"`
	MemberLabel    string         `json:"memberLabel,omitempty"`
	ServesGroups   []string       `json:"servesGroups,omitempty"`
	ServesClusters []string       `json:"servesClusters,omitempty"`
}

// InstallationDTO is one installation in force (S3a §8): its fields, where
// it is, its credential's state, its subscribers, and its latest read.
type InstallationDTO struct {
	SourceDraftDTO
	// Revision is the installation's, as shown: a credential action is
	// refused once it moves
	Revision    string   `json:"revision"`
	Where       string   `json:"where"`
	Credential  string   `json:"credential"`
	Subscribers []string `json:"subscribers"`
	// the latest read: when, how many rows matched no member's scope, cut
	// at its cap, and its failure
	ReadAt    string `json:"readAt,omitempty"`
	Unmatched *int   `json:"unmatched,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	ReadError string `json:"readError,omitempty"`
}

// SubscriptionDTO is a cluster's own subscription block: an installation,
// with its scope, tenant and match over the installation's defaults, or
// off.
type SubscriptionDTO struct {
	Source string            `json:"source,omitempty"`
	Scope  string            `json:"scope,omitempty"`
	Off    bool              `json:"off,omitempty"`
	Tenant string            `json:"tenant,omitempty"`
	Match  map[string]string `json:"match,omitempty"`
}

// SubscriptionRowDTO is one running cluster's alerts (S3a §8): how they
// resolved, the scope sentence, what its own block shadows, and that
// block.
type SubscriptionRowDTO struct {
	Cluster  string   `json:"cluster"`
	Mode     string   `json:"mode"`
	Via      string   `json:"via"`
	Source   string   `json:"source,omitempty"`
	Sentence string   `json:"sentence"`
	Shadowed []string `json:"shadowed,omitempty"`
	// Block is its own subscription or off block; Legacy, that its own
	// block is a legacy one, which stays the file's
	Block  *SubscriptionDTO `json:"block,omitempty"`
	Legacy bool             `json:"legacy,omitempty"`
}

// SourcesStateDTO is Observability's alert sources (S3a §8).
type SourcesStateDTO struct {
	// Kind is the kind the state is of: alertmanager, loki or metrics
	Kind          string               `json:"kind"`
	Sentence      string               `json:"sentence"`
	Installations []InstallationDTO    `json:"installations"`
	Subscriptions []SubscriptionRowDTO `json:"subscriptions"`
	// FileDiffers: fleet.yaml's sources or subscription blocks are not the
	// ones in force ("Use the file's values" applies them)
	FileDiffers bool   `json:"fileDiffers"`
	FileError   string `json:"fileError,omitempty"`
}

// SourcesDraftDTO is an Observability draft of one kind (S3b §5): one
// installation of that kind added, edited or removed, and the clusters
// whose own block of that kind changes. No kind is Alertmanager's.
type SourcesDraftDTO struct {
	Kind          string                `json:"kind,omitempty"`
	Installation  *InstallationEditDTO  `json:"installation,omitempty"`
	Subscriptions []SubscriptionEditDTO `json:"subscriptions,omitempty"`
}

// InstallationEditDTO is one installation's change: add, edit or remove.
// A removal names it by its id alone.
type InstallationEditDTO struct {
	Op     string         `json:"op"`
	Source SourceDraftDTO `json:"source"`
}

// SubscriptionEditDTO is a cluster's own block of the draft's kind as it
// should be; nil removes it, so the cluster resolves by name, group or
// discovery (Loki: none).
type SubscriptionEditDTO struct {
	Cluster string           `json:"cluster"`
	Block   *SubscriptionDTO `json:"block"`
}

const (
	installAdd    = "add"
	installEdit   = "edit"
	installRemove = "remove"
)

// observabilityKinds are the kinds of Observability's sources, in the page's
// order (S3b §8).
var observabilityKinds = []string{config.EndpointAlertmanager, config.EndpointLoki, config.EndpointMetrics}

// kindOf is a request's kind: one of observabilityKinds, none Alertmanager's (S3a's
// requests).
func kindOf(kind string) (string, error) {
	if kind == "" {
		return config.EndpointAlertmanager, nil
	}
	for _, k := range observabilityKinds {
		if k == kind {
			return k, nil
		}
	}
	return "", fmt.Errorf("%s is not a kind of source: alertmanager, loki or metrics", wordOf(kind))
}

// kindNames are a kind's words: its section's title and its data's noun.
var kindNames = map[string]struct{ title, noun string }{
	config.EndpointAlertmanager: {"Alerts", "alerts"},
	config.EndpointLoki:         {"Logs", "logs"},
	config.EndpointMetrics:      {"Metrics", "metrics"},
}

// GetSources is a kind's sources in force, each cluster's subscription of
// that kind, and whether the file says otherwise.
func (s *SourcesService) GetSources(kind string) SourcesStateDTO {
	out := SourcesStateDTO{Kind: kind, Installations: []InstallationDTO{}, Subscriptions: []SubscriptionRowDTO{}}
	k, err := kindOf(kind)
	if err != nil {
		out.FileError = err.Error()
		return out
	}
	kind, out.Kind = k, k
	a := s.d.Active()
	if a == nil {
		out.Sentence = kindNames[kind].title + ": no installations"
		return out
	}
	sources := a.Sources()
	running := s.cs.fleetClusters()
	res, err := config.Resolve(kind, sources, running)
	if err != nil {
		res = map[string]config.AMResolution{}
	}
	byID := map[string]config.SourceConfig{}
	for _, src := range sources {
		byID[src.ID] = src
	}
	subscribers := map[string][]string{}
	for _, cl := range running {
		r := res[cl.Name]
		block := cl.Block(kind)
		row := SubscriptionRowDTO{Cluster: cl.Name, Mode: r.Mode, Via: r.Via, Source: r.Source, Sentence: scopeSentence(kind, r, byID[r.Source], block), Shadowed: r.Shadowed}
		if block != nil {
			if block.Source != "" || block.Off {
				row.Block = subscriptionOf(block)
			} else {
				row.Legacy = true
			}
		}
		if r.Mode == config.AMSubscribed {
			subscribers[r.Source] = append(subscribers[r.Source], cl.Name)
		}
		out.Subscriptions = append(out.Subscriptions, row)
	}
	for _, src := range ofKind(sources, kind) {
		_, rev, _ := a.Installation(src.ID)
		in := InstallationDTO{SourceDraftDTO: sourceDraftOf(src), Revision: strconv.FormatUint(rev, 10), Where: whereOf(src),
			Credential: s.credentialState(src), Subscribers: append([]string{}, subscribers[src.ID]...)}
		// an Alertmanager installation's latest read: a Loki or metrics one
		// has none, read only for its members' views
		if s.d.Read != nil {
			if r, ok := s.d.Read(src.ID); ok {
				n := r.Unmatched
				in.ReadAt, in.Unmatched, in.Truncated, in.ReadError = r.At.UTC().Format(time.RFC3339), &n, r.Truncated, r.Err
			}
		}
		out.Installations = append(out.Installations, in)
	}
	out.Sentence = sourcesSentence(kind, res, running)
	if raw, _, err := config.ReadFleetVersioned(s.path); err != nil {
		out.FileError = "fleet.yaml cannot be read: " + config.DisplayError(err)
	} else if cfg, err := config.ParseFleet(raw); err != nil {
		out.FileError = "fleet.yaml does not load: " + config.DisplayError(err)
	} else {
		srcs, blocks := fileTerms(kind, cfg, running)
		out.FileDiffers = !reflect.DeepEqual(normSources(ofKind(srcs, kind)), normSources(ofKind(sources, kind))) || len(blocks) > 0
	}
	return out
}

// credentialState is an installation's credential, in words: a hosted one
// is read through its host and needs none.
func (s *SourcesService) credentialState(src config.SourceConfig) string {
	switch {
	case src.Endpoint == "":
		return "not needed: read through its host"
	case src.Auth == nil:
		return "not needed: no auth configured"
	case s.d.Store == nil || s.d.Revs == nil:
		return "unknown: no keychain"
	}
	revKey := endpointcred.SourceAccount(src.ID)
	cred := endpointcred.Resolve(s.d.Store, endpointcred.Input{Cluster: revKey, Kind: src.Kind, Account: revKey,
		Spec: config.EndpointConfig{Endpoint: src.Endpoint, Auth: src.Auth}, Revoked: s.d.Revs.Revoked(revKey, src.Kind)})
	if cred.Usable() {
		return "stored"
	}
	if cred.Why != "" {
		return cred.Why
	}
	return string(cred.State)
}

// sourcesCand is what a plan is of: a draft, or a kind's file values.
type sourcesCand struct {
	Kind  string           `json:"kind"`
	Draft *SourcesDraftDTO `json:"draft,omitempty"`
	// Of is the kind of source the file's values are adopted for
	Of string `json:"of,omitempty"`
}

// sourcesPlanned is a plan with what its Apply writes and publishes.
type sourcesPlanned struct {
	plan PlanDTO
	edit config.SourcesEdit
	// runSources are the installations to put in force; runBlocks, the
	// running clusters' own blocks to take, of the plan's kind
	runSources []config.SourceConfig
	runBlocks  map[config.BlockRef]*config.EndpointConfig
}

func sourcesRefused(kind, why string) PlanDTO {
	return PlanDTO{Section: "observability", Kind: kind, Refused: why, Runtime: map[string]string{}, Changes: []PlanChangeDTO{}, Ends: []PlanWorkDTO{}, Continues: []PlanWorkDTO{}}
}

// PlanSources plans a draft from what runs.
func (s *SourcesService) PlanSources(d SourcesDraftDTO) PlanDTO {
	return s.plan(sourcesCand{Kind: planSave, Draft: &d}).plan
}

// PlanSourcesFromFile plans "Use the file's values" for a kind (S3a §5,
// S3b §5): the file's installations of that kind and its clusters'
// subscription blocks of that kind, nothing else.
func (s *SourcesService) PlanSourcesFromFile(kind string) PlanDTO {
	return s.plan(sourcesCand{Kind: planFile, Of: kind}).plan
}

func (s *SourcesService) plan(c sourcesCand) sourcesPlanned {
	refuse := func(why string) sourcesPlanned { return sourcesPlanned{plan: sourcesRefused(c.Kind, why)} }
	raw, version, err := config.ReadFleetVersioned(s.path)
	if err != nil {
		return refuse("fleet.yaml cannot be read, so nothing was planned: " + config.DisplayError(err))
	}
	file, err := config.ParseFleet(raw)
	if err != nil {
		return refuse("fleet.yaml does not load, so nothing was planned: " + config.DisplayError(err))
	}
	a := s.d.Active()
	if a == nil {
		return refuse("the installations in force are not available")
	}
	kind := c.Of
	if c.Draft != nil {
		kind = c.Draft.Kind
	}
	kind, err = kindOf(kind)
	if err != nil {
		return refuse(err.Error())
	}
	key := kind // a kind's block's key in an entry
	active := a.Sources()
	activeRev := a.Revision()
	running := s.cs.fleetClusters()
	runRev := RunningRevision(s.cs)

	out := sourcesPlanned{runSources: cloneSourceList(active), runBlocks: map[config.BlockRef]*config.EndpointConfig{}}
	runClusters := append([]config.ClusterConfig(nil), running...)
	fileHas := map[string]*config.ClusterConfig{}
	for i := range file.Clusters {
		fileHas[file.Clusters[i].Name] = &file.Clusters[i]
	}
	runIndex := map[string]int{}
	for i, cl := range runClusters {
		runIndex[cl.Name] = i
	}
	var notes []PlanChangeDTO
	switch c.Kind {
	case planSave:
		d := c.Draft
		if d == nil || d.Installation == nil && len(d.Subscriptions) == 0 {
			return refuse("the draft changes nothing")
		}
		if e := d.Installation; e != nil {
			fileSources := cloneSourceList(file.Sources)
			id := e.Source.ID
			activeAt, fileAt := sourceIndex(active, id), sourceIndex(fileSources, id)
			src := sourceOf(kind, e.Source)
			switch e.Op {
			case installAdd:
				if activeAt >= 0 || fileAt >= 0 {
					return refuse("an installation " + shownInstallation(id) + " exists already")
				}
				out.runSources = append(out.runSources, src)
				fileSources = append(fileSources, src)
			case installEdit:
				if activeAt < 0 {
					return refuse(shownInstallation(id) + " is not in force: add it instead")
				}
				if k := active[activeAt].Kind; k != kind {
					return refuse(shownInstallation(id) + " is a " + k + " installation: its kind does not change; remove it and add another")
				}
				if fileAt < 0 {
					return refuse("fleet.yaml no longer has " + shownInstallation(id) + ": use the file's values first, or add it again")
				}
				if !reflect.DeepEqual(normSource(fileSources[fileAt]), normSource(active[activeAt])) {
					notes = append(notes, PlanChangeDTO{ID: "file.sources." + id, Label: "fleet.yaml's " + shownInstallation(id), From: "edited in the file, not in force", To: "replaced by this edit"})
				}
				out.runSources[activeAt] = src
				fileSources[fileAt] = src
			case installRemove:
				if activeAt < 0 {
					return refuse(shownInstallation(id) + " is not in force")
				}
				if k := active[activeAt].Kind; k != kind {
					return refuse(shownInstallation(id) + " is a " + k + " installation, removed from its own panel")
				}
				out.runSources = append(out.runSources[:activeAt], out.runSources[activeAt+1:]...)
				if fileAt >= 0 {
					fileSources = append(fileSources[:fileAt], fileSources[fileAt+1:]...)
				}
			default:
				return refuse("an installation is added, edited or removed")
			}
			out.edit.Sources = &fileSources
		}
		seen := map[string]bool{}
		for _, se := range d.Subscriptions {
			if seen[se.Cluster] {
				return refuse(se.Cluster + " is edited twice")
			}
			seen[se.Cluster] = true
			if fileHas[se.Cluster] == nil {
				return refuse(se.Cluster + " is not in fleet.yaml")
			}
			ref := config.BlockRef{Cluster: se.Cluster, Kind: kind}
			block := blockOf(se.Block)
			if out.edit.Blocks == nil {
				out.edit.Blocks = map[config.BlockRef]*config.EndpointConfig{}
			}
			out.edit.Blocks[ref] = block
			if i, ok := runIndex[se.Cluster]; ok {
				runClusters[i].SetBlock(kind, block)
				out.runBlocks[ref] = block
			}
		}
	case planFile:
		srcs, blocks := fileTerms(kind, file, running)
		if reflect.DeepEqual(normSources(ofKind(srcs, kind)), normSources(ofKind(active, kind))) && len(blocks) == 0 {
			return refuse("fleet.yaml's " + kindNames[kind].noun + " sources are the ones in force")
		}
		// the file's installations of this kind; the other kinds' in force
		// stay as they are, each adopted from its own panel
		out.runSources = append(ofKind(srcs, kind), notOfKind(active, kind)...)
		for ref, b := range blocks {
			runClusters[runIndex[ref.Cluster]].SetBlock(kind, b)
			out.runBlocks[ref] = b
		}
	default:
		return refuse("unknown plan")
	}

	// the runtime candidate resolves, every kind (S3a §5, step 2; S3b §2.2)
	if _, err := config.CheckSources(out.runSources, runClusters); err != nil {
		return refuse("with this change, the installations do not resolve, so nothing was planned: " + config.DisplayError(err))
	}
	// the disk candidate loads strictly, resolves and changes only the
	// plan's blocks
	writes := out.edit.Sources != nil || len(out.edit.Blocks) > 0
	if writes {
		if _, _, err := config.BuildSourcesCandidate(raw, out.edit); err != nil {
			return refuse(config.DisplayError(err))
		}
	}

	p := PlanDTO{Section: "observability", Kind: c.Kind, FileVersion: version, Writes: writes, Activates: true,
		Changes: []PlanChangeDTO{}, Ends: []PlanWorkDTO{}, Continues: []PlanWorkDTO{}}
	p.Changes = append(p.Changes, installationChanges(active, out.runSources)...)
	p.Changes = append(p.Changes, notes...)
	if c.Kind == planSave {
		for _, se := range c.Draft.Subscriptions {
			from := fileHas[se.Cluster].Block(kind)
			label := se.Cluster + "'s " + key + " block"
			if i, ok := runIndex[se.Cluster]; ok {
				from = running[i].Block(kind)
			} else {
				label += " (not running: checked for its later connect)"
			}
			p.Changes = append(p.Changes, PlanChangeDTO{ID: "clusters." + se.Cluster + "." + key, Label: label, From: blockWords(kind, from), To: blockWords(kind, blockOf(se.Block))})
		}
	} else {
		refs := make([]config.BlockRef, 0, len(out.runBlocks))
		for ref := range out.runBlocks {
			refs = append(refs, ref)
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].Cluster < refs[j].Cluster })
		for _, ref := range refs {
			p.Changes = append(p.Changes, PlanChangeDTO{ID: "clusters." + ref.Cluster + "." + key, Label: ref.Cluster + "'s " + key + " block",
				From: blockWords(kind, running[runIndex[ref.Cluster]].Block(kind)), To: blockWords(kind, out.runBlocks[ref])})
		}
	}
	// for every kind, the members whose data moves, each with its scope
	// sentence before and after; and every member whose installation or
	// subscription changes re-keys. Only the plan's kind can move, and a
	// kind that does not move ends nothing (§13)
	oldByID, newByID := sourcesByID(active), sourcesByID(out.runSources)
	moved := map[string][]string{}
	for _, k := range observabilityKinds {
		before, err := config.Resolve(k, active, running)
		if err != nil {
			before = map[string]config.AMResolution{}
		}
		after, err := config.Resolve(k, out.runSources, runClusters)
		if err != nil {
			return refuse("with this change, the installations do not resolve, so nothing was planned: " + config.DisplayError(err))
		}
		for i, cl := range running {
			rb, ra := before[cl.Name], after[cl.Name]
			sb, sa := scopeSentence(k, rb, oldByID[rb.Source], cl.Block(k)), scopeSentence(k, ra, newByID[ra.Source], runClusters[i].Block(k))
			name := kindNames[k]
			if sb != sa {
				p.Changes = append(p.Changes, PlanChangeDTO{ID: name.noun + "." + cl.Name, Label: name.title + " for " + cl.Name, From: sb, To: sa})
			}
			if sb != sa || rb.Mode != ra.Mode || rb.Source != ra.Source ||
				ra.Mode == config.AMSubscribed && !reflect.DeepEqual(normSource(oldByID[ra.Source]), normSource(newByID[ra.Source])) {
				moved[k] = append(moved[k], cl.Name)
				p.Ends = append(p.Ends, PlanWorkDTO{ID: name.noun + ":" + cl.Name, Kind: name.noun, Cluster: cl.Name, What: movedWords(k, cl.Name)})
			}
		}
	}
	if len(p.Changes) == 0 {
		return refuse("the draft changes nothing")
	}
	if s.d.Work != nil && len(moved) > 0 {
		p.Ends = append(p.Ends, s.d.Work(moved)...)
	}
	p.Runtime = map[string]string{"running": strconv.FormatUint(runRev, 10), "sources": strconv.FormatUint(activeRev, 10), "work": workVersion(p.Ends)}
	bind, _ := json.Marshal(c)
	seal(&p, string(bind))
	s.cache.put(p.Digest, c)
	out.plan = p
	return out
}

// movedWords is what ends for a member whose terms of a kind move (S3b
// §5): its alerts are read again, its log views start again, or its
// metrics are read again with their cache re-keyed.
func movedWords(kind, cluster string) string {
	switch kind {
	case config.EndpointLoki:
		return cluster + "'s log views start again under the new terms"
	case config.EndpointMetrics:
		return cluster + "'s metrics are read again under the new terms: its cached readings are dropped"
	}
	return cluster + "'s alerts are read again under the new terms"
}

// ApplySources applies the plan with digest as one transaction under the
// fleet Apply lock (S3a §5): re-planned from what runs, refused with the
// new plan when anything moved; the disk candidate written once; then the
// running clusters take the approved blocks and the installations and
// subscriptions are put in force, from the resolution already checked.
func (s *SourcesService) ApplySources(digest string) ApplyResultDTO {
	defer FleetApplyLock(s.cs)()
	// no alert dependency is recorded from the final work list until the
	// publication: one made meanwhile is checked against the new terms
	// (Codex's review of S3a-4a, R6)
	alertTerms.Lock()
	defer alertTerms.Unlock()
	v, ok := s.cache.get(digest)
	if !ok {
		return ApplyResultDTO{Error: "This plan expired or was already applied: review the changes again."}
	}
	c := v.(sourcesCand)
	re := s.plan(c)
	switch {
	case re.plan.Digest != digest:
		return ApplyResultDTO{Error: "Something changed since this was planned: review the new plan.", Plan: &re.plan}
	case re.plan.Refused != "":
		return ApplyResultDTO{Error: re.plan.Refused}
	}
	if re.plan.Writes {
		if _, err := config.EditSourcesAt(s.path, re.plan.FileVersion, re.edit); err != nil {
			if errors.Is(err, config.ErrFileChanged) {
				np := s.plan(c)
				return ApplyResultDTO{Error: "fleet.yaml changed while this was applied, so nothing was written: review the new plan.", Plan: &np.plan}
			}
			return ApplyResultDTO{Error: config.DisplayError(err)}
		}
	} else if err := s.cs.fileIs(re.plan.FileVersion); err != nil {
		// "Use the file's values" adopts the file as it is at the
		// publication, never as it was read (R1)
		np := s.plan(c)
		return ApplyResultDTO{Error: "fleet.yaml changed while this was applied, so nothing was put in force: review the new plan.", Plan: &np.plan}
	}
	s.cache.consume(digest)
	for ref, block := range re.runBlocks {
		SetRunningBlock(s.cs, ref.Cluster, ref.Kind, block)
	}
	if s.d.Publish != nil {
		if err := s.d.Publish(re.runSources, s.cs.fleetClusters()); err != nil {
			return ApplyResultDTO{Error: "fleet.yaml was written, but the change could not be put in force: " + config.DisplayError(err) + ". Restart Klyx to load it."}
		}
	}
	return ApplyResultDTO{OK: true}
}

// fileTerms are the file's sources and, for each running cluster, its
// subscription or off block of a kind where the file's differs from the
// running one (S3a §5, "Use the file's values"). A legacy block in the file
// stays C5's to adopt: a running subscription whose file block became
// legacy keeps running as it is.
func fileTerms(kind string, file *config.Config, running []config.ClusterConfig) ([]config.SourceConfig, map[config.BlockRef]*config.EndpointConfig) {
	inFile := map[string]config.ClusterConfig{}
	for _, cl := range file.Clusters {
		inFile[cl.Name] = cl
	}
	isSub := func(b *config.EndpointConfig) bool { return b != nil && (b.Source != "" || b.Off) }
	blocks := map[config.BlockRef]*config.EndpointConfig{}
	for _, cl := range running {
		fc, ok := inFile[cl.Name]
		if !ok {
			continue
		}
		ref := config.BlockRef{Cluster: cl.Name, Kind: kind}
		fb, rb := fc.Block(kind), cl.Block(kind)
		switch {
		case isSub(fb) && !reflect.DeepEqual(subscriptionOf(fb), subscriptionOf(rb)):
			blocks[ref] = blockOf(subscriptionOf(fb))
		case fb == nil && isSub(rb):
			blocks[ref] = nil
		}
	}
	return cloneSourceList(file.Sources), blocks
}

// scopeSentence is where a cluster's data of a kind comes from (S3a §3.5,
// S3b §3.4), in words; a long installation id is named by its length.
// block is the cluster's own block, which a legacy mode's sentence says.
func scopeSentence(kind string, r config.AMResolution, src config.SourceConfig, block *config.EndpointConfig) string {
	switch r.Mode {
	case config.AMOff:
		return "off"
	case config.AMNone:
		return "none"
	case config.AMSubscribed:
	default:
		return legacySentence(kind, r.Mode, block)
	}
	head := "from " + shownInstallation(r.Source)
	if src.Host != "" {
		head += ", hosted by " + src.Host
	} else if o, err := safehttp.OriginOf(src.Endpoint); err == nil {
		head += " at " + o
	}
	parts := []string{head}
	if r.Tenant != "" {
		parts = append(parts, "tenant "+r.Tenant)
	}
	if r.ScopeTenant {
		parts = append(parts, "everything in its tenant")
	} else {
		words := make([]string, 0, len(r.Match))
		for k, v := range r.Match {
			words = append(words, k+"="+v)
		}
		sort.Strings(words)
		parts = append(parts, "filtered by "+strings.Join(words, ", "))
	}
	return strings.Join(parts, ", ")
}

// legacySentence is the scope sentence of a cluster's own block of a
// kind, or of discovery in it (S3a §3.5), as fleet.yaml says it: where,
// the tenant, and the filter, or that nothing is filtered. An endpoint is
// named by its origin alone.
func legacySentence(kind, mode string, b *config.EndpointConfig) string {
	var where string
	switch mode {
	case config.AMService:
		where = "from its own Service"
		if b != nil && b.ServiceRef != nil {
			r := b.ServiceRef
			where = fmt.Sprintf("from the Service %s/%s:%s on this cluster", wordOf(r.Namespace), wordOf(r.Name), wordOf(r.Port))
		}
	case config.AMDirect:
		where = "from its own endpoint"
		if b != nil {
			if o, err := safehttp.OriginOf(b.Endpoint); err == nil {
				where += " at " + o
			}
		}
	default:
		where = "discovered in this cluster"
	}
	parts := []string{where}
	if b != nil && b.Tenant != "" {
		parts = append(parts, "tenant "+b.Tenant)
	}
	if b == nil || len(b.Match) == 0 {
		// a cluster's own Loki reads every stream there as its own (S3b
		// §3.4, as its views say)
		if kind == config.EndpointLoki {
			return strings.Join(append(parts, "unfiltered: every stream there is read as this cluster's"), ", ")
		}
		return strings.Join(append(parts, "unfiltered: everything there is read as this cluster's"), ", ")
	}
	words := make([]string, 0, len(b.Match))
	for k, v := range b.Match {
		words = append(words, k+"="+v)
	}
	sort.Strings(words)
	return strings.Join(append(parts, "filtered by "+strings.Join(words, ", ")), ", ")
}

// sourcesSentence is a kind's sentence (S3a §8): "Alerts: 9 clusters from
// 2 installations, 3 discovered in themselves, 1 off"; "Logs: ..., 4
// with none".
func sourcesSentence(kind string, res map[string]config.AMResolution, running []config.ClusterConfig) string {
	subscribed, discovered, off, own, none := 0, 0, 0, 0, 0
	installs := map[string]bool{}
	for _, cl := range running {
		switch r := res[cl.Name]; r.Mode {
		case config.AMSubscribed:
			subscribed++
			installs[r.Source] = true
		case config.AMOff:
			off++
		case config.AMNone:
			none++
		case config.AMDirect, config.AMService:
			own++
		default:
			discovered++
		}
	}
	count := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.Itoa(n) + " " + many
	}
	var parts []string
	if subscribed > 0 {
		parts = append(parts, count(subscribed, "cluster", "clusters")+" from "+count(len(installs), "installation", "installations"))
	}
	if discovered > 0 {
		parts = append(parts, strconv.Itoa(discovered)+" discovered in themselves")
	}
	if own > 0 {
		parts = append(parts, strconv.Itoa(own)+" with their own endpoint or Service")
	}
	if off > 0 {
		parts = append(parts, strconv.Itoa(off)+" off")
	}
	if none > 0 {
		parts = append(parts, strconv.Itoa(none)+" with none")
	}
	title := kindNames[kind].title
	if len(parts) == 0 {
		return title + ": no clusters"
	}
	return title + ": " + strings.Join(parts, ", ")
}

// installationChanges are the installations added, edited and removed,
// each in words.
func installationChanges(from, to []config.SourceConfig) []PlanChangeDTO {
	old, now := sourcesByID(from), sourcesByID(to)
	ids := map[string]bool{}
	for id := range old {
		ids[id] = true
	}
	for id := range now {
		ids[id] = true
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)
	var out []PlanChangeDTO
	for _, id := range sorted {
		o, had := old[id]
		n, has := now[id]
		switch {
		case had && has && reflect.DeepEqual(normSource(o), normSource(n)):
		case !had:
			out = append(out, PlanChangeDTO{ID: "sources." + id, Label: "Installation " + shownInstallation(id), From: "none", To: sourceWords(n)})
		case !has:
			out = append(out, PlanChangeDTO{ID: "sources." + id, Label: "Installation " + shownInstallation(id), From: sourceWords(o), To: "removed"})
		default:
			// each material input that changed, in its own line (R4),
			// decided by the inputs themselves, never by their words: a
			// change the words cannot show is named (Codex's re-review of
			// S3a-4a, RR1)
			of, nf := fieldMap(installationFields(o)), fieldMap(installationFields(n))
			oi, ni := installationInputs(o), installationInputs(n)
			for _, f := range sourceFieldOrder {
				if oi[f] == ni[f] {
					continue
				}
				from, to := orNone(of[f]), orNone(nf[f])
				to += unshownChange(f, o, n, from == to)
				out = append(out, PlanChangeDTO{ID: "sources." + id + "." + strings.ReplaceAll(f, " ", "-"), Label: shownInstallation(id) + "'s " + f, From: from, To: to})
			}
		}
	}
	return out
}

// whereOf is where an installation is read: its Service or discovery in
// its host, or its endpoint's origin.
func whereOf(src config.SourceConfig) string {
	switch {
	case src.ServiceRef != nil:
		return fmt.Sprintf("Service %s/%s:%s in %s", src.ServiceRef.Namespace, src.ServiceRef.Name, src.ServiceRef.Port, src.Host)
	case src.Discover:
		return "discovered in " + src.Host
	}
	if o, err := safehttp.OriginOf(src.Endpoint); err == nil {
		return o
	}
	return "an endpoint"
}

// sourceFieldOrder is the order an installation's inputs are said in.
var sourceFieldOrder = []string{"name", "host", "Service", "discovery", "endpoint", "certificate", "auth", "tenant", "member label", "serves"}

// installationFields are an installation's material inputs, each in words: the
// request's target, how it authenticates, its defaults and what it serves.
// A plan says each that changes (Codex's review of S3a-4a, R4); no secret
// is among them.
func installationFields(src config.SourceConfig) [][2]string {
	var out [][2]string
	add := func(k, v string) {
		if v != "" {
			out = append(out, [2]string{k, v})
		}
	}
	add("name", src.Name)
	add("host", src.Host)
	if r := src.ServiceRef; r != nil {
		scheme := r.Scheme
		if scheme == "" {
			scheme = "http"
		}
		svc := fmt.Sprintf("%s %s/%s:%s", scheme, wordOf(r.Namespace), wordOf(r.Name), wordOf(r.Port))
		if r.Path != "" {
			svc += ", path " + pathWordOf(r.Path)
		}
		add("Service", svc)
	}
	if src.Discover {
		add("discovery", "discovered in its host")
	}
	add("endpoint", endpointWords(src.Endpoint))
	if src.TLSSkipVerify {
		add("certificate", "not verified")
	}
	if a := src.Auth; a != nil {
		if a.Username != "" {
			add("auth", a.Type+" as "+a.Username)
		} else {
			add("auth", a.Type)
		}
	}
	add("tenant", src.Tenant)
	add("member label", src.MemberLabel)
	var serves []string
	for _, sv := range src.Serves {
		if sv.Group != "" {
			serves = append(serves, "group "+wordOf(sv.Group))
		} else {
			serves = append(serves, wordOf(sv.Cluster))
		}
	}
	sort.Strings(serves)
	add("serves", strings.Join(serves, ", "))
	return out
}

// endpointWords is an endpoint as a plan says it: its origin and path; a
// query is named, never shown.
func endpointWords(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "an endpoint that does not parse"
	}
	o, err := safehttp.OriginOf(endpoint)
	if err != nil {
		return "an endpoint that does not parse"
	}
	w := o + u.EscapedPath()
	if u.RawQuery != "" {
		w += " (with a query)"
	}
	return w
}

// installationInputs are an installation's inputs as written, by field: a
// plan decides what changed from them. A field of several values is
// encoded as JSON, whose boundaries no value can forge (Codex's third
// re-review of S3a-4a, R3-1: a value holding the delimiters of a joined
// string read as two).
func installationInputs(src config.SourceConfig) map[string]string {
	m := map[string]string{"name": src.Name, "host": src.Host, "endpoint": src.Endpoint, "tenant": src.Tenant, "member label": src.MemberLabel}
	if r := src.ServiceRef; r != nil {
		m["Service"] = inputOf([]string{r.Scheme, r.Namespace, r.Name, r.Port, r.Path})
	}
	if src.Discover {
		m["discovery"] = "true"
	}
	if src.TLSSkipVerify {
		m["certificate"] = "true"
	}
	if a := src.Auth; a != nil {
		m["auth"] = inputOf([]string{a.Type, a.Username})
	}
	// what it serves as typed selectors, in any order (R7)
	serves := make([][2]string, 0, len(src.Serves))
	for _, sv := range src.Serves {
		if sv.Group != "" {
			serves = append(serves, [2]string{"group", sv.Group})
		} else {
			serves = append(serves, [2]string{"cluster", sv.Cluster})
		}
	}
	sort.Slice(serves, func(i, j int) bool {
		if serves[i][0] != serves[j][0] {
			return serves[i][0] < serves[j][0]
		}
		return serves[i][1] < serves[j][1]
	})
	m["serves"] = inputOf(serves)
	return m
}

func inputOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// strings and their arrays always encode
		panic(err)
	}
	return string(b)
}

// plainWord and plainPath are values a plan shows bare: nothing in them can
// be read as the words around them. Any other value is quoted, so two
// different inputs never read the same (Codex's second re-review of
// S3a-4a, R2-1: a group "blue, group green" read as two groups).
var (
	plainWord = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*$`)
	plainPath = regexp.MustCompile(`^/?[A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*/?$`)
)

func wordOf(v string) string {
	if plainWord.MatchString(v) {
		return v
	}
	return strconv.Quote(v)
}

func pathWordOf(v string) string {
	if plainPath.MatchString(v) {
		return v
	}
	return strconv.Quote(v)
}

// unshownChange names what changed in a field that its words do not show,
// decided from the inputs: an endpoint's query or fragment, whose contents
// are never shown, or only how it is written; a Service's scheme written
// out as its default. same is that its words are the same.
func unshownChange(field string, was, now config.SourceConfig, same bool) string {
	switch field {
	case "endpoint":
		return endpointUnshown(was.Endpoint, now.Endpoint, same)
	case "Service":
		if same && sameServiceEffect(was.ServiceRef, now.ServiceRef) {
			return ": written differently, with the same effect"
		}
	}
	if same {
		// the words of every other field show its inputs: kept for honesty,
		// never as a claim that nothing changed
		return ": changed in a way these words cannot show"
	}
	return ""
}

// sameServiceEffect is two Services that differ only in a scheme left to
// its default and written out.
func sameServiceEffect(a, b *config.MetricsServiceRef) bool {
	if a == nil || b == nil {
		return false
	}
	scheme := func(r *config.MetricsServiceRef) string {
		if r.Scheme == "" {
			return "http"
		}
		return r.Scheme
	}
	return a.Namespace == b.Namespace && a.Name == b.Name && a.Port == b.Port && a.Path == b.Path && scheme(a) == scheme(b)
}

// endpointUnshown names what changed in an endpoint that its words do not
// show.
func endpointUnshown(was, now string, same bool) string {
	wu, werr := url.Parse(was)
	nu, nerr := url.Parse(now)
	if werr != nil || nerr != nil {
		if same {
			return ": changed, and is not shown"
		}
		return ""
	}
	var parts []string
	if wu.RawQuery != "" && nu.RawQuery != "" && wu.RawQuery != nu.RawQuery {
		parts = append(parts, "query")
	}
	if wu.EscapedFragment() != nu.EscapedFragment() {
		parts = append(parts, "fragment")
	}
	switch {
	case len(parts) == 2:
		return ": its query and fragment changed, and are not shown"
	case len(parts) == 1:
		return ": its " + parts[0] + " changed, and is not shown"
	case same:
		return ": written differently, to the same origin and path"
	}
	return ""
}

func fieldMap(fields [][2]string) map[string]string {
	m := map[string]string{}
	for _, f := range fields {
		m[f[0]] = f[1]
	}
	return m
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// sourceWords is an installation in words: every input, in order.
func sourceWords(src config.SourceConfig) string {
	var parts []string
	for _, f := range installationFields(src) {
		parts = append(parts, f[0]+" "+f[1])
	}
	return strings.Join(parts, "; ")
}

// blockWords is a cluster's own block of a kind in words; Loki is never
// discovered.
func blockWords(kind string, b *config.EndpointConfig) string {
	switch {
	case b == nil && kind == config.EndpointLoki:
		return "none: by name or group"
	case b == nil:
		return "none: by name, group or discovery"
	case b.Off:
		return "off"
	case b.Source == "" && b.Endpoint != "":
		return "its own endpoint"
	case b.Source == "" && kind == config.EndpointLoki:
		return "its own Service"
	case b.Source == "":
		return "its own Service or discovery"
	}
	parts := []string{"subscribed to " + shownInstallation(b.Source)}
	if b.Scope == config.ScopeTenant {
		parts = append(parts, "everything in its tenant")
	}
	if b.Tenant != "" {
		parts = append(parts, "tenant "+b.Tenant)
	}
	if len(b.Match) > 0 {
		words := make([]string, 0, len(b.Match))
		for k, v := range b.Match {
			words = append(words, k+"="+v)
		}
		sort.Strings(words)
		parts = append(parts, "match "+strings.Join(words, ", "))
	}
	return strings.Join(parts, ", ")
}

// sourceOf is a draft's installation, of the draft's kind.
func sourceOf(kind string, d SourceDraftDTO) config.SourceConfig {
	src := config.SourceConfig{ID: d.ID, Kind: kind, Name: d.Name, Host: d.Host, Discover: d.Discover,
		Endpoint: d.Endpoint, TLSSkipVerify: d.TLSSkipVerify, Tenant: d.Tenant, MemberLabel: d.MemberLabel}
	if r := d.ServiceRef; r != nil {
		src.ServiceRef = &config.MetricsServiceRef{Namespace: r.Namespace, Name: r.Name, Port: r.Port, Scheme: r.Scheme, Path: r.Path}
	}
	if d.AuthType != "" {
		src.Auth = &config.EndpointAuth{Type: d.AuthType, Username: d.AuthUsername}
	}
	for _, g := range d.ServesGroups {
		src.Serves = append(src.Serves, config.ServeConfig{Group: g})
	}
	for _, c := range d.ServesClusters {
		src.Serves = append(src.Serves, config.ServeConfig{Cluster: c})
	}
	return src
}

func sourceDraftOf(src config.SourceConfig) SourceDraftDTO {
	d := SourceDraftDTO{ID: src.ID, Name: src.Name, Host: src.Host, Discover: src.Discover, Endpoint: src.Endpoint,
		TLSSkipVerify: src.TLSSkipVerify, Tenant: src.Tenant, MemberLabel: src.MemberLabel}
	if r := src.ServiceRef; r != nil {
		d.ServiceRef = &ServiceRefDTO{Namespace: r.Namespace, Name: r.Name, Port: r.Port, Scheme: r.Scheme, Path: r.Path}
	}
	if src.Auth != nil {
		d.AuthType, d.AuthUsername = src.Auth.Type, src.Auth.Username
	}
	for _, sv := range src.Serves {
		if sv.Group != "" {
			d.ServesGroups = append(d.ServesGroups, sv.Group)
		} else {
			d.ServesClusters = append(d.ServesClusters, sv.Cluster)
		}
	}
	return d
}

func subscriptionOf(b *config.EndpointConfig) *SubscriptionDTO {
	if b == nil || b.Source == "" && !b.Off {
		return nil
	}
	return &SubscriptionDTO{Source: b.Source, Scope: b.Scope, Off: b.Off, Tenant: b.Tenant, Match: copyTags(b.Match)}
}

func blockOf(d *SubscriptionDTO) *config.EndpointConfig {
	if d == nil {
		return nil
	}
	if d.Off {
		return &config.EndpointConfig{Off: true}
	}
	b := &config.EndpointConfig{Source: d.Source, Scope: d.Scope, Tenant: d.Tenant}
	if len(d.Match) > 0 {
		b.Match = copyTags(d.Match)
	}
	return b
}

// normSource is an installation for a comparison: its serves in order of
// kind then name, nil and empty alike.
func normSource(s config.SourceConfig) config.SourceConfig {
	n := s
	n.Serves = append([]config.ServeConfig(nil), s.Serves...)
	sort.Slice(n.Serves, func(i, j int) bool {
		if n.Serves[i].Group != n.Serves[j].Group {
			return n.Serves[i].Group < n.Serves[j].Group
		}
		return n.Serves[i].Cluster < n.Serves[j].Cluster
	})
	if len(n.Serves) == 0 {
		n.Serves = nil
	}
	return n
}

// normSources is installations for a comparison, in order of id: their
// order in the file means nothing (Codex's review of S3a-4a, R7).
func normSources(list []config.SourceConfig) []config.SourceConfig {
	out := make([]config.SourceConfig, 0, len(list))
	for _, s := range list {
		out = append(out, normSource(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ofKind and notOfKind are the installations of a kind, and the others,
// in their order.
func ofKind(list []config.SourceConfig, kind string) []config.SourceConfig {
	var out []config.SourceConfig
	for _, s := range list {
		if s.Kind == kind {
			out = append(out, s)
		}
	}
	return out
}

func notOfKind(list []config.SourceConfig, kind string) []config.SourceConfig {
	var out []config.SourceConfig
	for _, s := range list {
		if s.Kind != kind {
			out = append(out, s)
		}
	}
	return out
}

func cloneSourceList(list []config.SourceConfig) []config.SourceConfig {
	return append([]config.SourceConfig{}, list...)
}

func sourceIndex(list []config.SourceConfig, id string) int {
	for i, s := range list {
		if s.ID == id {
			return i
		}
	}
	return -1
}

func sourcesByID(list []config.SourceConfig) map[string]config.SourceConfig {
	out := map[string]config.SourceConfig{}
	for _, s := range list {
		out[s.ID] = s
	}
	return out
}
