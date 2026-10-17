package crd

import (
	"fmt"
	"regexp"
	"strings"
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

// messageRules maps a kind name to its rule. Keyed by name in any group: an
// event names its involved object's kind, and its group only when the
// recorder set the apiVersion, so a rule errs towards withholding.
var messageRules = map[string]func(string) (string, bool){
	"ImageUpdateAutomation": AutomationMessage,
}

// MessageSensitive reports whether kind's condition and event messages pass
// a kind rule. Describe runs without events for such a kind, and releases a
// report only when every condition message passes unchanged.
func MessageSensitive(kind string) bool {
	_, ok := messageRules[kind]
	return ok
}

// MessageRule returns msg as kind's rule lets it cross, and whether the rule
// changed it. Another kind's message is returned unchanged.
func MessageRule(kind, msg string) (string, bool) {
	if r, ok := messageRules[kind]; ok {
		return r(msg)
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
			switch m := cm["message"].(type) {
			case nil:
			case string:
				if out, changed := MessageRule(kind, m); changed {
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

// markMessage records a message the kind rule changed: not an edit blocker
// (status is not written back), but Describe is withheld.
func (s *sanitizer) markMessage(path string) {
	s.mark(path, false)
	s.messages = true
}
