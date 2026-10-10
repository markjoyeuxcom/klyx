import { describe, it, expect, vi } from "vitest";
import { useFleet } from "../store/fleet";

vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  ExternalSecretsService: { Open: vi.fn(async () => {}), Close: vi.fn(async () => {}) },
}));
const handlers: Record<string, (ev: unknown) => void> = {};
vi.mock("@wailsio/runtime", () => ({ Events: { On: vi.fn((n: string, h: (ev: unknown) => void) => { handlers[n] = h; return () => {}; }) } }));
import { openExternalSecrets } from "./externalsecrets";

describe("ESO bridge fences (redesign step 4d)", () => {
  it("a late watch callback for A never overwrites B", async () => {
    await openExternalSecrets("A");
    useFleet.getState().setExternalSecretsLoading("B");
    handlers["externalsecrets:updated"]?.({ data: { cluster: "A", secrets: [{ namespace: "x", name: "from-A" }], stores: [] } });
    expect(useFleet.getState().externalsecrets.cluster).toBe("B");
    expect(useFleet.getState().externalsecrets.secrets).toEqual([]);
  });

  it("per-kind coverage reaches the store", async () => {
    await openExternalSecrets("A");
    handlers["externalsecrets:updated"]?.({ data: { cluster: "A", secrets: [], stores: [], coverage: [{ kind: "ClusterSecretStore", served: true, synced: false, error: "forbidden" }] } });
    expect(useFleet.getState().externalsecrets.coverage?.[0].error).toBe("forbidden");
  });
});
