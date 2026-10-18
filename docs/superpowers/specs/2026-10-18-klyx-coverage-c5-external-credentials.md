# Klyx coverage C5: credentials for external endpoints

Roadmap §1 C5 (`2026-10-16-klyx-coverage-roadmap.md`): a separate, reviewed preparatory change before S8 (Alertmanager, Loki). It gives every external HTTP endpoint Klyx reads one way to be found, authenticated and bounded, and moves the one credential Klyx keeps in plain text into the OS keychain.

## 1. What is there today (read from the code at c72224e)

**Configuration.** Metrics are per cluster only (`clusters[].metrics`, `internal/config/config.go:119-134`): `endpoint`, `token`, `tlsSkipVerify`, and `serviceRef` (namespace, name, port, scheme).
- There are no header, tenant, basic-auth or CA fields.
- `config.Load` decodes non-strictly, so unknown keys are silently ignored.

**Resolution.** `metrics.Resolve` (`internal/metrics/resolve.go:22-45`) tries the endpoint, then `serviceRef`, then discovery:
- **Direct:** a fresh `http.Transport` with a 3s client timeout. It sends `Authorization: Bearer <token>` when a token is set (`internal/metrics/transport.go:39-41`).
- **`serviceRef` and discovery:** both go through the kube-apiserver's service proxy with the kubeconfig credentials. The metrics token is never attached there (`Proxy(c)` takes no token).

**The plaintext token** lives in `fleet.yaml` and is read only at `resolve.go:29`.
- It is never logged, never sent to the frontend (`FleetClusterDTO` carries only `hasMetrics`) and never written by Klyx.
- No Klyx writer edits `clusters[].metrics` (`blockwrite.go` replaces top-level blocks; `AppendClusters` writes name, context, protected and tags).

**Gaps against C5:**
- **Redirects.** The direct client has no `CheckRedirect`: Go's default follows 10 redirects. It drops Authorization on a cross-host redirect but keeps it on a same-host HTTPS-to-HTTP downgrade.
- **Response size.** Responses are cut at 4 MiB by `io.LimitReader` without an error (`transport.go:18`), so a truncated body reads as a whole one.
- **The endpoint URL** is displayed as `source` (`Overview.tsx:488`). Userinfo in it would show and be sent.

**What exists to build on:**
- **The keychain.** `internal/ai/keystore.go` (zalando/go-keyring; service `io.moomora.klyx`, account `ai/<provider>`). A missing key, a locked or denied store, and an empty key are three distinct errors.
- **Migration precedent.** `MoveKeyToKeychain` writes the keychain first, then removes the key from the file, and says so when the second step fails.
- **A hardened client.** `internal/ai/transport.go`'s `Harden`:
  - redirects only to the same host, with no downgrade and at most 5 hops;
  - a capped transport that errors instead of truncating.

## 2. One endpoint shape for metrics, Alertmanager and Loki

A cluster may configure three external endpoints of one shape: the existing `metrics` block, and two new ones, `alertmanager` and `loki`. C5 parses, validates and shows the new ones; S8 reads them.

```yaml
clusters:
  - name: prd-weu
    metrics:
      endpoint: https://mimir.example/prometheus   # or serviceRef, or discovery
      auth: {type: bearer}                         # the token is in the keychain
      tenant: platform                             # X-Scope-OrgID, configuration
    alertmanager:
      serviceRef: {namespace: monitoring, name: alertmanager-operated, port: 9093}
    loki:
      endpoint: https://loki.example
      auth: {type: basic, username: klyx-reader}   # the password is in the keychain
      tenant: platform
```

**Fields:**
- `endpoint`, `serviceRef` and `tlsSkipVerify` keep their meaning.
- `auth.type` is `bearer` or `basic`; `auth.username` is the basic username, which is configuration and not a secret.
- `tenant` is sent as `X-Scope-OrgID`. Mimir, Loki and Cortex use it; it is configuration, not a secret, and it is shown in Settings.

**Resolution** for each endpoint, in order: the direct `endpoint`, then `serviceRef` through the apiserver proxy, then discovery. Discovery covers metrics as today. For Alertmanager and Loki it is a named-candidate list in S8; C5 defines no candidates.

**Validation:**
- **An `endpoint` with userinfo** (`https://user:pass@host`) is refused for use, not for loading. The endpoint reads unavailable: "the endpoint URL carries credentials; set them in Settings > Endpoints". The userinfo is never sent and never shown: the source is displayed through `urlscrub`.
- **Auth on a proxied endpoint.** `auth` together with a `serviceRef` (or discovery) is a configuration warning: external credentials never ride the kube-apiserver's authentication, so they are not sent on that path (§4).
- **The legacy `metrics.token`** is still read (§5).

## 3. Credentials: endpoint-scoped, in the keychain

**One keychain item per endpoint.**
- **Account:** `endpoint/<cluster>/<metrics|alertmanager|loki>`, beside the AI keys' `ai/<provider>`, under the same service `io.moomora.klyx`.
- **Value:** a small JSON document `{origin, type, secret}`.
  - `origin` is the scheme, host and port of the endpoint it was stored for.
  - `secret` is the bearer token, or the basic password (the username stays in the configuration).

**Endpoint-scoped.** A credential is sent only to the origin it was stored for.
- **When the configured endpoint's origin changes,** the credential is withheld and the endpoint says so: "the stored credential is for https://old.example; the endpoint is now https://new.example - set it again".
- **A config edit therefore never silently sends a token to a new host.**

**The store's states**, kept distinct as the AI keys keep them, and shown per endpoint:

| State | Meaning |
|---|---|
| not needed | no `auth` configured |
| stored | in the keychain, for this endpoint's origin |
| missing | `auth` configured, nothing stored |
| for another origin | stored, but for a different origin |
| keychain unavailable | locked, denied, or no store |
| legacy | a plaintext `metrics.token` in the file (§5) |

An endpoint whose credential is missing, for another origin, or unavailable reads **unavailable** with that reason. It never reads "no metrics", and it is never attempted without the credential.

**The store interface** moves from `internal/ai` to a shared package (`internal/secretstore`): `Get`, `Set` and `Delete` by account, with `ErrNoKey` and `KeychainError`. AI keeps its accounts and behaviour; C5 adds the endpoint accounts. Tests use an in-memory store with injectable failures, as `memKeys` does today.

## 4. The transport: bounded, and credentials never redirected

One client builder, `internal/safehttp`, serves every external endpoint. It is `ai.Harden`'s policy, generalised and shared, and AI moves onto it.

**Direct endpoints:**
- **Redirects** are followed only to the same origin (scheme, host and port), never from HTTPS to HTTP, and at most 5 hops. Any other redirect is an error ("redirected to another origin; Klyx does not follow it"), whether or not the request carries a credential.
- **Credentials** go only as the request's `Authorization` header, set by the client for the stored origin. They are never in the URL, and never re-sent by redirect handling.
- **The tenant header** is set on every request to the endpoint, including a followed same-origin redirect.
- **Responses** are capped per endpoint: 4 MiB for metrics as today, and S8 sets Alertmanager's and Loki's. Past the cap the read is an error (`ErrResponseTooLarge`), never a truncated body read as whole.
- **Timeouts** are per endpoint: 3s for metrics as today. The caller's context cancels.
- **No environment proxy**, as today: the transport's `Proxy` stays nil.

**Proxied endpoints** (`serviceRef`, discovery):
- They use the kubeconfig credentials only. `Authorization` from `auth` is never added: the apiserver would consume or reject it, and the kube credentials must never leave for an external origin.
- `tenant` is sent as a header on the proxied request. Verified on `kind-klyx` (v1.37, 2026-10-18) with an agnhost `netexec` Service behind `/api/v1/namespaces/<ns>/services/http:echo:80/proxy`:
  - `X-Scope-OrgID: tenant-a` arrives at the Service as sent.
  - An `Authorization` header from the client does not arrive.

  An external credential on this path would therefore never reach the Service either, which is one more reason not to send it.
- The apiserver proxy's own redirect handling is the apiserver's. Klyx adds the same response cap and the caller's timeout.

**Isolation, stated as tests (§7):**
- an external credential is never sent through the apiserver proxy;
- kube credentials are never sent to an external origin;
- no credential follows a redirect to another origin or a downgrade.

## 5. Migrating the plaintext metrics token

The legacy `metrics.token` keeps working until it is moved, so nothing breaks on upgrade.

**Settings > Endpoints** shows it as "legacy: stored in plain text in fleet.yaml", with **Move to keychain**. The move follows `MoveKeyToKeychain`'s order:
1. **Write the keychain item** for the endpoint's origin. On failure nothing changes, and the store's error is shown.
2. **Edit fleet.yaml:** replace that cluster's `token:` line with `auth: {type: bearer}`.
   - This is a new per-cluster textual edit in `internal/config`, under the shared `fleetWriteMu`. It writes a temporary file, re-runs `Load` on it, and checks through the decoded diff that only that cluster's `metrics.token` and `metrics.auth` changed. It then renames over the original, and refuses with `ErrFleetChangedOnDisk` if the file changed meanwhile.
   - The file's mode is kept. Today's writers leave fleet.yaml at 0600 after any edit; this edit preserves whatever mode it had.
3. **If step 2 fails**, the keychain holds the token and the file still holds it in plain text. The state reads "stored in the keychain, and still in plain text in fleet.yaml", and **Move to keychain** stays offered: both steps are idempotent, so it retries.

Klyx never writes a token into fleet.yaml, and never deletes the plaintext before the keychain write has succeeded.

**Which token wins.** While both exist, the file's token is used: it is what the user last wrote. Once the file no longer holds it, the keychain's is used.

## 6. What the operator sees

**Settings > Endpoints (new).** Per cluster, one row per configured endpoint:
- **The endpoint:** the source as configured (URL through `urlscrub`, or `serviceRef`), and its tenant.
- **The credential's state** (§3).
- **Actions:** Set (a password field; bearer token, or basic password with the configured username), Remove, Move to keychain (legacy only), and Test. Test reads the endpoint once and reports available, unavailable, or the reason.

Endpoints themselves are still edited in fleet.yaml, as metrics are today. Settings writes credentials, and only the `token` to `auth` edit in §5.

**Elsewhere:**
- **The Overview's metrics source** is shown through `urlscrub`.
- **"Unavailable" reasons** carry the credential state ("credential missing", "keychain unavailable", "stored for another origin").

## 7. Tests

- **Config:** parsing and validation of the three blocks, `auth` and `tenant`; userinfo refused for use; `auth` with `serviceRef` warned; legacy `token` still read.
- **Store:** every state in §3 with the in-memory store, including locked and unavailable; values round-trip; the origin check.
- **Transport** (an `httptest` server pair):
  - same-origin redirects followed, cross-origin and downgrade refused, at most 5 hops;
  - `Authorization` present on the first request only for the stored origin, and never on a refused redirect;
  - the tenant header on every request;
  - the cap returns an error at the limit plus one byte, never a short body.
- **Proxy:** a proxied request carries the tenant header and no `Authorization` from `auth`. An external credential is never added on this path, whatever the configuration.
- **Migration:**
  - keychain failure, then nothing changed;
  - file failure after the keychain write, then both and retryable;
  - a concurrent edit gives `ErrFleetChangedOnDisk`;
  - the file mode is kept;
  - the decoded diff limits the change to that cluster's `metrics` keys.
- **UI:** Settings > Endpoints states and actions, with no secret ever read back into the page (`TestAISettings_StateCarriesNoKey`'s pattern). The scrubbed source.
- **The metrics path end to end** with a keychain token: the same readings as with the legacy token.

## 8. Staging

One change:
1. `internal/secretstore` and `internal/safehttp` (AI moved onto both, unchanged in behaviour).
2. The config blocks and validation.
3. Metrics resolved through them, with the legacy token still read.
4. The migration edit.
5. Settings > Endpoints.

S8 then adds the Alertmanager and Loki readers on this base.

## 9. Not in C5

- Readers for Alertmanager and Loki (S8a, S8b), and their discovery candidates.
- Client certificates and custom CA bundles: a later request, not a C5 need.
- Writing endpoints from Settings: the file stays the place they are authored.
- Scrubbing the operator's own views: log text bound for AI goes through `internal/scrub` (the chat logs tool already does); the operator's log view stays unscrubbed and honest.
