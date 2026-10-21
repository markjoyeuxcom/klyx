package appbridge

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/fleet"
)

var errMetricsDown = fmt.Errorf("%w: down", fleet.ErrChatMetricsUnavailable)

// AI A1 §4 and §8: Klyx labels every finding from its own ledger -
// provenance, subject identity, time, coverage - and A-C1's counterexamples
// come out labelled as they must.

func ledgerFixture() []ChatRead {
	pod := ChatCheckSubject{Kind: "Pod", Namespace: "shop", Name: "web", UID: "uid-p", Identity: identityUID}
	return []ChatRead{
		{ID: "r1", Tool: toolGet, Target: "Pod shop/web", Outcome: "completed", Refs: []ChatRef{{GVR: "v1/pods", Namespace: "shop", Name: "web", UID: "uid-p"}},
			Checks: []ChatCheck{
				{ID: "r1.c1", Class: checkStatus, Subject: pod, Statement: "container web of Pod shop/web: waiting ImagePullBackOff", Observed: "2026-10-21T12:06:00Z", Coverage: "live"},
				{ID: "r1.c2", Class: checkCondition, Subject: pod, Statement: "Pod shop/web: Ready=False, reason ContainersNotReady", Observed: "2026-10-21T12:06:00Z", Onset: "2026-10-21T12:05:00Z", Coverage: "live"},
				{ID: "r1.c3", Class: checkEvent, Subject: pod, Statement: "Warning BackOff on Pod shop/web (3 times)", Observed: "2026-10-21T11:00:00Z", Coverage: "live"},
			}},
		{ID: "r2", Tool: toolChanges, Target: "Kustomization flux-system/apps", Outcome: "completed", Checks: []ChatCheck{
			{ID: "r2.c1", Class: checkDiff, Subject: ChatCheckSubject{Kind: "Service", Namespace: "shop", Name: "web", Identity: identityName},
				Statement: "the source render of revision 2222222 changes Service shop/web (by name), against 1111111: metadata.annotations[acme.io/rollout], changed (not shown) (a source render under the current spec, the historical spec unknown)",
				Observed:  "2026-10-21T12:00:00Z", Coverage: "render"}}},
		{ID: "r3", Tool: toolMetrics, Target: "pod_cpu shop/web", Outcome: "failed", Detail: "metrics unavailable on this cluster"},
		{ID: "r4", Tool: toolLogs, Target: "shop/web/web", Outcome: "truncated", Refs: []ChatRef{{GVR: "v1/pods", Namespace: "shop", Name: "web", UID: "uid-p"}}},
		{ID: "r5", Tool: toolChanges, Target: "Kustomization flux-system/apps", Outcome: "completed", Detail: "the render comparison was not established"},
		{ID: "r6", Tool: toolUnit, Target: "Kustomization flux-system/apps", Outcome: "completed", Checks: []ChatCheck{
			{ID: "r6.c1", Class: checkHealthCheck, Statement: "Kustomization flux-system/apps reports a failed health check (timed out) naming Widget shop/w (by name), status InProgress; " + joinAmbiguous}}},
	}
}

func finding(typ, kind, ns, name, statement string, cites ...string) string {
	if cites == nil {
		cites = []string{}
	}
	b, _ := json.Marshal(map[string]any{"type": typ, "statement": statement, "subject": map[string]any{"kind": kind, "namespace": ns, "name": name}, "cites": cites})
	return string(b)
}

func labelled(t *testing.T, findings ...string) FindingsDTO {
	t.Helper()
	in, err := parseFindings(json.RawMessage(`{"findings":[` + strings.Join(findings, ",") + `],"missing":["could not read the registry"],"remedy":{"git":"pin the image to 1.0; token ghp_abcdefghijklmnopqrstuvwxyz0123456789"}}`))
	if err != nil {
		t.Fatal(err)
	}
	ledger := ledgerFixture()
	return labelFindings(in, ledger, anchorOf(&podAnchor, ledger))
}

func TestFindingsACounterexamples(t *testing.T) {
	d := labelled(t,
		// an annotation-only diff beside an independent failure
		finding("cause", "Service", "shop", "web", "the annotation change broke the service", "r2.c1"),
		// a new image failing to pull because the registry is down
		finding("cause", "Pod", "shop", "web", "the new image is broken", "r1.c1"),
		// a real ledger ID cited for an unrelated object
		finding("observation", "Deployment", "shop", "api", "api is down", "r1.c1"),
		// a check older than the failure
		finding("observation", "Pod", "shop", "web", "the pod was backing off", "r1.c3"),
	)
	if d.Window != "2026-10-21T12:05:00Z" {
		t.Fatalf("the failure's window starts at the anchor's Ready=False: %q", d.Window)
	}
	annotation, pull, unrelated, older := d.Findings[0], d.Findings[1], d.Findings[2], d.Findings[3]
	if annotation.Label != labelSuspected || !strings.Contains(annotation.Checked[0].Statement, "metadata.annotations[acme.io/rollout], changed (not shown)") ||
		!contains(annotation.Notes, findingIdentityByName) {
		t.Fatalf("a cause stays suspected, its diff check names the annotation only, by name: %+v", annotation)
	}
	if pull.Label != labelSuspected || contains(pull.Notes, findingIdentityByName) || pull.Checked[0].Statement != "container web of Pod shop/web: waiting ImagePullBackOff" {
		t.Fatalf("a pull failure's cause is suspected, never proven: %+v", pull)
	}
	if unrelated.Label != labelUnsupported || !strings.Contains(strings.Join(unrelated.Notes, ";"), "none of its citations is about Deployment shop/api") {
		t.Fatalf("a real ID about another object does not support it: %+v", unrelated)
	}
	if older.Label != labelReading || !older.Checked[0].Before || !strings.Contains(strings.Join(older.Notes, ";"), "observed before the failure was reported") {
		t.Fatalf("evidence from before the failure is shown as such: %+v", older)
	}
	for _, f := range d.Findings {
		if f.Label == "checked" || f.Label == "proven" {
			t.Fatal("a model finding is never checked or proven")
		}
	}
}

func TestFindingsProvenanceCoverageAndMissing(t *testing.T) {
	d := labelled(t,
		finding("observation", "Pod", "shop", "web", "made up", "r9", "r1.c1"),
		finding("observation", "Pod", "shop", "web", "no evidence"),
		finding("observation", "Pod", "shop", "web", "the logs were cut", "r4"),
	)
	if d.Findings[0].Label != labelUnsupported || !strings.Contains(d.Findings[0].Notes[0], "r9, not in this investigation's reads") {
		t.Fatalf("%+v", d.Findings[0])
	}
	if d.Findings[1].Label != labelUnsupported || d.Findings[1].Notes[0] != "cites no evidence" {
		t.Fatalf("%+v", d.Findings[1])
	}
	if d.Findings[2].Label != labelReading || !strings.Contains(strings.Join(d.Findings[2].Notes, ";"), "cites r4, a read that was truncated") {
		t.Fatalf("a read cited by its ID, about the subject by UID, with its coverage: %+v", d.Findings[2])
	}
	var missing []string
	for _, m := range d.Missing {
		missing = append(missing, m.From+":"+m.Read)
	}
	if strings.Join(missing, ",") != "klyx:r3,klyx:r4,klyx:r5,klyx:r6.c1,model:" {
		t.Fatalf("Klyx's missing evidence from its ledger, then the model's: %v", missing)
	}
	if d.Remedy == nil || d.Remedy.Label != proposedNotRun || strings.Contains(d.Remedy.Git, "ghp_abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Fatalf("%+v", d.Remedy)
	}
	ledger := ledgerFixture()[1:] // no anchor read: not failing
	if w := labelFindings(findingsInput{}, ledger, anchorOf(&podAnchor, ledger)).Window; w != "no failure to date" {
		t.Fatalf("%q", w)
	}
}

func TestFindingsInputIsStrict(t *testing.T) {
	long := strings.Repeat("x", findingStatementMax+1)
	for _, raw := range []string{
		`{"findings":[` + finding("guess", "Pod", "shop", "web", "x", "r1") + `]}`,
		`{"findings":[` + finding("observation", "Pod", "shop", "web", long, "r1") + `]}`,
		`{"findings":[` + finding("observation", "Pod", "shop", "web", "x", "r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8", "r9", "r10", "r11") + `]}`,
		`{"findings":[],"verdict":"proven"}`,
		`{"findings":[` + finding("observation", "Pod", "shop", "bad name", "x", "r1") + `]}`,
	} {
		if _, err := parseFindings(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted: %.120s", raw)
		}
	}
}

func TestReportFindingsInATurn(t *testing.T) {
	bad := ai.ToolCall{ID: "f1", Name: toolFindings, Args: json.RawMessage(`{"findings":[{"type":"guess"}]}`)}
	good := ai.ToolCall{ID: "f2", Name: toolFindings, Args: json.RawMessage(`{"findings":[` + finding("observation", "Pod", "shop", "web", "the pod is not ready", "r1") + `]}`)}
	again := ai.ToolCall{ID: "f3", Name: toolFindings, Args: good.Args}
	f := anchoredFixture(t, calls(bad), calls(good, ai.ToolCall{ID: "o1", Name: toolOverview, Args: json.RawMessage(`{}`)}), calls(again), answer("done"))
	inv := f.svc.StartInvestigation("x", podAnchor)
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	ex := st.Exchanges[0]
	if ex.Findings == nil || len(ex.Findings.Findings) != 1 || ex.Findings.Findings[0].Label != labelReading {
		t.Fatalf("the retried report is recorded: %+v", ex.Findings)
	}
	for _, r := range ex.Reads {
		if r.Tool == toolFindings {
			t.Fatal("report_findings is not a read")
		}
	}
	res := func(i int) string { return f.prov.reqs[i].Messages[len(f.prov.reqs[i].Messages)-1].Results[0].Content }
	if !strings.Contains(res(1), "correct it and call once more") || !strings.Contains(res(2), `"label":"reading"`) || !strings.Contains(res(3), "already reported") {
		t.Fatalf("%s | %s | %s", res(1), res(2), res(3))
	}
	if !hasTool(f.prov.reqs[0].Tools, toolFindings) {
		t.Fatal("an investigation offers report_findings")
	}

	c := anchoredFixture(t, answer("chat"))
	cs := c.svc.Start("x")
	c.svc.Ask("x", cs.ConversationID, "hi", nil)
	if hasTool(c.prov.reqs[0].Tools, toolFindings) {
		t.Fatal("a chat does not")
	}
}

func TestAnInvestigationWithoutReportedFindingsStillShowsKlyxsMissingEvidence(t *testing.T) {
	f := anchoredFixture(t, calls(ai.ToolCall{ID: "m", Name: toolMetrics, Args: json.RawMessage(`{"template":"pod_cpu","namespace":"shop","name":"web"}`)}), answer("no findings"))
	f.conn.seriesErr = errMetricsDown
	inv := f.svc.StartInvestigation("x", podAnchor)
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	fd := st.Exchanges[0].Findings
	if fd == nil || fd.Note != "the model reported no findings this turn" || len(fd.Missing) != 1 || fd.Missing[0].From != "klyx" {
		t.Fatalf("%+v", fd)
	}
}

func hasTool(ts []ai.Tool, name string) bool {
	for _, t := range ts {
		if t.Name == name {
			return true
		}
	}
	return false
}
