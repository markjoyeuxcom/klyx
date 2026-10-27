package appbridge

import (
	"context"
	"fmt"
	"strconv"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/safehttp"
)

// Settings S3a §5: an installation's credential (source/<id>) is an
// immediate action on the installation in force, as a cluster endpoint's
// is: Store, Remove and Test under the endpoint lock, then the fleet Apply
// lock. It never adopts the installation's file block, and a credential is
// never copied from a cluster's record.

// WireSourceCredentials gives the endpoint actions the installations in
// force and their test (main: the registry's AlertSources and
// InstallationStatus).
func WireSourceCredentials(es *EndpointsService, active func() *fleet.AlertSources, status func(ctx context.Context, id string) (alertmanager.Status, string, error)) {
	es.sources, es.sourceStatus = active, status
}

// reviewedSource is the installation the person reviewed, still in force
// at its revision.
func (s *EndpointsService) reviewedSource(id, revision string) (config.SourceConfig, error) {
	if s.sources == nil || s.sources() == nil {
		return config.SourceConfig{}, fmt.Errorf("no installation is in force")
	}
	src, rev, ok := s.sources().Installation(id)
	if !ok {
		return config.SourceConfig{}, fmt.Errorf("%s is not in force", shownInstallation(id))
	}
	if strconv.FormatUint(rev, 10) != revision {
		return config.SourceConfig{}, fmt.Errorf("%s changed since it was shown: review it again", shownInstallation(id))
	}
	return src, nil
}

// SetSourceCredential stores the secret for an installation the person
// reviewed, bound to its endpoint's origin, under its own account; its
// credential revision moves, so every read takes it.
func (s *EndpointsService) SetSourceCredential(id, revision, secret string) EndpointActionDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	red := safehttp.NewRedactor([]string{secret})
	src, err := s.reviewedSource(id, revision)
	if err != nil {
		return fail(red, err)
	}
	switch {
	case s.store == nil:
		return EndpointActionDTO{Message: "keychain unavailable: no credential store"}
	case secret == "":
		return EndpointActionDTO{Message: "enter the credential to store"}
	case src.Endpoint == "":
		return EndpointActionDTO{Message: "a hosted installation is read through its host's API server; it takes no credential, and nothing was stored"}
	case src.Auth == nil:
		return EndpointActionDTO{Message: "this installation has no auth; add auth (bearer, or basic with a username) to it first"}
	}
	if p := config.EndpointURLProblem(src.Endpoint); p != "" {
		return EndpointActionDTO{Message: p}
	}
	origin, err := safehttp.OriginOf(src.Endpoint)
	if err != nil {
		return fail(red, err)
	}
	v, err := endpointcred.Record{V: 1, Origin: origin, Type: src.Auth.Type, Secret: secret}.Encode()
	if err != nil {
		return fail(red, err)
	}
	if s.fleetLock != nil {
		defer s.fleetLock()()
	}
	if err := s.store.Set(endpointcred.SourceAccount(id), v); err != nil {
		return fail(red, err)
	}
	s.revs.Bump("source/"+id, config.EndpointAlertmanager, true)
	return EndpointActionDTO{OK: true, Message: "Stored in the keychain for " + origin + "."}
}

// RemoveSourceCredential stops an installation's credential being used, at
// once, in memory, and then deletes it from the keychain.
func (s *EndpointsService) RemoveSourceCredential(id, revision string) EndpointActionDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	red := safehttp.NewRedactor()
	if _, err := s.reviewedSource(id, revision); err != nil {
		return fail(red, err)
	}
	if s.store == nil {
		return EndpointActionDTO{Message: "keychain unavailable: no credential store"}
	}
	if s.fleetLock != nil {
		defer s.fleetLock()()
	}
	s.revs.Revoke("source/"+id, config.EndpointAlertmanager)
	if err := s.store.Delete(endpointcred.SourceAccount(id)); err != nil {
		return EndpointActionDTO{Message: "Removed from use; the keychain still holds it - " + red.Text(err.Error())}
	}
	return EndpointActionDTO{OK: true, Message: "Removed from use and from the keychain."}
}

// TestSource reads the installation the person reviewed once, through the
// transport its reads take: its status, never its alerts.
func (s *EndpointsService) TestSource(id, revision string) EndpointActionDTO {
	s.mu.Lock()
	_, err := s.reviewedSource(id, revision)
	s.mu.Unlock()
	if err != nil {
		return fail(safehttp.NewRedactor(), err)
	}
	if s.sourceStatus == nil {
		return EndpointActionDTO{Message: "Test is not available"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), metricsTimeout)
	defer cancel()
	st, where, err := s.sourceStatus(ctx, id)
	if err != nil {
		return EndpointActionDTO{Message: "Unavailable: " + safehttp.NewRedactor().Text(err.Error())}
	}
	return EndpointActionDTO{OK: true, Message: "Available: " + amStatusText(st) + " at " + where + ", answered."}
}
