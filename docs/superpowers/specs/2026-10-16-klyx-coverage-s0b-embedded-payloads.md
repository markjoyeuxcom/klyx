# Coverage S0b: embedded payloads

Parent: `2026-10-16-klyx-coverage-roadmap.md`. A preparatory fix before S1a, from Codex's S1a design review (finding 1, 2026-10-16): "do the common sanitization/editability invariant now, preferably as a preparatory fix, with adapters/tests for the known embedding surfaces." Revision 1 answers Codex's design review of this note at 2961228 (§9). Revision 2 answers the re-review at 64ddecf (§10); revision 3 the re-review at 9c3a1d9 (§11).

## 1. The hole

Klyx masks Secrets by kind: `MaskSecretData` runs only when the object itself is a core/v1 Secret, and the apply path refuses only a masked Secret's YAML. Several kinds carry Secret material *inside* their spec or status instead, and today it reaches the paths of §3.

**The registry,** keyed by group and kind. It applies whatever version is served, deprecated spellings included.

| Kind | Payload | Shape |
|---|---|---|
| ResourceSet (`fluxcd.controlplane.io`) | `spec.resources[]`, `spec.steps[].resources[]` | embedded objects; a core/v1 Secret among them carries `data`/`stringData` |
| ResourceSet | `spec.resourcesTemplate`, `spec.steps[].resourcesTemplate` | opaque text |
| ResourceSet | `spec.inputs[]` | free-form values |
| ResourceSetInputProvider | `spec.defaultValues`; `status.exportedInputs[]` (every value, `id` included: an ExternalService provider accepts any unique string as `id`) | free-form values |
| FluxInstance | `spec.kustomize.patches[].patch` | opaque text |
| Kustomization (`kustomize.toolkit.fluxcd.io`) | `spec.patches[].patch`, and the deprecated `spec.patchesStrategicMerge[]` and `spec.patchesJson6902[].patch` where an older served version still carries them | opaque text |
| Kustomization | `spec.postBuild.substitute` | free-form values |
| HelmRelease (`helm.toolkit.fluxcd.io`) | `spec.values` | free-form values |
| HelmRelease | `spec.postRenderers[].kustomize.patches[].patch`, and the deprecated `patchesStrategicMerge`/`patchesJson6902` there | opaque text |
| Application (`argoproj.io`) | **every ApplicationSource**, at each location that carries one (§1.1) | the source adapter |
| ApplicationSet (`argoproj.io`) | `spec.template.spec` sources (the source adapter); `spec.templatePatch` (opaque text); every generator's `template` override, at any nesting (matrix and merge); generator inputs (list `elements`, git and cluster `values`, plugin `input.parameters`) as free-form values | as stated |
| any kind above | the `kubectl.kubernetes.io/last-applied-configuration` annotation | a duplicate snapshot, stripped |

**1.1. The Argo source adapter.** An ApplicationSource is found at:
- `spec.source` and `spec.sources[]`;
- `status.history[].source` and `.sources[]`;
- `status.sync.comparedTo.source` and `.sources[]`;
- `status.operationState.operation.sync.source` and `.sources[]`;
- `status.operationState.syncResult.source` and `.sources[]`;
- the top-level `operation.sync.source` and `.sources[]` (a pending operation);
- an ApplicationSet's template spec.

**Inline manifests:** `operation.sync.manifests[]` and `status.operationState.operation.sync.manifests[]` are inline manifest strings, and are treated as **opaque**. A manifest can contain a Secret directly.

Within each source:
- `helm.values` (a string) is opaque.
- These are free-form:
  - `helm.valuesObject`, `helm.parameters[].value` and the `helm.fileParameters` values;
  - `plugin.env[].value` and `plugin.parameters[]` (`string`, `array` and `map` values);
  - `directory.jsonnet.extVars[].value` and `directory.jsonnet.tlas[].value`.
- `kustomize.patches[].patch` is opaque.

Reference descriptors stay readable and are never fetched: `helm.valueFiles` paths, `directory.jsonnet.libs` paths, `repoURL` (sanitised as a URL, §2), `path`, `chart`, `targetRevision`.

**References are not payloads.** These stay readable and are never fetched or revealed:
- HelmRelease `valuesFrom`;
- Kustomization `postBuild.substituteFrom`;
- `secretRef`, `certSecretRef` and `pullSecret` names;
- file paths.

## 2. The sanitiser

**`crd.SanitizeEmbedded(gk, obj) (Sanitized, error)`** is one backend function: non-mutating (it returns a copy) and idempotent. It returns the sanitised copy with its policy metadata:
- the paths it masked or omitted;
- `EditEligible`: false when the sanitiser **changed anything** in the copy it was given. That covers a masked, omitted or withheld payload, and equally a URL whose user info it removed, on any kind;
- `DescribeMode`: `withheld` for a registered payload kind, otherwise `url-scrubbed` (§4).

**What the sanitiser runs on:**
- **Every kind:** the URL rule, on every string leaf. A kind does not need to be registered for its credential-bearing URLs to be removed: a GitRepository's `spec.url`, a HelmRepository's, an AppProject's `sourceRepos`, any CRD's url field.
- **Registered kinds:** the payload adapters, in addition.

| Shape | Treatment |
|---|---|
| Embedded core/v1 Secret | `data` and `stringData` values become `<masked>`, keys kept, through the existing `MaskSecretData` |
| Other embedded object | kept, and sanitised by its own kind's adapter when it has one |
| Opaque text | replaced by `<omitted: may carry secrets>`. **No byte count:** a length is information, and re-running on a placeholder must not change it. Siblings (a patch's `target`) are kept |
| Free-form values | the key structure is kept and every leaf value becomes `<omitted>`, whatever its type (string, number, bool). A list keeps its length as `[<omitted> × n]` |
| URL (every display string, §3) | user info is **removed** from absolute URLs, keeping only the exact password-free user `git`. SCP-style `git@host:path` is parsed as SSH syntax (user `git` kept, any other user removed). Percent-encoded user info is decoded before the check. An unparseable URL in a URL field is withheld (`<withheld: unparseable URL>`), never echoed. Secrets in a URL's path or query are out of scope, and said so |
| `last-applied-configuration` | stripped |

**Failure withholds, never leaks.** A payload deeper than 64 levels, larger than 1 MiB, or of an unexpected type is withheld whole (`<withheld>`), with the path recorded in the metadata. The sanitiser never returns raw content on failure.

**Raw originals stay private.** A backend operation that needs the raw value (a dry-run, a diff's comparison, an apply's precondition) reads it inside the backend and never serialises it. Display copies, and every string, link or cell derived from them, come from the sanitised copy only.

## 3. The outbound-path matrix

Every path that can carry a registered kind's data or a URL across the bridge, or into a prompt:

| Path | Code | Treatment |
|---|---|---|
| Generic detail: YAML, `fieldsJSON`, annotations, labels, facts, managers, owners | `fleet.GetInstanceDetail` | built from the sanitised copy only |
| Generic detail: Summary cells | `objectWithSummary` | for a registered payload kind, computed locally from the detail's own sanitised object, by the same rule as list cells; server cell text is discarded. Other kinds pass the URL rule |
| Generic list cells | `ListInstances` → `crd.FromTable` | **the cell rule.** For every kind, string cells pass the URL rule. For a registered payload kind, **server cell text is never released**:<br>• the list requests its page with `includeObject=Object`;<br>• each row's object passes the sanitiser first;<br>• the cells are computed locally, by evaluating each printer column's JSONPath (client-go `util/jsonpath`) against the **sanitised** object, and formatted by the column's type;<br>• the column definitions come from the CRD for the served version, through the slice 15c definition reader, cached. They choose presentation (name, type, priority, description) and the selector, but authorise no bytes. A definition that is stale, or changed between reads, can at worst show a wrong value, never protected data, because every value is read from sanitised data.<br>• **Fallbacks:** an unreadable definition, a selector that fails to evaluate, or a page without objects gives no custom cells for that kind (name, namespace and age only). The server's cells for these kinds are discarded unread |
| Condition messages and reasons, event messages | the generic detail, the Flux and Argo rows and inspectors, the events lens | the URL rule (a controller can echo `unable to clone 'https://user:token@…'`) |
| Flux source rows | `FluxSourceDTO.URL` from `sourceFields` | the URL rule |
| Flux Git links | `GitLinkDTO.URL`/`CopyText` from `ResolveGitLink` | built from the sanitised remote |
| Flux detail and inventory | `flux.ParseDetail` | conditions by the URL rule; no payload fields carried |
| Argo rows and records | `argo.go` `RepoURL` in the application, history, compared and ApplicationSet records; `promotion.go` `SourceName`; The Line's units (`line/units.go`, display strings) | the URL rule. Repository identity keys used internally (`NormalizeRepo`) are computed from the raw URL but never displayed raw |
| Copy affordances | copy YAML, copy URL, copy command | copy from the sanitised strings only |
| AI Explain and Suggest-fix bundles | `ai_service` (`redactManifestForAI`), `ai_evidence.go` | the manifest is the sanitised YAML; facts and evidence come from the sanitised copy |
| Chat tools and CLI snapshots | `chat_project.go` | the sanitiser runs before the structural projection |
| Cache diffs | `internal/cachediff` | §6 |
| `flux diff` (M10's on-demand CLI diff) | `fluxcli.DiffKustomization` | *revisions 5-6:* from the first payload kind's block on, the rest of the output is withheld (flux's text cannot be split safely after it); flux's error text is withheld unless it names a protected subject, which keeps only subject and reason; Go's own runner errors and lines before any payload block pass the URL rule |
| The Line's Git sources | `gitSources` → `GitService` | *revision 5:* the raw URL is unexported, used only to match and to suggest a credential-free URL; shown scrubbed |
| Argo projects | `ArgoService.ListProjects` | *revision 5:* the URL rule on `sourceRepos` and destinations |
| Describe | `ExecService.Describe` | §4 |
| Apply | `fleet.ApplyResourceYAML` | §5 |

## 4. Describe

*Revision 4 (found by the native smoke) replaces the first mode below; see §13.* Two modes, chosen by kind:
- **`withheld` (registered payload kinds), regardless of the object's current content.** *Superseded by §13: a payload kind is now described inside a version bracket, and withheld only when it carries a payload or the bracket fails.*
  - kubectl is not run, and neither stdout nor stderr is returned.
  - The result carries `Withheld` with the reason, for example: "Describe is withheld for a ResourceSet: kubectl's text would include its templates, patches or values, which can carry secrets. The Manifest view shows a sanitised copy."
  - The sheet shows the reason and offers no Copy of a report.
- **`url-scrubbed` (every other kind).** kubectl runs as today. Both stdout and stderr pass a conservative text policy before anything is returned. It treats an incomplete or ambiguous URL as credential-bearing, never as proof of safety (kubectl truncates annotation lines to 138 bytes plus `...`, so a URL can lose its `@`).
  - **A candidate** is either:
    - a token starting with a scheme (`[A-Za-z][A-Za-z0-9+.-]*://`), whose authority runs to the first `/`, `?`, `#`, whitespace or end of line. RFC 3986 user info may hold `'`, `,`, `;`, `(` and `)`, so none of those ends it (revision 5); or
    - an SCP-style token, `user@host:path`, with no scheme.
  - **Decoding:** the authority is percent-decoded before every check.
  - **A complete authority with `@`:** the user info is everything before the **last** `@`. It is removed, unless it is exactly the password-free `git` after decoding.
  - **An SCP-style token:** a user other than `git` is removed.
  - **Treated as credential-bearing, with the authority replaced by `<redacted>`:**
    - a token cut by a truncation marker (`...` or `…`) at or before the end of its authority;
    - an authority without `@` but with `:` followed by anything other than a numeric port;
    - a token that reaches the end of a line where kubectl wraps or truncates.
  - **A candidate that cannot be safely delimited** (unbalanced brackets in an IPv6 authority, control characters) withholds its whole line, replaced by `<line withheld: possible credential>`. There is no raw fallback.
  - **Labels:** when anything was removed or withheld, the result says "user info removed from URLs" (with the count of lines withheld), and it is no longer called kubectl's exact output. When nothing was, the existing exact-output contract is unchanged.

**Classification** uses the group and kind that the existing validated discovery path resolves from the requested resource, so a singular, plural or short-name alias cannot slip past it. A failed or forbidden classification withholds; it never falls through to raw output.

**The kubeconfig-pinned copy command** stays in both modes, so the user can run kubectl themselves, knowingly. Klyx never runs it for a withheld kind.

## 5. Apply

For **every** kind (dry-run and real apply alike), the backend:
1. reads the live object; a failed or missing read refuses (the inspector's apply never becomes a create);
2. checks the live object's UID against the UID the inspector showed, and refuses on a mismatch;
3. runs the sanitiser over **both** the submitted document and the live object, and refuses if either is not edit-eligible, that is, if the sanitiser would change anything in it (a payload, or a URL's user info):
   - for a payload kind: "This <kind> carries templates, patches or values that Klyx shows sanitised; edit it in Git";
   - for a URL: "This <kind> has a URL with credentials, which Klyx shows without them; edit it in Git".

   Any `<masked>`, `<omitted…>` or `<withheld…>` placeholder anywhere in the submitted document also refuses;
4. otherwise applies with the live `resourceVersion` set in the submitted object as an atomic precondition, so a change between the check and the write fails the apply instead of racing it.

The frontend hides Edit for an object whose sanitiser metadata says it is not edit-eligible, with the same reason. The backend refusal holds whatever the frontend does. Authoring a new payload on an empty object would need its own unsanitised-input path with provenance, and is not offered.

## 6. Cache diffs

- **Compare raw first, inside the backend.** For each registered section, the raw live and dry-run values are compared before sanitising. Klyx records only whether the section changed: never a digest, never a length.
- **Then sanitise both sides and diff the copies.** A changed section reads "this section changed (omitted: may carry secrets)", including changes that sanitise to identical text (equal lengths, value-only changes to an embedded Secret, scalars and lists). An unchanged omitted section next to an unrelated visible change carries no notice.
- **Untouched:** ssa's own action result (configured, unchanged, created) is kept as it is, and the dry-run always uses the real desired object, never a sanitised one.

## 7. Tests

- **Bridge needles.** A unique plain marker (no scrubber pattern) is placed in every payload leaf, in varied leaf types (string, number, bool, list, map), including:
  - an opaque template;
  - a targeted JSON patch;
  - a nested `last-applied` snapshot;
  - a secret under an ordinary key;
  - an embedded Secret in a ResourceSet step;
  - an ExternalService exported input whose `id` is the marker;
  - an Argo Application with a clean spec but a historical source in `status.history`;
  - an ApplicationSet generator override, a `templatePatch`, and plugin parameters under ordinary keys;
  - URLs with user info, percent-encoded user info, SCP-style SSH, and a malformed URL;
  - an Argo pending top-level `operation` with a source, inline `manifests[]` in both locations, and Jsonnet `extVars` and `tlas` at each source location;
  - a GitRepository (no registered payload) whose `spec.url` carries credentials.

  The serialised bridge result of each path in §3 is searched for every marker:
  - the generic detail DTO, as JSON;
  - list cells and Summary cells;
  - `FluxSourceDTO`, Git links and Argo records;
  - events and conditions;
  - chat tool results and CLI snapshots;
  - the full Explain and Suggest-fix prompt bundles, after every cap and scrubber;
  - cache diffs;
  - Describe results;
  - refusal and error messages.

  Only the explicitly retained public keys and references are exempt.
- **The sanitiser:** idempotence (a second run is byte-identical), non-mutation of its input, and withholding on the depth, size and type limits.
- **Describe:**
  - withheld by kind for every registered payload kind and alias, and when classification fails or is forbidden, with no stdout or stderr returned;
  - url-scrubbed for a credential-bearing GitRepository: the marker user info is absent from both stdout and stderr, and the result is labelled;
  - kubectl's real annotation truncation: an annotation holding a credential URL longer than 138 bytes, described by the pinned kubectl and scrubbed, with no credential byte left;
  - malformed and truncated authorities, SCP-style user info, multiple `@`, percent-encoded user info, an IPv6 authority, and each of these on stderr as well as stdout;
  - unchanged output for an object without credentials.
- **Apply,** calling the backend directly:
  - placeholders submitted under a registered field;
  - a live surface removed after the inspector's read while the submitted document still holds placeholders;
  - a surface added between the check and the write (the `resourceVersion` precondition fails);
  - a replaced UID;
  - a failed live read;
  - an interval-only edit of a GitRepository whose URL carries credentials (refused, so the credentials survive);
  - a ResourceSetInputProvider whose only sensitive field is its URL (refused).
- **Cache diffs:** a changed omitted section with identical sanitised text; an unchanged omitted section beside a visible change; ssa's action preserved.
- **Cells** (for a protected kind, list and Summary alike):
  - a column whose definition changes between the definition read and the Table read, from `.status.phase` to `.spec.inputs[0].value`, keeping the same name, position, type, priority and a custom description: no payload marker appears, because cells come from the sanitised object;
  - an ancestor selector (`.spec`): it evaluates over sanitised data, so the omitted placeholders show, never the payload;
  - a known-safe scalar column, which shows its value;
  - a definition that cannot be read: no custom cells;
  - the server's cells for a protected kind are never read into the DTO (a needle placed only in the server Table does not appear).
- **Lab:** the `with-secret` ResourceSet and a Kustomization with a patch on `kind-klyx` carry markers. A native smoke step asserts no marker in the inspector's DOM, Describe withheld and Edit absent. The DOM check is supplementary; the bridge needles are the proof.

## 8. Not in this slice

- A reveal flow for omitted payloads.
- Detecting arbitrary secrets in every CRD: the registry covers the known embedding surfaces, and a new kind joins it by adapter.
- Secrets in URL paths and queries.
- Changing how Secrets themselves are masked.

## 9. Revision 1: Codex's design review of 2961228

| # | Finding | Where answered |
|---|---|---|
| 1 | Outbound bypasses: bespoke Flux and Argo DTOs, list and Summary cells, links and copies, AI evidence | §3's outbound-path matrix, the cell rule, and the URL rule applied to every display string |
| 2 | A live pre-read cannot guard Describe | §4: withheld by kind, classification through validated discovery, nothing returned |
| 3 | A live-only apply check lets a stale sanitised document round-trip | §5: both documents classified, placeholders refused, UID and `resourceVersion` preconditions, failed reads refuse |
| 4 | Argo keeps sources in status and operation records; ApplicationSet has more payloads | §1.1: every source-bearing location; `templatePatch`, generator overrides and inputs; plugin env and parameters; deprecated Kustomization spellings |
| 5 | An exported input's `id` is free-form | §1: every exported value omitted, `id` included; only an ordinal and count shown |

Also adopted from the review's other rulings: user info removed rather than masked, with SCP-style parsing, percent-decoding and malformed URLs withheld; the sanitiser's policy metadata, idempotence and withhold-on-failure; raw-first comparison in the cache diffs without digests; and needle tests at the serialised bridge result, not the DOM.

## 10. Revision 2: Codex's re-review of 64ddecf

| # | Finding | Where answered |
|---|---|---|
| 1 | URL-only kinds escaped the edit and Describe guards (a GitRepository's credential URL would be erased by an unrelated edit, and printed by raw Describe) | §2: `EditEligible` is false whenever the sanitiser changes anything, the URL rule runs on every kind's string leaves; §5: apply covers every kind; §4: Describe is `url-scrubbed` text for every non-payload kind, both streams, without a pre-read |
| 2 | Argo's top-level `operation`, inline `manifests[]`, and Jsonnet `extVars`/`tlas` | §1.1 |
| 3 | A Table description is not JSONPath provenance; an ancestor selector serialises protected subtrees | §3's cell rule: authoritative CRD printer columns per served version, disjointness that excludes ancestors, fail-closed selectors and unreadable definitions |

## 11. Revision 3: Codex's re-review of 9c3a1d9

| # | Finding | Where answered |
|---|---|---|
| 1 | An `@`-dependent matcher is not fail-closed: kubectl truncates annotation lines at 138 bytes, so a credential URL can lose its `@` | §4's conservative text policy: candidate delimiting; truncated, port-less-colon and end-of-line authorities redacted; undelimitable lines withheld; real-truncation regression tests |
| 2 | Cached CRD columns are not bound to a later Table's selector | §3: for protected kinds, server cell text is never released. Cells are evaluated locally over the sanitised object (`includeObject=Object`), so a stale definition can only show safe values |

## 12. As built

The design was accepted at 30147ae. The build is five commits: 9260be4 (stages 1-2), b310576 (3-5), bfce835 (6-7), 87bc1df (8), and a follow-up putting the URL rule on chat list rows.

**Stages 1-2: the policy and the sanitiser.**
- `internal/urlscrub` is the text policy of §4.
  - Candidates are scheme tokens, with the authority ending at `/ ? #`, whitespace, a quote, a comma, a semicolon or a bracket, and SCP-style tokens.
  - The authority is percent-decoded, and the user info is everything before the last `@`, kept only when it is exactly `git`.
  - Redacted: a truncation marker, a port-less colon, an unbalanced IPv6 authority. An undelimitable candidate withholds its line.
  - `Field` withholds a whole URL value that cannot be delimited. The policy is idempotent.
- `crd.SanitizeEmbedded` is the registry of §1 and §1.1 plus the URL rule over every string leaf.
  - `EditEligible` ignores only the removal of the last-applied snapshot (an apply never sends it).
  - Paths quote keys holding `.`, `[` or `"`, and `crd.ValueAt` reads them back.
  - Placeholders: `<omitted: may carry secrets>`, `<omitted>`, `<omitted> × n` for a list of scalars, `<withheld>` and `<masked>`.
- **A correction to §1.1:** Argo's `helm.fileParameters` entries are `{name, path}`, and `path` is a file reference, so it stays readable (not free-form).

**Stages 3-5: the detail, Describe and the apply.**
- **`GetInstanceDetail`:**
  - sanitises before any DTO field, with conditions now read from the sanitised object;
  - passes event messages through the URL rule;
  - carries `EditEligible` and `DescribeMode`.
- **Protected kinds' lists:**
  - `readTable(..., wholeObjects)` asks for `includeObject=Object`;
  - `crd.LocalPage` evaluates each printer column with client-go `util/jsonpath` over the sanitised row object, using the server's own coercions (`cellForJSONValue`, `ConvertToHumanReadableDateType`);
  - the server's cells are discarded unread.
- **Printer columns** are cached for one minute per CRD and version (`payloadColumns`), for presentation only.
- **Protected kinds' Summary** is evaluated the same way. Other kinds' cells pass the URL rule.
- **Describe:**
  - `DescribeKind` resolves the resource argument through a discovery RESTMapper with the short-name expander, trying `resource.version.group` and then `resource.group`, as kubectl does;
  - a failure, or a payload kind, is withheld before any command is built;
  - otherwise both streams are scrubbed, and `Scrubbed` and `LinesWithheld` label the result.
- **The apply,** `ApplyResourceYAML(…, expectedUID, …)`:
  - refuses placeholders and a failed or missing read (it never creates), and refuses a replaced UID;
  - refuses unless both documents are edit-eligible;
  - sets the live `resourceVersion` as the apply's precondition;
  - a precondition conflict reads "changed since it was opened".

**Stages 6-7: diffs, DTOs and chat.**
- **Cache diffs:** `omittedChanges` compares raw values at every sanitiser path of either side before the diff of sanitised copies, and `OmittedChanged` reaches the DTO and the rendered text.
- **The URL rule** now also covers:
  - `FluxSourceDTO.URL`, Flux condition messages, Git links (the remote is scrubbed before `normalizeRemote`, which cut at the first `@`);
  - Argo repo URLs, condition and operation messages;
  - The Line's chart URLs and promotion source names;
  - every event message across the bridge (generic, pods, nodes, events lens, Karpenter, ESO).
- **Chat:** the get tool sanitises before its projection, and list rows pass the URL rule.

**Stage 8: the frontend.**
- An object that is not edit-eligible opens a read-only `SanitisedManifest` with the reason; dry-run and apply send the inspector's UID.
- The Describe sheet shows a withheld reason (Copy disabled, the command offered), and a scrubbed caption.

**Tests:**
- **Bridge needles:** the serialised detail DTO (a ResourceSet; a credential GitRepository; an Argo Application with a pending operation, inline manifests, a historical source and Jsonnet `extVars`), the chat get result, and the full Suggest-fix prompt.
- **Protected-kind local cells:** a needle only in the server Table, and a column changed to a protected path.
- **Cache-diff omitted changes:** the same-length template change, a Secret value-only change, and an unchanged section beside a visible change.
- **The apply guard:** the credential-URL interval edit, placeholders, a replaced UID, a missing object, the `resourceVersion` precondition, and a payload kind with a stale baseline.
- **Describe:** withheld for a payload kind and when unclassified, both streams scrubbed, exact when clean.
- **The URL policy:** units, kubectl's 138-byte cut at four points, and **kubectl's real describe output** captured from the lab (`urlscrub/testdata/describe-gitrepository.txt`).
- **The gate:** vitest 2110 passed; `go test ./internal/...` passes.

**Lab and smoke:**
- **Fixtures** (`scratchpad/lab-s0b/apply.sh`, `lab-s1a/apply.sh`): the `with-secret` ResourceSet, a Kustomization `patched` with a patch and a substitute, and a GitRepository `creds-repo` with a token URL and a 150-byte credential annotation.
- **The native smoke step "embedded payloads stay out":**
  - no marker reaches the page or the detail;
  - the Manifest is read-only;
  - Describe is withheld for the ResourceSet and the Kustomization;
  - the GitRepository's real report is scrubbed.

## 13. Revision 4: payload kinds are described inside a version bracket

**Found by the native smoke after the build.** Withholding Describe for every registered kind, whatever its content, removed Describe from every Flux Kustomization, HelmRelease and Argo Application. Those are the GitOps objects Klyx shows most, and slice 14's Describe-in-every-inspector lost them. The smoke step "Describe from the Flux inspector" failed on a Kustomization that carries no patch at all.

**The protocol** is the fail-closed snapshot Codex named as acceptable in its review of 2961228 ("specify and prove a fail-closed snapshot/version protocol that buffers and discards BOTH stdout and stderr on uncertainty"). For a registered payload kind:
1. Read the object through the API (`DescribeSnapshot`). Withhold if the read fails, or if the sanitiser finds a payload (`Sanitized.Payload`: a non-empty registered surface; URL user info alone is not a payload).
2. Run kubectl describe, buffering stdout and stderr.
3. Read the object again. Release the report only if the UID and `resourceVersion` are unchanged and there is still no payload. Otherwise both buffers are discarded and Describe is withheld, saying the object changed while it was described.

**Why it holds:**
- Every write to an object changes its `resourceVersion`, and a deletion followed by re-creation changes its UID.
- Equal UID and `resourceVersion` before and after mean no write happened in between, so kubectl's own read saw exactly the payload-free version Klyx checked.
- kubectl's generic describer, used for these CRDs, prints the object and its events. The events, like the rest, pass the URL text policy.
- The `last-applied-configuration` annotation is skipped by kubectl describe itself.

**Unchanged:**
- non-payload kinds are URL-scrubbed;
- an unclassifiable resource is withheld;
- the copy command is offered in every case.

**Also:**
- **The detail's `DescribeMode`** now follows the object: `withheld` when it carries a payload, else `url-scrubbed`.
- **Secrets:** a Secret with data counts as a payload, so its Manifest is read-only, with "Klyx shows this Secret with its values masked." The apply already refused masked Secret YAML, so nothing changes in practice except that the editor is no longer offered.

**Tests** (`TestDescribe_PayloadKindBracket`):
- clean and unchanged: released;
- `resourceVersion` changed, UID replaced, a payload appearing, or the second read failing: each discarded;
- the first read failing, or a payload present: withheld, with kubectl not run.

## 14. Revision 5: Codex's implementation review of 84de836

| # | Finding | Fix | Tests |
|---|---|---|---|
| 1 | Sub-delimiters `' , ; ( )` ended the authority, so a valid credential URL passed | The authority runs to a hard end (`/ ? #`, space, characters a URL cannot hold). With an `@` anywhere in it, user info runs to the last one. Without one, the host ends at the first sub-delimiter, and a span that fails there or holds another `:` is redacted whole. A cut span is redacted whole | `TestUserinfoSubDelimiters` (Field, Text, kubectl cut), `TestSoftEndsWithoutUserinfo` |
| 2 | The Line's Git sources and Argo projects carried raw URLs | The Line keeps the raw URL in an unexported field for matching; Argo projects apply the URL rule to repos and destinations | `TestS0bLineGitSourcesAndArgoProjects` |
| 3 | A refused dry-run's text could echo a payload | For a payload kind or a Secret, `safeError` keeps only the status reason and code, each cause's type and field path, and a denying webhook's name. **Also, found while fixing this:** the M10 `flux diff` CLI path was missing from §3's matrix. Its output carries changed values, so a payload kind's block body is withheld and protected subjects' errors are reduced the same way | `TestDryRunErrorKeepsOnlyStructuredFacts`; `fluxcli` `TestSanitizeDiffWithholdsPayloadBlocks`, `TestSanitizeErrorWithholdsProtectedSubjects`, `TestDiffKustomizationSanitises` |
| 4 | kubectl re-resolved the alias and read mutable kubeconfig files, so the bracket did not prove what kubectl read | A bracketed describe runs `DescribePinnedCmd`: the resolved `resource.version.group`, and a kubeconfig snapshot (this context, minified and flattened, `0600` in a private temp dir, removed afterwards) used only after it resolves to the same server, TLS trust, token, basic auth, impersonation, exec plugin, auth provider (ignoring cached tokens) and proxy as the bracket's connection. Drift withholds, naming the field, never a value. Empty UID or `resourceVersion` withholds before kubectl runs | `TestDescribeBracketIsPinned`, `TestDescribePinnedCmd`, `TestKubeconfigSnapshotRefusesDrift` |
| 5 | The expected UID was optional | Required for dry-run and apply. The live UID must be non-empty and equal, and is set on the applied object | `TestApplyResourceYAML_S0bGuard` |
| 6 | Discovery ignored cancellation | Discovery runs under the caller's context (a context-carrying transport when a REST config exists), and the caller returns at the context's end | `TestDescribeTargetHonoursCancellation` |
| 7 | `directory.jsonnet` and `input.parameters` were joined as single keys | Segments are joined one at a time. Every recorded path must resolve against the input | `TestRecordedPathsResolveAgainstTheInput`, `TestOmittedChangesNestedSegments` |
| 8 | Unexpected types returned raw, and there was no 1 MiB bound | Every step to and at a protected surface checks its type, and a non-empty value of another type is withheld in place. A surface over 1 MiB, measured by a walk that stops once over, is withheld whole. The whole-object copy is linear in what the caller already holds, which the API server's request limit bounds | `TestUnexpectedTypesAreWithheld` (17 shapes), `TestOversizedSurfaceWithheld` |
| 9 | A Secret's policy was computed after masking | The sanitiser reads the original object; key info travels separately | `TestS0bSecretDetailPolicy` |
| 10 | A withheld refresh kept the last report and its Copy | A withheld reply is never a failed refresh: the report goes, Copy is disabled, and Copy command stays | `DescribePane` "clears the last report when a refresh is withheld" |
| note | Last-applied is excluded from eligibility, but the original was sent | The apply strips `kubectl.kubernetes.io/last-applied-configuration` before writing | `TestApplyResourceYAML_S0bGuard` |

**Also fixed:** `child_test.go` ran the confined build's `RunChild` in-process, which replaces the process's default HTTP transport and resolver as the real child must. It now restores both, so later tests in the package can reach their local API servers. Production is unaffected: the override only ever runs in the separate child process.

**Not adopted as written:** the probe `TestReviewS0bDescribePinsResolvedVersion` reads the pinned argument from `DescribeCmd`. The pinned run is now its own method, `DescribePinnedCmd`, because it also carries the kubeconfig snapshot; `TestDescribeBracketIsPinned` asserts the same pin on that method. The other twelve probes pass as written.

## 15. Revision 6: Codex's re-review of 293989b

| # | Finding | Fix | Tests |
|---|---|---|---|
| 1 | An embedded or core Secret whose `data` or `stringData` is not a map kept its value raw | `MaskSecretData` withholds a non-empty container of another type; the sanitiser marks it, so the object is payload-bearing, not editable, and Describe is withheld | `TestSecretContainersOfUnexpectedType` (string, list and number; embedded and core), `TestS0bEmbeddedSecretOfUnexpectedTypeBridge` |
| 2 | A values key can carry a line that looks like a `flux diff` header and end the withholding (confirmed with flux v2.9.6) | Withholding is sticky: from the first payload block's header, the rest of the output is withheld, with a note saying why | `TestSanitizeDiffForgedHeaderInBody` on Codex's captured real output (`testdata/diff-forged-header.txt`), `TestSanitizeDiffWithholdsPayloadBlocks` |
| 3 | A build error echoes an inline patch before naming any object | flux's error text is withheld whole unless it names a protected subject (then subject and reason only). Only Go's own runner errors, with no flux text, are kept | `TestSanitizeErrorBuildEchoesPatch` on Codex's captured real stderr (`testdata/build-error-patch.txt`), the updated real-error and partial-output tests |
| 4 | A literal `@` before a `%40` left user info behind | The separator is the later of the last literal `@` and the last `%40` | `TestMixedLiteralAndEncodedAt` |
| 5 | Describe's early refusals and errors skipped the URL rule | Every text a describe returns passes the rule, labelled as scrubbed | `TestS0bDescribeEarlyFailuresScrubbed` (discovery, snapshot, pin, argv, command) |

**Trade-off stated:** findings 2 and 3 make the `flux diff` CLI path less informative. A diff that touches a payload kind ends at that block, and flux's own error text is no longer shown. Klyx's cache diff (§6), which compares structured objects, is unaffected and remains the full view. The CLI message points to the terminal for the raw text.

