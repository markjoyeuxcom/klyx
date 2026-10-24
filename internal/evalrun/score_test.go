package evalrun

import (
	"testing"

	"github.com/moomora/klyx/internal/appbridge"
	"github.com/moomora/klyx/internal/evalrun/record"
)

// The scorer's corpus (eval design §6): synthetic exchanges with Klyx's
// labels, through the statuses and marks the design defines.

func e1() *Scenario {
	return &Scenario{Name: "e1-flux-crashloop",
		Anchor:     Ref{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations", Kind: "Kustomization", Namespace: "klyx-a1-flux", Name: "a1-web"},
		Subjects:   []Ref{{Version: "v1", Resource: "deployments", Group: "apps", Kind: "Deployment", Namespace: "klyx-a1-app", Name: "web"}, {Version: "v1", Resource: "pods", Kind: "Pod", Namespace: "klyx-a1-app", NamePrefix: "web-"}},
		Lookalikes: []Ref{{Version: "v1alpha1", Resource: "scaledobjects", Group: "keda.sh", Kind: "ScaledObject", Namespace: "klyx-s9-lab", Name: "bad-auth"}},
		Proxies:    Proxies{Cause: `\bMODE\b`, Lookalike: `\brelease\b`},
	}
}

var deployment = appbridge.ChatCheckSubject{Kind: "Deployment", Namespace: "klyx-a1-app", Name: "web"}

func cause(label, statement string, cites ...string) appbridge.FindingDTO {
	return appbridge.FindingDTO{Type: "cause", Label: label, Statement: statement, Subject: deployment, Cites: cites}
}

func established(read string, checks ...string) record.Facts {
	return record.Facts{Mode: "tools", Report: record.ReportAccepted, Predicates: []record.Outcome{{Predicate: "e1-diff", Established: true, Read: read, Checks: checks}}}
}

func exchange(window string, fs ...appbridge.FindingDTO) appbridge.ChatExchangeDTO {
	return appbridge.ChatExchangeDTO{Outcome: "answered", Findings: &appbridge.FindingsDTO{Window: window, Findings: fs},
		Reads: []appbridge.ChatRead{
			{ID: "r1", Tool: "get_resource", Target: "Kustomization klyx-a1-flux/a1-web", Refs: []appbridge.ChatRef{{GVR: "kustomize.toolkit.fluxcd.io/v1/kustomizations", Namespace: "klyx-a1-flux", Name: "a1-web"}}},
			{ID: "r5", Tool: "git_changes", Target: "Kustomization klyx-a1-flux/a1-web"},
			{ID: "r7", Tool: "get_resource", Target: "ScaledObject klyx-s9-lab/bad-auth", Refs: []appbridge.ChatRef{{GVR: "keda.sh/v1alpha1/scaledobjects", Namespace: "klyx-s9-lab", Name: "bad-auth"}}},
		}}
}

func TestTheCorpus(t *testing.T) {
	cases := []struct {
		name  string
		ex    appbridge.ChatExchangeDTO
		facts record.Facts
		check func(t *testing.T, m Marks)
	}{
		{"a negated expected cause still cites the planted diff: grounded, and the proxy matches",
			exchange("2026-10-24T12:00:00Z", cause("suspected", "MODE was correct; a registry outage caused the crash.", "r5")), established("r5"),
			func(t *testing.T, m Marks) {
				if !m.GroundedCause || m.ProxyCause != 1 {
					t.Fatalf("grounding verifies the citation, not the claim; the proxy says nothing about negation: %+v", m)
				}
			}},
		{"a correct diagnosis that rejects the lookalike mentions it, and does not cite it",
			exchange("2026-10-24T12:00:00Z", cause("suspected", "The release label was incidental; the MODE change made the process exit.", "r5")), established("r5"),
			func(t *testing.T, m Marks) {
				if !m.GroundedCause || m.CitedLookalike || m.ProxyLookalike != 1 {
					t.Fatalf("%+v", m)
				}
			}},
		{"an irrelevant diff of the planted object establishes nothing",
			exchange("2026-10-24T12:00:00Z", cause("suspected", "The release label broke it.", "r5")),
			record.Facts{Report: record.ReportAccepted, Predicates: []record.Outcome{{Predicate: "e1-diff", Reason: record.NoMatch}}},
			func(t *testing.T, m Marks) {
				if m.GroundedCause {
					t.Fatalf("%+v", m)
				}
			}},
		{"a cause citing a failed read is unsupported, never grounded",
			exchange("2026-10-24T12:00:00Z", cause("unsupported", "The image is missing.", "r9")), established("r5"),
			func(t *testing.T, m Marks) {
				if m.GroundedCause || m.UnsupportedCauses != 1 || m.UnsupportedFindings != 1 {
					t.Fatalf("%+v", m)
				}
			}},
		{"a cause citing a real but unrelated read",
			exchange("2026-10-24T12:00:00Z", cause("suspected", "The Kustomization is misconfigured.", "r1")), established("r5"),
			func(t *testing.T, m Marks) {
				if m.GroundedCause {
					t.Fatalf("%+v", m)
				}
			}},
		{"an unrelated check of the establishing read is not enough; the bound one is",
			exchange("2026-10-24T12:00:00Z", cause("suspected", "a label changed", "r5.c2"), cause("suspected", "MODE changed", "r5.c1")), established("r5", "r5.c1"),
			func(t *testing.T, m Marks) {
				if !m.GroundedCause {
					t.Fatalf("r5.c1 is bound: %+v", m)
				}
			}},
		{"only the unrelated check: not grounded",
			exchange("2026-10-24T12:00:00Z", cause("suspected", "a label changed", "r5.c2")), established("r5", "r5.c1"),
			func(t *testing.T, m Marks) {
				if m.GroundedCause {
					t.Fatalf("%+v", m)
				}
			}},
		{"a cause citing only the lookalike",
			exchange("2026-10-24T12:00:00Z", cause("suspected", "KEDA's auth is broken.", "r7")), established("r5"),
			func(t *testing.T, m Marks) {
				if !m.CitedLookalike || m.GroundedCause {
					t.Fatalf("%+v", m)
				}
			}},
		{"\"no cause is proven\" as an observation: no cause, proof words are no mark",
			exchange("2026-10-24T12:00:00Z", appbridge.FindingDTO{Type: "observation", Label: "reading", Statement: "No cause is proven; the pod restarts.", Subject: deployment, Cites: []string{"r1"}}), established("r5"),
			func(t *testing.T, m Marks) {
				if m.GroundedCause || m.ProxyCause != 0 {
					t.Fatalf("%+v", m)
				}
			}},
		{"planted evidence dropped by the cap: not grounded",
			exchange("2026-10-24T12:00:00Z", cause("suspected", "MODE changed", "r5")),
			record.Facts{Report: record.ReportAccepted, Predicates: []record.Outcome{{Predicate: "e1-diff", Reason: record.Capped, Capped: true}}},
			func(t *testing.T, m Marks) {
				if m.GroundedCause || len(m.Evidence) != 1 || m.Evidence[0].Reason != record.Capped {
					t.Fatalf("%+v", m)
				}
			}},
		{"early evidence is counted, never as an error",
			exchange("2026-10-24T12:00:00Z", appbridge.FindingDTO{Type: "cause", Label: "suspected", Statement: "MODE changed", Subject: deployment, Cites: []string{"r5"},
				Checked: []appbridge.FindingCheckDTO{{ID: "r5.c1", Before: true}}}), established("r5"),
			func(t *testing.T, m Marks) {
				if m.EarlyEvidence != 1 || !m.GroundedCause {
					t.Fatalf("%+v", m)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.check(t, Score(e1(), c.ex, c.facts)) })
	}
}

func control() *Scenario {
	return &Scenario{Name: "e2-healthy-control", Control: true,
		Anchor: Ref{Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment", Namespace: "klyx-eval", Name: "steady"}}
}

func TestTheControl(t *testing.T) {
	ok := record.Facts{Report: record.ReportAccepted}
	if m := Score(control(), exchange(windowNoFailure), ok); m.Control == nil || !*m.Control || !m.EmptyReport {
		t.Fatalf("a valid empty report passes: %+v", m)
	}
	if m := Score(control(), exchange(windowNoFailure, cause("unsupported", "an invented outage", "r99")), ok); *m.Control {
		t.Fatal("an unsupported cause still fails the control")
	}
	if m := Score(control(), exchange("2026-10-24T12:00:00Z"), ok); *m.Control {
		t.Fatal("a window with a failure fails the control")
	}
}

func plantOK() Plant { return Plant{OK: true, AnchorUID: "u1", Witnesses: map[string]string{"w": "1"}} }

func TestStatuses(t *testing.T) {
	answered := func(report string) RunFacts {
		return RunFacts{Before: plantOK(), Ready: true, AnchorRead: true, Outcome: "answered", Report: report, After: plantOK()}
	}
	changed := plantOK()
	changed.Witnesses = map[string]string{"w": "2"}
	replaced := plantOK()
	replaced.AnchorUID = "u2"
	cases := []struct {
		name string
		f    RunFacts
		want string
	}{
		{"a broken plant", RunFacts{Before: Plant{Failed: "no pods"}}, StatusNotReady},
		{"a correct plant whose Observer row is not ready", RunFacts{Before: plantOK(), ReadyReason: "no row for the anchor"}, StatusSUTNotReady},
		{"a witness changed during the run", func() RunFacts { f := answered(record.ReportAccepted); f.After = changed; return f }(), StatusDrift},
		{"the anchor was replaced: drift before the failed read", func() RunFacts {
			f := answered(record.ReportAccepted)
			f.After, f.AnchorRead, f.Outcome = replaced, false, "failed"
			return f
		}(), StatusDrift},
		{"the plant broke during the run", func() RunFacts { f := answered(record.ReportAccepted); f.After = Plant{Failed: "x"}; return f }(), StatusDrift},
		{"the investigation did not start", RunFacts{Before: plantOK(), Ready: true, StartError: "refused", After: plantOK()}, StatusSUTFailure},
		{"the anchor was not read", RunFacts{Before: plantOK(), Ready: true, Outcome: "failed", After: plantOK()}, StatusSUTFailure},
		{"a failed turn whose empty report would satisfy the control", RunFacts{Before: plantOK(), Ready: true, AnchorRead: true, Outcome: "failed", Report: record.ReportAccepted, After: plantOK()}, StatusModelFailure},
		{"a tools turn with no accepted report", answered(record.ReportMissing), StatusReportFailure},
		{"every report refused", answered(record.ReportRejected), StatusReportFailure},
		{"a malformed snapshot block", answered(record.ReportMalform), StatusReportFailure},
		{"an accepted report", answered(record.ReportAccepted), StatusCompleted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, why := Classify(c.f); got != c.want {
				t.Fatalf("want %s, got %s (%s)", c.want, got, why)
			}
		})
	}
}
