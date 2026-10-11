import { describe, it, expect, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import { ActivityView } from "./ActivityView";

beforeEach(() => { useFleet.setState({ actionLog: [] }); });

describe("ActivityView on the workspace template (redesign step 7d)", () => {
  it("empty log: one empty state", () => {
    render(<ActivityView />);
    expect(screen.getByText("No actions recorded yet.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Clear history" })).toBeNull();
  });

  it("a success is a fact, not a lamp; failures are counted and marked", () => {
    useFleet.setState({ actionLog: [
      { id: 2, at: Date.now(), kind: "error", message: "drain failed: forbidden" },
      { id: 1, at: Date.now(), kind: "success", message: "Reconciled apps" },
    ] as never });
    const { container } = render(<ActivityView />);
    expect(screen.getByText("▲ 1 failed")).toBeTruthy();
    expect(screen.getByLabelText("failed").textContent).toBe("▲");
    expect(screen.getByLabelText("succeeded").textContent).toBe("✓");
    expect(container.innerHTML).not.toMatch(/color-text-success/);
    fireEvent.click(screen.getByRole("button", { name: "Clear history" }));
    expect(useFleet.getState().actionLog).toEqual([]);
  });
});
