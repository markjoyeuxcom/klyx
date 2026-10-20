package admission

import (
	"regexp"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The classifier (coverage S3b §2): one API request's refusal, read from
// the server's own status (code, reason, message, details.causes) and
// never from wrapper prose a client added around it. A finding is
// attributed only when the outer status has one of five exact shapes; the
// first that matches wins, and content nested inside a matched envelope
// never re-attributes it. Anything else is not attributed.

// The envelopes, in precedence order (S3b §2.1).
const (
	// EnvWebhookDenied: a webhook's denial (ToStatusErr).
	EnvWebhookDenied = "webhook-denied"
	// EnvDryRunUnsupported: a webhook without dry-run support.
	EnvDryRunUnsupported = "dry-run-unsupported"
	// EnvWebhookCallFailed: a webhook that could not be called.
	EnvWebhookCallFailed = "webhook-call-failed"
	// EnvVAPDenied: a ValidatingAdmissionPolicy's denial.
	EnvVAPDenied = "vap-denied"
	// EnvPolicyDenied: the generic policy dispatcher's denial (used by
	// MutatingAdmissionPolicy); the policy's kind is not guessed.
	EnvPolicyDenied = "policy-denied"
)

// A call failure's cause (S3b §2.2).
const (
	CauseDeadline    = "deadline"
	CauseRefused     = "refused"
	CauseTLS         = "tls"
	CauseNoEndpoints = "no-endpoints"
	CauseOther       = "other"
)

// Finding is one refused request's admission reading.
type Finding struct {
	// Envelope is one of the Env constants, "" when not attributed.
	Envelope string
	// Name is the webhook's (never its configuration's) or the policy's.
	Name    string
	Binding string
	// Cause is a call failure's category.
	Cause string
	// Detail is the server's free text after the envelope: a denial's
	// message, or a call failure's cause (never its URL). It can echo
	// submitted values: the caller decides whether it may be shown.
	Detail string
	Code   int32
	Reason string
}

// Attributed: the refusal names its webhook or policy.
func (f Finding) Attributed() bool { return f.Envelope != "" }

const (
	webhookPrefix  = "admission webhook "
	deniedSuffix   = " denied the request"
	noExplanation  = " without explanation"
	dryRunSuffix   = " does not support dry run"
	internalPrefix = "Internal error occurred: failed calling webhook "
	callPrefix     = "failed to call webhook: "
)

var (
	vapCause = regexp.MustCompile(`^ValidatingAdmissionPolicy '([^']+)'(?: with binding '([^']+)')? denied request: `)
	// admission.NewForbidden's three message forms (apierrors.NewForbidden):
	// no resource, a resource, a resource and a Go-quoted name
	forbiddenPrefix = regexp.MustCompile(`^(?:forbidden: |[^ "]+ is forbidden: |[^ "]+ "(?:[^"\\]|\\.)*" is forbidden: )$`)
	genericSingle   = regexp.MustCompile(`^policy '([^']+)'(?: with binding '([^']+)')? denied request: `)
	genericDouble   = regexp.MustCompile(`^policy ("(?:[^"\\]|\\.)*") denied request: `)
)

// quotedName reads a Go-quoted name (%q) at the start of s, and the rest.
func quotedName(s string) (name, rest string, ok bool) {
	q, err := strconv.QuotedPrefix(s)
	if err != nil {
		return "", "", false
	}
	name, err = strconv.Unquote(q)
	if err != nil {
		return "", "", false
	}
	return name, s[len(q):], true
}

// Classify reads one status.
func Classify(st metav1.Status) Finding {
	f := Finding{Code: st.Code, Reason: string(st.Reason)}
	msg := st.Message
	// 1. a webhook's denial: the message starts with it
	if st.Code >= 400 && strings.HasPrefix(msg, webhookPrefix) {
		if name, rest, ok := quotedName(msg[len(webhookPrefix):]); ok {
			switch {
			case rest == deniedSuffix+noExplanation:
				f.Envelope, f.Name = EnvWebhookDenied, name
				return f
			case strings.HasPrefix(rest, deniedSuffix+": "):
				f.Envelope, f.Name, f.Detail = EnvWebhookDenied, name, rest[len(deniedSuffix+": "):]
				return f
			}
			// 2. dry run unsupported: the message is exactly it
			if st.Code == 400 && st.Reason == metav1.StatusReasonBadRequest && rest == dryRunSuffix {
				f.Envelope, f.Name = EnvDryRunUnsupported, name
				return f
			}
		}
	}
	// 3. a call failure: an internal error that starts with it
	if st.Code == 500 && st.Reason == metav1.StatusReasonInternalError && strings.HasPrefix(msg, internalPrefix) {
		if name, rest, ok := quotedName(msg[len(internalPrefix):]); ok && strings.HasPrefix(rest, ": ") {
			f.Envelope, f.Name = EnvWebhookCallFailed, name
			f.Cause, f.Detail = callCause(rest[2:])
			return f
		}
	}
	causes := []metav1.StatusCause(nil)
	if st.Details != nil {
		causes = st.Details.Causes
	}
	// 4. a ValidatingAdmissionPolicy's denial: NewForbidden's prefix,
	// then exactly the last cause, which names the policy
	if n := len(causes); n > 0 && st.Code >= 400 {
		last := causes[n-1].Message
		if m := vapCause.FindStringSubmatch(last); m != nil && strings.HasSuffix(msg, last) && forbiddenPrefix.MatchString(msg[:len(msg)-len(last)]) {
			f.Envelope, f.Name, f.Binding, f.Detail = EnvVAPDenied, m[1], m[2], last[len(m[0]):]
			return f
		}
	}
	// 5. the generic dispatcher's denial: NewForbidden's details, and the
	// message equal to the first cause, which names the policy
	if len(causes) > 0 && st.Code >= 400 && st.Details.Kind != "" && msg == causes[0].Message {
		if m := genericSingle.FindStringSubmatch(msg); m != nil {
			f.Envelope, f.Name, f.Binding, f.Detail = EnvPolicyDenied, m[1], m[2], msg[len(m[0]):]
			return f
		}
		if m := genericDouble.FindStringSubmatch(msg); m != nil {
			if name, err := strconv.Unquote(m[1]); err == nil {
				f.Envelope, f.Name, f.Detail = EnvPolicyDenied, name, msg[len(m[0]):]
				return f
			}
		}
	}
	return f
}

// callCause reads a call failure's text after the webhook's name: the
// cause after the quoted URL in `failed to call webhook: Post "<url>":
// <cause>` (url.Error's form), never the URL, whose ?timeout= query once
// read as a timeout; any other form is "other", its text kept.
func callCause(s string) (cause, detail string) {
	if strings.HasPrefix(s, callPrefix) {
		rest := s[len(callPrefix):]
		if op, after, ok := strings.Cut(rest, " "); ok && op != "" && !strings.ContainsAny(op, `"`) {
			if q, err := strconv.QuotedPrefix(after); err == nil && strings.HasPrefix(after[len(q):], ": ") {
				detail = after[len(q)+2:]
				return categoryOf(detail), detail
			}
		}
	}
	return CauseOther, s
}

func categoryOf(cause string) string {
	switch {
	case strings.Contains(cause, "context deadline exceeded"), strings.Contains(cause, "Client.Timeout exceeded"), strings.Contains(cause, "i/o timeout"):
		return CauseDeadline
	case strings.Contains(cause, "connection refused"):
		return CauseRefused
	case strings.Contains(cause, "x509:"), strings.Contains(cause, "tls:"):
		return CauseTLS
	case strings.Contains(cause, "no endpoints available for service"):
		return CauseNoEndpoints
	}
	return CauseOther
}

// UIDRefusal reads the UID guards' own refusals (S3b §2.5), by their wire
// shapes and never a substring: the guarded object gone (NotFound); a UID
// precondition failed in storage, which the registry answers as a wrapped
// Conflict; or the immutable UID refused as Invalid on metadata.uid. An
// attributed admission finding is never a replacement, whatever its text.
func UIDRefusal(st metav1.Status) bool {
	if Classify(st).Attributed() {
		return false
	}
	switch st.Reason {
	case metav1.StatusReasonNotFound:
		return st.Code == 404
	case metav1.StatusReasonConflict:
		return st.Code == 409 && strings.HasPrefix(st.Message, "Operation cannot be fulfilled on ") &&
			strings.Contains(st.Message, "AdditionalErrorMsg: Precondition failed: UID in precondition: ")
	case metav1.StatusReasonInvalid:
		if st.Details == nil {
			return false
		}
		for _, c := range st.Details.Causes {
			if c.Field == "metadata.uid" {
				return true
			}
		}
	}
	return false
}
