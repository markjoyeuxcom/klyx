package appbridge

import (
	"context"
	"fmt"
	"github.com/moomora/klyx/internal/urlscrub"
	"os"
	"strings"

	ssautil "github.com/fluxcd/pkg/ssa/utils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/cachediff"
	"github.com/moomora/klyx/internal/gitcli"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/line"
)

// The cache diff (The Line slice 6e; parent §7, slice 6 note §7.1): the
// Flux lens's "Compute diff" builds the Kustomization from the Git cache at
// the revision Flux would apply next, in a confined child, and dry-runs it
// on the station's own connection. It answers only when the source's
// repository is approved; the flux CLI path stays for every other.

// cacheStore is the Git cache as the cache diff uses it.
type cacheStore interface {
	cachediff.Store
	EnsureCommit(ctx context.Context, url, sha string) error
}

// childExe is Klyx's own binary, run as the confined build.
var childExe = os.Executable

// cacheDiff answers "Compute diff" from the cache, or says it does not
// (ok false) so the flux CLI path answers.
func (s *GitService) cacheDiff(ctx context.Context, conn GitOpsConn, ns, name string) (FluxDiffDTO, bool) {
	ks, err := conn.FluxObject(ctx, "Kustomization", ns, name)
	if err != nil {
		return FluxDiffDTO{}, false
	}
	// The M10-f gate, enforced here as the flux CLI path enforces it: a
	// healthy, unsuspended Kustomization heals drift each interval, so its
	// diff is empty or misleading. Nothing is fetched or built for it.
	if r := flux.ParseKustomization(ks); !r.Suspended && r.Ready != flux.Failed {
		return FluxDiffDTO{Available: true, Via: "cache", Error: "diff is only available for suspended or failing Kustomizations"}, true
	}
	srcKind, _, _ := unstructured.NestedString(ks.Object, "spec", "sourceRef", "kind")
	srcName, _, _ := unstructured.NestedString(ks.Object, "spec", "sourceRef", "name")
	srcNS, _, _ := unstructured.NestedString(ks.Object, "spec", "sourceRef", "namespace")
	if srcNS == "" {
		srcNS = ns
	}
	if srcKind != "GitRepository" {
		return FluxDiffDTO{}, false // the cache holds Git history only
	}
	src, err := conn.FluxObject(ctx, "GitRepository", srcNS, srcName)
	if err != nil {
		return FluxDiffDTO{}, false
	}
	rawURL, _, _ := unstructured.NestedString(src.Object, "spec", "url")
	repos, _ := s.approved()
	owner, _ := keyOwners(repos)
	url := owner[line.NormalizeRepo(rawURL)]
	if url == "" {
		return FluxDiffDTO{}, false // not approved: the flux CLI path
	}
	store, err := s.gitOn()
	if err != nil {
		return FluxDiffDTO{}, false
	}
	cs, ok := store.(cacheStore)
	if !ok {
		return FluxDiffDTO{}, false
	}
	// each fetch is admitted when it starts (F2b review, R1)
	cs = admittedStore{cacheStore: cs, s: s, what: "the cache diff"}
	d := FluxDiffDTO{Available: true, Via: "cache"}
	fail := func(format string, a ...any) (FluxDiffDTO, bool) {
		d.Error = gitcli.Scrub(fmt.Sprintf(format, a...))
		return d, true
	}

	// The fence (§7.1 point 3): the current spec and source, reconciled.
	if !observed(ks) {
		return fail("not reconciled yet: the Kustomization's current spec has not been observed by kustomize-controller")
	}
	if !observed(src) {
		return fail("not reconciled yet: the GitRepository's current spec has not been fetched by source-controller")
	}
	if !readyTrue(src) {
		return fail("not reconciled yet: the GitRepository is not Ready")
	}
	artifact, _, _ := unstructured.NestedString(src.Object, "status", "artifact", "revision")
	rev, ok := line.ParseGitRevision(artifact)
	if !ok {
		return fail("the GitRepository's artifact names no commit Klyx can read")
	}
	sha, err := gitcli.SHA(rev.SHA)
	if err != nil {
		return fail("%v", err)
	}
	d.Revision = rev.SHA

	if err := cs.EnsureCommit(ctx, url, rev.SHA); err != nil {
		return fail("fetching %s: %v", rev.SHA[:7], err)
	}
	specPath, _, _ := unstructured.NestedString(ks.Object, "spec", "path")
	source := cachediff.CacheSource{Store: cs, URL: url, Rev: sha}
	set, err := cachediff.Compute(ctx, source, specPath)
	if err != nil {
		return fail("%v", err)
	}
	var entries []gitcli.TreeEntry
	for _, e := range set.Files {
		entries = append(entries, e)
	}
	contents, err := source.Read(ctx, entries)
	if err != nil {
		return fail("%v", err)
	}
	exe, err := childExe()
	if err != nil {
		return fail("the confined build: %v", err)
	}
	spec, _, _ := unstructured.NestedMap(ks.Object, "spec")
	objs, err := cachediff.Build(ctx, exe, set, contents, spec)
	if err != nil {
		return fail("%v", err)
	}
	// commonMetadata is applied after the build, as the controller does.
	labels, _, _ := unstructured.NestedStringMap(ks.Object, "spec", "commonMetadata", "labels")
	annotations, _, _ := unstructured.NestedStringMap(ks.Object, "spec", "commonMetadata", "annotations")
	if len(labels) > 0 || len(annotations) > 0 {
		ssautil.SetCommonMetadata(objs, labels, annotations)
	}
	prune, _, _ := unstructured.NestedBool(ks.Object, "spec", "prune")
	changes, err := conn.ServerSideDiff(ctx, objs, inventoryIDs(ks), prune)
	if err != nil {
		return fail("the server-side dry-run: %v", err)
	}
	if _, has := spec["decryption"]; has {
		d.Notes = append(d.Notes, "SOPS-encrypted files are diffed as stored: Klyx never decrypts")
	}
	if _, has := spec["postBuild"]; has {
		d.Notes = append(d.Notes, "variables shown unsubstituted: postBuild substitution is not applied")
	}
	if set.Generated {
		d.Notes = append(d.Notes, "no kustomization file at the path: one was generated from its manifests, as Flux does")
	}
	var out strings.Builder
	for _, c := range changes {
		errText := urlscrub.Text(c.Error).Text
		d.Changes = append(d.Changes, FluxDiffChangeDTO{Action: c.Action, Kind: c.Kind, Namespace: c.Namespace, Name: c.Name,
			Diff: c.Diff, Error: errText, OmittedChanged: c.OmittedChanged})
		if c.Action == "unchanged" || c.Action == "skipped" {
			continue
		}
		d.HasChanges = true
		fmt.Fprintf(&out, "► %s %s %s\n", c.Kind, objectName(c.Namespace, c.Name), c.Action)
		if errText != "" {
			fmt.Fprintf(&out, "  %s\n", errText)
		}
		if c.Diff != "" {
			out.WriteString(c.Diff)
			if !strings.HasSuffix(c.Diff, "\n") {
				out.WriteByte('\n')
			}
		}
		if len(c.OmittedChanged) > 0 {
			fmt.Fprintf(&out, "  these sections changed (omitted: may carry secrets): %s\n", strings.Join(c.OmittedChanged, ", "))
		}
	}
	d.Output = out.String()
	return d, true
}

func objectName(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "/" + name
}

// observed: status.observedGeneration has reached metadata.generation.
func observed(u *unstructured.Unstructured) bool {
	og, found, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	return found && og == u.GetGeneration()
}

// readyTrue: the Ready condition is True.
func readyTrue(u *unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if ok && m["type"] == "Ready" {
			return m["status"] == "True"
		}
	}
	return false
}

// inventoryIDs are the Kustomization's inventory entries (ns_name_group_kind).
func inventoryIDs(ks *unstructured.Unstructured) []string {
	entries, _, _ := unstructured.NestedSlice(ks.Object, "status", "inventory", "entries")
	var ids []string
	for _, e := range entries {
		if m, ok := e.(map[string]any); ok {
			if id, ok := m["id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}
