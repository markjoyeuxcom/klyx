package appbridge

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/moomora/klyx/internal/config"
)

// FleetClusterDTO is one configured fleet entry as Settings shows it.
type FleetClusterDTO struct {
	Name       string `json:"name"`
	Context    string `json:"context"`
	Env        string `json:"env"`
	Region     string `json:"region"`
	Provider   string `json:"provider"`
	Account    string `json:"account"`
	Group      string `json:"group"`
	Protected  bool   `json:"protected"`
	HasMetrics bool   `json:"hasMetrics"`
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
}

// ConfigService exposes the fleet configuration to Settings. It reads the
// STARTUP config (the one the registry was built from) plus the clusters
// hot-added this session; AddClusters appends to the file on disk AND adds
// the conn to the running registry via the connect seam - no restart needed.
// The startup config itself is never mutated (other services hold the same
// pointer); session additions live in the guarded added slice.
type ConfigService struct {
	path string
	cfg  *config.Config

	mu    sync.Mutex
	added []config.ClusterConfig // hot-added this session, connect succeeded

	// seams for tests
	kubeconfigPath func() string
	kubeContexts   func(string) ([]string, error)
	appendClusters func(string, []string) error
	connect        func(config.ClusterConfig) error // adds to the running registry
}

func NewConfigService(path string, cfg *config.Config, connect func(config.ClusterConfig) error) *ConfigService {
	return &ConfigService{
		path:           path,
		cfg:            cfg,
		kubeconfigPath: config.DefaultKubeconfigPath,
		kubeContexts:   config.KubeContexts,
		appendClusters: config.AppendClusters,
		connect:        connect,
	}
}

// IsProtected reports whether the named cluster is protected, consulting the
// LIVE fleet (startup config plus this session's hot-added clusters) so a prd
// context imported mid-session is gated immediately - not left unprotected until
// a restart. This is the ProtectGuard's protection source; the guard closes over
// it in main so hot-added clusters honor the typed-confirmation invariant. An
// unknown name is treated as unprotected (the destructive verb will fail its
// own connected-cluster check).
func (s *ConfigService) IsProtected(cluster string) bool {
	for _, c := range s.fleetClusters() {
		if c.Name == cluster || c.Context == cluster {
			return c.IsProtected()
		}
	}
	return false
}

// fleetClusters returns the startup clusters plus this session's hot-added
// ones - the live fleet as Settings should report it.
func (s *ConfigService) fleetClusters() []config.ClusterConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]config.ClusterConfig, 0, len(s.cfg.Clusters)+len(s.added))
	out = append(out, s.cfg.Clusters...)
	out = append(out, s.added...)
	return out
}

// GetFleetConfig returns the fleet file state plus a fresh kubeconfig context
// scan. Scan failure is reported in ScanError, never fabricated as "no
// contexts".
func (s *ConfigService) GetFleetConfig() FleetConfigDTO {
	dto := FleetConfigDTO{
		Path:           s.path,
		KubeconfigPath: s.kubeconfigPath(),
		Warnings:       s.cfg.Warnings(),
		Clusters:       []FleetClusterDTO{},
		Contexts:       []KubeContextDTO{},
	}
	inFleet := map[string]bool{}
	for _, c := range s.fleetClusters() {
		inFleet[c.Name] = true
		inFleet[c.Context] = true
		tags := c.EffectiveTags()
		dto.Clusters = append(dto.Clusters, FleetClusterDTO{
			Name:       c.Name,
			Context:    c.Context,
			Env:        firstNonEmpty(c.Environment, tags["env"]),
			Region:     tags["region"],
			Provider:   tags["provider"],
			Account:    tags["account"],
			Group:      c.Group,
			Protected:  c.Protected,
			HasMetrics: c.Metrics != nil,
		})
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
	for _, c := range s.GetFleetConfig().Contexts {
		if !c.InFleet {
			n++
		}
	}
	return n
}

// ArmProtected records the user's typed confirmation for a protected cluster,
// unlocking exactly one destructive backend verb for the next 30 seconds. The
// ConfirmDialog (and the custom confirm modals) call this on confirm; the
// destructive service methods consume the grant via checkProtected. Arming an
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
	// and one would silently lose the other's entries. Adds are rare,
	// user-driven Settings actions - holding the lock across the file write
	// and connects is fine.
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.appendClusters(s.path, contexts); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	var failed []string
	for _, name := range contexts {
		cc := config.ClusterConfigForContext(name)
		if s.connect != nil {
			if err := s.connect(cc); err != nil {
				failed = append(failed, fmt.Sprintf("%s: %v", cc.Name, err))
				continue
			}
		}
		s.added = append(s.added, cc)
	}
	if len(failed) > 0 {
		return ActionResultDTO{Error: "added to fleet.yaml, but connecting failed - " + strings.Join(failed, "; ") + ". Restart Klyx to retry."}
	}
	return ActionResultDTO{OK: true}
}
