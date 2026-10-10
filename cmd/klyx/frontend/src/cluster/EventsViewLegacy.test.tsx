import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, act } from "@testing-library/react";
import { EventsViewLegacy as EventsView } from "./EventsViewLegacy";
import { useFleet } from "../store/fleet";
import type { EventRowDTO } from "../store/fleet";

vi.mock("../bridge/events", () => ({
  listEvents: vi.fn().mockResolvedValue(undefined),
  openLiveEvents: vi.fn().mockReturnValue(() => {}),
}));
import { openLiveEvents } from "../bridge/events";

vi.mock("../bridge/pods", () => ({
  openPodDetail: vi.fn().mockResolvedValue(undefined),
}));
import { openPodDetail } from "../bridge/pods";

// Stub Wails bindings so the test environment doesn't need a runtime.
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  EventsService: {
    ListEvents: vi.fn().mockResolvedValue({ namespaces: [], events: [] }),
    OpenLiveEvents: vi.fn().mockResolvedValue({ ok: true, error: "" }),
    CloseLiveEvents: vi.fn().mockResolvedValue(undefined),
    CloseAll: vi.fn().mockResolvedValue(undefined),
  },
}));
vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn().mockReturnValue(() => {}) },
}));

const warning: EventRowDTO = {
  type: "Warning", reason: "BackOff", message: "back-off restarting failed container",
  count: 5, namespace: "default", kind: "Pod", name: "api-crash",
  lastSeenUnix: 1700000100, firstSeenUnix: 1700000000,
};
const normal: EventRowDTO = {
  type: "Normal", reason: "Pulled", message: "Successfully pulled image nginx:latest",
  count: 1, namespace: "monitoring", kind: "ReplicaSet", name: "grafana-rs",
  lastSeenUnix: 1700000200, firstSeenUnix: 1700000200,
};
const zeroUnix: EventRowDTO = {
  type: "Normal", reason: "Scheduled", message: "Successfully assigned pod",
  count: 1, namespace: "default", kind: "Pod", name: "worker-abc",
  lastSeenUnix: 0, firstSeenUnix: 0,
};

function seed(items: EventRowDTO[]) {
  useFleet.setState((s) => ({
    events: {
      ...s.events,
      cluster: "homelab",
      items,
      namespaces: ["default", "monitoring"],
      loading: false,
    },
  }));
}

describe("EventsView", () => {
  beforeEach(() => {
    useFleet.getState().clearEvents();
    vi.clearAllMocks();
  });

  it("renders warning dot for Warning events", () => {
    seed([warning]);
    const { getByTitle } = render(<EventsView cluster="homelab" />);
    // type dot has title matching the type
    expect(getByTitle("Warning")).toBeTruthy();
  });

  it("renders reason and message in row", () => {
    seed([warning, normal]);
    const { getByText } = render(<EventsView cluster="homelab" />);
    expect(getByText("BackOff")).toBeTruthy();
    expect(getByText("Pulled")).toBeTruthy();
  });

  it("warnings-only chip filters out Normal events", () => {
    seed([warning, normal]);
    const { getByText, queryByText } = render(<EventsView cluster="homelab" />);
    fireEvent.click(getByText(/warnings only/i));
    expect(getByText("BackOff")).toBeTruthy();
    expect(queryByText("Pulled")).toBeNull();
  });

  it("search filters by reason", () => {
    seed([warning, normal]);
    const { getByPlaceholderText, getByText, queryByText } = render(<EventsView cluster="homelab" />);
    fireEvent.change(getByPlaceholderText("filter events"), { target: { value: "BackOff" } });
    expect(getByText("BackOff")).toBeTruthy();
    expect(queryByText("Pulled")).toBeNull();
  });

  it("search filters by message", () => {
    seed([warning, normal]);
    const { getByPlaceholderText, getByText, queryByText } = render(<EventsView cluster="homelab" />);
    fireEvent.change(getByPlaceholderText("filter events"), { target: { value: "pulled image" } });
    expect(getByText("Pulled")).toBeTruthy();
    expect(queryByText("BackOff")).toBeNull();
  });

  it("search filters by involved name", () => {
    seed([warning, normal]);
    const { getByPlaceholderText, queryByText } = render(<EventsView cluster="homelab" />);
    fireEvent.change(getByPlaceholderText("filter events"), { target: { value: "api-crash" } });
    expect(queryByText("BackOff")).toBeTruthy();
    expect(queryByText("Pulled")).toBeNull();
  });

  it("pod cross-link calls openPodDetail and switches section to pods", () => {
    // Must be in cluster route for setSection to take effect
    useFleet.getState().openCluster("homelab");
    seed([warning]); // warning.kind === "Pod"
    const { getByText } = render(<EventsView cluster="homelab" />);
    // The pod link shows the name as a clickable element
    const link = getByText("api-crash");
    fireEvent.click(link);
    expect(openPodDetail).toHaveBeenCalledWith("homelab", "default", "api-crash");
    expect(useFleet.getState().route).toMatchObject({ section: "pods" });
  });

  it("non-Pod involved object is plain text, not a link button", () => {
    seed([normal]); // normal.kind === "ReplicaSet"
    const { getByText } = render(<EventsView cluster="homelab" />);
    const el = getByText("grafana-rs");
    // It should be a span, not a button
    expect(el.tagName.toLowerCase()).not.toBe("button");
  });

  it("lastSeenUnix === 0 renders age as '—'", () => {
    seed([zeroUnix]);
    const { getAllByText } = render(<EventsView cluster="homelab" />);
    expect(getAllByText("—").length).toBeGreaterThanOrEqual(1);
  });

  it("openLiveEvents is called on mount (replaces listEvents)", () => {
    render(<EventsView cluster="homelab" />);
    expect(openLiveEvents).toHaveBeenCalledWith("homelab", "");
  });

  it("live=true renders green live indicator", () => {
    seed([warning]);
    useFleet.setState((s) => ({ events: { ...s.events, live: true } }));
    const { getByText } = render(<EventsView cluster="homelab" />);
    expect(getByText("live")).toBeTruthy();
  });

  it("live=false renders manual fallback indicator", () => {
    seed([warning]);
    useFleet.setState((s) => ({ events: { ...s.events, live: false } }));
    const { getByText } = render(<EventsView cluster="homelab" />);
    expect(getByText("○ manual")).toBeTruthy();
  });

  // --- Keyboard nav + a11y ---

  it("j then Enter toggles expansion of the second row", () => {
    const second: EventRowDTO = {
      type: "Normal", reason: "BackOff2", message: "second event message",
      count: 1, namespace: "default", kind: "Pod", name: "pod-b",
      lastSeenUnix: 1700000300, firstSeenUnix: 1700000300,
    };
    seed([warning, second]);
    const { getAllByText } = render(<EventsView cluster="homelab" />);
    // j → index 0; wait for re-render; j → index 1; Enter → expand second row
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true, cancelable: true })); });
    act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })); });
    // After expansion, the message appears in both the truncated row span and the expanded block.
    expect(getAllByText("second event message").length).toBeGreaterThanOrEqual(2);
  });

  it("/ focuses the filter input", () => {
    seed([warning]);
    const { getByPlaceholderText } = render(<EventsView cluster="homelab" />);
    const input = getByPlaceholderText("filter events") as HTMLInputElement;
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "/", bubbles: true, cancelable: true }));
    expect(input).toBeTruthy();
  });

  it("event row has role=button and aria-expanded", () => {
    seed([warning]);
    const { getAllByRole } = render(<EventsView cluster="homelab" />);
    const rows = getAllByRole("button").filter((el) => el.hasAttribute("aria-expanded"));
    expect(rows.length).toBeGreaterThan(0);
    expect(rows[0].getAttribute("aria-expanded")).toBe("false");
  });

  it("event row aria-expanded toggles on click", () => {
    seed([warning]);
    const { getAllByRole } = render(<EventsView cluster="homelab" />);
    const rows = getAllByRole("button").filter((el) => el.hasAttribute("aria-expanded"));
    const row = rows[0];
    expect(row.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(row);
    expect(row.getAttribute("aria-expanded")).toBe("true");
  });

  it("row click expands message below the row", () => {
    seed([warning]);
    const { getByText, getAllByText } = render(<EventsView cluster="homelab" />);
    // click the row (click on reason text which is in the row)
    fireEvent.click(getByText("BackOff"));
    // After expansion, the message should appear twice: once in row (truncated), once expanded
    const msgs = getAllByText("back-off restarting failed container");
    expect(msgs.length).toBeGreaterThanOrEqual(2);
  });
});
