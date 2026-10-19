import { describe, it, expect, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { PinnedGone, expectsOther } from "./PinnedGone";

// The Line slice 9 note §4.2: an object opened by UID that no row holds is
// said to be removed, replaced or not verified - never shown as the current
// same-name object.
describe("PinnedGone", () => {
  it("says what became of the object, with its UID", () => {
    for (const [gone, word] of [["removed", "Removed."], ["replaced", "Replaced."], ["unverified", "Not verified."]] as const) {
      const { container, unmount } = render(<PinnedGone cluster="prd" kind="Pod" namespace="shop" name="web" uid="0b6c1a2e-5f3d" gone={gone} onClose={() => {}} />);
      expect(container.querySelector(`[data-pinned-gone="${gone}"]`)?.textContent).toContain(word);
      expect(container.textContent).toContain("UID 0b6c1a2e");
      expect(container.querySelector("[data-pin-uid]")?.getAttribute("data-pin-uid")).toBe("0b6c1a2e-5f3d");
      unmount();
    }
  });

  it("offers the new object only when replaced", () => {
    const adopt = vi.fn();
    const r = render(<PinnedGone cluster="prd" kind="Pod" name="web" uid="u1" gone="replaced" onAdopt={adopt} onClose={() => {}} />);
    fireEvent.click(r.getByText("Open the new object"));
    expect(adopt).toHaveBeenCalled();
    r.rerender(<PinnedGone cluster="prd" kind="Pod" name="web" uid="u1" gone="removed" onAdopt={adopt} onClose={() => {}} />);
    expect(r.queryByText("Open the new object")).toBeNull();
  });

  it("offers the gone object's log history, pinned to its UID (coverage S8b)", () => {
    const history = vi.fn();
    const r = render(<PinnedGone cluster="prd" kind="Pod" name="web" uid="u1" gone="removed" onHistory={history} onClose={() => {}} />);
    fireEvent.click(r.getByText("Log history"));
    expect(history).toHaveBeenCalled();
    // replaced: the new object first, the history beside it
    r.rerender(<PinnedGone cluster="prd" kind="Pod" name="web" uid="u1" gone="replaced" onAdopt={() => {}} onHistory={history} onClose={() => {}} />);
    expect(r.getByText("Open the new object")).toBeTruthy();
    expect(r.getByText("Log history")).toBeTruthy();
    r.rerender(<PinnedGone cluster="prd" kind="Pod" name="web" uid="u1" gone="removed" onClose={() => {}} />);
    expect(r.queryByText("Log history")).toBeNull();
  });

  it("expectsOther: a UID the row does not have", () => {
    expect(expectsOther(undefined, "u1")).toBe(true);
    expect(expectsOther({ uid: "u2" }, "u1")).toBe(true);
    expect(expectsOther({ uid: "u1" }, "u1")).toBe(false);
    expect(expectsOther(undefined, undefined)).toBe(false);
  });
});
