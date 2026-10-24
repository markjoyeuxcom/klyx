package cachediff

import "errors"

// Failure classes of Compute and Build. Each is fixed text that carries
// nothing read from the repository. A caller that must not echo repository
// content (AI A1) reports a failure's class, never its message: kustomize's
// own errors can quote a patch, Helm values included, and no scrubber
// recognises an ordinary value.
var (
	ErrInputMissing     = errors.New("a path the build reads does not exist at this revision")
	ErrInputUnsupported = errors.New("the build reads what the confined build does not support: a remote base, a Helm chart, a symbolic link, a submodule or a path outside the repository")
	ErrInputUnreadable  = errors.New("a kustomization file is not readable")
	ErrInputBound       = errors.New("the build reads more than its bound allows")
	ErrBuildStart       = errors.New("the confined build did not start")
	ErrBuildDeadline    = errors.New("the confined build passed its time bound and was stopped")
	ErrBuildOutput      = errors.New("the build's output passed its size bound")
	ErrBuildDecode      = errors.New("the build's output was not readable as Kubernetes objects")
	ErrBuildFailed      = errors.New("kustomize reported an error")
)

// classed is a failure whose message stays the build's own, for the
// operator's local diagnostic, and whose class errors.Is reports.
type classed struct{ class, err error }

func (e classed) Error() string   { return e.err.Error() }
func (e classed) Unwrap() []error { return []error{e.class, e.err} }

func class(c, err error) error { return classed{class: c, err: err} }
