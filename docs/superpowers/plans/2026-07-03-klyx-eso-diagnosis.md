# M12: External Secrets Operator diagnosis lens

The M10 (Flux) / M11 (cert-manager) diagnosis pattern applied a third time, to
the third pillar of the owner's stack: ESO with Reloader. Same shape end to
end: capability detection → pure parser pkg → lazy watch → push service →
capability-gated broken-first view with a bound-resource headline.

## Why

A stuck ExternalSecret is diagnosed today with three kubectl invocations:
describe the ExternalSecret, find its store, describe the store. The stuck
cause is almost always the store (expired Azure credentials, wrong vault URL,
RBAC) - exactly the bound-issuer/bound-source insight from M10/M11.

## Vocabulary (speak ESO correctly)

- ExternalSecret Ready=True reason `SecretSynced` → **Synced**.
- Ready=False → **Failed** (reasons like `SecretSyncedError` carry the message).
- No Ready condition → **Unknown** (never guessed).
- **Refresh staleness is ESO-native staleness**: `status.refreshTime` vs
  `spec.refreshInterval`. `now - refreshTime > 2×interval` (grace ×2) means the
  controller has stopped refreshing - a distinct **Stale** signal even while
  Ready=True still describes the last successful sync. refreshInterval "0" or
  unset-by-default (1h) handled explicitly; interval 0 = never stale by time.
- SecretStore / ClusterSecretStore: Ready=True reason `Valid` → healthy;
  Ready=False (e.g. `InvalidProviderConfig`) is the headline for every
  ExternalSecret bound to it. Provider display name = the single key under
  `spec.provider` (azurekv, vault, aws, gcpsm, kubernetes, ...).

## Slices

- **M12-a detection**: `external-secrets.io` group served + the
  `external-secrets` Deployment health (namespace `external-secrets`);
  tier/reason/present/healthy on the cluster DTO; sidebar gating.
- **M12-b lens**: `internal/externalsecrets` pure pkg (ExternalSecret,
  SecretStore, ClusterSecretStore parsers + refresh staleness); lazy watch in
  fleet (opening/wanted single-flight, watch-error tracker, managedFields
  transform, GetByKey - the certmanagerwatch pattern verbatim);
  `ExternalSecretsService` push loop (domain-snap dirty check + 30s heartbeat);
  broken-first view with namespace/search/store filters, per-secret inspector
  with the bound store as the headline, target Secret name, provider, refresh
  ages. Read-only.
- **Deferred**: Reloader awareness (does a workload watch the target Secret?),
  ClusterExternalSecret/PushSecret kinds, day-2 "force refresh" verb
  (`force-sync` annotation patch - natural follow-up, needs protected-arm).

## Version resolution

Preferred version via the TTL discovery cache, fallback `v1beta1` (ESO ≥0.17
serves `v1`; older homelab installs may not).
