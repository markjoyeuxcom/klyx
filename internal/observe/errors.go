package observe

import (
	"context"
	"errors"
	"io"
	"net"
	"os/exec"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// classify names an error for coverage. Credentials that cannot list a kind
// are forbidden/unauthorized - never "controller down".
func classify(err error) string {
	switch {
	case apierrors.IsForbidden(err):
		return ClassForbidden
	case apierrors.IsUnauthorized(err):
		return ClassUnauthorized
	case apierrors.IsNotFound(err):
		return ClassNotFound
	case apierrors.IsTimeout(err), apierrors.IsServerTimeout(err), errors.Is(err, context.DeadlineExceeded):
		return ClassTimeout
	case errors.Is(err, exec.ErrNotFound):
		return ClassCLIMissing
	case isTransport(err):
		return ClassTransport
	default:
		return ClassOther
	}
}

func isTransport(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// benign errors are the watch protocol's own recovery paths: an expired or
// too-large resourceVersion makes the reflector list again and Replace, so
// they never invalidate freshness (spec §3.1).
func benign(err error) bool {
	return apierrors.IsResourceExpired(err) || apierrors.IsGone(err) ||
		apierrors.HasStatusCause(err, metav1.CauseTypeResourceVersionTooLarge)
}
