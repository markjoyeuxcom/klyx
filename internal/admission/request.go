package admission

import (
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// RequestError is one API request's error (coverage S3b §2.3). Every write
// path wraps the error of each API call it makes, so a finding is about
// the request that was refused, never the whole action, and a composite
// action's error still names it. Error and Unwrap leave the message and the
// status chain as they were.
type RequestError struct {
	Request Request
	// Kind, Namespace and Name are the request's subject.
	Kind, Namespace, Name string
	Err                   error
}

func (e *RequestError) Error() string { return e.Err.Error() }
func (e *RequestError) Unwrap() error { return e.Err }

// Wrap wraps one API call's error (nil stays nil).
func Wrap(q Request, kind, namespace, name string, err error) error {
	if err == nil {
		return nil
	}
	return &RequestError{Request: q, Kind: kind, Namespace: namespace, Name: name, Err: err}
}

// Refused finds the innermost request refusal in err: the request and its
// status's finding. ok is false when err carries no RequestError or no
// status.
func Refused(err error) (req *RequestError, f Finding, ok bool) {
	var last *RequestError
	for e := err; e != nil; {
		var re *RequestError
		if !errors.As(e, &re) {
			break
		}
		last, e = re, re.Err
	}
	if last == nil {
		return nil, Finding{}, false
	}
	var st apierrors.APIStatus
	if !errors.As(last.Err, &st) {
		return last, Finding{}, false
	}
	return last, Classify(st.Status()), true
}

// Subject names the request's subject: "Kind ns/name", or "Kind name".
func (e *RequestError) Subject() string {
	if e.Namespace == "" {
		return e.Kind + " " + e.Name
	}
	return e.Kind + " " + e.Namespace + "/" + e.Name
}

// What says an attributed finding in a few words: what refused the
// request, never the server's free text.
func (f Finding) What() string {
	switch f.Envelope {
	case EnvWebhookDenied:
		return fmt.Sprintf("denied by admission webhook %q", f.Name)
	case EnvDryRunUnsupported:
		return fmt.Sprintf("admission webhook %q does not support dry run", f.Name)
	case EnvWebhookCallFailed:
		return fmt.Sprintf("admission webhook %q could not be called (%s)", f.Name, causeWords[f.Cause])
	case EnvVAPDenied:
		if f.Binding != "" {
			return fmt.Sprintf("denied by ValidatingAdmissionPolicy %q (binding %q)", f.Name, f.Binding)
		}
		return fmt.Sprintf("denied by ValidatingAdmissionPolicy %q", f.Name)
	case EnvPolicyDenied:
		if f.Binding != "" {
			return fmt.Sprintf("denied by admission policy %q (binding %q)", f.Name, f.Binding)
		}
		return fmt.Sprintf("denied by admission policy %q", f.Name)
	}
	return ""
}

var causeWords = map[string]string{
	CauseDeadline: "it did not answer in time", CauseRefused: "its connection was refused", CauseTLS: "its TLS certificate was not trusted",
	CauseNoEndpoints: "its Service has no endpoints", CauseOther: "the API server could not reach or read it",
}

// Sentence is the refused request's one line (S3b §2.3): nothing of that
// request was written, since admission precedes storage.
func Sentence(req *RequestError, f Finding) string {
	return fmt.Sprintf("%s %s was refused by admission before it was stored: %s", strings.ToLower(req.Request.Operation), req.Subject(), f.What())
}
