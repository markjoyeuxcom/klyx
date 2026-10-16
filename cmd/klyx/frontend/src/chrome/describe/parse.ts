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
const FIELD = /^( *)([^\s:\-][^:]*?):(?:([ \t]+)(.*))?$/;
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

// eventsOpener finds the Events section by its shape, never by its words
// alone: an event message prints its newlines raw, so a message line can
// read "Events: …" (review 2). The opener is a column-0 "Events:" followed
// by the events table's header and dash row, or "Events:  <none>" as the
// last line with text. -1 when neither is there.
function eventsOpener(S: string[]): number {
  for (let i = 0; i + 2 < S.length; i++) {
    if (!/^Events: *$/.test(S[i]) || !DASH_ROW.test(S[i + 2]) || indentOf(S[i + 1]) === 0) continue;
    const head = cellsOf(S[i + 1], dashCols(S[i + 2]));
    if (head.every((h) => TITLE.test(h)) && ["Type", "Reason", "Message"].every((h) => head.includes(h))) return i;
  }
  let last = S.length - 1;
  while (last >= 0 && S[last].trim() === "") last--;
  return last >= 0 && /^Events: +<none> *$/.test(S[last]) ? last : -1;
}

export function parseDescribe(text: string): DTree {
  const lines = text.split("\n");
  const S = lines.map(strip);
  const n = lines.length;
  const atRoot = (k: string) => S.some((l) => l.startsWith(`${k}:`) && FIELD.test(l));
  const generic = atRoot("API Version") && atRoot("Kind") && atRoot("Metadata");
  const eventsAt = eventsOpener(S);
  const end = eventsAt >= 0 ? eventsAt : n;
  // ConfigMap and Secret data: a "====" underline opens an opaque region.
  let dataAt = -1;
  for (let i = 0; i + 1 < end; i++) if (S[i].trim() !== "" && UNDERLINE.test(S[i + 1])) { dataAt = i; break; }

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

  for (let i = 0; i < n; i++) {
    const raw = lines[i];
    const s = S[i];

    if (i === dataAt) {
      stack.length = 0;
      root.push({ t: "opaque", title: s.trim(), raw: lines.slice(i, end) });
      open = null;
      i = end - 1;
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

    // 3. A continuation aligned to the open field's value column.
    if (open && open.valueCol >= 0 && indent === open.valueCol) {
      open.cont.push({ raw, text: s.trim(), kind: "aligned" });
      continue;
    }

    // 4. A field - unless the context says the line cannot be one.
    const f = asField(raw);
    const notField =
      // everything after the Events line belongs to Events
      (inEvents && indent === 0) ||
      // Command and Args print one argument per line, whatever it says
      (!generic && top !== null && (top.key === "Command" || top.key === "Args") && indent > top.indent) ||
      // a list of scalars stays text
      (top !== null && top.listAt !== undefined && indent === top.listAt) ||
      // a line indented past the open field's value column continues it: a
      // long annotation prints its value indented under the key
      (open !== null && open.valueCol >= 0 && indent > open.valueCol) ||
      // generic: a scalar field never has children - deeper lines continue it
      (generic && open !== null && open.value !== "" && indent > open.indent) ||
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

    // 5. A value's own next line: kubectl prints a value's embedded
    // newlines raw, at any indent (often column 0).
    if (open && open.value !== "") {
      // Relative to the value column when indented that far, so a long
      // annotation's value keeps its own shape.
      const rel = open.valueCol >= 0 && indent >= open.valueCol ? s.slice(open.valueCol) : s;
      open.cont.push({ raw, text: rel.trimEnd(), kind: "multiline" });
      continue;
    }

    // 6. A text line in its place: a list item (Command, Mounts,
    // Hostnames) or prose.
    const into = containerFor(indent);
    const owner = topOf();
    if (owner && owner.listAt === undefined && into === owner.children) owner.listAt = indent;
    into.push({ t: "text", raw, text: s.trim() });
    open = null;
  }

  if (generic) for (const node of root) if (node.t === "field") splitItems(node);
  return { generic, nodes: root };
}

// KNOWN_LISTS: the list fields whose items Describe separates, each with its
// item's keys - label as printed, and the field name kubectl sorted (review
// 2). smartLabelFor is not invertible ("URL" and "a" print URL, A; "Foo" and
// "foo" both print Foo), so a boundary is certain only where the schema is
// known: every key of the block is one of the list's own fields. Any other
// list reads as one block, exactly as the text does.
const CONDITION_KEYS: Record<string, string> = {
  "Last Heartbeat Time": "lastHeartbeatTime", "Last Probe Time": "lastProbeTime", "Last Transition Time": "lastTransitionTime",
  "Last Update Time": "lastUpdateTime", Message: "message", "Observed Generation": "observedGeneration", Reason: "reason",
  Severity: "severity", Status: "status", Type: "type",
};
const keys = (...names: string[]): Record<string, string> => Object.fromEntries(names.map((n) => [n.replace(/^./, (c) => c.toUpperCase()).replace(/([a-z])([A-Z])/g, "$1 $2"), n]));
export const KNOWN_LISTS: Record<string, Record<string, string>> = {
  Conditions: CONDITION_KEYS,
  "Owner References": { "API Version": "apiVersion", "Block Owner Deletion": "blockOwnerDeletion", Controller: "controller", Kind: "kind", Name: "name", UID: "uid" },
  // Gateway API
  Listeners: keys("allowedRoutes", "attachedRoutes", "conditions", "hostname", "name", "port", "protocol", "supportedKinds", "tls"),
  "Supported Kinds": keys("group", "kind"),
  "Parent Refs": keys("group", "kind", "name", "namespace", "port", "sectionName"),
  Parents: keys("conditions", "controllerName", "parentRef"),
  Rules: keys("backendRefs", "filters", "matches", "name", "sessionPersistence", "timeouts"),
  "Backend Refs": keys("filters", "group", "kind", "name", "namespace", "port", "weight"),
  Matches: keys("headers", "method", "path", "queryParams"),
  Addresses: keys("type", "value"),
  // Flux
  "Depends On": keys("name", "namespace"),
  "Health Checks": { "API Version": "apiVersion", Kind: "kind", Name: "name", Namespace: "namespace" },
  // External Secrets
  Data: keys("remoteRef", "secretKey", "sourceRef"),
};

// splitItems: in generic mode, the field children of a known list start a
// new item where the boundary is certain - a key repeats, or sorts before
// the previous one in kubectl's own order.
function splitItems(f: DField): void {
  for (const c of f.children) if (c.t === "field") splitItems(c);
  const known = KNOWN_LISTS[f.key];
  const kids = f.children;
  if (!known || kids.length === 0 || !kids.every((c) => c.t === "blank" || (c.t === "field" && c.key in known))) return;
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
