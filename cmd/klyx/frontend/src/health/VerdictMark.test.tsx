import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { VerdictMark } from "./VerdictView";
import { TABLE_ROW_HEIGHT } from "../layout/LensLayout";

// The Line slice 11a: a table row's one mark, the 40px row, and the source
// rules every lens table follows.
const fsModule = "node:fs";
const { readFileSync, readdirSync } = (await import(/* @vite-ignore */ fsModule)) as {
  readFileSync: (p: URL, enc: "utf8") => string;
  readdirSync: (p: URL) => string[];
};
const cwd = (globalThis as unknown as { process: { cwd(): string } }).process.cwd();
const src = (p: string) => readFileSync(new URL(`file://${cwd}/src/${p}`), "utf8");

describe("VerdictMark", () => {
  it("names a verdict's mark; a healthy row's empty mark is not announced", () => {
    const { container, rerender } = render(<VerdictMark health={{ kind: "fault", label: "CrashLoopBackOff" }} />);
    const mark = container.querySelector(".row-mark") as HTMLElement;
    expect(mark.getAttribute("role")).toBe("img");
    expect(mark.getAttribute("aria-label")).toBe("CrashLoopBackOff");
    expect(mark.querySelector('[data-aspect="fault"]')).toBeTruthy();
    rerender(<VerdictMark health={{ kind: "ok", label: "Running" }} />);
    const quiet = container.querySelector(".row-mark") as HTMLElement;
    expect(quiet.getAttribute("aria-hidden")).toBe("true");
    expect(quiet.getAttribute("role")).toBeNull();
    expect(quiet.querySelector("[data-aspect]")).toBeNull();
  });

  it("a stale ok row takes the dashed syncing mark; a stale fault keeps its own", () => {
    const { container, rerender } = render(<VerdictMark health={{ kind: "ok", label: "Ready" }} stale />);
    expect(container.querySelector('[data-aspect="syncing"]')).toBeTruthy();
    expect(container.querySelector(".row-mark")?.getAttribute("aria-label")).toBe("Syncing: changes not yet realized");
    rerender(<VerdictMark health={{ kind: "fault", label: "Failed" }} stale />);
    expect(container.querySelector('[data-aspect="fault"]')).toBeTruthy();
    rerender(<VerdictMark health={{ kind: "degraded", label: "needs attention" }} label="needs attention: no owner" />);
    expect(container.querySelector(".row-mark")?.getAttribute("aria-label")).toBe("needs attention: no owner");
    expect(container.querySelector(".row-mark")?.getAttribute("title")).toBe("needs attention: no owner");
  });
});

describe("lens tables (parent §2.3, §9)", () => {
  it("rows are 40px, in the constant and the CSS default, and no list passes its own", () => {
    expect(TABLE_ROW_HEIGHT).toBe(40);
    expect(src("layout/layout.css")).toMatch(/--table-row-height: 40px;/);
    for (const f of readdirSync(new URL(`file://${cwd}/src/cluster/`))) {
      if (f.endsWith(".tsx")) expect(src(`cluster/${f}`), f).not.toMatch(/rowHeight=\{\d+\}/);
    }
  });

  it("a stale row's mark is never dimmed: it carries meaning", () => {
    expect(src("layout/layout.css")).not.toMatch(/data-stale="true"\][^{]*\{[^}]*opacity/);
  });

  it("every verdict table starts with the mark column; evidence and simulations do not", () => {
    // events are evidence, not an object's verdict; an access grant is a simulation's answer
    const exempt = new Set(["AccessView.tsx GRANT_COLUMNS", "EventsView.tsx COLUMNS", "KarpenterView.tsx EVENT_COLUMNS"]);
    const seen: string[] = [];
    for (const f of readdirSync(new URL(`file://${cwd}/src/cluster/`))) {
      if (!f.endsWith(".tsx") || f.includes(".test.")) continue;
      for (const m of src(`cluster/${f}`).matchAll(/const (\w*COLUMNS)[^=]*=\s*\[\s*(\S+)/g)) {
        const id = `${f} ${m[1]}`;
        seen.push(id);
        if (!exempt.has(id)) expect(m[2], id).toBe("ROW_MARK,");
      }
    }
    expect(seen.length).toBeGreaterThan(15);
  });

  it("a status cell never repeats the mark's shape as ▲ text", () => {
    for (const f of readdirSync(new URL(`file://${cwd}/src/cluster/`))) {
      if (f.endsWith(".tsx") && !f.includes(".test.")) expect(src(`cluster/${f}`), f).not.toMatch(/=== "fault" \? "▲ " : ""\}\{role\.label\}/);
    }
  });
});
