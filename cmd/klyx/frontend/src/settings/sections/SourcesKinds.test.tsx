import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ThemeProvider } from "../../theme/ThemeProvider";
import type { Plan } from "../../bridge/plan";
import type { SourceKind, SourcesState } from "../../bridge/sources";
import type { EndpointsState } from "../../bridge/endpoints";
import { _resetNavGuardForTest, mustAsk } from "../navGuard";
import { SourcesPanel } from "./SourcesPanel";

// Settings S3b §8: Observability's Sources, one panel per kind. Each panel
// reads, plans and adopts its own kind, says its own words, has its own
// leave-guard owner and controls of their own address; Loki has no Detect
// and a subscription's own Test; metrics has Detect.

const b = vi.hoisted(() => ({
  getSources: vi.fn(),
  migrationOffers: vi.fn(),
  planSources: vi.fn(),
  planSourcesFromFile: vi.fn(),
  applySources: vi.fn(),
  detect: vi.fn(),
  setSourceCredential: vi.fn(),
  removeSourceCredential: vi.fn(),
  testSource: vi.fn(),
  testSubscription: vi.fn(),
}));
vi.mock("../../bridge/sources", () => b);
const ep = vi.hoisted(() => ({ getEndpoints: vi.fn() }));
vi.mock("../../bridge/endpoints", () => ({ getEndpoints: () => ep.getEndpoints(), setCredential: vi.fn(), removeCredential: vi.fn(), moveToKeychain: vi.fn(), testEndpoint: vi.fn() }));
import { ObservabilitySection } from "./panels";

const stateOf = (kind: SourceKind, over: Partial<SourcesState> = {}): SourcesState => ({
  kind,
  sentence: `${kind}: 1 cluster from 1 installation`,
  installations: [{ id: `lgtm-${kind}`, revision: "r1", where: "Service monitoring/x:80 in prd", credential: "not needed: read through its host", subscribers: ["dev"], host: "prd", serviceRef: { namespace: "monitoring", name: "x", port: "80" } }],
  subscriptions: [
    { cluster: "dev", mode: "subscribed", via: "its own block", source: `lgtm-${kind}`, sentence: `from lgtm-${kind}, hosted by prd, filtered by cluster=dev`, block: { source: `lgtm-${kind}` }, instRevision: "7", subRevision: "9" },
    { cluster: "prd", mode: "service", via: "its own block", sentence: "from the Service monitoring/x:80 on this cluster, unfiltered", legacy: true },
  ],
  fileDiffers: true,
  ...over,
});

const aPlan = (over: Partial<Plan> = {}): Plan => ({
  section: "observability", kind: "save", digest: "d1", fileVersion: "v1", runtime: {},
  changes: [{ id: "logs.dev", label: "Logs for dev", from: "none", to: "from lgtm-loki" }], ends: [], continues: [], writes: true, activates: true, ...over,
});

const renderKind = (kind: SourceKind) => render(<ThemeProvider><SourcesPanel kind={kind} refreshMs={0} /></ThemeProvider>);

describe("Settings > Observability > Sources, per kind (S3b §8)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    _resetNavGuardForTest();
    b.getSources.mockImplementation((kind: SourceKind) => Promise.resolve(stateOf(kind)));
    b.migrationOffers.mockResolvedValue([]);
    ep.getEndpoints.mockResolvedValue({ rows: [] } as EndpointsState);
  });

  it("the Logs panel reads, plans and adopts Loki's, in its own words, with no Detect", async () => {
    b.planSources.mockResolvedValue(aPlan());
    b.planSourcesFromFile.mockResolvedValue(aPlan({ kind: "file" }));
    renderKind("loki");
    await screen.findByText("Each cluster's logs");
    expect(b.getSources).toHaveBeenCalledWith("loki");
    expect(b.migrationOffers).toHaveBeenCalledWith("loki");
    expect(screen.queryByText("Detect")).toBeNull();
    expect(screen.getByText("Its own loki block is set in fleet.yaml; External endpoints tests it.")).toBeTruthy();
    expect(screen.getAllByTestId("subscription-row")[0].getAttribute("data-anchor")).toContain("clusters[].loki.source");
    fireEvent.click(screen.getByRole("button", { name: "use the file's log sources" }));
    await waitFor(() => expect(b.planSourcesFromFile).toHaveBeenCalledWith("loki"));
    // a subscription's draft is Loki's, its fields clusters[].loki.*
    fireEvent.click(screen.getByRole("button", { name: "edit dev's log subscription" }));
    const ed = await screen.findByRole("group", { name: "Edit dev's log subscription" });
    expect(ed.querySelector('[data-setting="clusters[].loki.source"]')).not.toBeNull();
    expect(ed.querySelector('[data-setting="clusters[].alertmanager.source"]')).toBeNull();
    fireEvent.click(within(ed).getByRole("checkbox", { name: /scope: tenant/ }));
    fireEvent.click(within(ed).getByRole("button", { name: "Review and apply" }));
    await waitFor(() => expect(b.planSources).toHaveBeenCalledWith({ kind: "loki", subscriptions: [{ cluster: "dev", block: { source: "lgtm-loki", scope: "tenant" } }] }));
  });

  it("a Loki row's Test sends the member's name and the revisions shown, never its scope", async () => {
    b.testSubscription.mockResolvedValue({ ok: true, message: "Available: Loki 3.4.2. dev's logs, read from lgtm-loki: streams answered in the last hour." });
    renderKind("loki");
    fireEvent.click(await screen.findByRole("button", { name: "test dev's log subscription" }));
    await waitFor(() => expect(b.testSubscription).toHaveBeenCalledWith("dev", "loki", "7", "9"));
    expect((await screen.findByTestId("sources-banner")).textContent).toContain("streams answered in the last hour");
    // a legacy row has no Test of its own
    expect(screen.queryByRole("button", { name: "test prd's log subscription" })).toBeNull();
  });

  it("the Logs panel's installation editor offers no discovery", async () => {
    renderKind("loki");
    fireEvent.click(await screen.findByRole("button", { name: "add a Loki installation" }));
    const ed = await screen.findByTestId("installation-editor");
    expect(within(ed).queryByLabelText("discovered in a host")).toBeNull();
    expect(within(ed).getByLabelText("a Service in a host")).toBeTruthy();
  });

  it("the Metrics panel has Detect for metrics, and a proposal reviews as a metrics draft", async () => {
    b.detect.mockResolvedValue({
      clusters: [{ cluster: "prd", outcome: "found", detail: "Service monitoring/prometheus-operated:9090 (found by its name)" }],
      proposals: [{ host: "prd", where: "Service monitoring/prometheus-operated:9090 in prd", sample: "label metadata for the hour", notes: ["This is label metadata for the hour asked for, not proof."],
        memberLabel: "cluster", membersSeen: ["dev", "prd"],
        draft: { kind: "metrics", installation: { op: "add", source: { id: "metrics-prd", host: "prd", serviceRef: { namespace: "monitoring", name: "prometheus-operated", port: "9090" }, memberLabel: "cluster", servesClusters: ["dev", "prd"] } } } }],
    });
    b.planSources.mockResolvedValue(aPlan());
    renderKind("metrics");
    fireEvent.change(await screen.findByLabelText("what metrics Detect looks in"), { target: { value: "c:prd" } });
    fireEvent.click(screen.getByRole("button", { name: "detect metrics" }));
    await waitFor(() => expect(b.detect).toHaveBeenCalledWith({ cluster: "prd", kind: "metrics" }));
    fireEvent.click(await screen.findByRole("button", { name: "Edit and review…" }));
    const ed = await screen.findByTestId("installation-editor");
    fireEvent.click(within(ed).getByRole("button", { name: "Review and apply" }));
    await waitFor(() => expect(b.planSources).toHaveBeenCalled());
    const d = b.planSources.mock.calls[0][0];
    expect(d.kind).toBe("metrics");
    expect(d.installation.source.id).toBe("metrics-prd");
  });

  it("the three panels have controls of their own address, and their own leave-guard owner", async () => {
    render(<ThemeProvider><ObservabilitySection say={() => {}} ready={() => {}} /></ThemeProvider>);
    await act(async () => {});
    for (const kind of ["alertmanager", "loki", "metrics"]) await screen.findByTestId(`sources-${kind}`);
    const names = Array.from(document.querySelectorAll("[data-testid^=sources-] button, [data-testid^=sources-] select, [data-testid^=sources-] input"))
      .map((el) => el.getAttribute("aria-label") ?? el.textContent ?? "")
      .filter((n) => n !== "");
    const dup = names.filter((n, i) => names.indexOf(n) !== i && !["Edit", "Test", "Remove…", "Edit its block", "Give it a block", "Use the file's values", "Add an installation", "Detect"].includes(n));
    expect(dup).toEqual([]);
    // each button that repeats in text has an address of its own
    const repeated = Array.from(document.querySelectorAll("[data-testid^=sources-] button")).filter((el) => !el.getAttribute("aria-label"));
    expect(repeated.map((el) => el.textContent)).toEqual([]);
    // a draft in the Logs panel is the Logs panel's: leaving asks, naming it
    const logs = screen.getByTestId("sources-loki");
    fireEvent.click(within(logs).getByRole("button", { name: "edit dev's log subscription" }));
    const ed = await within(logs).findByRole("group", { name: "Edit dev's log subscription" });
    fireEvent.click(within(ed).getByRole("checkbox", { name: /scope: tenant/ }));
    expect(mustAsk()).toBe(true);
    // another panel's own state, changing, never answers for the Logs draft
    const metrics = screen.getByTestId("sources-metrics");
    fireEvent.change(within(metrics).getByLabelText("what metrics Detect looks in"), { target: { value: "c:dev" } });
    expect(mustAsk()).toBe(true);
  });
});
