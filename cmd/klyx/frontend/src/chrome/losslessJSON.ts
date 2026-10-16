// Lossless JSON for the Fields tree (The Line slice 15, implementation
// review 1). JSON.parse rounds an int64 past 2^53 and builds every container
// in full; this parser keeps each number as the text the server wrote (Num),
// and holds containers as arrays - an array's items (Arr), an object's
// entries in order (Obj) - so the tree reads a size as a length and a page
// as a slice, never a pass over every key.

export class Num {
  readonly text: string;
  constructor(text: string) { this.text = text; }
}
export class Arr {
  readonly items: JValue[];
  constructor(items: JValue[]) { this.items = items; }
}
export class Obj {
  readonly entries: [string, JValue][];
  constructor(entries: [string, JValue][]) { this.entries = entries; }
}
export type JValue = null | boolean | string | Num | Arr | Obj;

const NUMBER = /-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/y;

// parseLossless reads one JSON document. It throws a SyntaxError on text
// that is not JSON (a document nested past the stack throws a RangeError);
// callers treat either as undrawable.
export function parseLossless(text: string): JValue {
  let i = 0;
  const bad = (what: string) => new SyntaxError(`${what} at offset ${i}`);
  const ws = () => {
    for (; i < text.length; i++) {
      const c = text.charCodeAt(i);
      if (c !== 32 && c !== 9 && c !== 10 && c !== 13) return;
    }
  };
  // a string's escapes are JSON.parse's to decode; this only finds its end
  const str = (): string => {
    const start = i++;
    for (;;) {
      if (i >= text.length) throw bad("unterminated string");
      const c = text.charCodeAt(i);
      if (c === 34) break;
      i += c === 92 ? 2 : 1;
    }
    i++;
    return JSON.parse(text.slice(start, i)) as string;
  };
  const value = (): JValue => {
    ws();
    const c = text[i];
    if (c === "{") {
      i++;
      const entries: [string, JValue][] = [];
      ws();
      if (text[i] === "}") { i++; return new Obj(entries); }
      for (;;) {
        ws();
        if (text[i] !== '"') throw bad("expected a key");
        const key = str();
        ws();
        if (text[i] !== ":") throw bad("expected ':'");
        i++;
        entries.push([key, value()]);
        ws();
        if (text[i] === ",") { i++; continue; }
        if (text[i] === "}") { i++; return new Obj(entries); }
        throw bad("expected ',' or '}'");
      }
    }
    if (c === "[") {
      i++;
      const items: JValue[] = [];
      ws();
      if (text[i] === "]") { i++; return new Arr(items); }
      for (;;) {
        items.push(value());
        ws();
        if (text[i] === ",") { i++; continue; }
        if (text[i] === "]") { i++; return new Arr(items); }
        throw bad("expected ',' or ']'");
      }
    }
    if (c === '"') return str();
    if (text.startsWith("true", i)) { i += 4; return true; }
    if (text.startsWith("false", i)) { i += 5; return false; }
    if (text.startsWith("null", i)) { i += 4; return null; }
    NUMBER.lastIndex = i;
    const m = NUMBER.exec(text);
    if (!m) throw bad("unexpected text");
    i += m[0].length;
    return new Num(m[0]);
  };
  const v = value();
  ws();
  if (i !== text.length) throw bad("trailing text");
  return v;
}

// fromPlain turns an already-parsed value into the lossless form (tests and
// callers holding plain values). Numbers keep JavaScript's text for them.
export function fromPlain(v: unknown): JValue {
  if (v === null || typeof v === "boolean" || typeof v === "string") return v;
  if (typeof v === "number") return new Num(String(v));
  if (Array.isArray(v)) return new Arr(v.map(fromPlain));
  if (typeof v === "object") return new Obj(Object.entries(v as Record<string, unknown>).map(([k, x]) => [k, fromPlain(x)]));
  return null;
}
