import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet, crdCountKey } from "../store/fleet";

vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  CRDService: {
    ListCRDs: vi.fn(),
    CountKind: vi.fn(),
    ListInstances: vi.fn(),
    GetInstanceDetail: vi.fn(),
  },
}));
vi.mock("@wailsio/runtime", () => ({ Clipboard: { SetText: vi.fn() } }));

import { CRDService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { listCRDs, countKind, listInstancePage, loadInstances } from "./crd";

const ok = (fn: unknown, v: unknown) => (fn as ReturnType<typeof vi.fn>).mockResolvedValue(v);
const ref = { group: "cert-manager.io", version: "v1", plural: "certificates", kind: "Certificate", scope: "Namespaced" };

// Review 2026-10-03 #10: CRD reads resolve with `error` instead of rejecting;
// none of them may surface as "no CRDs" / a zero count / an empty list.
describe("CRD bridge error honesty", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.getState().clearCRDs();
  });

  it("a failed CRD list flags the slice instead of claiming no CRDs", async () => {
    ok(CRDService.ListCRDs, { groups: [], error: "customresourcedefinitions is forbidden" });
    await listCRDs("c");
    expect(useFleet.getState().crd.error).toBe("customresourcedefinitions is forbidden");
  });

  it("a failed count is stored with its error, never as a real zero", async () => {
    ok(CRDService.CountKind, { count: 0, capped: false, error: "forbidden" });
    await countKind("c", "cert-manager.io", "v1", "certificates");
    expect(useFleet.getState().crd.counts[crdCountKey("c", "cert-manager.io", "v1", "certificates")]?.error).toBe("forbidden");
  });

  it("listInstancePage rejects on a failed page and passes notServed through", async () => {
    ok(CRDService.ListInstances, { items: [], nextToken: "", error: "timeout" });
    await expect(listInstancePage("c", ref)).rejects.toThrow("timeout");

    ok(CRDService.ListInstances, { items: [], nextToken: "", notServed: true });
    await expect(listInstancePage("c", ref)).resolves.toMatchObject({ items: [], notServed: true });
  });

  it("loadInstances records the failure on the instances slice", async () => {
    useFleet.getState().setInstancesLoading("c", ref);
    ok(CRDService.ListInstances, { items: [], nextToken: "", error: "certificates.cert-manager.io is forbidden" });
    await loadInstances("c", ref);
    const s = useFleet.getState().instances;
    expect(s.loading).toBe(false);
    expect(s.error).toMatch(/forbidden/);
  });
});
