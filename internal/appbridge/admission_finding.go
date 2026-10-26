package appbridge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/admission"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/readfence"
	"github.com/moomora/klyx/internal/scrub"
	"github.com/moomora/klyx/internal/urlscrub"
)

// Coverage S3b §2.4, §3, §5: a write path's refused request, classified in
// process on the raw status and protected before anything is serialized,
// with the webhooks and policies whose rules could match it.

// AdmissionRequestDTO is the refused request.
type AdmissionRequestDTO struct {
	Operation string `json:"operation"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// AdmissionCandidateDTO is one webhook or policy whose rules could match.
type AdmissionCandidateDTO struct {
	Kind string `json:"kind"`
	// Config is a webhook's configuration: an inventory correlation, since
	// a denial names a webhook, never its configuration.
	Config string `json:"config,omitempty"`
	Name   string `json:"name"`
	// MayMatch says why, when its exact rules do not prove a match.
	MayMatch   string `json:"mayMatch,omitempty"`
	Attributed bool   `json:"attributed,omitempty"`
}

// AdmissionFindingDTO is one refused request's admission finding.
type AdmissionFindingDTO struct {
	Envelope string              `json:"envelope"`
	Name     string              `json:"name"`
	Binding  string              `json:"binding,omitempty"`
	Cause    string              `json:"cause,omitempty"`
	Code     int32               `json:"code"`
	Reason   string              `json:"reason,omitempty"`
	Request  AdmissionRequestDTO `json:"request"`
	// What says what refused the request; Summary is its one line.
	What    string `json:"what"`
	Summary string `json:"summary"`
	// Message is the server's text (clipped, scrubbed), or "" when
	// Withheld says why it is not shown.
	Message  string `json:"message,omitempty"`
	Withheld string `json:"withheld,omitempty"`
	// NothingWritten: admission precedes storage, so nothing of this
	// request was written.
	NothingWritten bool `json:"nothingWritten"`
	// Correlation says where the named webhook or policy is in Klyx's
	// inventory.
	Correlation       string                  `json:"correlation"`
	Candidates        []AdmissionCandidateDTO `json:"candidates"`
	CandidatesOmitted int                     `json:"candidatesOmitted,omitempty"`
	// Aliases counts the webhooks and policies whose Equivalent rules on
	// other resources could match through an alias Klyx cannot read.
	Aliases int `json:"aliases,omitempty"`
	// MaybeUnread: some webhooks or policies were not read.
	MaybeUnread bool `json:"maybeUnread,omitempty"`
	// Inventory is the admission read the candidates come from: "live",
	// "stale" (as of InventoryAsOf) or "unread" (not established).
	Inventory     string `json:"inventory"`
	InventoryAsOf int64  `json:"inventoryAsOf,omitempty"`
	// Exempt: the request is on an admission configuration kind, which no
	// webhook or policy sees.
	Exempt bool `json:"exempt,omitempty"`
}

// StepDTO is one request of a composite action and its outcome.
type StepDTO struct {
	Verb    string `json:"verb"`
	Subject string `json:"subject"`
	Written bool   `json:"written"`
	// Refused: the server refused this request, or Klyx did before sending
	// it (NotSent), so nothing of it was written; neither written nor
	// refused is unverified (a lost answer)
	Refused   bool                 `json:"refused,omitempty"`
	NotSent   bool                 `json:"notSent,omitempty"`
	Error     string               `json:"error,omitempty"`
	Admission *AdmissionFindingDTO `json:"admission,omitempty"`
}

const (
	maxCandidates     = 16
	maxFindingMessage = 512
	// maxErrorText bounds a refused request's legacy error text: the
	// server's message with the wrappers around it (Codex's implementation
	// review, finding 1)
	maxErrorText = 1024
)

// admissionRows reads the Observer's admission rows: a read, never a new
// watch.
type admissionRows interface {
	HealthRowsNow(ctx context.Context, task, part string) (observe.Rows, bool)
}

// protectedSubject: S0b's protected kinds, whose free server text can echo
// submitted values (cachediff.safeError's predicate).
func protectedSubject(group, kind string) bool {
	return crd.IsPayloadKind(group, kind) || (group == "" && kind == "Secret")
}

// writeError is a write path's error as the result carries it: today's
// text, or for a protected subject its structured facts alone, and the
// admission finding when the refusal is attributed.
func writeError(ctx context.Context, conn any, err error) (string, *AdmissionFindingDTO) {
	req, f, ok := admission.Refused(err)
	if req == nil {
		return err.Error(), nil
	}
	protected := protectedSubject(req.Request.Group, req.Kind)
	text := err.Error()
	if protected {
		text = withheldText(req, f)
	}
	if !ok || !f.Attributed() {
		return text, nil
	}
	if !protected {
		// the old field carries the server's text as the finding does:
		// scrubbed and bounded, never whole
		text = boundedText(text, maxErrorText)
	}
	return text, findingDTO(ctx, conn, req, f, protected)
}

// withheldText keeps a protected subject's structured facts: the status
// reason and code, and what refused it; never the server's text.
func withheldText(req *admission.RequestError, f admission.Finding) string {
	parts := []string{fmt.Sprintf("%s %s was refused", strings.ToLower(req.Request.Operation), req.Subject())}
	if f.Code != 0 {
		parts = append(parts, fmt.Sprintf("%s (%d)", f.Reason, f.Code))
	}
	if f.Attributed() {
		parts = append(parts, f.What())
	}
	return strings.Join(parts, ": ") + "; " + withheldSentence(req.Kind)
}

func withheldSentence(kind string) string {
	return fmt.Sprintf("the server's message is withheld for a %s: it can echo submitted values", kind)
}

// findingMessage is the server's text for a subject that is not protected:
// scrubbed of credentials and URL secrets, then clipped.
func findingMessage(s string) string { return boundedText(s, maxFindingMessage) }

// boundedText scrubs credentials and URL secrets from s, then clips it to
// max bytes on a rune boundary.
func boundedText(s string, max int) string {
	s, _ = scrub.Credentials(s)
	s = urlscrub.Text(s).Text
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

func findingDTO(ctx context.Context, conn any, req *admission.RequestError, f admission.Finding, protected bool) *AdmissionFindingDTO {
	d := &AdmissionFindingDTO{Envelope: f.Envelope, Name: f.Name, Binding: f.Binding, Cause: f.Cause, Code: f.Code, Reason: f.Reason,
		Request:        AdmissionRequestDTO{Operation: req.Request.Operation, Kind: req.Kind, Namespace: req.Namespace, Name: req.Name},
		What:           f.What(),
		Summary:        admission.Sentence(req, f),
		NothingWritten: true,
		Candidates:     []AdmissionCandidateDTO{},
		Inventory:      "unread",
	}
	if protected {
		d.Withheld = withheldSentence(req.Kind)
	} else {
		d.Message = findingMessage(f.Detail)
	}
	r, ok := conn.(admissionRows)
	if !ok {
		d.Correlation = correlation(f, nil, false)
		return d
	}
	configs, policies, inventory, asOf, unread := admissionInventory(ctx, r)
	d.Inventory, d.MaybeUnread = inventory, unread
	if asOf != nil {
		d.InventoryAsOf = asOf.Unix()
	}
	if inventory == "unread" {
		d.Correlation = correlation(f, nil, false)
		return d
	}
	c := admission.Match(req.Request, configs, policies)
	d.Exempt = c.Exempt
	d.MaybeUnread = d.MaybeUnread || c.Unread
	named := namedRows(f, configs, policies)
	d.Correlation = correlation(f, named, d.MaybeUnread)
	list := c.List
	// the attributed one is marked, first; listed even when its rules,
	// as read, do not match
	seen := map[string]bool{}
	var out []AdmissionCandidateDTO
	for _, n := range named {
		out = append(out, AdmissionCandidateDTO{Kind: n.Kind, Config: n.Config, Name: n.Name, Attributed: true})
		seen[n.Kind+"/"+n.Config+"/"+n.Name] = true
	}
	for _, cand := range list {
		k := cand.Kind + "/" + cand.Config + "/" + cand.Name
		if seen[k] {
			// keep the attributed row, with its own match reading
			for i := range out {
				if out[i].Kind+"/"+out[i].Config+"/"+out[i].Name == k {
					out[i].MayMatch = cand.Why
				}
			}
			continue
		}
		seen[k] = true
		out = append(out, AdmissionCandidateDTO{Kind: cand.Kind, Config: cand.Config, Name: cand.Name, MayMatch: cand.Why})
	}
	// the alias count excludes any already listed (the attributed one)
	for _, a := range c.Aliased {
		if !seen[a.Kind+"/"+a.Config+"/"+a.Name] {
			d.Aliases++
		}
	}
	if len(out) > maxCandidates {
		d.CandidatesOmitted, out = len(out)-maxCandidates, out[:maxCandidates]
	}
	d.Candidates = out
	return d
}

// namedRows are the inventory rows the finding names: every webhook of
// that name (names are not unique across configurations), or the policy.
func namedRows(f admission.Finding, configs []*admission.Config, policies []*admission.Policy) []admission.Candidate {
	var out []admission.Candidate
	switch f.Envelope {
	case admission.EnvWebhookDenied, admission.EnvDryRunUnsupported, admission.EnvWebhookCallFailed:
		for _, c := range configs {
			kind := admission.KindValidatingWebhook
			if c.Kind == admission.Mutating {
				kind = admission.KindMutatingWebhook
			}
			for _, w := range c.Webhooks {
				if w.Name == f.Name {
					out = append(out, admission.Candidate{Kind: kind, Config: c.Name, Name: w.Name})
				}
			}
		}
	case admission.EnvVAPDenied, admission.EnvPolicyDenied:
		for _, p := range policies {
			kind := admission.KindValidatingPolicy
			if p.Kind == admission.Mutating {
				kind = admission.KindMutatingPolicy
			}
			if p.Name == f.Name && (f.Envelope == admission.EnvPolicyDenied || kind == admission.KindValidatingPolicy) {
				out = append(out, admission.Candidate{Kind: kind, Name: p.Name})
			}
		}
	}
	return out
}

// correlation says where the named webhook or policy is in Klyx's
// inventory (S3b §3.4).
func correlation(f admission.Finding, named []admission.Candidate, unread bool) string {
	if len(named) == 0 {
		return "not in Klyx's admission inventory (it may not have been read)"
	}
	more := ""
	if unread {
		more = ", and possibly others Klyx did not read"
	}
	if named[0].Config == "" {
		if f.Envelope != admission.EnvPolicyDenied {
			return fmt.Sprintf("the %s %s%s", named[0].Kind, named[0].Name, more)
		}
		// the generic envelope does not say the policy's kind (S3b §2.1):
		// every policy of that name is said, never the first one's kind
		var rows []string
		for _, n := range named {
			rows = append(rows, "the "+n.Kind+" "+n.Name)
		}
		sort.Strings(rows)
		s := fmt.Sprintf("a policy named %s: %s in Klyx's inventory", f.Name, strings.Join(rows, " and "))
		if len(rows) > 1 {
			s += "; the refusal does not say which"
		}
		return s + more
	}
	var configs []string
	for _, n := range named {
		configs = append(configs, n.Config)
	}
	sort.Strings(configs)
	return fmt.Sprintf("a webhook named %s in %s%s", f.Name, strings.Join(configs, " and "), more)
}

// admissionInventory reads the admission rows under their coverage:
// "live", "stale" with the oldest time, or "unread"; unread also when a
// kind was capped or a part has no rows.
func admissionInventory(ctx context.Context, r admissionRows) (configs []*admission.Config, policies []*admission.Policy, state string, asOf *time.Time, maybeUnread bool) {
	state = "live"
	for _, part := range []string{"webhooks", "policies"} {
		rows, ok := r.HealthRowsNow(ctx, healthsrc.TaskAdmission, part)
		if !ok {
			return nil, nil, "unread", nil, true
		}
		for _, k := range rows.Part.Kinds {
			if k.Role != "" && k.Role != "counted" {
				continue
			}
			switch k.Coverage {
			case observe.Live:
			case observe.Stale:
				state = "stale"
				if k.AsOf != nil && (asOf == nil || k.AsOf.Before(*asOf)) {
					t := *k.AsOf
					asOf = &t
				}
			case observe.Absent:
			default:
				return nil, nil, "unread", nil, true
			}
			maybeUnread = maybeUnread || k.Truncated
		}
		switch set := rows.Rows.(type) {
		case healthsrc.RowSet[healthsrc.AdmissionConfigRow]:
			for _, row := range set.Rows {
				if row.Data.Config != nil {
					configs = append(configs, row.Data.Config)
				}
			}
		case healthsrc.RowSet[healthsrc.AdmissionPolicyRow]:
			for _, row := range set.Rows {
				if row.Data.Policy != nil {
					policies = append(policies, row.Data.Policy)
				}
			}
		}
	}
	return configs, policies, state, asOf, maybeUnread
}

// stepsDTO converts a composite action's steps, each with its own finding
// and its own subject's protection.
func stepsDTO(ctx context.Context, conn any, verbSteps []stepOutcome) []StepDTO {
	out := make([]StepDTO, 0, len(verbSteps))
	for _, s := range verbSteps {
		d := StepDTO{Verb: s.verb, Subject: s.subject, Written: s.written, Refused: s.refused, NotSent: s.notSent}
		if s.err != nil {
			d.Error, d.Admission = writeError(ctx, conn, s.err)
		}
		out = append(out, d)
	}
	return out
}

// stepOutcome is a fleet step, decoupled from fleet's type.
type stepOutcome struct {
	verb, subject             string
	written, refused, notSent bool
	err                       error
}

// failed is a write path's failed result (coverage S3b §5). A fence's
// refusal also says what reached the cluster (Settings slice F §2): not
// sent when nothing of the write was forwarded, else unverified - for a
// verb that records no receipts too.
func failed(ctx context.Context, conn any, err error) ActionResultDTO {
	text, f := writeError(ctx, conn, err)
	r := ActionResultDTO{Error: text, Admission: f}
	if readfence.Failed(err) {
		r.Outcome, r.NotSent = fleet.WriteOutcome(err)
	}
	return r
}

// failedScrubbed is failed for a path whose text passes the URL rule.
func failedScrubbed(ctx context.Context, conn any, err error) ActionResultDTO {
	r := failed(ctx, conn, err)
	r.Error = urlscrub.Text(r.Error).Text
	return r
}

// adhocFinding is an ad-hoc result's error and finding: the refused
// request when there is one, today's text otherwise.
func adhocFinding(ctx context.Context, conn any, text string, refusal error) (string, *AdmissionFindingDTO) {
	if refusal == nil {
		return text, nil
	}
	t, f := writeError(ctx, conn, refusal)
	if req, _, _ := admission.Refused(refusal); req != nil && protectedSubject(req.Request.Group, req.Kind) {
		return t, f
	}
	if f != nil {
		// an attributed refusal's text is scrubbed and bounded (finding 1)
		return boundedText(text, maxErrorText), f
	}
	return text, f
}
