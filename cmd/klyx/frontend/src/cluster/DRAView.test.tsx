import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, within } from "@testing-library/react";
import type { DRAPartRows } from "../health/useDRARows";
import type { DRAClaimDTO, DRAClassDTO, DRAPoolDTO, DRAViewDTO } from "../bridge/dra";
import { useFleet } from "../store/fleet";

const live = (kind: string) => ({ kind, served: true, synced: true, coverage: "live" as const });
const part = <T,>(rows: T[], kinds: string[], complete = true): DRAPartRows<T> => ({ rows, coverage: kinds.map(live), watchError: null, loading: false,
  rowsState: { updating: false, refreshing: false, evaluatedAt: null }, counts: null, complete });
let claims: DRAPartRows<DRAClaimDTO>;
let pools: DRAPartRows<DRAPoolDTO>;
let classes: DRAPartRows<DRAClassDTO>;
vi.mock("../health/useDRARows", async (orig) => ({ ...(await orig<typeof import("../health/useDRARows")>()),
  useDRAClaims: () => claims, useDRAPools: () => pools, useDRAClasses: () => classes }));
const { DRAView, outcomeWords } = await import("./DRAView");
const { NodeDRABlock, PodClaimsBlock } = await import("./DRABlocks");

const rule = (role: string, label: string) => ({ included: role === "fault" || role === "degraded", role, label });
const claim = (name: string, role: string, label: string, over: Partial<DRAClaimDTO> = {}): DRAClaimDTO => ({ uid: `uid-${name}`, namespace: "lab", name,
  rule: rule(role, label), reading: { kind: role, label }, requests: [{ name: "gpu", class: "gpu.example.com", mode: "ExactCount", count: 1 }],
  allocated: false, results: [], reservedCount: 0, reserved: [], devices: [], consumers: [], ...over });
const view = (node: string, outcome: string, devices = 0, over: Partial<DRAViewDTO> = {}): DRAViewDTO => ({ node, agreed: true, kind: outcome,
  variants: [{ minor: 36, partitionable: false, outcome, devices }, { minor: 37, partitionable: false, outcome, devices }], ...over });
const pool = (key: string, role: string, label: string, views: DRAViewDTO[], over: Partial<DRAPoolDTO> = {}): DRAPoolDTO => {
  const [driver, p] = key.split("/");
  return { key, driver, pool: p, generation: 1, slices: 1, rule: rule(role, label), reading: { kind: role, label }, views, assumed: "read for a 1.36 or 1.37 scheduler", ...over };
};
const rowTexts = () => [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");
const tab = (name: RegExp) => within(screen.getByRole("group", { name: "DRA objects" })).getByRole("button", { name });

describe("DRAView (coverage S11b)", () => {
  beforeEach(() => {
    claims = part([], ["ResourceClaim"]);
    pools = part([], ["ResourceSlice"]);
    classes = part([], ["DeviceClass"]);
    useFleet.setState({ clusters: [{ name: "c", state: "Synced", draPresent: true } as never] });
  });

  it("claims are broken first, and a blocked claim's inspector quotes the scheduler, says the meaning, and points at pool problems without blaming them", () => {
    claims = part([
      claim("ok", "ok", "Allocated", { allocated: true, reservedCount: 1, reserved: [{ resource: "pods", name: "user", uid: "u" }], results: [{ request: "gpu", driver: "gpu.example.com", pool: "n1", device: "gpu-0" }] }),
      claim("stuck", "degraded", "the scheduler cannot allocate it", { consumers: [{ pod: "trainer", row: "b", kind: "degraded", label: "the scheduler cannot allocate it",
        reason: "cannot-allocate", meaning: "the scheduler could not allocate the pod's claims", message: "0/1 nodes are available: 1 cannot allocate all claims." }] }),
    ], ["ResourceClaim"]);
    pools = part([pool("gpu.example.com/broken", "degraded", "Invalid", [view("n2", "invalid")])], ["ResourceSlice"]);
    render(<DRAView cluster="c" />);
    expect(rowTexts()[0]).toContain("lab / stuck");
    fireEvent.click(screen.getByText("lab / stuck"));
    const panel = screen.getByTestId("dra-claim-panel");
    expect(within(panel).getByTestId("dra-claim-consumer").textContent).toContain('The scheduler: "0/1 nodes are available: 1 cannot allocate all claims."');
    expect(panel.textContent).toContain("could not allocate the pod's claims");
    expect(panel.textContent).toContain("Which pools this claim's class can select is decided by its selectors, which Klyx does not evaluate.");
    expect(panel.textContent).toContain("gpu.example.com/broken · Invalid");
    expect(panel.textContent).toContain("selectors not shown");
  });

  it("an allocated claim names its devices and its consumers, a PodGroup as not a pod", () => {
    claims = part([claim("x", "ok", "Allocated", { allocated: true, reservedCount: 2, reserved: [{ resource: "pods", name: "a", uid: "1" }, { resource: "podgroups", name: "g", uid: "2" }],
      results: [{ request: "gpu", driver: "gpu.example.com", pool: "n1", device: "gpu-3", shareId: "s1" }],
      devices: [{ driver: "gpu.example.com", pool: "n1", device: "gpu-3", conditions: [{ type: "Ready", status: "True", reason: "Configured" }] }] })], ["ResourceClaim"]);
    render(<DRAView cluster="c" />);
    fireEvent.click(screen.getByText("lab / x"));
    const panel = screen.getByTestId("dra-claim-panel");
    expect(panel.textContent).toContain("gpu.example.com/n1/gpu-3");
    expect(panel.textContent).toContain("a share");
    expect(panel.textContent).toContain("In use by pod a, podgroups g (not a pod).");
    expect(within(panel).getByTestId("dra-device-status").textContent).toContain("Ready True (Configured)");
  });

  it("a pool shows its allocated devices joined from the claims, qualified when the claims read is not whole, and each node's view", () => {
    claims = part([claim("a", "ok", "Allocated", { allocated: true, results: [{ request: "gpu", driver: "d", pool: "p", device: "g0" }, { request: "gpu", driver: "d", pool: "p", device: "g1", adminAccess: true }] }),
      claim("b", "ok", "Allocated", { allocated: true, results: [{ request: "gpu", driver: "d", pool: "p", device: "g0", shareId: "s" }] })], ["ResourceClaim"], false);
    pools = part([pool("d/p", "unknown", "Scheduler health not established", [view("n1", "complete", 8),
      view("n2", "", 0, { agreed: false, variants: [{ minor: 34, partitionable: false, outcome: "updating", devices: 0, have: 1, want: 2 }, { minor: 35, partitionable: false, outcome: "complete", devices: 4 }] })])],
    ["ResourceSlice"]);
    render(<DRAView cluster="c" />);
    fireEvent.click(tab(/^Pools/));
    expect(rowTexts()[0]).toContain("1+ (1 shared)");
    fireEvent.click(screen.getByText("d / p"));
    const panel = screen.getByTestId("dra-pool-panel");
    expect(within(panel).getByTestId("dra-pool-allocated").textContent).toContain("1+ device allocated per the API, 1 as shares");
    expect(panel.textContent).toContain("this is a lower bound");
    const views = within(panel).getAllByTestId("dra-pool-view").map((v) => v.textContent);
    expect(views[0]).toContain("n1 · 8 devices");
    expect(views[1]).toContain("not established");
    expect(views[1]).toContain("1.34: updating · 1.35: complete");
    expect(within(panel).getByTestId("dra-pool-assumed").textContent).toContain("read for a 1.36 or 1.37 scheduler");
    expect(panel.textContent).not.toMatch(/available/i);
  });

  it("a pool past the row's bound counts the nodes it does not list, and its obsolete ones", () => {
    pools = part([pool("d/wide", "ok", "Complete", [view("n1", "complete", 8)], { nodes: 2000, viewsOmitted: 1999, obsolete: ["n7"], obsoleteOmitted: 40 })], ["ResourceSlice"]);
    render(<DRAView cluster="c" />);
    fireEvent.click(tab(/^Pools/));
    expect(rowTexts()[0]).toContain("2000 nodes");
    fireEvent.click(screen.getByText("d / wide"));
    const panel = screen.getByTestId("dra-pool-panel");
    expect(panel.textContent).toContain("Nodes · 2000");
    expect(screen.getByTestId("dra-pool-views-omitted").textContent).toBe("1999 more nodes not listed: the 1 worst are shown, and the reading above is from every node.");
    expect(panel.textContent).toContain("Obsolete on n7 and 40 more");
  });

  it("device classes are facts, their claims joined", () => {
    claims = part([claim("a", "ok", "Pending")], ["ResourceClaim"]);
    classes = part([{ uid: "c1", name: "gpu.example.com", selectors: 1, configDrivers: ["gpu.example.com"], extendedResourceName: "example.com/gpu" }], ["DeviceClass"]);
    render(<DRAView cluster="c" />);
    fireEvent.click(tab(/^Device classes/));
    expect(rowTexts()[0]).toContain("example.com/gpu");
    fireEvent.click(document.querySelector(".lens-row .lens-cell-name")!);
    expect(screen.getByTestId("dra-class-panel").textContent).toContain("1 request it");
    expect(screen.getByTestId("dra-class-panel").textContent).toContain("CEL not shown");
  });

  it("outcomeWords: variants that agree only in severity keep their distinct device counts", () => {
    expect(outcomeWords(view("n1", "ok", 0, { variants: [{ minor: 37, partitionable: false, outcome: "none", devices: 0 }, { minor: 37, partitionable: true, outcome: "complete", devices: 4 }] })))
      .toBe("reads OK under every allowed scheduler: 0 to 4 devices, depending on the scheduler");
    expect(outcomeWords(view("n1", "updating", 0, { variants: [{ minor: 37, partitionable: false, outcome: "updating", devices: 0, have: 1, want: 2 }] }))).toBe("being updated (1 of 2 slices)");
  });
});

describe("the DRA blocks (coverage S11b §5)", () => {
  beforeEach(() => {
    claims = part([], ["ResourceClaim"]);
    pools = part([], ["ResourceSlice"]);
  });

  it("the node block lists the pools naming the node, and says no slices is not 'not registered'", () => {
    pools = part([pool("gpu.example.com/n1", "ok", "Complete", [view("n1", "complete", 8), view("", "none")]), pool("other/n2", "ok", "Complete", [view("n2", "complete", 2)])], ["ResourceSlice"]);
    render(<NodeDRABlock cluster="c" node="n1" />);
    const lines = screen.getAllByTestId("node-dra-pool").map((l) => l.textContent);
    expect(lines).toEqual(["gpu.example.com / n1 · 8 devices"]);
    expect(screen.getByTestId("node-dra").textContent).toContain('never "not registered"');
  });

  it("unhealthy devices are named by the kubelet's ID, at most four, the rest counted", () => {
    const refs = [{ podClaim: "gpu", source: "template", claim: "p-gpu-x", state: "resolved" }];
    const ids = ["cdi/0", "cdi/1", "cdi/2", "cdi/3", "cdi/4", "cdi/5"];
    render(<PodClaimsBlock cluster="c" namespace="lab" claims={refs} unbound={false}
      health={[{ container: "app", name: "claim:gpu", devices: ids.map((id) => ({ id, health: "Unhealthy" })) }]} />);
    expect(screen.getByTestId("pod-claim").textContent).toContain("6 devices unhealthy (reported by the kubelet): cdi/0, cdi/1, cdi/2, cdi/3, +2 more");
  });

  it("the node block is absent where no DRA objects are read", () => {
    const { container } = render(<NodeDRABlock cluster="c" node="n1" />);
    expect(container.textContent).toBe("");
  });

  it("the pod's claims join the claims rows; a missing claim says the scheduler waits only for an unbound pod; none-needed is never missing", () => {
    claims = part([claim("p-gpu-x", "degraded", "the scheduler cannot allocate it", { reading: { kind: "degraded", label: "the scheduler cannot allocate it", message: "pod p: cannot allocate" } })], ["ResourceClaim"]);
    const refs = [{ podClaim: "gpu", source: "template", claim: "p-gpu-x", state: "resolved" }, { podClaim: "nic", source: "claim", claim: "shared-nic", state: "resolved" },
      { podClaim: "opt", source: "template", state: "none-needed" }, { podClaim: "later", source: "template", state: "not-created" }];
    const health = [{ container: "app", name: "claim:gpu/req", devices: [{ id: "cdi/a", health: "Unhealthy" }, { id: "cdi/b", health: "Healthy" }] }];
    const { rerender } = render(<PodClaimsBlock cluster="c" namespace="lab" claims={refs} health={health} unbound />);
    const lines = screen.getAllByTestId("pod-claim").map((l) => l.textContent ?? "");
    expect(lines[0]).toContain("p-gpu-x · from a template");
    expect(lines[0]).toContain("the scheduler cannot allocate it");
    expect(lines[0]).toContain("1 device unhealthy (reported by the kubelet): cdi/a");
    expect(lines[1]).toContain("does not exist: the scheduler waits for it without an event");
    expect(lines[2]).toContain("no claim was needed");
    expect(lines[3]).toContain("not created yet");
    // a claim the rows hold links to the DRA lens; one they do not is named only
    expect(screen.getAllByTestId("claim-link").map((l) => l.textContent)).toEqual(["p-gpu-x"]);
    expect(lines[1]).not.toContain("no device health reported");
    rerender(<PodClaimsBlock cluster="c" namespace="lab" claims={refs} health={health} unbound={false} />);
    expect(screen.getAllByTestId("pod-claim")[1].textContent).not.toContain("waits");
    // a bound pod's claim with no report says so; an unbound pod's says nothing
    expect(screen.getAllByTestId("pod-claim")[1].textContent).toContain("no device health reported");
    expect(screen.getAllByTestId("pod-claim")[2].textContent).not.toContain("no device health reported");
    claims = part([], ["ResourceClaim"], false);
    rerender(<PodClaimsBlock cluster="c" namespace="lab" claims={refs} health={[]} unbound />);
    expect(screen.getAllByTestId("pod-claim")[1].textContent).toContain("not established");
  });
});
