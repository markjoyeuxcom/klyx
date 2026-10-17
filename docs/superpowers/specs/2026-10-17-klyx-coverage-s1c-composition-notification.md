# Coverage S1c: source composition and notification facts

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), slice S1c. S1a (the Flux Operator, accepted at 421fb2e) and S1b (image automation, accepted at 6f8824f) come before it. This slice reuses S1a's projection budget and resume evidence, and S1b's message allowlist.

The roadmap's S1c:
- **Source composition:** `ExternalArtifact` and the source-watcher's `ArtifactGenerator` composing and decomposing sources, drawn as source → generated artifact → reconciler dependency, in the dependency graph and the bound-source panels. The Line test that marks ExternalArtifact as an unknown applied source changes.
- **Notification facts** for Providers, Alerts and Receivers.

**One finding reaches beyond this slice.** Klyx exposes two bearer credentials today: a Receiver's `status.webhookPath`, and the webhook URLs that Alert dispatch failures echo. It shows them in the generic browser, Describe, the events lens and chat, and can send them to AI. §6 closes both through the S0b registry and the message rule. This part of the slice protects data Klyx already reads, and it lands first.

## 1. The API

Read from source-controller v1.9.6, source-watcher v2.2.4, notification-controller v1.9.4 and kustomize-controller v1.9.6, at their tags, the versions Flux v2.9.6 installs. They are cloned read-only in `scratchpad/fluxsrc-s1c`.

**ExternalArtifact** (`source.toolkit.fluxcd.io/v1`, the only version; short name `ea`):
- **Spec:** an optional `sourceRef` (apiVersion, kind, name, namespace), naming its producer.
- **Status:**
  - `artifact`: `revision`, `digest`, `url`, `path` and `lastUpdateTime`, with optional `size` and `metadata`;
  - conditions.

  There is no `spec.suspend`, no `observedGeneration` and no `lastHandledReconcileAt`.
- **Who writes it:** source-controller never does. Its producers are other controllers.
  - The source-watcher writes only Ready=True, reason `Succeeded`, "Artifact is ready", and never Ready=False (`artifactgenerator_controller.go` 499-511).
  - **Its Ready therefore says nothing about its producer's health.** When a generator's source fails, the ExternalArtifacts keep their last artifact and stay Ready=True; only the generator turns Ready=False (142-152).
- **Consumers:**
  - a Kustomization by `spec.sourceRef.kind: ExternalArtifact`;
  - a HelmRelease by `spec.chartRef.kind: ExternalArtifact`.
  - kustomize-controller checks only that `status.artifact` exists, never its Ready (`kustomization_controller.go` 241-246), and needs its `ExternalArtifact` feature gate. The gate defaults to false (`features.go` 100-102); `flux install` turns it on. Without the gate, the Kustomization fails `AccessDenied` "...feature gate is disabled" (703-708).

**ArtifactGenerator** (`source.extensions.fluxcd.io/v1beta1`, the only version; short name `ag`):
- **Spec:**
  - `sources[]`: alias, name, namespace and kind, one of Bucket, GitRepository, OCIRepository, HelmChart or ExternalArtifact, so generators chain;
  - `artifacts[]`: name, `revision` (`@alias`), `originRevision`, and `copy[]` (from, to, exclude, strategy);
  - `pathPattern` and `commonMetadata`.
- **Status:**
  - `lastHandledReconcileAt` and conditions;
  - `inventory[]` of the generated ExternalArtifacts (name, namespace, digest, filename);
  - `observedSourcesDigest`, one digest over every source. **No per-source revision is recorded,** and there is no `status.observedGeneration`.
- **Suspension** is the annotation `source.extensions.fluxcd.io/reconcile: disabled`, case-insensitive. While disabled, conditions are left untouched.
  - **Initialization:** a generator created disabled gets Ready=True, reason `ReconciliationDisabled`, "Initialized with reconciliation suspended", the same initialization as S1a's.
  - **Requests:** `lastHandledReconcileAt` is written in a deferred summary even while disabled, so it proves nothing (S1a §4).
- **Conditions:**
  - Ready=Unknown `Progressing` with Reconciling, while it builds;
  - Ready=False `SourceFetchFailed`, `BuildFailed` or `ReconciliationFailed`;
  - Stalled with `ValidationFailed`, `AccessDenied` or `BuildFailed` on terminal errors;
  - Ready=True `Succeeded`, "reconciliation succeeded, generated N artifact(s)".

  **A no-drift run writes no condition** (160-166).
- **Ownership:**
  - no ownerReferences;
  - the generated ExternalArtifacts carry `app.kubernetes.io/managed-by: source-watcher` and `source.extensions.fluxcd.io/generator: <generator UID>`, and their `spec.sourceRef` names the generator;
  - orphans are garbage-collected.
- **Revisions:** an ExternalArtifact's `artifact.revision` is `latest@<digest>`, or a source's revision under `revision: @alias`. Its `metadata["org.opencontainers.image.revision"]` carries the origin revision.

**Notification kinds** (notification-controller v1.9.4):
- **Versions:** Alert and Provider are served only as `v1beta3`, Receiver only as `v1`. `v1beta2` was dropped in Flux v2.9.
- **Alert and Provider are static.** `v1beta3` has no status at all (`provider_types.go` 178-183, `alert_types.go` 88-93). Their controllers only strip a legacy finalizer and emit a `Migration` event.
  - **Their only health evidence is events on the Alert:** `NotificationDispatchFailed`, `InvalidConfig`, `SourceFetchFailed` and `MetadataAppendFailed` (`event_handlers.go`).
  - A suspended Provider skips dispatch silently.
- **Provider spec:** `type` (32 values), `address`, `channel`, `username`, `proxy` (deprecated), `proxySecretRef`, `secretRef`, `certSecretRef`, `serviceAccountName`, `timeout`, `commitStatusExpr`, `suspend`.
- **Alert spec:** `providerRef`, `eventSeverity`, `eventSources[]` (kinds include ArtifactGenerator and ExternalArtifact), `inclusionList`, `exclusionList`, `eventMetadata`, `suspend`.
- **Receiver v1:**
  - **Spec:** `type` (including `generic-oidc`), `interval`, `events`, `resources[]` (each with a CEL `filter`), `resourceFilter`, `secretRef`, `oidcProviders[]`, `suspend`.
  - **Status:** conditions, `observedGeneration` (default -1), `lastHandledReconcileAt` and `webhookPath` (`/hook/` + sha256 of token, name and namespace).
  - **Conditions:** Ready=True `Succeeded`, "Receiver initialized for path: /hook/…"; Ready=False `TokenNotFound`, which clears the path; Stalled with `ValidationFailed` or `InvalidCELExpression`.
  - `observedGeneration` advances only on Ready or a terminal error, so S1b's Stage 0 rule applies.
  - Webhook deliveries leave no trace on the Receiver, so **its Ready means "token found, spec valid", never "webhooks arriving".**

## 2. What Klyx shows

**Composition, in the Flux lens:**
- **ArtifactGenerators** join the Sources view as their own kind, beside the five source kinds. Their verdict is their own (§4). The inspector shows:
  - each declared source, linked to its row with its verdict;
  - each generated artifact from `inventory`, linked to its ExternalArtifact;
  - the copy rules, as text;
  - suspension, with S1a's resume states.
- **ExternalArtifacts** join the Sources view.
  - **The verdict comes from the artifact and the producer, never from Ready alone:**
    - Ready False, from a third-party producer that writes it, is a fault;
    - an artifact present is healthy;
    - no artifact is a fault: "no artifact yet: its consumers cannot apply".
  - **Its producer is a separate block** after its own verdict (S1b's rule: the selected object's own verdict first). A failing generator reads "the producer is failing; this artifact is its last good one (<age>)".
  - **Who the producer is (revision 1).** There are two pieces of evidence: the label `source.extensions.fluxcd.io/generator=<UID>` (an identity), and `spec.sourceRef` (a name, of any kind).
    - **`sourceRef`'s kind is read as declared.** Only `source.extensions.fluxcd.io` `ArtifactGenerator` is resolved as a generator. Any other kind is shown as declared ("produced by <kind> <ns>/<name>; Klyx does not read that kind's health"), never matched to a same-named generator. Its namespace defaults to the artifact's.
    - **Both present:** the generator found by name must carry the labelled UID.
      - **A mismatch** reads "the producer reference and the recorded identity disagree: <ns>/<name> is not the generator this artifact records", with no health attributed (revision 2). A mismatch proves only that the named object is a different one: the recorded producer may still be live under its UID while `sourceRef` points elsewhere.
      - **The labelled UID is then looked up** as below. A live generator found under it is named ("the recorded producer is <ns>/<name>"), still with no health attributed. Only a complete read that finds nothing adds "the recorded producer no longer exists".
    - **The label alone:** a UID lookup among the watched generators (the part's rows are keyed by UID). "Producer gone" is said only after a complete read of the generators finds nothing; a capped or unreadable read is as below.
    - **A generator `sourceRef` alone:** resolved by name, labelled "named by the artifact; its identity is not recorded".
    - **Capped or unreadable generator evidence** reads "incomplete" or "not readable", never a health.
  - The inspector shows the revision, the digest, the origin revision from metadata (labelled "origin revision, as the producer recorded it"), the artifact's age, and the consumers.
- **The dependency graph** (`internal/gitops/depgraph`) gains three edges:
  - Kustomization → ExternalArtifact, by `sourceRef`;
  - HelmRelease → ExternalArtifact, by `chartRef`;
  - ExternalArtifact → ArtifactGenerator, under the identity rules above. A disagreeing, ambiguous or unverified producer is drawn as such, never as a verified edge. Then ArtifactGenerator → each of its sources.

  A generator's chain to another ExternalArtifact is followed with the graph's existing cycle and depth bounds.
- **The bound-source panel** of a Kustomization or HelmRelease bound to an ExternalArtifact shows the artifact, then its producer's verdict as its own line.
  - A Kustomization failing `AccessDenied` with the feature-gate message names the cause: "kustomize-controller runs without its ExternalArtifact feature gate".
- **The Line (revision 1, scope stated):**
  - **Applied history only.** A Kustomization bound to an ExternalArtifact records its last applied revision as an artifact value (`AppliedArtifact`, the full revision string) in the applied-history records (`appbridge/applied_history.go`), as OCI and Bucket revisions are. `TestAppliedValue`'s "unsupported source" case becomes this case.
  - **The fleet board is unchanged:** its units already exclude artifact sources (OCI, Bucket) with their own source-identity contract, and ExternalArtifact joins that exclusion with the same "artifact source" reason. A board comparison for artifact sources is a separate scope.
  - The origin revision is shown as a hint, never compared.

**Notification facts, in the Flux lens:**
- **A Notifications view,** beside Sources and Operator, listing Providers, Alerts and Receivers.
- **Providers and Alerts are facts, not verdicts.** They have no status to judge.
  - **A Provider:**
    - its type and channel;
    - whether an address is set (the address itself is withheld, §6);
    - the Secrets it names, never read;
    - suspension;
    - the Alerts that use it.
  - **An Alert:**
    - its provider, with whether that Provider exists and is suspended;
    - severity, event sources, and the inclusion and exclusion lists;
    - suspension.
  - **Dispatch evidence:** the Alert's recent `NotificationDispatchFailed`, `InvalidConfig`, `SourceFetchFailed` and `MetadataAppendFailed` events. They show as a count and reasons, labelled "from the controller's events (they expire after about an hour)", never as a health verdict.
- **Receivers have a verdict** (§4), and their own part: Ready means the token was found and the spec is valid. The inspector says so, and that Klyx cannot see whether webhooks arrive.

## 3. The source contract (roadmap C1)

| Part | Acquisition | Projection | Counted entity | Health | Notifications |
|---|---|---|---|---|---|
| `sources` (existing, task `flux`), gaining ArtifactGenerators and ExternalArtifacts | observer watches `artifactgenerators` (v1beta1) and `externalartifacts` (v1) | field by field, as S1a §3 | each object's UID | §4 | eligible, as sources |
| `receivers` (new, task `flux`) | observer watch `receivers` (v1) | field by field, without `webhookPath` | the Receiver's UID | §4 | eligible |
| Providers and Alerts | read on demand when the Notifications view or an inspector opens: lists capped at 500 each, with "+N more" said | field by field, without `address` or `proxy` | none: facts | none | none |

**Projected fields:**
- **ArtifactGenerator:**
  - classification facts: the reconcile annotation's state, Ready's status, reason and fingerprint (S1a §4), Reconciling and Stalled, and generation with Ready's own `observedGeneration`;
  - **join inputs, kept whole:** `sources[]` (kind, name, namespace, alias) and `inventory[]` (name, namespace);
  - for display: artifact names and copy rules, clipped.
- **ExternalArtifact:**
  - Ready's status and reason;
  - **join inputs, kept whole:** `artifact.revision` and `artifact.digest`, `spec.sourceRef`, and the generator label;
  - for display: `lastUpdateTime`, `size`, and the origin revision from metadata. No other metadata key is projected, because producer metadata is free text.
- **Receiver:**
  - classification facts as for Flux objects, and `spec.suspend`;
  - `type`, `events`, the `resources` kinds and names (CEL filters as counts), `secretRef`'s name, and the OIDC provider count.
  - **Never** `webhookPath`, and never the Ready message (§6).

**Versions:** each kind at its one served version. A cluster serving none of a kind reads it as absent; one serving another version reads "unsupported version", never empty.

**Controllers:** the source-watcher and notification-controller Deployments are found by their `app.kubernetes.io/component` label in the Flux namespace that S1a's detection resolves, and feed the Flux task as S1b's image controllers do.

**The budget,** by S1a §3's mechanism:
- three watches;
- 4 KiB per object, checked;
- these three kinds together hold at most **1 MiB per cluster** (generators 512 KiB, artifacts 384 KiB, receivers 128 KiB), admitted by namespace and name, with "capped" coverage past it;
- ten clusters reserve **10 MiB**.
- The on-demand Provider and Alert lists retain nothing after the view closes.

**Dependency completeness:** the joins (generator ↔ artifacts ↔ consumers, Alert → Provider) read the shared rows or the on-demand lists. Missing reads "not found", unreadable "not readable", and past the cap "incomplete". A verdict never depends on another object, except an ExternalArtifact's producer block, which is shown and never folded into the artifact's own verdict.

## 4. Health rules

- **ArtifactGenerator:**
  - suspension by its own annotation is Suspended;
  - **resume evidence is S1a §4's, unchanged:**
    - the Ready fingerprint against the last observed disabled state;
    - `ReconciliationDisabled` is never evidence or a verdict;
    - no timestamps or tokens are compared;
  - otherwise the shared Flux rule with S1b's Stage 0, keyed on Ready's own `observedGeneration`, since there is no top-level one;
  - **what stays unproven:** a no-drift run writes no condition, so a resumed generator whose sources and spec did not change leaves no evidence. It reads "Resumed · awaiting evaluation: nothing has changed since suspension, and the generator records nothing when nothing drifts". It is not counted, and "Reconcile" is offered.
- **ExternalArtifact:**
  - Ready False is a fault with its reason;
  - no `status.artifact` is a fault, "no artifact yet";
  - an artifact present is ok;
  - there is no suspension and no generation lag (no `observedGeneration`).
- **Receiver:**
  - `spec.suspend` is Suspended;
  - otherwise the shared Flux rule with Stage 0:
    - `TokenNotFound` is a fault, with Klyx's own fixed wording: "token unavailable: its Secret could not be read or has no token; webhooks are refused". The reason covers a missing key and a failed Secret read alike (`receiver_controller.go` 381-390), so the cause is never claimed beyond that;
    - Stalled is a fault that will not retry;
  - **Ready True is worded by type (revision 1):**
    - for a Receiver with a `secretRef`: "Ready: configuration accepted, token read";
    - for `generic-oidc` without one: "Ready: configuration accepted (OIDC, no token Secret)". `generic-oidc` needs no Secret (359-365).
- **Providers and Alerts:** no verdict, no count, no notification. A suspended Provider or Alert is shown as such.

## 5. Verbs (roadmap C4)

| Kind | Reconcile | Suspend / resume |
|---|---|---|
| ArtifactGenerator | `reconcile.fluxcd.io/requestedAt` | `source.extensions.fluxcd.io/reconcile: disabled`; resume sets `enabled` and a fresh `requestedAt`, as S1a's |
| ExternalArtifact | none: it is reconciled by its producer, and the inspector links to the producer's verbs | none |
| Receiver | `requestedAt` | `spec.suspend` |
| Provider, Alert | none (static) | `spec.suspend` |

- Every verb carries the UID guard, ConfirmDialog with ProtectGuard, and S1b's ownership consequence line.
- A verb reports "requested", never "completed".

## 6. Sensitive data

**The S0b registry gains:**
- `Receiver` `status.webhookPath`: opaque. The path is the bearer credential: a `generic` Receiver authenticates nothing but the path (`receiver_handlers.go` 262-263).
- `Provider` `spec.address` and `spec.proxy`: opaque. For Slack, Teams, Discord, Rocket, Webex, Lark and generic forwarders the address is the credential, its secret in the path. `spec.proxy` can carry `user:password@`.
- `ExternalArtifact` `status.artifact.metadata`: free-form, with only `org.opencontainers.image.revision` shown. Producer metadata is arbitrary text.
- `ExternalArtifact` `status.artifact.url`: opaque, shown only as its scheme and host (revision 1). A producer may publish a signed URL with a bearer token in its path or query; source-watcher's own is in-cluster.

S0b's machinery then applies to them on every path: the generic detail, Describe (withheld while a value is present), the editor guard, diffs, chat and AI.

**Messages fail closed, as S1b §6's rule does:**
- **Receiver condition and event messages.** The Ready message names the webhook path. The allowlist is `Receiver initialized for path: <withheld>` (the path replaced). `TokenNotFound` shows Klyx's fixed wording (§4), never the upstream text, which is not one fixed string. The Stalled reasons are kept. Anything else is withheld, keeping the reason.
- **Alert and Provider event messages.** Notifier errors print the webhook URL, unmasked (`slack.go` 72, `teams.go` 127, `forwarder.go` 49 and others). The asynchronous path masks only the Secret's `token`. retryablehttp's "giving up" message redacts only a user-info password, so path and query tokens survive. Non-2xx response bodies are echoed verbatim.
  - These messages are **withheld whole on every path,** keeping the reason (`NotificationDispatchFailed`, and so on), the involved object and the time.
  - `InvalidConfig` messages name regular expressions and label selectors, which are configuration. They pass the URL rule.
- **Fetch errors reduce every URL to its origin (revision 1).** An ExternalArtifact's URL is copied into its consumers' fetches, and the fetcher prints the full URL in its errors (`http/fetch/archive_fetcher.go` 190-194).
  - **Where the error lands:**
    - source-watcher writes it into an ArtifactGenerator's `SourceFetchFailed` condition and events;
    - kustomize-controller writes it into a consuming Kustomization's `ArtifactFailed` condition and events (`kustomization_controller.go` 371-373).
  - **A new urlscrub mode,** `Origins` (revision 3). Nothing in a message proves where a URL ends:
    - the URL is a producer's string;
    - `http.NewRequest` accepts literal spaces, and the fetcher's non-200 error echoes them with `%s`;
    - Go's request errors quote control characters as `\n`;
    - kubectl prints an embedded line break raw, with its following spaces.

    So `Origins` never looks for the end:
    - **The text before the first URL candidate** (a `<scheme>://`, case-insensitive) is kept.
    - **The candidate shows as `<scheme>://<host>`** only when its authority is well formed. The authority runs to the first `/`, `?` or `#`, or to the message's end. A user info part, up to its last `@` or `%40`, is dropped. What remains must be a host name, an IPv4 address or a bracketed IPv6 address, with an optional all-digit port. Anything else shows as `<scheme>://<withheld>`.
    - **Everything after the authority, to the end of the message, is withheld:** "<the rest of the message is withheld: it follows a URL>". That includes the path, the query, later lines and any later URL.

    It works on a whole message, never on a line.
  - **It applies to every condition and event message of every ArtifactGenerator, ExternalArtifact, Kustomization and HelmRelease, whatever its current reference** (revision 2).
    - **Why not by reference:** a message a past ExternalArtifact reference left behind (an older generation's `ArtifactFailed` condition, or an event) records nothing about the reference it came from. A Kustomization first seen after it moved to a GitRepository still carries the signed URL. So the rule is read from the kind alone: a condition's own object, and an event's `involvedObject.kind`.
    - **The cost:** a message from these kinds loses everything after its first URL's host. That includes the artifact paths of fetch errors from GitRepository, OCIRepository, Bucket and Helm sources, and any status code after the URL. The source reference is shown as its own fact, the reason (`ArtifactFailed`, and so on) is kept, and the source's own inspector carries its fetch error under the URL rule.

    It runs on every path that carries such text: detail, events lens, chat, AI and health reasons.
  - **The resume fingerprint** (S1a §4) is computed from the raw condition before this reduction, as it is before display clipping.
  - Build errors, paths from Git content and pathPattern captures keep their text under this rule.

**Describe (revision 2).** kubectl describe prints the object's conditions as they are and appends its events. S0b's version bracket fences the object's payload fields, not its messages, and not its events at all. A payload-free Alert can still print a webhook token from a `NotificationDispatchFailed` event. A payload-free ImageUpdateAutomation whose template was removed after a failed push can still print the old protected value from a retained condition.

**The principle:** the bracket never authorizes raw text that the kind's structured rule would withhold. kubectl's text cannot be rewritten safely, because a message's own lines are indistinguishable from the report's fields (revision 3). So a message-sensitive kind's report is released only when the snapshot's text passes the kind's rule unchanged, and is otherwise withheld. The policy is by kind, and holds whatever the object's current reference.

**Every message-sensitive kind is described with `--show-events=false`.** Its events are read through Klyx's own events surface, under the message rules above. The sheet says "events are shown in the inspector, under Klyx's message rules".
- **The `Origins` kinds** (ArtifactGenerator, ExternalArtifact, Kustomization, HelmRelease): the report is released only when every string in the bracket's snapshot passes `Origins` unchanged, that is, when no URL in it has anything after a well-formed authority. Otherwise it is withheld: "Describe withheld: this object holds a URL whose path Klyx does not show; the inspector shows it under Klyx's rules".
- **The allowlist kinds** (ImageUpdateAutomation, Receiver, Alert, Provider):
  - **The report is released only when every condition message in the bracket's snapshot passes its kind's rule unchanged:** exactly one of the controller's fixed messages, or, for ImageUpdateAutomation, exactly a push summary's first line with nothing after it.
  - **Otherwise the report is withheld:** "Describe withheld: a condition message is outside Klyx's message rules; the inspector shows it under them". The kubeconfig-pinned copy command stays offered.
  - **Kind by kind:** Alert and Provider have no allowlisted message, so any condition message left over from an older API version withholds the report. A Receiver's Ready message names its path, which a rewrite would replace, so a Ready Receiver's report is always withheld (its `webhookPath` would withhold it anyway).
- **A payload still present withholds the report,** through S0b's bracket, in both families.
- **Classification is read inside the bracket's snapshot:** the kind, and for an allowlist kind its condition messages. A report whose object changed between the bracket's two reads is withheld, as S0b's is.

**AI facts:** kinds, verdicts, reasons, names, revisions and digests, types and channels. Never addresses, webhook paths, artifact URL paths, producer metadata, or Alert or Provider event messages.

**The fix for existing exposure lands first,** as Stage 0 of this slice with its own needle tests across the generic detail, Describe, the events lens, chat and AI:
- the three registry entries;
- the Receiver and Alert/Provider message rules;
- the `Origins` rule for fetch errors, and the Describe policy above.

## 7. Tests, lab and smoke

- **Stage 0 (§6):**
  - needles in a Receiver's `webhookPath` and Ready message, in a Provider's `address` (a Slack-style path token) and `proxy` (user info), and in an Alert's `NotificationDispatchFailed` message (a URL with a path token, and a response body);
  - each absent from the serialised generic detail, Describe, the events lens, the chat tools and AI evidence;
  - the allowlisted Receiver message keeps its fixed text with the path replaced;
  - signed-path and signed-query needles in an ExternalArtifact's URL, absent from the generator's `SourceFetchFailed` message, a consuming Kustomization's `ArtifactFailed` message, their events, Describe, chat and AI;
  - Describe of an otherwise empty Alert whose only credential is in a `NotificationDispatchFailed` event, and of a payload-free object with an older sensitive event: no token in either report;
  - **residual messages (revision 2):**
    - a Kustomization first seen referencing a GitRepository, with an older generation's `ArtifactFailed` condition and an older event, both quoting a signed ExternalArtifact URL: no token in the detail, the events lens, chat, AI or Describe;
    - a payload-free ImageUpdateAutomation with no events, whose retained Ready condition quotes a push option: Describe withheld, and the inspector shows the reason with the withheld wording;
    - an ImageUpdateAutomation whose conditions are all fixed messages: Describe released, without events;
    - a push summary with text after its first line: Describe withheld, and the inspector shows the parsed first line;
    - an ArtifactGenerator whose `SourceFetchFailed` condition holds a signed URL: Describe withheld; the inspector shows the scheme and host;
  - **`Origins` (revision 3),** each needle absent from every path:
    - a needle on a line after the URL, unindented and indented;
    - several continuation lines;
    - a quoted URL whose escaped `\n` precedes the needle;
    - a URL with a literal space before the needle, as a non-200 fetch error prints it;
    - user info with a space or `/` in its password;
    - an invalid port;
    - a second URL after the first;
    - and the kept cases: the text before the URL, and a well-formed origin;
  - Receivers: `generic-oidc` without a Secret (Ready wording), a forbidden Secret read and a missing token key (both "token unavailable").
- **Unit:**
  - **parsing and rules** for each kind;
  - **resume:**
    - a generator's resume through the projected Observer path: evidence by a Ready change, `ReconciliationDisabled` never evidence, and the no-drift case staying "awaiting";
    - an ExternalArtifact with an artifact and Ready True from a failing generator: ok, with the producer block failing;
    - no artifact: a fault;
  - **the dependency graph:**
    - the three new edges;
    - a chained generator (ExternalArtifact → generator → ExternalArtifact);
    - a cycle bounded;
    - **producer identity:**
      - a generator label without `sourceRef`;
      - both fields agreeing;
      - a generator deleted and recreated under the same name: "disagree", and "the recorded producer no longer exists" only after a complete read finds no object with the label's UID;
      - a mismatch whose recorded producer is still live under another name: "disagree", naming it, never "gone";
      - a third-party `sourceRef` kind (never a generator);
      - a `sourceRef` without a namespace;
      - capped generator evidence (incomplete);
  - **The Line:**
    - a Kustomization bound to an ExternalArtifact giving an `AppliedArtifact` in the applied history;
    - the board's unit excluded as an artifact source;
    - the origin revision never compared;
  - **the bound-source panel,** with the feature-gate `AccessDenied` named;
  - **the Notifications view:**
    - an Alert whose Provider is missing or suspended;
    - dispatch-failure evidence counted from events, with no message shown;
    - capped lists;
  - **C1 beyond parity:**
    - projection equality;
    - UID replacement;
    - the budget;
    - an unsupported version.
- **Parity:**
  - the ArtifactGenerator, ExternalArtifact and Receiver CRDs on `kind-klyx` and in `klyx-parity.sh`;
  - oracle-marked fixtures for each verdict;
  - a source-watcher and a notification-controller stand-in;
  - the oracle's `sources` and `receivers` parts and the frontend `ROW_PARTS`.
- **Lab (`kind-klyx`):**
  - **ArtifactGenerators:** one Ready generator with two artifacts; one failing `SourceFetchFailed` whose artifact stays Ready; one suspended;
  - **ExternalArtifacts:** one consumed by a Kustomization;
  - **Receivers:** one Ready, one `TokenNotFound`;
  - **Providers and Alerts:** a Provider with an address, an Alert with dispatch-failure events written as Kubernetes events;
  - all status written through the status subresource.
- **Native smoke:**
  - the Sources view lists generators and artifacts, with the producer block;
  - the dependency drawing draws a Kustomization → ExternalArtifact → generator → GitRepository chain;
  - the Notifications view lists the three kinds, with no address or webhook path anywhere on the page.
- **Live captures:** the Sources view with a generator, the drawing with the composition chain, and the Notifications view, night and day, at 1440 and 1024.

## 8. Not in S1c

- **Whether webhooks arrive at a Receiver.** Nothing records it.
- **helm-controller's handling of ExternalArtifact chart references,** beyond showing the reference. Its source was not read for this note, and the HelmRelease rule is unchanged.
- **Evaluating Alert event-source selectors** against live objects.

## 9. Revision 1: Codex's design review of f630ac7

| # | Finding | Where answered |
|---|---|---|
| 1 | An ExternalArtifact's URL can be signed, and fetch errors print it in full into generator and consumer messages | §6: the URL is registered (scheme and host only); a new `Origins` rule reduces every URL in generator and ExternalArtifact-consumer messages, on every path; the fingerprint is computed from raw; §7 adds signed-path and signed-query needles, including a consuming Kustomization |
| 2 | kubectl describe prints events, which the version bracket does not fence | §6: message-sensitive kinds are described with `--show-events=false`, by kind and whatever the object holds; consumers' reports pass `Origins`; classification read inside the bracket; §7 tests the event-only Alert and an older sensitive event |
| 3 | The producer join lacked identity and type rules | §2: `sourceRef`'s kind read as declared; label UID and name must agree; replaced, gone, unverified and incomplete results; the graph draws the same; §7 names each case |
| 4 | Receiver Ready and `TokenNotFound` were overstated | §4: type-qualified Ready wording; "token unavailable" in Klyx's fixed words; §6 drops the upstream text; §7 tests `generic-oidc`, a forbidden read and a missing key |
| P3 | The Line's scope | §2: the applied history only; the board keeps excluding artifact sources, ExternalArtifact among them |

## 10. Revision 2: Codex's re-review of 87bb8d6

| # | Finding | Where answered |
|---|---|---|
| 1 | The current reference kind cannot classify messages a past ExternalArtifact reference left behind | §6: `Origins` applies to every ArtifactGenerator, ExternalArtifact, Kustomization and HelmRelease message, read from the kind alone, with its cost stated and a line rule for messages with line breaks; Describe no longer classifies by reference; §7 tests a Kustomization first seen on a GitRepository with an older `ArtifactFailed` condition and an older event, on every path |
| 2 | `--show-events=false` leaves the report's conditions raw | §6: the bracket never authorizes text the structured rule would withhold. The `Origins` kinds' whole report passes `Origins`. An allowlist kind's report is released only when every condition message in the snapshot passes its rule unchanged, and is otherwise withheld. §7 tests a payload-free automation with an unsafe retained condition and no events, and a generator's report |
| Wording | A UID mismatch proves the named object is different, not that the producer is gone | §2: "the producer reference and the recorded identity disagree"; a live recorded producer is named; "no longer exists" only after a complete read; §7 adds the live-elsewhere case |

## 11. Revision 3: Codex's re-review of 1cb75ac

| # | Finding | Where answered |
|---|---|---|
| 1 | Indentation does not prove a message or URL has ended; spaces and escaped line breaks continue a URL too | §6: `Origins` never looks for a URL's end. It keeps the text before the first URL, the scheme, and the host only when the authority is well formed. It withholds everything after the authority to the end of the message, and works on whole messages only. Describe no longer rewrites kubectl's text: every message-sensitive kind runs without events, and an `Origins` kind's report is released only when its snapshot passes `Origins` unchanged. §7 adds indented, multi-line, escaped, literal-space, user-info, port and second-URL needles |

## 12. As built: Stage 0

Stage 0 closes the exposure Klyx already had before any S1c view exists.

**`urlscrub.Origins`** is the whole-message rule of §6 (revision 3).
- It recognises its own output: the sanitiser must be idempotent, and the rule can meet text it has already rewritten.
- Tested against every needle in §7: continuations, an escaped `\n`, a literal space, user info with a space or `/`, an invalid port and a second URL.

**The registry gains:**
- a Receiver's `status.webhookPath` (opaque);
- a Provider's `spec.address` and `spec.proxy` (opaque);
- an ExternalArtifact's `status.artifact.metadata` (free-form) and `status.artifact.url`: a new shape, `shapeOrigin`, which shows the scheme and host and counts as a payload.

**`crd.MessageRule(kind, reason, msg)`** gains a reason, because an Alert's `InvalidConfig` message keeps its text under the URL rule:
- **Receiver:** the Ready message with its path replaced; anything else withheld whole.
- **Alert and Provider:** withheld whole, except `InvalidConfig`.
- **ArtifactGenerator, ExternalArtifact, Kustomization and HelmRelease:** `Origins`, by kind alone.

**Where the rule runs:** wherever S1b's does (the sanitiser's conditions, every event row), and also:
- a Kustomization's and HelmRelease's parsed message, which every Flux row, Line unit, promotion row and health reason reads;
- the Flux detail's conditions (`flux.ParseDetail`);
- the dependency graph's node messages.

**Describe:**
- every message-sensitive kind is bracketed and runs without events;
- an `Origins` kind is held when any string of the snapshot would change under `Origins` (`originsHold`), with its own wording: "it holds a URL whose path Klyx does not show".

**Found while building, in S1b too.** The generic browser's list and Summary cells for a kind not in `payloadResources` are the server's own. For ImageUpdateAutomation, Receiver and ArtifactGenerator, the "Status" column is the raw Ready message.
- ImageUpdateAutomation is fixed as an S1b commit, `dc8b666`.
- The notification kinds, ExternalArtifact and ArtifactGenerator are listed here.
- Two tests keep every payload kind's resource, and every message-rule kind's resource, in the list.


## 13. Stage 0 review 1: Codex at 1c6271e

Codex asked for changes on three findings. Each is fixed as below, with the design extended where the finding needed it.

**1. A generic Event object bypassed its subject's rule.** Stage 0 ruled event rows and condition messages. An Event read as an object (the generic browser's list, Summary, YAML and fields, Describe, chat) still carried its text raw. This applied to core/v1 `message` and events.k8s.io/v1 `note`.
- The sanitiser now applies to an Event's own text the message rule of every object the Event names: `involvedObject`, `regarding` and `related`. It covers both field names in either API, because either API serves every Event.
- A subject kind that cannot be read withholds the text.
- The text is a writable top-level field, not status. A change therefore blocks a round-trip edit as well as Describe (`markEventMessage`), and it never counts as a payload.
- The editor refuses such an Event in its own words. A submitted document with a placeholder was already refused.
- `events` (core) and `events.k8s.io/events` join `payloadResources`. Their list and Summary cells are local. The columns are Klyx's own (`BuiltinPayloadColumns`: type, reason, subject kind and name, and the text), since a built-in kind has no printer-column definition.
- Describe brackets every Event, in either API, without events (`MessageBracketed`). A held Event is withheld in its own words.

**2. Finding no `scheme://` did not prove a fetch error safe.** The ExternalArtifact CRD declares its URL a plain string. `http.NewRequest` accepts `https:/host/TOKEN`, `/TOKEN`, or a URL with no URL syntax at all, and Go's HTTP stack prints whatever target it was given. The design is extended in two layers.
- **`Origins` gains candidates.** Each is withheld from its start to the end of the message:
  - a request echo, as Go's HTTP stack prints a target: url.Error's `<Op> "<target>"` (an HTTP method in title case, or `parse`) and retryablehttp's `<METHOD> <target> giving up`;
  - `http:` or `https:` without `//`.

  An echo whose target is itself a `scheme://` candidate keeps its origin (`GET https://host <withheld…>`). These are library shapes, not one controller's wording, so they hold for any Go fetcher.
- **Under a fetch reason, only the pinned forms pass.** The reasons are `ArtifactFailed` for Kustomization and HelmRelease, and `SourceFetchFailed` for ArtifactGenerator. A message passes only in the forms that kustomize-controller v1.9.6, helm-controller v1.6.5, source-watcher v2.2.4 and fluxcd/pkg's archive fetcher write:
  - with a URL candidate, the text before it must be one of their fetch-error prefixes;
  - without one, the message must start with one of their wordings that never carry the URL.

  Anything else, multi-line text included, is withheld whole as `FetchWithheld`, and the reason is kept. An unrecognised wording, or a future controller version, therefore fails closed.
- **Describe** follows both layers. `originsHold` sees the new candidates, and a condition message the fetch rule changes holds the version.
- **Known losses, deliberate:**
  - a HelmRelease's chart-content error under `ArtifactFailed` (Helm's loader wordings are many, and none is pinned) is withheld;
  - a source lookup error that echoes an API server URL after `unable to get source '…'` is withheld whole;
  - `parse "` and an upper-case method followed by a space can over-withhold in other messages of the Origins kinds.

**3. `InvalidConfig` was not proven safe.** notification-controller v1.9.4 quotes the expression that failed to compile (event_handlers.go:212, :232), and that expression can be a webhook URL. The exception is removed. Alert and Provider messages are withheld whole under every reason, as §6 already said; the reason is kept.

Tests: `urlscrub.TestOriginsMalformedURLs`; `crd.TestFetchFailures`, with Codex's two `http.NewRequest` shapes; `crd.TestEventObjects`; `fleet.TestEventDetailUsesSubjectRule` and `fleet.TestApplyRefusesASanitisedEvent`, for both APIs; and `appbridge.TestDescribe_EventObjects`.

## 14. Stage 0 review 2: Codex at 64dba4e

Codex accepted the fetch and `InvalidConfig` changes and asked for two more on Events.

**1. The typed event readers kept the old rule.** `workloads.SummarizeEvents` (the events lens, the Flux drift surface, the Warning-events snapshot, chat and AI), the inspector's `instanceEvents` and the Karpenter lens applied only `involvedObject`'s rule. So an Event about a Pod with a related Provider kept its text in those rows, while the generic sanitiser withheld it.
- There is now one classifier, `crd.EventMessage(reason, msg, subjects)`, and every adapter passes the references that are present.
- The typed adapter, `workloads.EventMessage`, passes `involvedObject`, and `related` when set. The generic sanitiser passes whichever of `involvedObject`, `regarding` and `related` the object carries.
- The events watch only signals that the list changed and carries no text, so it needs nothing.

**2. A present subject without a usable kind passed.**
- A reference that is present without a non-empty string kind is now an unreadable subject. So is an Event with neither primary reference.
- Either one withholds the text (`EventWithheld`), blocks the editor and holds Describe.
- An absent or null `related` stays harmless.

**Found while fixing:** the gated load test (`KLYX_LOAD=1`, health 1c §7) serves every group. It had not registered the kinds S1a and S1b watch, so it could not run since S1a. Every coverage kind is now registered.

Tests: `crd.TestEventObjects` covers no kind, an empty kind, an empty related kind, no subject, a null related, and the classifier table. `workloads.TestSummarizeEventsSharesTheEventClassifier`, `appbridge.TestListEventsRelatedSubjectRule` (the serialised bridge result) and `fleet.TestEventDetailUsesSubjectRule` (now with the inspector's typed events) cover the readers.

## 15. As built: Stages 1 to 8

**Stage 1 (918db30): pure packages.**
- `internal/gitops/resume` is S1a's resume tracker, generalised over a `Sample` (kind, UID, disabled, Ready fingerprint, and whether this Ready is evidence). The Flux Operator's tracker is now an adapter over it. It has the same bounds, lost markers and saturation, failing closed.
- `internal/gitops/composition` holds:
  - the field-by-field projection, budgeted at 4 KiB per object;
  - the generator and artifact parse, and their rules;
  - `ProducerOf`, which gives every producer state in §2.
- The artifact's URL is projected as its origin only (`urlscrub.OriginOf`, Stage 6). A copy rule that is not an object marks the rules incomplete.

**Stage 2 (707d6d4): `internal/gitops/notify`.**
- The Receiver projection never carries `webhookPath` or a message.
- `ReceiverRule` uses Klyx's own wording, and `ReadyMeaning` says what Ready means by type.
- Providers and Alerts are facts. The address and proxy are read only for whether they are set, and only the keys of event metadata are read.
- Dispatch evidence is a count of reasons.

**Stage 3a (791a8f9): controllers.** source-watcher and notification-controller are found by component label. Each is needed only for its own served resources, judged per Deployment, and reaches the Flux task. The views' presence flags (Stage 7) come from the same detection.

**Stage 3b (38c9a3c): the Observer.**
- Three watches, each at its one served version; another served version is a named gap. The budget is 512, 384 and 128 KiB, 1 MiB per cluster.
- **Deviation:** generators and artifacts are a part of their own, `composition`, rather than rows of `sources`. The row types differ, and so does the inspector shape: an artifact's producer block. The Sources view is unchanged, and the composition part is shown in its own view (Stage 7).
- The part joins a generator's inventory and an artifact's producer from the same capture. The tracker is pruned only by a complete read.

**Stage 4 (5621832): verbs.**
- A generator uses source-watcher's annotations, through the operator's UID-guarded annotation path, and only at v1beta1.
- A Receiver reconciles and suspends.
- A Provider or an Alert suspends only: it is static, and reconcile is refused.
- An ExternalArtifact has no verb. Reconcile with source is refused for a resource bound to one (Stage 5).

**Stage 5 (6f5b59e): drawings and joins.**
- The dependency graph follows an artifact's generator sourceRef, qualified by the identity the artifact records (verified, named, disagree), and then the generator's sources.
- A disagreeing generator is drawn but never attributed: no node reached only through it is an upstream failure. A producer of another kind is declared, never followed.
- The bound-source panel gives an artifact its own rule and a producer line from the shared composition rows, and names a feature-gate refusal for its controller.
- `AppliedArtifact` covers artifact-bound Kustomizations. The Line excludes them as artifact sources (`ExcludedArtifact`), as it does OCI and Bucket.

**Stage 6 (d8fbf11): the bridge.**
- Row DTOs for both parts.
- `GetNotifications`:
  - reads at v1beta3, capped at 500 each, with what is past the cap said;
  - joins both ways, where a capped read is "incomplete", never "not found";
  - keeps only the reasons of the Alerts' events.
- `GetCompositionDetail` is UID-fenced. It gives events under the message rules, a generator's sources and an artifact's consumers from the shared rows.
- The AI evidence allowlists cover both parts. Chat verdicts map the composition, Receiver and image kinds; the image kinds were missing since S1b.

**Stage 7 (8b870e1): the UI.**
- **Deviation:** a Composition view beside Sources rather than inside it, for the same reason as Stage 3b.
- The Composition view leads each inspector with the object's own verdict, then the producer block, which carries a health only when the producer's identity is established.
- The Notifications view has three tabs: Receivers with verdicts, and Providers and Alerts as facts read when the view opens.
- The dependency drawing qualifies producer edges.
- Capability flags gate both destinations, and reveal, attention, Describe and the smoke titles know the new kinds.

**Stage 8: the lab, parity and smoke.**
- The lab (`scratchpad/lab-s1c/apply.sh`, kind-klyx only):
  - installs the pinned ArtifactGenerator CRD, read first, and stand-ins for both controllers;
  - adds generators (ready, failing with a signed URL, suspended);
  - adds artifacts (recorded producer, declared producer with a signed URL, none);
  - adds an artifact-bound Kustomization, Receivers, Providers, Alerts, and two Events whose text must never reach the screen.

  Every protected value carries one needle, and the smoke asserts it is absent from every view it opens.
- Parity copies the three new CRDs, seeds a healthy generator, artifact and Receiver with their stand-ins, and both oracles name the two parts.

**Found while building:** the gated load test (§14).
