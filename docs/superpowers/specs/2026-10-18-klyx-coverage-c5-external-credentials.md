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

**Alertmanager and Loki in C5.** Their blocks are parsed, validated and shown with their credential state in C5, so a credential can be set before S8 reads them. C5 does not read them.

**Validation:**
- **An `endpoint` with userinfo** (`https://user:pass@host`) is refused for use, not for loading. The endpoint reads unavailable: "the endpoint URL carries credentials; set them in Settings > Endpoints". The userinfo is never sent and never shown: the source is displayed through `urlscrub`.
- **Auth on a proxied endpoint.** `auth` together with a `serviceRef` (or discovery) is a configuration warning: external credentials never ride the kube-apiserver's authentication, so they are not sent on that path (§4).
- **The legacy `metrics.token`** is still read (§6).

## 3. Credentials: endpoint-scoped, in the keychain

**One keychain item per endpoint.**
- **Account:** `endpoint/<cluster>/<metrics|alertmanager|loki>`, beside the AI keys' `ai/<provider>`, under the same service `io.moomora.klyx`.
- **Value:** a small, versioned JSON document `{v: 1, origin, type, secret}`.
  - `origin` is the canonical origin of the endpoint it was stored for.
  - `type` is `bearer` or `basic`.
  - `secret` is the bearer token, or the basic password; the username stays in the configuration.

  A record that does not parse, has another version, or whose `type` differs from the configured `auth.type` is refused: "the stored credential cannot be used (‹why›) - set it again". It is never guessed at.

**Canonical origin.** An origin is the scheme in lower case, the host lower-cased and IDNA-normalised (Unicode to its ASCII form, IPv6 in brackets), and the effective port (443 for https and 80 for http when none is written). Two origins match only when all three are equal. A URL that cannot be parsed has no origin, and nothing is sent to it.

**Endpoint-scoped.** A stored credential is sent only to the origin it was stored for.
- **When the configured endpoint's origin changes,** the credential is withheld and the endpoint says so: "the stored credential is for https://old.example; the endpoint is now https://new.example - set it again".
- **A config edit therefore never sends a stored token to a new host.**
- **The legacy plaintext `metrics.token` is the one exception** (§6): it is whatever fleet.yaml says, for whatever origin fleet.yaml names, as today, until it is moved.

**The store's states**, kept distinct as the AI keys keep them, and shown per endpoint:

| State | Meaning |
|---|---|
| not needed | no `auth` configured |
| stored | in the keychain, for this endpoint's origin |
| missing | `auth` configured, nothing stored |
| for another origin | stored, but for a different origin |
| unusable record | stored, but not a record Klyx can use (above) |
| keychain unavailable | locked, denied, or no store |
| legacy | a plaintext `metrics.token` in the file (§6); with "and stored in the keychain" when both exist |

An endpoint whose credential is missing, for another origin, unusable or unavailable reads **unavailable** with that reason. It never reads "no metrics", and it is never attempted without the credential.

**The store interface** moves from `internal/ai` to a shared package, `internal/secretstore`: `Get`, `Set` and `Delete` by account, with `ErrNoKey` and `KeychainError`. AI keeps its accounts and behaviour; C5 adds the endpoint accounts. Tests use an in-memory store with injectable failures, as `memKeys` does today.

## 4. The transport: bounded, and no credential ever redirected

One client builder, `internal/safehttp`, serves every external endpoint. It is `ai.Harden`'s policy generalised: same-origin redirects only and an erroring cap. Each user states its own environment-proxy policy explicitly:
- **AI keeps `ProxyFromEnvironment`**, its behaviour today (`ai/transport.go:35` clones `http.DefaultTransport`).
- **Metrics, Alertmanager and Loki keep `Proxy = nil`,** metrics' behaviour today.

Nothing about AI's egress changes.

**Direct endpoints:**
- **Redirects** are followed only to the same canonical origin, never from HTTPS to HTTP, and at most 5 hops. Any other redirect is refused before a request is made to its target: "redirected to another origin; Klyx does not follow it". This holds whether or not the request carries a credential.
- **Authorization** is set by the client on every request to the stored origin, the first and every allowed same-origin hop alike. It is never put in a URL. A refused redirect sends nothing, so no credential reaches another origin or a downgraded connection.
- **The tenant header** is set on every request to the endpoint, and on every allowed hop.
- **Responses** are capped per endpoint: 4 MiB for metrics as today, and S8 sets Alertmanager's and Loki's. Past the cap the read is an error (`ErrResponseTooLarge`), never a truncated body read as whole.
- **Timeouts** are per endpoint: 3s for metrics as today. The caller's context cancels.

**Proxied endpoints** (`serviceRef`, discovery) go through the apiserver's service proxy with the kubeconfig credentials, and only those.
- **The `auth` credential is never added.** The apiserver would consume or reject it, and the kube credentials must never be mixed with an external origin's.
- **A dedicated client fences the apiserver origin** (Codex's design review, finding 1):
  - The service-proxy path no longer uses the clientset's shared REST client, whose `http.Client` follows redirects. client-go's auth round-tripper re-adds `Authorization` (and the TLS transport presents its client certificate) on every redirected request, so a backend's `302` to another origin through the proxy would carry the user's kube credential there. A local probe with today's code shows it.
  - C5 builds a separate client from the same `rest.Config` (`rest.HTTPClientFor`, then `rest.RESTClientForConfigAndClient`). Its `CheckRedirect` allows only the apiserver's own canonical origin, refuses any downgrade and any other origin, and allows at most 5 hops.
  - `CheckRedirect` runs before the redirected request is handed to the transport stack, so a refused redirect never reaches the bearer, basic, client-certificate or exec-plugin layers: **the other origin receives no request at all**.
  - The response cap and the caller's timeout apply here too.
- **What the Service does with a request is a separate hop,** behind the apiserver. Klyx's guard is the client-side one.
- **The tenant header** is sent on the proxied request. Verified on `kind-klyx` (v1.37, 2026-10-18) with an agnhost `netexec` Service behind `/api/v1/namespaces/<ns>/services/http:echo:80/proxy`:
  - `X-Scope-OrgID: tenant-a` arrives at the Service as sent.
  - An `Authorization` header from the client does not arrive.

## 5. A credential-safe diagnostic boundary

Klyx's endpoint diagnostics must never carry a credential back out (Codex's design review, finding 3). Today they can:
- the Prometheus parser copies a response's `error` field into its error (`internal/metrics/parse.go:79`);
- `Probe` copies that into the capability's `Reason` (`capability.go:14`);
- `MetricsService` returns it to the page.

An endpoint that echoes the `Authorization` header into a JSON error therefore puts the token on screen. A local probe shows it.

**The boundary.** Every string that leaves the endpoint client for anything Klyx shows, returns or logs is redacted first. That covers:
- an error;
- a capability reason;
- a Test result;
- a "Read notes" line;
- an Activity entry.

This is `safehttp`'s `Redactor`, built per request from the credential actually in use, and applied before any clipping:
- **The exact secrets** go: the bearer token; the basic password; the base64 `username:password`, with and without `Basic `; `Bearer ‹token›`.
- **Then `scrub.Credentials`** removes credential shapes generally, which covers a legacy token and a header echoed in an unexpected form.

**Where it sits.** Endpoint errors leave the endpoint packages (`internal/metrics` today, the S8 readers next) only through it. The DTO converters keep their own field allowlists. A test feeds every reader an endpoint that echoes the request's credentials in each place a message can come from:
- a JSON error body, for both instant and range queries;
- a plain-text body;
- a transport error;
- a keychain error;
- a parse failure quoting the body.

The test asserts that no form of the secret reaches a public DTO, a log line or the Test result.

**The operator's own log view** (the logs pane, and Loki's in S8) stays unscrubbed and honest. The boundary covers Klyx's auth diagnostics, not the application logs the operator asked to read.

## 6. Changes reviewed, fenced and serialised

Settings acts on what the person reviewed, never on whatever fleet.yaml says by the time the handler runs (Codex's design review, finding 2).

**A revision per endpoint.** The Settings state carries, for each endpoint, an opaque revision: a hash of the cluster's endpoint entry as read (origin, `auth`, username, tenant, and for a legacy entry a hash of the token), plus the file's mode. Every action (Set, Remove, Move to keychain, Test) sends back the revision the person saw.

**One action at a time.** Endpoint actions are serialised by one lock, held for the whole action, keychain calls included. The lock is taken before the file writer's `fleetWriteMu`, in one order only.

**Each action re-reads the file under the lock and refuses on drift.** If the endpoint's revision differs from the one sent, nothing is written: "this endpoint changed in fleet.yaml since you looked; review it and try again". So:
- **Set** binds the entered secret to the origin the person saw, or to nothing.
- **Remove** removes the credential of the endpoint the person saw.
- **Test** tests that endpoint.

**Move to keychain** is a transaction against one snapshot:
1. **Snapshot.** Under the lock, read fleet.yaml once: its bytes, mode and modification time. Check the action's revision against it, and take the token from that snapshot.
2. **Keychain.** Write the keychain item for the snapshot's origin. On failure nothing changes, and the store's error is shown, redacted (§5).
3. **The file edit, against the original snapshot.** Prepare the edit (that cluster's `token:` line becomes `auth: {type: bearer}`) from the snapshot's bytes:
   - in a private temporary file, mode 0600, beside fleet.yaml;
   - re-run `Load` on it, and check through the decoded diff that only that cluster's `metrics.token` and `metrics.auth` changed;
   - give it the snapshot's mode;
   - just before the rename, compare fleet.yaml's current bytes and mode with the snapshot. If anything changed since step 1 (another editor, another Klyx action), the newer file is left untouched and the temporary file removed: "the token is stored in the keychain for ‹origin›; fleet.yaml changed meanwhile and still holds a token - review it and move again".
4. **If step 3 fails** for any reason, the keychain holds the token and the file still holds it in plain text. The state reads "legacy, and stored in the keychain", and Move stays offered: every step is idempotent, so it retries.

Klyx never writes a token into fleet.yaml, and never deletes the plaintext before the keychain write has succeeded.

**Which token wins.** While both exist, the file's token is used: it is what the person last wrote, and the move is not finished. Once the file no longer holds it, the keychain's is used.

## 7. Credentials at run time: revision and revocation

A stored credential reaches requests only through the endpoint's current credential revision (Codex's design review, finding 4).

**What is cached today.** A metrics resolution and its token-bearing transport are cached for the connection's lifetime (`fleet/metrics.go:42-46, 101-122`). A keychain change that only updated storage would leave the old client sending the old token.

**The revision.** Each endpoint has an in-memory credential revision that moves on:
- Set, Remove and a successful Move;
- a fleet.yaml reload that changes the endpoint;
- the first read after start.

**Invalidation.** Every cached resolution, transport and availability records the revision it was built under. A read finds a different revision, discards them, and rebuilds. This covers every path: instant, range, liveness, workload, node, route and rule metrics, and S8's readers.

**Fencing in-flight reads.** A read started under an older revision may finish after the change. Its result and its availability are dropped, never cached or shown: the cache write is a compare-and-set on the revision.

**Remove stops authentication first.** For an active keychain credential:
1. The endpoint is marked revoked in memory, with a tombstone that no client may load past.
2. The revision moves and the cached clients are dropped.
3. Only then is the keychain item deleted.

New authenticated requests therefore stop at step 1, even when the keychain delete fails. The failure is then shown ("removed from use; the keychain still holds it - ‹error›"), and Remove stays offered. A restart re-reads the keychain, so the tombstone is in-memory by design, and the state says so until the delete succeeds.

**Remove while a legacy token wins** is labelled for what it does: "Remove the keychain copy - fleet.yaml still holds a token, and that is what Klyx sends". It never promises that authentication stopped.

## 8. What the operator sees

**Settings > Endpoints (new).** Per cluster, one row per configured endpoint (metrics, Alertmanager, Loki):
- **The endpoint:** the source as configured (URL through `urlscrub`, or `serviceRef`), and its tenant.
- **The credential's state** (§3).
- **Actions** (§6, each carrying its revision):
  - **Set:** a password field. The bearer token, or the basic password with the configured username.
  - **Remove,** labelled as §7 says when a legacy token wins.
  - **Move to keychain:** legacy only.
  - **Test,** metrics only in C5. Test reads the endpoint once and reports available, unavailable, or the redacted reason. For Alertmanager and Loki, Test reads "not implemented until S8": a successful `GET /` is not a reader's availability, and S8 defines their API, cap and liveness.

Endpoints themselves are still edited in fleet.yaml, as metrics are today. Settings writes credentials, and only the `token` to `auth` edit in §6.

**Elsewhere:**
- **The Overview's metrics source** is shown through `urlscrub`.
- **"Unavailable" reasons** carry the credential state ("credential missing", "keychain unavailable", "stored for another origin").

## 9. Tests

- **Config:**
  - parsing and validation of the three blocks, `auth` and `tenant`;
  - userinfo refused for use;
  - `auth` with `serviceRef` warned;
  - the legacy `token` still read.
- **Store:**
  - every state in §3 with the in-memory store, including locked, unavailable and unusable records (unparseable, another version, type mismatch);
  - canonical origin matching: case, IDNA, default ports, IPv6.
- **Direct transport** (an `httptest` server pair):
  - same-origin redirects followed, cross-origin and downgrade refused before any request reaches the target, at most 5 hops;
  - `Authorization` on every request to the stored origin, and nothing at all at a refused target;
  - the tenant header on every allowed request;
  - the cap returns an error at the limit plus one byte, never a short body.
- **Proxy client** (a fake apiserver that redirects to a second origin):
  - with bearer and with basic kube credentials, the second origin receives no request at all;
  - mTLS and exec-plugin configurations are covered by the same assertion, because the redirected request is never handed to the transport;
  - the tenant header passes, and no `auth` credential is added;
  - Codex's characterisation probe is adopted.
- **Diagnostics** (§5): every reader, every message source, every encoding of the secret, through every public boundary; Codex's echo probe is adopted.
- **Fencing** (§6):
  - an edit to fleet.yaml before Set, between Set's read and its keychain write, and after it;
  - the same for Move, between its keychain write and its file edit: the newer file untouched, the staged item reported, the retry working;
  - stale-UI Set, Remove and Test refused;
  - a mode change alone counts as drift;
  - competing actions serialised;
  - the temporary file private.
- **Run time** (§7), each on an endpoint that is already available and cached:
  - Set, Remove and Move change what the next request sends;
  - an in-flight read under the old revision neither caches nor shows;
  - Remove stops authenticated requests even when the keychain delete fails;
  - the legacy-wins label.
- **UI:** Settings > Endpoints states and actions with no secret ever read back into the page (`TestAISettings_StateCarriesNoKey`'s pattern); the scrubbed source; Test disabled for Alertmanager and Loki.
- **The metrics path end to end** with a keychain token: the same readings as with the legacy token.

## 10. Staging

One change, in order:
1. `internal/secretstore` and `internal/safehttp`, with AI moved onto both, its behaviour unchanged (environment proxy included).
2. The proxy client's apiserver-origin redirect guard. This also fixes today's metrics path.
3. The diagnostic boundary on today's metrics reads.
4. The config blocks for metrics, Alertmanager and Loki, with validation and the canonical origin.
5. Metrics resolved through them, with the credential revision, the cache invalidation and the legacy token still read.
6. The fenced actions and the migration edit.
7. Settings > Endpoints.

S8 then adds the Alertmanager and Loki readers on this base.

## 11. Not in C5

- Readers for Alertmanager and Loki (S8a, S8b), their discovery candidates, and their Test.
- Client certificates and custom CA bundles: a later request, not a C5 need.
- Writing endpoints from Settings: the file stays the place they are authored.
- Scrubbing the operator's own views: log text bound for AI goes through `internal/scrub` (the chat logs tool already does); the operator's log view stays unscrubbed and honest.

## 12. Revision 1: Codex's design review of dbd7da0

| # | Finding | Where answered |
|---|---|---|
| 1 (P1) | A service-proxy redirect would carry the kube credential to another origin (client-go re-adds auth on redirected requests); "the apiserver's own handling" does not establish isolation | §4: a dedicated client for the proxy path, with an apiserver-origin `CheckRedirect` that refuses before the redirected request reaches the auth/TLS stack; bearer, basic, mTLS and exec covered; this also fixes today's metrics path (§10 step 2) |
| 2 (P1) | The keychain-first migration and Set could act on a fleet.yaml that changed after the person reviewed it | §6: an opaque per-endpoint revision carried by every action; one serialising lock; refusal on drift; Move as a transaction against one snapshot, the file compared with that snapshot (bytes and mode) before the rename, the newer file left untouched |
| 3 (P1) | Endpoint errors can carry a credential back out (a JSON error echoing the header reaches the capability reason) | §5: a per-request redactor of the exact secrets in every encoding, then `scrub.Credentials`, before clipping, at the endpoint packages' boundary; tested for every reader and message source |
| 4 (P2) | Cached clients keep sending a changed or removed credential | §7: a per-endpoint credential revision; every cached resolution, transport and availability invalidated by it; in-flight completions fenced by compare-and-set; Remove revokes in memory before deleting from the keychain; the legacy-wins Remove label |

Codex's answers, adopted:
- origin binding, with canonical comparison and refused malformed or mismatched records, and the legacy token stated as the exception (§3);
- every cross-origin redirect refused (§4);
- the file's token wins while both exist (§6);
- the authored mode kept, with private temporary files (§6);
- Alertmanager and Loki blocks parsed, validated and shown in C5, their Test "not implemented until S8" (§2, §8);
- AI's environment proxy kept, and auth on allowed same-origin hops stated (§4).

**Accepted at 58a234c** (Codex, design revision 1, handoff bcd4aa47, 2026-10-18). This accepts the design only; the implementation keeps its own review.

## 13. As built

Built on `feat/coverage` in six stages, after the design's acceptance at 58a234c:
- 464af65: the shared secret store and the safe HTTP policy.
- 2409e8c: the guarded proxy client and the diagnostic boundary, fixing today's metrics path.
- cf5ac9f: the config blocks.
- c30ac3b: credentials with revisions.
- f8f0035 and 24fedc7: the fenced actions and the migration.
- 36b465d: Settings > Endpoints.

### 13.1 The shared pieces (§3, §4)

- **`internal/secretstore`** holds every Klyx secret by account under `io.moomora.klyx`. AI moves onto it: its accounts are unchanged, its `KeychainError` is the shared type (an alias), and it keeps its own `ErrNoKey` and wording.
- **`internal/safehttp`** carries the shared HTTP policy:
  - the canonical origin (scheme, IDNA host, IPv6 bracketed, effective port);
  - `SameOriginRedirect`, at most 5 hops; a downgrade is another origin;
  - `Cap`, which errors past its limit;
  - `NewClient` with an explicit environment-proxy policy;
  - the `Redactor`: the exact forms of a credential, longest first, then `scrub.Credentials`. `Err` keeps only the cancellation and deadline sentinels, so the original error, whose text may hold the secret, cannot be reached.
- **AI's behaviour is unchanged.** Its cap moves onto `safehttp.Cap`. Its redirect check and its environment proxy stay its own.

### 13.2 The proxy client and the diagnostic boundary (§4, §5), fixing today's path

- **`metrics.NewGuardedProxyREST`** builds the CoreV1 REST client from the cluster's `rest.Config` (`rest.HTTPClientFor`, then `rest.RESTClientForConfigAndClient`), with `CheckRedirect = SameOriginRedirect`.
  - The metrics service-proxy path uses it whenever the connection has a `rest.Config`. Fake clients without one use their clientset's.
  - If the guarded client cannot be built, the proxy path reads unavailable with the reason. It never falls back to an unguarded client.
- **Codex's probe is adopted:** with bearer and with basic kube credentials, the second origin receives no request at all. A same-origin redirect is still followed, and a fleet-level test proves the connection really uses the guarded client.
- **The transport that holds a secret redacts its own outputs.** The direct transport replaces every exact form of its credential in the response body before any parse can quote it, and in every error. The probe's reason then loses general credential shapes too.
- **Codex's echo probe is adopted,** and extended to JSON and plain-text echoes, instant and range queries, and a credential shape the endpoint wrote that is not the request's own.
- **The proxied read's 4 MiB cap is now an error** (`ErrResponseTooLarge`). It used to truncate silently; the direct read's cap is `safehttp`'s.

### 13.3 The config (§2)

- **`metrics`, `alertmanager` and `loki` share one shape:** endpoint, serviceRef, `tlsSkipVerify`, `auth`, `tenant`.
  - `auth` and `tenant` are flat fields on `MetricsConfig`, beside the legacy `token`, so its 122 literal uses stay valid.
  - Alertmanager and Loki use `EndpointConfig`.
- **Load errors**, following the `serviceRef` precedent:
  - an unknown auth type;
  - basic auth without a username;
  - a username with bearer auth;
  - a legacy `token` beside basic auth;
  - control characters in a tenant (it is a header).
- **Warnings:** `auth` on a proxied endpoint, endpoint beside serviceRef, and userinfo.
- **Userinfo.** An endpoint URL with userinfo reads unavailable ("the endpoint URL carries credentials; set them in Settings > Endpoints"), and its source is shown without them.

### 13.4 Credentials at run time (§3, §7)

- **`internal/endpointcred`** resolves an endpoint's credential into one of ten distinct states:
  - not needed;
  - not sent (on a proxied endpoint);
  - stored;
  - missing;
  - for another origin;
  - unusable record;
  - keychain unavailable;
  - removed;
  - legacy;
  - endpoint unusable.

  The legacy token wins while it is in the file, the stated exception to origin binding. A keychain copy beside it is reported.
- **The `Registry`** holds each endpoint's revision and revocation tombstone. `DefaultConnFactory` now takes the store and the registry, created once in `main` and shared with Settings.
- **Every reader obeys the revision:**
  - The cached metrics resolution records the revision it was built under, and is rebuilt when it moves. Every reader goes through `ensureMetricsLocked`: cluster, workload, node, route, rule, sparklines and Hubble.
  - **Beyond the design,** the direct transport checks a liveness gate before every request and refuses once the revision has moved (`ErrCredentialChanged`). Readers snapshot the transport and query outside the lock, and the gate stops their requests too.
  - A probe that finishes after a change reads "the endpoint's credential changed during the read" and is not cached.
- **Tests** run on an endpoint that is already available and cached:
  - Set changes the next request's header;
  - Remove stops a client a reader already holds;
  - an in-flight probe is dropped;
  - another origin sends nothing;
  - legacy precedence holds;
  - basic auth and the tenant are carried.

### 13.5 Fenced actions and the migration (§6)

- **`config.Snapshot`** is one read of the file: its bytes, mode and decoded configuration. `EndpointRevision` hashes the endpoint's spec (the tenant included), a legacy token's hash and the file's mode.
- **`config.MoveMetricsTokenToAuth`** works from that snapshot:
  - It finds the cluster's `metrics.token` key with `yaml.v3` nodes and rewrites only that line, to `auth: {type: bearer}`, or removes it when `auth` is already there.
  - It edits only a plain one-line value; a flow or block scalar is left to the person to move by hand.
  - It checks the decoded diff, then prepares the edit in a private 0600 temporary file and gives it the snapshot's mode.
  - Just before the rename it compares the live file's bytes and mode with the snapshot.
- **`EndpointsService`** runs every action under one lock, held across the keychain calls and taken before the writer's lock. Each action re-reads the file and refuses on drift.
  - **Set** is refused while a legacy token is in the file: move it first.
  - **Move** tells the live connection (`SetMetricsConfig`) and moves the revision only after the file edit succeeded.
- **Tests** cover:
  - a stale review refusing Set, Remove and Test;
  - a mode change alone counting as drift;
  - an edit after Set reading "for another origin";
  - Remove's revocation surviving a failed keychain delete;
  - the legacy Remove label;
  - Move's keychain failure (nothing changed, the message redacted);
  - an edit to fleet.yaml landing between Move's keychain write and its file edit, through a store whose Set edits the file: the newer file untouched, the connection not told, the retry working;
  - actions serialised.

### 13.6 Settings > Endpoints (§8)

- **Each endpoint** shows its source without credentials, its tenant, its auth type and username, and its state in words.
- **A password field** stores or replaces the credential and clears itself.
- **Move to keychain, Remove and Test** carry each row's revision. Test is offered for metrics only; Alertmanager and Loki say Test waits for S8.
- **Every action button** has an accessible name that includes its endpoint.
- **The Settings note** now names this fourth place Klyx writes fleet.yaml.
- **The native smoke** checks that the panel reads.

### 13.7 Verified, and not

- **On kind-klyx (v1.37):** the API server's service proxy passes `X-Scope-OrgID` to the Service, and not a client's `Authorization` (§4).
- **The native smoke** passes 181/181 in each of night and day at 1440 and 1024 at 36b465d. Its metrics steps read through the guarded proxy client on kind-klyx, and its Settings step reads the panel.
- **Not exercised on a real keychain.** Every test uses the in-memory store. The OS store's code is the AI keys' (`zalando/go-keyring`), already in use.
- **The review server was not used for captures of Set or Move,** because those would write to the owner's login keychain.

### 13.8 Gates

- **Go:** `go test -race ./internal/...` passes; golangci-lint reports 0 issues.
- **Frontend:** vitest 2,266 passed; tsc clean; eslint 0 errors.
- **Native smoke:** 181/181 in all four modes at 36b465d, the labs re-applied first.
  - The first run failed one Velero step in every mode because a lab Backup had aged out of Velero's 24-hour "recent" window. The re-apply script now recreates it; that is lab maintenance, not C5.
- **Mutations:** 18 Go mutations caught (5 in stage B, 7 in D, 6 in E). An equivalent probe mutation was rerun so the import it removed stays in use.
- **Captures of Settings > Endpoints** in night and day at 1440 and 1024 (`pw/c5.mjs`, review config `cfg-c5`), each with its expected text, no horizontal scroll, and no trace of the legacy token on the page. They show:
  - metrics missing a credential;
  - Alertmanager on a serviceRef, with "no credential configured" and Test not yet implemented;
  - Loki with basic auth, missing;
  - a legacy plaintext token, with Move to keychain.
- **From the captures:** every user-facing message now names the section "Settings > External endpoints", its title.

## 14. Revision 1: Codex's implementation review of c8d9a3e

Codex requested changes (handoff b928765a, 2026-10-19) with seven findings, each reproduced by a probe. Every probe is adopted as a test, and each fix is mutation-checked.

| # | Finding | Fix | Tests |
|---|---|---|---|
| 1 (P1) | A credential draft could be submitted with another endpoint's revision after a reload, binding it to the new origin | `EndpointsPanel` keys each draft by `cluster/kind@revision`, and a reload drops every draft whose revision is gone, a remove and re-add included | "drops the draft when the endpoint's revision changes" (Codex's mounted probe) |
| 2 (P1) | JSON decoding restores a credential the pre-parse redaction missed (`c5\u002fSECRET`) | `metrics.Client` redacts after decoding: every error from an instant, scalar, liveness, vector or range read passes through its transport's own redactor, and decoded label names and values lose the exact secret. The pre-parse body redaction stays | `TestDecodedEchoOfTheCredentialDoesNotLeak` |
| 3 (P1) | A URL that does not parse kept its userinfo in Settings and in the capability | `config.EndpointURLProblem` (was `UserinfoProblem`) refuses an unparseable URL with a fixed message; `safehttp`'s origin errors are fixed text; `Resolve` shows such a source as "an endpoint URL that does not parse" | `TestMalformedEndpointURLDoesNotLeakToSettings`, `TestMalformedEndpointURLNeverBecomesAReason`, `TestUnparseableEndpointURL`, `TestOriginErrorsNeverEchoTheURL` |
| 4 (P1) | Remove did not stop a later same-origin redirect hop | A direct transport with a revision gate wraps its client's round tripper, so every send (the first request and each redirect hop) is refused once the revision moves; the same-origin `CheckRedirect` is unchanged. A response that finishes arriving after the move is dropped | `TestRemoveStopsAnAuthenticatedRedirectHop`, `TestResponseCompletingAfterARevocationIsDropped` |
| 5 (P2) | The proxy cap missed non-2xx bodies, which client-go reads whole | `NewGuardedProxyREST` puts `safehttp.Cap` on the client's transport, below client-go's decoding; `ErrResponseTooLarge` survives client-go's wrapping | `TestProxiedNon2xxBodyIsCapped` |
| 6 (P2) | Only the probe was fenced; later query results crossed a credential change | Every reader takes one snapshot (capability, transport and revision) under the lock and, after its queries, drops the result if the revision moved: cluster samples (not cached either), workload, node and container usage, route and rule metrics, Hubble flows, cluster and workload sparklines | `TestSampleReadAcrossARevocationIsDropped`, `TestEveryMetricsReaderDropsAReadAcrossARevisionMove` (each reader, with a control read that must be available) |
| 7 (P2) | Test and Set used the live connection's startup configuration, not the reviewed one | Under the service lock, after the review check, Set (metrics) and Test bring the live connection to the snapshot's configuration when it differs. `ClusterConn.SetMetricsConfig` moves the revision in the same critical section as the configuration and the cache, so a read in flight on the old configuration is dropped and none rebuilds from it in between. Test refuses a kind with no reader before anything else | `TestSettingsTestReadsTheReviewedEndpoint` (now requiring a passing Test that reached only the reviewed endpoint), `TestSetUsesTheReviewedEndpointConfiguration`, `TestAConfigurationChangeDropsAReadInFlight` |

Codex's choices, kept as stated: Set stays refused while the legacy token wins; automatic migration stays limited to a safely editable one-line token; the tombstone holds until an explicit credential installation; and there is no unguarded proxy fallback.

**Where the fixes reach beyond the findings:**
- The reader fence covers every change of the endpoint's revision, not only a credential change: a Settings-driven change of configuration also drops a read in flight.
- The revision moves inside `SetMetricsConfig`, not after it. A first version moved it in the service after the connection's lock was released; a mutation that removed that move survived every test, so the move went where a reader cannot slip between the two.

### 14.1 Gates

- **Go:** `go test -race ./internal/...` passes; golangci-lint reports 0 issues; gofmt is clean on every changed file.
- **Frontend:** vitest 2,267 passed; tsc clean; eslint 0 errors (three warnings, all in files this revision does not touch).
- **Mutations:** 18 Go and 1 frontend, all caught:
  - the round-trip gate, the completion gate, decoded redaction, the proxy cap, the unparseable-URL refusal and the origin error text;
  - Test's and Set's synchronisation, and the in-lock revision move;
  - each reader's fence, and `credentialMoved` itself.
- **Native smoke:** 181/181 in each of night and day at 1440 and 1024, on this revision's tree, the labs re-applied first.
