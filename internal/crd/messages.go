package crd

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/moomora/klyx/internal/urlscrub"
)

// Message rules (coverage S1b §6). Some controllers publish text no rule can
// bound: ImageUpdateAutomation's condition and event messages can carry the
// rendered commit message after the push summary, a template error that
// echoes the template, or a Git server's rejection quoting a push option on
// its first line. For such a kind the messages pass an allowlist, and
// everything else is withheld whole; the condition's reason, the object's
// structured references and the verdict still say what failed.

// Describe holds (S0b §4, S1b §6): why a bracketed describe's text would be
// withheld for a version.
const (
	HoldPayload  = "payload"  // a registered payload surface is non-empty
	HoldMessages = "messages" // a condition message its kind's rule changes
)

// Hold is the reason this version's Describe is withheld, empty when it is
// not.
func (s Sanitized) Hold() string {
	switch {
	case s.Payload:
		return HoldPayload
	case s.Messages:
		return HoldMessages
	}
	return ""
}

// AutomationWithheld replaces an ImageUpdateAutomation message outside the
// allowlist.
const AutomationWithheld = "the controller's message is withheld: automation messages can quote push options, template values or a Git server's reply"

// The notification kinds' withheld wordings (coverage S1c §6).
const (
	ReceiverWithheld     = "the controller's message is withheld: a Receiver's messages can name its webhook path"
	NotificationWithheld = "the controller's message is withheld: notification messages can carry a webhook URL"
	ReceiverPathPrefix   = "Receiver initialized for path: "
)

// messageRules maps a kind name to its rule, given the condition's or
// event's reason. Keyed by name in any group: an event names its involved
// object's kind, and its group only when the recorder set the apiVersion,
// so a rule errs towards withholding.
var messageRules = map[string]func(reason, msg string) (string, bool){
	"ImageUpdateAutomation": func(_, m string) (string, bool) { return AutomationMessage(m) },
	// S1c §6: a Receiver's Ready message names its path; Alert and
	// Provider messages echo webhook URLs, path and query tokens included;
	// a fetch error prints an ExternalArtifact's URL whole into its
	// generator's and consumers' messages, so those kinds' messages pass
	// Origins whatever their current reference.
	"Receiver":          ReceiverMessage,
	"Alert":             NotificationMessage,
	"Provider":          NotificationMessage,
	"ArtifactGenerator": fetchRule("ArtifactGenerator"),
	"ExternalArtifact":  originsMessage,
	"Kustomization":     fetchRule("Kustomization"),
	"HelmRelease":       fetchRule("HelmRelease"),
}

// OriginsKind reports whether kind's messages pass urlscrub.Origins. Its
// Describe is released only when every string of the snapshot passes
// Origins unchanged (S1c §6, revision 3).
func OriginsKind(kind string) bool {
	switch kind {
	case "ArtifactGenerator", "ExternalArtifact", "Kustomization", "HelmRelease":
		return true
	}
	return false
}

func originsMessage(_, msg string) (string, bool) { return urlscrub.Origins(msg) }

// FetchWithheld replaces a fetch failure Klyx cannot place the URL in.
const FetchWithheld = "the controller's message is withheld: it reports a failed artifact fetch in a form Klyx does not recognise, and a fetch error can quote the artifact URL"

// Fetch failures (Codex's Stage 0 review, finding 2). Under the reason a
// controller gives a failed artifact fetch, Origins finding no URL proves
// nothing: an ExternalArtifact's URL is any string, even one with no URL
// syntax, and an unrecognised wording could print it anywhere. So under
// that reason a message passes only in the forms the pinned controllers
// write (kustomize-controller v1.9.6, helm-controller v1.6.5,
// source-watcher v2.2.4, fluxcd/pkg http/fetch): with a URL candidate,
// the text before it must be one of their fetch-error prefixes; without
// one, the message must start with one of their wordings that never carry
// the URL. Anything else is withheld whole, the reason kept.
type fetchForms struct {
	reason string
	// before a URL candidate: one of wrap's prefixes, then one of lead's
	wrap, lead []string
	// with no candidate: one of wrap's prefixes, then one of these
	plain []string
}

// The fluxcd/pkg archive fetcher's prefixes before the URL (a request
// error, retryablehttp's give-up, a non-200 status), and its wordings that
// never carry it.
var (
	archiveLead  = []string{"", "failed to create a new request: ", "failed to download archive: ", "failed to download archive from "}
	archivePlain = []string{"artifact is ", "failed to copy temp contents: ", "failed to seek back to beginning", "failed to verify archive: ",
		"failed to extract archive ", "failed to create temp file: ", "failed to create target file: ", "file not found"}
)

var fetchKinds = map[string]fetchForms{
	"Kustomization": {reason: "ArtifactFailed", wrap: []string{""}, lead: archiveLead,
		plain: append([]string{"Source artifact not found, retrying in ", "Source is not ready, artifact not found, retrying in ",
			"kustomization path not found: ", "unable to get source '"}, archivePlain...)},
	// the condition says "Could not load chart: <err>", its event <err>
	"HelmRelease": {reason: "ArtifactFailed", wrap: []string{"Could not load chart: ", ""},
		lead: []string{"", "failed to download chart from '", "failed to parse URL to overwrite hostname: "},
		plain: []string{"could not get Source object: ", "Source not ready: artifact not found. Retrying in ",
			"failed to parse digest '", "failed to copy and verify chart artifact: ", "integrity failure: "}},
	"ArtifactGenerator": {reason: "SourceFetchFailed", wrap: []string{"fetch sources failed: ", "get sources failed: "}, lead: archiveLead,
		plain: append([]string{"failed to create source dir: ", "unable to get source '", "source '"}, archivePlain...)},
}

// fetchRule is Origins, and under the kind's fetch reason the pinned
// forms only.
func fetchRule(kind string) func(reason, msg string) (string, bool) {
	f := fetchKinds[kind]
	return func(reason, msg string) (string, bool) {
		out, changed := urlscrub.Origins(msg)
		if reason != f.reason || msg == "" || f.known(msg) {
			return out, changed
		}
		return FetchWithheld, true
	}
}

func (f fetchForms) known(msg string) bool {
	if strings.Contains(msg, "\n") {
		return false
	}
	if prefix, ok := urlscrub.Prefix(msg); ok {
		for _, w := range f.wrap {
			for _, l := range f.lead {
				if prefix == w+l {
					return true
				}
			}
		}
		return false
	}
	for _, w := range f.wrap {
		if rest, ok := strings.CutPrefix(msg, w); ok {
			for _, p := range f.plain {
				if strings.HasPrefix(rest, p) {
					return true
				}
			}
		}
	}
	return false
}

// ReceiverMessage is the Receiver rule: the Ready message with its path
// replaced; anything else withheld whole, the reason kept.
func ReceiverMessage(_, msg string) (string, bool) {
	switch {
	case msg == "":
		return msg, false
	case strings.HasPrefix(msg, ReceiverPathPrefix) && !strings.Contains(msg, "\n"):
		out := ReceiverPathPrefix + WithheldValue
		return out, out != msg
	}
	return ReceiverWithheld, true
}

// NotificationMessage is the Alert and Provider rule: notifier errors print
// the webhook URL unmasked and echo response bodies, and an InvalidConfig
// message quotes the expression that failed to compile, which can be a
// URL (Codex's Stage 0 review, finding 3) - so every message is withheld
// whole, the reason kept.
func NotificationMessage(_, msg string) (string, bool) {
	if msg == "" {
		return msg, false
	}
	return NotificationWithheld, true
}

// MessageSensitive reports whether kind's condition and event messages pass
// a kind rule. Describe runs without events for such a kind, and releases a
// report only when every condition message passes unchanged.
func MessageSensitive(kind string) bool {
	_, ok := messageRules[kind]
	return ok
}

// MessageRule returns msg as kind's rule lets it cross, given its reason,
// and whether the rule changed it. Another kind's message is returned
// unchanged.
func MessageRule(kind, reason, msg string) (string, bool) {
	if r, ok := messageRules[kind]; ok {
		return r(reason, msg)
	}
	return msg, false
}

// The image-automation-controller v1.2.5 fixed messages.
var automationFixed = map[string]bool{
	"repository up-to-date":               true,
	"no change since last reconciliation": true,
	"reconciliation in progress":          true,
}

var automationProcessing = regexp.MustCompile(`^processing object: new generation [0-9]{1,19} -> [0-9]{1,19}$`)

// AutomationMessage is the ImageUpdateAutomation rule: the controller's own
// fixed messages pass, and a push summary passes as its first line only,
// parsed (nothing after the first line is read: it is the rendered commit
// message). Anything else is withheld whole.
func AutomationMessage(msg string) (string, bool) {
	if msg == "" || automationFixed[msg] || automationProcessing.MatchString(msg) {
		return msg, false
	}
	first, _, multi := strings.Cut(msg, "\n")
	if _, ok := ParsePushSummary(first); ok {
		return first, multi
	}
	return AutomationWithheld, true
}

// PushSummary is the parsed first line of the controller's push summary
// (PushResult.Summary).
type PushSummary struct {
	Commit   string // the seven-character hash
	Branch   string
	Refspecs []string
}

var shortHash = regexp.MustCompile(`^[0-9a-f]{7}$`)

// ParsePushSummary parses `pushed commit '<7 hex>' to branch '<branch>'`,
// optionally followed by ` and refspecs '<refspec>'[, '<refspec>']`. The
// branch must be a valid Git ref name and each refspec valid at both
// endpoints; a quote inside a name makes the line ambiguous, so it fails.
func ParsePushSummary(line string) (PushSummary, bool) {
	rest, ok := strings.CutPrefix(line, "pushed commit '")
	if !ok || len(rest) < 7 || !shortHash.MatchString(rest[:7]) {
		return PushSummary{}, false
	}
	p := PushSummary{Commit: rest[:7]}
	if rest, ok = strings.CutPrefix(rest[7:], "' to branch '"); !ok {
		return PushSummary{}, false
	}
	i := strings.IndexByte(rest, '\'')
	if i < 0 || !validRefName(rest[:i], true) {
		return PushSummary{}, false
	}
	p.Branch, rest = rest[:i], rest[i+1:]
	if rest == "" {
		return p, true
	}
	if rest, ok = strings.CutPrefix(rest, " and refspecs '"); !ok || !strings.HasSuffix(rest, "'") {
		return PushSummary{}, false
	}
	for _, r := range strings.Split(rest[:len(rest)-1], "', '") {
		if !validRefspec(r) {
			return PushSummary{}, false
		}
		p.Refspecs = append(p.Refspecs, r)
	}
	return p, true
}

// validRefspec is `[+]<src>[:<dst>]` with each endpoint a valid ref name.
// Globs and deletions (an empty source) fail closed.
func validRefspec(r string) bool {
	r = strings.TrimPrefix(r, "+")
	src, dst, pair := strings.Cut(r, ":")
	if !validRefName(src, true) {
		return false
	}
	return !pair || validRefName(dst, true)
}

// validRefName is git check-ref-format's rules, with one-level names allowed
// when oneLevel, plus no quote (the summary's delimiter).
func validRefName(n string, oneLevel bool) bool {
	if n == "" || len(n) > 255 || n == "@" || strings.HasPrefix(n, "/") || strings.HasSuffix(n, "/") ||
		strings.HasSuffix(n, ".") || strings.Contains(n, "..") || strings.Contains(n, "//") || strings.Contains(n, "@{") {
		return false
	}
	if !oneLevel && !strings.Contains(n, "/") {
		return false
	}
	for _, c := range n {
		if c < 0x20 || c == 0x7f || strings.ContainsRune(" ~^:?*[\\'", c) {
			return false
		}
	}
	for _, comp := range strings.Split(n, "/") {
		if strings.HasPrefix(comp, ".") || strings.HasSuffix(comp, ".lock") {
			return false
		}
	}
	return true
}

// conditionMessages applies kind's message rule to status.conditions[]
// messages in place. A message of an unexpected type, or conditions that
// are not a list, are withheld rather than passed raw.
func (s *sanitizer) conditionMessages(kind string, o map[string]any, at string) {
	if !MessageSensitive(kind) {
		return
	}
	st, ok := o["status"].(map[string]any)
	if !ok {
		return
	}
	cat := join(join(at, "status"), "conditions")
	switch conds := st["conditions"].(type) {
	case nil:
	case []any:
		for i, c := range conds {
			cm, ok := c.(map[string]any)
			if !ok {
				if !empty(c) {
					conds[i] = WithheldValue
					s.markMessage(fmt.Sprintf("%s[%d]", cat, i))
				}
				continue
			}
			at := fmt.Sprintf("%s[%d].message", cat, i)
			reason, _ := cm["reason"].(string)
			switch m := cm["message"].(type) {
			case nil:
			case string:
				if out, changed := MessageRule(kind, reason, m); changed {
					cm["message"] = out
					s.markMessage(at)
				}
			default:
				cm["message"] = WithheldValue
				s.markMessage(at)
			}
		}
	default:
		if !empty(conds) {
			st["conditions"] = WithheldValue
			s.markMessage(cat)
		}
	}
}

// originsHold holds Describe for an Origins kind when any string of the
// object would change under Origins (S1c §6, revision 3): kubectl's text
// cannot be rewritten safely, so the report is released only when the
// snapshot passes unchanged. The strings themselves are left to their own
// rules.
func (s *sanitizer) originsHold(v any, at string, depth int) {
	if depth > sanitizeMaxDepth {
		s.messages = true
		return
	}
	switch x := v.(type) {
	case string:
		if _, changed := urlscrub.Origins(x); changed {
			s.messages = true
		}
	case map[string]any:
		for k, e := range x {
			s.originsHold(e, join(at, k), depth+1)
		}
	case []any:
		for _, e := range x {
			s.originsHold(e, at, depth+1)
		}
	}
}

// IsEventKind reports whether group/kind is a Kubernetes Event, in either
// API.
func IsEventKind(group, kind string) bool {
	return kind == "Event" && (group == "" || group == "events.k8s.io")
}

// MessageBracketed reports whether a describe of group/kind runs inside
// the version bracket for its messages, without events: a message-
// sensitive kind, and an Event, whose text is its subject's.
func MessageBracketed(group, kind string) bool {
	return MessageSensitive(kind) || IsEventKind(group, kind)
}

// EventWithheld replaces an Event's text when Klyx cannot tell which object
// it is about.
const EventWithheld = "the event's message is withheld: Klyx cannot tell which object it is about, so cannot apply that object's message rules"

// EventSubject is a reference an Event carries, by its kind: "" when the
// reference is present without a usable kind.
type EventSubject struct{ Kind string }

// EventMessage is an Event's text under the rules of the objects it names
// (Codex's Stage 0 reviews 1 and 2). Every adapter - the generic sanitiser
// and each typed event reader - passes the references that are present:
// the subject (involvedObject or regarding), which every Event has, and
// related when set. Every subject's rule applies; a subject without a
// usable kind, or no subject at all, withholds the text.
func EventMessage(reason, msg string, subjects []EventSubject) (string, bool) {
	if msg == "" {
		return msg, false
	}
	if len(subjects) == 0 {
		return EventWithheld, true
	}
	for _, s := range subjects {
		if s.Kind == "" {
			return EventWithheld, true
		}
	}
	out, changed := msg, false
	for _, s := range subjects {
		if r, c := MessageRule(s.Kind, reason, out); c {
			out, changed = r, true
		}
	}
	return out, changed
}

// eventSubjects reads the references an Event object carries, in either
// API: involvedObject (core/v1) or regarding (events.k8s.io/v1), and related.
// A reference that is present without a non-empty string kind counts as an
// unreadable subject; an Event with neither primary reference has none.
func eventSubjects(o map[string]any) []EventSubject {
	var out []EventSubject
	primary := false
	for _, f := range []string{"involvedObject", "regarding", "related"} {
		v, ok := o[f]
		if !ok || v == nil {
			continue
		}
		if f != "related" {
			primary = true
		}
		ref, _ := v.(map[string]any)
		k, _ := ref["kind"].(string)
		out = append(out, EventSubject{Kind: k})
	}
	if !primary {
		out = append(out, EventSubject{})
	}
	return out
}

// eventMessages applies EventMessage to an Event object's own text,
// message (core/v1) and note (events.k8s.io/v1), in either API: each API
// serves every Event. The text is a writable top-level field, so a change
// blocks a round-trip edit as well as Describe.
func (s *sanitizer) eventMessages(o map[string]any, at string) {
	subjects := eventSubjects(o)
	reason, _ := o["reason"].(string)
	for _, f := range []string{"message", "note"} {
		path := join(at, f)
		switch m := o[f].(type) {
		case nil:
		case string:
			if out, changed := EventMessage(reason, m, subjects); changed {
				o[f] = out
				s.markEventMessage(path)
			}
		default:
			o[f] = WithheldValue
			s.markEventMessage(path)
		}
	}
}

// markEventMessage records an Event text its subject's rule changed: it
// blocks an edit (the text is not status) and Describe.
func (s *sanitizer) markEventMessage(path string) {
	s.mark(path, false)
	s.edit = true
	s.messages = true
}

// markMessage records a message the kind rule changed: not an edit blocker
// (status is not written back), but Describe is withheld.
func (s *sanitizer) markMessage(path string) {
	s.mark(path, false)
	s.messages = true
}
