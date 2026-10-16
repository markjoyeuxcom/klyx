# Coverage S0b: embedded payloads

Parent: `2026-10-16-klyx-coverage-roadmap.md`. A preparatory fix before S1a, from Codex's S1a design review (finding 1, 2026-10-16): "do the common sanitization/editability invariant now, preferably as a preparatory fix, with adapters/tests for the known embedding surfaces."

## 1. The hole

Klyx masks Secrets by kind: `MaskSecretData` runs only when the object itself is a core/v1 Secret, and the apply path refuses only a masked Secret's YAML. Several kinds carry Secret material *inside* their spec instead, and today it reaches every raw path:

| Kind | Where the payload sits | Shape |
|---|---|---|
| ResourceSet (`fluxcd.controlplane.io`) | `spec.resources[]`, `spec.steps[].resources[]` | embedded objects; a core/v1 Secret among them carries `data`/`stringData` |
| ResourceSet | `spec.resourcesTemplate`, `spec.steps[].resourcesTemplate` | opaque template text |
| ResourceSet | `spec.inputs[]` | free-form values |
| ResourceSetInputProvider | `spec.defaultValues`, `status.exportedInputs[]` | free-form values |
| FluxInstance | `spec.kustomize.patches[].patch` | opaque patch text |
| Kustomization (`kustomize.toolkit.fluxcd.io`) | `spec.patches[].patch` | opaque patch text |
| Kustomization | `spec.postBuild.substitute` | free-form values |
| HelmRelease (`helm.toolkit.fluxcd.io`) | `spec.values` | free-form values |
| HelmRelease | `spec.postRenderers[].kustomize.patches[].patch` | opaque patch text |
| Application (`argoproj.io`) | `spec.source.helm.values` and `spec.sources[].helm.values` | opaque text |
| Application | `spec.source.helm.valuesObject` and `spec.sources[].helm.valuesObject` | free-form values |
| Application | `spec.source.helm.parameters[].value` (and `sources[]`) | free-form values |
| Application | `spec.source.kustomize.patches[].patch` (and `sources[]`) | opaque patch text |
| ApplicationSet (`argoproj.io`) | the same fields under `spec.template.spec` | as above |
| any kind above | the `kubectl.kubernetes.io/last-applied-configuration` annotation | a duplicate snapshot of everything |

**References are not payloads.** These stay readable and are never fetched or revealed:
- HelmRelease `valuesFrom` (a ConfigMap or Secret reference);
- `secretRef`, `certSecretRef` and `pullSecret` names;
- Kustomization `postBuild.substituteFrom`.

**URLs** with credentials in their user info are payloads in the URL fields Klyx shows:
- GitRepository `spec.url`, OCIRepository `spec.url`, Bucket `spec.endpoint`;
- FluxInstance `spec.sync.url`, ResourceSetInputProvider `spec.url`, FluxReport `spec.sync.source`;
- Argo `spec.source(s).repoURL`.

**The paths that expose them today:**
- the generic detail: Manifest YAML, `fieldsJSON`, annotations, facts;
- Describe (kubectl's raw stdout);
- the guarded YAML apply;
- the Flux cache diffs (`internal/cachediff`: live vs dry-run, masked only for Secrets by Flux's sanitiser);
- AI Explain (it re-reads the inspector's YAML and masks by key heuristics);
- chat: for an unknown kind it keeps scalar spec leaves to depth 4, scrubbed and capped at 200 characters, so a template's first 200 characters can go out.

## 2. The policy

**One backend sanitiser,** `crd.SanitizeEmbedded(gk, obj)`, runs before any DTO or string is made. It is a registry keyed by group and kind, version-agnostic, with one adapter per row of §1.

| Shape | Treatment |
|---|---|
| Embedded object that is a core/v1 Secret (`apiVersion: v1`, `kind: Secret`) | `data` and `stringData` values become `<masked>`, keys kept, through the existing `MaskSecretData` |
| Embedded object of another kind | kept, recursively sanitised by its own kind's adapter if it has one |
| Opaque text (a template, a patch, a values string) | **omitted**: replaced by `<omitted: may carry secrets · N bytes>`. Its siblings, such as a patch's `target`, are kept |
| Free-form values | **keys kept, leaf values omitted** (`<omitted>`), so structure and names stay readable. An exported input's `id` is kept |
| A URL with user info | the password, and a lone user that looks like a token (longer than 20 characters, or not `git`), becomes `<masked>`; host and path kept |
| `last-applied-configuration` | stripped for every registered kind |

- **Bytes never cross.** The omitted and masked values do not cross the bridge in any form. There is no collapse that carries them.
- **No reveal in this slice.** A later, explicit, local, UID-fenced reveal could be specified on its own; none is offered now.
- **Free-form values are omitted, not heuristically masked.** An opaque token under an ordinary key (`inputs.value`), or a value that becomes Secret data after templating, is exactly what key heuristics miss.

## 3. Where the policy runs

1. **The generic detail** (`fleet.GetInstanceDetail`): sanitised before `ToYAML`, `FieldsJSONOf`, annotations, labels, facts, managers and the summary. Every DTO field already reads the sanitised object; this extends what "sanitised" means.
2. **Describe** (`ExecService.Describe`): for a registered kind whose live object has a non-empty registered surface, Describe is **withheld**, never redacted. kubectl's prose cannot be parsed safely for templates and patches.
   - The result carries `Withheld` with the reason: "Describe is withheld for this <kind>: kubectl's text would include its templates, patches or values, which can carry secrets. The Manifest view shows a sanitised copy."
   - The sheet shows that reason in place of the report, and offers no Copy for it.
   - The kubeconfig-pinned copy command stays, so the user can run kubectl themselves, knowingly.
3. **Apply** (`fleet.ApplyResourceYAML`, the backend, for dry-run and real apply alike): refused for a registered kind whenever the **live** object has a non-empty registered surface: "This <kind> carries templates, patches or values that Klyx shows sanitised; edit it in Git."
   - The frontend hides Edit for such an object, with the same reason. The backend refusal holds whatever the frontend does.
   - A sanitised baseline can therefore never round-trip `<omitted>` or `<masked>` placeholders, or silently erase a live section.
4. **Cache diffs** (`internal/cachediff`): the live and dry-run objects of a registered kind pass through the same sanitiser before `unifiedDiff`. An omitted section that differs reads "differs (omitted: may carry secrets)", so a difference is still visible without its content.
5. **AI and chat:**
   - Explain reads the sanitised YAML, so it inherits the policy.
   - The chat projection applies the sanitiser before its structural rules, so a registered surface never reaches a model, scrubbed or not.

## 4. Tests

- **Per adapter:** each §1 row with a positive and a negative case.
  - The value is absent from the DTO's YAML, `fieldsJSON`, annotations and facts.
  - Keys, names and references stay readable.
- **The cases Codex named:**
  - an opaque template;
  - a targeted JSON patch (the target kept, the patch omitted);
  - a nested `last-applied-configuration` snapshot;
  - a secret value under an ordinary input key;
  - an embedded Secret inside a ResourceSet step.
- **Describe:** withheld for a ResourceSet with a template, and for a Kustomization with a patch. Not withheld for one without, so the existing Describe behaviour is unchanged elsewhere.
- **Apply:** refused by the backend, dry-run and real, for each registered kind with a live payload, even when the submitted YAML is otherwise valid.
- **Cache diff:** a differing omitted section reads as such, without content.
- **Chat:** no registered surface in a projection, by a needle test (a unique marker string in every payload must not appear anywhere in the serialised projection).
- **AI Explain:** the same needle test over the prompt bundle.
- **Lab:** the `with-secret` ResourceSet on `kind-klyx` (S1a fixtures) carries markers in an embedded Secret, an input and a template. A native smoke step opens its inspector and asserts no marker in the page, then that Describe is withheld and Edit is absent.

## 5. Not in this slice

- A reveal flow for omitted payloads.
- Detecting arbitrary secrets in every CRD: the registry covers the known embedding surfaces, and a new kind joins it by adapter.
- Changing how Secrets themselves are masked: this extends that masking, it does not replace it.
