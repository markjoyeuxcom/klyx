package appbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
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

// Settings S3a §5, §8: Observability's alert sources - the installations
// in force and each cluster's subscription - planned from what runs and
// applied as one transaction under the fleet Apply lock. A plan has two
// candidates: the file with the plan's edits, which is written; and the
// running fleet with the installations in force and exactly the terms the
// plan approves, which is resolved and published. Whatever else differs in
// the file is neither adopted nor published.

// SourcesDeps are what the sources service reads and publishes through.
type SourcesDeps struct {
	// Active are the installations and subscriptions in force
	Active func() *fleet.AlertSources
	// Publish puts installations and the running fleet's subscriptions in
	// force (the registry's PublishSources)
	Publish func(sources []config.SourceConfig, running []config.ClusterConfig) error
	// AlertWork is the AI work holding alerts read on clusters
	AlertWork func(clusters []string) []PlanWorkDTO
	// Read is an installation's latest read
	Read func(id string) (fleet.InstallationRead, bool)
	// Store and Revs say an installation's credential state
	Store secretstore.Store
	Revs  *endpointcred.Registry
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
	Sentence      string               `json:"sentence"`
	Installations []InstallationDTO    `json:"installations"`
	Subscriptions []SubscriptionRowDTO `json:"subscriptions"`
	// FileDiffers: fleet.yaml's sources or subscription blocks are not the
	// ones in force ("Use the file's values" applies them)
	FileDiffers bool   `json:"fileDiffers"`
	FileError   string `json:"fileError,omitempty"`
}

// SourcesDraftDTO is an Observability draft: one installation added,
// edited or removed, and the clusters whose own block changes.
type SourcesDraftDTO struct {
	Installation  *InstallationEditDTO  `json:"installation,omitempty"`
	Subscriptions []SubscriptionEditDTO `json:"subscriptions,omitempty"`
}

// InstallationEditDTO is one installation's change: add, edit or remove.
// A removal names it by its id alone.
type InstallationEditDTO struct {
	Op     string         `json:"op"`
	Source SourceDraftDTO `json:"source"`
}

// SubscriptionEditDTO is a cluster's own block as it should be; nil
// removes it, so the cluster resolves by name, group or discovery.
type SubscriptionEditDTO struct {
	Cluster string           `json:"cluster"`
	Block   *SubscriptionDTO `json:"block"`
}

const (
	installAdd    = "add"
	installEdit   = "edit"
	installRemove = "remove"
)

// GetSources is the alert sources in force, each cluster's subscription,
// and whether the file says otherwise.
func (s *SourcesService) GetSources() SourcesStateDTO {
	out := SourcesStateDTO{Installations: []InstallationDTO{}, Subscriptions: []SubscriptionRowDTO{}}
	a := s.d.Active()
	if a == nil {
		out.Sentence = "Alerts: no installations"
		return out
	}
	sources := a.Sources()
	running := s.cs.fleetClusters()
	res, err := config.ResolveAlertmanager(sources, running)
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
		row := SubscriptionRowDTO{Cluster: cl.Name, Mode: r.Mode, Via: r.Via, Source: r.Source, Sentence: scopeSentence(r, byID[r.Source]), Shadowed: r.Shadowed}
		if b := cl.Alertmanager; b != nil {
			if b.Source != "" || b.Off {
				row.Block = subscriptionOf(b)
			} else {
				row.Legacy = true
			}
		}
		if r.Mode == config.AMSubscribed {
			subscribers[r.Source] = append(subscribers[r.Source], cl.Name)
		}
		out.Subscriptions = append(out.Subscriptions, row)
	}
	for _, src := range sources {
		_, rev, _ := a.Installation(src.ID)
		in := InstallationDTO{SourceDraftDTO: sourceDraftOf(src), Revision: strconv.FormatUint(rev, 10), Where: whereOf(src),
			Credential: s.credentialState(src), Subscribers: append([]string{}, subscribers[src.ID]...)}
		if s.d.Read != nil {
			if r, ok := s.d.Read(src.ID); ok {
				n := r.Unmatched
				in.ReadAt, in.Unmatched, in.Truncated, in.ReadError = r.At.UTC().Format(time.RFC3339), &n, r.Truncated, r.Err
			}
		}
		out.Installations = append(out.Installations, in)
	}
	out.Sentence = sourcesSentence(res, running)
	if raw, _, err := config.ReadFleetVersioned(s.path); err != nil {
		out.FileError = "fleet.yaml cannot be read: " + config.DisplayError(err)
	} else if cfg, err := config.ParseFleet(raw); err != nil {
		out.FileError = "fleet.yaml does not load: " + config.DisplayError(err)
	} else {
		srcs, blocks := fileTerms(cfg, running)
		out.FileDiffers = !reflect.DeepEqual(normSources(srcs), normSources(sources)) || len(blocks) > 0
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
	revKey := "source/" + src.ID
	cred := endpointcred.Resolve(s.d.Store, endpointcred.Input{Cluster: revKey, Kind: config.EndpointAlertmanager, Account: endpointcred.SourceAccount(src.ID),
		Spec: config.EndpointConfig{Endpoint: src.Endpoint, Auth: src.Auth}, Revoked: s.d.Revs.Revoked(revKey, config.EndpointAlertmanager)})
	if cred.Usable() {
		return "stored"
	}
	if cred.Why != "" {
		return cred.Why
	}
	return string(cred.State)
}

// sourcesCand is what a plan is of: a draft, or the file's values.
type sourcesCand struct {
	Kind  string           `json:"kind"`
	Draft *SourcesDraftDTO `json:"draft,omitempty"`
}

// sourcesPlanned is a plan with what its Apply writes and publishes.
type sourcesPlanned struct {
	plan PlanDTO
	edit config.SourcesEdit
	// runSources are the installations to put in force; runBlocks, the
	// running clusters' own blocks to take
	runSources []config.SourceConfig
	runBlocks  map[string]*config.EndpointConfig
}

func sourcesRefused(kind, why string) PlanDTO {
	return PlanDTO{Section: "observability", Kind: kind, Refused: why, Runtime: map[string]string{}, Changes: []PlanChangeDTO{}, Ends: []PlanWorkDTO{}, Continues: []PlanWorkDTO{}}
}

// PlanSources plans a draft from what runs.
func (s *SourcesService) PlanSources(d SourcesDraftDTO) PlanDTO {
	return s.plan(sourcesCand{Kind: planSave, Draft: &d}).plan
}

// PlanSourcesFromFile plans "Use the file's values" (S3a §5): the file's
// sources block and its clusters' subscription blocks, nothing else.
func (s *SourcesService) PlanSourcesFromFile() PlanDTO {
	return s.plan(sourcesCand{Kind: planFile}).plan
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
		return refuse("alert installations are not available")
	}
	active := a.Sources()
	activeRev := a.Revision()
	running := s.cs.fleetClusters()
	runRev := RunningRevision(s.cs)

	out := sourcesPlanned{runSources: cloneSourceList(active), runBlocks: map[string]*config.EndpointConfig{}}
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
			src := sourceOf(e.Source)
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
			fc := fileHas[se.Cluster]
			if fc == nil {
				return refuse(se.Cluster + " is not in fleet.yaml")
			}
			block := blockOf(se.Block)
			if out.edit.Blocks == nil {
				out.edit.Blocks = map[string]*config.EndpointConfig{}
			}
			out.edit.Blocks[se.Cluster] = block
			if i, ok := runIndex[se.Cluster]; ok {
				runClusters[i].Alertmanager = block
				out.runBlocks[se.Cluster] = block
			}
		}
	case planFile:
		srcs, blocks := fileTerms(file, running)
		if reflect.DeepEqual(normSources(srcs), normSources(active)) && len(blocks) == 0 {
			return refuse("fleet.yaml's alert sources are the ones in force")
		}
		out.runSources = srcs
		for name, b := range blocks {
			runClusters[runIndex[name]].Alertmanager = b
			out.runBlocks[name] = b
		}
	default:
		return refuse("unknown plan")
	}

	// the runtime candidate resolves (S3a §5, step 2)
	after, err := config.CheckSources(out.runSources, runClusters)
	if err != nil {
		return refuse("with this change, the alert installations do not resolve, so nothing was planned: " + config.DisplayError(err))
	}
	// the disk candidate loads strictly, resolves and changes only the
	// plan's blocks
	writes := out.edit.Sources != nil || len(out.edit.Blocks) > 0
	if writes {
		if _, _, err := config.BuildSourcesCandidate(raw, out.edit); err != nil {
			return refuse(config.DisplayError(err))
		}
	}
	before, err := config.ResolveAlertmanager(active, running)
	if err != nil {
		before = map[string]config.AMResolution{}
	}

	p := PlanDTO{Section: "observability", Kind: c.Kind, FileVersion: version, Writes: writes, Activates: true,
		Changes: []PlanChangeDTO{}, Ends: []PlanWorkDTO{}, Continues: []PlanWorkDTO{}}
	p.Changes = append(p.Changes, installationChanges(active, out.runSources)...)
	p.Changes = append(p.Changes, notes...)
	if c.Kind == planSave {
		for _, se := range c.Draft.Subscriptions {
			from := fileHas[se.Cluster].Alertmanager
			label := se.Cluster + "'s alertmanager block"
			if i, ok := runIndex[se.Cluster]; ok {
				from = running[i].Alertmanager
			} else {
				label += " (not running: checked for its later connect)"
			}
			p.Changes = append(p.Changes, PlanChangeDTO{ID: "clusters." + se.Cluster + ".alertmanager", Label: label, From: blockWords(from), To: blockWords(blockOf(se.Block))})
		}
	} else {
		names := make([]string, 0, len(out.runBlocks))
		for n := range out.runBlocks {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p.Changes = append(p.Changes, PlanChangeDTO{ID: "clusters." + n + ".alertmanager", Label: n + "'s alertmanager block", From: blockWords(running[runIndex[n]].Alertmanager), To: blockWords(out.runBlocks[n])})
		}
	}
	// the members whose alerts move, each with its scope sentence before
	// and after; and every member whose installation or subscription
	// changes re-keys
	oldByID, newByID := sourcesByID(active), sourcesByID(out.runSources)
	var affected []string
	for _, cl := range running {
		rb, ra := before[cl.Name], after[cl.Name]
		sb, sa := scopeSentence(rb, oldByID[rb.Source]), scopeSentence(ra, newByID[ra.Source])
		if sb != sa {
			p.Changes = append(p.Changes, PlanChangeDTO{ID: "alerts." + cl.Name, Label: "Alerts for " + cl.Name, From: sb, To: sa})
		}
		moved := sb != sa || rb.Mode != ra.Mode || rb.Source != ra.Source ||
			ra.Mode == config.AMSubscribed && !reflect.DeepEqual(normSource(oldByID[ra.Source]), normSource(newByID[ra.Source]))
		if moved {
			affected = append(affected, cl.Name)
			p.Ends = append(p.Ends, PlanWorkDTO{ID: "alerts:" + cl.Name, Kind: "alerts", Cluster: cl.Name, What: cl.Name + "'s alerts are read again under the new terms"})
		}
	}
	if len(p.Changes) == 0 {
		return refuse("the draft changes nothing")
	}
	if s.d.AlertWork != nil && len(affected) > 0 {
		p.Ends = append(p.Ends, s.d.AlertWork(affected)...)
	}
	p.Runtime = map[string]string{"running": strconv.FormatUint(runRev, 10), "sources": strconv.FormatUint(activeRev, 10), "work": workVersion(p.Ends)}
	bind, _ := json.Marshal(c)
	seal(&p, string(bind))
	s.cache.put(p.Digest, c)
	out.plan = p
	return out
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
	for name, block := range re.runBlocks {
		SetRunningEndpoint(s.cs, name, config.EndpointAlertmanager, nil, block)
	}
	if s.d.Publish != nil {
		if err := s.d.Publish(re.runSources, s.cs.fleetClusters()); err != nil {
			return ApplyResultDTO{Error: "fleet.yaml was written, but the change could not be put in force: " + config.DisplayError(err) + ". Restart Klyx to load it."}
		}
	}
	return ApplyResultDTO{OK: true}
}

// fileTerms are the file's sources and, for each running cluster, its
// subscription or off block where the file's differs from the running one
// (S3a §5, "Use the file's values"). A legacy block in the file stays C5's
// to adopt: a running subscription whose file block became legacy keeps
// running as it is.
func fileTerms(file *config.Config, running []config.ClusterConfig) ([]config.SourceConfig, map[string]*config.EndpointConfig) {
	fileBlock := map[string]*config.EndpointConfig{}
	inFile := map[string]bool{}
	for _, cl := range file.Clusters {
		inFile[cl.Name] = true
		fileBlock[cl.Name] = cl.Alertmanager
	}
	isSub := func(b *config.EndpointConfig) bool { return b != nil && (b.Source != "" || b.Off) }
	blocks := map[string]*config.EndpointConfig{}
	for _, cl := range running {
		if !inFile[cl.Name] {
			continue
		}
		fb, rb := fileBlock[cl.Name], cl.Alertmanager
		switch {
		case isSub(fb) && !reflect.DeepEqual(subscriptionOf(fb), subscriptionOf(rb)):
			blocks[cl.Name] = blockOf(subscriptionOf(fb))
		case fb == nil && isSub(rb):
			blocks[cl.Name] = nil
		}
	}
	return cloneSourceList(file.Sources), blocks
}

// scopeSentence is where a cluster's alerts come from (S3a §3.5), in
// words; a long installation id is named by its length.
func scopeSentence(r config.AMResolution, src config.SourceConfig) string {
	switch r.Mode {
	case config.AMOff:
		return "off"
	case config.AMDirect:
		return "its own endpoint"
	case config.AMService:
		return "its own Service"
	case config.AMSubscribed:
	default:
		return "discovery in the cluster"
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

// sourcesSentence is Observability's sentence (S3a §8): "Alerts: 9
// clusters from 2 installations, 3 discovered in themselves, 1 off".
func sourcesSentence(res map[string]config.AMResolution, running []config.ClusterConfig) string {
	subscribed, discovered, off, own := 0, 0, 0, 0
	installs := map[string]bool{}
	for _, cl := range running {
		switch r := res[cl.Name]; r.Mode {
		case config.AMSubscribed:
			subscribed++
			installs[r.Source] = true
		case config.AMOff:
			off++
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
	if len(parts) == 0 {
		return "Alerts: no clusters"
	}
	return "Alerts: " + strings.Join(parts, ", ")
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
			// each material input that changed, in its own line (R4)
			of, nf := fieldMap(installationFields(o)), fieldMap(installationFields(n))
			for _, f := range sourceFieldOrder {
				if of[f] != nf[f] {
					out = append(out, PlanChangeDTO{ID: "sources." + id + "." + strings.ReplaceAll(f, " ", "-"), Label: shownInstallation(id) + "'s " + f,
						From: orNone(of[f]), To: orNone(nf[f])})
				}
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
		svc := fmt.Sprintf("%s %s/%s:%s", scheme, r.Namespace, r.Name, r.Port)
		if r.Path != "" {
			svc += ", path " + r.Path
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
			serves = append(serves, "group "+sv.Group)
		} else {
			serves = append(serves, sv.Cluster)
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

// blockWords is a cluster's own alertmanager block in words.
func blockWords(b *config.EndpointConfig) string {
	switch {
	case b == nil:
		return "none: by name, group or discovery"
	case b.Off:
		return "off"
	case b.Source == "":
		return "its own " + map[bool]string{true: "endpoint", false: "Service or discovery"}[b.Endpoint != ""]
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

func sourceOf(d SourceDraftDTO) config.SourceConfig {
	src := config.SourceConfig{ID: d.ID, Kind: config.SourceKindAlertmanager, Name: d.Name, Host: d.Host, Discover: d.Discover,
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
