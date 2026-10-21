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
  - **counted, not listed:** a rule whose operations and scope match but whose resources name something else. It could match only through an alias Klyx cannot see. The count is of distinct webhooks and policies, excluding any already listed, and carries the inventory's completeness qualification when it cannot be exact. The candidates end with "N more webhooks or policies use Equivalent matching on other resources, and could match through an alias the API server registers; Klyx cannot read that registry";
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
- **Scope:** `ReconcileWithSource` with the source patch succeeding and the target denied (the source reported written, the target refused), and the converse (the source denied, the target still attempted and written, as the verb's best-effort source step already does: `gitopsactions.go` L180-222), each request with its own finding and subject protection, both outcomes asserted in the result, the toast and the Activity entry; a multi-document create with one document denied. S3b reports a composite action's outcomes and never changes its steps.
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

## 11. Revision 3: Codex's re-review of 8807808

Codex (handoff bf417e67) requested changes: one P2 in the composite-action test. The equivalence finding is resolved.

| # | Finding | Where answered |
|---|---|---|
| P2 | The converse test ("the source denied, nothing after it attempted") would change `ReconcileWithSource`, whose source step is best effort: the target is still patched | §8: the source denied, the target attempted and written, each request with its own finding and protection, both outcomes in the result, toast and Activity. S3b reports a composite action's outcomes and never changes its steps |

Codex's guidance, taken: the alias count counts distinct webhooks and policies, excludes those already listed, and keeps the inventory's completeness qualification (§3.3).

**Design accepted at 360c1eb** (Codex, revision 3, handoff 4baace78, 2026-10-20), with no remaining findings. Codex's implementation detail, kept: the refused source's structured outcome is retained even when the target succeeds (today the method returns nil then). This accepts the design only; the implementation and its gates keep their own review.

## 12. As built

Built on `feat/coverage-s3b`, rebased onto S11b's accepted head (3fa15cb), after the accepted design (360c1eb):

| Commit | What |
|---|---|
| 398b916 | Stage 1: `admission.Classify` (five outer envelopes in precedence order, a call failure's cause read after its quoted URL), `UIDRefusal` by the guards' wire shapes, the status corpus built with the upstream code at four versions; the GitOps verbs' UID guard (`refusedForUID`) reads `UIDRefusal`, never a substring |
| 558fede | Stage 2: candidates. S3a's rules keep exact identifiers apart from the display copy (`RuleIDs`, inexact and cut flags) and the policies' `matchPolicy`; `admission.Match` (the exact pass held to `rules.Matcher` at four versions, the Equivalent pass listed or counted) |
| 22b18b4 | Stage 3: every API-backed write path wraps each request's error (`admission.RequestError`); the bridge's `writeError` classifies it, applies the protected-subject rule, adds the candidates from the Observer's admission rows; the YAML panel's finding, the toasts' line with each step, the Activity entry's finding; the drift and dry-run sentences; the formatter's admission regexes removed |
| 5b6dbe9 | The smoke: a YAML apply refused by lab-s3b's webhook, a scale refused by its policy |
| 28399bb | The envelopes' exact shapes, a webhook's 404 and 409 denials, the ad-hoc refusal's finding |
| fdceb12 | The smoke edits the ConfigMap's data; lab-s3b's unreachable webhook is broken first too |
| 0419425 | The smoke watchdog allows 15 minutes and names the step the runner last began (§12.2) |
| 441ce8c, 1c83552, f1b8aad, b98e6e4 | The smoke: the YAML step types into the mounted editor and reports its footer when Apply stays disabled; a Flux resume refused by the unreachable webhook, confirmed through the confirm dialog's own button; only a new Activity entry counts |
| 0203a5e | The YAML editor's validator reads metadata's own keys only (§12.1, found by the smoke) |
| 9d420e5, edfe128 | gofmt; the watchdog's comment |
| bda2396 | The mutation gate's four survivors, each with a test (§12.3) |
| 8c4ffd9 | staticcheck S1016 in the matcher's oracle |

### 12.1 Where the build departs from the note

- **An attributed refusal is a definite refusal** (§2.1 said "only an attributed finding upgrades a 500"; the build applies it to every guard). `definiteRefusal` treats any attributed finding as a refusal, so a fail-closed webhook's call failure (500) reads "nothing was written"; any other 500 stays unverified.
- **A webhook's 409 is its denial, never an apply conflict.** The YAML editor's conflict branches (`crd.go`) read a Conflict as "the object changed since it was opened" or "apply conflict" only when it is not attributed; a webhook denying with 409 shows its finding.
- **The live diff names a refusal, without candidates.** `cachediff`'s `safeError` (S0b) now says `Finding.What()` for an attributed refusal ("denied by admission webhook <n>") and nothing for an unattributed one; its `webhookDenied` regex is gone. The live diff has no write result to carry candidates, so it lists none. S0b's test changed with it: its fixture was an Invalid whose message quotes a webhook denial mid-text, which the S3b negative rule no longer attributes; a real denial fixture (a HelmRelease) now carries the name.
- **The matcher says each rule's reason.** A rule whose value was not kept exactly reads "may match: a value could not be kept exactly" per rule, before the Equivalent reason, so the reason a candidate is listed is its own.
- **The alias count excludes what is listed** by name (`Candidates.Aliased`), so a webhook listed as a candidate is never also counted as an alias.
- **Requests on the admission configuration kinds are exempt.** No webhook or policy sees a request on `validatingwebhookconfigurations`, `mutatingwebhookconfigurations` or the policy and binding kinds (upstream's exemption); the finding says so and lists no candidates.
- **"No endpoints" reads "refused" on kind.** The lab's endpoint-less Service is refused at connect on kind (`connect: connection refused`), not "no endpoints available"; the classifier reads what the server sends, so the lab's call failure is category `refused`. The no-endpoints form stays in the corpus.
- **`ReconcileWithSourceSteps`** returns each step's outcome (`Step{Verb, Subject, Written, Err}`); `ReconcileWithSource` keeps its signature and wraps it. The source's refusal is kept even when the target is written.
- **An ad-hoc document keeps its refusal unexported** (`AdhocResult.refusal`, read through `Refusal()`), so the result's JSON shape is unchanged and the bridge converts it.
- **The YAML editor's dry run** says "passed admission and defaulting as of <time>; a later apply's admission may differ", the dry-run sentence of §4 on the editor as well as the diff.
- **The formatter's admission regexes are removed** (`formatKubernetesActionError`): an admission refusal's headline is the backend's finding.
- **No parity or load run.** S3b changes no verdict and adds no watch; the candidates read `HealthRowsNow`.
- **AI unchanged**, as §6 says: write errors never cross.
- **The YAML editor refused nearly every real object, and S3b fixes it** (outside the note; found by the smoke). Its local pre-check (`validateResourceYAML`, on `main` since 2026-06-30) matched every indented line under `metadata` against a key pattern without `/`, so a prefixed label or annotation (`app.kubernetes.io/name`, `klyx.dev/lab`), a finalizer or owner-reference item, and any top-level sequence (a Role's `rules`) read "YAML parse error" and kept Dry-run and Apply disabled. The YAML panel's finding, S3b's headline surface, could not be reached for a real object. Identity is now read from metadata's own keys only; nested lines are the server dry run's to validate, and an owner reference's `name:` no longer reads as the object's. A separate commit (0203a5e) with its test.

### 12.2 The lab and the smoke

lab-s3b (namespace `klyx-s3b-lab`, labelled `klyx.dev/lab=s3b`, with a teardown): agnhost's admission webhook (`/always-deny`) behind a real Service, a second Service with no endpoints, a ValidatingWebhookConfiguration with a denying webhook (ConfigMaps labelled `klyx.dev/s3b=webhook`, UPDATE) and an unreachable one (Kustomizations labelled `klyx.dev/s3b=unreachable`, UPDATE, `failurePolicy: Fail`), and a ValidatingAdmissionPolicy with a binding on `deployments/scale` in the lab's namespace. The smoke drives Klyx's own writes through each:

| Step | Klyx's write | Refused by | What Klyx says |
|---|---|---|---|
| A YAML apply | UPDATE ConfigMap `klyx-s3b-lab/lab-webhook` (server-side apply) | `deny.s3b.klyx.dev` (webhook-denied) | the panel's headline "update ConfigMap klyx-s3b-lab/lab-webhook was refused by admission before it was stored: denied by admission webhook "deny.s3b.klyx.dev"", the server's message, and the candidates with the named one marked |
| A scale | UPDATE `deployments/scale` `lab-frozen` | `klyx-s3b-freeze` with its binding (vap-denied) | the toast "Scale refused: denied by ValidatingAdmissionPolicy "klyx-s3b-freeze" (binding "klyx-s3b-freeze-binding")"; the Activity entry keeps the line and the candidates' names |
| A Flux resume | UPDATE Kustomization `lab-unreachable` (a merge patch) | `unreachable.s3b.klyx.dev`, whose Service has no endpoints (webhook-call-failed) | the toast "Resume refused: admission webhook "unreachable.s3b.klyx.dev" could not be called (its connection was refused)" |

At b98e6e4, after `relabs.sh` and `lab-s3b/apply.sh`: **208/208 in all four modes** (night and day, 1440 and 1024). The steps that had failed on the way told what was wrong: the editor's footer named its validator (§12.1); at 1024 the inspector is a drawer that is itself a `role="dialog"`, so the resume step clicks the button inside the confirm dialog (the one holding `confirm-detail`). The step records `typed: 1`: the editor never needed a second edit.

**Captures** (headless Chrome on the server-mode build, kind-klyx, night and day at 1440 and 1024; `pw/cov-s3b.mjs` in the session scratchpad): the YAML panel's finding, the scale's toast, the resume's toast and the Activity log, 16 of 16 with every expected text present and no horizontal overflow. The ConfigMap list is paged (100 loaded, "No ConfigMap instances match in the 100 loaded so far", Load more), so the capture opens the object through ⌘K's index.

**The watchdog.** The walk outgrew the smoke watchdog's five minutes. With nobody at the machine (`UserIsActive 0`) a step took about 1.4 s on average, consistent with throttled timers in an unwatched window, and a mode about five minutes at either width (S11b's run at 3fa15cb, about 5 minutes a mode, earlier runs under 2). S3b's added steps pushed it over: "incomplete 0/0", no step named. `ReportWithin` now allows 15 minutes: it bounds a runner that never reports, never the walk's pace. `SmokeService.Progress` records each step the runner begins (and logs it with its time), and an incomplete result names that step.

### 12.3 Mutations

45 mutations (35 Go, 10 frontend; `mut-s3b.py` in the session scratchpad), one at a time, each against its package's tests: the classifier's envelopes, causes and UID shapes; the matcher's scope, subresources, exemption, lost values, unread and alias counts, and the default policy; the exact identifiers; the bridge's protection, scrub, clip, inventory state, alias exclusion and the named candidate; the write paths' definite refusal, the 409 guard, scale's subresource, the source step and the composer's refusal; the live diff's name; the watchdog's step; and the frontend's toast line, refused steps, stored findings, five shown, unread inventory, drift and dry-run sentences, the YAML panel's finding and the editor's validator.

At b98e6e4, 38 were caught. Four survived, each closed by a test (bda2396): a message whose tail is one character off the policy's cause, a generic denial whose message is not its first cause, an Invalid on `metadata.name` (not the UID guard), and the clip, whose test compared against the constant it mutated. Three did not build, a fault of the harness (each removed an import's only use); rewritten, they are caught. At bda2396, **45 of 45 caught**.

### 12.4 Gates

At bda2396 (8c4ffd9 changes one test line for golangci-lint): bindings regenerate; `go vet ./internal/...` clean; `go test -race ./internal/...` passes; golangci-lint 0 issues at 8c4ffd9; `tsc --noEmit` clean; eslint 0 errors (the 4 warnings `main` has); vitest 2,416 passed (2 skipped). The native smoke (208/208, four modes) ran at b98e6e4 and the captures on that build; what follows changes tests and comments only. No parity or load run (§12.1).

## 13. Implementation revision 1: Codex's review of 8140c8c

Codex (handoff 1743d05b) requested changes: seven P2 findings, each with a probe. It found the YAML validator repair and the watchdog change justified and in scope.

| # | Finding | Fix |
|---|---|---|
| 1 | The legacy `Error` field kept the server's whole text for an unprotected subject: a credential and an unbounded denial crossed beside the scrubbed, clipped finding | An attributed refusal's legacy text (a result's, a step's and an ad-hoc document's) passes the finding's scrub and is bounded at 1,024 bytes (`boundedText`, `maxErrorText`); a protected subject keeps its withheld form, and an unattributed refusal keeps today's text |
| 2 | The scratch-delete bridge cast the reply to `{ok, error}`, dropping the finding | `deleteAdhocObject` passes `admission` through; a test drives the real bridge |
| 3 | The live diff said its dry run passed admission when objects failed or were skipped, and a skipped-only result "matches the live objects" | Only objects whose dry run completed (created, configured, unchanged, without an error) count: all completed keeps the sentence; a partial result says how many completed and that the rest say nothing of admission; none completed says nothing. A result with no completed dry run says "nothing was dry-run" or names the skipped objects |
| 4 | When both steps were refused, the Activity entry kept the target's finding only | The entry keeps every step's finding and the result's own, each request once |
| 5 | A step whose answer was lost (a timeout) read "not written" | `Step.Refused` (`definiteRefusal`) separates refused from unverified; the toast says "outcome unverified" for such a step and "not every step was confirmed" when any step is unverified |
| 6 | The generic policy envelope's correlation took the first same-name row's kind | Under the generic envelope every policy of the name is said, with "the refusal does not say which" when there are several; the kind is named only under the ValidatingAdmissionPolicy envelope |
| 7 | DeletePod read a webhook's NotFound denial as "it is already gone" | An attributed NotFound is the denial; the server's own NotFound is still the pod gone |
