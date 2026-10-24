package evalrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

// The eval design's §3 refusals: the preflight runs before any connection,
// on the config it was given, and refuses anything but the owned lab.

const (
	labServer = "https://127.0.0.1:52011"
	labRepo   = "https://github.com/example/klyx-lab-a1.git"
	token     = "t-0123456789abcdef"
)

const kubeconfigFmt = `apiVersion: v1
kind: Config
clusters:
- name: kind-klyx
  cluster:
    server: %s
    insecure-skip-tls-verify: true
- name: elsewhere
  cluster:
    server: https://api.production.example.invalid:6443
    insecure-skip-tls-verify: true
contexts:
- name: kind-klyx
  context: {cluster: kind-klyx, user: u}
- name: kind-elsewhere
  context: {cluster: elsewhere, user: u}
users:
- name: u
  user: {token: lab}
current-context: kind-klyx
`

type labDir struct {
	dir string
}

// newLab writes an eval directory: its marker, kubeconfig, token and a
// config whose cluster block is cluster (with KUBECONFIG substituted).
func newLab(t *testing.T, cluster, extra string) labDir {
	t.Helper()
	dir := t.TempDir()
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(DirMarker, "")
	write("kubeconfig", strings.Replace(kubeconfigFmt, "%s", labServer, 1))
	write(TokenFile, token+"\n")
	kc := filepath.Join(dir, "kubeconfig")
	write("fleet.yaml", "clusters:\n"+strings.ReplaceAll(cluster, "KUBECONFIG", kc)+extra)
	return labDir{dir}
}

const oneCluster = `- name: lab
  context: kind-klyx
  kubeconfig: KUBECONFIG
`

func (l labDir) preflight() Preflight {
	return Preflight{
		ConfigPath: filepath.Join(l.dir, "fleet.yaml"),
		KindName:   "klyx",
		Repos:      []string{labRepo},
		KindServer: func(context.Context, string) (string, error) { return labServer, nil },
		OwnerToken: func(context.Context, *rest.Config) (string, error) { return token, nil },
	}
}

func refused(t *testing.T, p Preflight, want string) {
	t.Helper()
	_, err := p.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want a refusal naming %q, got %v", want, err)
	}
}

func TestTheOwnedLabPasses(t *testing.T) {
	l := newLab(t, oneCluster+`  alertmanager:
    serviceRef: {namespace: monitoring, name: alertmanager, port: "9093"}
`, "git:\n  repos:\n  - url: "+labRepo+"\n")
	lab, err := l.preflight().Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lab.Server != labServer || lab.Cluster.Name != "lab" || lab.REST == nil {
		t.Fatalf("%+v", lab)
	}
}

func TestRefusals(t *testing.T) {
	t.Run("a kind- context pointing elsewhere", func(t *testing.T) {
		l := newLab(t, strings.Replace(oneCluster, "context: kind-klyx", "context: kind-elsewhere", 1), "")
		refused(t, l.preflight(), "points at")
	})
	t.Run("a loopback endpoint that is not kind's record", func(t *testing.T) {
		p := newLab(t, oneCluster, "").preflight()
		p.KindServer = func(context.Context, string) (string, error) { return "https://127.0.0.1:60000", nil }
		refused(t, p, "points at")
	})
	t.Run("kind's record is not loopback", func(t *testing.T) {
		l := newLab(t, strings.Replace(oneCluster, "context: kind-klyx", "context: kind-elsewhere", 1), "")
		p := l.preflight()
		p.KindServer = func(context.Context, string) (string, error) {
			return "https://api.production.example.invalid:6443", nil
		}
		refused(t, p, "not loopback")
	})
	t.Run("kind has no such cluster", func(t *testing.T) {
		p := newLab(t, oneCluster, "").preflight()
		p.KindServer = func(context.Context, string) (string, error) { return "", errors.New("no cluster") }
		refused(t, p, "kind has no record")
	})
	t.Run("no ownership marker", func(t *testing.T) {
		p := newLab(t, oneCluster, "").preflight()
		p.OwnerToken = func(context.Context, *rest.Config) (string, error) { return "", errors.New("not found") }
		refused(t, p, "no ownership marker")
	})
	t.Run("a foreign ownership marker", func(t *testing.T) {
		p := newLab(t, oneCluster, "").preflight()
		p.OwnerToken = func(context.Context, *rest.Config) (string, error) { return "t-someone-else", nil }
		refused(t, p, "does not match")
	})
	t.Run("no local token", func(t *testing.T) {
		l := newLab(t, oneCluster, "")
		if err := os.Remove(filepath.Join(l.dir, TokenFile)); err != nil {
			t.Fatal(err)
		}
		refused(t, l.preflight(), "no ownership token")
	})
	for _, ep := range []string{"alertmanager", "loki"} {
		t.Run("an explicit "+ep+" endpoint", func(t *testing.T) {
			l := newLab(t, oneCluster+"  "+ep+":\n    endpoint: https://"+ep+".example.invalid\n", "")
			refused(t, l.preflight(), "service reference")
		})
	}
	t.Run("an explicit Prometheus endpoint", func(t *testing.T) {
		l := newLab(t, oneCluster+"  metrics:\n    endpoint: https://prometheus.example.invalid\n", "")
		refused(t, l.preflight(), "Prometheus must be a service reference")
	})
	t.Run("an approved repository outside the scenarios", func(t *testing.T) {
		l := newLab(t, oneCluster, "git:\n  repos:\n  - url: https://github.com/example/production.git\n")
		refused(t, l.preflight(), "not a scenario's fixture repository")
	})
	t.Run("a second cluster", func(t *testing.T) {
		l := newLab(t, oneCluster+strings.Replace(oneCluster, "name: lab", "name: other", 1), "")
		refused(t, l.preflight(), "names 2 clusters")
	})
	t.Run("no kubeconfig: the owner's default", func(t *testing.T) {
		l := newLab(t, "- name: lab\n  context: kind-klyx\n", "")
		refused(t, l.preflight(), "names no kubeconfig")
	})
	t.Run("a kubeconfig outside the eval directory", func(t *testing.T) {
		other := t.TempDir()
		kc := filepath.Join(other, "kubeconfig")
		if err := os.WriteFile(kc, []byte(strings.Replace(kubeconfigFmt, "%s", labServer, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		l := newLab(t, "- name: lab\n  context: kind-klyx\n  kubeconfig: "+kc+"\n", "")
		refused(t, l.preflight(), "outside the eval directory")
	})
	t.Run("a KUBECONFIG list", func(t *testing.T) {
		l := newLab(t, "- name: lab\n  context: kind-klyx\n  kubeconfig: KUBECONFIG"+string(os.PathListSeparator)+"KUBECONFIG\n", "")
		refused(t, l.preflight(), "must be one file")
	})
	t.Run("not an eval directory", func(t *testing.T) {
		l := newLab(t, oneCluster, "")
		if err := os.Remove(filepath.Join(l.dir, DirMarker)); err != nil {
			t.Fatal(err)
		}
		refused(t, l.preflight(), "not an eval directory")
	})
}
