import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, act, within, screen } from "@testing-library/react";
import { EventsView, destinationOf } from "./EventsView";
import { useFleet } from "../store/fleet";
import type { EventRowDTO } from "../store/fleet";

vi.mock("../bridge/events", () => ({
  listEvents: vi.fn().mockResolvedValue(undefined),
  openLiveEvents: vi.fn().mockReturnValue(() => {}),
}));
vi.mock("../bridge/pods", () => ({ openPodDetail: vi.fn().mockResolvedValue(undefined) }));
import { openPodDetail } from "../bridge/pods";

const nowS = Math.floor(Date.now() / 1000);
const ev = (over: Partial<EventRowDTO>): EventRowDTO => ({
  type: "Normal", reason: "Pulled", message: "Successfully pulled image", count: 1, namespace: "apps", kind: "Pod", name: "web-1",
  lastSeenUnix: nowS - 60, firstSeenUnix: nowS - 120, ...over,
});
function seed(items: EventRowDTO[], over: Record<string, unknown> = {}) {
  useFleet.setState((s) => ({ events: { ...s.events, cluster: "x", namespace: "", items, namespaces: ["apps"], loading: false, live: true, truncated: false, error: null, rowsScope: "x|", ...over } }));
}
const insp = () => within(screen.getByTestId("lens-inspector"));
const rowsText = () => [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");

describe("EventsView (redesign step 5)", () => {
  beforeEach(() => { useFleet.getState().clearEvents(); vi.clearAllMocks(); });

  it("warnings are evidence: amber text, no lamp anywhere", () => {
    seed([ev({ type: "Warning", reason: "BackOff" }), ev({})]);
    const { container } = render(<EventsView cluster="x" />);
    expect(screen.getByText("1 warnings")).toBeTruthy();
    expect(container.querySelector('[role="img"]')).toBeNull();
    const warn = screen.getAllByText("Warning").find((el) => el.closest(".lens-row")) as HTMLElement;
    expect(warn.style.color).toBe("var(--color-text-warning)");
  });

  it("the full message lives in the inspector, wrapped - never truncated-only", () => {
    const long = "0/3 nodes are available: 1 node(s) had untolerated taint {node-role.kubernetes.io/control-plane: }, 2 Insufficient memory. preemption: 0/3 nodes are available";
    seed([ev({ type: "Warning", reason: "FailedScheduling", message: long })]);
    render(<EventsView cluster="x" />);
    fireEvent.click(document.querySelector(".lens-row") as HTMLElement);
    expect(insp().getByText(long)).toBeTruthy();
    expect(insp().getByText("FailedScheduling")).toBeTruthy();
  });

  it("a failed first read is unreadable, never 'no events'; a failed re-read keeps rows marked *", () => {
    seed([], { error: "events is forbidden", rowsScope: null, live: false });
    const first = render(<EventsView cluster="x" />);
    expect(screen.getByText("Could not read events.")).toBeTruthy();
    expect(screen.queryByText(/^No events/)).toBeNull();
    first.unmount();
    seed([ev({})], { error: "timeout", live: false });
    render(<EventsView cluster="x" />);
    expect(screen.getByText("1*")).toBeTruthy();
    expect(screen.getByText(/showing the last good read/)).toBeTruthy();
  });

  it("before the first read it is loading, never empty", () => {
    seed([], { rowsScope: null, loading: true, live: false });
    render(<EventsView cluster="x" />);
    expect(screen.queryByText(/^No events/)).toBeNull();
  });

  it("the 500-event cap is stated in words with a lower-bound count", () => {
    seed([ev({})], { truncated: true });
    render(<EventsView cluster="x" />);
    expect(screen.getByText("1+")).toBeTruthy();
    expect(screen.getByText(/First 500 events only/)).toBeTruthy();
  });

  it("an honest empty state names the scope and that quiet can be real", () => {
    seed([], { namespace: "apps", rowsScope: "x|apps" });
    render(<EventsView cluster="x" />);
    expect(screen.getByText("No events in apps.")).toBeTruthy();
    expect(screen.getByText(/quiet can be real/)).toBeTruthy();
  });

  it("Warnings chip filters; search covers reason, object and message", () => {
    seed([ev({ type: "Warning", reason: "BackOff", name: "crash" }), ev({ reason: "Pulled", name: "ok", message: "pulled nginx" })]);
    render(<EventsView cluster="x" />);
    fireEvent.click(screen.getByRole("button", { name: /^Warnings/ }));
    expect(rowsText().length).toBe(1);
    fireEvent.click(screen.getByRole("button", { name: "All" }));
    fireEvent.change(screen.getByPlaceholderText("Search reason, object, message"), { target: { value: "nginx" } });
    expect(rowsText().length).toBe(1);
    expect(rowsText()[0]).toContain("ok");
  });

  it("an aged-out selected event keeps its last evidence; a failed re-read does not claim it aged out", () => {
    const gone = ev({ type: "Warning", reason: "BackOff", name: "gone" });
    seed([gone, ev({ name: "other" })]);
    render(<EventsView cluster="x" />);
    fireEvent.click(screen.getByText("gone").closest(".lens-row") as HTMLElement);
    act(() => seed([ev({ name: "other" })], { error: "timeout" }));
    expect(screen.queryByText("Event no longer listed.")).toBeNull();
    act(() => seed([ev({ name: "other" })]));
    expect(insp().getByText("Event no longer listed.")).toBeTruthy();
    expect(insp().queryByRole("button", { name: "Open Pod" })).toBeNull();
  });

  it("Open Pod navigates to the pod; ambiguous kinds fail closed", () => {
    seed([ev({ type: "Warning", reason: "BackOff" })]);
    render(<EventsView cluster="x" />);
    fireEvent.click(document.querySelector(".lens-row") as HTMLElement);
    fireEvent.click(insp().getByRole("button", { name: "Open Pod" }));
    expect(openPodDetail).toHaveBeenCalledWith("x", "apps", "web-1");
    expect(destinationOf("x", ev({ kind: "Widget" }))).toBeNull();
    expect(destinationOf("x", ev({ kind: "Deployment" }))?.label).toBe("Open Deployment");
    expect(destinationOf("x", ev({ kind: "Kustomization" }))?.label).toBe("Open Kustomization");
  });

  it("changing namespace drops the selection (a new scope)", () => {
    seed([ev({})]);
    render(<EventsView cluster="x" />);
    fireEvent.click(document.querySelector(".lens-row") as HTMLElement);
    expect(screen.getByTestId("event-panel")).toBeTruthy();
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "apps" } });
    expect(screen.queryByTestId("event-panel")).toBeNull();
  });
});

describe("EventsView lifecycle corrections (Codex 8de24158)", () => {
  beforeEach(() => { useFleet.getState().clearEvents(); vi.clearAllMocks(); });

  it("missing from a capped read proves nothing; only an uncapped complete read says it aged out", () => {
    const e = ev({ type: "Warning", reason: "BackOff", name: "gone" });
    seed([e, ev({ name: "other" })], { truncated: true });
    render(<EventsView cluster="x" />);
    fireEvent.click(screen.getByText("gone").closest(".lens-row") as HTMLElement);
    act(() => seed([ev({ name: "other" })], { truncated: true }));
    expect(insp().getByText("Not in the current partial read.")).toBeTruthy();
    expect(screen.queryByText("Event no longer listed.")).toBeNull();
    act(() => seed([ev({ name: "other" })], { truncated: false }));
    expect(insp().getByText("Event no longer listed.")).toBeTruthy();
  });

  it("A -> B never carries a selection, even onto a colliding row key", () => {
    const e = ev({ name: "same" });
    seed([e]);
    const { rerender } = render(<EventsView cluster="x" />);
    fireEvent.click(document.querySelector(".lens-row") as HTMLElement);
    expect(screen.getByTestId("event-panel")).toBeTruthy();
    rerender(<EventsView cluster="y" />);
    act(() => useFleet.setState((s) => ({ events: { ...s.events, cluster: "y", namespace: "", rowsScope: "y|", items: [e], loading: false } })));
    expect(screen.queryByTestId("event-panel")).toBeNull();
    expect(screen.queryByText("Event no longer listed.")).toBeNull();
  });

  it("rows read for another scope are never shown", () => {
    seed([ev({ name: "from-other-ns" })], { namespace: "apps", rowsScope: "x|", loading: true });
    render(<EventsView cluster="x" />);
    expect(rowsText().length).toBe(0);
  });

  it("a later operator filter that excludes the selection closes it", () => {
    seed([ev({ name: "normal" }), ev({ type: "Warning", reason: "BackOff", name: "warn" })]);
    render(<EventsView cluster="x" />);
    fireEvent.click(screen.getByText("normal").closest(".lens-row") as HTMLElement);
    expect(screen.getByTestId("event-panel")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /^Warnings/ }));
    expect(screen.queryByTestId("event-panel")).toBeNull();
  });
});
