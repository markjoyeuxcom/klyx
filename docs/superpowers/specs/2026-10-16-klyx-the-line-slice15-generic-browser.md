# The Line slice 15: the generic browser

Parent spec: `2026-10-14-klyx-the-line-redesign.md` (§2 visual system). Scale: slice 13. Describe: slice 14.

On 2026-10-16 the owner compared Klyx with Headlamp, k9s, Lens and Aptakube. They asked for research on Headlamp and Lens, and for the generic resource browser to be polished. That browser is the views for kinds without a bespoke lens: the API browser, the Resources lens and the generic instance inspector.

## 1. What is wrong, and why

Lab captures are in `scratchpad/gen15/`, at main a0395e9.

1. **List columns are hand-written for 15 built-in kinds.** These include Services, PVCs, Ingresses, Jobs and HPAs, plus a few CRD groups (cert-manager, External Secrets, two Flux kinds, Cilium policies).
   - Every other kind lists Namespace, Name and Age only. A GitRepository shows no URL, Ready or Status, though its CRD declares all three as printer columns, and `kubectl get` prints them.
   - The generic path reads metadata only (`internal/fleet/crd.go`, `ListInstances`).
2. **The generic inspector is thin and partly pre-slice-13:**
   - **Conditions** are 11px rows with a coloured dot, which is green when healthy. Messages are ellipsized.
   - **The panel always shows "No direct related objects found."**
   - **Object facts are missing:** UID, generation against observed generation, finalizers, a deletion in progress, field managers.
   - **Spec and status** exist only as YAML in the Manifest tab.
   - **`last-applied-configuration`** is a raw annotation chip.
3. **The API browser lags the rest of the app:**
   - Sections start collapsed, and a search that matches a kind leaves its section closed.
   - It says "1 kinds".
   - The location crumb reads "CRDs by API group" while the page is titled "API browser".
   - A kind has no definition view: versions, storage, conversion, printer columns or schema.
   - When CRDs cannot be listed (RBAC), the page is an error with nothing to browse.
   - A CRD's instance list breadcrumb returns to "Resources", not to the API browser.

## 2. What Headlamp and Lens do

The research read the source of Headlamp `main` at 959ce358 (v0.46.0), and of FreeLens `main` at 3295db4e (v1.10.3), the maintained fork of open-source Lens. Current Lens Desktop is closed source.

- **Printer columns.** Both evaluate a CRD's JSONPath in the client.
  - **Headlamp** ignores `priority` and `format`, shows dates as absolute, and makes every value a string.
  - **FreeLens** keeps only priority 0 in the list, ignores the column type, and sorts numbers as text. It rewrites column names (startCase) and drops any column named "age".
- **Detail.**
  - Both show metadata, chips for labels and annotations, owner links, conditions and events.
  - **Headlamp** hides UID, finalizers and generation, links owners only for built-in kinds, and fetches events once by name. Its conditions colour only `Available`.
  - **FreeLens** shows finalizers and managed fields per manager, links custom-resource owners, and matches events by UID. It treats any False condition as an error, which inverts negative types such as Stalled.
  - **Neither renders spec or status.** Both send you to the YAML.
- **The CRD view.**
  - **Headlamp** embeds the instances table. It reads v1beta1 fields that are always empty on v1 CRDs, and has no printer columns or schema.
  - **FreeLens** shows printer columns for one version, and the schema and conversion as raw JSON.
  - Neither shows counts.
- **Gaps in both:**
  - conditions aware of polarity, and staleness from generation against observed generation;
  - a structured spec/status view;
  - a navigable schema;
  - GitOps ownership;
  - typed columns;
  - `last-applied-configuration` handling.

  FreeLens falls back to discovery when CRDs cannot be listed. Headlamp's groups vanish silently.

**Klyx's advantage:** the API server evaluates printer columns itself when asked for a table (`Accept: application/json;as=Table;v=v1;g=meta.k8s.io`). Checked on the lab:
- GitRepository returns Name, URL, Age (`date`), Ready and Status. Each CRD column's description carries its JSONPath ("Custom resource definition column (in JSONPath format): .spec.url").
- Deployment returns typed cells (`2/2`, `2`, `2`) and its wide columns at priority 1.
- `continue` pages.

That is exactly what kubectl prints: first match, the type coercion, relative dates and `priority`. Neither GUI uses it.

## 3. What changes

### 15a. Columns from the server's table

**Go** (`ListInstances`):
- **A kind without a hand-written list asks for the server's table.** The request is the REST client with `includeObject=Metadata`, the same `limit` and `continue`.
- **A server that cannot answer with a table** falls back to the metadata list it uses today: a 406, or a response that is not a `meta.k8s.io` Table. So does a connection without a REST client (fake clients). Rows are then Namespace, Name and Age, as now.
- **The 15 hand-written lists stay.** They feed the risk filter, and the server's columns for those kinds are no better.
- **`InstancePageDTO` gains `columns`:** `{name, type, format, priority, description}` per column, `numeric` for integer and number columns, and `jsonPath` when the description carries it.
- **`InstanceDTO` gains `cells`,** strings in column order. Numbers are formatted, booleans read `true`/`false`, a null is "".
- **Two columns are dropped, because Klyx draws its own name and a live Age:**
  - the first column when its format is `name`;
  - the creation-time column: one named Age (in any case) whose JSONPath is `.metadata.creationTimestamp`, or a built-in table's Age.

**The list:**
- **Column priority.** Priority-0 columns join the list after Name, fitted by `fitColumns` in the server's order. Columns of priority 1 or more are kubectl's `-o wide`: they appear in the inspector's summary (15b), not the list.
- **Cells.** A numeric column is right-aligned with tabular figures.
- **Dates.** A `date` cell is the server's relative text at the read ("6d"), titled "as of the read at 12:04:31".
- **Header tooltips.** A column's header title is its description. The automatic description reads "JSONPath .spec.url".
- **Search** matches cell text, as it matches today's fields.
- **The row mark is a recorded condition, never a health verdict.**
  - It applies when a column's name is a condition type whose polarity Describe knows. The polarity table moves to `chrome/conditions.ts` and is shared.
  - Values: a cell of `False` for a positive type, or `True` for a negative one, marks the row as a fault, titled "Ready False (the Ready column)". `Unknown` is checking. Anything else is unmarked.
  - The mark column appears only when such a column exists.
- **Fleet mode** uses the columns only when every cluster that answered returned identical definitions. Otherwise it lists without them and says "columns differ between clusters (CRD versions?)".

### 15b. The generic inspector

This applies to InstanceDetail, which serves every kind without a bespoke lens.

- **Summary** comes first under the verdict. It is the object's own table row (one GET with the same Accept header) at every priority, wide columns included.
  - Empty values are hidden.
  - Each row's title is its column description.
  - A server without tables leaves the block out.
- **An Object block:**
  - created, absolute with its age;
  - UID, with copy;
  - generation, and `status.observedGeneration` when present. When observed is behind, it reads "Not yet observed: generation 4, observed 3", with the syncing mark;
  - a deletion: "Deleting since …". The finalizers it waits on are listed, with the owning controller named when a finalizer is a known one;
  - finalizers;
  - owners, moved here from Related, as links;
  - managed by: the GitOps unit from labels and annotations (`detectGitOwner`), and the Helm release from its labels;
  - field managers from `managedFields`: manager, operation, subresource and time. These are read before the detail strips managedFields, and none of their field sets are sent.
- **Conditions.** The shared `InspectorConditions` moves to the slice-13 scale. This changes Pods, Nodes, the Flux detail, ResourceInspector and InstanceDetail together.
  - **Rows:** the mark, the type (13/600 ink-1), the status word, the reason (mono, ink-3) and the age of the last transition (absolute in its title).
  - **The message** sits below in ink-2 and wraps. Past three lines it clamps, with "Show all".
  - **A condition observed for an older generation** says "for generation 3 of 4".
  - **Marks keep today's polarity** (`conditionColor`, plus each caller's `colorOf`), mapped to Aspect: danger is a fault, Unknown is checking, healthy has no mark. The green dot goes.
- **Fields.** Spec, status and any other top-level field (except apiVersion, kind and metadata) render as a structured tree from the object itself, which is real structure, unlike slice 14's text.
  - **Backend:** the backend sends `fieldsJSON`, the sanitised object minus metadata. It is omitted for Secrets, which keep their own masked section. Above 256 KiB it is omitted with "Too large to draw here; see Manifest".
  - **Layout:** each top-level field is an `InspectorBlock` (Spec, Status, then others in the object's order), using the Describe sheet's grid, guides and item dividers.
  - **Lists and maps:** list items are real, so they can be counted ("3 items"). Maps and lists below depth 2 are collapsed, with their size.
  - **Values:** times get ages, `null` and empty values read in ink-3, and a long string clamps with Show all.
- **Labels and annotations** stay as chips. `kubectl.kubernetes.io/last-applied-configuration` is not a chip: it reads "last-applied-configuration · N bytes · Show", and shows pretty-printed JSON when opened.
- **Related** draws nothing when empty. The "No direct related objects found." panel goes.
- **Events** are unchanged: they are already matched by UID. With none, they show one quiet line.

### 15c. The API browser and the definition view

- **Sections:**
  - A search opens every section that holds a match.
  - It reads "1 kind" for one kind.
  - A section's total shows "…" only while it is counting.
- **Naming.** The task's label becomes "API browser", so the crumb matches the title. "Built-in resources" becomes "Resources". Keywords keep "custom resources", "crd", "kinds" and "gvr".
- **The instance list breadcrumb** goes back to the API browser for a custom resource ("API browser / GitRepository"), and to Resources for a built-in kind.
- **The definition inspector.** A click on a kind selects it and opens an inspector, while ↵ and a double click still open its instances.
  - **Verdict** from the CRD's own conditions:
    - Established and NamesAccepted are positive;
    - NonStructuralSchema True is a fault, because the API server refuses to serve a non-structural schema well;
    - a CRD that is not yet Established reads as not served.
  - **Primary:** Open instances. **More:** Describe (`customresourcedefinition.apiextensions.k8s.io`), Copy name.
  - **Facts:**
    - group and scope;
    - kind, plural, singular, short names and categories;
    - the operator attribution;
    - the instance count.
  - **Versions:** a table of name, served, storage and deprecated (with its warning).
    - A version in `status.storedVersions` that is no longer served is a look-at: "objects may still be stored as v1beta1".
  - **Conversion in words:**
    - "None: versions share one schema shape";
    - or "Webhook: <ns>/<service>:<port><path>, review versions v1".
  - **Printer columns:** a table per version of name, type, JSONPath, priority and description.
  - **Schema** for the storage version, with a version switch:
    - a tree of fields with type, format, a required mark, enum (at most 20), default, and description (clamped, with Show all);
    - `x-kubernetes-preserve-unknown-fields` and `int-or-string` are named;
    - a filter matches field paths.

    This is `kubectl explain` as a view.
- **The same schema gives 15b's tree** a title on each field of a custom resource: its type and description, by path.
- **The discovery fallback.** When the CRD list fails (for example, forbidden):
  - the browser lists the served groups that are not built-in, from discovery, under "Definitions could not be read: <reason>. Kinds come from discovery";
  - their instances still open;
  - the definition inspector says it cannot be read.
- **Go:** `CRDService.GetCRDDefinition(cluster, name)` returns the DTO above.
  - The schema is flattened in Go into nodes of `{path, depth, type, format, required, enum, default (JSON, at most 200 characters), description (at most 1,000), preserveUnknown, intOrString}`.
  - It caps at 3,000 nodes or depth 12, with a truncation note.
  - The list's discovery fallback reuses the cached discovery.

## 4. Out of scope

- Objects this object owns: a reverse owner lookup across kinds.
- Removing finalizers or forcing a deletion: a write path, which needs the owner's decision.
- Editor tabs: a schema-docs tab and a review diff.
- A view of all instances across CRDs.
- Comparing CRD versions across the fleet.
- Column sorting.
- Live (watched) lists.

## 5. Testing

- **Go:**
  - Table decoding from an httptest server: columns, cells, the two dropped columns, priority, `continue`.
  - The fallback on 406, on a non-Table answer, and with no REST client.
  - The single-object table.
  - Managed-field summaries without field sets.
  - The deletion and finalizer facts.
  - `fieldsJSON`: sanitised, never for Secrets, the size cap.
  - `GetCRDDefinition`: versions, stored-not-served, conversion words, printer columns, the schema flattening with its caps, and NonStructuralSchema.
  - The discovery fallback.
- **Frontend:**
  - the list's columns: priority split, numeric alignment, date titles, header descriptions;
  - the row mark: positive, negative, Unknown, and no condition column;
  - fleet mode with identical and with differing definitions;
  - the inspector's Summary, Object block, conditions at the new scale (in every inspector that uses them), the Fields tree and its caps, last-applied handling, no empty Related;
  - the API browser: search opening sections, "1 kind", the crumb, the breadcrumb back;
  - the definition inspector: its verdicts and the schema filter;
  - the discovery fallback.
- **Live:**
  - headless captures on the kind lab of GitRepository, GatewayClass, Order, AppProject and Deployment lists, an inspector, and a definition with its schema, in night and day at 1440 and 1024;
  - the native smoke gains "a CRD's printer columns": the GitRepository list shows URL and Ready.
