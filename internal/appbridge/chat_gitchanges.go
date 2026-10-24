package appbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/gitcli"
	"github.com/moomora/klyx/internal/line"
)

// AI A1 §3.2: git_changes says what changed in Git between a Kustomization's
// last applied (healthy) revision and its last attempted one: the commits
// and files under spec.path, and the two revisions rendered and compared
// object by object. Each render is a source render under today's spec,
// never the applied objects: the spec in force at the applied revision is
// unknown. Raw file content is never sent.

const (
	toolChanges = "git_changes"

	// chatChangesResultMax caps git_changes' result (A1 §1).
	chatChangesResultMax      = 32 * 1024
	chatChangesStatementPaths = 5
	chatResolveMax            = 30 // kinds resolved for one comparison
)

const historicalSpecUnknown = "a source render under the current spec: the spec in force at the applied revision is unknown"

type chatCommit struct {
	SHA     string `json:"sha"`
	Author  string `json:"author"`
	Time    string `json:"time"`
	Subject string `json:"subject"`
}

type chatRenderInfo struct {
	// State is compared, or not established (the files and commits stand).
	State          string   `json:"state"`
	Reason         string   `json:"reason,omitempty"`
	Qualifications []string `json:"qualifications,omitempty"`
}

type chatChangesData struct {
	Applied      string             `json:"appliedRevision"`
	BaseFrom     string             `json:"baseFrom"` // where the compared-from revision came from
	Attempted    string             `json:"attemptedRevision"`
	Path         string             `json:"path,omitempty"`
	Ancestor     bool               `json:"appliedIsAncestor"`
	Commits      []chatCommit       `json:"commits,omitempty"`
	CommitsTotal int                `json:"commitsTotal"`
	Files        []string           `json:"files,omitempty"`
	FilesTotal   int                `json:"filesTotal"`
	Render       chatRenderInfo     `json:"render"`
	Objects      []chatRenderChange `json:"objects,omitempty"`
	ObjectsTotal int                `json:"objectsTotal"`
	Note         string             `json:"note"`
}

func (t *chatTools) changes(ctx context.Context, ns, name string) ChatToolResult {
	target := "Kustomization " + ns + "/" + name
	ks, err := t.conn.FluxObject(ctx, "Kustomization", ns, name)
	if err != nil {
		return t.failed(toolChanges, "api", target, err)
	}
	if ks.GetKind() != "Kustomization" {
		return t.refuse(toolChanges, target, fmt.Errorf("the object read is a %s, not a Kustomization", label(ks.GetKind())))
	}
	gv, _ := schema.ParseGroupVersion(ks.GetAPIVersion())
	gvr := gv.WithResource("kustomizations")
	uid := string(ks.GetUID())
	env := chatEnvelope{Tool: toolChanges, Source: "git", ReadAt: t.stamp(),
		Identity: &chatIdentity{GVR: gvrString(gvr), Namespace: ns, Name: name, UID: uid, Generation: ks.GetGeneration()}}
	read := ChatRead{Tool: toolChanges, Target: target, Outcome: "completed", Refs: []ChatRef{{GVR: gvrString(gvr), Namespace: ns, Name: name, UID: uid}}}
	p := &projector{}
	stop := func(note string) ChatToolResult {
		env.Complete, env.Note = true, note
		read.Detail = note
		return t.finishWithin(env, read, chatChangesResultMax)
	}

	// the revisions, read in process from the raw status (the sanitised
	// copy's URL rule drops a revision's ref)
	appliedRaw, _, _ := unstructured.NestedString(ks.Object, "status", "lastAppliedRevision")
	attemptedRaw, _, _ := unstructured.NestedString(ks.Object, "status", "lastAttemptedRevision")
	applied, okA := line.ParseGitRevision(appliedRaw)
	attempted, okB := line.ParseGitRevision(attemptedRaw)
	baseFrom := "lastAppliedRevision"
	if okB && (!okA || applied.SHA == attempted.SHA) {
		// Flux records a revision as applied once a reconcile of it succeeds,
		// health check included; a later failure does not undo that. Its
		// history says which other revision last reconciled successfully.
		if prev, ok := lastOtherSucceeded(ks, attempted.SHA); ok {
			applied, okA, baseFrom = prev, true, "status.history: the newest other revision Flux reconciled successfully"
		}
	}
	switch {
	case !okB:
		return stop("The unit records no attempted Git revision, so there is nothing to compare; nothing more was read.")
	case !okA:
		return stop("The unit records no other revision it applied or reconciled successfully, so there is nothing to compare; nothing more was read.")
	case applied.SHA == attempted.SHA:
		return stop(fmt.Sprintf("The applied and attempted revisions are the same commit (%s), and Flux's history records no other revision reconciled successfully: there is no pair to compare. Flux records a revision as applied once a reconcile of it succeeds; a later failure does not undo that. Nothing more was read.", applied.SHA[:7]))
	}
	srcKind, _, _ := unstructured.NestedString(ks.Object, "spec", "sourceRef", "kind")
	srcName, _, _ := unstructured.NestedString(ks.Object, "spec", "sourceRef", "name")
	srcNS, _, _ := unstructured.NestedString(ks.Object, "spec", "sourceRef", "namespace")
	if srcNS == "" {
		srcNS = ns
	}
	if srcKind != "GitRepository" {
		return stop(fmt.Sprintf("The unit's source is a %s, not a GitRepository: Klyx reads Git history only; nothing more was read.", label(srcKind)))
	}
	if t.git == nil {
		return t.failed(toolChanges, "git", target, errors.New("Git history is not available in this session"))
	}
	src, err := t.conn.FluxObject(ctx, "GitRepository", srcNS, srcName)
	if err != nil {
		return t.failed(toolChanges, "api", "GitRepository "+srcNS+"/"+srcName, err)
	}
	repoURL, _, _ := unstructured.NestedString(src.Object, "spec", "url")
	spec, _, _ := unstructured.NestedMap(ks.Object, "spec")
	// the comparison runs inside the render admission; only what it keeps
	// outlives the renders
	var objects []chatRenderChange
	objectsTotal := 0
	out, err := t.git.changes(ctx, gitChangesReq{RepoURL: repoURL, Applied: applied.SHA, Attempted: attempted.SHA, Spec: spec,
		Compare: func(old, new []*unstructured.Unstructured) {
			objects, objectsTotal = compareRenders(old, new, t.resolver(ctx), p)
		}})
	if err != nil {
		if errors.Is(err, errChatGitNotApproved) {
			return t.refuse(toolChanges, target, err)
		}
		return t.failed(toolChanges, "git", target, errors.New(gitcli.Scrub(readError(err))))
	}

	specPath, _ := spec["path"].(string)
	data := chatChangesData{Applied: applied.SHA, BaseFrom: baseFrom, Attempted: attempted.SHA, Path: p.str(specPath, chatStringMax), Ancestor: out.Ancestor,
		CommitsTotal: out.CommitsTotal, FilesTotal: out.FilesTotal,
		Note: "Commits and files are those under the unit's path; a change in a base outside it shows in the render, not here."}
	commitTime := ""
	for _, c := range out.Commits {
		data.Commits = append(data.Commits, chatCommit{SHA: c.SHA, Author: p.str(c.Author, 200), Time: rfcOrEmpty(c.Time), Subject: p.str(c.Subject, 200)})
		if c.SHA == attempted.SHA {
			commitTime = rfcOrEmpty(c.Committed)
		}
	}
	for _, f := range out.Files {
		data.Files = append(data.Files, p.str(f.Status+" "+f.Path, 300))
	}
	if !out.Ancestor {
		data.Note += " The applied revision is not an ancestor of the attempted one: history was rewritten or the branch changed."
	}
	env.Complete = out.CommitsTotal <= len(out.Commits) && out.FilesTotal <= len(out.Files)

	quals := []string{historicalSpecUnknown}
	partial := false
	if _, has := spec["postBuild"]; has {
		quals, partial = append(quals, "partial: postBuild substitution is not applied, so variables stay unsubstituted"), true
	}
	if _, has := spec["decryption"]; has {
		quals, partial = append(quals, "partial: SOPS is not decrypted; an encrypted field that changed reads changed (encrypted)"), true
	}
	if out.Generated {
		quals = append(quals, "no kustomization file at the path: one was generated from its manifests, as Flux does")
	}
	if og, ok, _ := unstructured.NestedInt64(ks.Object, "status", "observedGeneration"); ok && ks.GetGeneration() > og {
		quals = append(quals, fmt.Sprintf("the spec changed since kustomize-controller last observed it (generation %d, observed %d)", ks.GetGeneration(), og))
	}
	if out.RenderErr != "" {
		data.Render = chatRenderInfo{State: "not established", Reason: p.str(out.RenderErr, chatStringMax), Qualifications: quals}
		env.Complete = false
		read.Detail = "the render comparison was not established"
		env.Data = data
		return t.finishWithin(env, read, chatChangesResultMax)
	}
	data.Render = chatRenderInfo{State: "compared", Qualifications: quals}
	data.Objects, data.ObjectsTotal = objects, objectsTotal
	if data.ObjectsTotal > len(data.Objects) {
		env.Complete = false
	}
	qualification := "a source render under the current spec, the historical spec unknown"
	coverage := "render"
	if partial {
		qualification += "; partial"
		coverage = "partial"
	}
	var checks []ChatCheck
	for _, o := range data.Objects {
		subj := ChatCheckSubject{Kind: o.Kind, Namespace: o.Namespace, Name: o.Name, Identity: identityName}
		verb := map[string]string{"added": "adds", "removed": "removes", "changed": "changes"}[o.Change]
		st := fmt.Sprintf("the source render of revision %s %s %s, against %s", attempted.SHA[:7], verb, subj.words(), applied.SHA[:7])
		if o.Change == "changed" {
			st += ": " + changedPaths(o)
		}
		checks = append(checks, ChatCheck{Class: checkDiff, Subject: subj, Statement: st + " (" + qualification + ")", Observed: commitTime, Coverage: coverage})
	}
	env.Checks = capChecks(checks)
	env.Data = data
	read.Redacted = p.redacted
	read.Detail = fmt.Sprintf("%d commits, %d files, %d objects changed", out.CommitsTotal, out.FilesTotal, data.ObjectsTotal)
	return t.finishWithin(env, read, chatChangesResultMax)
}

// lastOtherSucceeded is the newest revision in a Kustomization's
// status.history, other than sha, whose reconcile succeeded.
func lastOtherSucceeded(ks *unstructured.Unstructured, sha string) (line.GitRev, bool) {
	hist, _, _ := unstructured.NestedSlice(ks.Object, "status", "history")
	for _, h := range hist { // newest first
		m, ok := h.(map[string]any)
		if !ok {
			continue
		}
		status, _ := m["lastReconciledStatus"].(string)
		raw, _, _ := unstructured.NestedString(m, "metadata", "revision")
		rev, ok := line.ParseGitRevision(raw)
		if ok && status == "ReconciliationSucceeded" && rev.SHA != sha {
			return rev, true
		}
	}
	return line.GitRev{}, false
}

// changedPaths names an object's first changed paths, visible then hidden.
func changedPaths(o chatRenderChange) string {
	var parts []string
	for _, v := range o.Visible {
		parts = append(parts, v.Path)
	}
	for _, h := range o.Hidden {
		parts = append(parts, h.Path+", "+h.Change)
	}
	more := o.PathsTotal - len(parts)
	if len(parts) > chatChangesStatementPaths {
		more += len(parts) - chatChangesStatementPaths
		parts = parts[:chatChangesStatementPaths]
	}
	s := strings.Join(parts, "; ")
	if more > 0 {
		s += fmt.Sprintf("; +%d more", more)
	}
	return s
}

// resolver names a rendered kind's resource for chat's projection: the
// served resource of that group and kind, or a plural guess for a kind the
// cluster does not serve (its projection is then the generic one).
func (t *chatTools) resolver(ctx context.Context) chatResolve {
	seen := map[string]schema.GroupVersionResource{}
	return func(gv schema.GroupVersion, kind string) schema.GroupVersionResource {
		key := gv.Group + "/" + kind
		if r, ok := seen[key]; ok {
			return r
		}
		r := gv.WithResource(pluralGuess(kind))
		if len(seen) < chatResolveMax {
			if ks, _, err := t.conn.ChatResolveKind(ctx, kind, gv.Group); err == nil {
				for _, k := range ks {
					if k.GVR.Group == gv.Group && k.Kind == kind {
						r = gv.WithResource(k.GVR.Resource)
						break
					}
				}
			}
		}
		seen[key] = r
		return r
	}
}

func pluralGuess(kind string) string {
	k := strings.ToLower(kind)
	switch {
	case strings.HasSuffix(k, "y") && !strings.HasSuffix(k, "ay") && !strings.HasSuffix(k, "ey"):
		return k[:len(k)-1] + "ies"
	case strings.HasSuffix(k, "s"), strings.HasSuffix(k, "x"), strings.HasSuffix(k, "ch"):
		return k + "es"
	}
	return k + "s"
}
