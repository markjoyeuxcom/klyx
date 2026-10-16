// parseDescribe reads `kubectl describe` text into a tree for the Describe
// sheet (The Line slice 14a). The tree is a reading of kubectl's report,
// never a rewrite: printDescribe(parseDescribe(text)) === text, byte for byte.
// Every input line lives in exactly one node, in order; a line the rules do
// not understand is a text node, never dropped.
//
// The reading is best-effort and conservative (design review 1): kubectl's
// text is ambiguous in places - a value's embedded newline can look like a
// field, and two list shapes can print alike - so structure is promoted only
// in contexts recognised here, and the Text view stays the authority.
//
// kubectl's describers print (k8s.io/kubectl pkg/describe):
//   - "Key:  value" fields, nested by indent. Built-in describers indent by
//     their own levels (a Deployment's pod template prints Containers'
//     children at three spaces), so nesting is relative, never a level count.
//   - tables: a header, usually a row of dashes, then aligned rows (Events,
//     node conditions, a node's pods). Pod Conditions prints no dash row.
//   - continuation lines aligned to a field's value column (Labels,
//     Annotations, Tolerations), and a value's own newlines, printed raw.
// The generic (CRD) describer prints every map's keys sorted and a list of
// maps with no item markers (printUnstructuredContent).

export type DCont = { raw: string; text: string; kind: "aligned" | "multiline" };

export type DField = {
  t: "field";
  raw: string;
  indent: number;
  key: string;
  // value is the text after the key on its own line, trimmed; "" when none.
  value: string;
  // valueCol is where the value starts on the key's line; -1 without one.
  valueCol: number;
  cont: DCont[];
  children: DNode[];
  // listAt: the indent of this field's text children - a list of scalars
  // stays text at that indent (review 1).
  listAt?: number;
  // list: the known list this field is, by its object's kind and its path
  // (review 3) - "conditions" or another KNOWN list name.
  list?: string;
};

export type DRow = { cells: string[]; raw: string; extra: string[] };

export type DTable = {
  t: "table";
  // header: the header line, then the dash row when there is one.
  header: string[];
  indent: number;
  dashed: boolean;
  cols: number[];
  head: string[];
  rows: DRow[];
};

export type DText = { t: "text"; raw: string; text: string };
export type DBlank = { t: "blank"; raw: string };
// DItem is one certain item of a generic-mode list of maps.
export type DItem = { t: "item"; children: DNode[] };
// DOpaque keeps a region as raw lines: ConfigMap and Secret data.
export type DOpaque = { t: "opaque"; title: string; raw: string[] };
export type DNode = DField | DTable | DText | DBlank | DItem | DOpaque;
export type DTree = { generic: boolean; nodes: DNode[] };

const DASH_ROW = /^ *-+( +-+)+ *$/;
const UNDERLINE = /^=+ *$/;
// A field: "Key:" then whitespace and a value, or nothing. The key never
// starts with whitespace, ":" or "-", and the colon must be followed by
// whitespace or the end of the line.
const FIELD = /^( *)([^\s:-][^:]*?):(?:([ \t]+)(.*))?$/;
// A column title: capitalised words ("Type", "CPU Requests").
const TITLE = /^[A-Z][A-Za-z]*(?: [A-Za-z]+)*$/;

const indentOf = (s: string) => s.length - s.trimStart().length;
const strip = (raw: string) => raw.replace(/\r$/, "");

function asField(raw: string): DField | null {
  const s = strip(raw);
  const m = FIELD.exec(s);
  if (!m) return null;
  const key = m[2];
  // A key with a run of spaces is a table header or prose, not a key.
  if (/\s{2,}|\t/.test(key) || key !== key.trimEnd()) return null;
  const value = (m[4] ?? "").trimEnd();
  const indent = m[1].length;
  return {
    t: "field", raw, indent, key, value,
    valueCol: value ? indent + key.length + 1 + m[3].length : -1,
    cont: [], children: [],
  };
}

function dashCols(s: string): number[] {
  const cols: number[] = [];
  const re = /-+/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s))) cols.push(m.index);
  return cols;
}

// fits: text never crosses a column start - the character before every
// column start is a space (or the row is shorter).
function fits(s: string, cols: number[]): boolean {
  for (let i = 1; i < cols.length; i++) {
    const c = cols[i];
    if (s.length > c && s[c - 1] !== " ") return false;
  }
  return true;
}

export function cellsOf(s: string, cols: number[]): string[] {
  return cols.map((c, i) => s.slice(c, i + 1 < cols.length ? cols[i + 1] : undefined).trim());
}

// rootField: an object's top-level keys are distinct, so a repeat is a
// value's own line ("Status: …" inside a message); the object's own maps
// print with no value and its apiVersion and kind with one (review 2).
// smartLabelFor is not invertible, so key order proves nothing here.
function rootField(f: DField, seen: Set<string>): boolean {
  if (seen.has(f.key)) return false;
  if ((f.key === "Metadata" || f.key === "Spec" || f.key === "Status") && f.value !== "") return false;
  if ((f.key === "API Version" || f.key === "Kind") && f.value === "") return false;
  return true;
}

// eventsCandidate: a column-0 line that has the Events section's shape -
// "Events:" followed by the events table's header and dash row, or
// "Events:  <none>" as the last line with text. An event message prints its
// newlines raw, so the words alone prove nothing (review 2).
function eventsCandidates(S: string[], from: number): number[] {
  const out: number[] = [];
  let last = S.length - 1;
  while (last >= 0 && S[last].trim() === "") last--;
  for (let i = from; i < S.length; i++) {
    if (i === last && /^Events: +<none> *$/.test(S[i])) { out.push(i); continue; }
    if (i + 2 >= S.length || !/^Events: *$/.test(S[i]) || !DASH_ROW.test(S[i + 2]) || indentOf(S[i + 1]) === 0) continue;
    const head = cellsOf(S[i + 1], dashCols(S[i + 2]));
    if (head.every((h) => TITLE.test(h)) && ["Type", "Reason", "Message"].every((h) => head.includes(h))) out.push(i);
  }
  return out;
}

// layout settles the report's two footers before anything is parsed (review
// 3): ConfigMap and Secret data, and Events.
//   - Data: a line underlined by "====" opens data. ConfigMap prints Data
//     and then BinaryData, whose lines are sizes, so the real Events follows
//     the LAST underline; an Events-shaped block inside the data comes
//     before it and is never chosen.
//   - Events: exactly one candidate (after the last underline, if any) is
//     the opener. Several cannot be told apart, so everything from the data
//     (or the first candidate) on stays opaque - never a guess.
function layout(S: string[]): { opaqueAt: number; opaqueEnd: number; eventsAt: number } {
  const unders: number[] = [];
  for (let i = 0; i + 1 < S.length; i++) if (S[i].trim() !== "" && UNDERLINE.test(S[i + 1]) && indentOf(S[i]) === 0) unders.push(i);
  const from = unders.length ? unders[unders.length - 1] + 2 : 0;
  const cands = eventsCandidates(S, from);
  if (cands.length === 1) return { opaqueAt: unders.length ? unders[0] : -1, opaqueEnd: cands[0], eventsAt: cands[0] };
  if (unders.length) return { opaqueAt: unders[0], opaqueEnd: S.length, eventsAt: -1 };
  if (cands.length > 1) return { opaqueAt: cands[0], opaqueEnd: S.length, eventsAt: -1 };
  return { opaqueAt: -1, opaqueEnd: S.length, eventsAt: -1 };
}

export function parseDescribe(text: string): DTree {
  const lines = text.split("\n");
  const S = lines.map(strip);
  const n = lines.length;
  const atRoot = (k: string) => S.some((l) => l.startsWith(`${k}:`) && FIELD.test(l));
  const generic = atRoot("API Version") && atRoot("Kind") && atRoot("Metadata");
  const { opaqueAt, opaqueEnd, eventsAt } = layout(S);

  const root: DNode[] = [];
  const stack: DField[] = [];
  // The field whose line (or continuation) was the last line read: only it
  // can take a continuation, so the printed order never changes.
  let open: DField | null = null;
  // Generic mode: the top-level keys seen from API Version on - the
  // prelude (Name, Namespace, Labels, Annotations) prints on its own.
  let rootKeys: Set<string> | null = null;

  const containerFor = (indent: number): DNode[] => {
    while (stack.length && stack[stack.length - 1].indent >= indent) stack.pop();
    return stack.length ? stack[stack.length - 1].children : root;
  };
  const topOf = () => (stack.length ? stack[stack.length - 1] : null);
  const continueValue = (f: DField, raw: string, s: string, indent: number) => {
    // Relative to the value column when indented that far, so a long
    // annotation's value keeps its own shape.
    const rel = f.valueCol >= 0 && indent >= f.valueCol ? s.slice(f.valueCol) : s;
    f.cont.push({ raw, text: rel.trimEnd(), kind: "multiline" });
  };
  const textLine = (raw: string, s: string, indent: number) => {
    const into = containerFor(indent);
    const owner = topOf();
    if (owner && owner.listAt === undefined && into === owner.children) owner.listAt = indent;
    into.push({ t: "text", raw, text: s.trim() });
    open = null;
  };

  for (let i = 0; i < n; i++) {
    const raw = lines[i];
    const s = S[i];

    if (i === opaqueAt) {
      stack.length = 0;
      root.push({ t: "opaque", title: s.trim(), raw: lines.slice(i, opaqueEnd) });
      open = null;
      i = opaqueEnd - 1;
      continue;
    }
    if (s.trim() === "") {
      (topOf()?.children ?? root).push({ t: "blank", raw });
      open = null;
      continue;
    }
    const indent = indentOf(s);
    const top = topOf();
    const inEvents = eventsAt >= 0 && i > eventsAt;

    // Value context first (review 3): a line the context says belongs to a
    // value or a list is never promoted to a table or a field.
    //   - aligned to the open field's value column: a continuation
    //     (Labels, Annotations, Tolerations);
    //   - indented past it: the value's own next line (a long annotation
    //     prints its value under its key);
    //   - generic: deeper than a scalar field - a map or list prints with
    //     no value, so a field with one never has children.
    if (open && open.valueCol >= 0 && indent === open.valueCol) {
      open.cont.push({ raw, text: s.trim(), kind: "aligned" });
      continue;
    }
    if (open && open.value !== "" && (indent > open.valueCol || (generic && indent > open.indent))) {
      continueValue(open, raw, s, indent);
      continue;
    }
    //   - Command and Args print one argument per line, whatever it says;
    //   - in generic mode a list of scalars is arbitrary strings: it stays
    //     text once it starts. (Built-in describers print prose before a
    //     table - "Allocated resources" - so there only fields are barred.)
    const listed = top !== null && top.listAt !== undefined && indent === top.listAt;
    if (top && ((!generic && (top.key === "Command" || top.key === "Args") && indent > top.indent) || (generic && listed))) {
      textLine(raw, s, indent);
      continue;
    }

    // 1. A table with a dash row of two or more runs and titled columns.
    const next = i + 1 < n ? S[i + 1] : "";
    if (DASH_ROW.test(next) && indentOf(next) === indent) {
      const cols = dashCols(next);
      const head = cellsOf(s, cols);
      if (fits(s, cols) && head.every((h) => TITLE.test(h))) {
        const table: DTable = { t: "table", header: [raw, lines[i + 1]], indent, dashed: true, cols, head, rows: [] };
        const events = inEvents && top?.key === "Events" && top.indent === 0;
        let j = i + 2;
        for (; j < n; j++) {
          const r = S[j];
          const row = r.trim() !== "" && indentOf(r) >= indent && fits(r, cols);
          const cells = row ? cellsOf(r, cols) : [];
          // An Event row starts with its type (Normal or Warning) and has a
          // reason; any other line continues the last row's message, which
          // kubectl prints with its newlines raw.
          if (events) {
            if (row && indentOf(r) === indent && (cells[0] === "Normal" || cells[0] === "Warning") && cells[1] !== "") table.rows.push({ cells, raw: lines[j], extra: [] });
            else if (table.rows.length) table.rows[table.rows.length - 1].extra.push(lines[j]);
            else break;
            continue;
          }
          if (!row) break;
          table.rows.push({ cells, raw: lines[j], extra: [] });
        }
        containerFor(indent).push(table);
        open = null;
        i = j - 1;
        continue;
      }
    }

    // 2. Pod Conditions: the undashed "Type  Status" table, only as the
    // first child of a built-in Conditions field.
    if (!generic && top && open === top && top.key === "Conditions" && top.value === "" && top.children.length === 0 && indent > top.indent) {
      const words = s.trim().split(/ {2,}/);
      if (words.length === 2 && words[0] === "Type" && words[1] === "Status") {
        const cols = [indent, s.indexOf("Status")];
        const isRow = (r: string) => r.trim() !== "" && indentOf(r) === indent && r.length > cols[1] && r[cols[1] - 1] === " " && r[cols[1]] !== " ";
        if (i + 1 < n && isRow(S[i + 1])) {
          const table: DTable = { t: "table", header: [raw], indent, dashed: false, cols, head: words, rows: [] };
          let j = i + 1;
          for (; j < n && isRow(S[j]); j++) table.rows.push({ cells: cellsOf(S[j], cols), raw: lines[j], extra: [] });
          top.children.push(table);
          open = null;
          i = j - 1;
          continue;
        }
      }
    }

    // 3. A field - unless the context says the line cannot be one.
    const f = asField(raw);
    const notField =
      // a list of scalars stays text once it starts
      listed ||
      // everything after the Events line belongs to Events
      (inEvents && indent === 0) ||
      // generic: a top-level key after the prelude that cannot be one
      (generic && f !== null && indent === 0 && i !== eventsAt && rootKeys !== null && !rootField(f, rootKeys));
    if (f && !notField) {
      containerFor(f.indent).push(f);
      stack.push(f);
      open = f;
      if (generic && indent === 0 && i !== eventsAt) {
        if (f.key === "API Version" && rootKeys === null) rootKeys = new Set();
        rootKeys?.add(f.key);
      }
      continue;
    }

    // 4. A value's own next line: kubectl prints a value's embedded
    // newlines raw, at any indent (often column 0).
    if (open && open.value !== "") {
      continueValue(open, raw, s, indent);
      continue;
    }

    // 5. A text line in its place: a list item (Command, Mounts,
    // Hostnames) or prose.
    textLine(raw, s, indent);
  }

  if (generic) {
    const value = (k: string) => root.find((x): x is DField => x.t === "field" && x.indent === 0 && x.key === k)?.value ?? "";
    const apiVersion = value("API Version");
    const family = knownLists(apiVersion.includes("/") ? apiVersion.slice(0, apiVersion.indexOf("/")) : "", value("Kind"));
    for (const node of root) if (node.t === "field") markLists(node, [], family);
  }
  return { generic, nodes: root };
}

// Known lists (review 3): a property name is not a schema. A list splits
// into items only at a path whose schema is known for the object's own
// group and kind - every key of the block one of that list's fields, each
// recorded as printed and as the name kubectl sorted. smartLabelFor is not
// invertible ("URL" and "a" print URL, A; "Foo" and "foo" both print Foo),
// so anywhere else - an unknown kind, a values map, a same-named property
// elsewhere - a list reads as one block, exactly as the text does.
type Keys = Record<string, string>;
const keys = (...names: string[]): Keys => Object.fromEntries(names.map((n) => [n.replace(/^./, (c) => c.toUpperCase()).replace(/([a-z])([A-Z])/g, "$1 $2"), n]));
export const CONDITION_KEYS: Keys = keys("lastHeartbeatTime", "lastProbeTime", "lastTransitionTime", "lastUpdateTime", "message", "observedGeneration", "reason", "severity", "status", "type");
const OWNER_REFS: Keys = { "API Version": "apiVersion", "Block Owner Deletion": "blockOwnerDeletion", Controller: "controller", Kind: "kind", Name: "name", UID: "uid" };
const PARENT_REF = keys("group", "kind", "name", "namespace", "port", "sectionName");
const ADDRESSES = keys("type", "value");
type Family = { group: string; kinds: string[]; lists: Record<string, Keys> };
const FAMILIES: Family[] = [
  { group: "gateway.networking.k8s.io", kinds: ["Gateway"], lists: {
    "Spec/Listeners": keys("allowedRoutes", "hostname", "name", "port", "protocol", "tls"),
    "Spec/Addresses": ADDRESSES,
    "Status/Addresses": ADDRESSES,
    "Status/Conditions": CONDITION_KEYS,
    "Status/Listeners": keys("attachedRoutes", "conditions", "name", "supportedKinds"),
    "Status/Listeners/Conditions": CONDITION_KEYS,
    "Status/Listeners/Supported Kinds": keys("group", "kind"),
  } },
  { group: "gateway.networking.k8s.io", kinds: ["HTTPRoute", "GRPCRoute"], lists: {
    "Spec/Parent Refs": PARENT_REF,
    "Spec/Rules": keys("backendRefs", "filters", "matches", "name", "retry", "sessionPersistence", "timeouts"),
    "Spec/Rules/Backend Refs": keys("filters", "group", "kind", "name", "namespace", "port", "weight"),
    "Spec/Rules/Matches": keys("headers", "method", "path", "queryParams"),
    "Status/Parents": keys("conditions", "controllerName", "parentRef"),
    "Status/Parents/Conditions": CONDITION_KEYS,
  } },
  { group: "kustomize.toolkit.fluxcd.io", kinds: ["Kustomization"], lists: {
    "Status/Conditions": CONDITION_KEYS,
    "Spec/Depends On": keys("name", "namespace"),
    "Spec/Health Checks": { "API Version": "apiVersion", Kind: "kind", Name: "name", Namespace: "namespace" },
  } },
  { group: "helm.toolkit.fluxcd.io", kinds: ["HelmRelease"], lists: { "Status/Conditions": CONDITION_KEYS, "Spec/Depends On": keys("name", "namespace") } },
  { group: "source.toolkit.fluxcd.io", kinds: ["GitRepository", "OCIRepository", "HelmRepository", "HelmChart", "Bucket"], lists: { "Status/Conditions": CONDITION_KEYS } },
  { group: "cert-manager.io", kinds: ["Certificate", "CertificateRequest", "Issuer", "ClusterIssuer"], lists: { "Status/Conditions": CONDITION_KEYS } },
  { group: "external-secrets.io", kinds: ["ExternalSecret"], lists: { "Status/Conditions": CONDITION_KEYS, "Spec/Data": keys("remoteRef", "secretKey", "sourceRef") } },
  { group: "external-secrets.io", kinds: ["SecretStore", "ClusterSecretStore"], lists: { "Status/Conditions": CONDITION_KEYS } },
  { group: "argoproj.io", kinds: ["Application", "ApplicationSet"], lists: { "Status/Conditions": CONDITION_KEYS } },
  { group: "karpenter.sh", kinds: ["NodePool", "NodeClaim"], lists: { "Status/Conditions": CONDITION_KEYS } },
];

function knownLists(group: string, kind: string): Record<string, Keys> {
  const fam = FAMILIES.find((f) => f.group === group && f.kinds.includes(kind));
  // Every object's metadata is ObjectMeta.
  return { "Metadata/Owner References": OWNER_REFS, ...(fam?.lists ?? {}) };
}

// markLists names each known list by its path, and splits its field
// children into items where the boundary is certain - a key repeats, or
// sorts before the previous one by the recorded names.
function markLists(f: DField, parent: string[], lists: Record<string, Keys>): void {
  const path = [...parent, f.key];
  for (const c of f.children) {
    if (c.t === "field") markLists(c, path, lists);
    if (c.t === "item") for (const g of c.children) if (g.t === "field") markLists(g, path, lists);
  }
  const known = lists[path.join("/")];
  if (!known) return;
  const kids = f.children;
  if (kids.length === 0 || !kids.every((c) => c.t === "blank" || (c.t === "field" && c.key in known))) return;
  f.list = known === CONDITION_KEYS ? "conditions" : path[path.length - 1];
  const items: DItem[] = [{ t: "item", children: [] }];
  let prev: string | null = null;
  let seen = new Set<string>();
  for (const c of kids) {
    if (c.t === "field") {
      const k = known[c.key];
      if (seen.has(k) || (prev !== null && k < prev)) {
        items.push({ t: "item", children: [] });
        seen = new Set();
      }
      seen.add(k);
      prev = k;
    }
    items[items.length - 1].children.push(c);
  }
  if (items.length > 1) f.children = items;
}

export function printDescribe(tree: DTree): string {
  const out: string[] = [];
  const emit = (node: DNode) => {
    switch (node.t) {
      case "field":
        out.push(node.raw);
        for (const c of node.cont) out.push(c.raw);
        node.children.forEach(emit);
        return;
      case "table":
        out.push(...node.header);
        for (const r of node.rows) out.push(r.raw, ...r.extra);
        return;
      case "item":
        node.children.forEach(emit);
        return;
      case "opaque":
        out.push(...node.raw);
        return;
      default:
        out.push(node.raw);
    }
  };
  tree.nodes.forEach(emit);
  return out.join("\n");
}
