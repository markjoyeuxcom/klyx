import { describe, it, expect } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { CHILD_PAGE, MAX_DEPTH, MAX_ROWS, ObjectTree, flattenTree, type PathSeg } from "./ObjectTree";
import { Arr, Num, Obj, fromPlain, parseLossless } from "./losslessJSON";

describe("ObjectTree (slice 15b)", () => {
  it("draws real structure: fields, list items counted, containers below depth 2 collapsed", () => {
    const value = fromPlain({ replicas: 3, ports: [80, 443], template: { spec: { containers: [{ name: "a" }] } } });
    const { getByText, container } = render(<ObjectTree value={value} />);
    expect(getByText("replicas")).toBeTruthy();
    expect(getByText("2 items")).toBeTruthy();
    // template (depth 0) and spec (depth 1) open; containers (depth 2) collapsed
    expect(container.querySelector('[aria-label="Expand containers"]')).toBeTruthy();
  });

  it("bounds a 100,000-item shallow list: one page drawn, a bounded Show more, truthful counts (design review 1)", () => {
    const value = fromPlain({ values: new Array(100_000).fill(0) });
    const toggles = new Map([["/\"values\"", true]]);
    const rows = flattenTree(value, [], toggles, new Map());
    expect(rows.length).toBe(1 + CHILD_PAGE + 1);
    expect(rows[rows.length - 1]).toMatchObject({ t: "more", hidden: 100_000 - CHILD_PAGE });
    const { getByText, container } = render(<ObjectTree value={value} />);
    fireEvent.click(getByText("100000 items").closest("button")!);
    fireEvent.click(getByText(`Show ${CHILD_PAGE} more (${(100_000 - CHILD_PAGE).toLocaleString()} not shown)`));
    expect(container.querySelectorAll(".otree-row").length).toBe(1 + 2 * CHILD_PAGE + 1);
  });

  it("stops the whole tree at MAX_ROWS, said, however it is expanded", () => {
    const wide: Record<string, number[]> = {};
    for (let i = 0; i < 40; i++) wide[`k${i}`] = new Array(90).fill(1);
    const toggles = new Map(Object.keys(wide).map((k) => [`/${JSON.stringify(k)}`, true] as [string, boolean]));
    const rows = flattenTree(fromPlain(wide), [], toggles, new Map());
    expect(rows.filter((r) => r.t !== "stop").length).toBe(MAX_ROWS);
    expect(rows[rows.length - 1].t).toBe("stop");
  });

  it("stops at MAX_DEPTH and says the rest is nested deeper", () => {
    let deep: unknown = { leaf: 1 };
    for (let i = 0; i < MAX_DEPTH + 3; i++) deep = { n: deep };
    const toggles = new Map<string, boolean>();
    let id = "";
    for (let i = 0; i < MAX_DEPTH + 3; i++) { id = `${id}/"n"`; toggles.set(id, true); }
    const rows = flattenTree(fromPlain(deep), [], toggles, new Map());
    expect(rows.some((r) => r.t === "deeper")).toBe(true);
    expect(Math.max(...rows.map((r) => r.depth))).toBe(MAX_DEPTH);
  });

  it("titles a field from the schema by its path, map values and array items included", () => {
    const titles = new Map<string, string>([
      [JSON.stringify([{ k: "p", n: "spec" }, { k: "p", n: "mode" }]), "string · How fast."],
      [JSON.stringify([{ k: "p", n: "spec" }, { k: "p", n: "labels" }, { k: "m" }]), "string"],
      [JSON.stringify([{ k: "p", n: "spec" }, { k: "p", n: "ports" }, { k: "i" }]), "integer"],
    ]);
    const titleOf = (p: PathSeg[]) => titles.get(JSON.stringify(p));
    const { getByText, getAllByText } = render(<ObjectTree value={fromPlain({ mode: "fast", labels: { team: "a" }, ports: [80] })} base={[{ k: "p", n: "spec" }]} titleOf={titleOf} />);
    expect(getByText("mode").getAttribute("title")).toBe("string · How fast.");
    expect(getByText("team").getAttribute("title")).toBe("string");
    expect(getAllByText("1")[0].getAttribute("title")).toBe("integer");
  });

  // implementation review 1: a page is a slice, read by index - the rest of
  // a 100,000-item list is never touched.
  it("reads only the drawn page of a container's children", () => {
    let reads = 0;
    const items = new Proxy(new Array(100_000).fill(new Num("0")), {
      get(target, prop, receiver) {
        if (typeof prop === "string" && /^\d+$/.test(prop)) reads++;
        return Reflect.get(target, prop, receiver);
      },
    });
    const rows = flattenTree(new Arr(items), [], new Map(), new Map());
    expect(rows.length).toBe(CHILD_PAGE + 1);
    expect(reads).toBeLessThanOrEqual(CHILD_PAGE);
  });

  it("draws numbers exactly as the server wrote them", () => {
    const { getByText } = render(<ObjectTree value={parseLossless('{"offset":9007199254740993,"ratio":1.50,"big":1e400,"neg":-0}')} />);
    expect(getByText("9007199254740993")).toBeTruthy();
    expect(getByText("1.50")).toBeTruthy();
    expect(getByText("1e400")).toBeTruthy();
    expect(getByText("-0")).toBeTruthy();
  });
});

describe("parseLossless", () => {
  it("keeps numbers as text, entries in order, any key as a plain entry, escapes decoded", () => {
    const v = parseLossless(' {"b": [1, 2.5e-3, true, null], "a": {"__proto__": "x", "s": "q\\"\\u00e9\\n"}} ');
    expect(v).toBeInstanceOf(Obj);
    const o = v as Obj;
    expect(o.entries.map(([k]) => k)).toEqual(["b", "a"]);
    const b = o.entries[0][1] as Arr;
    expect((b.items[0] as Num).text).toBe("1");
    expect((b.items[1] as Num).text).toBe("2.5e-3");
    expect(b.items.slice(2)).toEqual([true, null]);
    const a = o.entries[1][1] as Obj;
    expect(a.entries).toEqual([["__proto__", "x"], ["s", 'q"\u00e9\n']]);
  });

  it("refuses what is not JSON", () => {
    for (const bad of ["", "{", '{"a":}', "[1,]", "01", "tru", '{"a":1} x', '"unterminated', "NaN"]) {
      expect(() => parseLossless(bad), bad).toThrow();
    }
  });
});
