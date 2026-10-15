package cachediff

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/kustomize/api/konfig"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/provider"
	kustypes "sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/yaml"
)

// ChildFlag runs Klyx's own binary as the confined build (slice 6 note
// §7.1 point 1): `klyx --kustomize-build`, dispatched before the app
// starts, reading the input tar on stdin and writing the built YAML stream
// on stdout. Its environment is empty and every dial is refused, because
// kustomize's loader fetches http(s) content and runs git itself.
const ChildFlag = "--kustomize-build"

// The tar's layout: the request beside the repository's files.
const (
	requestName = ".klyx-request.json"
	repoDir     = "repo"
)

// buildRequest is what the child builds: the root and the Flux
// Kustomization's spec, whose overlay fields it applies as Flux's own
// generator does.
type buildRequest struct {
	Root string         `json:"root"`
	Spec map[string]any `json:"spec"`
}

// RunChild is the child's whole life. It returns the process exit code.
func RunChild(stdin io.Reader, stdout, stderr io.Writer) int {
	refuseNetwork()
	out, err := childBuild(stdin)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := stdout.Write(out); err != nil {
		return 1
	}
	return 0
}

// refuseNetwork: every dial through the default transport fails, and
// nothing in the child can reach a resolver either.
func refuseNetwork() {
	refuse := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("the confined build has no network")
	}
	http.DefaultTransport = &http.Transport{DialContext: refuse, Proxy: nil}
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: refuse}
}

func childBuild(stdin io.Reader) ([]byte, error) {
	req, fs, err := readInput(stdin)
	if err != nil {
		return nil, err
	}
	root := "/" + repoDir
	if req.Root != "" {
		root = path.Join(root, req.Root)
	}
	if err := applyOverlay(fs, root, req.Spec); err != nil {
		return nil, err
	}
	// The in-memory filesystem holds only the input set: no restriction
	// inside it is the repository-root confinement Flux applies (kustomize's
	// root-only would refuse a generator file in a parent directory, which
	// Flux loads). Plugins are builtins only, statically linked; no exec,
	// no Helm.
	opts := krusty.MakeDefaultOptions()
	opts.LoadRestrictions = kustypes.LoadRestrictionsNone
	opts.PluginConfig = kustypes.DisabledPluginConfig()
	m, err := krusty.MakeKustomizer(opts).Run(fs, root)
	if err != nil {
		return nil, fmt.Errorf("kustomize build failed: %w", err)
	}
	return m.AsYaml()
}

// readInput loads the tar into an in-memory filesystem: regular files only,
// no absolute path, no "..", within the caps.
func readInput(r io.Reader) (buildRequest, filesys.FileSystem, error) {
	fs := filesys.MakeFsInMemory()
	var req buildRequest
	var seenReq bool
	var total int64
	files := 0
	tr := tar.NewReader(io.LimitReader(r, MaxBytes+(4<<20))) // content plus headers
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return req, nil, fmt.Errorf("reading the input: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			return req, nil, fmt.Errorf("%s is not a regular file", h.Name)
		}
		if h.Size < 0 || total+h.Size > MaxBytes {
			return req, nil, fmt.Errorf("the input passes %d MiB", MaxBytes>>20)
		}
		total += h.Size
		data, err := io.ReadAll(io.LimitReader(tr, h.Size))
		if err != nil {
			return req, nil, err
		}
		if h.Name == requestName {
			if err := json.Unmarshal(data, &req); err != nil {
				return req, nil, fmt.Errorf("the build request: %w", err)
			}
			seenReq = true
			continue
		}
		rel, ok := strings.CutPrefix(h.Name, repoDir+"/")
		clean := path.Clean(rel)
		if !ok || path.IsAbs(rel) || clean != rel || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(rel, "\\") {
			return req, nil, fmt.Errorf("%q is not a path in the repository", h.Name)
		}
		if files++; files > MaxFiles {
			return req, nil, fmt.Errorf("the input has more than %d files", MaxFiles)
		}
		if err := fs.WriteFile("/"+repoDir+"/"+clean, data); err != nil {
			return req, nil, err
		}
	}
	if !seenReq {
		return req, nil, errors.New("no build request")
	}
	if c, err := filepathClean(req.Root); err != nil || c != req.Root {
		return req, nil, fmt.Errorf("the build root %q is not a repository path", req.Root)
	}
	return req, fs, nil
}

func filepathClean(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	c := path.Clean(p)
	if path.IsAbs(c) || c == ".." || strings.HasPrefix(c, "../") {
		return "", errors.New("leaves the repository")
	}
	return c, nil
}

// applyOverlay writes the root's kustomization as kustomize-controller's
// generator would (fluxcd/pkg/kustomize v1.43.0, GenerateManifest): find
// or generate it, then apply the Kustomization's targetNamespace,
// namePrefix, nameSuffix, patches, components (ignoreMissingComponents),
// patchesStrategicMerge, patchesJson6902, images and buildMetadata.
// commonMetadata is applied to the built objects by the caller, as the
// controller does after the build.
func applyOverlay(fs filesys.FileSystem, root string, spec map[string]any) error {
	data, kfile, generated, err := findOrGenerate(fs, root)
	if err != nil {
		return err
	}
	kus := kustypes.Kustomization{TypeMeta: kustypes.TypeMeta{APIVersion: kustypes.KustomizationVersion, Kind: kustypes.KustomizationKind}}
	if err := yaml.Unmarshal(data, &kus); err != nil {
		return fmt.Errorf("%s: %w", strings.TrimPrefix(kfile, "/"+repoDir+"/"), err)
	}
	if !generated && len(kus.Resources) == 0 {
		kus.BuildMetadata = []string{"originAnnotations"}
	}
	var s fluxSpec
	if b, err := json.Marshal(spec); err != nil {
		return err
	} else if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("the Kustomization's spec: %w", err)
	}
	if s.TargetNamespace != nil {
		kus.Namespace = *s.TargetNamespace
	}
	if s.NamePrefix != nil {
		kus.NamePrefix = *s.NamePrefix
	}
	if s.NameSuffix != nil {
		kus.NameSuffix = *s.NameSuffix
	}
	for _, p := range s.Patches {
		kus.Patches = append(kus.Patches, kustypes.Patch{Patch: p.Patch, Target: p.Target.adapt()})
	}
	for _, c := range s.Components {
		if Remote(c) || path.IsAbs(c) {
			return fmt.Errorf("component path '%s' must be local and relative", c)
		}
		if s.IgnoreMissingComponents && !fs.Exists(path.Join(root, c)) {
			continue
		}
		kus.Components = append(kus.Components, c)
	}
	for _, p := range s.PatchesStrategicMerge {
		kus.PatchesStrategicMerge = append(kus.PatchesStrategicMerge, kustypes.PatchStrategicMerge(p)) //nolint:staticcheck // Flux's generator writes the deprecated field, mirrored
	}
	for _, p := range s.PatchesJSON6902 {
		kus.PatchesJson6902 = append(kus.PatchesJson6902, kustypes.Patch{Patch: string(p.Patch), Target: p.Target.adapt()}) //nolint:staticcheck // as above
	}
	for _, im := range s.Images {
		if i := slices.IndexFunc(kus.Images, func(x kustypes.Image) bool { return x.Name == im.Name }); i >= 0 {
			if im.NewName != "" {
				kus.Images[i].NewName = im.NewName
			}
			if im.NewTag != "" || im.Digest != "" {
				kus.Images[i].NewTag, kus.Images[i].Digest = im.NewTag, im.Digest
			}
		} else {
			kus.Images = append(kus.Images, kustypes.Image{Name: im.Name, NewName: im.NewName, NewTag: im.NewTag, Digest: im.Digest})
		}
	}
	if len(s.BuildMetadata) > 0 {
		kus.BuildMetadata = s.BuildMetadata
	}
	out, err := yaml.Marshal(kus)
	if err != nil {
		return err
	}
	return fs.WriteFile(kfile, out)
}

// findOrGenerate reads the root's kustomization file, or generates one
// listing the root's manifests as Flux does: a subdirectory with its own
// kustomization file is one resource; any other .yaml or .yml file that
// decodes as Kubernetes objects is one; nothing found sets a placeholder
// namespace so the build is not empty.
func findOrGenerate(fs filesys.FileSystem, root string) ([]byte, string, bool, error) {
	for _, n := range konfig.RecognizedKustomizationFileNames() {
		if p := path.Join(root, n); fs.Exists(p) && !fs.IsDir(p) {
			data, err := fs.ReadFile(p)
			return data, p, false, err
		}
	}
	rf := provider.NewDefaultDepProvider().GetResourceFactory()
	var resources []string
	err := fs.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || p == root {
			return err
		}
		if info.IsDir() {
			for _, n := range konfig.RecognizedKustomizationFileNames() {
				if k := path.Join(p, n); fs.Exists(k) && !fs.IsDir(k) {
					resources = append(resources, "."+strings.TrimPrefix(p, root))
					return filepath.SkipDir
				}
			}
			return nil
		}
		if ext := path.Ext(p); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		data, err := fs.ReadFile(p)
		if err != nil {
			return err
		}
		if _, err := rf.SliceFromBytes(data); err != nil {
			return fmt.Errorf("failed to decode Kubernetes YAML from %s: %w", strings.TrimPrefix(p, "/"+repoDir+"/"), err)
		}
		resources = append(resources, "."+strings.TrimPrefix(p, root))
		return nil
	})
	if err != nil {
		return nil, "", true, err
	}
	kus := kustypes.Kustomization{TypeMeta: kustypes.TypeMeta{APIVersion: kustypes.KustomizationVersion, Kind: kustypes.KustomizationKind}}
	if len(resources) == 0 {
		kus.Namespace = "_placeholder"
	} else {
		kus.Resources = resources
	}
	data, err := yaml.Marshal(kus)
	return data, path.Join(root, konfig.DefaultKustomizationFileName()), true, err
}

// fluxSpec is the part of a Flux Kustomization's spec the overlay reads.
type fluxSpec struct {
	TargetNamespace         *string           `json:"targetNamespace"`
	NamePrefix              *string           `json:"namePrefix"`
	NameSuffix              *string           `json:"nameSuffix"`
	Patches                 []fluxPatch       `json:"patches"`
	Components              []string          `json:"components"`
	IgnoreMissingComponents bool              `json:"ignoreMissingComponents"`
	PatchesStrategicMerge   []json.RawMessage `json:"patchesStrategicMerge"`
	PatchesJSON6902         []fluxJSONPatch   `json:"patchesJson6902"`
	Images                  []fluxImage       `json:"images"`
	BuildMetadata           []string          `json:"buildMetadata"`
}

type fluxPatch struct {
	Patch  string        `json:"patch"`
	Target *fluxSelector `json:"target"`
}

type fluxJSONPatch struct {
	Patch  json.RawMessage `json:"patch"`
	Target *fluxSelector   `json:"target"`
}

type fluxImage struct {
	Name, NewName, NewTag, Digest string
}

type fluxSelector struct {
	Group, Version, Kind, Name, Namespace, LabelSelector, AnnotationSelector string
}

func (s *fluxSelector) adapt() *kustypes.Selector {
	if s == nil {
		return nil
	}
	out := &kustypes.Selector{Name: s.Name, Namespace: s.Namespace, LabelSelector: s.LabelSelector, AnnotationSelector: s.AnnotationSelector}
	out.Group, out.Version, out.Kind = s.Group, s.Version, s.Kind
	return out
}
