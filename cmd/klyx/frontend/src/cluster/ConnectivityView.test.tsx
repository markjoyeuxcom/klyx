import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import type { ConnectivityReportDTO } from "../store/fleet";
import { ConnectivityView } from "./ConnectivityView";

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
});
