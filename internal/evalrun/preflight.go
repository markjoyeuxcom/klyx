// Package evalrun is the AI eval's runner (docs/superpowers/specs/
// 2026-10-22-klyx-ai-eval.md): the lab-only preflight, the scenarios and
// their plant checks, the run statuses, the scoring and the report. Only
// the eval build (cmd/klyx, -tags eval) links it.
package evalrun

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/cluster"
	"github.com/moomora/klyx/internal/config"
)

// The eval's own directory holds KLYX_CONFIG, the lab's kubeconfig and the
// ownership token, and is marked as the eval's (eval design §3).
const (
	DirMarker      = ".klyx-eval"
	TokenFile      = "owner-token"
	OwnerConfigMap = "klyx-eval-owner"
	ownerNamespace = "kube-system"
)

// Preflight is the lab-only check that runs before any connection starts
// (eval design §3). KindServer and OwnerToken are seams for tests; nil
// means the real kind CLI and a read of the ownership ConfigMap.
type Preflight struct {
	ConfigPath string   // KLYX_CONFIG
	KindName   string   // the kind cluster's name
	Repos      []string // the scenarios' declared fixture repositories
	KindServer func(ctx context.Context, name string) (string, error)
	OwnerToken func(ctx context.Context, rc *rest.Config) (string, error)
}

// Lab is what the preflight established: the one cluster, its connection
// as the app resolves it, and the config it read.
type Lab struct {
	Dir     string
	Config  *config.Config
	Cluster config.ClusterConfig
	REST    *rest.Config
	Server  string
	// digests are the checked files' contents: the config and the kubeconfig
	digests map[string][32]byte
}

// Unchanged says the files the preflight checked still hold what it read:
// the fleet runs on the checked configuration and connection, and a change
// to either file stops the eval.
func (l Lab) Unchanged() error {
	for path, want := range l.digests {
		b, err := os.ReadFile(path)
		if err != nil || sha256.Sum256(b) != want {
			return fmt.Errorf("%s changed after the preflight", path)
		}
	}
	return nil
}

// Run refuses unless KLYX_CONFIG names exactly the owned kind lab: one
// cluster, through the eval's own kubeconfig, on kind's recorded loopback
// endpoint, carrying the ownership marker, with only in-cluster evidence
// sources and the scenarios' own repositories.
func (p Preflight) Run(ctx context.Context) (Lab, error) {
	var lab Lab
	if p.ConfigPath == "" {
		return lab, fmt.Errorf("refusing: no KLYX_CONFIG")
	}
	abs, err := filepath.Abs(p.ConfigPath)
	if err != nil {
		return lab, fmt.Errorf("refusing: %w", err)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return lab, fmt.Errorf("refusing: the config's directory: %w", err)
	}
	lab.Dir = dir
	if _, err := os.Stat(filepath.Join(dir, DirMarker)); err != nil {
		return lab, fmt.Errorf("refusing: %s is not an eval directory (no %s)", dir, DirMarker)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return lab, fmt.Errorf("refusing: %w", err)
	}
	cfg, err := config.Load(abs)
	if err != nil {
		return lab, fmt.Errorf("refusing: %w", err)
	}
	lab.digests = map[string][32]byte{abs: sha256.Sum256(raw)}
	lab.Config = cfg
	if len(cfg.Clusters) != 1 {
		return lab, fmt.Errorf("refusing: the eval config names %d clusters; it must name exactly the lab", len(cfg.Clusters))
	}
	cc := cfg.Clusters[0]
	lab.Cluster = cc
	if err := inDir(dir, cc.Kubeconfig); err != nil {
		return lab, err
	}
	kraw, err := os.ReadFile(cc.Kubeconfig)
	if err != nil {
		return lab, fmt.Errorf("refusing: %w", err)
	}
	lab.digests[cc.Kubeconfig] = sha256.Sum256(kraw)
	rc, err := cluster.RESTConfig(cc)
	if err != nil {
		return lab, fmt.Errorf("refusing: %w", err)
	}
	// what was parsed and resolved is what was digested
	if err := lab.Unchanged(); err != nil {
		return lab, fmt.Errorf("refusing: %w", err)
	}
	lab.REST, lab.Server = rc, rc.Host
	if p.KindName == "" {
		return lab, fmt.Errorf("refusing: no kind cluster name")
	}
	kindServer := p.KindServer
	if kindServer == nil {
		kindServer = KindServer
	}
	want, err := kindServer(ctx, p.KindName)
	if err != nil || want == "" {
		return lab, fmt.Errorf("refusing: kind has no record of cluster %q: %v", p.KindName, err)
	}
	if rc.Host != want {
		return lab, fmt.Errorf("refusing: context %q points at %q; kind's %q is at %q", cc.Context, rc.Host, p.KindName, want)
	}
	if !loopback(rc.Host) {
		return lab, fmt.Errorf("refusing: API server %q is not loopback", rc.Host)
	}
	if err := sources(cc); err != nil {
		return lab, err
	}
	if err := repos(cfg, p.Repos); err != nil {
		return lab, err
	}
	state, err := os.ReadFile(filepath.Join(dir, TokenFile))
	if err != nil || strings.TrimSpace(string(state)) == "" {
		return lab, fmt.Errorf("refusing: no ownership token in %s", dir)
	}
	owner := p.OwnerToken
	if owner == nil {
		owner = OwnerToken
	}
	got, err := owner(ctx, rc)
	if err != nil {
		return lab, fmt.Errorf("refusing: no ownership marker on the lab: %v", err)
	}
	if got == "" || got != strings.TrimSpace(string(state)) {
		return lab, fmt.Errorf("refusing: the lab's ownership marker does not match %s", filepath.Join(dir, TokenFile))
	}
	return lab, nil
}

// inDir requires the cluster's kubeconfig to be one file inside the eval's
// directory: never the owner's, never a KUBECONFIG list.
func inDir(dir, kubeconfig string) error {
	if kubeconfig == "" {
		return fmt.Errorf("refusing: the lab cluster names no kubeconfig; the default would be the owner's")
	}
	if len(cluster.KubeconfigPaths(kubeconfig)) != 1 {
		return fmt.Errorf("refusing: the lab's kubeconfig must be one file")
	}
	abs, err := filepath.Abs(kubeconfig)
	if err != nil {
		return fmt.Errorf("refusing: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("refusing: the lab's kubeconfig: %w", err)
	}
	rel, err := filepath.Rel(dir, real)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("refusing: the lab's kubeconfig %s is outside the eval directory %s", real, dir)
	}
	return nil
}

func loopback(server string) bool {
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// sources requires every evidence endpoint to be a service in the lab
// cluster: an explicit endpoint could be anything.
func sources(cc config.ClusterConfig) error {
	type src struct {
		name     string
		endpoint string
		inside   bool
	}
	var all []src
	if m := cc.Metrics; m != nil {
		all = append(all, src{"Prometheus", m.Endpoint, m.ServiceRef != nil})
	}
	if a := cc.Alertmanager; a != nil {
		all = append(all, src{"Alertmanager", a.Endpoint, a.ServiceRef != nil})
	}
	if l := cc.Loki; l != nil {
		all = append(all, src{"Loki", l.Endpoint, l.ServiceRef != nil})
	}
	for _, s := range all {
		if s.endpoint != "" || !s.inside {
			return fmt.Errorf("refusing: %s must be a service reference in the lab cluster, not an explicit endpoint", s.name)
		}
	}
	return nil
}

// repos requires every approved repository to be one the scenarios
// declare.
func repos(cfg *config.Config, declared []string) error {
	if cfg.Git == nil {
		return nil
	}
	ok := map[string]bool{}
	for _, r := range declared {
		ok[r] = true
	}
	for _, r := range cfg.Git.Repos {
		if !ok[r.URL] {
			return fmt.Errorf("refusing: approved repository %q is not a scenario's fixture repository", r.URL)
		}
	}
	return nil
}

// KindServer is the API server kind records for a cluster.
func KindServer(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "kind", "get", "kubeconfig", "--name", name).Output()
	if err != nil {
		return "", err
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[0] == "server:" {
			return f[1], nil
		}
	}
	return "", fmt.Errorf("no server in kind's kubeconfig")
}

// OwnerToken reads the lab's ownership marker.
func OwnerToken(ctx context.Context, rc *rest.Config) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return "", err
	}
	cm, err := cs.CoreV1().ConfigMaps(ownerNamespace).Get(ctx, OwnerConfigMap, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return cm.Data["token"], nil
}
