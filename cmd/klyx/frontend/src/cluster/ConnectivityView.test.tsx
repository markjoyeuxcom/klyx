import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, act } from "@testing-library/react";
import type { ConnectivityReportDTO } from "../store/fleet";
import { useFleet } from "../store/fleet";
import { ConnectivityView, parsePort } from "./ConnectivityView";

vi.mock("../bridge/connectivity", () => ({ canReach: vi.fn(async () => ({}) as ConnectivityReportDTO) }));
import { canReach } from "../bridge/connectivity";

const dir = (over = {}) => ({ isolated: true, allowed: true, policies: [], explain: "admitted by allow-web", ...over });
const rep = (over: Partial<ConnectivityReportDTO> = {}): ConnectivityReportDTO => ({
  allowed: true, ingress: dir(), egress: dir({ isolated: false, explain: "source not isolated for egress" }), srcLabels: { app: "web" }, dstLabels: { app: "db" }, ciliumPolicies: 0, ...over,
});
type D<T> = { promise: Promise<T>; resolve: (v: T) => void };
const deferred = <T,>(): D<T> => { let resolve!: (v: T) => void; const promise = new Promise<T>((r) => (resolve = r)); return { promise, resolve }; };
function fill(port = "5432") {
  fireEvent.change(screen.getByLabelText("source namespace"), { target: { value: "team-a" } });
  fireEvent.change(screen.getByLabelText("source pod"), { target: { value: "web-1" } });
  fireEvent.change(screen.getByLabelText("destination namespace"), { target: { value: "team-b" } });
  fireEvent.change(screen.getByLabelText("destination pod"), { target: { value: "db-0" } });
  fireEvent.change(screen.getByLabelText("port"), { target: { value: port } });
}

describe("ConnectivityView (redesign step 5)", () => {
  beforeEach(() => vi.clearAllMocks());

  it("parsePort: empty is any, integers 1..65535, never NaN", () => {
    expect(parsePort("")).toBe(0);
    expect(parsePort("443")).toBe(443);
    expect(parsePort("abc")).toBeNull();
    expect(parsePort("70000")).toBeNull();
    expect(parsePort("0")).toBeNull();
  });

  it("an invalid port blocks the run with a reason; Can it reach? needs both pods", () => {
    render(<ConnectivityView cluster="x" />);
    expect((screen.getByRole("button", { name: "Can it reach?" }) as HTMLButtonElement).disabled).toBe(true);
    fill("http");
    expect(screen.getByRole("alert").textContent).toContain("Port must be a number");
    expect((screen.getByRole("button", { name: "Can it reach?" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("an answer names its question, both directions and policies; no lamp", async () => {
    vi.mocked(canReach).mockResolvedValue(rep({ allowed: false, ingress: dir({ allowed: false, explain: "no policy admits", policies: [{ namespace: "team-b", name: "deny-all", admits: false }] }) }));
    const { container } = render(<ConnectivityView cluster="x" />);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Can it reach?" }));
    expect(await screen.findByText("Blocked")).toBeTruthy();
    expect(screen.getByText("Can team-a/web-1 → team-b/db-0 on TCP/5432?")).toBeTruthy();
    expect(vi.mocked(canReach).mock.calls[0]).toEqual(["x", "team-a", "web-1", "team-b", "db-0", 5432, "TCP"]);
    const open = vi.spyOn(useFleet.getState(), "openResource");
    fireEvent.click(screen.getByRole("button", { name: "open NetworkPolicy team-b/deny-all" }));
    expect(open).toHaveBeenCalledWith(expect.objectContaining({ plural: "networkpolicies" }));
    expect(container.querySelector('[role="img"]')).toBeNull();
  });

  it("Cilium in scope, or uncountable, makes the verdict partial in words", async () => {
    vi.mocked(canReach).mockResolvedValueOnce(rep({ ciliumPolicies: 2 }));
    render(<ConnectivityView cluster="x" />);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Can it reach?" }));
    expect(await screen.findByText("Reachable by NetworkPolicy")).toBeTruthy();
    expect(screen.getByTestId("cilium-partial").textContent).toContain("2 Cilium policies are in scope and not simulated");
    vi.mocked(canReach).mockResolvedValueOnce(rep({ ciliumUnknown: "ciliumnetworkpolicies is forbidden" }));
    fireEvent.click(screen.getByRole("button", { name: "Can it reach?" }));
    expect(await screen.findByTestId("cilium-unknown")).toBeTruthy();
  });

  it("an edited form marks the answer as answering a previous question; errors are the answer", async () => {
    vi.mocked(canReach).mockResolvedValueOnce(rep());
    render(<ConnectivityView cluster="x" />);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Can it reach?" }));
    await screen.findByText("Reachable");
    fireEvent.change(screen.getByLabelText("port"), { target: { value: "443" } });
    expect(screen.getByText(/The form has changed since this answer/)).toBeTruthy();
    vi.mocked(canReach).mockResolvedValueOnce(rep({ error: "pod team-b/db-0 not found" }));
    fireEvent.click(screen.getByRole("button", { name: "Can it reach?" }));
    expect(await screen.findByText("pod team-b/db-0 not found")).toBeTruthy();
    expect(screen.queryByText("Reachable")).toBeNull();
  });

  it("a late answer from a previous cluster never lands on the new one", async () => {
    const late = deferred<ConnectivityReportDTO>();
    vi.mocked(canReach).mockReturnValueOnce(late.promise);
    const { rerender } = render(<ConnectivityView cluster="a" />);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Can it reach?" }));
    rerender(<ConnectivityView cluster="b" />);
    await act(async () => { late.resolve(rep()); await late.promise; });
    expect(screen.queryByText("Reachable")).toBeNull();
    expect((screen.getByRole("button", { name: "Can it reach?" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("inputs never auto-capitalize Kubernetes names", () => {
    render(<ConnectivityView cluster="x" />);
    for (const i of [...document.querySelectorAll("input")]) expect(i.getAttribute("autocapitalize")).toBe("off");
  });
});
