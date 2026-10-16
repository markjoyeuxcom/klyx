import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, fireEvent, within, act } from "@testing-library/react";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { applySummary, resetHealthStore } from "../store/health";
import { applyLine, resetLineStore, type LineDTO, type LineStationDTO } from "../store/line";
import { core, kind, replace, summary, task, verdict } from "../test/healthFixtures";
import { resetExpiryStore } from "../store/expiry";
import type { ExpiryScanResultDTO } from "../store/fleet";

const scans = vi.hoisted(() => ({ byCluster: {} as Record<string, unknown> }));
vi.mock("../bridge/expiry", () => ({
  getExpiryScan: vi.fn(async (cluster: string) => (scans.byCluster[cluster] ?? { items: [], warnings: [] }) as ExpiryScanResultDTO),
}));
import { FleetView } from "./FleetView";

// The Line slice 5a: the fleet screen is the line's verdict and the board,
// from LineDTO and each cluster's summary; it fetches nothing of its own.

const SHA = "a1b9e3c0a1b2c3d4e5f60718293a4b5c6d7e8f90";
const OLD = "9f2c11d0a1b2c3d4e5f60718293a4b5c6d7e8f90";
const c = (name: string, state = "Synced", over: Partial<ClusterDTO> = {}): ClusterDTO => ({ name, state, reason: "", nodesTotal: 1, pods: 10, env: "", region: "", ...over } as unknown as ClusterDTO);
const st = (cluster: string, env: string, rank: number, over: Partial<LineStationDTO> = {}): LineStationDTO =>
  ({ cluster, env, track: "westeurope", rank, state: "matches", single: SHA, e: 1, a: [SHA], u: 0, read: "ok", units: [], flags: { failing: false }, ...over } as unknown as LineStationDTO);
const line = (stations: LineStationDTO[], over: Partial<LineDTO> = {}): LineDTO => ({
  version: 1, key: { kind: "git", repo: "github.com/acme/fleet", refKind: "branch", refName: "main", label: "github.com/acme/fleet @ branch main" },
  keyHow: "most stations", keyStations: stations.length, keys: [], r: SHA, rStations: 2, applied: 2, compared: 3, notCompared: 0, checking: 0,
  tracks: [{ name: "westeurope", kind: "region", label: "westeurope" }], fallback: false, stations, siding: [], mesh: { drawn: [], card: {}, offFleet: [] }, ...over,
} as unknown as LineDTO);

beforeEach(() => {
  useFleet.setState({ clusters: [], route: { name: "fleet" } });
  resetHealthStore();
  resetLineStore();
  resetExpiryStore();
  scans.byCluster = {};
});

describe("the fleet screen (slice 5a)", () => {
  it("no clusters: one empty state with the way forward", () => {
    render(<FleetView />);
    expect(screen.getByText("No clusters connected yet.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Open Settings" })).toBeTruthy();
  });

  it("before the first LineDTO the board is reading, never empty or quiet", () => {
    useFleet.setState({ clusters: [c("a")] });
    render(<FleetView />);
    expect(screen.getByRole("status", { name: "reading the line" })).toBeTruthy();
    expect(screen.queryByText(/nothing raised/)).toBeNull();
  });

  it("the verdict, the legend and the board: lit only between two matching stations", () => {
    useFleet.setState({ clusters: [c("d"), c("s"), c("p", "Synced", { protected: true })] });
    applyLine(line([st("d", "dev", 0), st("s", "stg", 2), st("p", "prd", 3, { state: "differs", single: OLD, flags: { failing: true } as never })]));
    const { container } = render(<FleetView />);
    expect(screen.getByRole("heading", { level: 2, name: /a1b9e3c is applied on 2 of 3 compared stations/ })).toBeTruthy();
    expect(screen.getByText(/Comparing github\.com\/acme\/fleet @ branch main · on 3 of 3 stations · chosen by most stations/)).toBeTruthy();
    expect(screen.getByLabelText("Legend").textContent).toContain("Lit: matches the comparison revision. A comparison, not a delivery path.");
    const segs = [...container.querySelectorAll(".line-board-seg")].map((e) => e.hasAttribute("data-lit"));
    expect(segs).toEqual([true, false]);
    const prd = screen.getByRole("button", { name: /^p, prd on West Europe/ });
    expect(prd.getAttribute("aria-label")).toBe("p, prd on West Europe, protected, 9f2c11d · differs; health checking…");
    expect(prd.getAttribute("data-mark")).toBe("fault");
  });

  it("a cell carries its cluster's reading, in areas; clicking it opens the cluster", () => {
    useFleet.setState({ clusters: [c("d"), c("p")] });
    applySummary(summary(replace(core(), task("workloads", [verdict("workloads", { fault: 2, ok: 1 }, [kind("deployments")])])), {}, "p"));
    applySummary(summary(core(), {}, "d"));
    applyLine(line([st("d", "dev", 0), st("p", "prd", 3)]));
    render(<FleetView />);
    const p = screen.getByRole("button", { name: /^p, prd on West Europe/ });
    expect(within(p).getByText("1 area needs attention").getAttribute("data-tone")).toBe("danger");
    expect(within(screen.getByRole("button", { name: /^d, dev on West Europe/ })).getByText("nothing raised")).toBeTruthy();
    fireEvent.click(p);
    expect(useFleet.getState().route).toEqual({ name: "cluster", cluster: "p", section: "overview" });
  });

  it("an unreachable cluster is a dashed cell stating its connection, never its line facts as current", () => {
    useFleet.setState({ clusters: [c("d"), c("p", "Failed", { reason: "dial tcp" })] });
    applyLine(line([st("d", "dev", 0), st("p", "prd", 3, { state: "checking", read: "error" })]));
    render(<FleetView />);
    const p = screen.getByRole("button", { name: /^p, prd on West Europe/ });
    expect(p.hasAttribute("data-unreachable")).toBe(true);
    // its connection word, then the reason on one line; the full reading is
    // in its name and title (slice 13d.8)
    expect(p.querySelector("[data-unreachable-word]")?.textContent).toBe("unreachable");
    expect(p.querySelector(".station-cell-reason")?.textContent).toBe("dial tcp");
    expect(p.getAttribute("title")).toContain("failed: dial tcp");
    expect(p.querySelector(".station-cell-rev")).toBeNull();
  });

  it("held and failing times use the proof's words", () => {
    useFleet.setState({ clusters: [c("p")] });
    applyLine(line([st("d", "dev", 0), st("p", "prd", 3, {
      held: true, heldWhy: "attempted a1b9e3c, failing", flags: { failing: true } as never,
      heldSince: { basis: "unknown", capped: true } as never, failingSince: { basis: "rule", since: new Date(2026, 9, 15, 20, 21).toISOString() } as never,
    })]));
    render(<FleetView />);
    const p = screen.getByRole("button", { name: /^p, prd on West Europe/ });
    expect(p.textContent).toContain("held · start unknown (proof capped)");
    expect(p.textContent).toContain("failing since 20:21");
    expect(screen.getByText(/prd · West Europe is held: attempted a1b9e3c, failing - start unknown \(proof capped\)/)).toBeTruthy();
  });

  it("the siding, the fallback, and mesh notes under the board", () => {
    useFleet.setState({ clusters: [c("a"), c("b"), c("lab")] });
    applyLine(line([st("a", "prd", 3), st("b", "prd", 3, { track: "northeurope" })], {
      fallback: true, siding: [st("lab", "", -1, { state: "not compared" })],
      tracks: [{ name: "westeurope", kind: "region", label: "westeurope" }, { name: "northeurope", kind: "region", label: "northeurope" }] as never,
      mesh: { drawn: [], card: {}, offFleet: [{ a: "a", b: "elsewhere", mutual: false }], unknown: ["b"] } as never,
    }));
    const { container } = render(<FleetView />);
    expect(screen.getByText(/Fewer than two environment ranks/)).toBeTruthy();
    expect(container.querySelectorAll(".line-board-seg")).toHaveLength(0);
    expect(screen.getByText("Siding · no environment")).toBeTruthy();
    expect(screen.getByText("Off-fleet mesh peers: a ↔ elsewhere (one-way)")).toBeTruthy();
    expect(screen.getByText("Mesh unverified for b")).toBeTruthy();
  });

  it("a drawn one-way peering is drawn between adjacent tracks, naming the configuring side", () => {
    useFleet.setState({ clusters: [c("pw"), c("pn"), c("dw")] });
    applyLine(line([st("dw", "dev", 0), st("pw", "prd", 3), st("pn", "prd", 3, { track: "northeurope" })], {
      tracks: [{ name: "westeurope", kind: "region", label: "westeurope" }, { name: "northeurope", kind: "region", label: "northeurope" }] as never,
      mesh: { drawn: [{ a: "pw", b: "pn", mutual: false, from: "pn" }], card: {}, offFleet: [] } as never,
    }));
    const { container } = render(<FleetView />);
    const link = container.querySelector(".line-board-link") as HTMLElement;
    expect(link.hasAttribute("data-oneway")).toBe(true);
    expect(link.textContent).toBe("one-way · pn");
  });
});

describe("the aside and footer (slice 5c)", () => {
  const days = (n: number) => Math.floor(Date.now() / 1000) + n * 86400;

  it("the footer counts connections only, never 'live'", () => {
    useFleet.setState({ clusters: [c("a"), c("b", "Degraded"), c("x", "Failed"), c("n", "Connecting"), c("s", "Stale")] });
    render(<FleetView />);
    expect(screen.getByTestId("fleet-footer").textContent).toBe("5 clusters · 2 connected · 1 stale · 1 failed · 1 connecting");
    expect(document.body.textContent).not.toMatch(/\blive\b/i);
  });

  it("the Ledger lists the revisions on the line now, R marked", () => {
    useFleet.setState({ clusters: [c("d"), c("p")] });
    applyLine(line([st("d", "dev", 0), st("p", "prd", 3, { state: "differs", single: OLD })]));
    render(<FleetView />);
    const ledger = screen.getByRole("region", { name: "Ledger" });
    expect(within(ledger).getAllByRole("listitem").map((li) => li.getAttribute("aria-label"))).toEqual([
      "a1b9e3c, the comparison revision, on 1 station", "9f2c11d, on 1 station",
    ]);
  });

  it("the Horizon: the next 30 days soonest first, expired rows, and a failed or unscanned cluster named", async () => {
    scans.byCluster = {
      a: { items: [
        { source: "cert-manager", namespace: "shop", name: "storefront-tls", detail: "", subject: "", notAfterUnix: days(6) },
        { source: "tls-secret", namespace: "edge", name: "old", detail: "", subject: "", notAfterUnix: days(-2) },
        { source: "tls-secret", namespace: "edge", name: "later", detail: "", subject: "", notAfterUnix: days(45) },
      ], warnings: [] },
      x: { items: [], warnings: [], error: "forbidden" },
    };
    useFleet.setState({ clusters: [c("a", "Synced", { env: "stg", region: "westeurope" }), c("x")] });
    render(<FleetView />);
    await act(async () => { await new Promise<void>((r) => setTimeout(r, 0)); });
    const horizon = screen.getByRole("region", { name: "Horizon" });
    const rows = within(horizon).getAllByRole("listitem").map((li) => li.textContent);
    expect(rows).toEqual(["expired 2d agoTLS secret edge/old · stg · West Europe a", "6dCertificate shop/storefront-tls · stg · West Europe a"]);
    expect(horizon.textContent).toContain("x: could not be scanned.");
  });
});
