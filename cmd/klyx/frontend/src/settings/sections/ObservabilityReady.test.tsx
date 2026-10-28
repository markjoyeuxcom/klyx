import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";
import { ThemeProvider } from "../../theme/ThemeProvider";
import type { SourcesState } from "../../bridge/sources";
import type { EndpointsState } from "../../bridge/endpoints";
import { _resetNavGuardForTest } from "../navGuard";

// Codex's review of S3a-4c, R6: Observability is ready - and an anchor to
// an installation applied - only once both of its panels have read, or
// failed to, whichever answers first.

const ep = vi.hoisted(() => ({ getEndpoints: vi.fn() }));
vi.mock("../../bridge/endpoints", () => ({ getEndpoints: () => ep.getEndpoints(), setCredential: vi.fn(), removeCredential: vi.fn(), moveToKeychain: vi.fn(), testEndpoint: vi.fn() }));
const src = vi.hoisted(() => ({ getSources: vi.fn(), migrationOffers: vi.fn() }));
vi.mock("../../bridge/sources", () => ({
  getSources: () => src.getSources(), migrationOffers: () => src.migrationOffers(), planSources: vi.fn(), planSourcesFromFile: vi.fn(), applySources: vi.fn(),
  detect: vi.fn(), setSourceCredential: vi.fn(), removeSourceCredential: vi.fn(), testSource: vi.fn(),
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
    it(`waits for the sources after the endpoints answer (the sources ${sourcesRead ? "read" : "fail"})`, async () => {
      let answer: (s: SourcesState | null) => void = () => {};
      src.getSources.mockImplementation(() => new Promise((r) => { answer = r; }));
      ep.getEndpoints.mockResolvedValue(endpoints);
      const ready = vi.fn();
      render(<ThemeProvider><ObservabilitySection say={() => {}} ready={ready} /></ThemeProvider>);
      await act(async () => {});
      expect(ep.getEndpoints).toHaveBeenCalled();
      expect(ready).not.toHaveBeenCalled();
      await act(async () => { answer(sourcesRead ? sources : null); });
      expect(ready).toHaveBeenCalledTimes(1);
    });
  }

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
