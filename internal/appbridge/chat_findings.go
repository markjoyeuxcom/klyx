package appbridge

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// AI A1 §4: the model ends an investigation's turn with findings; Klyx
// decides, for every finding, what its citations hold up (A-C1): provenance
// (every cited ID is in this investigation's ledger), subject identity (a
// cited read or check is about the finding's subject, by UID or by name),
// time (evidence observed before the failure was reported does not
// establish it), coverage, and the label. An observation is the model's
// reading, a cause is suspected, and a finding whose citations do not hold
// up is unsupported. No cause is ever shown as proven. Missing evidence
// comes from Klyx's ledger, with the model's own notes added as its own.

const (
	toolFindings          = "report_findings"
	findingsMax           = 20
	findingCheckedMax     = 12 // checks shown beside one finding
	findingStatementMax   = 500
	findingCitesMax       = 10
	findingsMissingMax    = 10
	findingsMissingNote   = 300
	findingsRemedyMax     = 2000
	findingsAttemptsTurn  = 2 // one invalid call may be retried once
	labelReading          = "reading"
	labelSuspected        = "suspected"
	labelUnsupported      = "unsupported"
	proposedNotRun        = "proposed, not run"
	findingIdentityByName = "by name: the object at that name may have been replaced"
)

func findingsToolDef() ChatToolDef {
	subject := obj(map[string]any{"kind": strProp("Kind."), "namespace": strProp("Namespace, when namespaced."), "name": strProp("Name.")}, "kind", "name")
	finding := obj(map[string]any{
		"type":      map[string]any{"type": "string", "enum": []string{"observation", "cause"}, "description": "observation: what the evidence shows; cause: what you think explains the failure."},
		"statement": map[string]any{"type": "string", "maxLength": findingStatementMax, "description": "One finding, plainly."},
		"subject":   subject,
		"cites":     map[string]any{"type": "array", "maxItems": findingCitesMax, "items": map[string]any{"type": "string"}, "description": "Read and check IDs it rests on, e.g. r3 or r3.c1."},
	}, "type", "statement", "subject", "cites")
	return ChatToolDef{toolFindings, "End the investigation's turn: report your findings, what you could not read, and a remedy as text. Klyx checks every citation and labels each finding itself.", obj(map[string]any{
		"findings": map[string]any{"type": "array", "maxItems": findingsMax, "items": finding},
		"missing":  map[string]any{"type": "array", "maxItems": findingsMissingMax, "items": map[string]any{"type": "string", "maxLength": findingsMissingNote}},
		"remedy": obj(map[string]any{
			"git":        map[string]any{"type": "string", "maxLength": findingsRemedyMax, "description": "A change to commit to Git, described."},
			"mitigation": map[string]any{"type": "string", "maxLength": findingsRemedyMax, "description": "A mitigation the operator could run."},
		}),
	}, "findings")}
}

// findingsInput is report_findings' arguments.
type findingsInput struct {
	Findings []struct {
		Type      string `json:"type"`
		Statement string `json:"statement"`
		Subject   struct {
			Kind      string `json:"kind"`
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		} `json:"subject"`
		Cites []string `json:"cites"`
	} `json:"findings"`
	Missing []string `json:"missing"`
	Remedy  *struct {
		Git        string `json:"git"`
		Mitigation string `json:"mitigation"`
	} `json:"remedy"`
}

func parseFindings(raw json.RawMessage) (findingsInput, error) {
	var in findingsInput
	if err := strictArgs(raw, &in); err != nil {
		return in, err
	}
	if len(in.Findings) > findingsMax {
		return in, fmt.Errorf("at most %d findings", findingsMax)
	}
	if len(in.Missing) > findingsMissingMax {
		return in, fmt.Errorf("at most %d missing notes", findingsMissingMax)
	}
	for i, f := range in.Findings {
		switch {
		case f.Type != "observation" && f.Type != "cause":
			return in, fmt.Errorf("finding %d: type must be observation or cause", i+1)
		case strings.TrimSpace(f.Statement) == "" || utf8.RuneCountInString(f.Statement) > findingStatementMax:
			return in, fmt.Errorf("finding %d: a statement of 1 to %d characters", i+1, findingStatementMax)
		case len(f.Cites) > findingCitesMax:
			return in, fmt.Errorf("finding %d: at most %d cites", i+1, findingCitesMax)
		}
		if err := firstErr(checkKind(f.Subject.Kind), checkName("namespace", f.Subject.Namespace, false), checkName("name", f.Subject.Name, true)); err != nil {
			return in, fmt.Errorf("finding %d: subject: %w", i+1, err)
		}
	}
	for _, m := range in.Missing {
		if utf8.RuneCountInString(m) > findingsMissingNote {
			return in, fmt.Errorf("a missing note is at most %d characters", findingsMissingNote)
		}
	}
	if in.Remedy != nil && (utf8.RuneCountInString(in.Remedy.Git) > findingsRemedyMax || utf8.RuneCountInString(in.Remedy.Mitigation) > findingsRemedyMax) {
		return in, fmt.Errorf("a remedy part is at most %d characters", findingsRemedyMax)
	}
	return in, nil
}

// FindingCheckDTO is a Klyx check a finding cites, in Klyx's own words.
type FindingCheckDTO struct {
	ID        string `json:"id"`
	Class     string `json:"class"`
	Statement string `json:"statement"`
	Observed  string `json:"observed,omitempty"`
	// Before: observed before the failure was reported; it does not
	// establish the current failure (A-C1 time rule).
	Before bool `json:"before,omitempty"`
}

// FindingDTO is one of the model's findings as Klyx labels it.
type FindingDTO struct {
	Type      string            `json:"type"`  // observation | cause
	Label     string            `json:"label"` // reading | suspected | unsupported
	Statement string            `json:"statement"`
	Subject   ChatCheckSubject  `json:"subject"`
	Cites     []string          `json:"cites"`
	Checked   []FindingCheckDTO `json:"checked,omitempty"`
	Notes     []string          `json:"notes,omitempty"`
}

// MissingDTO is evidence that was not read: Klyx's, or the model's note.
type MissingDTO struct {
	From string `json:"from"` // klyx | model
	Read string `json:"read,omitempty"`
	Text string `json:"text"`
}

// RemedyDTO is the model's remedy: text only, never run.
type RemedyDTO struct {
	Git        string `json:"git,omitempty"`
	Mitigation string `json:"mitigation,omitempty"`
	Label      string `json:"label"`
}

// FindingsDTO is an investigation turn's findings.
type FindingsDTO struct {
	// Window is when the anchor's failure was reported, "no failure to
	// date", or "unknown".
	Window   string       `json:"window"`
	Findings []FindingDTO `json:"findings"`
	Missing  []MissingDTO `json:"missing"`
	Remedy   *RemedyDTO   `json:"remedy,omitempty"`
	Note     string       `json:"note,omitempty"`
	// Scope is "Snapshot: Klyx read a fixed set of evidence" for a
	// snapshot investigation (§1.1).
	Scope string `json:"scope,omitempty"`
}

// anchorFacts are what Klyx reads off the anchor's read: its identity and
// its failure's window.
type anchorFacts struct {
	subject ChatCheckSubject
	failing bool
	start   string // RFC 3339; "" when unknown
}

// anchorOf reads the anchor's facts from the first exchange's seed read.
func anchorOf(anchor *ChatSeedDTO, ledger []ChatRead) anchorFacts {
	a := anchorFacts{subject: ChatCheckSubject{Namespace: anchor.Namespace, Name: anchor.Name, UID: anchor.UID, Identity: identityUID}}
	for _, r := range ledger {
		for _, c := range r.Checks {
			if c.Subject.UID != anchor.UID || c.Subject.UID == "" {
				continue
			}
			if a.subject.Kind == "" {
				a.subject.Kind = c.Subject.Kind
			}
			// a failure in force when read; a past restart is not one (A1
			// implementation review, finding 5)
			if !c.Failing {
				continue
			}
			a.failing = true
			// the failure's window starts at its Ready=False transition, or
			// - a Flux unit, which reads Unknown while it retries - when its
			// revision first failed its health check: an upper bound on when
			// it began
			ready := c.Class == checkCondition && strings.Contains(c.Statement, "Ready=False")
			if (ready || c.Class == checkHealthCheck) && c.Onset != "" && (a.start == "" || c.Onset < a.start) {
				a.start = c.Onset
			}
		}
		if len(r.Refs) > 0 && r.Refs[0].UID == anchor.UID && a.subject.Kind == "" {
			a.subject.Kind, _, _ = strings.Cut(r.Target, " ")
		}
	}
	return a
}

// labelFindings is Klyx's decision on every finding (A-C1).
func labelFindings(in findingsInput, ledger []ChatRead, a anchorFacts) FindingsDTO {
	reads := map[string]ChatRead{}
	checks := map[string]ChatCheck{}
	for _, r := range ledger {
		if r.ID == "" {
			continue
		}
		reads[r.ID] = r
		for _, c := range r.Checks {
			checks[c.ID] = c
		}
	}
	n := 0
	out := FindingsDTO{Findings: []FindingDTO{}, Missing: missingFrom(ledger)}
	switch {
	case !a.failing:
		out.Window = "no failure to date"
	case a.start == "":
		out.Window = "unknown"
	default:
		out.Window = a.start
	}
	for _, f := range in.Findings {
		subj := ChatCheckSubject{Kind: f.Subject.Kind, Namespace: f.Subject.Namespace, Name: f.Subject.Name}
		fd := FindingDTO{Type: f.Type, Statement: chatScrub(f.Statement, &n), Subject: subj, Cites: append([]string{}, f.Cites...)}
		var unknown []string
		about, byUID, before, cited := false, false, 0, 0
		shown := map[string]bool{}
		// check is one check the finding rests on, cited directly or held
		// by a read it cites: its time and coverage qualify the finding
		// either way (A1 implementation review, finding 6)
		check := func(c ChatCheck, via string) bool {
			if shown[c.ID] || len(fd.Checked) == findingCheckedMax {
				return false
			}
			shown[c.ID] = true
			fc := FindingCheckDTO{ID: c.ID, Class: c.Class, Statement: c.Statement, Observed: c.Observed}
			fc.Before = a.failing && a.start != "" && c.Observed != "" && c.Observed < a.start
			fd.Checked = append(fd.Checked, fc)
			if qualifiedCoverage(c.Coverage) {
				if via == "" {
					fd.Notes = append(fd.Notes, fmt.Sprintf("cites %s, whose coverage is %s", c.ID, c.Coverage))
				} else {
					fd.Notes = append(fd.Notes, fmt.Sprintf("cites %s, which holds %s, whose coverage is %s", via, c.ID, c.Coverage))
				}
			}
			if fc.Before && via != "" {
				fd.Notes = append(fd.Notes, fmt.Sprintf("cites %s, whose %s was observed before the failure was reported", via, c.ID))
			}
			return fc.Before
		}
		for _, id := range f.Cites {
			if c, ok := checks[id]; ok {
				cited++
				if check(c, "") {
					before++
				}
				if sameSubject(c.Subject, subj) {
					about = true
					byUID = byUID || c.Subject.Identity == identityUID
				}
				continue
			}
			r, ok := reads[id]
			if !ok {
				unknown = append(unknown, id)
				continue
			}
			// a read is evidence read now, never wholly before the failure
			cited++
			if r.Outcome != "completed" {
				fd.Notes = append(fd.Notes, fmt.Sprintf("cites %s, a read that %s", id, map[string]string{"failed": "failed", "truncated": "was truncated", "cancelled": "was cancelled"}[r.Outcome]))
			}
			if len(r.Gaps) > 0 {
				fd.Notes = append(fd.Notes, fmt.Sprintf("cites %s, part of which could not be read", id))
			}
			for _, ref := range r.Refs {
				if ref.Namespace == subj.Namespace && ref.Name == subj.Name && refIsKind(ref.GVR, subj.Kind) {
					about = true
					byUID = byUID || ref.UID != ""
				}
			}
			for _, c := range r.Checks {
				if sameSubject(c.Subject, subj) {
					about = true
					byUID = byUID || c.Subject.Identity == identityUID
					check(c, id)
				}
			}
			// a by-name read supports a by-name reading, never one by UID
			// (A1 implementation review, finding 7)
			for _, s := range r.About {
				if sameSubject(s, subj) {
					about = true
					byUID = byUID || s.Identity == identityUID
				}
			}
		}
		switch {
		case len(unknown) > 0:
			fd.Label = labelUnsupported
			fd.Notes = append(fd.Notes, "cites "+strings.Join(unknown, ", ")+", not in this investigation's reads")
		case cited == 0:
			fd.Label = labelUnsupported
			fd.Notes = append(fd.Notes, "cites no evidence")
		case !about:
			fd.Label = labelUnsupported
			fd.Notes = append(fd.Notes, "none of its citations is about "+subj.words())
		case f.Type == "cause":
			fd.Label = labelSuspected
		default:
			fd.Label = labelReading
		}
		if fd.Label != labelUnsupported {
			if !byUID {
				fd.Notes = append(fd.Notes, findingIdentityByName)
			}
			if before > 0 && cited == before {
				fd.Notes = append(fd.Notes, "its evidence was observed before the failure was reported, so it does not establish the failure")
			}
		}
		out.Findings = append(out.Findings, fd)
	}
	for _, m := range in.Missing {
		out.Missing = append(out.Missing, MissingDTO{From: "model", Text: chatScrub(m, &n)})
	}
	if in.Remedy != nil && (in.Remedy.Git != "" || in.Remedy.Mitigation != "") {
		out.Remedy = &RemedyDTO{Git: chatScrub(in.Remedy.Git, &n), Mitigation: chatScrub(in.Remedy.Mitigation, &n), Label: proposedNotRun}
	}
	return out
}

// qualifiedCoverage is a check's coverage that qualifies what rests on it.
func qualifiedCoverage(c string) bool {
	return c != "" && c != "live" && c != "render"
}

func sameSubject(c, f ChatCheckSubject) bool {
	return strings.EqualFold(c.Kind, f.Kind) && c.Namespace == f.Namespace && c.Name == f.Name
}

// refIsKind: a reference's GVR names the kind's resource.
func refIsKind(gvr, kind string) bool {
	res := gvr[strings.LastIndex(gvr, "/")+1:]
	return res == pluralGuess(kind) || strings.EqualFold(res, kind+"s") || strings.EqualFold(res, kind)
}

// missingFrom is the evidence Klyx did not read, from its ledger.
func missingFrom(ledger []ChatRead) []MissingDTO {
	out := []MissingDTO{}
	for _, r := range ledger {
		switch {
		case r.Outcome == "failed" || r.Outcome == "cancelled":
			out = append(out, MissingDTO{From: "klyx", Read: r.ID, Text: strings.TrimSpace(fmt.Sprintf("%s %s %s: %s", r.Tool, r.Target, r.Outcome, r.Detail))})
		case r.Outcome == "truncated":
			out = append(out, MissingDTO{From: "klyx", Read: r.ID, Text: strings.TrimSpace(r.Tool + " " + r.Target + " was truncated: part of it was not sent")})
		case r.Tool == toolChanges && strings.Contains(r.Detail, "not established"):
			out = append(out, MissingDTO{From: "klyx", Read: r.ID, Text: "git_changes " + r.Target + ": the render comparison was not established"})
		}
		for _, g := range r.Gaps {
			if r.Outcome == "failed" || r.Outcome == "cancelled" {
				break // its outcome says so
			}
			out = append(out, MissingDTO{From: "klyx", Read: r.ID, Text: strings.TrimSpace(r.Tool + " " + r.Target + ": part of it could not be read: " + g)})
		}
		covered := map[string]bool{}
		for _, c := range r.Checks {
			if c.Class == checkHealthCheck && (strings.Contains(c.Statement, joinMissing) || strings.Contains(c.Statement, "ambiguous") || strings.Contains(c.Statement, joinNoInventory)) {
				out = append(out, MissingDTO{From: "klyx", Read: c.ID, Text: "a health-check entry was not joined to the inventory"})
			}
			// stale, partial, checking or unreadable evidence, once per
			// read and coverage
			if qualifiedCoverage(c.Coverage) && !covered[c.Coverage] {
				covered[c.Coverage] = true
				out = append(out, MissingDTO{From: "klyx", Read: c.ID, Text: fmt.Sprintf("%s %s holds evidence whose coverage is %s", r.Tool, r.Target, c.Coverage)})
			}
		}
	}
	return out
}
