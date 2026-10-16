package cachediff

import (
	"context"
	"fmt"
	"github.com/moomora/klyx/internal/crd"
	"reflect"
	"sort"
	"strings"

	"github.com/fluxcd/pkg/ssa"
	"github.com/fluxcd/pkg/ssa/normalize"
	ssautil "github.com/fluxcd/pkg/ssa/utils"
	"github.com/pmezard/go-difflib/difflib"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// Change is one object's diff result (slice 6 note §7.1 point 6): what a
// server-side dry-run of the built object finds against the station - never
// a claim about delivery or cause.
type Change struct {
	Action    string `json:"action"` // created | configured | unchanged | skipped | would be pruned | unread | failed
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Diff      string `json:"diff,omitempty"`  // configured: the live object against the dry-run, Secret values masked
	Error     string `json:"error,omitempty"` // unread, failed
	// OmittedChanged names the sanitised sections (coverage S0b §6) whose
	// raw values differ between live and dry-run: a change the diff text
	// cannot show, because both sides read "<omitted…>".
	OmittedChanged []string `json:"omittedChanged,omitempty"`
}

// Diff actions beyond ssa's own.
const (
	ActionPruned = "would be pruned"
	ActionUnread = "unread"
	ActionFailed = "failed"
)

// fluxOwner is kustomize-controller's field manager, so a dry-run shows what
// Flux itself would change.
var fluxOwner = ssa.Owner{Field: "kustomize-controller", Group: "kustomize.toolkit.fluxcd.io"}

// reconcileDisabled is the label or annotation Flux skips objects by.
var reconcileDisabled = map[string]string{"kustomize.toolkit.fluxcd.io/reconcile": "disabled"}

// ServerSideDiff dry-runs each built object on the station's own connection
// with Flux's field manager, as flux diff does. Each live object is read
// first: a read that fails is that object's result, never an empty object
// diffed as "configured". With prune on, inventory entries the build no
// longer holds read "would be pruned".
func ServerSideDiff(ctx context.Context, cfg *rest.Config, objs []*unstructured.Unstructured, inventory []string, prune bool) ([]Change, error) {
	c, err := client.New(rest.CopyConfig(cfg), client.Options{})
	if err != nil {
		return nil, fmt.Errorf("connecting for the dry-run: %w", err)
	}
	if err := normalize.UnstructuredList(objs); err != nil {
		return nil, fmt.Errorf("normalising the built objects: %w", err)
	}
	sort.Sort(ssa.SortableUnstructureds(objs))
	rm := ssa.NewResourceManager(c, nil, fluxOwner)
	built := map[string]bool{}
	var out []Change
	for _, obj := range objs {
		ch := Change{Kind: obj.GetKind(), Namespace: obj.GetNamespace(), Name: obj.GetName()}
		built[inventoryID(obj.GetNamespace(), obj.GetName(), obj.GroupVersionKind().Group, obj.GetKind())] = true
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(obj.GroupVersionKind())
		if err := c.Get(ctx, client.ObjectKeyFromObject(obj), live); err != nil && !apierrors.IsNotFound(err) {
			ch.Action, ch.Error = ActionUnread, err.Error()
			out = append(out, ch)
			continue
		}
		entry, existing, dryRun, err := rm.Diff(ctx, obj, ssa.DiffOptions{Exclusions: reconcileDisabled})
		if err != nil {
			ch.Action, ch.Error = ActionFailed, err.Error()
			out = append(out, ch)
			continue
		}
		ch.Action = string(entry.Action)
		if entry.Action == ssa.ConfiguredAction && existing != nil && dryRun != nil {
			// Coverage S0b §6: compare the raw sections first, inside the
			// backend - only whether each changed is kept, never a digest or a
			// length - then sanitise both sides and diff the copies. The
			// dry-run above already ran on the real desired object, and ssa's
			// action stands as it is.
			ch.OmittedChanged = omittedChanges(existing, dryRun)
			if err := ssa.SanitizeUnstructuredData(existing, dryRun); err != nil {
				return nil, fmt.Errorf("masking %s: %w", ssautil.FmtUnstructured(obj), err)
			}
			gk := existing.GroupVersionKind()
			ls := crd.SanitizeEmbedded(gk.Group, gk.Kind, existing.Object)
			ds := crd.SanitizeEmbedded(gk.Group, gk.Kind, dryRun.Object)
			if ch.Diff, err = unifiedDiff(&unstructured.Unstructured{Object: ls.Object}, &unstructured.Unstructured{Object: ds.Object}); err != nil {
				return nil, err
			}
		}
		out = append(out, ch)
	}
	if prune {
		for _, id := range inventory {
			if built[id] {
				continue
			}
			ns, name, _, kind, ok := parseInventoryID(id)
			if !ok {
				continue
			}
			out = append(out, Change{Action: ActionPruned, Kind: kind, Namespace: ns, Name: name})
		}
	}
	return out, nil
}

// inventoryID is Flux's inventory entry id: namespace_name_group_kind.
func inventoryID(ns, name, group, kind string) string {
	return ns + "_" + name + "_" + group + "_" + kind
}

func parseInventoryID(id string) (ns, name, group, kind string, ok bool) {
	f := strings.Split(id, "_")
	if len(f) != 4 {
		return "", "", "", "", false
	}
	return f[0], f[1], f[2], f[3], true
}

// unifiedDiff is the live object against the dry-run, as YAML.
func unifiedDiff(live, dryRun *unstructured.Unstructured) (string, error) {
	a, err := yaml.Marshal(live.Object)
	if err != nil {
		return "", err
	}
	b, err := yaml.Marshal(dryRun.Object)
	if err != nil {
		return "", err
	}
	return difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: difflib.SplitLines(string(a)), B: difflib.SplitLines(string(b)),
		FromFile: "live", ToFile: "from Git (dry-run)", Context: 3})
}

// omittedChanges reports which sanitised paths (on either side) hold
// different raw values in live and dry-run. The last-applied snapshot is not
// reported: Flux never applies it.
func omittedChanges(live, dryRun *unstructured.Unstructured) []string {
	gk := live.GroupVersionKind()
	paths := map[string]bool{}
	for _, s := range []crd.Sanitized{crd.SanitizeEmbedded(gk.Group, gk.Kind, live.Object), crd.SanitizeEmbedded(gk.Group, gk.Kind, dryRun.Object)} {
		for _, p := range s.Paths {
			if !strings.Contains(p, "last-applied-configuration") {
				paths[p] = true
			}
		}
	}
	var out []string
	for p := range paths {
		a, aok := crd.ValueAt(live.Object, p)
		b, bok := crd.ValueAt(dryRun.Object, p)
		if aok != bok || !reflect.DeepEqual(a, b) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
