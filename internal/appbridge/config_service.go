package appbridge

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/policy"
)

// FleetClusterDTO is one configured fleet entry as Settings shows it.
type FleetClusterDTO struct {
	Name     string `json:"name"`
	Context  string `json:"context"`
	Env      string `json:"env"`
	Region   string `json:"region"`
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Group    string `json:"group"`
	// Protected is the EFFECTIVE protection the backend guard enforces
	// (explicit flag OR a prd/prod/production token); ProtectedInferred marks
	// the token-inferred case so Settings can say why a cluster is locked.
	Protected         bool `json:"protected"`
	ProtectedInferred bool `json:"protectedInferred"`
	HasMetrics        bool `json:"hasMetrics"`
	// Drift says how fleet.yaml now differs from this running entry, which
	// "Use the file's values" plans (Settings review slice 0, S2 §4.6); ""
	// when they agree.
	Drift string `json:"drift,omitempty"`
	// ProtectedBy names the input that decides protection (S2 §4.3), ""
	// when it is off.
	ProtectedBy string `json:"protectedBy,omitempty"`
	// Held: saved, not connected yet, or a connection that failed to build,
	// with why (S2 §4.5); "" while active. CanRevert and CanRetry say which
	// plans it offers.
	Held      string `json:"held,omitempty"`
	CanRevert bool   `json:"canRevert,omitempty"`
	CanRetry  bool   `json:"canRetry,omitempty"`
}

// KubeContextDTO is one kubeconfig context with its fleet membership.
type KubeContextDTO struct {
	Name    string `json:"name"`
	InFleet bool   `json:"inFleet"`
}

// FleetConfigDTO is the Settings payload: the loaded fleet config plus a FRESH
// kubeconfig scan (re-read on every call, so a context added to ~/.kube/config
// after startup shows up without restarting Klyx).
type FleetConfigDTO struct {
	Path           string            `json:"path"`
	KubeconfigPath string            `json:"kubeconfigPath"`
	Warnings       []string          `json:"warnings"`
	ScanError      string            `json:"scanError,omitempty"`
	Clusters       []FleetClusterDTO `json:"clusters"`
	Contexts       []KubeContextDTO  `json:"contexts"`
	// LoadError: fleet.yaml did not load when Klyx started, so it connected
	// no cluster (Settings review slice 0). FileError: the file does not load
	// now. FileOnly: clusters in the file that are not running, until a
	// restart.
	LoadError string   `json:"loadError,omitempty"`
	FileError string   `json:"fileError,omitempty"`
	FileOnly  []string `json:"fileOnly,omitempty"`
}

// ConfigService exposes the fleet configuration to Settings. It holds the
// RUNNING fleet (Settings S2 §2.6): the startup configuration, then each
// addition, edit and removal this session, with a revision. AddClusters
// appends to the file on disk AND adds the conn to the running registry via
// the connect seam - no restart needed. The startup config itself is never
// mutated (other services hold the same pointer).
type ConfigService struct {
	path   string
	cfg    *config.Config
	policy *policy.Registry // slice F §1; nil in tests that need none

	// applyMu is the fleet Apply lock (S2 §4.2, §4.5): it serialises every
	// change to the fleet - AddClusters end to end (fleet.yaml
	// read-modify-write and the slow connects), an endpoint activation, and
	// S2b's Applies. It is deliberately NOT mu: protection checks and the 1s
	// fleet push read the running fleet through mu, and must never wait
	// behind a cluster connect.
	applyMu sync.Mutex

	mu sync.Mutex
	// running is the running fleet, in fleet order; runningRev moves with
	// each change, and each change replaces one entry, never the list
	running    []config.ClusterConfig
	runningRev uint64

	// loadErr is why fleet.yaml did not load at startup, as shown; "" when
	// it loaded, or did not exist yet (a first run)
	loadErr string

	// fp is the fleet plans (S2 §4); nil until WireFleetPlans
	fp *fleetPlans

	// seams for tests
	kubeconfigPath func() string
	kubeContexts   func(string) ([]string, error)
	appendClusters func(string, []string) error
	connect        func(config.ClusterConfig) error // adds to the running registry
	readSnapshot   func(string) (config.Snapshot, error)
}

func NewConfigService(path string, cfg *config.Config, connect func(config.ClusterConfig) error) *ConfigService {
	var running []config.ClusterConfig
	if cfg != nil {
		running = append(running, cfg.Clusters...)
	}
	return &ConfigService{
		path:           path,
		cfg:            cfg,
		running:        running,
		kubeconfigPath: config.DefaultKubeconfigPath,
		kubeContexts:   config.KubeContexts,
		appendClusters: config.AppendClusters,
		connect:        connect,
		readSnapshot:   config.ReadSnapshot,
	}
}

// WithConfigLoadError records why fleet.yaml did not load at startup (main
// then starts with an empty fleet): Settings and the fleet's empty state say
// so, and adding clusters is refused until the file is fixed. A missing
// file is a first run, not an error.
func WithConfigLoadError(s *ConfigService, err error) *ConfigService {
	if err != nil && !config.IsMissing(err) {
		s.loadErr = config.DisplayError(err)
	}
	return s
}

// LoadError is why fleet.yaml did not load when Klyx started; "" when it
// did, or there was none yet.
func (s *ConfigService) LoadError() string { return s.loadErr }

// IsProtected reports whether the named cluster is protected, consulting the
// LIVE fleet (startup config plus this session's hot-added clusters) so a prd
// context imported mid-session is gated immediately - not left unprotected until
// a restart. This is the ProtectGuard's protection source; the guard closes over
// it in main so hot-added clusters honor the typed-confirmation invariant. An
// unknown name is treated as unprotected (the destructive verb will fail its
// own connected-cluster check).
//
// The exact fleet NAME is matched first; a kubeconfig context is only a
// fallback when no cluster carries that name. Matching name OR context in
// config order let an earlier cluster whose context happens to equal a
// protected cluster's name answer for it - fail-open on the prd guard while
// the destructive verb (which resolves by name) hit the protected cluster.
//
// Settings slice F §1: with a policy registry, a known cluster's protection
// is the registry's published value, so every reader agrees with what the
// guard admits under; the registry is seeded from this same configuration.
func (s *ConfigService) IsProtected(cluster string) bool {
	c, ok := s.clusterConfig(cluster)
	if !ok {
		return false
	}
	if s.policy != nil {
		if snap, ok := s.policy.Snapshot(c.Name); ok {
			return snap.Protected
		}
	}
	return c.IsProtected()
}

// WithPolicy publishes the running fleet's protection through reg (slice F
// §1): every running cluster is seeded, and AddClusters seeds a new one
// before it connects. Call once at startup, before any cluster connects.
func WithPolicy(s *ConfigService, reg *policy.Registry) *ConfigService {
	s.policy = reg
	for _, c := range s.fleetClusters() {
		reg.Seed(c.Name, c.IsProtected())
	}
	return s
}

// clusterConfig resolves a cluster in the live fleet: exact fleet name first,
// then kubeconfig context. Unexported on purpose - a ClusterConfig carries the
// metrics bearer token and must never cross the JS binding surface (main wires
// it in through LiveClusterConfig instead).
func (s *ConfigService) clusterConfig(cluster string) (config.ClusterConfig, bool) {
	fleet := s.fleetClusters()
	for _, c := range fleet {
		if c.Name == cluster {
			return c, true
		}
	}
	for _, c := range fleet {
		if c.Context == cluster {
			return c, true
		}
	}
	return config.ClusterConfig{}, false
}

// LiveClusterConfig returns a lookup over the LIVE fleet (startup config plus
// this session's hot-added clusters) for the FleetService, so a hot-added
// cluster's DTO carries its env/group/protection instead of zero values. A
// free function rather than a ConfigService method on purpose: exported
// methods on a bound service land in the JS binding surface.
func LiveClusterConfig(s *ConfigService) func(string) (config.ClusterConfig, bool) {
	return s.clusterConfig
}

// LiveFleetClusters returns the live fleet (startup clusters plus hot-adds,
// in fleet.yaml order) as a function: a package-level accessor, so it is not
// bound to the webview.
func LiveFleetClusters(s *ConfigService) func() []config.ClusterConfig {
	return s.fleetClusters
}

// fleetClusters returns the running fleet (S2 §2.6), a copy.
func (s *ConfigService) fleetClusters() []config.ClusterConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]config.ClusterConfig(nil), s.running...)
}

// ForgetRunningCluster drops a removed cluster from the running fleet (S2
// §2.5): a lifecycle forget hook.
func ForgetRunningCluster(s *ConfigService, name string) { s.dropRunning(name) }

// FleetApplyLock takes the fleet Apply lock and returns its release (S2
// §2.6): an endpoint activation runs under it, so it never interleaves with
// an Apply's preparation.
func FleetApplyLock(s *ConfigService) func() {
	s.applyMu.Lock()
	return s.applyMu.Unlock
}

// SetRunningEndpoint records an endpoint the connection now uses as the
// running entry's (S2 §2.6): metrics when m is set, else the Alertmanager
// or Loki endpoint e of kind. The caller holds the fleet Apply lock, so a
// reconnect prepared from the running entry keeps it. A cluster the
// running fleet does not have is ignored.
func SetRunningEndpoint(s *ConfigService, cluster, kind string, m *config.MetricsConfig, e *config.EndpointConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.running {
		if s.running[i].Name != cluster {
			continue
		}
		c := s.running[i] // a copy: the entry is replaced, never changed in place
		switch kind {
		case config.EndpointMetrics:
			if m != nil {
				mc := *m
				c.Metrics = &mc
			}
		case config.EndpointAlertmanager:
			c.Alertmanager = copyEndpoint(e)
		case config.EndpointLoki:
			c.Loki = copyEndpoint(e)
		}
		s.running[i] = c
		s.runningRev++
		return
	}
}

func copyEndpoint(e *config.EndpointConfig) *config.EndpointConfig {
	if e == nil {
		return nil
	}
	c := *e
	return &c
}

// RunningRevision is the running fleet's revision.
func RunningRevision(s *ConfigService) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runningRev
}

// GetFleetConfig returns the fleet file state plus a fresh kubeconfig context
// scan. Scan failure is reported in ScanError, never fabricated as "no
// contexts".
func (s *ConfigService) GetFleetConfig() FleetConfigDTO { return s.fleetConfig(true) }

// fleetConfig is GetFleetConfig; withFile also reads fleet.yaml for how it
// now differs from the running fleet.
func (s *ConfigService) fleetConfig(withFile bool) FleetConfigDTO {
	dto := FleetConfigDTO{
		Path:           s.path,
		KubeconfigPath: s.kubeconfigPath(),
		Warnings:       s.cfg.Warnings(),
		Clusters:       []FleetClusterDTO{},
		Contexts:       []KubeContextDTO{},
		LoadError:      s.loadErr,
	}
	running := s.fleetClusters()
	var disk map[string]config.ClusterConfig
	if withFile {
		disk = s.fileDrift(&dto, running)
	}
	inFleet := map[string]bool{}
	for _, c := range running {
		inFleet[c.Name] = true
		inFleet[c.Context] = true
		tags := c.EffectiveTags()
		dto.Clusters = append(dto.Clusters, FleetClusterDTO{
			Name:     c.Name,
			Context:  c.Context,
			Env:      firstNonEmpty(c.Environment, tags["env"]),
			Region:   tags["region"],
			Provider: tags["provider"],
			Account:  tags["account"],
			Group:    c.Group,
			// the published value (S2 §2.7): every reader agrees with the guard
			Protected: s.IsProtected(c.Name),
			// Inferred = locked by a prd/prod/production token, not the flag.
			ProtectedInferred: s.IsProtected(c.Name) && !c.Protected,
			HasMetrics:        c.Metrics != nil,
			Drift:             clusterDrift(c, disk),
			ProtectedBy:       c.ProtectedBy(),
		})
		s.heldOf(&dto.Clusters[len(dto.Clusters)-1])
	}
	sort.Slice(dto.Clusters, func(a, b int) bool { return dto.Clusters[a].Name < dto.Clusters[b].Name })

	ctxs, err := s.kubeContexts(dto.KubeconfigPath)
	if err != nil {
		dto.ScanError = err.Error()
		return dto
	}
	for _, name := range ctxs {
		dto.Contexts = append(dto.Contexts, KubeContextDTO{Name: name, InFleet: inFleet[name]})
	}
	return dto
}

// NewContextCount returns how many kubeconfig contexts are not in the fleet -
// the sidebar badge. Scan failures count as zero (the badge never lies about
// data it could not read; Settings shows the error).
func (s *ConfigService) NewContextCount() int {
	n := 0
	for _, c := range s.fleetConfig(false).Contexts {
		if !c.InFleet {
			n++
		}
	}
	return n
}

// ArmProtected records the user's typed confirmation for a protected cluster,
// unlocking exactly one destructive backend verb for the next 30 seconds. The
// ConfirmDialog (and the custom confirm modals) call this on confirm; the
// destructive service methods consume the grant via guardFor. Arming an
// unprotected cluster is a no-op success, so the frontend calls this
// unconditionally.
func (s *ConfigService) ArmProtected(cluster, typed string) ActionResultDTO {
	g := activeProtectGuard.Load()
	if g == nil {
		return ActionResultDTO{OK: true} // gating not configured (tests, klyxctl)
	}
	if err := g.Arm(cluster, typed); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

// AddClusters appends the given kubeconfig contexts to the fleet file
// (validated before write; comments preserved), then hot-adds each one to the
// running registry so no restart is needed. A connect failure after a
// successful file append is reported honestly: the entry is on disk and a
// restart retries it.
func (s *ConfigService) AddClusters(contexts []string) ActionResultDTO {
	if len(contexts) == 0 {
		return ActionResultDTO{Error: "no contexts selected"}
	}
	// Serialize the whole add: appendClusters is a read-modify-write of
	// fleet.yaml, and two concurrent calls would each read the same original
	// and one would silently lose the other's entries. addMu, not mu: the
	// connects below can take tens of seconds, and protection checks / the
	// fleet push must not wait behind them.
	if s.loadErr != "" {
		return ActionResultDTO{Error: "fleet.yaml did not load when Klyx started, so nothing was added: fix the file in an editor and restart Klyx (" + s.loadErr + ")"}
	}
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	// Settings S2 §4.1: each name is reserved first - absent, a placeholder
	// that admits nothing and is published nowhere - and unreserved last,
	// so nothing is admitted on a cluster until its addition is committed
	if s.policy != nil {
		var toks []policy.Token
		defer func() {
			for _, t := range toks {
				s.policy.Unreserve(t)
			}
		}()
		for _, name := range contexts {
			tok, err := s.policy.Reserve(config.ClusterConfigForContext(name).Name)
			if err != nil {
				return ActionResultDTO{Error: name + " is applying a change to its settings; try again in a moment"}
			}
			toks = append(toks, tok)
		}
	}
	// an addition is for a name that does not run: one that does - its entry
	// removed from fleet.yaml by hand, say - is refused before anything is
	// written; the file's values remove it (Codex's re-review of S2b, RR4)
	for _, name := range contexts {
		n := config.ClusterConfigForContext(name).Name
		_, running := s.runningEntry(n)
		known := false
		if s.policy != nil {
			_, known = s.policy.Snapshot(n)
		}
		if running || known {
			return ActionResultDTO{Error: n + " is already running, so nothing was added: use the file's values to remove it first"}
		}
	}
	// each connection is prepared before anything is written, so one that
	// cannot be built adds nothing (S2 §4.5; Codex's review of S2b, R6)
	var prepared []*fleet.Prepared
	discard := func() {
		for _, p := range prepared {
			p.Discard()
		}
	}
	if s.fp != nil {
		for _, name := range contexts {
			p, err := s.fp.d.Prepare(config.ClusterConfigForContext(name))
			if err != nil {
				discard()
				return ActionResultDTO{Error: "Klyx cannot connect to " + name + ", so nothing was added: " + config.DisplayError(err)}
			}
			prepared = append(prepared, p)
		}
	}
	if err := s.appendClusters(s.path, contexts); err != nil {
		discard()
		return ActionResultDTO{Error: err.Error()}
	}
	if s.fp != nil {
		return s.installAdded(contexts, prepared)
	}
	var failed []string
	for _, name := range contexts {
		cc := config.ClusterConfigForContext(name)
		// Record the config BEFORE connecting: the moment the registry can
		// hand out the new conn, its protection must already be known, or a
		// destructive verb could race a not-yet-recorded prd cluster.
		s.mu.Lock()
		s.running = append(s.running, cc)
		s.runningRev++
		s.mu.Unlock()
		if s.policy != nil {
			s.policy.Seed(cc.Name, cc.IsProtected()) // over its placeholder: still reserved
		}
		if s.connect != nil {
			if err := s.connect(cc); err != nil {
				s.dropRunning(cc.Name)
				if s.policy != nil {
					s.policy.Forget(cc.Name) // nothing of it runs
				}
				failed = append(failed, fmt.Sprintf("%s: %v", cc.Name, err))
				continue
			}
		}
	}
	if len(failed) > 0 {
		return ActionResultDTO{Error: "added to fleet.yaml, but connecting failed - " + strings.Join(failed, "; ") + ". Restart Klyx to retry."}
	}
	return ActionResultDTO{OK: true}
}

// installAdded commits each prepared addition under its cluster's commit
// gate - seeded over its placeholder, still reserved - then installs and
// starts its connection.
func (s *ConfigService) installAdded(contexts []string, prepared []*fleet.Prepared) ActionResultDTO {
	var failed []string
	for i, name := range contexts {
		cc := config.ClusterConfigForContext(name)
		p := prepared[i]
		err := s.fp.d.Life.Decide(cc.Name, Bound{}, func() error {
			s.appendRunning(cc)
			s.policy.Seed(cc.Name, cc.IsProtected())
			s.fp.attempt(cc.Name, p.ID(), s2Of(cc))
			return nil
		})
		if err == nil {
			err = s.fp.d.Install(s.fp.d.Ctx, p)
			if err != nil {
				s.dropRunning(cc.Name)
				s.policy.Forget(cc.Name)
			}
		}
		if err != nil {
			p.Discard()
			failed = append(failed, fmt.Sprintf("%s: %v", cc.Name, err))
		}
	}
	if s.fp.d.Changed != nil {
		s.fp.d.Changed()
	}
	if len(failed) > 0 {
		return ActionResultDTO{Error: "added to fleet.yaml, but connecting failed - " + strings.Join(failed, "; ") + ". Restart Klyx to retry."}
	}
	return ActionResultDTO{OK: true}
}

// dropRunning removes an entry from the running fleet: a hot-added one
// whose connect failed (it stays in fleet.yaml; a restart retries it), or a
// removed cluster.
func (s *ConfigService) dropRunning(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.running {
		if c.Name == name {
			s.running = append(s.running[:i:i], s.running[i+1:]...)
			s.runningRev++
			return
		}
	}
}

// fileDrift reads fleet.yaml as it is now, for how it differs from the
// running fleet (Settings review slice 0): a cluster's identity, placement
// and protection are taken at startup, so a hand edit waits for a restart,
// and Settings says so. (Its endpoints are not compared: External endpoints
// reads them from the file, and Test adopts an edited one.) It returns
// the file's clusters by name, or nil when the file does not load.
func (s *ConfigService) fileDrift(dto *FleetConfigDTO, running []config.ClusterConfig) map[string]config.ClusterConfig {
	snap, err := s.readSnapshot(s.path)
	if err != nil {
		if !(config.IsMissing(err) && len(running) == 0) {
			dto.FileError = config.DisplayError(err)
		}
		return nil
	}
	disk := map[string]config.ClusterConfig{}
	for _, c := range snap.Config.Clusters {
		disk[c.Name] = c
	}
	inRun := map[string]bool{}
	for _, c := range running {
		inRun[c.Name] = true
	}
	for _, c := range snap.Config.Clusters {
		if !inRun[c.Name] {
			dto.FileOnly = append(dto.FileOnly, c.Name)
		}
	}
	sort.Strings(dto.FileOnly)
	return disk
}

// clusterDrift says how the file's entry for a running cluster differs from
// it, in the fields a restart applies; "" when they agree or the file was
// not read.
func clusterDrift(run config.ClusterConfig, disk map[string]config.ClusterConfig) string {
	if disk == nil {
		return ""
	}
	f, ok := disk[run.Name]
	if !ok {
		return "not in fleet.yaml any more: it stays connected until Klyx restarts"
	}
	var changed []string
	if f.IsProtected() != run.IsProtected() {
		if f.IsProtected() {
			changed = append(changed, "protection (the file protects it; it is not protected until Klyx restarts)")
		} else {
			changed = append(changed, "protection (the file no longer protects it; it stays protected until Klyx restarts)")
		}
	}
	for _, d := range []struct {
		name string
		same bool
	}{
		{"context", f.Context == run.Context},
		{"kubeconfig", f.Kubeconfig == run.Kubeconfig},
		{"group", f.Group == run.Group},
		{"environment", f.Environment == run.Environment},
		{"tags", reflect.DeepEqual(f.EffectiveTags(), run.EffectiveTags())},
	} {
		if !d.same {
			changed = append(changed, d.name)
		}
	}
	if len(changed) == 0 {
		return ""
	}
	return "fleet.yaml now differs (" + strings.Join(changed, ", ") + "): Klyx applies it at a restart"
}
