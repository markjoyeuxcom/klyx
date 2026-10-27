import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { PlanView } from "./PlanView";
import type { Plan } from "../bridge/plan";

vi.mock("../bridge/configsvc", () => ({ armProtected: vi.fn() }));

const plan = (over: Partial<Plan>): Plan => ({ section: "ai", kind: "file", digest: "p1", fileVersion: "v", runtime: {}, changes: [], ends: [], continues: [], writes: false, activates: false, ...over });

// Settings slice F §5-§6; the F2a implementation review's probes, adopted.
describe("PlanView", () => {
  it("R5: a fresh Retry plan that only activates offers Apply, and says so", () => {
    render(<PlanView plan={plan({ activates: true })} busy={false} onApply={vi.fn()} onCancel={vi.fn()} interlockTitle="Protected egress" />);
    expect(screen.getByRole("button", { name: "Apply" })).toBeTruthy();
    expect(screen.queryByText("Nothing changes: these are the settings in use.")).toBeNull();
    expect(screen.getByTestId("plan-activates").textContent).toContain("applying starts them");
  });

  it("offers nothing to apply when the backend says nothing changes", () => {
    render(<PlanView plan={plan({})} busy={false} onApply={vi.fn()} onCancel={vi.fn()} interlockTitle="Protected egress" />);
    expect(screen.queryByRole("button", { name: "Apply" })).toBeNull();
    expect(screen.getByText("Nothing changes: these are the settings in use.")).toBeTruthy();
  });

  it("R4: a confirmed step's request is said to continue, its verification to end", () => {
    render(<PlanView plan={plan({ kind: "save", writes: true, activates: true,
      ends: [{ id: "step:c1:1:verify", kind: "step", cluster: "lab", what: "the verification of step 1 of the action plan on lab: it ends unverified" }],
      continues: [{ id: "step:c1:1:request", kind: "step", cluster: "lab", what: "step 1 of the action plan on lab: its confirmed request goes ahead, and may still write" }] })}
      busy={false} onApply={vi.fn()} onCancel={vi.fn()} interlockTitle="Protected egress" />);
    expect(screen.getByTestId("plan-ends").textContent).toContain("the verification of step 1");
    expect(screen.getByText("Continues")).toBeTruthy();
    expect(screen.getByRole("region").textContent).toContain("its confirmed request goes ahead");
  });

  // Settings S2 §4.2: a fleet plan says what its Apply does
  it("says what a fleet plan's Apply does, by kind and class", () => {
    const fleet = (over: Partial<Plan>) => plan({ section: "fleet", activates: true, ...over });
    const { rerender } = render(<PlanView plan={fleet({ kind: "file", class: "removal" })} busy={false} onApply={vi.fn()} onCancel={vi.fn()} interlockTitle="Remove dev?" />);
    expect(screen.getByRole("region").textContent).toContain("fleet.yaml no longer lists this cluster: applying disconnects it and writes nothing.");
    rerender(<PlanView plan={fleet({ kind: "revert", class: "reconnect", writes: true })} busy={false} onApply={vi.fn()} onCancel={vi.fn()} interlockTitle="Revert dev?" />);
    expect(screen.getByRole("region").textContent).toContain("writes the terms that last connected back to fleet.yaml");
    rerender(<PlanView plan={fleet({ kind: "remove", class: "removal" })} busy={false} onApply={vi.fn()} onCancel={vi.fn()} interlockTitle="Remove dev?" />);
    expect(screen.getByRole("region").textContent).toContain("fleet.yaml has no entry for it to remove");
  });
});
