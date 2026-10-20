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
  - a `details.causes[]` entry, appended last, whose message is exactly "ValidatingAdmissionPolicy '<p>' [with binding '<b>'] denied request: <msg>", the same text that ends the outer message (L301-316).
- **The generic policy dispatcher's denial** (MutatingAdmissionPolicy) is `admission.NewForbidden` with its message replaced by the first denied policy's own text ("policy '<p>' [with binding '<b>'] denied request: ..." or `policy "<p>" denied request: ...`), and every denial appended as a cause (`policy/generic/policy_dispatcher.go` L233-262).
- **A denial message can echo submitted values.** A webhook writes its own message, and `ToStatusErr` keeps it verbatim; S0b's `cachediff.safeError` (`ssa.go` L126-157) therefore withholds free server text for Secrets and registered payload kinds (`TestDryRunErrorKeepsOnlyStructuredFacts`).
- **A call failure** reaches the client as `InternalError` (500): "Internal error occurred: failed calling webhook "<n>": failed to call webhook: Post "<url>": <cause>". `<cause>` is Go's `url.Error` text. A webhook that answered with a status of its own (`ErrCallingWebhook.Status`) returns that status instead.
- **The UID guards' own errors:**
  - a UID precondition fails in storage (`storage.Preconditions.Check`, `storage/interfaces.go` L159), and the registry's error interpretation wraps it as a Conflict (409) whose client-visible message is `Operation cannot be fulfilled on <resource> "<name>": StorageError: invalid object, ... AdditionalErrorMsg: Precondition failed: UID in precondition: <a>, UID in object meta: <b>` (revision 1: the inner phrase never starts the message);
  - an immutable UID fails as Invalid, with a cause on field `metadata.uid` (`objectmeta.go` L332).
- **The rule matcher** (`webhook/predicates/rules/rules.go`) matches scope, operation, group, version and resource (with subresources: `*`, `*/*`, `pods/*`, `*/scale`). Selectors, `matchConditions`, `excludeResourceRules` and bindings only narrow a match further.
- **`matchPolicy: Equivalent`** (the default) adds a second pass: the dispatchers ask the API server's equivalent-resource registry for the request's equivalents and rerun the matcher on each (`generic/webhook.go` L324-350, `policy/matching/matching.go` L151-175). An equivalent can be in another group (`extensions/v1beta1 deployments` for `apps/v1 deployments`), so the exact matcher alone is not the whole rule.

## 2. The classifier

A pure Go function, `admission.Classify(status) Finding`, replaces both parsers. It reads the server's own status (code, reason, message, `details.causes`), never wrapper prose that Klyx or a client added around it: every write path keeps the `apierrors.APIStatus` it received, and Klyx's own context ("scale deployment x: ...") is added beside the finding, never parsed.

### 2.1 Envelopes, in precedence order (revision 1)

A finding is attributed only when the **outer** status has one of these exact shapes. The first that matches wins; content nested inside a matched envelope (a webhook's own causes, a message that quotes another component) never re-attributes it.

| # | Envelope | Recognised by | Finding |
|---|---|---|---|
| 1 | a webhook's denial (`ToStatusErr`) | code 400 or more, and the message **starts** with `admission webhook "<n>" denied the request` followed by `: ` or ` without explanation` | webhook denied, name `<n>` |
| 2 | dry run unsupported | code 400, reason BadRequest, the message **is** `admission webhook "<n>" does not support dry run` | webhook does not support dry run, name `<n>` |
| 3 | a webhook call failure | code 500, reason InternalError, the message **starts** with `Internal error occurred: failed calling webhook "<n>": ` | webhook could not be called, name `<n>`, cause read only from the text after the quoted URL (§2.2) |
| 4 | a ValidatingAdmissionPolicy denial | the message has the `NewForbidden` form `<resource> "<name>" is forbidden: ` (or `forbidden: ` without a name) followed **exactly** by the **last** cause's message, and that cause matches `ValidatingAdmissionPolicy '<p>'[ with binding '<b>'] denied request: ` | policy denied, kind ValidatingAdmissionPolicy, `<p>`, `<b>` |
| 5 | the generic policy dispatcher's denial | reason Forbidden or the policy's own, `details.name` and `details.kind` set by `NewForbidden`, the message **equal** to the first cause's message, and that message matching `policy '<p>'[ with binding '<b>'] denied request: ` or `policy "<p>" denied request: ` | policy denied, kind not guessed ("admission policy <p>") |

**Anything else is not attributed**, and keeps today's wording. That includes an ordinary Invalid whose field detail contains an admission phrase, a 500 that is not envelope 3 (it stays "the outcome is unverified"), and text without a status. Only an attributed finding upgrades a 500 to a definite refusal.

### 2.2 A call failure's cause

The cause is the text after the quoted URL in `failed to call webhook: Post "<url>": <cause>`, never the URL itself (whose `?timeout=` query defeated the old regex). Its category: deadline (`context deadline exceeded`, `Client.Timeout exceeded`, `i/o timeout`), refused (`connection refused`), TLS (`x509:`, `tls:`), no endpoints (`no endpoints available for service`), or other. A failure the dispatcher reports without that form ("could not get REST client", "received invalid webhook response") is "other", its text kept as §2.4 allows.

### 2.3 Scope: one API request (revision 1)

A finding is about **the API request that was refused**, never the whole action:
- **Each write path wraps every API call's error** in `admission.RequestError{Request, Err}` (§3), so `errors.As` finds the refused request inside a composite action's error.
- **The sentence** is "<verb> <kind> <name> was refused by admission before it was stored". Nothing of that request was written; admission precedes storage for create, update, patch and delete.
- **Earlier steps keep their own outcome.** A composite action reports each step: `ReconcileWithSource` patches the source, then the target; when the target is denied, the result says the source was annotated (written) and the target refused. The toast and the Activity entry say both.
- **Multi-document creates** (the ad-hoc composer) keep their per-document results, each with its own finding.

### 2.4 What is shown, and kept (revision 1: the S0b boundary)

The classifier runs in process on the raw status. Before anything is serialized, shown, copied or retained, the subject's protection is applied, the same predicate `cachediff.safeError` uses:
- **A protected subject** (a Secret, or a kind registered in S0b's payload registry, such as HelmRelease): the finding keeps only structured facts (the envelope kind; the webhook or policy name; the binding; the call failure's category; code and reason). Every free text is withheld: the denial message, the causes' messages, the dispatcher's own text and the raw error string. It says "the server's message is withheld for a <kind>: it can echo submitted values". The old raw `Error` field is replaced by the same withheld form.
- **Any other subject:** the denial message is shown clipped to 512 bytes, after the existing credential scrub.
- **The toast, the clipboard copy and the Activity log** receive only the serialized finding, never the raw error. The Activity log's stored entry is the one-line summary and the candidates' names.
- **A regression gate:** `TestDryRunErrorKeepsOnlyStructuredFacts` stays, and new needle tests (§8) cover the DTO, the toast text and the Activity entry.

### 2.5 The UID guards (revision 1)

`refusedForUID` reads the guards' wire shapes, never a substring:
- **NotFound**, as today: the guarded object is gone.
- **A Conflict** (409) whose message starts `Operation cannot be fulfilled on ` and carries `AdditionalErrorMsg: Precondition failed: UID in precondition: ` (§1.3's wrapped form).
- **An Invalid** with a cause on field `metadata.uid`.

An attributed admission finding is never a replacement, whatever its text.

## 3. Candidates

### 3.1 The request each path declares

Each API-backed write path declares its request: the operation (CREATE, UPDATE, DELETE), group, version, resource, subresource, and whether it is namespaced.

| Write path | The request |
|---|---|
| YAML apply and dry-run | UPDATE, or CREATE for the composer, on the object's resource |
| Scale | UPDATE on `<resource>/scale` |
| Restart, pause, rollout undo, Flux and Argo verbs, KEDA verbs, FluxOp | UPDATE (a patch) on the object's resource |
| Delete pod, delete scratch | DELETE |
| Cordon | UPDATE on `nodes` |
| Diagnostic pod | CREATE on `pods` |
| The live diff's server-side dry run | UPDATE, or CREATE when the object is absent |

### 3.2 The paths outside the converter (revision 1)

| Path | Transport | In S3b |
|---|---|---|
| Drain | `kubectl drain` as a subprocess: it cordons, then evicts each pod (CREATE on `pods/eviction`), falling back to DELETE where eviction is unsupported. Errors are kubectl's text in a log stream, after the stream has started | **Deferred.** No structured status reaches Klyx. The stream keeps kubectl's own text; S3b attributes nothing there and says so in the drain panel's help |
| Debug | `kubectl debug` in an external terminal: a PATCH on `pods/<name>/ephemeralcontainers` (UPDATE), then an attach. Klyx returns before that request is made | **Outside.** Klyx never sees the result; the request is UPDATE, not CONNECT (corrected) |
| Exec, port-forward | CONNECT, in a terminal or a stream | **Outside.** Klyx never sees admission's answer |
| Helm rollback | the helm CLI's text | **Outside.** No structured status |

### 3.3 Matching (revision 1)

Candidates are the webhooks and policies whose rules could match the request, by the request's own resource or a same-named equivalent. The list is a superset of those, and its soundness rests on exact inputs; rules that could match only through a differently named equivalent are counted, not listed (revision 2):
- **Exact identifiers.** S3a's projection clips strings to 64 bytes for display, which changed the bytes a match compares (a long API group). The S3a projection gains exact identifiers for matching (groups, versions, resources, operations, scope) kept apart from the display copy, within S3a's budget. An identifier that cannot be kept exactly marks the rule "may match: a value could not be kept exactly", and the candidate stays.
- **The exact pass** follows `rules.Matcher` (scope, operation, group, version, resource with its subresource forms), held to it by an oracle (§8).
- **The Equivalent pass.** Klyx cannot read the API server's equivalent-resource registry, which groups resources by their storage identity, not their names: a storage alias can make a differently named resource equivalent (`NewEquivalentResourceRegistryWithIdentity`, the storage factory's resource-prefix overrides). So, under `matchPolicy: Equivalent` (the default):
  - **listed:** a rule whose resource name matches the request's (or `*`), in any group and version, labelled "may match through an equivalent resource";
  - **counted, not listed:** a rule whose operations and scope match but whose resources name something else. It could match only through an alias Klyx cannot see. The candidates end with "N more webhooks or policies use Equivalent matching on other resources, and could match through an alias the API server registers; Klyx cannot read that registry";
  - so the list is a superset of the rules that match by the request's own resource or a same-named equivalent, and **not** of every possible match: the count says how many it leaves out, and the note never calls the list complete.

  Under `Exact`, only the exact pass counts, with no alias line. The policies' `matchPolicy` joins S3a's projection (today it keeps the webhooks' only).
- **Not evaluated, and only narrowing:** namespace and object selectors, `matchConditions`, `excludeResourceRules`, bindings.
- **Rules or lists S3a capped:** "may match: not every rule was read". S3a's projection gains a flag per capped list.
- **Configurations or webhooks S3a omitted** (a capped read, a per-kind budget, an unread kind): the candidates say "and possibly webhooks or policies Klyx did not read".
- **Bounds:** at most 16 candidates are serialized, then "N more"; the UI shows 5, then the rest on demand.
- **Labelled always:** "Candidates: their rules could match this request. Klyx does not evaluate their namespace or object selectors, match conditions or bindings, so a candidate may not have run."

### 3.4 The attributed one

- **The named webhook or policy** is marked among the candidates. The error gives a webhook's name only, never its configuration's UID, so a link to configuration A is an inventory correlation: "a webhook named <n> in A", or "in A and B" when several have one, plus "and possibly others Klyx did not read" under §3.3's omissions.
- **A finding whose name matches no inventory row** keeps its name and says so: "not in Klyx's admission inventory (it may not have been read)".
- **No other webhook is inferred to have allowed the request.** Validating webhooks run in parallel and only the first denial returns (§1.3).
- **Under an unread or stale admission read,** the candidates are "not established" or "as of".

## 4. Where it shows

- **The YAML panel:** the finding as its headline, the candidates below. The inline notice replaces the regex's.
- **Every other API-backed write path:** the toast says the finding in one line ("Scale refused: denied by admission webhook <n>"), with earlier steps' outcomes when there are any (§2.3). The Activity log keeps that line and the candidates' names; its stored format gains the finding, and older entries read as before.
- **The admission lens:** a candidate's link opens its S3a row.
- **The live diff and the Flux drift surfaces** add, unconditionally: "A difference between the live object and Git can come from a mutating webhook or policy, a controller, or a person. Klyx does not attribute it." No wording calls a difference "not drift", or attributes it to a mutator.
- **A completed server-side dry-run diff** says that its result passed mutating admission and defaulting as of the dry run's time, so it shows what the API server would have stored then; a later apply's admission may differ. A skipped, unread or failed dry run says nothing of the kind.

## 5. The bridge

- **The DTO:** `ActionResultDTO` and `AdhocResultDTO` gain:
  - `admission?: AdmissionFindingDTO`: the envelope kind, name, binding, call-failure category, the refused request (verb, kind, namespace, name), the message (clipped, or withheld for a protected subject), and `nothingWritten` for that request;
  - `steps?: StepDTO[]`: each earlier step's outcome in a composite action;
  - `candidates?: CandidateDTO[]`: kind, configuration (as a correlation), name, `mayMatch` and its reason, `attributed`, the admission read's state, at most 16, with `candidatesOmitted`.

  Old fields stay, carrying the withheld form for a protected subject.
- **The converter:** one function, `toAdmission(err, subject)`, called by every API-backed write path's error conversion (§3.1). It finds the innermost `RequestError`, classifies its status, applies §2.4, and computes the candidates from the Observer's admission rows (`HealthRowsNow`) under their coverage: a read, never a new watch.

## 6. AI and chat

Write errors never cross to AI or chat, and S3b keeps that. The admission finding's kind and the webhook or policy's name are allowlisted for Explain on the admission rows (S3a's allowlists); the denial message never crosses.

## 7. Not in S3b

- Attributing a mutation or a drift to a mutator (needs audit logs).
- Evaluating selectors, match conditions or CEL, or reading the API server's equivalent-resource registry.
- Drain, debug, exec, port-forward and Helm rollback (§3.2).
- Admission errors seen only in a controller's Events (for example Flux's apply failures): their text is the controller's; the existing event surfaces show it.

## 8. Tests

- **The classifier's oracle, positive and negative.** A generator in the scratchpad builds every shape with the upstream code at v0.34.12, v0.35.9, v0.36.5 and v0.37.1, serialized as the API server sends them (the status JSON a client decodes), committed as testdata:
  - positive: `ToStatusErr` with a message, a reason and neither; `NewDryRunUnsupportedErr`; an `ErrCallingWebhook` around real `url.Error`s (deadline, refused, x509, no endpoints), wrapped by `NewInternalError` as the dispatchers do; the validating policy dispatcher's `NewForbidden` with its appended cause, with and without a binding; the generic dispatcher's form with several causes;
  - negative and ambiguous: a webhook denial whose own causes carry a VAP-looking message naming another policy (attributed to the webhook only); `apierrors.NewInvalid` with a `field.Forbidden` detail quoting a webhook denial (not attributed); an ordinary 500 (stays unverified); a message embedding a denial phrase mid-text (not attributed);
  - the UID guards: a precondition built through `storage.Preconditions.Check` and the registry's update and delete error interpretation, then serialized; an immutable-UID Invalid; a resourceVersion-precondition Conflict and messages merely containing "uid" (controls, not replacements).
- **The matcher's oracle.** The upstream `rules.Matcher` at the same versions runs on a fixture table: operations, groups (one longer than 64 bytes), versions, resources with each subresource form, scopes, and the namespace object itself. Klyx's exact pass must agree on every row. The Equivalent pass is tested against an upstream equivalent-resource registry probe: a same-named equivalent in another group is listed; a differently named equivalent sharing a storage identity (`widgets` and `widgetaliases`) is not listed but is counted in the alias line; `Exact` controls list neither.
- **Protected subjects:** needles in a webhook's denial message, for a Secret and a HelmRelease, absent from the serialized DTO, the toast text and the Activity entry, while the webhook's name is present. `TestDryRunErrorKeepsOnlyStructuredFacts` stays as a gate.
- **Scope:** `ReconcileWithSource` with the source patch succeeding and the target denied (the source reported written, the target refused), and the converse (the source denied, nothing after it attempted); a multi-document create with one document denied. The protected-subject rule applies to each step's own subject.
- **Write paths:** each path's request declaration (a table test), and each path returning the finding through its DTO.
- **The frontend:** the YAML panel's headline and candidates; a toast's line, with steps; the Activity log's old and new entries; the drift sentence on both diff surfaces, and no "not drift" wording anywhere (a source scan); the dry-run sentence only for a completed dry run.
- **The lab** (`lab-s3b`, labelled and namespaced, with a teardown): a validating webhook that denies a labelled object, backed by a real Service; one whose Service has no endpoints (a call failure, "no endpoints"); a ValidatingAdmissionPolicy with a binding that denies a labelled object; each exercised by Klyx's YAML apply, scale and a Flux-suspend patch on fixture objects.
- **Parity:** none. S3b changes no verdict.
- **The smoke:** a YAML apply refused by the lab webhook shows the finding and the candidates; a scale refused by the policy shows its toast line.
- **Mutations, captures, as-built,** as every slice.

## 9. Revision 1: Codex's design review of f4b9642

Codex (handoff 2a3f5249) requested changes: one P1 and five P2 findings, each with an upstream or overlay probe.

| # | Finding | Where answered |
|---|---|---|
| P1 | The finding would carry a denial message, which can echo submitted values, past S0b's protected-kind withholding (Secrets, payload kinds) into the DTO, toasts and Activity | §2.4: classify in process, then apply `safeError`'s protection before any serialization or retention; a protected subject keeps structured facts only, its raw `Error` withheld too. §8: needle tests on the DTO, toast and Activity; the S0b test stays as a gate |
| P2 | "Nothing was written" was said of the whole action, though `ReconcileWithSource` may have written its source before the target was denied | §2.3: the finding is one API request's (`RequestError`), earlier steps keep their outcomes in the result, toast and Activity; multi-document creates stay per document. §8: source-success/target-denied |
| P2 | Fixed phrases and causes do not prove attribution: a webhook's own VAP-looking cause, or an Invalid quoting a denial | §2.1: five outer envelopes in precedence order, matched by code, reason and message position; nested content never re-attributes; anything else unattributed, and only an attributed finding upgrades a 500. §8: the negative and ambiguous corpus |
| P2 | The UID guard's wire form is a wrapped `StorageError` Conflict, not a message starting with the inner phrase | §1.3 and §2.5: the wrapped Conflict, the `metadata.uid` Invalid and NotFound. §8: built through the storage check and the error interpretation, with resourceVersion and "uid" controls |
| P2 | The candidate superset was not sound: S3a clips identifiers to 64 bytes, and Equivalent matching crosses groups | §1.3 and §3.3: exact identifiers kept apart from display, a value not kept exactly keeps the candidate; under Equivalent, a resource-name match in any group is a candidate; omitted configurations said; the attributed webhook's configuration is a correlation (§3.4). §8: the long group, cross-group equivalence, `Exact` controls |
| P2 | Drain and debug do not fit one converter: drain is a kubectl stream, debug a PATCH in an external terminal (UPDATE, not CONNECT) | §3.2: a per-path boundary table; drain deferred, debug, exec, port-forward and Helm rollback outside, debug's request corrected |

Codex's guidance, taken: a named denial keeps its name with no inventory row (§3.4); no other webhook is inferred to have allowed the request (§3.4); the dry-run sentence describes that dry run and its time, only when it completed (§4); serialized candidates are bounded with omissions counted (§3.3).

## 10. Revision 2: Codex's re-review of ab32cff

Codex (handoff 76087d53) requested changes: one residual P2. The P1 and the other four P2 findings are resolved.

| # | Finding | Where answered |
|---|---|---|
| P2 | Equivalent matching does not keep the resource name: the registry groups resources by storage identity, so a differently named alias can match, and a same-name prefilter is not a sound superset of every possible match | §3.3: same-named equivalents are listed; rules on other resources under Equivalent are counted, not listed, in an alias line that says why; the list is described as a superset only of matches by the request's own resource or a same-named equivalent. §8: a different-plural pair sharing a storage identity, with an `Exact` control |

Codex's implementation guidance, taken: the protected-subject rule applies to each step's own subject, and the converse composite case is tested (§8); the policies' `matchPolicy` joins S3a's projection (§3.3).
