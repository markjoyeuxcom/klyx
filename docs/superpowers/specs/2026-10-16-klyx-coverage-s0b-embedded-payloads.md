# Coverage S0b: embedded payloads

Parent: `2026-10-16-klyx-coverage-roadmap.md`. A preparatory fix before S1a, from Codex's S1a design review (finding 1, 2026-10-16): "do the common sanitization/editability invariant now, preferably as a preparatory fix, with adapters/tests for the known embedding surfaces." Revision 1 answers Codex's design review of this note at 2961228 (§9). Revision 2 answers the re-review at 64ddecf (§10).

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
| Generic detail: Summary cells | `objectWithSummary` from the server's Table | each cell passes the cell rule below |
| Generic list cells | `ListInstances` → `crd.FromTable` | **the cell rule.** For every kind, string cells pass the URL rule. For a registered payload kind, each cell additionally needs proven provenance:<br>• **Where the selector comes from:** the authoritative printer columns of the served version, from the CRD itself (the definition reader of The Line slice 15c, cached per cluster, CRD and version), matched to the Table's columns by name and position. Never the Table's description text, which a CRD author can override verbatim.<br>• **When a cell is kept:** only when its selector is a simple field path proven **disjoint** from every protected subtree, so that it is neither a descendant nor an **ancestor** of one (a column selecting `.spec` would serialise the whole subtree).<br>• **When a cell is omitted:** wildcard, recursive, union or filter selectors (except the generated condition-status selector); a definition that cannot be read; and any cell whose provenance is not proven. All omit as `<omitted>`, failing closed |
| Condition messages and reasons, event messages | the generic detail, the Flux and Argo rows and inspectors, the events lens | the URL rule (a controller can echo `unable to clone 'https://user:token@…'`) |
| Flux source rows | `FluxSourceDTO.URL` from `sourceFields` | the URL rule |
| Flux Git links | `GitLinkDTO.URL`/`CopyText` from `ResolveGitLink` | built from the sanitised remote |
| Flux detail and inventory | `flux.ParseDetail` | conditions by the URL rule; no payload fields carried |
| Argo rows and records | `argo.go` `RepoURL` in the application, history, compared and ApplicationSet records; `promotion.go` `SourceName`; The Line's units (`line/units.go`, display strings) | the URL rule. Repository identity keys used internally (`NormalizeRepo`) are computed from the raw URL but never displayed raw |
| Copy affordances | copy YAML, copy URL, copy command | copy from the sanitised strings only |
| AI Explain and Suggest-fix bundles | `ai_service` (`redactManifestForAI`), `ai_evidence.go` | the manifest is the sanitised YAML; facts and evidence come from the sanitised copy |
| Chat tools and CLI snapshots | `chat_project.go` | the sanitiser runs before the structural projection |
| Cache diffs | `internal/cachediff` | §6 |
| Describe | `ExecService.Describe` | §4 |
| Apply | `fleet.ApplyResourceYAML` | §5 |

## 4. Describe

Two modes, chosen by kind with no pre-read, so no race:
- **`withheld` (registered payload kinds), regardless of the object's current content.**
  - kubectl is not run, and neither stdout nor stderr is returned.
  - The result carries `Withheld` with the reason, for example: "Describe is withheld for a ResourceSet: kubectl's text would include its templates, patches or values, which can carry secrets. The Manifest view shows a sanitised copy."
  - The sheet shows the reason and offers no Copy of a report.
- **`url-scrubbed` (every other kind).** kubectl runs as today, and **both stdout and stderr** pass the URL rule as text before anything is returned.
  - Every `scheme://userinfo@` in the text loses its user info (except the exact `git`).
  - The rule matches the pattern in prose and does not need to parse the URL, so it fails closed.
  - When anything was removed, the result says "user info removed from URLs" and is no longer called kubectl's exact output. When nothing was removed, the existing exact-output contract is unchanged.

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
- **Cells:**
  - a CRD column whose author-written description mimics the generated JSONPath text for `.status.phase` while selecting `.spec.inputs[0].value` (omitted, because provenance comes from the CRD, not the description);
  - an ancestor selector (`.spec`) (omitted);
  - a wildcard selector (omitted);
  - a known-safe scalar column (kept);
  - a definition that cannot be read (every cell omitted for that kind);
  - the same set in the detail's Summary.
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

