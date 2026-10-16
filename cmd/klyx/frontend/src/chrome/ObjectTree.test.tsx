import { describe, it, expect } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { CHILD_PAGE, MAX_DEPTH, MAX_ROWS, ObjectTree, flattenTree, type PathSeg } from "./ObjectTree";

describe("ObjectTree (slice 15b)", () => {
  it("draws real structure: fields, list items counted, containers below depth 2 collapsed", () => {
    const value = { replicas: 3, ports: [80, 443], template: { spec: { containers: [{ name: "a" }] } } };
    const { getByText, container } = render(<ObjectTree value={value} />);
    expect(getByText("replicas")).toBeTruthy();
    expect(getByText("2 items")).toBeTruthy();
    // template (depth 0) and spec (depth 1) open; containers (depth 2) collapsed
    expect(container.querySelector('[aria-label="Expand containers"]')).toBeTruthy();
  });

  it("bounds a 100,000-item shallow list: one page drawn, a bounded Show more, truthful counts (design review 1)", () => {
    const value = { values: new Array(100_000).fill(0) };
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
    const rows = flattenTree(wide, [], toggles, new Map());
    expect(rows.filter((r) => r.t !== "stop").length).toBe(MAX_ROWS);
    expect(rows[rows.length - 1].t).toBe("stop");
  });

  it("stops at MAX_DEPTH and says the rest is nested deeper", () => {
    let deep: unknown = { leaf: 1 };
    for (let i = 0; i < MAX_DEPTH + 3; i++) deep = { n: deep };
    const toggles = new Map<string, boolean>();
    let id = "";
    for (let i = 0; i < MAX_DEPTH + 3; i++) { id = `${id}/"n"`; toggles.set(id, true); }
    const rows = flattenTree(deep, [], toggles, new Map());
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
    const { getByText, getAllByText } = render(<ObjectTree value={{ mode: "fast", labels: { team: "a" }, ports: [80] }} base={[{ k: "p", n: "spec" }]} titleOf={titleOf} />);
    expect(getByText("mode").getAttribute("title")).toBe("string · How fast.");
    expect(getByText("team").getAttribute("title")).toBe("string");
    expect(getAllByText("1")[0].getAttribute("title")).toBe("integer");
  });
});
