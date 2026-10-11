import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, act } from "@testing-library/react";

vi.mock("../bridge/healthRows", () => ({ openRows: vi.fn(() => () => {}) }));
import { useCertManagerRows, _resetCertManagerRowsForTest } from "./useCertManagerRows";
import { useHealth, resetHealthStore, addSub, openStarted, openSucceeded, openFailed, applyRows, applySummary, dueForBootstrap, type HealthPart, type RowsDTO } from "../store/health";
import { useFleet } from "../store/fleet";

// The cert-manager lens's data from the shared rows (1c slice 3 §5).

const part = (id: string, kinds: HealthPart["kinds"], ver = 1): HealthPart => ({
  id, entity: id, scope: "", classifier: "native", coverage: "live", kinds, obs: ver, ver,
});
const live = (kind: string): HealthPart["kinds"][number] => ({ kind, role: "counted", scope: "", coverage: "live", objects: 1 });
const rowsOf = (cluster: string, id: string, rows: unknown[], kinds: HealthPart["kinds"], ver = 1): RowsDTO => ({ cluster, incarnation: 1, task: "certs", part: part(id, kinds, ver), evaluatedAt: "2026-10-11T12:00:00Z", rows });

function seed(cluster: string, handle: string, id: string, dto?: RowsDTO, error?: string) {
  addSub(handle, cluster, "certs", id);
  openStarted(handle, `tok-${handle}`);
  if (error) openFailed(handle, `tok-${handle}`, error, 0);
  else {
    openSucceeded(handle, `tok-${handle}`, 1);
    if (dto) applyRows(handle, dto, 0);
  }
}

function Probe({ cluster }: { cluster: string | null }) {
  useCertManagerRows(cluster);
  return null;
}

beforeEach(() => {
  resetHealthStore();
  _resetCertManagerRowsForTest();
  useFleet.getState().clearCertManager();
});

describe("useCertManagerRows", () => {
  it("fills the slice from both parts, with the Observer's coverage unreduced", () => {
    seed("A", "c", "certificates", rowsOf("A", "certificates", [{ uid: "u1", name: "web" }], [live("certificates")]));
    seed("A", "i", "issuers", rowsOf("A", "issuers", [{ uid: "i1", name: "le" }], [live("issuers"),
      { kind: "clusterissuers", role: "counted", scope: "", coverage: "stale", class: "forbidden", error: "rbac", objects: 0, lastGoodAt: "2026-10-11T11:00:00Z" }]));
    render(<Probe cluster="A" />);
    const cm = useFleet.getState().certmanager;
    expect(cm).toMatchObject({ cluster: "A", loading: false });
    expect(cm.certificates).toEqual([{ uid: "u1", name: "web" }]);
    expect(cm.issuers).toEqual([{ uid: "i1", name: "le" }]);
    expect(cm.coverage?.map((k) => [k.kind, k.coverage])).toEqual([["Certificate", "live"], ["Issuer", "live"], ["ClusterIssuer", "stale"]]);
    expect(cm.coverage?.[2].lastGoodAt).toBe("2026-10-11T11:00:00Z");
    expect(cm.watchError).toBe("ClusterIssuer: forbidden: rbac");
  });

  it("a failed open is unreadable kinds and an error, never an empty list", () => {
    seed("A", "c", "certificates", rowsOf("A", "certificates", [], [live("certificates")]));
    seed("A", "i", "issuers", undefined, "cluster not observed: A");
    render(<Probe cluster="A" />);
    const cm = useFleet.getState().certmanager;
    expect(cm.coverage?.filter((k) => k.coverage === "unreadable").map((k) => k.kind)).toEqual(["Issuer", "ClusterIssuer"]);
    expect(cm.watchError).toContain("cluster not observed: A");
  });

  it("loads until a part answers; an unanswered part stays checking, never another part's evidence", () => {
    addSub("c", "A", "certs", "certificates");
    addSub("i", "A", "certs", "issuers");
    render(<Probe cluster="A" />);
    expect(useFleet.getState().certmanager).toMatchObject({ cluster: "A", loading: true });
    act(() => { openStarted("c", "tok-c"); openSucceeded("c", "tok-c", 1); applyRows("c", rowsOf("A", "certificates", [{ uid: "u1" }], [live("certificates")]), 0); });
    const cm = useFleet.getState().certmanager;
    expect(cm.loading).toBe(false);
    expect(cm.coverage?.map((k) => [k.kind, k.coverage])).toEqual([["Certificate", "live"], ["Issuer", "checking"], ["ClusterIssuer", "checking"]]);
    expect(cm.issuers).toEqual([]);
  });

  it("never writes over another cluster's visit", () => {
    seed("A", "c", "certificates", rowsOf("A", "certificates", [{ uid: "a" }], [live("certificates")]));
    seed("A", "i", "issuers", rowsOf("A", "issuers", [], [live("issuers")]));
    const { rerender } = render(<Probe cluster="A" />);
    act(() => useFleet.getState().setCertManagerLoading("B")); // B's lens took the slice
    rerender(<Probe cluster="A" />);
    act(() => { applyRows("c", rowsOf("A", "certificates", [{ uid: "a2" }], [live("certificates")], 2), 0); });
    expect(useFleet.getState().certmanager).toMatchObject({ cluster: "B", certificates: [] });
  });

  it("two consumers share the slice; the last to leave clears it", () => {
    seed("A", "c", "certificates", rowsOf("A", "certificates", [{ uid: "a" }], [live("certificates")]));
    seed("A", "i", "issuers", rowsOf("A", "issuers", [], [live("issuers")]));
    const first = render(<Probe cluster="A" />);
    const second = render(<Probe cluster="A" />);
    first.unmount();
    expect(useFleet.getState().certmanager.cluster).toBe("A");
    second.unmount();
    expect(useFleet.getState().certmanager.cluster).toBeNull();
  });

  it("passes the A4 state through: refreshing past the deadline", () => {
    seed("A", "c", "certificates", rowsOf("A", "certificates", [], [live("certificates")]));
    seed("A", "i", "issuers", rowsOf("A", "issuers", [], [live("issuers")]));
    render(<Probe cluster="A" />);
    act(() => {
      applySummary({ cluster: "A", incarnation: 1, seq: 2, evaluatedAt: "", connection: "connected",
        tasks: [{ id: "certs", coverage: "live", parts: [part("certificates", [live("certificates")], 5), part("issuers", [live("issuers")], 1)] }] }, 0);
      dueForBootstrap(2000);
    });
    expect(useFleet.getState().certmanager.rowsState).toMatchObject({ refreshing: true });
    expect(Object.keys(useHealth.getState().subs).length).toBe(2);
  });

  it("a refreshing part names its own capture time, and the totals move to the summary's", () => {
    seed("A", "c", "certificates", { ...rowsOf("A", "certificates", [], [live("certificates")]), evaluatedAt: "2026-10-11T12:05:00Z" });
    seed("A", "i", "issuers", { ...rowsOf("A", "issuers", [], [live("issuers")]), evaluatedAt: "2026-10-11T11:55:00Z" });
    render(<Probe cluster="A" />);
    act(() => {
      const issuers = { ...part("issuers", [live("issuers")], 4), native: { total: 7, included: 2, word: "", roles: { total: 7, fault: 2, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 5 } } };
      applySummary({ cluster: "A", incarnation: 1, seq: 2, evaluatedAt: "", connection: "connected",
        tasks: [{ id: "certs", coverage: "live", parts: [part("certificates", [live("certificates")], 1), issuers] }] }, 0);
      dueForBootstrap(2000);
    });
    const cm = useFleet.getState().certmanager;
    expect(cm.rowsState).toMatchObject({ refreshing: true, evaluatedAt: "2026-10-11T11:55:00Z" }); // the issuers' rows, not the certificates'
    expect(cm.partCounts?.issuers).toEqual({ total: 7, included: 2 });
  });
});
