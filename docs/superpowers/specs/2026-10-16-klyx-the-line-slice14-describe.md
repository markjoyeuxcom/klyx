# The Line slice 14: Describe

Parent spec: `2026-10-14-klyx-the-line-redesign.md` (§2 visual system). Scale: slice 13 (`2026-10-16-klyx-the-line-slice13-fidelity.md` §13a).

On 2026-10-16 the owner sent a screenshot of Describe on a Gateway (`infra/internal`) and asked for improvements to describing objects.

## 1. What is wrong, and why

`chrome/DescribePane.tsx` runs `kubectl describe` and shows its text as one `<pre>`.

1. **It predates slice 13's scale.** The pane is a 760×560 overlay with inline styles. The title is mono 12px, the text mono 11px and the controls 24px. Slice 13 set body text at 14px and controls at 36px.
2. **Nothing has hierarchy.** Keys, values, sections and nesting are all one colour and weight. The eye has nowhere to land.
3. **List items run together.** For a CRD, kubectl's generic describer prints a list of maps with no item markers. In the screenshot the Gateway's second condition starts with a second "Last Transition Time:" straight after the first condition's "Type:". In the source (`k8s.io/kubectl/pkg/describe`, `printUnstructuredContent`) the keys of every map are printed sorted, and list items are not delimited.
4. **Times are absolute only.** Examples are "2026-10-10T18:34:40Z" and "Sat, 10 Oct 2026 12:29:38 +0100", with no age.
5. **Tables break.** Events, node conditions and a node's pods are aligned text. `pre-wrap` folds them in a narrow pane, so their columns no longer line up.
6. **The pane can't search, refresh or say what ran.** It has no find, no way to re-run, no record of when kubectl ran, and no command to copy for a terminal.
7. **Few screens offer it.** Only the generic instance panel, Pods and Nodes have Describe. The lens inspectors don't: Flux, Argo, cert-manager, External Secrets, Gateways, routes, workloads and Karpenter.

## 2. What changes

Describe stays **kubectl's own report, read-only**. Klyx never rewrites the report, and it never adds a word that kubectl didn't print, except where the note says so (ages, counts, a find count, the UID notice). A Text view shows kubectl's exact output, and Copy always copies that output.

### 14a. The parser (pure, `chrome/describe/parse.ts`)

`parseDescribe(text)` returns a tree, and `printDescribe(tree)` returns the input byte for byte. **The round trip is the invariant**: every input line lives in exactly one node, in order. A line the parser does not understand becomes a `text` node, so it is never dropped.

**Node types:**
- `field`: indent, key and value, plus continuation lines and children.
- `table`: indent, header, column offsets and rows.
- `text`: one line.
- `blank`: an empty line.

**Line rules,** in order:
1. **Table with a dash row.** A line followed by a line made only of dash runs and spaces is a table header. The dash runs give the column start offsets. Rows follow at the table's indent or deeper until a blank line or a shallower line. The last column takes the rest of the line. Examples: Events, node Conditions, a node's pods and allocated resources, a Deployment's Conditions.
2. **Table without a dash row.** This is how the Pod Conditions table prints ("Type Status"). It is accepted only when:
   - the header has two or more single capitalised words separated by two or more spaces;
   - every following line at the same indent has text at each column start, with a space before it.

   Anything else is not a table.
3. **Continuation.** A line indented exactly to the previous field's value column continues that field. Examples: Labels, Annotations and Tolerations.
4. **Field.** `Key:` with a value or with nothing. The key must not begin with `-`, and the colon must be followed by whitespace or the end of the line. Nesting is relative: a deeper indent makes the line a child. Indents are not assumed to be multiples of two, because a Deployment's pod template prints `Containers:` children at three spaces.
5. **List item.** A deeper line under a field that has no value, and that is not a field itself (Command, Mounts, Hostnames).
6. **Multi-line value.** Any other line continues the previous field's value. kubectl prints a value's embedded newlines raw, at column 0.

**Generic mode and list items.** Generic mode applies when the top level has `API Version`, `Kind` and `Metadata`; that is the CRD describer.
- Below those keys, a field in a block starts a **new list item** when its key repeats, or sorts before the previous sibling. kubectl sorts map keys, and the comparison here lowercases them and removes spaces.
- Each item is a child group. When the order rule misfires (mixed-case keys), the cost is a spurious item divider; no line is lost or moved.
- Built-in describers are not sorted, so the rule never applies outside generic mode.

### 14b. The sheet (`DescribePane` rebuilt)

**The shell** is the shared wide dialog (`.l-dialog .l-dialog-wide`, at most 1180px wide and 88vh high).

- **Head:**
  - the section label "DESCRIBE";
  - the kind in ink-3 sans 14;
  - the object as `ns / name` in `t-object-title` (mono 19/500).
- **Meta line** (12/16, ink-3):
  - the command as run (`kubectl --context kind-klyx describe gateways.gateway.networking.k8s.io -n infra -- internal`);
  - "ran 12:04:31 · 3s ago".
- **Actions,** all 36px:
  - a `Formatted | Text` segmented control;
  - Copy, which copies kubectl's output;
  - Copy command;
  - Refresh;
  - Close.

**Formatted view:**
- **Index rail** (168px), shown when the sheet is 820px or wider, measured on the element so it follows the app zoom:
  - "Overview" (the leading scalar fields), then every top-level field with children or a table. "Events" and "Conditions" are always listed when present, even when their value is `<none>`.
  - Events shows its count, plus an attention Aspect when any row is a Warning.
  - A conditions table shows a fault Aspect when a row is a fault.
  - Clicking a section scrolls to it, and the current section follows the scroll.
  - Below 820px the rail becomes a wrapping row of the same buttons above the body.
- **Sections:** a top-level field with children is a collapsible section. Its head uses `t-section-label` with a chevron, `aria-expanded`, and is open by default. Consecutive top-level scalar fields are a key/value grid with no head.
- **Fields:** a grid with the key in sans 13/20 ink-3 and the value in mono 13/20 ink-1. A nested block indents 20px and has a hairline guide. A list item after the first gets 8px of space and a hairline above. Continuation and list lines stack in the value column. `<none>`, `<unset>` and `<unknown>` read in ink-3.
- **Conditions shape.** A generic-mode list whose items all carry `Type` and `Status` renders as a table: Type, Status, Reason, Age (from Last Transition Time), Message, then any other keys in kubectl's order. Every value from the tree appears in a cell.
- **Tables:**
  - A real `<table>`, scrolling sideways inside its own box, never wrapped. The header is sans 12 ink-3.
  - Cells are mono 13, except a Message column, which wraps.
  - A fault or attention row carries an Aspect in its first cell, and the words stay ink-1 or ink-2 (slice 13c: the tone sits on the mark).
  - Condition polarity uses the existing `conditionColor`, so MemoryPressure True is a fault and Ready False is a fault.
  - An Events row of type Warning carries the attention Aspect.
- **Times:** an RFC 3339 or RFC 1123Z value gets "· 6d ago" after it, in ink-3, with the local absolute time as its title. kubectl's own words stay as printed.

**Text view:** kubectl's output in mono 13/20 on the sunk ground, `white-space: pre`, scrolling sideways, so its columns stay aligned.

**Find** (both views):
- A 36px input above the body. ⌘F or Ctrl+F focuses it while the sheet is open.
- Matches are marked, and the current one is ringed and scrolled into view. The count reads "3 of 12" or "No matches".
- Enter goes to the next match and Shift+Enter to the previous.
- Sections that hold a match open while the query is active.
- Matching runs over the view model's text runs, in render order, so the count and the ring always agree with what is on screen.

**Refresh** keeps the last report on screen, with "refreshing…" in the meta. If the refresh fails, the error shows above the report, which is then labelled "Output from 12:04:31".

**UID notice.** A caller that knows the object's UID passes it. If the report's Metadata UID differs, an attention evidence panel says: "This describes a newer object at this name (UID …), not the one in the inspector." A report with no UID claims nothing.

**Keys:**
- Tab is trapped (`trapTab`), and focus returns to the opener when the sheet closes.
- Escape clears a non-empty find, and otherwise closes the sheet.
- Keys stop at the dialog, so lists behind it never act.

**Go** (`appbridge.ExecService.Describe`): `DescribeResultDTO` gains `command`. It is the argv with `argv[0]` shown as `kubectl`, through the existing `displayCommand` quoting. The bindings are regenerated.

### 14c. Describe from every inspector

- A hook, `useDescribe(cluster, target | null)`, returns a "Describe" More item and the sheet.
- The target is `{ kind, namespace, name, uid }`. Its kubectl argument is `singular.group` from one table in `chrome/describe/targets.ts`. I checked that kubectl accepts `kustomization.kustomize.toolkit.fluxcd.io`, `httproute.gateway.networking.k8s.io` and `deployment.apps` in the lab, and `DescribeCmd` already validates this shape.
- **Added to these inspectors' More menus:**
  - Flux reconciliations and sources;
  - Argo Applications and ApplicationSets;
  - Certificates and issuers;
  - ExternalSecrets and stores;
  - Gateways and routes;
  - workloads;
  - Karpenter NodeClaims and NodePools.
- Kinds the table does not know get no item.
- Helm releases are not Kubernetes objects and get none.
- A removed or unverified object gets none either: Describe reads the live object by name.

## 3. Out of scope

- Links from values to objects ("Controlled By", "Node", parent refs).
- Describe as a console verb.
- Klyx's own describe for CRDs, built from the object rather than kubectl's text.

## 4. Testing

- **Parser:** the round trip on real lab captures (Gateway, HTTPRoute, Kustomization, Certificate, ExternalSecret, Pod, Node, Deployment, Service). Fixtures also cover:
  - a multi-line value at column 0;
  - mixed-case keys;
  - a table whose last column is ragged;
  - a three-space pod template.

  Structure asserts:
  - the Gateway and route conditions split into two items;
  - a Pod's Containers, Conditions (no dash row) and Events tables;
  - a Node's pods table.
- **Sheet:**
  - the head, meta and command;
  - Formatted and Text;
  - the find count, cycling and section open;
  - copy and copy command;
  - refresh keeping the last report;
  - the error and the UID notice;
  - Escape order and focus restore;
  - the rail at both widths.
- **14c:** each inspector's More carries Describe with the right argument, and nothing for an unknown kind, a removed object or a Helm release.
- **Live:**
  - the headless walk captures the sheet for a Gateway, a Pod and a Node, night and day, at 1440 and 1024;
  - the native smoke gains "Describe" steps: open it from the Flux inspector, and require a Status section and a conditions table row, and that the live report round-trips (`printDescribe(parseDescribe(output)) === output`).
