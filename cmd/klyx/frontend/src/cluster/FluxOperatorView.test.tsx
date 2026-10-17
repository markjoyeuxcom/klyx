import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, waitFor } from "@testing-library/react";
import type { FluxOperatorRows } from "../health/useFluxOperatorRows";
import type { FluxInstanceDTO, FluxOpCommonDTO, FluxProviderDTO, FluxReportDTO, FluxSetDTO } from "../bridge/fluxop";
import { useFleet } from "../store/fleet";
import { FluxOperatorView } from "./FluxOperatorView";

vi.mock("../bridge/fluxop", () => ({
  fluxOpAct: vi.fn(async () => ({ ok: true, error: "" })),
  getFluxOpDetail: vi.fn(async () => ({ history: [], inventory: [], inventoryTotal: 0, events: [] })),
}));
import { fluxOpAct } from "../bridge/fluxop";

let rowsValue: FluxOperatorRows;
vi.mock("../health/useFluxOperatorRows", () => ({ useFluxOperatorRows: () => rowsValue }));
const live = (kind: string) => ({ kind, served: true, synced: true, coverage: "live" as const });
function setRows(r: Partial<FluxOperatorRows> = {}) {
  rowsValue = {
    instances: [], sets: [], reports: [], coverage: [live("FluxInstance"), live("ResourceSet"), live("ResourceSetInputProvider"), live("FluxReport")],
    watchError: null, loading: false, rowsState: { updating: false, refreshing: false, evaluatedAt: null }, reportReadAt: "2026-10-17T10:05:00Z",
    partCounts: { fluxinstance: null, resourcesets: null }, ...r,
  };
}

const common = (kind: string, name: string, over: Partial<FluxOpCommonDTO> = {}): FluxOpCommonDTO => ({
  uid: `uid-${name}`, gvr: `fluxcd.controlplane.io/v1/${kind.toLowerCase()}s`, generation: 1, kind, namespace: "team", name,
  suspended: false, ready: "Ready", clipped: [], rule: { included: false, role: "ok", label: "Ready" }, ...over,
});
const instance = (over: Partial<FluxInstanceDTO> = {}): FluxInstanceDTO => ({
  ...common("FluxInstance", "flux", { namespace: "flux-system" }), binding: "bound", version: "2.x", components: ["source-controller"],
  running: [{ name: "source-controller", tag: "v1.9.6" }], shards: [], syncName: "flux-system",
  sync: { kind: "GitRepository", url: "https://github.com/org/fleet", ref: "refs/heads/main", path: "clusters/prd" }, ...over,
});
const set = (name: string, over: Partial<FluxSetDTO> = {}): FluxSetDTO => ({ ...common("ResourceSet", name), inputsFrom: [], dependsOn: [], steps: [], ...over });
const provider = (name: string, over: Partial<FluxProviderDTO> = {}): FluxProviderDTO => ({ ...common("ResourceSetInputProvider", name), type: "GitHubBranch", schedule: [], exportedInputCount: 2, ...over });
const setRow = (s: FluxSetDTO) => ({ uid: s.uid, kind: "ResourceSet" as const, rule: s.rule, set: s });
const provRow = (p: FluxProviderDTO) => ({ uid: p.uid, kind: "ResourceSetInputProvider" as const, rule: p.rule, provider: p });
const report: FluxReportDTO = { uid: "r", namespace: "flux-system", name: "flux", reportingSucceeded: true, conditionChanged: "2026-10-17T09:00:00Z",
  distributionVersion: "v2.9.6", operatorVersion: "v0.61.0", components: [{ name: "source-controller", ready: true }],
  reconcilers: [{ kind: "Kustomization", running: 3, failing: 1, suspended: 0 }] };
const rowTexts = () => [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");

describe("FluxOperatorView (coverage S1a)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    setRows();
    useFleet.setState({ clusters: [{ name: "c", state: "Synced", fluxPresent: true, fluxOperatorPresent: true } as never] });
  });

  it("lists broken-first from the Go rule, and names the operator's report as its own account", () => {
    setRows({
      instances: [instance()], reports: [report],
      sets: [
        setRow(set("ok-set")),
        setRow(set("bad-set", { ready: "Failed", reason: "DependencyNotReady", rule: { included: true, role: "fault", label: "Failed" } })),
        provRow(provider("prs")),
      ],
    });
    render(<FluxOperatorView cluster="c" />);
    expect(rowTexts()[0]).toContain("bad-set");
    expect(rowTexts()[0]).toContain("DependencyNotReady");
    expect(screen.getByTestId("fluxop-report-head").textContent).toContain("as reported by the operator");
  });

  it("a resumed object without evidence says why it is not counted", async () => {
    setRows({ sets: [setRow(set("apps", { resume: "awaiting", ready: "Failed", reason: "BuildFailed", rule: { included: false, role: "unknown", label: "Resumed · awaiting evaluation" } }))] });
    render(<FluxOperatorView cluster="c" />);
    fireEvent.click(screen.getByText("apps"));
    const note = await screen.findByTestId("fluxop-note");
    expect(note.textContent).toContain("Last known before suspension: Failed (BuildFailed)");
    expect(note.textContent).toContain("not counted");
  });

  it("the instance inspector shows the report with its two times kept apart, and never as Flux's health", async () => {
    setRows({ instances: [instance()], reports: [report] });
    render(<FluxOperatorView cluster="c" />);
    fireEvent.click(screen.getByText("flux"));
    const block = await screen.findByTestId("fluxop-report");
    expect(block.textContent).toContain("reporting succeeded");
    expect(block.textContent).toContain("condition last changed");
    expect(block.textContent).toContain("read by Klyx");
  });

  it("a scheduled provider offers Fetch now; the verb carries the armed UID", async () => {
    setRows({ sets: [provRow(provider("prs", { schedule: [{ cron: "0 * * * *" }] }))] });
    render(<FluxOperatorView cluster="c" />);
    fireEvent.click(screen.getByText("prs"));
    fireEvent.click(await screen.findByRole("button", { name: /more/i }));
    fireEvent.click(await screen.findByText("Fetch now, outside its schedule"));
    fireEvent.click(await screen.findByRole("button", { name: "Fetch now" }));
    await waitFor(() => expect(fluxOpAct).toHaveBeenCalledWith("c", "ResourceSetInputProvider", "team", "prs", "uid-prs", "fetch"));
  });

  it("a capped kind is said, with its lower bound", () => {
    setRows({ sets: [setRow(set("a"))], coverage: [live("FluxInstance"), { ...live("ResourceSet"), truncated: true, notRetained: 7 }, live("ResourceSetInputProvider")] });
    render(<FluxOperatorView cluster="c" />);
    expect(screen.getByTestId("fluxop-capped").textContent).toContain("7 ResourceSets not retained");
  });

  it("not installed: an empty state, never an empty list", () => {
    setRows({ coverage: ["FluxInstance", "ResourceSet", "ResourceSetInputProvider", "FluxReport"].map((k) => ({ kind: k, served: false, synced: false, coverage: "absent" as const })) });
    render(<FluxOperatorView cluster="c" />);
    expect(screen.getByText("The Flux Operator is not installed on this cluster.")).toBeTruthy();
  });
});
