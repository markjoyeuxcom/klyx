import { describe, it, expect, vi, afterEach } from "vitest";
import ts from "typescript";
// The frontend carries no Node types; vitest runs this file in Node.
// @ts-expect-error -- node:fs is untyped here
import fs from "node:fs";
// @ts-expect-error -- node:path is untyped here
import path from "node:path";
import { readStored, writeStored, isStorageKey } from "./storage";
import { STORAGE_KEYS, type StorageKey } from "./storageKeys";
import snapshot from "./registry.snapshot.json";

declare const __dirname: string;

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
  sessionStorage.clear();
});

describe("the storage facade (Settings S1 §3.2)", () => {
  it("keeps each key in its tier", () => {
    writeStored("klyx-view-search", "{}");
    writeStored("klyx-route", "{\"name\":\"fleet\"}");
    expect(sessionStorage.getItem("klyx-view-search")).toBe("{}");
    expect(localStorage.getItem("klyx-view-search")).toBeNull();
    expect(readStored("klyx-route")).toBe("{\"name\":\"fleet\"}");
  });

  it("refuses a cast key before any access", () => {
    const get = vi.spyOn(Storage.prototype, "getItem");
    const set = vi.spyOn(Storage.prototype, "setItem");
    const forged = "klyx-secret" as StorageKey;
    expect(isStorageKey(forged)).toBe(false);
    expect(readStored(forged)).toBeNull();
    writeStored(forged, "x");
    expect(get).not.toHaveBeenCalled();
    expect(set).not.toHaveBeenCalled();
    expect(isStorageKey("toString")).toBe(false); // an inherited name is not a key
  });

  it("is best-effort when storage throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("denied"); });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("full"); });
    expect(readStored("klyx-theme")).toBeNull();
    expect(() => writeStored("klyx-theme", "day")).not.toThrow();
  });

  it("has a registry entry for every key, and a key for every webview entry", () => {
    const entries = (snapshot as { entries: { id: string; scope: string }[] }).entries;
    const webview = entries.filter((e) => e.scope === "webview").map((e) => e.id.replace(/^store\./, "")).sort();
    expect(Object.keys(STORAGE_KEYS).sort()).toEqual(webview);
  });
});

// A reference to webview storage outside settings/storage.ts, however it is
// spelled: the identifier itself (called, passed or aliased), and element
// access by a string literal.
function storageRefs(source: string, file = "x.ts"): string[] {
  const sf = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, file.endsWith("x") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
  const out: string[] = [];
  const names = new Set(["localStorage", "sessionStorage"]);
  const visit = (n: ts.Node) => {
    if (ts.isIdentifier(n) && names.has(n.text)) out.push(n.text);
    if (ts.isElementAccessExpression(n) && ts.isStringLiteralLike(n.argumentExpression) && names.has(n.argumentExpression.text)) out.push(`[${n.argumentExpression.text}]`);
    ts.forEachChild(n, visit);
  };
  visit(sf);
  return out;
}

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const e of fs.readdirSync(dir, { withFileTypes: true }) as { name: string; isDirectory(): boolean }[]) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) out.push(...sourceFiles(p));
    else if (/\.(ts|tsx)$/.test(e.name) && !/\.test\.(ts|tsx)$/.test(e.name)) out.push(p);
  }
  return out;
}

describe("the storage scan", () => {
  it("finds no reference to webview storage outside the facade", () => {
    const src = path.resolve(__dirname, "..");
    const files = sourceFiles(src);
    expect(files.length).toBeGreaterThan(100);
    const found: string[] = [];
    for (const f of files) {
      if (f === path.join(src, "settings", "storage.ts")) continue;
      for (const r of storageRefs(fs.readFileSync(f, "utf8"), f)) found.push(`${path.relative(src, f)}: ${r}`);
    }
    expect(found).toEqual([]);
  });

  it.each([
    ["called", "localStorage.getItem('k')"],
    ["passed to a helper", "read(localStorage, 'k')"],
    ["aliased", "const s = window.localStorage; s.getItem('k')"],
    ["through globalThis", "globalThis.sessionStorage.setItem('k', 'v')"],
    ["by brackets", "window['sessionStorage'].getItem('k')"],
    ["destructured", "const { localStorage: ls } = window"],
  ])("refuses storage %s", (_, source) => {
    expect(storageRefs(source).length).toBeGreaterThan(0);
  });

  it("passes a clean file, and comments", () => {
    expect(storageRefs("// localStorage is reached through the facade\nreadStored('klyx-theme')")).toEqual([]);
  });
});
