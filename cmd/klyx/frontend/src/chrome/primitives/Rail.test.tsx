import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { Rail, type RailStep } from "./Rail";

// The Line slice 8a: Rail's segments are typed, styled by type, and never
// carried by style alone - every type's words are read, and the meaningful
// ones are shown.

const step = (key: string, segment: RailStep["segment"], over: Partial<RailStep> = {}): RailStep => ({ key, segment, name: key, ...over });

describe("Rail", () => {
  it("is a named list; the root has no incoming segment", () => {
    const { getByRole } = render(<Rail label="Dependencies" steps={[step("root", "reported"), step("src", "source")]} />);
    const list = getByRole("list", { name: "Dependencies" });
    const items = list.querySelectorAll(":scope > li");
    expect(items).toHaveLength(2);
    expect(items[0].getAttribute("data-segment")).toBeNull();
    expect(items[0].querySelector("[data-seg-in]")).toBeNull();
    expect(items[0].textContent).toBe("root");
    expect(items[1].getAttribute("data-segment")).toBe("source");
  });

  it("reads every segment's words, and shows the ones that carry meaning", () => {
    const steps = [
      step("root", "chain"),
      step("a", "source"),
      step("b", "dependsOn"),
      step("c", "reported"),
      step("d", "incomplete", { segmentWords: "incomplete: cycle" }),
      step("e", "completed"),
      step("f", "failed"),
      step("g", "cancelled"),
      step("h", "chain"),
    ];
    const { container } = render(<Rail label="r" steps={steps} />);
    const text = (k: string) => container.querySelector(`[data-step="${k}"]`)?.textContent;
    const shownWords = (k: string) => container.querySelector(`[data-step="${k}"] [data-seg-words]`)?.textContent ?? null;
    expect(text("a")).toBe("source: a");
    expect(shownWords("a")).toBeNull(); // read, not shown
    expect(text("b")).toBe("depends on: b");
    expect(text("c")).toBe("reported blocker: c");
    expect(shownWords("c")).toBe("reported blocker: ");
    expect(text("d")).toBe("incomplete: cycle: d");
    expect(shownWords("d")).toBe("incomplete: cycle: ");
    expect(text("e")).toBe("completed: e");
    expect(shownWords("f")).toBe("failed: ");
    expect(shownWords("g")).toBe("cancelled: ");
    expect(text("h")).toBe("h"); // a chain link has no words
  });

  it("styles each segment by its type: the outgoing line takes the next step's", () => {
    const { container } = render(<Rail label="r" steps={[step("root", "chain"), step("rep", "reported"), step("inc", "incomplete")]} />);
    const out = (k: string) => (container.querySelector(`[data-step="${k}"] > [data-seg-out]`) as HTMLElement | null)?.style.borderLeft ?? "";
    const inn = (k: string) => (container.querySelector(`[data-step="${k}"] > [data-seg-in]`) as HTMLElement | null)?.style.borderLeft ?? "";
    expect(out("root")).toContain("2px solid");
    expect(inn("rep")).toContain("2px solid");
    expect(out("rep")).toContain("dashed");
    expect(inn("inc")).toContain("dashed");
    expect(container.querySelector(`[data-step="inc"] > [data-seg-out]`)).toBeNull(); // the last step
    for (const el of container.querySelectorAll("[data-seg-in], [data-seg-out]")) expect(el.getAttribute("aria-hidden")).toBe("true");
  });

  it("nests branches as rails of their own, each with its first segment", () => {
    const steps = [
      step("ks", "chain", {
        tags: ["upstream failure"],
        branches: [[step("dep-a", "reported")], [step("dep-b", "dependsOn"), step("src-b", "source")]],
      }),
    ];
    const { container } = render(<Rail label="r" steps={steps} />);
    const branches = container.querySelectorAll('[data-step="ks"] > [data-branch]');
    expect(branches).toHaveLength(2);
    expect(branches[0].querySelector('[data-step="dep-a"]')?.getAttribute("data-segment")).toBe("reported");
    expect(branches[0].querySelector('[data-step="dep-a"] > [data-seg-in]')).not.toBeNull();
    expect(branches[1].querySelectorAll(":scope > li")).toHaveLength(2);
    expect(container.querySelector('[data-step="ks"] [data-tag]')?.textContent).toBe("upstream failure");
  });

  it("joins branches to their parent: a trunk below the parent, an elbow in each branch's segment style", () => {
    const steps = [step("ks", "chain", { branches: [[step("dep-a", "reported")], [step("dep-b", "dependsOn")]] })];
    const { container } = render(<Rail label="r" steps={steps} />);
    expect(container.querySelector('[data-step="ks"] > [data-trunk]')).not.toBeNull();
    const elbowA = container.querySelector('[data-step="dep-a"] > [data-elbow]') as HTMLElement;
    const elbowB = container.querySelector('[data-step="dep-b"] > [data-elbow]') as HTMLElement;
    expect(elbowA.style.borderTop).toContain("2px solid");
    expect(elbowB.style.borderTop).toContain("2px solid"); // 2px lines throughout (slice 13c.6)
    // a top-level step keeps its stub, never an elbow
    const { container: flat } = render(<Rail label="r2" steps={[step("a", "chain"), step("b", "source")]} />);
    expect(flat.querySelector("[data-elbow]")).toBeNull();
  });

  it("draws an aspect when given, and a quiet box otherwise; marks are hidden", () => {
    const { container } = render(<Rail label="r" steps={[step("x", "chain", { aspect: "fault" }), step("y", "source")]} />);
    expect(container.querySelector('[data-step="x"] [data-aspect="fault"]')).not.toBeNull();
    expect(container.querySelector('[data-step="y"] [data-aspect]')).toBeNull();
  });
});
