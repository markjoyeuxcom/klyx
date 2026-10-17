import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, waitFor } from "@testing-library/react";
import type { PartRows } from "../health/useCompositionRows";
import type { ArtifactDTO, CompositionCommonDTO, CompositionRowDTO, GeneratorDTO } from "../bridge/fluxcomposition";
import { useFleet } from "../store/fleet";
import { CompositionView } from "./CompositionView";

vi.mock("../bridge/fluxcomposition", () => ({
  getCompositionDetail: vi.fn(async (_c: string, _k: string, _ns: string, _n: string, uid: string) => ({ uid, sources: [], consumers: [], events: [] })),
}));
vi.mock("../bridge/fluxop", () => ({ fluxOpAct: vi.fn(async () => ({ ok: true, error: "" })) }));
import { getCompositionDetail } from "../bridge/fluxcomposition";
import { fluxOpAct } from "../bridge/fluxop";

let rowsValue: PartRows<CompositionRowDTO>;
vi.mock("../health/useCompositionRows", () => ({ useCompositionRows: () => rowsValue, COMPOSITION_PART_KINDS: ["ArtifactGenerator", "ExternalArtifact"] }));
const live = (kind: string) => ({ kind, served: true, synced: true, coverage: "live" as const });
function setRows(rows: CompositionRowDTO[]) {
  rowsValue = { rows, coverage: [live("ArtifactGenerator"), live("ExternalArtifact")], watchError: null, loading: false,
    rowsState: { updating: false, refreshing: false, evaluatedAt: null }, counts: null };
}
const ok = { included: false, role: "ok", label: "Ready" };
const fault = { included: true, role: "fault", label: "Failed" };
const common = (kind: string, name: string, over: Partial<CompositionCommonDTO> = {}): CompositionCommonDTO => ({
  uid: `uid-${name}`, gvr: "x", kind, namespace: "flux-system", name, generation: 1, ready: "Ready", clipped: [], incomplete: [], rule: ok, ...over,
});
const gen = (name: string, over: Partial<GeneratorDTO> = {}): CompositionRowDTO => {
  const g: GeneratorDTO = { ...common("ArtifactGenerator", name), suspended: false, sources: [{ kind: "GitRepository", alias: "fleet", namespace: "flux-system", name: "fleet", found: false }],
    outputs: [{ name: "apps-a", copy: ["@fleet/apps/** → @artifact/"] }], generated: [{ kind: "ExternalArtifact", namespace: "flux-system", name: "apps-a", found: true, label: "Ready", role: "ok" }],
    generatedState: "found", ...over };
  return { uid: g.uid, kind: "ArtifactGenerator", rule: g.rule, generator: g };
};
const art = (name: string, over: Partial<ArtifactDTO> = {}): CompositionRowDTO => {
  const a: ArtifactDTO = { ...common("ExternalArtifact", name), hasArtifact: true, revision: "latest@sha256:" + "a".repeat(64), origin: "http://source-watcher.flux-system.svc",
    lastUpdate: "2026-10-17T09:00:00Z", producer: { state: "verified", kind: "ArtifactGenerator", namespace: "flux-system", name: "apps", label: "Ready", role: "ok" }, ...over };
  return { uid: a.uid, kind: "ExternalArtifact", rule: a.rule, artifact: a };
};
const rowTexts = () => [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");

describe("CompositionView (coverage S1c)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    setRows([]);
    useFleet.setState({ clusters: [{ name: "c", state: "Synced", fluxPresent: true, fluxCompositionPresent: true } as never] });
  });

  it("lists broken-first, an artifact with no artifact as a fault", () => {
    setRows([gen("apps"), art("apps-a"), art("empty", { hasArtifact: false, rule: { included: true, role: "fault", label: "No artifact" } })]);
    render(<CompositionView cluster="c" />);
    expect(rowTexts()[0]).toContain("empty");
    expect(rowTexts()[0]).toContain("no artifact yet");
    expect(rowTexts().join("|")).toContain("1 source → 1 artifact");
  });

  it("an artifact leads with its own verdict; a failing producer is a separate block that never replaces it", async () => {
    setRows([gen("apps", { rule: fault, ready: "Failed", reason: "SourceFetchFailed" }),
      art("apps-a", { producer: { state: "verified", kind: "ArtifactGenerator", namespace: "flux-system", name: "apps", label: "Failed", role: "fault", failing: true } })]);
    render(<CompositionView cluster="c" />);
    fireEvent.click(screen.getByText("apps-a"));
    const panel = await screen.findByTestId("composition-panel");
    const text = panel.textContent ?? "";
    expect(text.indexOf("Ready")).toBeLessThan(text.indexOf("produced by ArtifactGenerator flux-system/apps"));
    expect(screen.getByTestId("composition-producer").textContent).toContain("The producer is failing; this artifact is its last good one");
    expect(screen.getByTestId("composition-artifact").textContent).toContain("http://source-watcher.flux-system.svc");
  });

  it("a disagreeing producer carries no health", async () => {
    setRows([art("apps-a", { producer: { state: "disagree", kind: "ArtifactGenerator", namespace: "flux-system", name: "apps", recordedGone: true } })]);
    render(<CompositionView cluster="c" />);
    fireEvent.click(screen.getByText("apps-a"));
    const p = await screen.findByTestId("composition-producer");
    expect(p.textContent).toContain("disagree");
    expect(p.textContent).toContain("the recorded producer no longer exists");
    expect(p.textContent).not.toContain("·");
  });

  it("a generator awaiting evaluation says why, its sources come from the detail, and its verbs are its annotations", async () => {
    vi.mocked(getCompositionDetail).mockImplementationOnce(async (_c, _k, _ns, _n, uid) => ({ uid, events: [], consumers: [], sourcesState: "found",
      sources: [{ kind: "GitRepository", alias: "fleet", namespace: "flux-system", name: "fleet", found: true, state: "found", label: "Ready", role: "ok" }] }));
    setRows([gen("apps", { rule: { included: false, role: "unknown", label: "Resumed · awaiting evaluation" }, resume: "awaiting" })]);
    render(<CompositionView cluster="c" />);
    fireEvent.click(screen.getByText("apps"));
    expect((await screen.findByTestId("composition-awaiting")).textContent).toContain("records nothing when nothing drifts");
    await waitFor(() => expect(screen.getByTestId("composition-panel").textContent).toContain("fleet: GitRepository flux-system/fleet · Ready"));
    expect(screen.getByTestId("composition-panel").textContent).toContain("@fleet/apps/** → @artifact/");
    expect(vi.mocked(fluxOpAct)).not.toHaveBeenCalled();
  });
});
