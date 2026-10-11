import { describe, it, expect, beforeEach, beforeAll, afterAll, vi } from "vitest";
import { render, act } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { ClusterDTO, InstanceDTO, ResourceRef } from "../store/fleet";

const rows: InstanceDTO[] = [
  { namespace: "apps", name: "config-a", created: "" },
  { namespace: "apps", name: "config-b", created: "" },
];
vi.mock("../bridge/crd", () => ({
  // A loader that behaves like the real one: claims loading, then fills rows.
  loadInstances: vi.fn(async (cluster: string, ref: ResourceRef) => {
    const { useFleet: store } = await import("../store/fleet");
    store.getState().setInstancesLoading(cluster, ref);
    store.getState().setInstancePage(rows, "");
  }),
  listInstancePage: vi.fn(async () => ({ items: [], nextToken: "" })),
  getInstanceDetail: vi.fn(async () => {}),
  gitOpsOwnership: vi.fn(async () => null),
  copyText: vi.fn(async () => {}),
  revealSecretKey: vi.fn(async () => null),
  dryRunInstanceYAML: vi.fn(async () => ({ ok: true, error: "" })),
  applyInstanceYAML: vi.fn(async () => ({ ok: true, error: "" })),
}));
import { getInstanceDetail, loadInstances } from "../bridge/crd";
import { ClusterDetail } from "../cluster/ClusterDetail";
import { _resetHistoryForTest, back, forward, initHistory } from "./history";

const cm: ResourceRef = { group: "", version: "v1", plural: "configmaps", kind: "ConfigMap", scope: "Namespaced" };
const tick = () => act(async () => { await new Promise<void>((r) => setTimeout(r, 0)); });
const widthDesc = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientWidth");
beforeAll(() => { Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 1800 }); });
afterAll(() => { if (widthDesc) Object.defineProperty(HTMLElement.prototype, "clientWidth", widthDesc); });
let off = () => {};

beforeEach(() => {
  vi.clearAllMocks();
  off();
  _resetHistoryForTest();
  useFleet.setState({ clusters: [{ name: "x", state: "Synced" } as ClusterDTO], route: { name: "fleet" }, navIntent: null });
  off = initHistory();
});

describe("history across instances of a mounted list (Codex 7b review)", () => {
  for (const section of ["resources", "crds"] as const) {
    it(`A -> B -> Back -> Forward keeps the ${section} list populated and reloads each detail`, async () => {
      useFleet.setState({ route: { name: "cluster", cluster: "x", section } });
      useFleet.getState().openResource(cm);
      render(<ClusterDetail />);
      await tick();
      expect(useFleet.getState().instances.rows).toHaveLength(2);
      act(() => useFleet.getState().openInstance("apps", "config-a"));
      await tick();
      act(() => useFleet.getState().openInstance("apps", "config-b"));
      await tick();
      vi.mocked(getInstanceDetail).mockClear();

      act(() => { back(); });
      await tick();
      let s = useFleet.getState();
      expect(s.route).toMatchObject({ section, resource: cm, instance: { namespace: "apps", name: "config-a" } });
      expect(s.instances.rows).toHaveLength(2);
      expect(s.instances.loading).toBe(false);
      expect(getInstanceDetail).toHaveBeenCalledWith("x", cm, { namespace: "apps", name: "config-a" });

      act(() => { forward(); });
      await tick();
      s = useFleet.getState();
      expect(s.route).toMatchObject({ instance: { namespace: "apps", name: "config-b" } });
      expect(s.instances.rows).toHaveLength(2);
      expect(s.instances.loading).toBe(false);
      expect(loadInstances).toHaveBeenCalledTimes(1);
    });
  }
});
