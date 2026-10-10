import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { TriageTape, tapeRepollMs } from "./TriageTape";
import { useFleet } from "../store/fleet";
import type { TapeCounts } from "../store/fleet";
import type { VerdictObservation } from "../health/verdict";

vi.mock("../bridge/tape", () => ({ fetchTape: vi.fn().mockResolvedValue(undefined) }));
import { fetchTape } from "../bridge/tape";

// obs builds a verdict observation: fault/degraded/unknown counts (+ ok rows), capped or not.
function obs(fault = 0, degraded = 0, unknown = 0, partial = false): VerdictObservation {
  const ok = 3;
  return { coverage: partial ? "partial" : "complete", counts: { total: fault + degraded + unknown + ok, fault, degraded, unknown, progressing: 0, paused: 0, ok, attention: fault + degraded } };
}

const quiet: TapeCounts = { workloads: obs(), pods: obs(), events: 0, nodes: obs(), helm: 0, flux: 0, argo: 0 };

function seed(counts: Partial<TapeCounts>) {
  useFleet.setState({ tape: { cluster: "nelli", loading: false, counts: { ...quiet, ...counts } } });
}

describe("TriageTape", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.getState().clearTape();
    useFleet.setState({ route: { name: "cluster", cluster: "nelli", section: "pods" } });
  });

  it("fetches once per cluster entry", () => {
    seed({});
    render(<TriageTape cluster="nelli" />);
    expect(fetchTape).toHaveBeenCalledWith("nelli");
  });

  it("all-zero counts state 'everything is quiet' with no chips", () => {
    seed({});
    const { getByText, getAllByRole } = render(<TriageTape cluster="nelli" />);
    expect(getByText("everything is quiet")).toBeTruthy();
    // The only button left is the refresh affordance - no alert chips.
    const buttons = getAllByRole("button");
    expect(buttons).toHaveLength(1);
    expect(buttons[0].getAttribute("aria-label")).toBe("refresh triage");
  });

  it("refresh button refetches the same cluster", () => {
    seed({});
    const { getByRole } = render(<TriageTape cluster="nelli" />);
    expect(fetchTape).toHaveBeenCalledTimes(1);
    fireEvent.click(getByRole("button", { name: "refresh triage" }));
    expect(fetchTape).toHaveBeenCalledTimes(2);
    expect(fetchTape).toHaveBeenLastCalledWith("nelli");
  });

  it("re-polls on the gentle interval and stops on unmount", () => {
    vi.useFakeTimers();
    try {
      seed({});
      const { unmount } = render(<TriageTape cluster="nelli" />);
      expect(fetchTape).toHaveBeenCalledTimes(1);
      vi.advanceTimersByTime(tapeRepollMs);
      expect(fetchTape).toHaveBeenCalledTimes(2);
      vi.advanceTimersByTime(tapeRepollMs);
      expect(fetchTape).toHaveBeenCalledTimes(3);
      unmount();
      vi.advanceTimersByTime(tapeRepollMs * 3);
      expect(fetchTape).toHaveBeenCalledTimes(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it("setTapeLoading keeps counts for the same cluster (no re-poll flicker) and resets for a new one", () => {
    seed({ workloads: obs(2) });
    useFleet.getState().setTapeLoading("nelli");
    expect(useFleet.getState().tape.counts.workloads).toEqual(obs(2));
    expect(useFleet.getState().tape.loading).toBe(true);
    useFleet.getState().setTapeLoading("blue");
    expect(useFleet.getState().tape.counts.workloads).toBe("unreadable");
  });

  it("nonzero counts render chips (singularized); click jumps to the filtered lens", () => {
    seed({ workloads: obs(1), events: 3 });
    const { getByRole, getByText } = render(<TriageTape cluster="nelli" />);
    expect(getByText("everything else is quiet")).toBeTruthy();
    expect(getByRole("button", { name: "3 warning events" })).toBeTruthy();
    fireEvent.click(getByRole("button", { name: "1 workload needs attention" }));
    const st = useFleet.getState();
    expect(st.route).toMatchObject({ name: "cluster", section: "workloads" });
    expect(st.workloads.needsAttention).toBe(true);
  });

  it("argo chip navigates to the argo section", () => {
    seed({ argo: 2 });
    const { getByRole } = render(<TriageTape cluster="nelli" />);
    fireEvent.click(getByRole("button", { name: "2 argo apps not synced" }));
    expect(useFleet.getState().route).toMatchObject({ section: "argo" });
  });

  it("unreadable lenses are never claimed quiet", () => {
    useFleet.setState({ tape: { cluster: "nelli", loading: false, counts: { workloads: "unreadable", pods: "unreadable", events: "unreadable", nodes: "unreadable", helm: "unreadable", flux: "unreadable", argo: "unreadable" } } });
    const { getByText } = render(<TriageTape cluster="nelli" />);
    expect(getByText("triage unavailable")).toBeTruthy();
  });

  it("absent tools are expected, not unreadable - a Flux-only cluster is quiet", () => {
    seed({ argo: "absent", helm: "absent" });
    const { getByText, queryByText } = render(<TriageTape cluster="nelli" />);
    expect(getByText("everything is quiet")).toBeTruthy();
    expect(queryByText(/unreadable/)).toBeNull();
  });

  it("genuinely unreadable lenses are counted in the trailer", () => {
    useFleet.setState({ tape: { cluster: "nelli", loading: false, counts: { workloads: obs(2), pods: "unreadable", events: 0, nodes: "unreadable", helm: "absent", flux: 0, argo: "absent" } } });
    const { getByText, getByRole } = render(<TriageTape cluster="nelli" />);
    expect(getByRole("button", { name: "2 workloads need attention" })).toBeTruthy();
    expect(getByText("2 lenses unreadable")).toBeTruthy();
  });

  it("Warning events alone are evidence: clickable, never lit, never an alert", () => {
    useFleet.setState((s) => ({
      tape: { ...s.tape, cluster: "kind", loading: false,
        counts: { workloads: obs(), pods: obs(), events: 12, nodes: obs(), helm: "absent", flux: "absent", argo: "absent" } },
    }));
    const { getByRole, getByText } = render(<TriageTape cluster="kind" />);
    expect(getByRole("button", { name: "12 warning events" })).toBeTruthy();
    expect(getByText("everything is quiet")).toBeTruthy();
  });

  it("a capped zero-attention read is never quiet", () => {
    seed({ pods: obs(0, 0, 0, true), helm: "absent", flux: "absent", argo: "absent" });
    const { getByText, queryByText } = render(<TriageTape cluster="nelli" />);
    expect(getByText("unverified (capped)")).toBeTruthy();
    expect(queryByText("everything is quiet")).toBeNull();
    expect(getByText(/1 lens unverified/)).toBeTruthy();
  });

  it("an all-unknown read is never quiet", () => {
    seed({ nodes: obs(0, 0, 2), helm: "absent", flux: "absent", argo: "absent" });
    const { getByText, queryByText } = render(<TriageTape cluster="nelli" />);
    expect(getByText("2 unverified")).toBeTruthy();
    expect(queryByText("everything is quiet")).toBeNull();
  });

  it("degraded-only attention is amber; any fault is danger", () => {
    seed({ workloads: obs(0, 2), pods: obs(1, 1), helm: "absent", flux: "absent", argo: "absent" });
    const { getByRole } = render(<TriageTape cluster="nelli" />);
    const wl = getByRole("button", { name: "2 workloads need attention" });
    const pods = getByRole("button", { name: "2 pods need attention" });
    expect(wl.innerHTML).toContain("warning");
    expect(wl.innerHTML).not.toContain("danger");
    expect(pods.innerHTML).toContain("danger");
  });
});

