import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { CountSentence, MarkedPhrase } from "./CountSentence";

// The Line slice 11b: a lens header's count sentence.
const text = (el: HTMLElement) => [...el.children].map((c) => c.textContent);

describe("CountSentence", () => {
  it("says the total with its noun; a marked count is never singular", () => {
    const { container, rerender } = render(<div><CountSentence total={1} noun={["pod", "pods"]} /></div>);
    expect(text(container.firstChild as HTMLElement)).toEqual(["1 pod"]);
    rerender(<div><CountSentence total={2} noun={["pod", "pods"]} /></div>);
    expect(text(container.firstChild as HTMLElement)).toEqual(["2 pods"]);
    rerender(<div><CountSentence total={1} marker="+" noun={["pod", "pods"]} /></div>);
    expect(text(container.firstChild as HTMLElement)).toEqual(["1+ pods"]);
  });

  it("leads the attention part with its Aspect: fault when any counted object is a fault", () => {
    const { container, rerender } = render(<div><CountSentence total={9} noun={["pod", "pods"]}
      attention={{ count: 3, marker: "*", phrase: "need attention", fault: true, detail: "2 fault, 1 degraded", testid: "a" }} clear="none need attention" /></div>);
    const a = container.querySelector('[data-testid="a"]') as HTMLElement;
    expect(a.textContent).toBe("3* need attention (2 fault, 1 degraded)");
    expect(a.querySelector('[data-aspect="fault"]')).toBeTruthy();
    expect(a.style.color).toBe("var(--color-text-danger)");
    expect(container.textContent).not.toContain("▲");
    expect(container.textContent).not.toContain("none need attention");
    rerender(<div><CountSentence total={9} noun={["pod", "pods"]} attention={{ count: 1, phrase: "", fault: false, text: "1 failed · 2 pending", testid: "a" }} /></div>);
    const b = container.querySelector('[data-testid="a"]') as HTMLElement;
    expect(b.textContent).toBe("1 failed · 2 pending");
    expect(b.querySelector('[data-aspect="attention"]')).toBeTruthy();
  });

  it("with nothing to attend to it says the lens's all-clear or not-known words, quietly, with no mark", () => {
    const { container } = render(<div><CountSentence total={4} noun={["node", "nodes"]} attention={{ count: 0, phrase: "need attention", fault: false }} clear="no known attention">
      <span>1 cordoned</span>
    </CountSentence></div>);
    expect(text(container.firstChild as HTMLElement)).toEqual(["4 nodes", "no known attention", "1 cordoned"]);
    expect(container.querySelector("[data-aspect]")).toBeNull();
  });

  it("a marked phrase carries the Aspect in place of the ▲", () => {
    const { container } = render(<MarkedPhrase fault>upstream EOL 3d ago</MarkedPhrase>);
    expect(container.textContent).toBe("upstream EOL 3d ago");
    expect(container.querySelector('[data-aspect="fault"]')).toBeTruthy();
  });
});
