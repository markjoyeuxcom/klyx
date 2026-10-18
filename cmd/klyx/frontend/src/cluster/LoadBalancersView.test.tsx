import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, within } from "@testing-library/react";
import type { LBPartRows } from "../health/useLBRows";
import type { LBPoolDTO, LBServiceDTO, LBStateDTO } from "../bridge/loadbalancers";
import { useFleet } from "../store/fleet";

const live = (kind: string) => ({ kind, served: true, synced: true, coverage: "live" as const });
const part = <T,>(rows: T[], kinds: string[]): LBPartRows<T> => ({ rows, coverage: kinds.map(live), watchError: null, loading: false,
  rowsState: { updating: false, refreshing: false, evaluatedAt: null }, counts: null });
let svcs: LBPartRows<LBServiceDTO>;
let pools: LBPartRows<LBPoolDTO>;
let states: LBPartRows<LBStateDTO>;
vi.mock("../health/useLBRows", () => ({ useLBServices: () => svcs, useLBPools: () => pools, useLBStates: () => states }));
const { LoadBalancersView, ALLOCATION_LIMIT, stationsOf } = await import("./LoadBalancersView");

const rule = (role: string, label: string) => ({ included: role === "fault" || role === "degraded", role, label });
// useNow is quantised to its 30s tick: ages sit half a minute past a boundary
const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString();
const svc = (name: string, role: string, label: string, over: Partial<LBServiceDTO> = {}): LBServiceDTO => ({
  uid: `uid-${name}`, gvr: "v1/services", namespace: "app", name, generation: 1, ipFamilies: [], allocated: false, ips: [], hostnames: [], addressCount: 0,
  entries: 0, families: [], expected: { known: true, candidates: [{ controller: "MetalLB", detected: true, limit: "unless started with --lb-class, which Klyx cannot read" }] },
  conditions: [], conditionCount: 0, limits: ["An assigned address proves allocation, not that traffic reaches the Service."], rule: rule(role, label), ...over });
const pool = (name: string, role: string, label: string, over: Partial<LBPoolDTO> = {}): LBPoolDTO => ({
  kind: "metallb", uid: `uid-${name}`, gvr: "metallb.io/v1beta1/ipaddresspools", namespace: "metallb-system", name, generation: 1, blocks: ["172.18.255.200-172.18.255.210"],
  blockCount: 1, conditions: [], selectedBy: [], unresolved: [], invalid: [], limits: [], rule: rule(role, label), ...over });
const tab = (name: RegExp) => within(screen.getByRole("group", { name: "Load balancer objects" })).getByRole("button", { name });
const rowTexts = () => [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");

describe("LoadBalancersView (coverage S4)", () => {
  beforeEach(() => {
    svcs = part([], ["Service"]);
    pools = part([], ["IPAddressPool", "CiliumLoadBalancerIPPool"]);
    states = part([], ["ConfigurationState"]);
    useFleet.setState({ clusters: [{ name: "c", state: "Synced", loadBalancingPresent: true } as never] });
  });

  it("pending first; a lower-bound start is marked; the inspector draws the chain, the evidence and the limits", () => {
    svcs = part([
      svc("ok", "ok", "Address assigned", { allocated: true, ips: ["172.18.255.200"], addressCount: 1, families: ["IPv4"] }),
      svc("young", "progressing", "Pending", { pendingSince: ago(2.6), pendingObserved: false,
        diagnosis: { events: [], eventCount: 0, eventsRead: "complete", svclbRead: true, request: [], summary: "the expected controller wrote no event or condition for it", silent: "the expected controller wrote no event or condition for it" } }),
      svc("stuck", "fault", "No address", { pendingSince: ago(30.6), pendingObserved: true, reason: "No address: AllocationFailed: no available IPs",
        diagnosis: { events: [{ reason: "AllocationFailed", message: "Failed to allocate IP for \"app/stuck\": no available IPs", count: 3 }], eventCount: 1, eventsRead: "partial",
          svclbRead: true, request: ["metallb.io/loadBalancerIPs=172.18.255.250"], pool: { name: "gold", state: "missing", namespace: "metallb-system" }, summary: "AllocationFailed: no available IPs" } }),
    ], ["Service"]);
    render(<LoadBalancersView cluster="c" />);
    const rows = rowTexts();
    expect(rows[0]).toContain("app/stuck");
    expect(rows[1]).toContain("app/young");
    expect(rows[1]).toContain("≥2m");
    expect(rows[2]).toContain("172.18.255.200");
    fireEvent.click(screen.getByText("app/stuck"));
    const panel = screen.getByTestId("lb-service-panel");
    expect(within(panel).getByTestId("lb-chain").textContent).toContain("gold · missing");
    const evidence = within(panel).getByTestId("lb-evidence").textContent ?? "";
    expect(evidence).toContain("AllocationFailed");
    expect(evidence).toContain("capped or stale: an absent event proves nothing");
    expect(evidence).toContain("does not exist in metallb-system");
    expect(evidence).toContain("context, never the cause");
    expect(within(panel).getByTestId("lb-limits").textContent).toContain(ALLOCATION_LIMIT);
    expect(within(panel).getByTestId("lb-pending").textContent).toContain("Pending for 30m");
  });

  it("an unobserved start says it is a lower bound, and the chain never claims an owner", () => {
    svcs = part([svc("young", "progressing", "Pending", { pendingSince: ago(3.6), pendingObserved: false, limits: ["Its start was not observed: the time is a lower bound, from Klyx's first observation."],
      expected: { known: true, candidates: [], note: "no controller Klyx can see serves Services without a class" },
      diagnosis: { events: [], eventCount: 0, eventsRead: "unverified", svclbRead: false, request: [], summary: "no controller Klyx can see serves Services without a class", silent: "no controller Klyx can see serves Services without a class" } })],
    ["Service"]);
    render(<LoadBalancersView cluster="c" />);
    fireEvent.click(screen.getByText("app/young"));
    const panel = screen.getByTestId("lb-service-panel");
    expect(within(panel).getByTestId("lb-pending").textContent).toContain("Pending at least 3m");
    expect(within(panel).getByTestId("lb-pending").textContent).toContain("a fault after 10 minutes");
    expect(panel.textContent).toContain("Events were not read: none can be said absent.");
    expect(within(panel).getByTestId("lb-limits").textContent).toContain("lower bound");
    const st = stationsOf(svcs.rows[0]);
    expect(st.find((s) => s.label === "controller")).toEqual({ label: "controller", detail: "none detected", aspect: "syncing" });
  });

  it("pools: a missing MetalLB count is not reported, never zero; Cilium's exact counts and a historical conflict", () => {
    pools = part([
      pool("bronze", "degraded", "No advertisement", { reason: "No advertisement: No advertisement selects it", status: { assignedIPv4: 0 },
        limits: ["From the cluster's configuration objects, not the speakers' announcement state."], adsRead: "complete", instance: "state" }),
      pool("c6", "ok", "18446744073709551615 of 18446744073709551616 available", { kind: "cilium", namespace: undefined, gvr: "cilium.io/v2/ciliumloadbalancerippools",
        counts: { total: "18446744073709551616", available: "18446744073709551615", used: "1" },
        conflict: { type: "cilium.io/PoolConflict", status: "True", reason: "cidr_overlap", current: false } }),
    ], ["IPAddressPool", "CiliumLoadBalancerIPPool"]);
    render(<LoadBalancersView cluster="c" />);
    fireEvent.click(tab(/^Pools/));
    expect(rowTexts()[0]).toContain("bronze");
    fireEvent.click(screen.getByText("metallb-system/bronze"));
    let panel = screen.getByTestId("lb-pool-panel");
    expect(within(panel).getByTestId("lb-counts").textContent).toContain("not reported available · 0 assigned");
    expect(within(panel).getByTestId("lb-pool-limits").textContent).toContain("not the speakers' announcement state");
    fireEvent.click(screen.getByText("c6"));
    panel = screen.getByTestId("lb-pool-panel");
    expect(within(panel).getByTestId("lb-counts").textContent).toContain("18446744073709551615 of 18446744073709551616");
    expect(panel.textContent).toContain("historical: for an earlier generation");
  });

  it("the MetalLB configuration tab appears with MetalLB pools or states", () => {
    render(<LoadBalancersView cluster="c" />);
    expect(screen.queryByRole("button", { name: /MetalLB configuration/ })).toBeNull();
    states = part([{ uid: "s", gvr: "metallb.io/v1beta1/configurationstates", namespace: "metallb-system", name: "controller", role: "controller", result: "Invalid",
      error: "secret type mismatch", conditions: [], conditionCount: 0, reason: "Configuration invalid: secret type mismatch", rule: rule("fault", "Configuration invalid") }], ["ConfigurationState"]);
    render(<LoadBalancersView cluster="c" />);
    fireEvent.click(screen.getAllByRole("button", { name: /MetalLB configuration/ })[0]);
    fireEvent.click(screen.getAllByText("controller")[0]);
    expect(screen.getByTestId("lb-state-panel").textContent).toContain("last valid one");
  });
});
