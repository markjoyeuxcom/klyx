import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, within } from "@testing-library/react";
import type { DRAPartRows } from "../health/useDRARows";
import type { DRAClaimDTO, DRAClassDTO, DRAPoolDTO, DRAViewDTO } from "../bridge/dra";
import { useFleet } from "../store/fleet";

const live = (kind: string) => ({ kind, served: true, synced: true, coverage: "live" as const });
const part = <T,>(rows: T[], kinds: string[], complete = true): DRAPartRows<T> => ({ rows, coverage: kinds.map(live), watchError: null, loading: false,
  rowsState: { updating: false, refreshing: false, evaluatedAt: null }, counts: null, complete, held: false, asOf: null, refreshError: null, capped: [] });
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
    claims.capped = [{ kind: "ResourceClaim", served: true, synced: true, coverage: "live", truncated: true, notRetained: 3 }];
    pools = part([pool("d/p", "unknown", "Scheduler health not established", [view("n1", "complete", 8),
      view("n2", "", 0, { agreed: false, variants: [{ minor: 34, partitionable: false, outcome: "updating", devices: 0, have: 1, want: 2 }, { minor: 35, partitionable: false, outcome: "complete", devices: 4 }] })])],
    ["ResourceSlice"]);
    render(<DRAView cluster="c" />);
    fireEvent.click(tab(/^Pools/));
    expect(rowTexts()[0]).toContain("1+ (1 shared)");
    fireEvent.click(screen.getByText("d / p"));
    const panel = screen.getByTestId("dra-pool-panel");
    expect(within(panel).getByTestId("dra-pool-allocated").textContent).toContain("1+ device allocated per the API, 1 as shares");
    expect(panel.textContent).toContain("a lower bound: the claims read was capped");
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
    claims = part([claim("p-gpu-x", "degraded", "the scheduler cannot allocate it", { ownerKind: "Pod", ownerName: "p", ownerUid: "uid-p", reading: { kind: "degraded", label: "the scheduler cannot allocate it", message: "pod p: cannot allocate" } })], ["ResourceClaim"]);
    const refs = [{ podClaim: "gpu", source: "template", claim: "p-gpu-x", state: "resolved" }, { podClaim: "nic", source: "claim", claim: "shared-nic", state: "resolved" },
      { podClaim: "opt", source: "template", state: "none-needed" }, { podClaim: "later", source: "template", state: "not-created" }];
    const health = [{ container: "app", name: "claim:gpu/req", devices: [{ id: "cdi/a", health: "Unhealthy" }, { id: "cdi/b", health: "Healthy" }] }];
    const { rerender } = render(<PodClaimsBlock cluster="c" namespace="lab" podUid="uid-p" claims={refs} health={health} unbound />);
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
    rerender(<PodClaimsBlock cluster="c" namespace="lab" podUid="uid-p" claims={refs} health={health} unbound={false} />);
    expect(screen.getAllByTestId("pod-claim")[1].textContent).not.toContain("waits");
    // a bound pod's claim with no report says so; an unbound pod's says nothing
    expect(screen.getAllByTestId("pod-claim")[1].textContent).toContain("no device health reported");
    expect(screen.getAllByTestId("pod-claim")[2].textContent).not.toContain("no device health reported");
    claims = part([], ["ResourceClaim"], false);
    rerender(<PodClaimsBlock cluster="c" namespace="lab" podUid="uid-p" claims={refs} health={[]} unbound />);
    expect(screen.getAllByTestId("pod-claim")[1].textContent).toContain("not established");
  });
});

// Codex's implementation review of 177bfe9: each finding, mounted.
describe("the DRA reads are qualified by their own state (S11b implementation revision 1)", () => {
  beforeEach(() => {
    claims = part([], ["ResourceClaim"]);
    pools = part([], ["ResourceSlice"]);
    classes = part([], ["DeviceClass"]);
    useFleet.setState({ clusters: [{ name: "c", state: "Synced", draPresent: true } as never] });
  });
  const capped = (n: number) => [{ kind: "ResourceClaim", served: true, synced: true, coverage: "live" as const, truncated: true, notRetained: n }];

  it("5: an empty capped claims read proves no absence, and a held one is dated", () => {
    claims = { ...part([], ["ResourceClaim"], false), capped: capped(4) };
    const { unmount } = render(<DRAView cluster="c" />);
    expect(screen.getByText("No claims kept: the read was capped.")).toBeTruthy();
    expect(screen.getByText("none failing among those read")).toBeTruthy();
    expect(screen.queryByText("No claims on this cluster.")).toBeNull();
    unmount();
    claims = { ...part([], ["ResourceClaim"], false), held: true, asOf: "2026-10-20T09:00:00Z", refreshError: "boom" };
    render(<DRAView cluster="c" />);
    expect(screen.getByText(/^No claims as of /)).toBeTruthy();
    expect(screen.getByText("The newer read failed: boom")).toBeTruthy();
  });

  it("6: a stale claims read's count is last observed, an unread one not established, never a lower bound", () => {
    const pl = pool("d/p", "ok", "Complete", [view("n1", "complete", 2)]);
    pools = part([pl], ["ResourceSlice"]);
    claims = part([claim("a", "ok", "Allocated", { allocated: true, results: [{ request: "gpu", driver: "d", pool: "p", device: "g0" }] })], ["ResourceClaim"], false);
    claims.coverage = [{ kind: "ResourceClaim", served: true, synced: true, coverage: "stale" }];
    const { unmount } = render(<DRAView cluster="c" />);
    fireEvent.click(tab(/^Pools/));
    fireEvent.click(screen.getByText("d / p"));
    expect(screen.getByTestId("dra-pool-allocated").textContent).toBe("1* device allocated per the API");
    expect(screen.getByTestId("dra-pool-panel").textContent).toContain("as last observed");
    expect(screen.getByTestId("dra-pool-panel").textContent).not.toContain("lower bound");
    unmount();
    claims = { ...part([], ["ResourceClaim"], false), coverage: [{ kind: "ResourceClaim", served: true, synced: false, coverage: "unreadable" as never }] };
    render(<DRAView cluster="c" />);
    fireEvent.click(tab(/^Pools/));
    fireEvent.click(screen.getByText("d / p"));
    expect(screen.getByTestId("dra-pool-allocated").textContent).toBe("Allocated devices: not established");
  });

  it("7: a node a pool's kept views leave out is not established, never 'no slices'", () => {
    pools = part([pool("d/wide", "ok", "Complete", [view("n1", "complete", 1)], { nodes: 20, viewsOmitted: 5 })], ["ResourceSlice"]);
    render(<NodeDRABlock cluster="c" node="n19" />);
    expect(screen.getByTestId("node-dra").textContent).toContain("Not established: a pool lists only its worst nodes, and this node is not among them (d/wide).");
    expect(screen.getByTestId("node-dra").textContent).not.toContain("No slices name this node");
  });

  it("8: a template's claim another pod owns is not this pod's", () => {
    claims = part([claim("p-gpu-x", "ok", "Allocated", { ownerKind: "Pod", ownerName: "other", ownerUid: "uid-other", allocated: true })], ["ResourceClaim"]);
    const refs = [{ podClaim: "gpu", source: "template", claim: "p-gpu-x", state: "resolved" }];
    render(<PodClaimsBlock cluster="c" namespace="lab" podUid="uid-p" claims={refs} health={[]} unbound={false} />);
    expect(screen.getByTestId("pod-claim").textContent).toContain("not this pod's claim: it is owned by Pod other");
    expect(screen.getByTestId("pod-claim").textContent).not.toContain("Allocated");
  });

  it("9: a selected claim replaced at its name is said, never silently followed", () => {
    claims = part([claim("x", "ok", "Allocated", { uid: "uid-1", allocated: true })], ["ResourceClaim"]);
    const { rerender } = render(<DRAView cluster="c" />);
    fireEvent.click(screen.getByText("lab / x"));
    claims = part([claim("x", "degraded", "Device not ready", { uid: "uid-2" })], ["ResourceClaim"]);
    rerender(<DRAView cluster="c" />);
    expect(screen.getByTestId("dra-gone").textContent).toContain("Replaced: another ResourceClaim now holds this name.");
    expect(screen.getByTestId("dra-claim-panel").textContent).not.toContain("Device not ready");
    fireEvent.click(screen.getByText("Open the new object"));
    expect(screen.queryByTestId("dra-gone")).toBeNull();
  });

  it("10: a pool says its devices by taint effect and its slices by access form, at its generation", () => {
    pools = part([pool("d/p", "ok", "Complete", [view("n1", "complete", 2)], { generation: 3, taints: [{ of: "NoExecute", n: 1 }], access: [{ of: "node-local", n: 2 }] })], ["ResourceSlice"]);
    render(<DRAView cluster="c" />);
    fireEvent.click(tab(/^Pools/));
    fireEvent.click(screen.getByText("d / p"));
    const facts = screen.getByTestId("dra-pool-facts").textContent ?? "";
    expect(facts).toContain("2 slices node-local");
    expect(facts).toContain("1 device NoExecute");
    expect(screen.getByTestId("dra-pool-panel").textContent).toContain("From the slices at generation 3");
  });

  it("3: health counts every device the kubelet reports, the extended claim's own too, and a cut report is not established", () => {
    claims = part([], ["ResourceClaim"]);
    const refs = [{ podClaim: "", source: "extended", claim: "p-extended-resources-x", state: "resolved" }, { podClaim: "gpu", source: "claim", claim: "shared", state: "resolved" }];
    const health = [{ container: "app", name: "claim:p-extended-resources-x/request-0", devices: [{ id: "cdi/a", health: "Unhealthy" }], unhealthy: 3, healthy: 13, unknown: 0, devicesOmitted: 0 }];
    render(<PodClaimsBlock cluster="c" namespace="lab" podUid="uid-p" claims={refs} health={health} healthOmitted={2}
      claimHealth={[{ claim: "p-extended-resources-x", unhealthy: 3, healthy: 13, unknown: 0 }]} unbound={false} />);
    const lines = screen.getAllByTestId("pod-claim").map((l) => l.textContent ?? "");
    expect(lines[0]).toContain("3 devices unhealthy (reported by the kubelet): cdi/a, +2 more");
    expect(lines[1]).toContain("device health not established: Klyx kept the pod's first 1 reports, 2 more not read");
  });

  it("2: stale consumers' readings establish nothing", () => {
    claims = part([claim("w", "unknown", "Pending", { reading: { kind: "unknown", label: "Pending", rule: "claim.consumers-unknown" },
      consumers: [{ pod: "old", row: "b", kind: "degraded", label: "the scheduler cannot allocate it", reason: "cannot-allocate" }] })], ["ResourceClaim"]);
    render(<DRAView cluster="c" />);
    fireEvent.click(screen.getByText("lab / w"));
    expect(screen.getByTestId("dra-consumers-unestablished").textContent).toContain("they establish nothing about this claim now");
  });
});

// Codex's implementation re-review of f5bc6e9: the four residual findings.
describe("joined reads keep their state (S11b implementation revision 2)", () => {
  beforeEach(() => {
    claims = part([], ["ResourceClaim"]);
    pools = part([], ["ResourceSlice"]);
    classes = part([], ["DeviceClass"]);
    useFleet.setState({ clusters: [{ name: "c", state: "Synced", draPresent: true } as never] });
  });

  it("1: a claim's health comes from its summary over every report, and kept reports alone are never every report", () => {
    const refs = [{ podClaim: "gpu", source: "claim", claim: "shared", state: "resolved" }];
    const kept = [{ container: "c00", name: "claim:gpu/req0", devices: [{ id: "cdi/0", health: "Healthy" }], unhealthy: 0, healthy: 1, unknown: 0 }];
    const { rerender } = render(<PodClaimsBlock cluster="c" namespace="lab" podUid="u" claims={refs} health={kept} healthOmitted={1}
      claimHealth={[{ claim: "gpu", unhealthy: 1, healthy: 32, unknown: 0 }]} unbound={false} />);
    expect(screen.getByTestId("pod-claim").textContent).toContain("1 device unhealthy (reported by the kubelet)");
    rerender(<PodClaimsBlock cluster="c" namespace="lab" podUid="u" claims={refs} health={kept} healthOmitted={1} unbound={false} />);
    expect(screen.getByTestId("pod-claim").textContent).toContain("health not established: Klyx kept the pod's first 1 reports, 1 more not read");
  });

  it("2: populated node and pod lines carry their joined read's state, and a cut pool's view is not established", () => {
    pools = { ...part([pool("d/p", "ok", "Complete", [view("n1", "complete", 8)])], ["ResourceSlice"], false), held: true, asOf: "2026-10-20T09:00:00Z" };
    const { unmount } = render(<NodeDRABlock cluster="c" node="n1" />);
    expect(screen.getByTestId("node-dra-pool").textContent).toMatch(/· 8 devices \(as of /);
    unmount();
    pools = part([pool("d/p", "unknown", "Not established", [view("n1", "complete", 8)], { unread: true })], ["ResourceSlice"], false);
    const second = render(<NodeDRABlock cluster="c" node="n1" />);
    expect(screen.getByTestId("node-dra-pool").textContent).toContain("not established: not every slice was read");
    second.unmount();
    claims = { ...part([claim("shared", "ok", "Allocated", { allocated: true })], ["ResourceClaim"], false), held: true, asOf: "2026-10-20T09:00:00Z", refreshError: "boom" };
    render(<PodClaimsBlock cluster="c" namespace="lab" podUid="u" claims={[{ podClaim: "gpu", source: "claim", claim: "shared", state: "resolved" }]} health={[]} unbound={false} />);
    expect(screen.getByTestId("pod-claim").textContent).toMatch(/Allocated \(as of .+; the newer read failed: boom\)/);
  });

  it("3: every attention count keeps its counts' qualifiers, whatever tab is open", () => {
    claims = { ...part([claim("a", "degraded", "the scheduler cannot allocate it")], ["ResourceClaim"]), counts: { total: 2, included: 1, fault: 0, lower: true, stale: true } };
    render(<DRAView cluster="c" />);
    expect(screen.getByText("1+* cannot be allocated or appear stuck")).toBeTruthy();
    fireEvent.click(tab(/^Pools/));
    expect(tab(/^Claims/).textContent).toBe("Claims 1+*");
  });

  it("4: a cut pool read never says no device is tainted", () => {
    pools = part([pool("d/p", "unknown", "Not established", [view("n1", "complete", 2)], { unread: true, taints: [], access: [{ of: "node-local", n: 1 }] })], ["ResourceSlice"]);
    render(<DRAView cluster="c" />);
    fireEvent.click(tab(/^Pools/));
    fireEvent.click(screen.getByText("d / p"));
    expect(screen.getByTestId("dra-pool-facts").textContent).toContain("no tainted device in the slices read");
    expect(screen.getByTestId("dra-pool-facts").textContent).not.toContain("no device tainted");
  });
});
