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

Describe stays **kubectl's own report, read-only**. Klyx never rewrites the report, and it never adds a word that kubectl didn't print, except where the note says so (ages, counts, a find count, the UID notice). A Text view shows kubectl's exact stdout, byte for byte, and Copy always copies it.

**The Formatted view is a best-effort reading, and the Text view is the authority** (design review 1). kubectl's text is ambiguous in places:
- a value's embedded newline can look like a field;
- `[{alpha:1},{beta:2}]` and `[{alpha:1,beta:2}]` print identically.

So the parser promotes structure only in contexts it recognises, and otherwise keeps lines as text, in kubectl's order. It never asserts a count of list items.

### 14a. The parser (pure, `chrome/describe/parse.ts`)

`parseDescribe(text)` returns a tree, and `printDescribe(tree)` returns the input byte for byte. **The round trip is the invariant**: every input line lives in exactly one node, in order. A line the parser does not understand becomes a `text` node, so it is never dropped. The round trip proves that no text is lost. It does not prove the structure, so structural tests cover each rule and each ambiguity below.

**Node types:**
- `field`: indent, key and value, plus continuation lines and children.
- `table`: indent, header, column offsets and rows. A row may carry extra lines.
- `text`: one line.
- `blank`: an empty line.
- `item`: one certain list item (generic mode).
- `opaque`: a region kept as raw lines.

**Pre-scan:**
- **Generic mode:** the top level has `API Version:`, `Kind:` and `Metadata:` at column 0. This is the CRD describer.
- **The Events line,** recognised by its shape, never by its words alone. An event message prints its newlines raw, so a message line can read "Events: …" (review 2). The opener is either:
  - a column-0 `Events:` followed by the events table's header (titled cells that include Type, Reason and Message) and its dash row;
  - or `Events:  <none>` as the last line with text.

  Everything after the opener belongs to Events. An Event row starts with its type (`Normal` or `Warning`) and has a reason. Any other line continues the last row's message, so a message line that reads like a header or "Events: …" stays inside that message.
- **Data regions:** a line underlined by a run of `=` opens an opaque region that runs to the Events line. This is ConfigMap and Secret data (`Data` / `====`), whose content is arbitrary text and is never parsed.

**Line rules,** in order:
1. **Table with a dash row.** The next line is only dash runs and spaces, with **two or more** runs. Every header cell is capitalised words (`Type`, `CPU Requests`, `LastHeartbeatTime`).
   - The dash runs give the column starts.
   - Rows follow at the table's indent or deeper, while their text never crosses a column start.
   - In the Events table, a line after a row that is not a row continues that row's message: kubectl prints an event message's newlines raw.

   A ConfigMap key's single `----` is never a table.
2. **Pod Conditions.** In built-in mode, under a field keyed `Conditions` with no value, a header that is exactly `Type` and `Status` is a table. Its rows must have text at both column starts.
3. **Continuation.** A line indented exactly to the open field's value column continues that field (Labels, Annotations, Tolerations). Only the field whose line, or continuation, was the last line read is open.
4. **Field.** The line is `Key:` with a value or with nothing.
   - The key must not begin with `-`, must have no run of spaces, and its colon must be followed by whitespace or the end of the line.
   - Nesting is relative, because a Deployment's pod template prints `Containers:` children at three spaces.
   - **Not a field**, in these cases:
     - in built-in mode, the children of `Command` and `Args`: each argument is a text line, even `echo note: value`;
     - a line at the indent of a field's earlier text children: a list of scalars stays text;
     - in generic mode, a line deeper than a scalar field. A map or list prints with no value, so a field with a value never has children;
     - a line indented past the open field's value column: a long annotation prints its value indented under its key;
     - in generic mode, a column-0 line after the prelude that can't be a top-level key (review 2). The prelude is Name, Namespace, Labels and Annotations, which print on their own before `API Version`. Two cases:
       - a key already seen: an object's top-level keys are distinct, so a repeat is a value's own line ("Status: …" inside a message);
       - Metadata, Spec or Status with a value, or API Version or Kind without one.

       Key order proves nothing here, because `smartLabelFor` is not invertible.

     Each such line continues the open value (rule 5), or is text (rule 6).
5. **Multi-line value.** A line that is not a field, while the open field has a value, continues that value.
6. **Text.** Any other line is a text line in its place: a list item (Command, Mounts, Hostnames) or prose ("(Total limits may be over 100 percent…)").

**Generic mode and list items** (review 2):
- **Labels can't be turned back into names.** `smartLabelFor` is not invertible: "URL" and "a" print as URL and A, and "Foo" and "foo" both print as Foo. So a boundary is certain only where the schema is known.
- **Items split only in a known list** (`KNOWN_LISTS`):
  - Conditions, with the standard condition keys;
  - Owner References;
  - Gateway API: Listeners, Supported Kinds, Parent Refs, Parents, Rules, Backend Refs, Matches and Addresses;
  - Flux: Depends On and Health Checks;
  - External Secrets: Data.
- **Each known list records its items' keys,** both the label as printed and the field name kubectl sorted. A block splits only when **every** key in it is one of the list's own. A new item starts where a key repeats, or where a key sorts before the previous one, compared by the recorded names.
- **Any other list reads as one block,** exactly as the text does. The view never shows an item count.
- Built-in describers are not sorted, so the rule never applies outside generic mode.

### 14b. The sheet (`DescribePane` rebuilt)

**The shell** is the shared wide dialog (`.l-dialog .l-dialog-wide`, at most 1180px wide and 88vh high).

- **Head:**
  - the section label "DESCRIBE";
  - the kind in ink-3 sans 14;
  - the object as `ns / name` in `t-object-title` (mono 19/500).
- **Meta line** (12/16, ink-3):
  - the command as run, with its kubeconfig (see Go below);
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
- **Conditions shape** (review 1). Only a generic-mode field keyed `Conditions` becomes a table, and only when every certain item:
  - is scalar fields alone;
  - draws its keys from the standard set (Last Heartbeat Time, Last Probe Time, Last Transition Time, Last Update Time, Message, Observed Generation, Reason, Severity, Status, Type);
  - carries Type and Status.

  Its columns are Type, Status, Reason, the other keys in kubectl's order, then Message, with the keys as kubectl printed them. Every value appears in a cell. Anything else stays fields, so a non-condition list that happens to carry Type and Status is never reshaped.
- **Tables:**
  - A real `<table>`, scrolling sideways inside its own box, never wrapped. The header is sans 12 ink-3.
  - Cells are mono 13, except a Message column, which wraps.
  - A fault or attention row carries an Aspect in its first cell, and the words stay ink-1 or ink-2 (slice 13c: the tone sits on the mark).
  - **Condition marks** (review 1) use a polarity table that belongs to Describe. A mark describes the recorded condition, never the object's health, and the shared `conditionColor` is unchanged.
    - **Positive types,** a fault when False: Ready, Available, Accepted, Programmed, ResolvedRefs, Synced, Established, NamesAccepted, Initialized, ContainersReady, PodScheduled, PodReadyToStartContainers, Progressing and Complete.
    - **Negative types,** a fault when True: MemoryPressure, DiskPressure, PIDPressure, NetworkUnavailable, Stalled, ReplicaFailure and Failed.
    - Any other type, and Unknown, carries no mark: Reconciling, Issuing and custom types alike.
  - An Events row of type Warning carries the attention Aspect.
- **Times:** an RFC 3339 or RFC 1123Z value gets "· 6d ago" after it, in ink-3, with the local absolute time as its title. kubectl's own words stay as printed.

**Text view:** kubectl's output in mono 13/20 on the sunk ground, `white-space: pre`, scrolling sideways, so its columns stay aligned.

**Find** (both views):
- A 36px input above the body. ⌘F or Ctrl+F focuses it while the sheet is open.
- Matches are marked, and the current one is ringed and scrolled into view. The count reads "3 of 12" or "No matches".
- Enter goes to the next match and Shift+Enter to the previous.
- Sections that hold a match open while the query is active.
- Matching runs over the view model's text runs, in render order, so the count and the ring always agree with what is on screen.

**Runs, identity and cancellation** (review 1):
- **A run's identity** is {cluster, kind argument, namespace, name, expected UID}, plus a generation number. Its output, command, run time, error and UID notice belong to one reply and change together.
- **A changed identity** (A, then B, then A) clears the sheet to loading and cancels the run in flight. A reply applies only when both its identity and its generation are current.
- **Closing the sheet cancels the run.** The Go method takes the call's `context.Context`, and the bridge returns the generated `CancellablePromise` unwrapped, so its `cancel()` reaches Go and `exec.CommandContext` stops kubectl. The 30s bound stays.
- **Refresh is serialised.** It is disabled while a run is in flight. It keeps the last report on screen, with "refreshing…" in the meta.
- **A refresh that fails:**
  - with no stdout, the error shows above the last report, which is labelled "Output from 12:04:31, before the failed refresh";
  - with partial stdout, the newest evidence wins. The partial output shows, labelled "Partial output: kubectl exited with an error", beside the error.

**UID notice.** A caller that knows the object's UID passes it. If the report's Metadata UID differs, an attention evidence panel says: "This describes a different object at this name (UID …), not the one in the inspector." A report with no UID claims nothing.

**Keys:**
- Tab is trapped (`trapTab`), and focus returns to the opener when the sheet closes.
- Escape clears a non-empty find, and otherwise closes the sheet.
- Keys stop at the dialog, so lists behind it never act.

**Go** (`appbridge.ExecService.Describe`), review 1:
- **Stdout is returned unchanged:** no trailing-newline trim. Text and Copy are then exactly kubectl's stdout.
- **The method takes `ctx context.Context`,** and the run derives its 30s timeout from it.
- **`DescribeResultDTO` gains `command`,** built from `kubectlTerminalArgv()`, the same kubeconfig pinning the exec terminal uses, followed by the describe arguments. Under the host shell's quoting (`displayCommand`):
  - one kubeconfig file becomes `--kubeconfig <file>`;
  - a path list becomes `env KUBECONFIG=<list>`;
  - on Windows a list is refused. Copy command is then disabled with that reason (`commandError`).

  A context name shared by two kubeconfigs therefore can't resolve to another cluster in the person's terminal. No other environment value is shown.
- The bindings are regenerated.

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

- **Parser:**
  - The round trip on real lab captures: Gateway, HTTPRoute, Kustomization, Certificate, ExternalSecret, Pod, Node, Deployment and Service.
  - The round trip on trailing blank lines and on CRLF.
  - **Structural tests** (review 1), because the round trip cannot catch a wrong structure:
    - Command and Args with colon- and header-shaped arguments;
    - a multi-line condition message whose second line reads "Status: …";
    - a multi-line Event message;
    - ConfigMap data with a `----` key underline and YAML-shaped content;
    - `[{alpha:1},{beta:2}]` versus `[{alpha:1,beta:2}]`, which both read as one block with no divider;
    - URL then A, and Foo twice, in an unknown map: no divider;
    - a known list with a foreign key: no divider;
    - the prelude, then API Version through Events, with Metadata UID, read from the real reports;
    - a multi-line Event message holding "Events: …" and a header-looking line;
    - a ragged last column;
    - the three-space pod template;
    - the Gateway and route conditions as two certain items;
    - a Pod's Containers, Conditions and Events;
    - a Node's pods table.
- **Marks:** Stalled True is a fault and Stalled False is unmarked. Reconciling carries no mark either way. A non-condition list that carries Type and Status stays fields.
- **Sheet:**
  - the head, meta and command;
  - Formatted and Text;
  - the find count, cycling and section open;
  - copy and copy command (and disabled with a reason);
  - **runs:**
    - A, then B, then A, with late replies;
    - close during a read cancels it;
    - Refresh is disabled in flight;
    - a failed refresh, with and without partial stdout;
  - the error, and the UID notice saying "different object";
  - Escape order and focus restore;
  - the rail at both widths.
- **Go:**
  - stdout is unchanged, including trailing newlines;
  - the command for one kubeconfig, for a list, and for a Windows list (refused);
  - quoting of spaces;
  - two kubeconfigs that share a context name each yield their own pinned command;
  - cancellation stops the run.
- **14c:** each inspector's More carries Describe with the right argument, and nothing for an unknown kind, a removed object or a Helm release.
- **Live:**
  - the headless walk captures the sheet for a Gateway, a Pod and a Node, night and day, at 1440 and 1024;
  - the native smoke gains "Describe" steps. It opens Describe from the Flux inspector and requires a Status section, a conditions table row, and a live report that round-trips (`printDescribe(parseDescribe(output)) === output`).
