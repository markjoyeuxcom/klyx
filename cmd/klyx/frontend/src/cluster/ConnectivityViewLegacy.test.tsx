import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import type { ConnectivityReportDTO } from "../store/fleet";
import { ConnectivityViewLegacy as ConnectivityView } from "./ConnectivityViewLegacy";

vi.mock("../bridge/connectivity", () => ({
  canReach: vi.fn(async () => ({}) as ConnectivityReportDTO),
}));
import { canReach } from "../bridge/connectivity";

const report = (over: Partial<ConnectivityReportDTO> = {}): ConnectivityReportDTO => ({
  allowed: true,
  ingress: { isolated: false, allowed: true, policies: [], explain: "ingress allowed - no policy selects the pod (not isolated)" },
  egress: { isolated: false, allowed: true, policies: [], explain: "egress allowed - no policy selects the pod (not isolated)" },
  srcLabels: { app: "web" }, dstLabels: { app: "db" }, ciliumPolicies: 0,
  ...over,
});

function fill(getByLabelText: (l: string) => Element) {
  fireEvent.change(getByLabelText("source namespace"), { target: { value: "team-a" } });
  fireEvent.change(getByLabelText("source pod"), { target: { value: "web-1" } });
  fireEvent.change(getByLabelText("destination namespace"), { target: { value: "team-b" } });
  fireEvent.change(getByLabelText("destination pod"), { target: { value: "db-1" } });
  fireEvent.change(getByLabelText("port"), { target: { value: "5432" } });
}

describe("ConnectivityView", () => {
  beforeEach(() => vi.clearAllMocks());

  it("runs the check and renders REACHABLE with per-direction explains", async () => {
    vi.mocked(canReach).mockResolvedValue(report());
    const { getByLabelText, getByText, findByText } = render(<ConnectivityView cluster="c" />);
    fill(getByLabelText);
    fireEvent.click(getByText("can it reach?"));
    expect(await findByText("REACHABLE")).toBeTruthy();
    expect(vi.mocked(canReach)).toHaveBeenCalledWith("c", "team-a", "web-1", "team-b", "db-1", 5432, "TCP");
    expect(getByText(/egress allowed/)).toBeTruthy();
    expect(getByText(/ingress allowed/)).toBeTruthy();
  });

  it("renders BLOCKED with the selecting policies as click-through rows", async () => {
    vi.mocked(canReach).mockResolvedValue(report({
      allowed: false,
      ingress: { isolated: true, allowed: false, explain: "ingress DENIED - 1 policy selects the pod, none admits this peer/port", policies: [{ namespace: "team-b", name: "deny-all", admits: false }] },
    }));
    const { getByLabelText, getByText, findByText } = render(<ConnectivityView cluster="c" />);
    fill(getByLabelText);
    fireEvent.click(getByText("can it reach?"));
    expect(await findByText("BLOCKED")).toBeTruthy();
    expect(getByText("team-b/deny-all")).toBeTruthy();
    expect(getByText("selects")).toBeTruthy();
  });

  it("flags a PARTIAL verdict when Cilium policies are in scope", async () => {
    vi.mocked(canReach).mockResolvedValue(report({ ciliumPolicies: 2 }));
    const { getByLabelText, getByText, findByTestId } = render(<ConnectivityView cluster="c" />);
    fill(getByLabelText);
    fireEvent.click(getByText("can it reach?"));
    const partial = await findByTestId("cilium-partial");
    expect(partial.textContent).toContain("2 Cilium policies in scope are NOT simulated");
  });

  it("folds backend errors into the verdict well", async () => {
    vi.mocked(canReach).mockResolvedValue({ ...report(), error: "source pod team-a/ghost: not found" });
    const { getByLabelText, getByText, findByText } = render(<ConnectivityView cluster="c" />);
    fill(getByLabelText);
    fireEvent.click(getByText("can it reach?"));
    expect(await findByText(/source pod team-a\/ghost: not found/)).toBeTruthy();
  });

  // Review 2026-10-03 (K): a failed Cilium count is a missing input, not zero
  // policies - the verdict must read PARTIAL.
  it("marks the verdict partial when Cilium policies could not be counted", async () => {
    vi.mocked(canReach).mockResolvedValue(report({ ciliumUnknown: "ciliumnetworkpolicies is forbidden" }));
    const { getByLabelText, getByText, findByTestId } = render(<ConnectivityView cluster="c" />);
    fill(getByLabelText);
    fireEvent.click(getByText("can it reach?"));
    const note = await findByTestId("cilium-unknown");
    expect(note.textContent).toMatch(/could not be counted/);
    expect(note.getAttribute("title")).toBe("ciliumnetworkpolicies is forbidden");
  });

  // Review 2026-10-03 (netpol any-port): two directions that each admit some
  // port can still block; the note says why instead of two green explains
  // contradicting a BLOCKED verdict.
  it("renders the any-port note that qualifies the verdict", async () => {
    vi.mocked(canReach).mockResolvedValue(report({
      allowed: false,
      note: "ingress and egress each admit some TCP port, but no single port is admitted both ways",
    }));
    const { getByLabelText, getByText, findByText, getByTestId } = render(<ConnectivityView cluster="c" />);
    fill(getByLabelText);
    fireEvent.change(getByLabelText("port"), { target: { value: "" } });
    fireEvent.click(getByText("can it reach?"));
    expect(await findByText("BLOCKED")).toBeTruthy();
    expect(getByTestId("connectivity-note").textContent).toMatch(/no single port is admitted both ways/);
  });
});
