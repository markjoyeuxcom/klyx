package cachediff

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/gitcli"
)

// The cache diff's input set (slice 6 note §7.1 point 4): rounds from
// spec.path over the kustomization files, every remote reference refused.

// fakeSource is a revision's tree: path -> content, with optional modes.
type fakeSource struct {
	files map[string]string
	modes map[string]string
	reads [][]string
}

func (f *fakeSource) List(_ context.Context, p string) ([]gitcli.TreeEntry, error) {
	var es []gitcli.TreeEntry
	for fp := range f.files {
		if p == "" || fp == p || strings.HasPrefix(fp, p+"/") {
			mode, typ := "100644", "blob"
			if m := f.modes[fp]; m != "" {
				mode = m
				if m == "160000" {
					typ = "commit"
				}
			}
			es = append(es, gitcli.TreeEntry{Mode: mode, Type: typ, ID: fmt.Sprintf("%040x", len(fp)), Path: fp})
		}
	}
	slices.SortFunc(es, func(a, b gitcli.TreeEntry) int { return strings.Compare(a.Path, b.Path) })
	return es, nil
}

func (f *fakeSource) Read(_ context.Context, files []gitcli.TreeEntry) (map[string][]byte, error) {
	var paths []string
	for _, e := range files {
		paths = append(paths, e.Path)
	}
	f.reads = append(f.reads, paths)
	out := map[string][]byte{}
	for _, p := range paths {
		c, ok := f.files[p]
		if !ok {
			return nil, fmt.Errorf("no %s", p)
		}
		out[p] = []byte(c)
	}
	return out, nil
}

func paths(s InputSet) []string {
	var ps []string
	for p := range s.Files {
		ps = append(ps, p)
	}
	slices.Sort(ps)
	return ps
}

func TestTheInputSetFollowsTheKustomizations(t *testing.T) {
	src := &fakeSource{files: map[string]string{
		"clusters/prd/kustomization.yaml": `
resources:
  - ../../apps/overlays/prd
  - extra.yaml
patches:
  - path: ../../patches/replicas.yaml
configMapGenerator:
  - name: env
    files: [settings=../../config/settings.ini]
    envs: [../../config/app.env]
`,
		"clusters/prd/extra.yaml":             "kind: ConfigMap",
		"apps/overlays/prd/kustomization.yml": "resources: [../../base]\ncomponents: [../../../components/monitoring]\n",
		"apps/overlays/prd/patch.yaml":        "x",
		"apps/base/kustomization.yaml":        "resources: [deployment.yaml, service.yaml]\n",
		"apps/base/deployment.yaml":           "kind: Deployment",
		"apps/base/service.yaml":              "kind: Service",
		"components/monitoring/Kustomization": "kind: Component\nresources: [monitor.yaml]\n",
		"components/monitoring/monitor.yaml":  "kind: ServiceMonitor",
		"patches/replicas.yaml":               "replicas: 3",
		"patches/unused.yaml":                 "never read",
		"config/settings.ini":                 "a=1",
		"config/app.env":                      "B=2",
		"other/unrelated.yaml":                "never read",
	}}
	set, err := Compute(context.Background(), src, "./clusters/prd/")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"apps/base/deployment.yaml", "apps/base/kustomization.yaml", "apps/base/service.yaml",
		"apps/overlays/prd/kustomization.yml", "apps/overlays/prd/patch.yaml",
		"clusters/prd/extra.yaml", "clusters/prd/kustomization.yaml",
		"components/monitoring/Kustomization", "components/monitoring/monitor.yaml",
		"config/app.env", "config/settings.ini", "patches/replicas.yaml",
	}
	if got := paths(set); !reflect.DeepEqual(got, want) || set.Root != "clusters/prd" || set.Generated {
		t.Fatalf("input set %q root %q generated %v", got, set.Root, set.Generated)
	}
	// Each round reads only its kustomization files.
	if len(src.reads) != 3 || !reflect.DeepEqual(src.reads[0], []string{"clusters/prd/kustomization.yaml"}) {
		t.Fatalf("reads %q", src.reads)
	}
}

func TestARootWithoutAKustomizationIsGenerated(t *testing.T) {
	src := &fakeSource{files: map[string]string{"apps/a.yaml": "kind: A", "apps/sub/b.yaml": "kind: B"}}
	set, err := Compute(context.Background(), src, "apps")
	if err != nil || !set.Generated || len(set.Files) != 2 {
		t.Fatalf("%+v %v", set, err)
	}
}

func TestTheInputSetRefuses(t *testing.T) {
	for name, c := range map[string]struct {
		files map[string]string
		modes map[string]string
		want  string
	}{
		"a remote base":      {files: map[string]string{"k/kustomization.yaml": "resources: ['https://github.com/acme/base//apps?ref=v1']"}, want: "remote"},
		"a github path":      {files: map[string]string{"k/kustomization.yaml": "resources: [github.com/acme/base/apps]"}, want: "remote"},
		"an scp base":        {files: map[string]string{"k/kustomization.yaml": "resources: [git@github.com:acme/base.git]"}, want: "remote"},
		"git::":              {files: map[string]string{"k/kustomization.yaml": "bases: [git::https://example.test/x]"}, want: "remote"},
		"a remote file":      {files: map[string]string{"k/kustomization.yaml": "patches: [{path: 'http://example.test/p.yaml'}]"}, want: "remote"},
		"a parent escape":    {files: map[string]string{"k/kustomization.yaml": "resources: [../../outside]"}, want: "leaves the repository"},
		"a generator escape": {files: map[string]string{"k/kustomization.yaml": "configMapGenerator: [{name: x, files: [../../outside.txt]}]"}, want: "leaves the repository"},
		"an absolute path":   {files: map[string]string{"k/kustomization.yaml": "resources: [/etc/passwd]"}, want: "absolute"},
		"Helm":               {files: map[string]string{"k/kustomization.yaml": "helmCharts: [{name: redis}]"}, want: "Helm"},
		"a missing base":     {files: map[string]string{"k/kustomization.yaml": "resources: [../gone]"}, want: "does not exist"},
		"a missing root":     {files: map[string]string{"other/x.yaml": "x"}, want: "does not exist"},
		"a symlink":          {files: map[string]string{"k/kustomization.yaml": "resources: [link.yaml]", "k/link.yaml": "../secret"}, modes: map[string]string{"k/link.yaml": "120000"}, want: "symbolic link"},
		"a submodule":        {files: map[string]string{"k/kustomization.yaml": "resources: []", "k/sub": ""}, modes: map[string]string{"k/sub": "160000"}, want: "submodule"},
		"garbage":            {files: map[string]string{"k/kustomization.yaml": "resources: {"}, want: "not a readable kustomization"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Compute(context.Background(), &fakeSource{files: c.files, modes: c.modes}, "k")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

func TestTheRoundsAreCapped(t *testing.T) {
	files := map[string]string{}
	for i := 0; i <= MaxRounds; i++ {
		files[fmt.Sprintf("l%d/kustomization.yaml", i)] = fmt.Sprintf("resources: [../l%d]", i+1)
	}
	files[fmt.Sprintf("l%d/kustomization.yaml", MaxRounds+1)] = "resources: []"
	if _, err := Compute(context.Background(), &fakeSource{files: files}, "l0"); err == nil || !strings.Contains(err.Error(), "rounds") {
		t.Fatalf("deep nesting: %v", err)
	}
	many := map[string]string{"k/kustomization.yaml": "resources: []"}
	for i := 0; i < MaxFiles; i++ {
		many[fmt.Sprintf("k/f%05d.yaml", i)] = "x"
	}
	if _, err := Compute(context.Background(), &fakeSource{files: many}, "k"); err == nil || !strings.Contains(err.Error(), "files") {
		t.Fatalf("too many files: %v", err)
	}
}

func TestRemoteMirrorsKustomize(t *testing.T) {
	for _, e := range []string{"https://x/y", "HTTP://x/y", "ssh://git@x/y", "file:///srv/x", "git::x", "git@github.com:a/b",
		"user-1@host:path", "github.com/acme/x", "GitHub.com:acme/x", "s3://bucket/x", "github.com/acme/x?ref=v1"} {
		if !Remote(e) {
			t.Errorf("%q is remote", e)
		}
	}
	for _, e := range []string{"../base", "v1.2/apps", "deployment.yaml", "gitlab.example.com/acme/x", "./github.com.yaml", "1user@x"} {
		if Remote(e) {
			t.Errorf("%q is local", e)
		}
	}
}
