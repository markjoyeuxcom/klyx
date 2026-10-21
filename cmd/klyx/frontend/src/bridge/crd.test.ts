import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet, crdCountKey } from "../store/fleet";

vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  CRDService: {
    ListCRDs: vi.fn(),
    CountKind: vi.fn(),
    ListInstances: vi.fn(),
    GetInstanceDetail: vi.fn(),
    DeleteAdhocObject: vi.fn(),
  },
}));
vi.mock("@wailsio/runtime", () => ({ Clipboard: { SetText: vi.fn() } }));

import { CRDService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { listCRDs, countKind, listInstancePage, loadInstances, getInstanceDetail, deleteAdhocObject } from "./crd";

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

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}

// Review 2026-10-13 #3: identity guards alone let an older read of the same
// target win; reads are numbered and only the latest lands.
describe("CRD bridge read ordering", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.getState().clearInstances();
  });

  it("the latest detail read wins for the same object", async () => {
    const old = deferred<unknown>();
    const fresh = deferred<unknown>();
    vi.mocked(CRDService.GetInstanceDetail).mockImplementationOnce(() => old.promise as never).mockImplementationOnce(() => fresh.promise as never);
    const first = getInstanceDetail("c", ref, { namespace: "ns", name: "cert" });
    const second = getInstanceDetail("c", ref, { namespace: "ns", name: "cert" });
    fresh.resolve({ uid: "new", yaml: "new" });
    await second;
    old.resolve({ uid: "old", yaml: "old" });
    await first;
    expect(useFleet.getState().instanceDetail.detail?.uid).toBe("new");
  });

  it("a late page from an older list never appends to a refreshed one", async () => {
    const old = deferred<unknown>();
    useFleet.getState().setInstancesLoading("c", ref);
    useFleet.getState().setInstancePage([{ name: "first", namespace: "ns" } as never], "old-token");
    vi.mocked(CRDService.ListInstances)
      .mockImplementationOnce(() => old.promise as never)
      .mockResolvedValueOnce({ items: [{ name: "fresh", namespace: "ns" }], nextToken: "fresh-token" } as never);
    const page = loadInstances("c", ref, "old-token");
    await loadInstances("c", ref); // Refresh: a new list
    old.resolve({ items: [{ name: "obsolete", namespace: "ns" }], nextToken: "old-next" });
    await page;
    const s = useFleet.getState().instances;
    expect(s.rows.map((x) => x.name)).toEqual(["fresh"]);
    expect(s.nextToken).toBe("fresh-token");
  });

  it("load more appends when the list still ends at its token", async () => {
    useFleet.getState().setInstancesLoading("c", ref);
    vi.mocked(CRDService.ListInstances)
      .mockResolvedValueOnce({ items: [{ name: "a", namespace: "ns" }], nextToken: "t1" } as never)
      .mockResolvedValueOnce({ items: [{ name: "b", namespace: "ns" }], nextToken: "" } as never);
    await loadInstances("c", ref);
    await loadInstances("c", ref, "t1");
    const s = useFleet.getState().instances;
    expect(s.rows.map((x) => x.name)).toEqual(["a", "b"]);
    expect(s.nextToken).toBe("");
  });

  it("a failed duplicate continuation cannot taint a page that already landed (Codex review of cd291bd)", async () => {
    const old = deferred<unknown>();
    const fresh = deferred<unknown>();
    vi.mocked(CRDService.ListInstances)
      .mockResolvedValueOnce({ items: [{ namespace: "ns", name: "a" }], nextToken: "t1" } as never)
      .mockImplementationOnce(() => old.promise as never)
      .mockImplementationOnce(() => fresh.promise as never);
    await loadInstances("c", ref);
    const first = loadInstances("c", ref, "t1");
    const second = loadInstances("c", ref, "t1");
    fresh.resolve({ items: [{ namespace: "ns", name: "b" }], nextToken: "" });
    await second;
    old.resolve({ items: [], nextToken: "", error: "old duplicate timed out" });
    await first;
    const s = useFleet.getState().instances;
    expect(s.rows.map((x) => x.name)).toEqual(["a", "b"]);
    expect(s.error).toBeNull();
  });
});

// Review of ff41862: a rejected detail read is recorded as the read's
// outcome, so an object opened by UID reads "not verified".
describe("a failed detail read", () => {
  it("is recorded as an error, never the last good detail", async () => {
    useFleet.getState().clearInstanceDetail();
    vi.mocked(CRDService.GetInstanceDetail).mockRejectedValueOnce(new Error("bridge disconnected"));
    await getInstanceDetail("c", ref, { namespace: "ns", name: "cert", expectUid: "old-uid" });
    const d = useFleet.getState().instanceDetail.detail;
    expect(d?.error).toBe("bridge disconnected");
    expect(d?.notFound).toBeFalsy();
    expect(useFleet.getState().instanceDetail.loading).toBe(false);
  });
});

// Codex's S3b implementation review, finding 2: a refused scratch DELETE's
// finding crosses the real bridge
describe("deleteAdhocObject", () => {
  it("passes the refusal's finding through", async () => {
    const admission = { envelope: "webhook-denied", name: "deny.lab.klyx.dev", summary: "delete Pod klyx-scratch/p was refused", candidates: [] };
    ok(CRDService.DeleteAdhocObject, { ok: false, error: "refused", admission });
    const r = await deleteAdhocObject("c", { group: "", version: "v1", plural: "pods", kind: "Pod", namespace: "klyx-scratch", name: "p", uid: "u" } as never);
    expect(r).toEqual({ ok: false, error: "refused", admission });
    ok(CRDService.DeleteAdhocObject, { ok: true, error: "" });
    expect(await deleteAdhocObject("c", { group: "", version: "v1", plural: "pods", kind: "Pod", namespace: "klyx-scratch", name: "p", uid: "u" } as never)).toEqual({ ok: true, error: "" });
  });
});
