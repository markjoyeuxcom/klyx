import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";
import { ThemeProvider } from "../../theme/ThemeProvider";
import type { SourcesState } from "../../bridge/sources";
import type { EndpointsState } from "../../bridge/endpoints";
import { _resetNavGuardForTest } from "../navGuard";

// Codex's review of S3a-4c, R6; S3b §13: Observability is ready - and an
// anchor to an installation applied - only once every panel has read, or
// failed to, whichever answers first: the endpoints and each kind's
// sources.

const ep = vi.hoisted(() => ({ getEndpoints: vi.fn() }));
vi.mock("../../bridge/endpoints", () => ({ getEndpoints: () => ep.getEndpoints(), setCredential: vi.fn(), removeCredential: vi.fn(), moveToKeychain: vi.fn(), testEndpoint: vi.fn() }));
const src = vi.hoisted(() => ({ getSources: vi.fn(), migrationOffers: vi.fn() }));
vi.mock("../../bridge/sources", () => ({
  getSources: (kind: string) => src.getSources(kind), migrationOffers: (kind: string) => src.migrationOffers(kind), planSources: vi.fn(), planSourcesFromFile: vi.fn(), applySources: vi.fn(),
  detect: vi.fn(), setSourceCredential: vi.fn(), removeSourceCredential: vi.fn(), testSource: vi.fn(), testSubscription: vi.fn(),
}));
import { ObservabilitySection } from "./panels";

const sources: SourcesState = { kind: "alertmanager", sentence: "Alerts: 1 cluster", installations: [], subscriptions: [], fileDiffers: false };
const endpoints: EndpointsState = { rows: [] };

describe("Observability's readiness", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    _resetNavGuardForTest();
    src.migrationOffers.mockResolvedValue([]);
  });

  for (const sourcesRead of [true, false]) {
    it(`waits for every kind's sources after the endpoints answer (the sources ${sourcesRead ? "read" : "fail"})`, async () => {
      const answers: Record<string, (s: SourcesState | null) => void> = {};
      src.getSources.mockImplementation((kind: string) => new Promise((r) => { answers[kind] = r; }));
      ep.getEndpoints.mockResolvedValue(endpoints);
      const ready = vi.fn();
      render(<ThemeProvider><ObservabilitySection say={() => {}} ready={ready} /></ThemeProvider>);
      await act(async () => {});
      expect(ep.getEndpoints).toHaveBeenCalled();
      expect(Object.keys(answers).sort()).toEqual(["alertmanager", "loki", "metrics"]);
      await act(async () => { answers.alertmanager(sourcesRead ? sources : null); answers.loki(sourcesRead ? { ...sources, kind: "loki" } : null); });
      expect(ready).not.toHaveBeenCalled(); // the metrics panel has not read
      await act(async () => { answers.metrics(sourcesRead ? { ...sources, kind: "metrics" } : null); });
      expect(ready).toHaveBeenCalledTimes(1);
    });
  }

  it("says every kind's sentence once each has read", async () => {
    src.getSources.mockImplementation((kind: string) => Promise.resolve({ ...sources, kind, sentence: `${kind}: 1 cluster` }));
    ep.getEndpoints.mockResolvedValue(endpoints);
    const say = vi.fn();
    render(<ThemeProvider><ObservabilitySection say={say} ready={() => {}} /></ThemeProvider>);
    await act(async () => {});
    expect(say).toHaveBeenLastCalledWith("alertmanager: 1 cluster; loki: 1 cluster; metrics: 1 cluster");
    // never a partial one: a kind's sentence is said only with every kind's
    for (const [said] of say.mock.calls) expect(String(said).includes(": 1 cluster") && !String(said).includes("metrics: 1 cluster")).toBe(false);
  });

  it("waits for the endpoints, even failing, after the sources answer", async () => {
    let answer: (s: EndpointsState | null) => void = () => {};
    ep.getEndpoints.mockImplementation(() => new Promise((r) => { answer = r; }));
    src.getSources.mockResolvedValue(sources);
    const ready = vi.fn();
    render(<ThemeProvider><ObservabilitySection say={() => {}} ready={ready} /></ThemeProvider>);
    await act(async () => {});
    expect(ready).not.toHaveBeenCalled();
    await act(async () => { answer(null); });
    expect(ready).toHaveBeenCalledTimes(1);
  });
});
