package safehttp

import (
	"context"
	"encoding/base64"
	"errors"
	"sort"
	"strings"

	"github.com/moomora/klyx/internal/scrub"
)

// Redacted replaces a secret in redacted text.
const Redacted = "[redacted]"

// Redactor removes the exact credential a request carries, in every form it
// takes on the wire, from text about that request (coverage C5 §5): an
// endpoint that echoes the Authorization header into an error, a body quoted
// by a parse error, a transport error. The transport holding a secret
// redacts its own outputs, so no reader downstream sees it; Text adds the
// general credential shapes on top, for messages that leave Klyx's code.
type Redactor struct{ secrets []string }

// Bearer is the forms a bearer token takes: itself and its header value.
func Bearer(token string) []string {
	if token == "" {
		return nil
	}
	return []string{token, "Bearer " + token}
}

// Basic is the forms a basic credential takes: the password, the encoded
// username:password, and its header value.
func Basic(username, password string) []string {
	if password == "" {
		return nil
	}
	enc := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return []string{password, enc, "Basic " + enc}
}

// NewRedactor redacts the given forms, longest first, so a header value is
// replaced whole before the token inside it.
func NewRedactor(forms ...[]string) Redactor {
	var all []string
	for _, f := range forms {
		for _, s := range f {
			if s != "" {
				all = append(all, s)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return len(all[i]) > len(all[j]) })
	return Redactor{secrets: all}
}

// Empty reports whether there is nothing to redact.
func (r Redactor) Empty() bool { return len(r.secrets) == 0 }

// Exact removes the exact secrets only: for a response body still to be
// parsed, where general shapes could alter real data.
func (r Redactor) Exact(s string) string {
	for _, sec := range r.secrets {
		s = strings.ReplaceAll(s, sec, Redacted)
	}
	return s
}

// ExactBytes is Exact for a body.
func (r Redactor) ExactBytes(b []byte) []byte {
	if r.Empty() {
		return b
	}
	return []byte(r.Exact(string(b)))
}

// Text removes the exact secrets, then every general credential shape: for a
// message about to leave the endpoint code. Clip after this, never before.
func (r Redactor) Text(s string) string {
	out, _ := scrub.Credentials(r.Exact(s))
	return out
}

// Err is err with its text redacted. It keeps errors.Is for the
// cancellation and deadline sentinels a caller tests for, and nothing else:
// the original error, whose text may hold the secret, is not reachable.
func (r Redactor) Err(err error) error {
	if err == nil {
		return nil
	}
	re := &redactedError{msg: r.Text(err.Error())}
	for _, s := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, s) {
			re.is = append(re.is, s)
		}
	}
	return re
}

type redactedError struct {
	msg string
	is  []error
}

func (e *redactedError) Error() string { return e.msg }

func (e *redactedError) Is(target error) bool {
	for _, s := range e.is {
		if s == target {
			return true
		}
	}
	return false
}
