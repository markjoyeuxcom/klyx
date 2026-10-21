// Package cachediff is The Line's cache diff (parent spec §7, slice 6 note
// §7 and §7.1): a Flux Kustomization built from the Git cache at the
// revision Flux is applying, confined, and diffed server-side against the
// station. This file computes the build's input set in rounds, from the
// kustomization files themselves, refusing every remote reference before
// anything is built.
package cachediff

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/moomora/klyx/internal/gitcli"
)

// The input set's caps (parent §7).
const (
	MaxRounds = 10
	MaxFiles  = 5000
	MaxBytes  = 64 << 20
)

// Source reads one revision of the cached repository.
type Source interface {
	// List returns the blobs under p, recursively, or p itself when it is a
	// file; none when it does not exist at the revision.
	List(ctx context.Context, p string) ([]gitcli.TreeEntry, error)
	// Read returns the contents of these files, by path, acquired as content
	// first: exactly their blob ids.
	Read(ctx context.Context, files []gitcli.TreeEntry) (map[string][]byte, error)
}

// InputSet is every file the build may read, by repo-relative path.
type InputSet struct {
	Root  string // spec.path, cleaned ("" is the repository root)
	Files map[string]gitcli.TreeEntry
	// Generated: the root has no kustomization file, so the build lists
	// its manifests as Flux's own generator does.
	Generated bool
}

// kustomizationNames are the files kustomize reads in a directory, in its
// order.
var kustomizationNames = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

// Compute walks the kustomization tree from spec.path: each round lists
// the directories added in the last one, reads their kustomization files
// and adds what those reference. At most MaxRounds rounds, MaxFiles files.
func Compute(ctx context.Context, src Source, specPath string) (InputSet, error) {
	root, err := gitcli.CleanPath(specPath)
	if err != nil {
		return InputSet{}, err
	}
	set := InputSet{Root: root, Files: map[string]gitcli.TreeEntry{}}
	listed := map[string]bool{} // directories whose subtree is in the set
	parsed := map[string]bool{} // directories whose kustomization file was read
	pending := []string{root}
	for round := 1; len(pending) > 0; round++ {
		if round > MaxRounds {
			return InputSet{}, class(ErrInputBound, fmt.Errorf("the kustomization nests deeper than %d rounds", MaxRounds))
		}
		kfiles := map[string]string{} // directory -> its kustomization file
		for _, d := range pending {
			if parsed[d] {
				continue
			}
			parsed[d] = true
			if !under(d, listed) {
				es, err := src.List(ctx, d)
				if err != nil {
					return InputSet{}, err
				}
				if len(es) == 0 {
					return InputSet{}, class(ErrInputMissing, fmt.Errorf("%s does not exist at this revision", show(d)))
				}
				for _, e := range es {
					if err := set.add(e); err != nil {
						return InputSet{}, err
					}
				}
				listed[d] = true
			}
			if k := kustomizationIn(set.Files, d); k != "" {
				kfiles[d] = k
			} else if d == root {
				set.Generated = true
			}
		}
		if len(set.Files) > MaxFiles {
			return InputSet{}, class(ErrInputBound, fmt.Errorf("the build reads more than %d files", MaxFiles))
		}
		var entries []gitcli.TreeEntry
		for _, d := range sortedKeys(kfiles) {
			entries = append(entries, set.Files[kfiles[d]])
		}
		contents, err := src.Read(ctx, entries)
		if err != nil {
			return InputSet{}, err
		}
		var next []string
		for _, d := range sortedKeys(kfiles) {
			dirs, files, err := references(d, contents[kfiles[d]])
			if err != nil {
				return InputSet{}, fmt.Errorf("%s: %w", kfiles[d], err)
			}
			for _, f := range files {
				if _, ok := set.Files[f]; ok || under(f, listed) {
					continue
				}
				es, err := src.List(ctx, f)
				if err != nil {
					return InputSet{}, err
				}
				switch {
				case len(es) == 0:
					return InputSet{}, class(ErrInputMissing, fmt.Errorf("%s: %s does not exist at this revision", kfiles[d], f))
				case len(es) == 1 && es[0].Path == f:
					if err := set.add(es[0]); err != nil {
						return InputSet{}, err
					}
				default: // a directory named where a file may be
					dirs = append(dirs, f)
				}
			}
			for _, sub := range dirs {
				if !parsed[sub] {
					next = append(next, sub)
				}
			}
		}
		pending = next
	}
	if len(set.Files) > MaxFiles {
		return InputSet{}, class(ErrInputBound, fmt.Errorf("the build reads more than %d files", MaxFiles))
	}
	return set, nil
}

// add refuses what kustomize's in-memory filesystem cannot hold.
func (s *InputSet) add(e gitcli.TreeEntry) error {
	switch e.Mode {
	case "120000":
		return class(ErrInputUnsupported, fmt.Errorf("%s is a symbolic link, which the confined build cannot follow", e.Path))
	case "160000":
		return class(ErrInputUnsupported, fmt.Errorf("%s is a submodule, which the cache does not hold", e.Path))
	}
	if e.Type != "blob" {
		return class(ErrInputUnsupported, fmt.Errorf("%s is a %s, not a file", e.Path, e.Type))
	}
	s.Files[e.Path] = e
	return nil
}

// kustomizationIn is d's own kustomization file, in kustomize's order.
func kustomizationIn(files map[string]gitcli.TreeEntry, d string) string {
	for _, n := range kustomizationNames {
		p := path.Join(d, n)
		if d == "" {
			p = n
		}
		if _, ok := files[p]; ok {
			return p
		}
	}
	return ""
}

// under reports whether p sits in a listed directory's subtree.
func under(p string, listed map[string]bool) bool {
	for d := range listed {
		if d == "" || p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

func show(p string) string {
	if p == "" {
		return "the repository root"
	}
	return p
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

// kfile is what the input set reads from a kustomization file.
type kfile struct {
	Resources             []string    `yaml:"resources"`
	Bases                 []string    `yaml:"bases"`
	Components            []string    `yaml:"components"`
	Crds                  []string    `yaml:"crds"`
	Configurations        []string    `yaml:"configurations"`
	PatchesStrategicMerge []string    `yaml:"patchesStrategicMerge"`
	Patches               []pathField `yaml:"patches"`
	PatchesJSON6902       []pathField `yaml:"patchesJson6902"`
	Replacements          []pathField `yaml:"replacements"`
	ConfigMapGenerator    []generator `yaml:"configMapGenerator"`
	SecretGenerator       []generator `yaml:"secretGenerator"`
	Generators            []string    `yaml:"generators"`
	Transformers          []string    `yaml:"transformers"`
	Validators            []string    `yaml:"validators"`
	OpenAPI               struct {
		Path string `yaml:"path"`
	} `yaml:"openapi"`
	HelmCharts                  []any `yaml:"helmCharts"`
	HelmGlobals                 any   `yaml:"helmGlobals"`
	HelmChartInflationGenerator []any `yaml:"helmChartInflationGenerator"`
}

type pathField struct {
	Path string `yaml:"path"`
}

type generator struct {
	Files []string `yaml:"files"`
	Envs  []string `yaml:"envs"`
	Env   string   `yaml:"env"`
}

// references reads a kustomization file in dir: the directories and files
// it names, resolved and kept inside the repository. A remote reference,
// Helm, or a path leaving the repository is refused, naming it.
func references(dir string, content []byte) (dirs, files []string, err error) {
	var k kfile
	if err := yaml.Unmarshal(content, &k); err != nil {
		return nil, nil, class(ErrInputUnreadable, fmt.Errorf("not a readable kustomization: %v", err))
	}
	if len(k.HelmCharts) > 0 || k.HelmGlobals != nil || len(k.HelmChartInflationGenerator) > 0 {
		return nil, nil, class(ErrInputUnsupported, fmt.Errorf("Helm charts are not built from the cache"))
	}
	resolve := func(entry string) (string, error) {
		if Remote(entry) {
			return "", class(ErrInputUnsupported, fmt.Errorf("the remote reference %q is not supported yet; remote bases are refused", entry))
		}
		if path.IsAbs(entry) {
			return "", class(ErrInputUnsupported, fmt.Errorf("%q is absolute", entry))
		}
		p := path.Clean(path.Join(dir, entry))
		if p == ".." || strings.HasPrefix(p, "../") {
			return "", class(ErrInputUnsupported, fmt.Errorf("%q leaves the repository", entry))
		}
		if p == "." {
			p = ""
		}
		return p, nil
	}
	// Directories or files: kustomize decides by what is there.
	either := append(append(append([]string{}, k.Resources...), k.Bases...), k.Components...)
	var only []string // files
	only = append(only, k.Crds...)
	only = append(only, k.Configurations...)
	for _, p := range k.PatchesStrategicMerge {
		if !strings.Contains(p, "\n") { // an inline patch reads no file
			only = append(only, p)
		}
	}
	for _, fs := range [][]pathField{k.Patches, k.PatchesJSON6902, k.Replacements} {
		for _, f := range fs {
			if f.Path != "" {
				only = append(only, f.Path)
			}
		}
	}
	for _, g := range append(append([]generator{}, k.ConfigMapGenerator...), k.SecretGenerator...) {
		for _, f := range g.Files {
			if _, p, ok := strings.Cut(f, "="); ok { // key=path
				f = p
			}
			only = append(only, f)
		}
		only = append(only, g.Envs...)
		if g.Env != "" {
			only = append(only, g.Env)
		}
	}
	for _, ps := range [][]string{k.Generators, k.Transformers, k.Validators} {
		for _, p := range ps {
			if !strings.Contains(p, "\n") { // an inline plugin config reads no file
				only = append(only, p)
			}
		}
	}
	if k.OpenAPI.Path != "" {
		only = append(only, k.OpenAPI.Path)
	}
	// Every path is resolved; a resource entry may be a file or a directory,
	// so it is listed with the files and classified by what the tree holds.
	for _, e := range append(either, only...) {
		p, err := resolve(e)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, p)
	}
	return nil, files, nil
}

var scpUser = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]*@`)

// Remote reports whether kustomize would load entry remotely, as its own
// loader reads one (kustomize api v0.21.3, internal/git/repospec.go and
// internal/loader/fileloader.go): a "git::" prefix or any query aside, a
// ssh://, https://, http:// or file:// URL, a user@ (scp-style) form, or a
// github.com/ or github.com: path. Any other scheme-like string is refused
// too: it cannot be a path in the repository.
func Remote(entry string) bool {
	n := strings.TrimSpace(entry)
	if strings.HasPrefix(strings.ToLower(n), "git::") {
		return true
	}
	n, _, _ = strings.Cut(n, "?")
	lower := strings.ToLower(n)
	for _, s := range []string{"ssh://", "https://", "http://", "file://"} {
		if strings.HasPrefix(lower, s) {
			return true
		}
	}
	return scpUser.MatchString(n) || strings.HasPrefix(lower, "github.com/") || strings.HasPrefix(lower, "github.com:") ||
		strings.Contains(n, "://")
}
