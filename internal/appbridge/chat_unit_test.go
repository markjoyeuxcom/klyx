package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

// AI A1 §3.1 and §8: gitops_unit parses a failed health check strictly,
// joins each named object to the unit's inventory by name, and reads the
// object of that name now as a separate fact - never the health check's
// subject.

const unitSince = "2026-10-21T12:05:00Z"

func unitObj(msg string, inventory ...string) *unstructured.Unstructured {
	var entries []any
	for _, id := range inventory {
		entries = append(entries, map[string]any{"id": id, "v": "v1"})
	}
	return uobj(map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "app", "namespace": "flux-system", "uid": "uid-ks", "generation": int64(3)},
		"spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "fleet"},
			"dependsOn": []any{map[string]any{"name": "infra"}}},
		"status": map[string]any{"observedGeneration": int64(3), "lastAppliedRevision": "main@sha1:aaa", "lastAttemptedRevision": "main@sha1:bbb",
			"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "HealthCheckFailed", "message": msg, "lastTransitionTime": unitSince}},
			"inventory":  map[string]any{"entries": entries}}})
}

func unitChat(u *unstructured.Unstructured) *fakeChatConn {
	f := newFakeChat()
	f.flux = map[string]*unstructured.Unstructured{u.GetKind() + "|" + u.GetNamespace() + "|" + u.GetName(): u}
	f.kinds["service"] = []fleet.ChatKind{{GVR: schema.GroupVersionResource{Version: "v1", Resource: "services"}, Kind: "Service", Namespaced: true}}
	f.rows = map[string]observe.Rows{healthsrc.TaskFlux + "/reconciliations": {Task: healthsrc.TaskFlux, Incarnation: 2, EvaluatedAt: fixedNow(),
		Part: observe.Part{ID: "reconciliations", Classifier: "native", Coverage: observe.Live, Ver: 4},
		Rows: healthsrc.RowSet[flux.Resource]{Rows: []healthsrc.Row[flux.Resource]{{
			Ref:  healthsrc.RowRef{UID: u.GetName() + "-uid-ignored", GVR: "kustomize.toolkit.fluxcd.io/v1/kustomizations", Namespace: u.GetNamespace(), Name: u.GetName()},
			Eval: health.NativeEval{Included: true, Role: health.Fault, Label: "stale row"},
		}, {
			Ref:  healthsrc.RowRef{UID: string(u.GetUID()), GVR: "kustomize.toolkit.fluxcd.io/v1/kustomizations", Namespace: u.GetNamespace(), Name: u.GetName()},
			Eval: health.NativeEval{Included: true, Role: health.Fault, Label: "HealthCheckFailed"},
		}}}}}
	return f
}

func deployObj(name, uid, created string) *unstructured.Unstructured {
	return uobj(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": "shop", "uid": uid, "creationTimestamp": created}})
}

func workloadRows(uid, label string) observe.Rows {
	return observe.Rows{Task: healthsrc.TaskWorkloads, Incarnation: 1, EvaluatedAt: fixedNow(),
		Part: observe.Part{ID: "workloads", Classifier: "verdict", Coverage: observe.Live, Ver: 2},
		Rows: healthsrc.RowSet[workloads.PodSummary]{Rows: []healthsrc.Row[workloads.PodSummary]{{
			Ref:  healthsrc.RowRef{UID: uid, GVR: "apps/v1/deployments", Namespace: "shop", Name: "web"},
			Data: workloads.PodSummary{Health: health.Verdict{Kind: health.Fault, Label: label}},
		}}}}
}

func unitData(t *testing.T, env chatEnvelope) chatUnitData {
	t.Helper()
	b, _ := json.Marshal(env.Data)
	var d chatUnitData
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	return d
}

func TestUnitParsesTheHealthCheckAndJoinsByName(t *testing.T) {
	msg := "health check failed after 5m0.03s: timeout waiting for: [Deployment/shop/web status: 'InProgress', Service/shop/web status: 'Current', Widget/shop/w status: 'InProgress']"
	f := unitChat(unitObj(msg, "shop_web_apps_Deployment", "shop_web__Service", "shop_w_a.example.com_Widget", "shop_w_b.example.com_Widget"))
	f.objects[key(deployGVR, "shop", "web")] = deployObj("web", "uid-web", "2026-10-20T09:00:00Z")
	f.rows[healthsrc.TaskWorkloads+"/workloads"] = workloadRows("uid-web", "CrashLoopBackOff")
	env, read := run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"app"}`)
	if read.Outcome != "completed" || env.Identity == nil || env.Identity.UID != "uid-ks" || env.Identity.GVR != "kustomize.toolkit.fluxcd.io/v1/kustomizations" {
		t.Fatalf("%+v %+v", read, env)
	}
	d := unitData(t, env)
	// the revisions cross as the S0b sanitiser leaves them (its scp-form URL
	// rule reads "main@sha1:" as user@host: and drops "main@")
	if !strings.HasSuffix(d.AppliedRevision, "sha1:aaa") || !strings.HasSuffix(d.AttemptedRevision, "sha1:bbb") || d.Source != "GitRepository flux-system/fleet" ||
		len(d.DependsOn) != 1 || d.DependsOn[0] != "flux-system/infra" || d.InventoryTotal != 4 || d.Inventory[0] != "apps/Deployment shop/web" ||
		d.Inventory[1] != "Service shop/web" || d.Ready == nil || d.Ready.Since != unitSince || d.Verdict == nil || d.Verdict.Label != "HealthCheckFailed" {
		t.Fatalf("the unit's facts: %+v", d)
	}
	hc := d.HealthCheck
	if hc == nil || hc.Failure != "timeout" || hc.Total != 3 || hc.Unparsed || len(hc.Entries) != 3 {
		t.Fatalf("%+v", hc)
	}
	web, svc, widget := hc.Entries[0], hc.Entries[1], hc.Entries[2]
	if web.Join != joinInventory || web.Group != "apps" || web.Current == nil || web.Current.UID != "uid-web" || web.Current.Caveat != "" ||
		web.Current.Verdict == nil || web.Current.Verdict.Label != "CrashLoopBackOff" {
		t.Fatalf("a joined, failing entry reads the object of that name now: %+v %+v", web, web.Current)
	}
	if svc.Join != joinInventory || svc.Group != "" || svc.Current != nil {
		t.Fatalf("a Current entry joins but is not read: %+v", svc)
	}
	if widget.Join != joinAmbiguous || widget.Group != "" || widget.Current != nil {
		t.Fatalf("a kind and name in two groups is not joined: %+v", widget)
	}
	var classes []string
	for _, c := range env.Checks {
		classes = append(classes, c.Class)
	}
	if strings.Join(classes, ",") != "condition,healthcheck,healthcheck,healthcheck,verdict,verdict" {
		t.Fatalf("%v", classes)
	}
	h := env.Checks[1]
	if h.Statement != "Kustomization flux-system/app reports a failed health check (timed out) naming Deployment shop/web (by name), status InProgress (from its Ready condition)" ||
		h.Subject.UID != "uid-ks" || h.Subject.Identity != identityUID || h.Onset != unitSince || h.ID != ".c2" {
		t.Fatalf("the check speaks of the unit, by its UID: %+v", h)
	}
	if !strings.Contains(env.Checks[3].Statement, "Widget shop/w (by name), status InProgress; "+joinAmbiguous) {
		t.Fatalf("%+v", env.Checks[3])
	}
	if env.Checks[4].Subject.UID != "uid-ks" || env.Checks[4].Statement != "Klyx's verdict for Kustomization flux-system/app: HealthCheckFailed" {
		t.Fatalf("the unit's verdict before the named objects': %+v", env.Checks[4])
	}
	cur := env.Checks[5]
	if cur.Subject.UID != "uid-web" || cur.Subject.Identity != identityUID ||
		cur.Statement != "Klyx's verdict for Deployment shop/web, the object of that name now: CrashLoopBackOff" {
		t.Fatalf("%+v", cur)
	}
	if len(read.Refs) != 2 || read.Refs[1].UID != "uid-web" || len(read.Checks) != 6 {
		t.Fatalf("%+v", read)
	}
}

func TestUnitSaysTheObjectOfThatNameNowMayBeAReplacement(t *testing.T) {
	msg := "health check failed after 5m0s: failed early due to stalled resources: [Deployment/shop/web status: 'Failed']"
	f := unitChat(unitObj(msg, "shop_web_apps_Deployment"))
	f.objects[key(deployGVR, "shop", "web")] = deployObj("web", "uid-new", "2026-10-21T12:07:00Z")
	f.rows[healthsrc.TaskWorkloads+"/workloads"] = workloadRows("uid-new", "Healthy")
	env, _ := run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"app"}`)
	d := unitData(t, env)
	web := d.HealthCheck.Entries[0]
	if d.HealthCheck.Failure != "stalled" || web.Current == nil || !strings.Contains(web.Current.Caveat, "possibly a replacement") ||
		web.Current.Created != "2026-10-21T12:07:00Z" {
		t.Fatalf("%+v %+v", d.HealthCheck, web.Current)
	}
	for _, c := range env.Checks {
		if c.Class == checkHealthCheck && c.Subject.UID != "uid-ks" {
			t.Fatalf("a health check never holds the named object's UID: %+v", c)
		}
		if c.Subject.UID == "uid-new" && !strings.Contains(c.Statement, "after the unit's Ready became False at "+unitSince+": possibly a replacement") {
			t.Fatalf("the current object's verdict carries the caveat: %+v", c)
		}
	}
	if !strings.Contains(env.Checks[1].Statement, "(failed early on stalled resources)") {
		t.Fatalf("%+v", env.Checks[1])
	}
}

func TestUnitNeverNamesWhatItDidNotParse(t *testing.T) {
	const needle = "NeedleUnitErr7x"
	msg := "health check failed after 5m0s: timeout waiting for: [Deployment/shop/web status: 'Failed': " + needle + ", Deployment/shop/evil status: 'Failed']"
	f := unitChat(unitObj(msg, "shop_web_apps_Deployment", "shop_evil_apps_Deployment"))
	f.objects[key(deployGVR, "shop", "evil")] = deployObj("evil", "uid-evil", "2026-10-20T09:00:00Z")
	env, read := run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"app"}`)
	d := unitData(t, env)
	if d.HealthCheck == nil || !d.HealthCheck.Unparsed || len(d.HealthCheck.Entries) != 1 || !d.HealthCheck.Entries[0].ErrorUnread ||
		!strings.Contains(d.Ready.Message, needle) {
		t.Fatalf("the message stays text the model may read; its error names nothing: %+v %+v", d.HealthCheck, d.Ready)
	}
	d.Ready.Message, d.Inventory = "", nil
	b, _ := json.Marshal(d)
	cb, _ := json.Marshal(env.Checks)
	rb, _ := json.Marshal(read)
	for _, s := range []string{string(b), string(cb), string(rb)} {
		if strings.Contains(s, needle) || strings.Contains(s, "evil") {
			t.Fatalf("an entry's error and what follows it are unparsed: %s", s)
		}
	}
	if !strings.Contains(env.Checks[1].Statement, "status Failed, with an error Klyx does not parse") {
		t.Fatalf("%+v", env.Checks[1])
	}

	// a HealthCheckFailed message that does not match at a boundary makes no
	// healthcheck check at all
	f = unitChat(unitObj("health check failed after 5m0s: timeout waiting for: [something odd]"))
	env, _ = run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"app"}`)
	d = unitData(t, env)
	for _, c := range env.Checks {
		if c.Class == checkHealthCheck {
			t.Fatalf("%+v", c)
		}
	}
	if d.HealthCheck == nil || !d.HealthCheck.Unparsed || len(d.HealthCheck.Entries) != 0 || d.HealthCheck.Total != 0 {
		t.Fatalf("%+v", d.HealthCheck)
	}
}

func TestUnitJoinsDeclaredHealthChecksAndSaysWhatIsMissing(t *testing.T) {
	msg := "health check failed after 5m0s: timeout waiting for: [Deployment/shop/web status: 'InProgress', Deployment/shop/gone status: 'InProgress', Deployment/other/x status: 'InProgress', Deployment/web status: 'InProgress']"
	u := unitObj(msg, "shop_cfg__ConfigMap")
	_ = unstructured.SetNestedSlice(u.Object, []any{
		map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "namespace": "shop", "name": "web"},
		map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "namespace": "shop", "name": "gone"},
		map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "web"},
	}, "spec", "healthChecks")
	f := unitChat(u)
	f.objects[key(deployGVR, "shop", "web")] = deployObj("web", "uid-web", "2026-10-20T09:00:00Z")
	env, _ := run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"app"}`)
	d := unitData(t, env)
	e := d.HealthCheck.Entries
	if e[0].Join != joinHealthChecks || e[0].Group != "apps" || e[0].Current == nil || e[0].Current.UID != "uid-web" {
		t.Fatalf("%+v", e[0])
	}
	if e[1].Join != joinHealthChecks || e[1].Current == nil || e[1].Current.Error != "no object of that name now" || e[1].Current.UID != "" {
		t.Fatalf("an object gone now says so, with no UID: %+v %+v", e[1], e[1].Current)
	}
	if e[2].Join != joinMissing || e[2].Current != nil || !strings.HasSuffix(env.Checks[3].Statement, "status InProgress; "+joinMissing+" (from its Ready condition)") {
		t.Fatalf("%+v %+v", e[2], env.Checks[3])
	}
	if e[3].Current == nil || e[3].Current.Error != "the entry's scope does not match the served kind: not read" {
		t.Fatalf("a cluster-scoped name for a namespaced kind is not read: %+v", e[3].Current)
	}

	// a unit with no inventory says so
	f = unitChat(unitObj(msg))
	d = unitData(t, f.runUnit(t))
	if d.HealthCheck.Entries[2].Join != joinNoInventory {
		t.Fatalf("%+v", d.HealthCheck.Entries[2])
	}
}

func (f *fakeChatConn) runUnit(t *testing.T) chatEnvelope {
	t.Helper()
	env, _ := run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"app"}`)
	return env
}

func TestUnitParsesOnlyAHealthCheckFailure(t *testing.T) {
	msg := "health check failed after 5m0s: timeout waiting for: [Deployment/shop/web status: 'InProgress']"
	for _, set := range []func(c map[string]any){
		func(c map[string]any) { c["reason"] = "ReconciliationFailed" },
		func(c map[string]any) { c["status"] = "True"; c["reason"] = "ReconciliationSucceeded" },
	} {
		u := unitObj(msg, "shop_web_apps_Deployment")
		conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		set(conds[0].(map[string]any))
		_ = unstructured.SetNestedSlice(u.Object, conds, "status", "conditions")
		f := unitChat(u)
		env := f.runUnit(t)
		if d := unitData(t, env); d.HealthCheck != nil {
			t.Fatalf("only a Ready=False HealthCheckFailed message is parsed: %+v", d.HealthCheck)
		}
		for _, c := range env.Checks {
			if c.Class == checkHealthCheck {
				t.Fatalf("%+v", c)
			}
		}
	}
}

func TestUnitReadsAtMostFiveNamedObjects(t *testing.T) {
	var entries, inv []string
	for i := 0; i < 8; i++ {
		entries = append(entries, fmt.Sprintf("Deployment/shop/w%d status: 'InProgress'", i))
		inv = append(inv, fmt.Sprintf("shop_w%d_apps_Deployment", i))
	}
	f := unitChat(unitObj("health check failed after 5m0s: timeout waiting for: ["+strings.Join(entries, ", ")+"]", inv...))
	for i := 0; i < 8; i++ {
		n := fmt.Sprintf("w%d", i)
		f.objects[key(deployGVR, "shop", n)] = deployObj(n, "uid-"+n, "2026-10-20T09:00:00Z")
	}
	env, read := run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"app"}`)
	d := unitData(t, env)
	n := 0
	for _, e := range d.HealthCheck.Entries {
		if e.Current != nil {
			n++
		}
	}
	if n != chatUnitCurrentMax || len(read.Refs) != 1+chatUnitCurrentMax || len(env.Checks) > chatChecksMax {
		t.Fatalf("%d current reads, %d refs, %d checks", n, len(read.Refs), len(env.Checks))
	}
}

func TestUnitHelmReleaseValuesNeverSent(t *testing.T) {
	const needle = "NeedleHelmValue9q"
	hr := uobj(map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-hr", "generation": int64(5)},
		"spec": map[string]any{"chart": map[string]any{"spec": map[string]any{"chart": "web", "sourceRef": map[string]any{"kind": "HelmRepository", "name": "charts", "namespace": "flux-system"}}},
			"values":     map[string]any{"password": needle},
			"valuesFrom": []any{map[string]any{"kind": "Secret", "name": "web-values", "valuesKey": needle}}},
		"status": map[string]any{"observedGeneration": int64(4),
			"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "UpgradeSucceeded", "message": "Helm upgrade succeeded", "lastTransitionTime": unitSince}}}})
	f := unitChat(hr)
	env, read := run(t, f, toolUnit, `{"kind":"HelmRelease","namespace":"shop","name":"web"}`)
	b, _ := json.Marshal(env)
	rb, _ := json.Marshal(read)
	if strings.Contains(string(b), needle) || strings.Contains(string(rb), needle) {
		t.Fatalf("a HelmRelease's values never cross: %s", b)
	}
	d := unitData(t, env)
	if read.Outcome != "completed" || d.Source != "HelmRepository flux-system/charts" || d.HealthCheck != nil || d.InventoryTotal != 0 ||
		env.Identity.GVR != "helm.toolkit.fluxcd.io/v2/helmreleases" || !strings.Contains(env.Note, "not yet reconciled generation 5") {
		t.Fatalf("%+v %+v", d, env)
	}
}

func TestUnitRefusesBeforeAnyRead(t *testing.T) {
	f := unitChat(unitObj("x"))
	for _, args := range []string{
		`{"kind":"Deployment","namespace":"shop","name":"web"}`,
		`{"kind":"Kustomization","name":"app"}`,
		`{"kind":"Kustomization","namespace":"flux-system","name":"app","group":"x"}`,
		`{"kind":"Kustomization","namespace":"flux system","name":"app"}`,
	} {
		env, read := run(t, f, toolUnit, args)
		if read.Outcome != "failed" || len(env.Errors) != 1 {
			t.Fatalf("%s: %+v", args, read)
		}
	}
	if f.fluxCalls != 0 {
		t.Fatalf("%d reads", f.fluxCalls)
	}
	env, read := run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"nope"}`)
	if read.Outcome != "failed" || !strings.Contains(env.Errors[0], "not found") {
		t.Fatalf("%+v %+v", env, read)
	}
}

func TestUnitShrinksItsInventoryFirst(t *testing.T) {
	var inv []string
	for i := 0; i < 140; i++ {
		inv = append(inv, fmt.Sprintf("shop_%s-%03d_apps_Deployment", strings.Repeat("n", 200), i))
	}
	msg := "health check failed after 5m0s: timeout waiting for: [Deployment/shop/web status: 'InProgress']"
	f := unitChat(unitObj(msg, append(inv, "shop_web_apps_Deployment")...))
	env, read := run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"app"}`)
	d := unitData(t, env)
	if !env.Truncated || read.Outcome != "truncated" || d.InventoryTotal != 141 || len(d.Inventory) >= chatUnitInventoryMax ||
		d.HealthCheck == nil || len(d.HealthCheck.Entries) != 1 || d.Ready == nil || len(env.Checks) == 0 {
		t.Fatalf("the inventory goes first; the health check and checks stay: %d shown of %d, %+v", len(d.Inventory), d.InventoryTotal, d.HealthCheck)
	}
}

// The A1 lab's retry: while Flux runs a health check again, Ready and
// Healthy read Unknown, Progressing and the message is gone; the unit's own
// HealthCheckFailed event (by its UID) still holds it, and status.history
// says when this revision first failed.
func TestUnitReadsARetryingUnitsFailureFromItsEvent(t *testing.T) {
	msg := "health check failed after 1m30s: timeout waiting for: [Deployment/shop/web status: 'InProgress']"
	u := unitObj("ignored")
	st := u.Object["status"].(map[string]any)
	st["conditions"] = []any{
		map[string]any{"type": "Ready", "status": "Unknown", "reason": "Progressing", "message": "Running health checks", "lastTransitionTime": "2026-10-21T12:43:13Z"},
		map[string]any{"type": "Healthy", "status": "Unknown", "reason": "Progressing", "lastTransitionTime": "2026-10-21T12:43:13Z"},
	}
	st["history"] = []any{
		map[string]any{"lastReconciledStatus": "HealthCheckFailed", "firstReconciled": "2026-10-21T12:37:43Z", "metadata": map[string]any{"revision": "main@sha1:bbb"}},
		map[string]any{"lastReconciledStatus": "ReconciliationSucceeded", "firstReconciled": "2026-10-21T12:30:00Z", "metadata": map[string]any{"revision": "main@sha1:aaa"}},
	}
	f := unitChat(u)
	f.events = []workloads.EventSummary{
		{Type: "Warning", Reason: "HealthCheckFailed", UID: "uid-other", Message: "health check failed: timeout waiting for: [Deployment/shop/elsewhere status: 'Failed']", LastSeenUnix: 1950000000},
		{Type: "Warning", Reason: "HealthCheckFailed", UID: "uid-ks", Message: "health check failed: timeout waiting for: [Deployment/shop/older status: 'InProgress']", LastSeenUnix: 1792586000},
		{Type: "Warning", Reason: "HealthCheckFailed", UID: "uid-ks", Message: msg, LastSeenUnix: 1792586683, FirstSeenUnix: 1792586683},
	}
	env, _ := run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"app"}`)
	d := unitData(t, env)
	if d.HealthCheck == nil || len(d.HealthCheck.Entries) != 1 || d.HealthCheck.Entries[0].Name != "web" || d.HealthCheck.FirstFailed != "2026-10-21T12:37:43Z" ||
		!strings.HasPrefix(d.HealthCheck.From, "Flux's HealthCheckFailed event at 2026-10-21T12:44:43Z, while the unit retries") {
		t.Fatalf("the newest event of this unit, never another's; first failed from history: %+v", d.HealthCheck)
	}
	var hc *ChatCheck
	for i := range env.Checks {
		if env.Checks[i].Class == checkHealthCheck {
			hc = &env.Checks[i]
		}
	}
	if hc == nil || hc.Onset != "2026-10-21T12:37:43Z" || hc.Observed != rfcOrEmpty(time.Unix(1792586683, 0)) || !strings.Contains(hc.Statement, "while the unit retries") {
		t.Fatalf("an event's check is observed at the event's time: %+v", hc)
	}
	// the failure's window: when this revision first failed, though Ready
	// reads Unknown
	a := anchorOf(&ChatSeedDTO{Kind: "object", Name: "app", Namespace: "flux-system", UID: "uid-ks"}, []ChatRead{{ID: "r1", Checks: env.Checks}})
	if !a.failing || a.start != "2026-10-21T12:37:43Z" {
		t.Fatalf("%+v", a)
	}
	if !strings.Contains(d.HealthCheck.From, "status.history records the attempted revision's health check failing since 2026-10-21T12:37:43Z") {
		t.Fatalf("the retry the event is read under is said: %q", d.HealthCheck.From)
	}
	f.events = nil
	if env, _ := run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"app"}`); unitData(t, env).HealthCheck != nil {
		t.Fatal("no condition and no event: nothing is parsed")
	}
	// AI A1 implementation review, finding 4: the event is the current
	// failure only during a retry history establishes, and only when seen
	// within it
	newest := workloads.EventSummary{Type: "Warning", Reason: "HealthCheckFailed", UID: "uid-ks", Message: msg, LastSeenUnix: 1792586683}
	for _, tc := range []struct {
		name  string
		mod   func(st map[string]any)
		event workloads.EventSummary
	}{
		{"an event from before the retry", nil, workloads.EventSummary{Type: "Warning", Reason: "HealthCheckFailed", UID: "uid-ks", Message: msg, LastSeenUnix: 1792586000}},
		{"no history", func(st map[string]any) { delete(st, "history") }, newest},
		{"the attempted revision last reconciled", func(st map[string]any) {
			st["history"] = append([]any{map[string]any{"lastReconciledStatus": "ReconciliationSucceeded", "firstReconciled": "2026-10-21T12:40:00Z",
				"metadata": map[string]any{"revision": "main@sha1:bbb"}}}, st["history"].([]any)...)
		}, newest},
		{"history of another revision only", func(st map[string]any) { st["history"] = st["history"].([]any)[1:] }, newest},
		{"another revision failing", func(st map[string]any) {
			st["history"] = []any{map[string]any{"lastReconciledStatus": "HealthCheckFailed", "firstReconciled": "2026-10-21T12:37:43Z",
				"metadata": map[string]any{"revision": "main@sha1:ccc"}}}
		}, newest},
	} {
		u := unitObj("ignored")
		st := u.Object["status"].(map[string]any)
		st["conditions"] = []any{map[string]any{"type": "Ready", "status": "Unknown", "reason": "Progressing", "lastTransitionTime": "2026-10-21T12:43:13Z"}}
		st["history"] = []any{
			map[string]any{"lastReconciledStatus": "HealthCheckFailed", "firstReconciled": "2026-10-21T12:37:43Z", "metadata": map[string]any{"revision": "main@sha1:bbb"}},
			map[string]any{"lastReconciledStatus": "ReconciliationSucceeded", "firstReconciled": "2026-10-21T12:30:00Z", "metadata": map[string]any{"revision": "main@sha1:aaa"}},
		}
		if tc.mod != nil {
			tc.mod(st)
		}
		f := unitChat(u)
		f.events = []workloads.EventSummary{tc.event}
		if env, _ := run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"app"}`); unitData(t, env).HealthCheck != nil {
			t.Fatalf("%s: the event is not this retry's failure", tc.name)
		}
	}
}

// AI A1 re-review, finding 2: an older health check is history when a
// different stage fails now, or when the current stage is not read; it is
// the current failure while the health check runs.
func TestUnitADifferentCurrentFailureKeepsTheHealthCheckHistorical(t *testing.T) {
	msg := "health check failed after 1m30s: timeout waiting for: [Deployment/shop/web status: 'InProgress']"
	for _, tc := range []struct {
		name  string
		conds []any
		want  bool
	}{
		{"Ready=False for a build", []any{map[string]any{"type": "Ready", "status": "False", "reason": "BuildFailed", "message": "kustomization is invalid", "lastTransitionTime": "2026-10-21T12:44:00Z"}}, false},
		{"a build failing beside an old Healthy=False", []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "BuildFailed", "message": "kustomization is invalid", "lastTransitionTime": "2026-10-21T12:44:00Z"},
			map[string]any{"type": "Healthy", "status": "False", "reason": "HealthCheckFailed", "message": msg, "lastTransitionTime": "2026-10-21T12:40:00Z"}}, false},
		{"no Ready condition", []any{map[string]any{"type": "Healthy", "status": "False", "reason": "HealthCheckFailed", "message": msg}}, false},
		{"the control: the health check runs now", []any{
			map[string]any{"type": "Ready", "status": "Unknown", "reason": "Progressing", "lastTransitionTime": "2026-10-21T12:43:13Z"},
			map[string]any{"type": "Healthy", "status": "False", "reason": "HealthCheckFailed", "message": msg, "lastTransitionTime": "2026-10-21T12:40:00Z"}}, true},
		{"the control: Ready=False for the health check", []any{map[string]any{"type": "Ready", "status": "False", "reason": "HealthCheckFailed", "message": msg, "lastTransitionTime": "2026-10-21T12:44:00Z"}}, true},
	} {
		u := unitObj("ignored")
		st := u.Object["status"].(map[string]any)
		st["conditions"] = tc.conds
		st["history"] = []any{map[string]any{"lastReconciledStatus": "HealthCheckFailed", "firstReconciled": "2026-10-21T12:37:43Z", "metadata": map[string]any{"revision": "main@sha1:bbb"}}}
		f := unitChat(u)
		f.events = []workloads.EventSummary{{Type: "Warning", Reason: "HealthCheckFailed", UID: "uid-ks", Message: msg, LastSeenUnix: 1792586500}}
		env, _ := run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"app"}`)
		if got := unitData(t, env).HealthCheck != nil; got != tc.want {
			t.Fatalf("%s: a current health-check failure %v, want %v", tc.name, got, tc.want)
		}
	}
}

// AI A1 re-review 2, finding 1: a condition that observed an earlier
// generation describes an earlier spec. A recovered unit at generation 3,
// Ready=True, with Healthy=False left from generation 2 and a live OK
// verdict, has no failure in force; the stale condition stays a fact that
// says its generation.
func TestUnitAStaleConditionIsNotAFailure(t *testing.T) {
	u := unitObj("")
	st := u.Object["status"].(map[string]any)
	healthy := map[string]any{"type": "Healthy", "status": "False", "reason": "HealthCheckFailed", "observedGeneration": int64(2), "lastTransitionTime": "2026-10-21T12:40:00Z",
		"message": "health check failed: timeout waiting for: [Deployment/shop/web status: 'InProgress']"}
	st["conditions"] = []any{
		map[string]any{"type": "Ready", "status": "True", "reason": "ReconciliationSucceeded", "observedGeneration": int64(3), "lastTransitionTime": "2026-10-21T12:50:00Z"},
		healthy,
	}
	f := unitChat(u)
	for key, rows := range f.rows {
		rs := rows.Rows.(healthsrc.RowSet[flux.Resource])
		for i := range rs.Rows {
			rs.Rows[i].Eval = health.NativeEval{Included: true, Role: health.OK, Label: "Ready"}
			rs.Rows[i].Ref.Generation = 3
		}
		rows.Rows = rs
		f.rows[key] = rows
	}
	read := func() (chatEnvelope, ChatRead) {
		r := newChatTools(f, fixedNow).as("r1").Run(context.Background(), toolUnit, json.RawMessage(`{"kind":"Kustomization","namespace":"flux-system","name":"app"}`))
		var env chatEnvelope
		if err := json.Unmarshal(r.JSON, &env); err != nil {
			t.Fatal(err)
		}
		return env, r.Read
	}
	env, rd := read()
	seed := &ChatSeedDTO{UID: "uid-ks", Namespace: "flux-system", Name: "app"}
	if a := anchorOf(seed, []ChatRead{rd}); a.failing || unitData(t, env).HealthCheck != nil {
		t.Fatalf("a recovered unit has no failure in force: %+v %+v", a, rd.Checks)
	}
	stale := false
	for _, c := range rd.Checks {
		stale = stale || (c.Class == checkCondition && strings.Contains(c.Statement, "Healthy=False") && strings.Contains(c.Statement, "(as of generation 2; the object is at 3)") && !c.Failing)
	}
	if !stale {
		t.Fatalf("the stale condition stays a fact, dated by its generation: %+v", rd.Checks)
	}
	// the control: the same condition at the current generation is in force
	healthy["observedGeneration"] = int64(3)
	if _, rd := read(); !anchorOf(seed, []ChatRead{rd}).failing {
		t.Fatalf("a current Healthy=False is a failure: %+v", rd.Checks)
	}
}

// AI A1 implementation review, finding 4: a Ready unit has no current
// health-check failure, whatever older event or condition holds.
func TestUnitRecoveredDoesNotReviveAnOldFailure(t *testing.T) {
	msg := "health check failed after 1m30s: timeout waiting for: [Deployment/shop/web status: 'InProgress']"
	u := unitObj("ignored")
	st := u.Object["status"].(map[string]any)
	st["conditions"] = []any{
		map[string]any{"type": "Ready", "status": "True", "reason": "ReconciliationSucceeded", "lastTransitionTime": "2026-10-21T12:50:00Z"},
		map[string]any{"type": "Healthy", "status": "False", "reason": "HealthCheckFailed", "message": msg, "lastTransitionTime": "2026-10-21T12:40:00Z"},
	}
	st["history"] = []any{map[string]any{"lastReconciledStatus": "HealthCheckFailed", "firstReconciled": "2026-10-21T12:37:43Z", "metadata": map[string]any{"revision": "main@sha1:bbb"}}}
	f := unitChat(u)
	f.events = []workloads.EventSummary{{Type: "Warning", Reason: "HealthCheckFailed", UID: "uid-ks", Message: msg, LastSeenUnix: 1792586683}}
	env, _ := run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"app"}`)
	if d := unitData(t, env); d.HealthCheck != nil {
		t.Fatalf("a Ready unit reuses an older failure as current: %+v", d.HealthCheck)
	}
	for _, c := range env.Checks {
		if c.Class == checkHealthCheck {
			t.Fatalf("%+v", c)
		}
	}
	// read from its Ready condition, the revision still first failed when
	// history says, not at the condition's latest transition
	st["conditions"] = []any{map[string]any{"type": "Ready", "status": "False", "reason": "HealthCheckFailed", "message": msg, "lastTransitionTime": "2026-10-21T12:44:43Z"}}
	env, _ = run(t, f, toolUnit, `{"kind":"Kustomization","namespace":"flux-system","name":"app"}`)
	if d := unitData(t, env); d.HealthCheck == nil || d.HealthCheck.FirstFailed != "2026-10-21T12:37:43Z" || d.HealthCheck.From != "its Ready condition" {
		t.Fatalf("%+v", d.HealthCheck)
	}
}
