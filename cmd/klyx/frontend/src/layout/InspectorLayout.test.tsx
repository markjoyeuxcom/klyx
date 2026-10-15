import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { InspectorLayout, useInspectorPinContext } from "./InspectorLayout";
import { useInspectorPin } from "../nav/inspectorPin";
import { useFleet } from "../store/fleet";
import { LocationBar } from "../chrome/LocationBar";

vi.mock("../bridge/forwards", () => ({ stopForward: vi.fn(), stopAllForwards: vi.fn() }));
vi.mock("../chrome/CommandPalette", () => ({ openCommandPalette: vi.fn() }));
vi.mock("../bridge/configsvc", () => ({ openTerminal: vi.fn(async () => {}) }));
vi.mock("../bridge/healthRows", () => ({ startSummaries: vi.fn() }));

// The Line slice 4 §6: the inspector's pin, its context, the verdict's mark
// and drawing slots, and the location bar's object crumb.

function Inside() {
  const pin = useInspectorPinContext();
  return <span data-testid="inside">{pin ? `${pin.cluster}:${pin.uid ?? "-"}:${pin.kind}/${pin.namespace ?? ""}/${pin.name}` : "none"}</span>;
}
const Insp = ({ cluster = "dev", uid, name = "api-1" }: { cluster?: string; uid?: string; name?: string }) => (
  <InspectorLayout pin={{ cluster, uid }} kind="Pod" namespace="shop" name={name} onClose={() => {}} verdict={<span>Failing.</span>} aspect="fault" drawing={<span data-testid="drawing">d</span>}>
    <Inside />
  </InspectorLayout>
);

beforeEach(() => {
  useInspectorPin.setState({ held: null });
  useFleet.setState({ clusters: [{ name: "dev", state: "Synced" } as never], route: { name: "cluster", cluster: "dev", section: "pods" }, forwards: [] });
});

describe("InspectorLayout pin (§6.3)", () => {
  it("provides its pin to what renders inside, and publishes it while mounted", () => {
    const r = render(<Insp uid="u1" />);
    expect(r.getByTestId("inside").textContent).toBe("dev:u1:Pod/shop/api-1");
    expect(useInspectorPin.getState().held?.pin).toEqual({ cluster: "dev", uid: "u1", kind: "Pod", namespace: "shop", name: "api-1" });
    r.unmount();
    expect(useInspectorPin.getState().held).toBeNull();
  });

  it("an earlier inspector's release never clears a later one's pin", () => {
    const a = render(<Insp uid="u1" />);
    const b = render(<Insp uid="u2" name="api-2" />);
    a.unmount();
    expect(useInspectorPin.getState().held?.pin.uid).toBe("u2");
    b.unmount();
  });

  it("the verdict carries its mark; the drawing sits between verdict and actions", () => {
    const r = render(<Insp uid="u1" />);
    const verdict = r.container.querySelector(".insp-verdict") as HTMLElement;
    expect(verdict.querySelector('[data-aspect="fault"]')).toBeTruthy();
    expect(verdict.nextElementSibling?.getAttribute("class")).toBe("insp-drawing");
  });
});

describe("the object crumb (§6.4)", () => {
  it("names the open inspector's object; clicking it focuses the inspector", () => {
    const r = render(<><LocationBar /><Insp uid="u1" /></>);
    const crumb = r.getByRole("button", { name: "Pod api-1: go to its details" });
    expect(r.getByRole("navigation", { name: "Location" }).textContent).toMatch(/\/Pods\/Pod api-1$/);
    fireEvent.click(crumb);
    expect(document.activeElement?.id).toBe("inspector-identity");
  });

  it("a pin on another cluster gives no crumb", () => {
    const r = render(<><LocationBar /><Insp cluster="prd" uid="u1" /></>);
    expect(r.queryByRole("button", { name: /go to its details/ })).toBeNull();
  });
});

describe("Replaced (§6.3, design review 37f00bc3)", () => {
  it("says Replaced in place of the verdict, runs no action, and offers only Open the new object", () => {
    const onNew = vi.fn();
    const act1 = vi.fn();
    const r = render(
      <InspectorLayout pin={{ cluster: "dev", uid: "u1" }} kind="Pod" namespace="shop" name="api-1" onClose={() => {}} verdict={<span>Failing.</span>}
        primary={{ label: "Logs", onSelect: act1 }} more={[{ label: "Delete pod", onSelect: act1 }]} replaced={onNew}>
        <span>last evidence</span>
      </InspectorLayout>,
    );
    expect(r.getByText("Replaced.")).toBeTruthy();
    expect(r.queryByText("Failing.")).toBeNull();
    expect(r.queryByRole("button", { name: /^Logs/ })).toBeNull();
    expect(r.queryByRole("button", { name: /^More/ })).toBeNull();
    expect(r.getByText("last evidence")).toBeTruthy();
    fireEvent.click(r.getByRole("button", { name: "Open the new object" }));
    expect(onNew).toHaveBeenCalledTimes(1);
    expect(act1).not.toHaveBeenCalled();
  });
});
