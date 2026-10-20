# Coverage S3b: proven admission attribution

The roadmap's S3b (`2026-10-16-klyx-coverage-roadmap.md` §2, S3) has two parts:
- **A write error that names its webhook or policy is attributed to it**, starting from the existing parser in `InstanceDetail.tsx`. The policies and webhooks matching the request are listed as *candidates*, labelled so.
- **A live-vs-Git difference is never attributed to a mutator**, and never discounted as "not drift". Mutation attribution stays unknown; an audit-log integration is a separate scope.

S3a (`2026-10-18-klyx-coverage-s3a-admission-availability.md`) gave Klyx the admission inventory: webhook configurations, policies and bindings, each backend's availability, and the rules kept within its projection. S3b joins that inventory to Klyx's own write errors.

## 1. What Klyx does today, read from the code

Surveyed at `043a2e4`.

### 1.1 The parsers

**Two parsers read admission errors from text:**
- **`formatKubernetesActionError`** (`InstanceDetail.tsx` L217-257). Only the YAML panel uses it: dry-run L1149, apply L1162, refresh L1186, and the inline notice L1229-1233.
- **The `webhookDenied` regex** in `cachediff/ssa.go` L123-158.

**Every other write path flattens a `metav1.Status` into a string:**
- `ActionResultDTO{OK, Error string}` (`gitops_dto.go` L11-16), `AdhocResultDTO`, and the toasts and Activity log built from them;
- `armedWriteErr` (`gitopsactions.go` L137-147) adds "- nothing was written" for a 4xx and "- the outcome is unverified" otherwise.

### 1.2 Five defects

1. **A call failure reads as a timeout.** The frontend's timeout regex `/failed calling webhook "([^"]+)": .*?(context deadline exceeded|timeout|timed out)/i` (L221-224) matches the `?timeout=10s` query that client-go adds to every webhook URL (`rest/request.go` L472-474). The error reads `failed calling webhook "x": failed to call webhook: Post "https://...?timeout=10s": <cause>` (`util/webhook/error.go` L34-36; `validating/dispatcher.go` L311). A refused connection, missing endpoints or an x509 failure therefore reads "timed out, usually transient". The test (`InstanceDetail.test.tsx` L135) has no query string.
2. **Four shapes go unparsed:**
   - a webhook's denial "without explanation" (`webhook/errors/statuserror.go` L29-49);
   - a ValidatingAdmissionPolicy denial (`policy/validating/dispatcher.go` L305-307);
   - the generic policy dispatcher's denial ("policy '%s' with binding '%s' denied request", `policy/generic/policy_dispatcher.go` L509-513, used by MutatingAdmissionPolicy);
   - "admission webhook %q does not support dry run" (statuserror.go L59-61): the dry-run branch (L247) catches it but drops the webhook's name.
3. **`refusedForUID` misreads policy denials** (`fleet/gitopsactions.go` L152-155). It is `IsNotFound || (IsInvalid || IsConflict) && contains(lower(err), "uid")`, so a policy denial (Invalid by default) whose message contains "uid" (in "guide", "fluid", "uuid") reads as "the object was replaced" on every UID-guarded verb. No test covers it.
4. **A webhook call failure reads as unverified.** A fail-closed webhook whose call failed returns 500 (`apierrors.NewInternalError`, validating `dispatcher.go` L218), and `armedWriteErr` says "the outcome is unverified". But admission runs before storage, so an error from admission proves nothing was written.
5. **There is no matching logic anywhere**, so Klyx never lists candidates.

### 1.3 Upstream facts the design rests on

Sources: `k8s.io/apiserver` v0.34.12, v0.35.9, v0.36.5 and v0.37.1.

- **A webhook denial names the webhook** (`webhooks[].name`), never its configuration. Names are not unique across configurations. Mutating and validating webhooks share `ToStatusErr` (`mutating/dispatcher.go` L330).
- **Validating webhooks run in parallel, and only the first denial is returned** (`validating/dispatcher.go` L239-245). A denial proves that one webhook denied; it says nothing of the others.
- **A ValidatingAdmissionPolicy denial** is `admission.NewForbidden(<resource> "<name>" is forbidden: ...)`, with:
  - the reason the policy chose (default Invalid, 422);
  - a `details.causes[]` entry whose message is exactly "ValidatingAdmissionPolicy '<p>' [with binding '<b>'] denied request: <msg>" (L301-316).
- **A call failure** reaches the client as `InternalError` (500): "Internal error occurred: failed calling webhook "<n>": failed to call webhook: Post "<url>": <cause>". `<cause>` is Go's `url.Error` text. A webhook that answered with a status of its own (`ErrCallingWebhook.Status`) returns that status instead.
- **The UID guards' own errors:**
  - a UID precondition fails as Conflict, "Precondition failed: UID in precondition: <a>, UID in object meta: <b>" (`storage/interfaces.go` L159);
  - an immutable UID fails as Invalid, with a cause on field `metadata.uid` (`objectmeta.go` L332).
- **The rule matcher** (`webhook/predicates/rules/rules.go`) matches scope, operation, group, version and resource (with subresources: `*`, `*/*`, `pods/*`, `*/scale`). Selectors, `matchConditions`, `excludeResourceRules` and bindings only narrow a match further.

## 2. The classifier

A pure Go package function, `admission.Classify(err error) Finding`, replaces both parsers. It reads the structured status first (code, reason, `details.causes`) and the message only at fixed, quoted anchors. The upstream wraps a message in several layers, so it is never matched by a free-standing regex.

| Finding | Recognised by | Klyx says |
|---|---|---|
| **webhook denied** | the message contains `admission webhook "<n>" denied the request` followed by `: ` or ` without explanation` | "denied by admission webhook <n>", with its message when there is one |
| **webhook call failed** | `failed calling webhook "<n>": `, and the cause after the quoted URL classified by its own text: deadline (`context deadline exceeded`, `Client.Timeout exceeded`, `i/o timeout`), refused (`connection refused`), TLS (`x509:`, `tls:`), no endpoints (`no endpoints available`), or other | "admission webhook <n> could not be called: <cause>". The cause is read only after the URL, so the URL's own `timeout=` never counts |
| **no dry run** | `admission webhook "<n>" does not support dry run` | "admission webhook <n> does not support dry run", naming it |
| **policy denied** | a `details.causes[]` message, or the message, matching `ValidatingAdmissionPolicy '<p>'[ with binding '<b>'] denied request: ` or `policy '<p>'[ with binding '<b>'] denied request: ` or `policy "<p>" denied request: ` | "denied by ValidatingAdmissionPolicy <p> (binding <b>)", or "denied by admission policy <p>" for the generic form (Klyx does not guess its kind) |
| **not admission** | none of the above | the existing wording |

**Bounds:** names at most 253 bytes and quoted. A denial message is shown clipped to 512 bytes, locally only (§6).

**Outcome:** every admission finding is written "nothing was written: admission refused the request", including a call failure's 500, since admission precedes storage (§1.3). The finding replaces "the outcome is unverified" for that error only.

**`refusedForUID`** reads the guards' own shapes (§1.3), never the substring "uid": a Conflict whose message starts "Precondition failed: UID in precondition", or an Invalid with a cause on `metadata.uid`. An admission finding is never a replacement.

## 3. Candidates

Each write path declares its request:
- the operation (CREATE, UPDATE, DELETE);
- group, version, resource, subresource (`scale`, `eviction`, `status`);
- whether it is namespaced.

| Write path | The request |
|---|---|
| YAML apply and dry-run | UPDATE, or CREATE for the composer, on the object's resource |
| Scale | UPDATE on `<resource>/scale` |
| Restart, pause, Flux and Argo verbs | UPDATE (a patch) |
| Rollout undo | UPDATE on the Deployment |
| Delete pod, delete scratch | DELETE |
| Cordon | UPDATE on `nodes` |
| Drain | CREATE on `pods/eviction` (kubectl) |
| Diagnostic pod | CREATE on `pods` |
| KEDA verbs, FluxOp | UPDATE |

**Candidates are the webhooks and policies whose rules could match that request** (S3a's rows), by the upstream matcher's semantics, held to the upstream matcher by an oracle (§8):
- **Over-inclusive by construction.** Selectors, `matchConditions`, exclusions and bindings are not evaluated: they only narrow. With `matchPolicy: Equivalent` (the default), a rule naming another version of the same group and resource is a candidate.
- **Rules not all read.** S3a keeps 8 rules per webhook and caps each list at 8. A webhook or policy with rules or list entries past the bound is listed as "may match: not every rule was read". The S3a projection gains a flag per capped list, so Klyx knows when it cut one.
- **Labelled always:** "Candidates: their rules could match this request. Klyx does not evaluate their namespace or object selectors, match conditions or bindings, so a candidate may not have run."
- **The attributed webhook or policy** is marked among the candidates and linked to its S3a row:
  - when its name is a webhook name in exactly one configuration, that configuration;
  - when several configurations have a webhook of that name, each one, said as "a webhook named <n> in <A>, <B>".
- **Under an unread or stale admission read**, the candidates are "not established" or "as of".

## 4. Where it shows

- **The YAML panel:** the finding as its headline; the candidates below, collapsed beyond 5 with a count. The inline notice replaces the regex's.
- **Every other write path:** the toast says the finding in one line ("Scale refused: denied by admission webhook <n>"). The Activity log keeps that line and the candidates' names; its stored format gains the finding, and older entries read as before.
- **The admission lens:** a candidate's link opens its S3a row.
- **The live diff and the Flux drift surfaces** add, unconditionally: "A difference between the live object and Git can come from a mutating webhook or policy, a controller, or a person. Klyx does not attribute it." Nothing is discounted: no wording calls a difference "not drift", or attributes it to a mutator. A server-side dry-run diff says that the dry run passed mutating admission and defaulting, so it compares what the API server would store.

## 5. The bridge

- **The DTO:** `ActionResultDTO` and `AdhocResultDTO` gain `admission?: AdmissionFindingDTO` (kind, name, binding, cause, message clipped, `nothingWritten`) and `candidates?: CandidateDTO[]` (kind, configuration, name, `capped`, `attributed`, state). Old fields stay.
- **The converter:** one function, `toAdmission(err, request)`, called by every write path's error conversion. A path cannot report an error without passing its request.
- **The candidates** are computed when the error is returned, from the Observer's admission rows (`HealthRowsNow`), under its coverage. It is a read, never a new watch.

## 6. AI and chat

Write errors never cross to AI or chat, and S3b keeps that. The admission finding's kind and the webhook or policy's name are allowlisted for Explain on the admission rows (S3a's allowlists); the denial message never crosses.

## 7. Not in S3b

- Attributing a mutation or a drift to a mutator (needs audit logs).
- Evaluating selectors, match conditions or CEL.
- Admission errors seen only in a controller's Events (for example Flux's apply failures): their text is the controller's; the existing event surfaces show it.
- `kubectl exec`, debug and port-forward, whose admission (CONNECT) Klyx never sees.

## 8. Tests

- **The classifier's oracle.** A generator in the scratchpad builds every error shape with the upstream code at v0.34.12, v0.35.9, v0.36.5 and v0.37.1:
  - `ToStatusErr` with a message, a reason and neither;
  - `NewDryRunUnsupportedErr`;
  - an `ErrCallingWebhook` around real `url.Error`s (deadline, refused, x509, no endpoints), wrapped as the dispatchers wrap it;
  - `admission.NewForbidden` with the policy dispatcher's message and causes;
  - the generic dispatcher's two forms;
  - the UID precondition Conflict and the immutable-UID Invalid.

  The serialised statuses are committed as testdata. `Classify` must read each exactly, and an unrelated Invalid containing "uid" must not read as a replacement.
- **The matcher's oracle.** The upstream `rules.Matcher` at the same versions runs on a fixture table: operations, groups, versions, resources with each subresource form, scopes, and the namespace object itself. Klyx's matcher must agree on every row.
- **Write paths:** each path's request declaration (a table test), and each path returning the finding through its DTO.
- **The frontend:** the YAML panel's headline and candidates; a toast's line; the Activity log's old and new entries; the drift sentence present on both diff surfaces, and no "not drift" wording anywhere (a source scan).
- **The lab** (`lab-s3b`, labelled and namespaced, with a teardown):
  - a validating webhook that denies a labelled object, backed by a real Service;
  - one whose Service has no endpoints (a call failure: "no endpoints");
  - a ValidatingAdmissionPolicy with a binding that denies a labelled object;
  - each exercised by Klyx's YAML apply, scale and a Flux-suspend patch on fixture objects.
- **Parity:** none. S3b changes no verdict.
- **The smoke:** a YAML apply refused by the lab webhook shows the finding and the candidates; a scale refused by the policy shows its toast line.
- **Mutations, captures, as-built,** as every slice.
