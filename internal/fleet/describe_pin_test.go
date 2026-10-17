package fleet

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/moomora/klyx/internal/clibin"
)

const pinKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: lab
  cluster:
    server: https://127.0.0.1:6443
    certificate-authority-data: Q0EtREFUQQ==
- name: other
  cluster:
    server: https://10.9.9.9:6443
contexts:
- name: kind-lab
  context: {cluster: lab, user: lab-user}
- name: other
  context: {cluster: other, user: lab-user}
current-context: other
users:
- name: lab-user
  user:
    token: PINtoken1
`

// pinConn is a connection built from a kubeconfig file, as the factory
// builds one, and the file it was built from.
func pinConn(t *testing.T) (*ClusterConn, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(pinKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: path}, &clientcmd.ConfigOverrides{CurrentContext: "kind-lab"}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	rc.QPS, rc.Burst = 50, 100 // tuning is never compared
	old := kubectlBin
	t.Cleanup(func() { kubectlBin = old })
	kubectlBin = clibin.Binary{Name: "kubectl", LookPath: func(string) (string, error) { return "/usr/bin/kubectl", nil }}
	return &ClusterConn{kubeContext: "kind-lab", kubeconfigPaths: []string{path}, restConfig: rc}, path
}

// Codex's S0b implementation review, finding 4: kubectl reads a flattened
// snapshot of exactly this connection, through the resolved
// resource.version.group, and the snapshot is removed afterwards.
func TestDescribePinnedCmd(t *testing.T) {
	c, _ := pinConn(t)
	gvr := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	cmd, cleanup, err := c.DescribePinnedCmd(context.Background(), gvr, "flux-system", "apps", true)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(cmd.Args[1:], " ")
	if !strings.HasPrefix(args, "--kubeconfig ") || !strings.Contains(args, "--context kind-lab describe kustomizations.v1.kustomize.toolkit.fluxcd.io -n flux-system -- apps") {
		t.Fatalf("argv: %s", args)
	}
	snapPath := cmd.Args[2]
	raw, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	snap := string(raw)
	if !strings.Contains(snap, "127.0.0.1:6443") || strings.Contains(snap, "10.9.9.9") || !strings.Contains(snap, "current-context: kind-lab") {
		t.Fatalf("the snapshot is not this context alone:\n%s", snap)
	}
	if fi, _ := os.Stat(snapPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode %v", fi.Mode().Perm())
	}
	if env := strings.Join(cmd.Env, "\n"); !strings.Contains(env, "KUBECONFIG="+snapPath) {
		t.Fatal("KUBECONFIG does not name the snapshot")
	}
	cleanup()
	if _, err := os.Stat(snapPath); !os.IsNotExist(err) {
		t.Fatalf("the snapshot outlived its describe: %v", err)
	}
	if _, _, err := c.DescribePinnedCmd(context.Background(), schema.GroupVersionResource{Resource: "pods", Version: "v1"}, "ns", "p", true); err == nil {
		t.Fatal("a pinned describe needs a group")
	}
}

// A kubeconfig edited since connecting refuses, naming the field, never a
// value; tuning and a refreshed auth-provider token do not.
func TestKubeconfigSnapshotRefusesDrift(t *testing.T) {
	for _, c := range []struct{ name, from, to, field string }{
		{"server", "https://127.0.0.1:6443", "https://127.0.0.1:7443", "server"},
		{"CA", "Q0EtREFUQQ==", "T1RIRVItQ0E=", "TLS"},
		{"token", "PINtoken1", "PINtoken2", "token"},
		{"context removed", "name: kind-lab\n", "name: renamed\n", ""},
	} {
		conn, path := pinConn(t)
		if err := os.WriteFile(path, []byte(strings.Replace(pinKubeconfig, c.from, c.to, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := conn.kubeconfigSnapshot()
		if !errors.Is(err, errKubeconfigChanged) || (c.field != "" && !strings.Contains(err.Error(), c.field)) {
			t.Errorf("%s: %v", c.name, err)
		}
		if err != nil && (strings.Contains(err.Error(), "PINtoken") || strings.Contains(err.Error(), "7443")) {
			t.Errorf("%s: a value in the error: %v", c.name, err)
		}
	}
	conn, _ := pinConn(t)
	if _, err := conn.kubeconfigSnapshot(); err != nil {
		t.Fatalf("an unchanged kubeconfig pins: %v", err)
	}
	conn.restConfig = nil
	if _, err := conn.kubeconfigSnapshot(); err == nil {
		t.Fatal("no connection config cannot pin")
	}
}

// Codex's S0b implementation review, finding 6: classification ends with
// the caller's context, with or without a REST config.
func TestDescribeTargetHonoursCancellation(t *testing.T) {
	for _, withREST := range []bool{false, true} {
		blocked, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api":
				w.Write([]byte(`{"kind":"APIVersions","versions":["v1"]}`))
			case "/apis":
				w.Write([]byte(`{"kind":"APIGroupList","groups":[]}`))
			case "/api/v1":
				once.Do(func() { close(blocked) })
				select {
				case <-release:
				case <-r.Context().Done():
				}
				w.Write([]byte(`{"kind":"APIResourceList","groupVersion":"v1","resources":[{"name":"pods","kind":"Pod","namespaced":true,"verbs":["get"]}]}`))
			default:
				http.NotFound(w, r)
			}
		}))
		cfg := &rest.Config{Host: srv.URL, Timeout: 5 * time.Second}
		cs, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		c := &ClusterConn{typed: cs}
		if withREST {
			c.restConfig = cfg
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		done := make(chan error, 1)
		go func() { _, _, err := c.DescribeTarget(ctx, "pods"); done <- err }()
		select {
		case <-blocked:
		case <-time.After(2 * time.Second):
			t.Fatal("fixture did not reach discovery")
		}
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("rest=%v: a cancelled classification succeeded", withREST)
			}
		case <-time.After(500 * time.Millisecond):
			t.Errorf("rest=%v: DescribeTarget ignored the caller's cancellation", withREST)
		}
		close(release)
		cancel()
		srv.Close()
	}
}
