package appbridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/helmcli"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

var evalAt = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// podRows is a pods part capture holding one crash-looping pod.
func podRows(uid string, gen int64, kinds ...observe.KindCoverage) observe.Rows {
	if kinds == nil {
		kinds = []observe.KindCoverage{{Kind: "pods", Role: "counted", Coverage: observe.Live}}
	}
	pod := workloads.PodSummary{
		Namespace: "shop", Name: "api-7f9", UID: "pod-uid", Generation: gen, IP: "10.1.2.3", OwnerKind: "ReplicaSet", OwnerName: "api-7f", OwnerUID: "rs-uid-secret-ish",
		Restarts: 9, Containers: []workloads.ContainerSummary{{Name: "api", Restarts: 9, State: "waiting: CrashLoopBackOff"}},
		Health: health.Verdict{Kind: health.Fault, Label: "CrashLoopBackOff", Reason: "pod.current-failure", Message: "back-off restarting failed container api", Rule: "P5"},
	}
	set := healthsrc.RowSet[workloads.PodSummary]{Rows: []healthsrc.Row[workloads.PodSummary]{{Ref: healthsrc.RowRef{UID: uid, GVR: "v1/pods", Namespace: "shop", Name: "api-7f9", Generation: gen}, Data: pod}}}
	return observe.Rows{Cluster: "c", Incarnation: 3, Task: healthsrc.TaskPods, EvaluatedAt: evalAt, Part: observe.Part{ID: "pods", Classifier: "verdict", Kinds: kinds}, Rows: set}
}

func podDetail(uid string, gen int64) crd.InstanceDetail {
	return crd.InstanceDetail{
		Kind: "Pod", Namespace: "shop", Name: "api-7f9", UID: uid, Generation: gen,
		Labels: map[string]string{"app": "label-sentinel"}, Annotations: map[string]string{"note": "annotation-sentinel"}, YAML: "yaml-sentinel",
		Conditions: []crd.Condition{{Type: "Ready", Status: "False", Reason: "ContainersNotReady", Message: "containers with unready status: [api]"}},
		Events:     []crd.Event{{Type: "Warning", Reason: "BackOff", Message: "Back-off restarting failed container", Count: 41, Last: evalAt}, {Type: "Normal", Reason: "Pulled", Message: "normal-event-sentinel"}},
	}
}

func explainSvc(cl *fakeCompleter, conn *fakeExplainConn) *AIService {
	return newAI(cl, "hosted", true, false, conn)
}

func rowTarget() ExplainTargetDTO {
	return ExplainTargetDTO{Kind: "row", Task: healthsrc.TaskPods, Part: "pods", UID: "pod-uid"}
}

// evidenceOf extracts the JSON evidence document from a captured prompt.
func evidenceOf(t *testing.T, user string) map[string]any {
	t.Helper()
	start, end := strings.Index(user, "<evidence>\n"), strings.LastIndex(user, "\n</evidence>")
	if start != 0 || end < 0 {
		t.Fatalf("the evidence is not delimited: %q", user)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(user[len("<evidence>\n"):end]), &m); err != nil {
		t.Fatalf("evidence is not JSON: %v", err)
	}
	return m
}

func TestExplain_RowIsAnchoredOnKlyxsVerdict(t *testing.T) {
	cl := &fakeCompleter{reply: "The container keeps exiting."}
	conn := &fakeExplainConn{rows: map[string]observe.Rows{"pods/pods": podRows("pod-uid", 1)}, details: map[string]crd.InstanceDetail{"/pods/shop/api-7f9": podDetail("pod-uid", 1)}}
	r := explainSvc(cl, conn).Explain("c", rowTarget())
	if !r.OK {
		t.Fatalf("want OK, got %+v", r)
	}
	if cl.gotSystem != explainSystem || strings.Contains(cl.gotSystem, "api-7f9") {
		t.Fatal("the system prompt must be Klyx's fixed text, with no cluster text")
	}
	ev := evidenceOf(t, cl.gotUser)
	v := ev["klyxVerdict"].(map[string]any)
	if v["label"] != "CrashLoopBackOff" || v["kind"] != "fault" || v["rule"] != "P5" {
		t.Fatalf("anchor: %v", v)
	}
	lens := ev["lensFields"].(map[string]any)
	if lens["restarts"] != float64(9) || lens["containers"] == nil {
		t.Fatalf("lens fields: %v", lens)
	}
	for _, absent := range []string{"10.1.2.3", "rs-uid-secret-ish", "label-sentinel", "annotation-sentinel", "yaml-sentinel", "normal-event-sentinel"} {
		if strings.Contains(cl.gotUser, absent) {
			t.Fatalf("a field outside the allowlist reached the prompt: %q", absent)
		}
	}
	if !strings.Contains(r.GroundedOn, `Klyx verdict "CrashLoopBackOff"`) {
		t.Fatalf("groundedOn: %q", r.GroundedOn)
	}
}

// Spec §4.1: a replacement between the capture and the GET is refused before
// any model call; so is a row gone from the capture, and a target without a
// UID.
func TestExplain_IdentityFence(t *testing.T) {
	cases := map[string]struct {
		conn   *fakeExplainConn
		target ExplainTargetDTO
	}{
		"replaced after capture": {&fakeExplainConn{rows: map[string]observe.Rows{"pods/pods": podRows("pod-uid", 1)}, details: map[string]crd.InstanceDetail{"/pods/shop/api-7f9": podDetail("new-uid", 1)}}, rowTarget()},
		"gone from the capture":  {&fakeExplainConn{rows: map[string]observe.Rows{"pods/pods": podRows("other-uid", 1)}}, rowTarget()},
		"no uid":                 {&fakeExplainConn{rows: map[string]observe.Rows{"pods/pods": podRows("pod-uid", 1)}}, ExplainTargetDTO{Kind: "row", Task: "pods", Part: "pods"}},
		"object replaced":        {&fakeExplainConn{details: map[string]crd.InstanceDetail{"/pods/shop/api-7f9": podDetail("new-uid", 1)}}, ExplainTargetDTO{Kind: "object", Version: "v1", Plural: "pods", Namespace: "shop", Name: "api-7f9", UID: "pod-uid"}},
		"object without uid":     {&fakeExplainConn{details: map[string]crd.InstanceDetail{"/pods/shop/api-7f9": podDetail("pod-uid", 1)}}, ExplainTargetDTO{Kind: "object", Version: "v1", Plural: "pods", Namespace: "shop", Name: "api-7f9"}},
		"no observer":            {&fakeExplainConn{}, rowTarget()},
		"unknown part":           {&fakeExplainConn{}, ExplainTargetDTO{Kind: "row", Task: "events", Part: "warnings", UID: "x"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cl := &fakeCompleter{reply: "x"}
			r := explainSvc(cl, tc.conn).Explain("c", tc.target)
			if r.OK || r.Error == "" {
				t.Fatalf("want a refusal, got %+v", r)
			}
			if cl.calls != 0 {
				t.Fatal("the model must not be called")
			}
		})
	}
}

// A generation change and a non-live dependency qualify the verdict.
func TestExplain_Qualifications(t *testing.T) {
	asOf := evalAt.Add(-10 * time.Minute)
	rows := podRows("pod-uid", 1, observe.KindCoverage{Kind: "pods", Role: "counted", Coverage: observe.Live}, observe.KindCoverage{Kind: "replicasets", Role: "dependency", Coverage: observe.Stale, AsOf: &asOf})
	cl := &fakeCompleter{reply: "x"}
	conn := &fakeExplainConn{rows: map[string]observe.Rows{"pods/pods": rows}, details: map[string]crd.InstanceDetail{"/pods/shop/api-7f9": podDetail("pod-uid", 2)}}
	if r := explainSvc(cl, conn).Explain("c", rowTarget()); !r.OK {
		t.Fatalf("got %+v", r)
	}
	q, _ := json.Marshal(evidenceOf(t, cl.gotUser)["qualifications"])
	for _, want := range []string{"generation 1", "generation 2", "replicasets (dependency) is stale"} {
		if !strings.Contains(string(q), want) {
			t.Fatalf("qualification %q missing: %s", want, q)
		}
	}
}

// Included is kept apart from Role, and the prompt says it never means
// failed (Codex review of the spec, change 5).
func TestExplain_NativeIncludedIsNotFault(t *testing.T) {
	res := flux.Resource{Kind: flux.KustomizationKind, Namespace: "flux-system", Name: "apps", Reason: "Progressing", Message: "reconciliation in progress"}
	set := healthsrc.RowSet[flux.Resource]{Rows: []healthsrc.Row[flux.Resource]{{
		Ref:  healthsrc.RowRef{UID: "ks-uid", GVR: "kustomize.toolkit.fluxcd.io/v1/kustomizations", Namespace: "flux-system", Name: "apps", Generation: 4},
		Eval: health.NativeEval{Included: true, Role: health.Progressing, Label: "Reconciling"},
		Data: res,
	}}}
	rows := observe.Rows{Task: healthsrc.TaskFlux, EvaluatedAt: evalAt, Part: observe.Part{ID: "reconciliations", Classifier: "native"}, Rows: set}
	detail := crd.InstanceDetail{Kind: "Kustomization", Namespace: "flux-system", Name: "apps", UID: "ks-uid", Generation: 4}
	cl := &fakeCompleter{reply: "x"}
	conn := &fakeExplainConn{rows: map[string]observe.Rows{"flux/reconciliations": rows}, details: map[string]crd.InstanceDetail{"kustomize.toolkit.fluxcd.io/kustomizations/flux-system/apps": detail}}
	r := explainSvc(cl, conn).Explain("c", ExplainTargetDTO{Kind: "row", Task: healthsrc.TaskFlux, Part: "reconciliations", UID: "ks-uid"})
	if !r.OK {
		t.Fatalf("got %+v", r)
	}
	v := evidenceOf(t, cl.gotUser)["klyxVerdict"].(map[string]any)
	if v["role"] != "progressing" || v["includedInAttention"] != true || v["kind"] != nil {
		t.Fatalf("native anchor: %v", v)
	}
	if !strings.Contains(cl.gotSystem, "Included never means failed") {
		t.Fatal("the prompt must say Included is not a fault")
	}
}

// A route's evaluation is its reduced attachment, not Eval.
func TestExplain_RouteAnchorFromAttachment(t *testing.T) {
	set := healthsrc.RowSet[observe.Attached]{Rows: []healthsrc.Row[observe.Attached]{{
		Ref:  healthsrc.RowRef{UID: "rt-uid", GVR: "gateway.networking.k8s.io/v1/httproutes", Namespace: "shop", Name: "api"},
		Data: observe.Attached{UID: "rt-uid", Namespace: "shop", Name: "api", Role: "fault", Label: "backend not found", Included: true},
	}}}
	rows := observe.Rows{Task: healthsrc.TaskGateway, EvaluatedAt: evalAt, Part: observe.Part{ID: "routes", Classifier: "native"}, Rows: set}
	cl := &fakeCompleter{reply: "x"}
	conn := &fakeExplainConn{rows: map[string]observe.Rows{"gateway/routes": rows}, details: map[string]crd.InstanceDetail{"gateway.networking.k8s.io/httproutes/shop/api": {Kind: "HTTPRoute", Namespace: "shop", Name: "api", UID: "rt-uid"}}}
	if r := explainSvc(cl, conn).Explain("c", ExplainTargetDTO{Kind: "row", Task: healthsrc.TaskGateway, Part: "routes", UID: "rt-uid"}); !r.OK {
		t.Fatalf("got %+v", r)
	}
	v := evidenceOf(t, cl.gotUser)["klyxVerdict"].(map[string]any)
	if v["role"] != "fault" || v["label"] != "backend not found" {
		t.Fatalf("route anchor: %v", v)
	}
}

func TestExplain_ObjectWithoutVerdict(t *testing.T) {
	cl := &fakeCompleter{reply: "x"}
	conn := &fakeExplainConn{details: map[string]crd.InstanceDetail{"/configmaps/shop/cfg": {Kind: "ConfigMap", Namespace: "shop", Name: "cfg", UID: "cm-uid"}}}
	r := explainSvc(cl, conn).Explain("c", ExplainTargetDTO{Kind: "object", Version: "v1", Plural: "configmaps", Namespace: "shop", Name: "cfg", UID: "cm-uid"})
	if !r.OK {
		t.Fatalf("got %+v", r)
	}
	ev := evidenceOf(t, cl.gotUser)
	if ev["klyxVerdict"] != nil || !strings.Contains(ev["noVerdict"].(string), "no verdict") {
		t.Fatalf("evidence: %v", ev)
	}
}

func TestExplain_HelmAnchorsOnTheRead(t *testing.T) {
	cl := &fakeCompleter{reply: "x"}
	conn := &fakeExplainConn{
		releases: []helmcli.Release{{Name: "shop", Namespace: "apps", Chart: "shop-1.2.0", Status: "failed", Revision: 7}},
		history: []helmcli.HistoryEntry{
			{Revision: 8, Status: "pending-upgrade", Description: "Upgrade with password=hunter2 in progress"},
			{Revision: 7, Status: "failed", Description: "Upgrade failed: timed out"},
		},
	}
	r := explainSvc(cl, conn).Explain("c", ExplainTargetDTO{Kind: "helm", Namespace: "apps", Name: "shop"})
	if !r.OK {
		t.Fatalf("got %+v", r)
	}
	ev := evidenceOf(t, cl.gotUser)
	if v := ev["klyxVerdict"].(map[string]any); v["role"] != "fault" {
		t.Fatalf("helm anchor: %v", v)
	}
	q, _ := json.Marshal(ev["qualifications"])
	if !strings.Contains(string(q), "revision 7 to 8") {
		t.Fatalf("a newer revision must qualify the verdict: %s", q)
	}
	if strings.Contains(cl.gotUser, "hunter2") {
		t.Fatal("a history description leaked a credential")
	}
}

// Spec §4.2 (Codex review change 7): a secret sentinel planted in EVERY
// allowed free-text surface, in each scrubbed shape, never reaches the prompt.
func TestExplain_SentinelsInEveryAllowedSurfaceAreScrubbed(t *testing.T) {
	shapes := map[string]string{
		"bearer":   "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123",
		"pem":      "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END PRIVATE KEY-----",
		"userinfo": "pull from https://deploy:s3cr3tpassw0rd@git.example.com/repo.git failed",
		"pair":     "connect failed: password=Sup3rS3cret!",
		"aws":      "creds AKIAABCDEFGHIJKLMNOP used",
		"github":   "token ghp_abcdefghijklmnopqrstuvwxyz0123456789 rejected",
		"jwt":      "bad token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
	}
	leaks := []string{"abcdefghijklmnopqrstuvwxyz0123", "MIIEvQIBADANBgkqhkiG9w0BAQEFAASC", "s3cr3tpassw0rd", "Sup3rS3cret!", "AKIAABCDEFGHIJKLMNOP", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"}
	for name, secret := range shapes {
		t.Run(name, func(t *testing.T) {
			rows := podRows("pod-uid", 1)
			set := rows.Rows.(healthsrc.RowSet[workloads.PodSummary])
			set.Rows[0].Data.Health.Message = "verdict " + secret
			set.Rows[0].Data.Health.Label = "label " + secret
			set.Rows[0].Data.Containers[0].State = "state " + secret
			rows.Rows = set
			d := podDetail("pod-uid", 1)
			d.Conditions[0].Message = "condition " + secret
			d.Conditions[0].Reason = "reason " + secret
			d.Events[0].Message = "event " + secret
			d.Facts = []crd.Fact{{Label: "fact " + secret, Value: "value " + secret}}
			d.Related = []crd.RelatedRef{{Version: "v1", Plural: "configmaps", Namespace: "shop", Name: "cfg", Relation: "mounts"}}
			related := crd.InstanceDetail{Kind: "ConfigMap", Namespace: "shop", Name: "cfg", UID: "cm", Conditions: []crd.Condition{{Type: "X", Status: "False", Message: "related " + secret}}}
			cl := &fakeCompleter{reply: "x"}
			conn := &fakeExplainConn{rows: map[string]observe.Rows{"pods/pods": rows}, details: map[string]crd.InstanceDetail{"/pods/shop/api-7f9": d, "/configmaps/shop/cfg": related}}
			r := explainSvc(cl, conn).Explain("c", rowTarget())
			if !r.OK {
				t.Fatalf("got %+v", r)
			}
			for _, leak := range leaks {
				if strings.Contains(cl.gotUser, leak) {
					t.Fatalf("%s: %q reached the prompt", name, leak)
				}
			}
			if r.Scrubbed == 0 {
				t.Fatal("the scrub count must be reported")
			}
		})
	}
}

// Per-field and total caps hold (spec §4.2).
func TestExplain_Caps(t *testing.T) {
	d := podDetail("pod-uid", 1)
	d.Conditions[0].Message = strings.Repeat("m", 5000)
	for i := 0; i < 200; i++ {
		d.Events = append(d.Events, crd.Event{Type: "Warning", Reason: "R", Message: strings.Repeat("e", 900), Last: evalAt.Add(time.Duration(i) * time.Second)})
	}
	cl := &fakeCompleter{reply: "x"}
	conn := &fakeExplainConn{rows: map[string]observe.Rows{"pods/pods": podRows("pod-uid", 1)}, details: map[string]crd.InstanceDetail{"/pods/shop/api-7f9": d}}
	if r := explainSvc(cl, conn).Explain("c", rowTarget()); !r.OK {
		t.Fatalf("got %+v", r)
	}
	body := cl.gotUser[len("<evidence>\n") : len(cl.gotUser)-len("\n</evidence>")]
	if len(body) > evidenceTotalMax {
		t.Fatalf("evidence %d bytes exceeds %d", len(body), evidenceTotalMax)
	}
	if strings.Contains(cl.gotUser, strings.Repeat("m", ai.EvidenceFieldMax+1)) {
		t.Fatal("a field exceeded its cap")
	}
	ev := evidenceOf(t, cl.gotUser)
	if evs, _ := ev["warningEvents"].([]any); len(evs) > evidenceListMax {
		t.Fatalf("%d events exceed the list cap", len(evs))
	}
}

func TestExplain_RefusesWhenTheProviderIsOffOrEgressIsRefused(t *testing.T) {
	conn := &fakeExplainConn{rows: map[string]observe.Rows{"pods/pods": podRows("pod-uid", 1)}, details: map[string]crd.InstanceDetail{"/pods/shop/api-7f9": podDetail("pod-uid", 1)}}
	if r := newAI(nil, "off", false, false, conn).Explain("c", rowTarget()); r.OK {
		t.Fatal("no provider must refuse")
	}
	cl := &fakeCompleter{reply: "x"}
	if r := newAI(cl, "hosted", false, true, conn).Explain("prd", rowTarget()); r.OK || !strings.Contains(r.Error, "protected") || cl.calls != 0 {
		t.Fatalf("a protected cluster without the opt-in must refuse: %+v", r)
	}
}

// List caps hold on their own (not only through the total cap).
func TestExplain_ListCaps(t *testing.T) {
	d := podDetail("pod-uid", 1)
	for i := 0; i < 30; i++ {
		d.Events = append(d.Events, crd.Event{Type: "Warning", Reason: "R", Message: "short", Last: evalAt.Add(time.Duration(i) * time.Second)})
		d.Conditions = append(d.Conditions, crd.Condition{Type: "C", Status: "True"})
	}
	cl := &fakeCompleter{reply: "x"}
	conn := &fakeExplainConn{rows: map[string]observe.Rows{"pods/pods": podRows("pod-uid", 1)}, details: map[string]crd.InstanceDetail{"/pods/shop/api-7f9": d}}
	if r := explainSvc(cl, conn).Explain("c", rowTarget()); !r.OK {
		t.Fatalf("got %+v", r)
	}
	ev := evidenceOf(t, cl.gotUser)
	if evs := ev["warningEvents"].([]any); len(evs) != evidenceListMax {
		t.Fatalf("want %d events, got %d", evidenceListMax, len(evs))
	}
	if conds := ev["target"].(map[string]any)["conditions"].([]any); len(conds) != 2*evidenceListMax {
		t.Fatalf("want %d conditions, got %d", 2*evidenceListMax, len(conds))
	}
}

// The whole document stays within evidenceTotalMax, shedding the least
// essential parts first; the anchor always stays.
func TestExplain_TotalCap(t *testing.T) {
	big := strings.Repeat("x", 600)
	d := podDetail("pod-uid", 1)
	for i := 0; i < 20; i++ {
		d.Facts = append(d.Facts, crd.Fact{Label: big, Value: big})
		d.Conditions = append(d.Conditions, crd.Condition{Type: big, Status: "True", Reason: big, Message: big})
	}
	related := crd.InstanceDetail{Kind: "ConfigMap", Namespace: "shop", Name: "cfg", UID: "cm"}
	for i := 0; i < 20; i++ {
		related.Conditions = append(related.Conditions, crd.Condition{Type: big, Status: "True", Message: big})
	}
	d.Related = []crd.RelatedRef{{Version: "v1", Plural: "configmaps", Namespace: "shop", Name: "cfg"}}
	cl := &fakeCompleter{reply: "x"}
	conn := &fakeExplainConn{rows: map[string]observe.Rows{"pods/pods": podRows("pod-uid", 1)}, details: map[string]crd.InstanceDetail{"/pods/shop/api-7f9": d, "/configmaps/shop/cfg": related}}
	if r := explainSvc(cl, conn).Explain("c", rowTarget()); !r.OK {
		t.Fatalf("got %+v", r)
	}
	body := cl.gotUser[len("<evidence>\n") : len(cl.gotUser)-len("\n</evidence>")]
	if len(body) > evidenceTotalMax {
		t.Fatalf("evidence %d bytes exceeds %d", len(body), evidenceTotalMax)
	}
	ev := evidenceOf(t, cl.gotUser)
	if ev["truncated"] != true || ev["klyxVerdict"] == nil {
		t.Fatalf("want a truncated document that keeps the anchor: truncated=%v verdict=%v", ev["truncated"], ev["klyxVerdict"])
	}
}
