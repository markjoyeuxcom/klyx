//go:build !windows

package cachediff

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/gitcli"
)

// The confined build (slice 6 note §7.1 points 1, 2 and 5): this test
// binary is the child, as Klyx's own binary is in the app.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == ChildFlag {
		os.Exit(RunChild(os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func self(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// repo builds an input set over files, the build rooted at root.
func repo(root string, files map[string]string) (InputSet, map[string][]byte) {
	set := InputSet{Root: root, Files: map[string]gitcli.TreeEntry{}}
	contents := map[string][]byte{}
	for p, c := range files {
		set.Files[p] = gitcli.TreeEntry{Mode: "100644", Type: "blob", Path: p}
		contents[p] = []byte(c)
	}
	return set, contents
}

func names(t *testing.T, set InputSet, contents map[string][]byte, spec map[string]any) []string {
	t.Helper()
	objs, err := Build(context.Background(), self(t), set, contents, spec)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, o := range objs {
		out = append(out, o.GetKind()+" "+o.GetNamespace()+"/"+o.GetName())
	}
	slices.Sort(out)
	return out
}

const deployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  template:
    spec:
      containers:
        - name: web
          image: ghcr.io/acme/web:1.0
`

func TestTheChildBuildsWhatFluxWouldApply(t *testing.T) {
	set, contents := repo("clusters/prd", map[string]string{
		"clusters/prd/kustomization.yaml":     "resources: [../../apps/base]\nconfigMapGenerator:\n  - name: settings\n    files: [../../config/app.ini]\n",
		"apps/base/kustomization.yaml":        "resources: [deployment.yaml]\n",
		"apps/base/deployment.yaml":           deployment,
		"config/app.ini":                      "a=1",
		"components/extra/kustomization.yaml": "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\nresources: [cm.yaml]\n",
		"components/extra/cm.yaml":            "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: extra\n",
	})
	// A generator file in a parent directory loads, as it does for Flux.
	got := names(t, set, contents, nil)
	if len(got) != 2 || !strings.HasPrefix(got[0], "ConfigMap /settings-") || got[1] != "Deployment /web" {
		t.Fatalf("plain build %q", got)
	}
	// Flux's overlay: targetNamespace, namePrefix, images, a patch, a
	// component, and a missing component ignored.
	spec := map[string]any{
		"targetNamespace": "shop", "namePrefix": "prd-",
		"images":     []any{map[string]any{"name": "ghcr.io/acme/web", "newTag": "2.0"}},
		"patches":    []any{map[string]any{"patch": "- op: add\n  path: /metadata/labels\n  value: {tier: web}", "target": map[string]any{"kind": "Deployment"}}},
		"components": []any{"../../components/extra", "../../components/missing"}, "ignoreMissingComponents": true,
	}
	objs, err := Build(context.Background(), self(t), set, contents, spec)
	if err != nil {
		t.Fatal(err)
	}
	var dep, extra bool
	for _, o := range objs {
		if o.GetNamespace() != "shop" || !strings.HasPrefix(o.GetName(), "prd-") {
			t.Fatalf("%s %s/%s: the overlay was not applied", o.GetKind(), o.GetNamespace(), o.GetName())
		}
		if o.GetKind() == "Deployment" {
			dep = true
			cs, _, _ := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
			if len(cs) != 1 || cs[0].(map[string]any)["image"] != "ghcr.io/acme/web:2.0" || o.GetLabels()["tier"] != "web" {
				t.Fatalf("image or patch not applied: %v %v", cs, o.GetLabels())
			}
		}
		extra = extra || o.GetName() == "prd-extra"
	}
	if !dep || !extra {
		t.Fatalf("objects %v", objs)
	}
}

func TestAMissingKustomizationIsGeneratedAsFluxDoes(t *testing.T) {
	set, contents := repo("apps", map[string]string{
		"apps/web.yaml":                deployment,
		"apps/notes.txt":               "not a manifest",
		"apps/sub/kustomization.yaml":  "resources: [cm.yaml]\n",
		"apps/sub/cm.yaml":             "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: sub\n",
		"apps/sub/ignored-deeper.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: never\n",
	})
	if got := names(t, set, contents, nil); !slices.Equal(got, []string{"ConfigMap /sub", "Deployment /web"}) {
		t.Fatalf("generated build %q", got)
	}
}

// The parent's fixtures (§7): nothing outside the input set exists in the
// child, so an escape fails; and with no network and no PATH, a remote
// reference that slipped past the scan fails fast instead of fetching.
func TestTheChildIsConfined(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"a generator reading ../outside.txt": {"k/kustomization.yaml": "configMapGenerator: [{name: x, files: [../../outside.txt]}]\n"},
		"a nested base escaping the root":    {"k/kustomization.yaml": "resources: [../../../etc]\n"},
		"a remote file":                      {"k/kustomization.yaml": "resources: ['https://raw.githubusercontent.com/acme/x/main/cm.yaml']\n"},
		"a remote base":                      {"k/kustomization.yaml": "resources: ['https://github.com/acme/x//base?ref=main']\n"},
		"Helm":                               {"k/kustomization.yaml": "helmCharts: [{name: redis, repo: 'https://charts.example.test'}]\n"},
	} {
		t.Run(name, func(t *testing.T) {
			set, contents := repo("k", files)
			start := time.Now()
			_, err := Build(context.Background(), self(t), set, contents, nil)
			if err == nil {
				t.Fatal("the build succeeded")
			}
			if time.Since(start) > 20*time.Second {
				t.Fatalf("the refusal took %v", time.Since(start))
			}
		})
	}
}

// AI A1 implementation review, P1: kustomize's error for an invalid patch
// quotes the patch, values and all. The failure keeps that text for the
// operator's local diagnostic and carries a fixed class for a caller that
// must not echo it.
func TestABuildFailureHasAFixedClass(t *testing.T) {
	const needle = "PrivateHelmValueNeedle"
	patch := "- op: add\n  path: /spec/values\n  value: {featureMode: " + needle + "}\n- invalid\n"
	var k strings.Builder
	k.WriteString("resources: [object.yaml]\npatches:\n- patch: |-\n")
	for _, l := range strings.Split(strings.TrimSuffix(patch, "\n"), "\n") {
		k.WriteString("    " + l + "\n")
	}
	set, contents := repo("", map[string]string{"kustomization.yaml": k.String(),
		"object.yaml": "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: web\nspec:\n  values:\n    featureMode: " + needle + "\n"})
	_, err := Build(context.Background(), self(t), set, contents, nil)
	if err == nil || !errors.Is(err, ErrBuildFailed) {
		t.Fatalf("an invalid patch fails, classed: %v", err)
	}
	if !strings.Contains(err.Error(), needle) {
		t.Fatalf("the control: kustomize's own text quotes the value, so it is kept for local use only: %v", err)
	}
	for _, c := range []error{ErrInputMissing, ErrInputUnsupported, ErrInputUnreadable, ErrInputBound, ErrBuildStart, ErrBuildDeadline, ErrBuildOutput, ErrBuildDecode, ErrBuildFailed} {
		if strings.Contains(c.Error(), needle) || strings.Contains(c.Error(), "%") {
			t.Fatalf("a class is fixed text: %q", c)
		}
	}
}

func TestTheChildRefusesABadInput(t *testing.T) {
	tarOf := func(entries ...func(*tar.Writer)) []byte {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, e := range entries {
			e(tw)
		}
		_ = tw.Close()
		return buf.Bytes()
	}
	file := func(name, content string) func(*tar.Writer) {
		return func(tw *tar.Writer) {
			_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg})
			_, _ = tw.Write([]byte(content))
		}
	}
	link := func(tw *tar.Writer) {
		_ = tw.WriteHeader(&tar.Header{Name: "repo/k/link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	}
	req := file(requestName, `{"root":"k"}`)
	// RunChild refuses the network for its whole process, as the confined
	// child must; run in-process here, the test restores it afterwards so
	// later tests in this package can still reach their local API servers.
	transport, resolver := http.DefaultTransport, net.DefaultResolver
	t.Cleanup(func() { http.DefaultTransport, net.DefaultResolver = transport, resolver })
	for name, in := range map[string][]byte{
		"a symlink":        tarOf(req, link),
		"a parent path":    tarOf(req, file("repo/../etc/x", "x")),
		"an absolute path": tarOf(req, file("/etc/x", "x")),
		"outside repo/":    tarOf(req, file("other/x", "x")),
		"no request":       tarOf(file("repo/k/kustomization.yaml", "resources: []")),
		"a bad root":       tarOf(file(requestName, `{"root":"../x"}`)),
	} {
		var out, errb bytes.Buffer
		if code := RunChild(bytes.NewReader(in), &out, &errb); code == 0 {
			t.Errorf("%s was accepted", name)
		}
	}
}
