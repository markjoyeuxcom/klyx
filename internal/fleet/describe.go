package fleet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/moomora/klyx/internal/clibin"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/execenv"
)

// kubectlBin is the shared kubectl resolution stack (env override -> PATH ->
// package-manager fallbacks), same shape as fluxcli/helmcli. The fallback step
// matters on macOS .app launches, which do not inherit the interactive shell
// PATH. Tests swap the lookup hooks.
var kubectlBin = clibin.Binary{
	Name:          "kubectl",
	EnvVar:        "KLYX_KUBECTL_PATH",
	FallbackPaths: defaultKubectlFallbackPaths,
}

func defaultKubectlFallbackPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/opt/homebrew/bin/kubectl", "/usr/local/bin/kubectl", "/opt/local/bin/kubectl"}
	case "linux":
		return []string{"/usr/local/bin/kubectl", "/usr/bin/kubectl", "/snap/bin/kubectl"}
	case "windows":
		var paths []string
		if root := strings.TrimSpace(os.Getenv("ChocolateyInstall")); root != "" {
			paths = append(paths, filepath.Join(root, "bin", "kubectl.exe"))
		}
		if root := strings.TrimSpace(os.Getenv("ProgramFiles")); root != "" {
			paths = append(paths, filepath.Join(root, "kubectl", "kubectl.exe"))
		}
		return paths
	default:
		return nil
	}
}

// validateObjectName guards the describe argv against flag or whitespace
// injection while accepting every legal Kubernetes object name. It is wider
// than validateK8sName because not all object names are DNS-1123 subdomains:
// RBAC objects legally carry ':' and uppercase (system:node-proxier). Still
// an allowlist that fails closed: must start alphanumeric (never
// flag-shaped), only [A-Za-z0-9._:-] after that, 253 max.
func validateObjectName(what, name string) error {
	if name == "" || len(name) > 253 {
		return fmt.Errorf("invalid %s name %q: must be 1-253 characters", what, name)
	}
	if !isObjectNameAlnum(name[0]) {
		return fmt.Errorf("invalid %s name %q: must start with an alphanumeric character", what, name)
	}
	for i := 1; i < len(name); i++ {
		ch := name[i]
		if isObjectNameAlnum(ch) || ch == '-' || ch == '.' || ch == '_' || ch == ':' {
			continue
		}
		return fmt.Errorf("invalid %s name %q: character %q is not allowed", what, name, rune(ch))
	}
	return nil
}

func isObjectNameAlnum(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9'
}

// DescribeCmd builds the `kubectl describe` invocation for one object. It does
// NOT run the process; the appbridge layer owns the lifecycle (buffers +
// timeout via ctx). Read-only by construction: describe never mutates.
//
// kindArg is the kubectl resource argument: "pod"/"node" for the core panels,
// plural.group for typed instances (plural alone when group is ""). It is
// validated as a DNS-1123 subdomain - which plural.group always is - so a
// flag-shaped or whitespace-bearing argument can never reach the argv. The
// object name is validated by validateObjectName and additionally placed
// after "--" so it can never parse as a flag (same guard as DrainNodeCmd).
//
// An empty kubeContext is refused (same guard as ExecCommand/DrainNodeCmd):
// kubectl would silently fall back to the kubeconfig's current-context and
// describe an object on whatever cluster the user's terminal points at.
func (c *ClusterConn) DescribeCmd(ctx context.Context, kindArg, namespace, name string) (*exec.Cmd, error) {
	args, err := c.describeArgs(kindArg, namespace, name)
	if err != nil {
		return nil, err
	}
	bin, ok := kubectlBin.Resolve()
	if !ok {
		return nil, fmt.Errorf("kubectl not found: install kubectl or point KLYX_KUBECTL_PATH at the binary")
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // argv validated above, binary resolved via clibin
	cmd.Env = execenv.Environ(c.cliEnv())
	return cmd, nil
}

// DescribeArgv is the same describe as a command for a terminal (The Line
// slice 14, design review 1): the cluster's kubeconfig selection travels in
// the argv (kubectlTerminalArgv - --kubeconfig for one file, env KUBECONFIG
// for a list, a list refused on Windows), so a copied command can never
// resolve the context through another file to another cluster.
func (c *ClusterConn) DescribeArgv(kindArg, namespace, name string) ([]string, error) {
	args, err := c.describeArgs(kindArg, namespace, name)
	if err != nil {
		return nil, err
	}
	argv, err := c.kubectlTerminalArgv()
	if err != nil {
		return nil, err
	}
	return append(argv, args...), nil
}

// describeArgs validates one describe and returns its kubectl arguments.
func (c *ClusterConn) describeArgs(kindArg, namespace, name string) ([]string, error) {
	if c.kubeContext == "" {
		return nil, fmt.Errorf("cluster has no kubeconfig context")
	}
	if err := validateK8sName("resource", kindArg); err != nil {
		return nil, err
	}
	if namespace != "" {
		if err := validateK8sName("namespace", namespace); err != nil {
			return nil, err
		}
	}
	if err := validateObjectName("object", name); err != nil {
		return nil, err
	}
	args := []string{"--context", c.kubeContext, "describe", kindArg}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	return append(args, "--", name), nil
}

// DescribeTarget resolves a describe's resource argument to its resource
// and kind through discovery, the way kubectl will (a resource, a short name,
// resource.group or resource.version.group), so an alias cannot slip past
// the payload classification (coverage S0b §4). An error means it could not
// be classified, and the caller withholds.
func (c *ClusterConn) DescribeTarget(ctx context.Context, kindArg string) (schema.GroupVersionResource, schema.GroupVersionKind, error) {
	type target struct {
		gvr schema.GroupVersionResource
		gvk schema.GroupVersionKind
		err error
	}
	if c.typed == nil {
		return schema.GroupVersionResource{}, schema.GroupVersionKind{}, fmt.Errorf("discovery unavailable")
	}
	if err := ctx.Err(); err != nil {
		return schema.GroupVersionResource{}, schema.GroupVersionKind{}, err
	}
	// Discovery is bound to the caller (Codex's S0b implementation review,
	// finding 6): with a REST config its requests carry ctx and are cancelled
	// with it; either way the caller returns at ctx's end, so closing the
	// sheet or the Describe budget ends classification.
	dc := discovery.DiscoveryInterface(c.typed.Discovery())
	if c.restConfig != nil {
		cfg := rest.CopyConfig(c.restConfig)
		cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper { return ctxTransport{rt: rt, ctx: ctx} })
		if d, err := discovery.NewDiscoveryClientForConfig(cfg); err == nil {
			dc = d
		}
	}
	done := make(chan target, 1)
	go func() {
		gvr, gvk, err := resolveTarget(dc, kindArg)
		done <- target{gvr, gvk, err}
	}()
	select {
	case t := <-done:
		return t.gvr, t.gvk, t.err
	case <-ctx.Done():
		return schema.GroupVersionResource{}, schema.GroupVersionKind{}, ctx.Err()
	}
}

// ctxTransport sends every request under ctx, so a cancelled Describe
// cancels its discovery reads.
type ctxTransport struct {
	rt  http.RoundTripper
	ctx context.Context
}

func (t ctxTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	return t.rt.RoundTrip(req.WithContext(t.ctx))
}

func resolveTarget(dc discovery.DiscoveryInterface, kindArg string) (schema.GroupVersionResource, schema.GroupVersionKind, error) {
	var none schema.GroupVersionResource
	grs, err := restmapper.GetAPIGroupResources(dc)
	if len(grs) == 0 {
		if err == nil {
			err = fmt.Errorf("discovery returned no groups")
		}
		return none, schema.GroupVersionKind{}, err
	}
	m := restmapper.NewShortcutExpander(restmapper.NewDiscoveryRESTMapper(grs), dc, nil)
	gvr, gr := schema.ParseResourceArg(kindArg)
	if gvr != nil {
		if full, err := m.ResourceFor(*gvr); err == nil {
			if gvk, err := m.KindFor(full); err == nil && !gvk.Empty() {
				return full, gvk, nil
			}
		}
	}
	full, rerr := m.ResourceFor(gr.WithVersion(""))
	if rerr != nil {
		return none, schema.GroupVersionKind{}, rerr
	}
	gvk, kerr := m.KindFor(full)
	if kerr != nil || gvk.Empty() {
		if kerr == nil {
			kerr = fmt.Errorf("unknown resource %q", kindArg)
		}
		return none, schema.GroupVersionKind{}, kerr
	}
	return full, gvk, nil
}

// DescribePinnedCmd is kubectl describe bound to what the version bracket
// checks (Codex's S0b implementation review, finding 4):
//   - the resource is the resolved resource.version.group, never an alias
//     kubectl would re-resolve through its own discovery and cache;
//   - kubectl reads a snapshot of this cluster's kubeconfig, flattened and
//     written now to a private file, after the snapshot is shown to resolve
//     to the same server, trust, credentials and proxy as the connection
//     the bracket reads through. A kubeconfig edited since Klyx connected
//     refuses rather than letting kubectl read another cluster.
//
// cleanup removes the snapshot; call it once the command has ended.
func (c *ClusterConn) DescribePinnedCmd(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*exec.Cmd, func(), error) {
	if gvr.Group == "" || gvr.Version == "" || gvr.Resource == "" {
		return nil, nil, fmt.Errorf("a pinned describe needs a resolved group, version and resource")
	}
	args, err := c.describeArgs(gvr.Resource+"."+gvr.Version+"."+gvr.Group, namespace, name)
	if err != nil {
		return nil, nil, err
	}
	snap, err := c.kubeconfigSnapshot()
	if err != nil {
		return nil, nil, err
	}
	bin, ok := kubectlBin.Resolve()
	if !ok {
		return nil, nil, fmt.Errorf("kubectl not found: install kubectl or point KLYX_KUBECTL_PATH at the binary")
	}
	dir, err := os.MkdirTemp("", "klyx-describe-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(path, snap, 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"--kubeconfig", path}, args...)...) //nolint:gosec // argv validated, binary resolved via clibin
	cmd.Env = execenv.Environ(execenv.KubeconfigEnv(path))
	return cmd, cleanup, nil
}

// errKubeconfigChanged is a snapshot that no longer resolves to the
// connection Klyx holds.
var errKubeconfigChanged = errors.New("this cluster's kubeconfig changed since Klyx connected; reconnect the cluster to describe")

// kubeconfigSnapshot is this cluster's context, minified and flattened (file
// references embedded) from the kubeconfig files read now, refused unless it
// resolves to the connection's own server, trust, credentials and proxy.
func (c *ClusterConn) kubeconfigSnapshot() ([]byte, error) {
	if c.restConfig == nil {
		return nil, fmt.Errorf("no connection configuration to pin kubectl to")
	}
	if c.kubeContext == "" || len(c.kubeconfigPaths) == 0 {
		return nil, fmt.Errorf("cluster has no kubeconfig selection to pin kubectl to")
	}
	raw, err := (&clientcmd.ClientConfigLoadingRules{Precedence: c.kubeconfigPaths}).Load()
	if err != nil {
		return nil, fmt.Errorf("reading the kubeconfig: %w", err)
	}
	if _, ok := raw.Contexts[c.kubeContext]; !ok {
		return nil, errKubeconfigChanged
	}
	raw.CurrentContext = c.kubeContext
	if err := clientcmdapi.MinifyConfig(raw); err != nil {
		return nil, err
	}
	if err := clientcmdapi.FlattenConfig(raw); err != nil {
		return nil, err
	}
	rc, err := clientcmd.NewNonInteractiveClientConfig(*raw, c.kubeContext, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("resolving the kubeconfig: %w", err)
	}
	if err := sameConnection(rc, c.restConfig); err != nil {
		return nil, err
	}
	return clientcmd.Write(*raw)
}

// sameConnection compares what decides which server is reached, how it is
// trusted and who is authenticated. Tuning (QPS, timeouts, user agent) is
// not compared. Values never appear in the error.
func sameConnection(a, b *rest.Config) error {
	la, lb := rest.CopyConfig(a), rest.CopyConfig(b)
	if err := rest.LoadTLSFiles(la); err != nil {
		return err
	}
	if err := rest.LoadTLSFiles(lb); err != nil {
		return err
	}
	ta, tb := la.TLSClientConfig, lb.TLSClientConfig
	diff := func(field string, same bool) error {
		if same {
			return nil
		}
		return fmt.Errorf("%w (%s differs)", errKubeconfigChanged, field)
	}
	for _, d := range []error{
		diff("server", strings.TrimRight(la.Host, "/") == strings.TrimRight(lb.Host, "/") && la.APIPath == lb.APIPath),
		diff("TLS", ta.Insecure == tb.Insecure && ta.ServerName == tb.ServerName && bytes.Equal(ta.CAData, tb.CAData) &&
			bytes.Equal(ta.CertData, tb.CertData) && bytes.Equal(ta.KeyData, tb.KeyData)),
		diff("token", la.BearerToken == lb.BearerToken && la.BearerTokenFile == lb.BearerTokenFile),
		diff("basic auth", la.Username == lb.Username && la.Password == lb.Password),
		diff("impersonation", reflect.DeepEqual(la.Impersonate, lb.Impersonate)),
		diff("exec plugin", sameExec(la.ExecProvider, lb.ExecProvider)),
		diff("auth provider", sameAuthProvider(la.AuthProvider, lb.AuthProvider)),
		diff("proxy", sameProxy(la, lb)),
	} {
		if d != nil {
			return d
		}
	}
	return nil
}

func sameExec(a, b *clientcmdapi.ExecConfig) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Command == b.Command && a.APIVersion == b.APIVersion && a.ProvideClusterInfo == b.ProvideClusterInfo &&
		reflect.DeepEqual(a.Args, b.Args) && reflect.DeepEqual(a.Env, b.Env)
}

// sameAuthProvider ignores the tokens and expiries a provider caches back
// into the kubeconfig as it refreshes.
func sameAuthProvider(a, b *clientcmdapi.AuthProviderConfig) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	keys := func(m map[string]string) []string {
		var out []string
		for k, v := range m {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "token") || strings.Contains(lk, "expir") {
				continue
			}
			out = append(out, k+"="+v)
		}
		sort.Strings(out)
		return out
	}
	return a.Name == b.Name && reflect.DeepEqual(keys(a.Config), keys(b.Config))
}

// sameProxy asks both configs which proxy they would use for the server.
func sameProxy(a, b *rest.Config) bool {
	if a.Proxy == nil || b.Proxy == nil {
		return a.Proxy == nil && b.Proxy == nil
	}
	req, err := http.NewRequest(http.MethodGet, a.Host, nil)
	if err != nil {
		return false
	}
	pa, ea := a.Proxy(req)
	pb, eb := b.Proxy(req)
	if ea != nil || eb != nil {
		return false
	}
	if pa == nil || pb == nil {
		return pa == nil && pb == nil
	}
	return pa.String() == pb.String()
}

// DescribeSnapshot reads the object a describe names, for the version
// bracket around kubectl's own read (S0b revision 4): its UID, its
// resourceVersion and whether it carries a registered payload.
func (c *ClusterConn) DescribeSnapshot(ctx context.Context, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, ns, name string) (string, string, bool, error) {
	if c.dyn == nil {
		return "", "", false, fmt.Errorf("dynamic client unavailable")
	}
	res := c.dyn.Resource(gvr)
	var u *unstructured.Unstructured
	var err error
	if ns != "" {
		u, err = res.Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	} else {
		u, err = res.Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return "", "", false, err
	}
	s := crd.SanitizeEmbedded(gvk.Group, gvk.Kind, u.Object)
	return string(u.GetUID()), u.GetResourceVersion(), s.Payload, nil
}
