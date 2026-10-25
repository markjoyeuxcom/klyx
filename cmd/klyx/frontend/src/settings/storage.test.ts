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
// spelled: the identifier itself (called, passed or aliased), and any string
// naming it - a bracket key, a constant alias, a Reflect.get - since a
// computed key starts from one (Codex's S1 implementation review, R3).
const STORAGE_NAMES = /\b(localStorage|sessionStorage)\b/;
function storageRefs(source: string, file = "x.ts"): string[] {
  const sf = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, file.endsWith("x") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
  const out: string[] = [];
  const visit = (n: ts.Node) => {
    if (ts.isIdentifier(n) && (n.text === "localStorage" || n.text === "sessionStorage")) out.push(n.text);
    const text = ts.isStringLiteralLike(n) || ts.isTemplateHead(n) || ts.isTemplateMiddle(n) || ts.isTemplateTail(n) ? n.text : null;
    if (text !== null && STORAGE_NAMES.test(text)) out.push(`"${text}"`);
    ts.forEachChild(n, visit);
  };
  visit(sf);
  return out;
}

// The type-aware half: any expression whose type is the DOM's Storage,
// however it was obtained. An expression typed any escapes it - indexing
// Window by keyof Window is any, since some of its members are - so a
// deliberate escape through any is a review matter, not a test's.
const OPTIONS: ts.CompilerOptions = {
  target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.ESNext, moduleResolution: ts.ModuleResolutionKind.Bundler,
  jsx: ts.JsxEmit.ReactJSX, lib: ["lib.es2020.d.ts", "lib.dom.d.ts", "lib.dom.iterable.d.ts"], strict: true,
  noEmit: true, skipLibCheck: true, allowImportingTsExtensions: true, resolveJsonModule: true, types: [],
};
function isStorage(type: ts.Type): boolean {
  if (type.isUnion() || type.isIntersection()) return type.types.some(isStorage);
  const decl = type.getSymbol()?.getDeclarations()?.[0];
  return type.getSymbol()?.getName() === "Storage" && !!decl && /lib\.dom\.d\.ts$/.test(decl.getSourceFile().fileName);
}
function typedStorageRefs(program: ts.Program, files: Set<string>): string[] {
  const checker = program.getTypeChecker();
  const out: string[] = [];
  for (const sf of program.getSourceFiles()) {
    if (!files.has(path.resolve(sf.fileName))) continue;
    const visit = (n: ts.Node) => {
      if (ts.isTypeNode(n) || ts.isImportDeclaration(n)) return;
      if ((ts.isPropertyAccessExpression(n) || ts.isElementAccessExpression(n) || ts.isIdentifier(n) || ts.isCallExpression(n)) && isStorage(checker.getTypeAtLocation(n))) {
        const { line } = sf.getLineAndCharacterOfPosition(n.getStart());
        out.push(`${sf.fileName}:${line + 1}: ${n.getText().slice(0, 60)}`);
        return;
      }
      ts.forEachChild(n, visit);
    };
    visit(sf);
  }
  return out;
}
function fixtureProgram(source: string): { program: ts.Program; files: Set<string> } {
  const name = path.resolve("/fixture/storage-fixture.ts");
  const host = ts.createCompilerHost(OPTIONS);
  const get = host.getSourceFile;
  host.getSourceFile = (f, v, ...rest) => (path.resolve(f) === name ? ts.createSourceFile(f, source, v) : get.call(host, f, v, ...rest));
  host.fileExists = ((exists) => (f: string) => path.resolve(f) === name || exists(f))(host.fileExists);
  return { program: ts.createProgram([name], OPTIONS, host), files: new Set([name]) };
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

  it("finds no Storage-typed expression outside the facade, by the type checker", () => {
    const src = path.resolve(__dirname, "..");
    const files = sourceFiles(src).filter((f) => f !== path.join(src, "settings", "storage.ts"));
    const program = ts.createProgram(files, OPTIONS);
    expect(typedStorageRefs(program, new Set(files.map((f) => path.resolve(f))))).toEqual([]);
  }, 120_000);

  it.each([
    ["called", "localStorage.getItem('k')"],
    ["passed to a helper", "read(localStorage, 'k')"],
    ["aliased", "const s = window.localStorage; s.getItem('k')"],
    ["through globalThis", "globalThis.sessionStorage.setItem('k', 'v')"],
    ["by brackets", "window['sessionStorage'].getItem('k')"],
    ["destructured", "const { localStorage: ls } = window"],
    ["by a constant key", 'const key = "localStorage";\nwindow[key].setItem("unregistered-setting", "yes");'],
    ["by Reflect", 'Reflect.get(globalThis, "sessionStorage")'],
  ])("refuses storage %s", (_, source) => {
    expect(storageRefs(source).length).toBeGreaterThan(0);
  });

  it.each([
    ["a computed key built at run time", 'const k = ["local", "Storage"].join("") as "localStorage";\nexport const s = window[k];'],
    ["a getter's result", "export const s = Object.getOwnPropertyDescriptor(window, ['local', 'Storage'].join(''))?.get?.call(window) as Storage;"],
    ["a helper's key parameter", "export function pick(w: Window, k: 'local' | 'session') { const m = { local: w.localStorage, session: w.sessionStorage }; return m[k]; }"],
  ])("the type checker refuses storage obtained as %s", (_, source) => {
    const { program, files } = fixtureProgram(source);
    expect(typedStorageRefs(program, files).length).toBeGreaterThan(0);
  }, 60_000);

  it("the type checker passes a clean file", () => {
    const { program, files } = fixtureProgram("export const n: number = window.innerWidth;");
    expect(typedStorageRefs(program, files)).toEqual([]);
  }, 60_000);

  it("passes a clean file, and comments", () => {
    expect(storageRefs("// localStorage is reached through the facade\nreadStored('klyx-theme')")).toEqual([]);
  });
});
