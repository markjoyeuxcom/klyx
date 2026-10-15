import { describe, it, expect, vi, beforeEach } from "vitest";
import type { ExpiryScanResultDTO } from "./fleet";

// The Line slice 5 §6 (design review d25f7275): one scan per cluster per
// session; a joining cluster once; a leaving cluster dropped; Refresh anew;
// a reply from before a Refresh, or for a cluster that left, never lands.
const pending: Record<string, ((r: ExpiryScanResultDTO) => void)[]> = {};
vi.mock("../bridge/expiry", () => ({
  getExpiryScan: vi.fn((cluster: string) => new Promise<ExpiryScanResultDTO>((res) => { (pending[cluster] ??= []).push(res); })),
}));
import { getExpiryScan } from "../bridge/expiry";
import { ensureExpiryScans, refreshExpiryScans, resetExpiryStore, useExpiry } from "./expiry";

const ok = (n: number): ExpiryScanResultDTO => ({ items: Array.from({ length: n }, (_, i) => ({ source: "tls-secret", namespace: "a", name: `s${i}`, detail: "", subject: "", notAfterUnix: 1 })), warnings: [] });
const answer = async (cluster: string, i: number, r: ExpiryScanResultDTO) => { pending[cluster][i](r); await Promise.resolve(); await Promise.resolve(); };
const calls = () => vi.mocked(getExpiryScan).mock.calls.map((c) => c[0]);

beforeEach(() => {
  vi.clearAllMocks();
  for (const k of Object.keys(pending)) delete pending[k];
  resetExpiryStore();
});

describe("the session's expiry scans", () => {
  it("each cluster once; a joining one once; nobody rescanned for it", async () => {
    ensureExpiryScans(["a", "b"]);
    ensureExpiryScans(["a", "b"]);
    expect(calls()).toEqual(["a", "b"]);
    await answer("a", 0, ok(1));
    ensureExpiryScans(["a", "b", "c"]);
    expect(calls()).toEqual(["a", "b", "c"]);
    expect(Object.keys(useExpiry.getState().results)).toEqual(["a"]);
  });

  it("a leaving cluster's result is dropped, and its late reply never lands", async () => {
    ensureExpiryScans(["a", "b"]);
    await answer("a", 0, ok(1));
    ensureExpiryScans(["b"]);
    expect(Object.keys(useExpiry.getState().results)).toEqual([]);
    ensureExpiryScans(["b"]);
    await answer("a", 0, ok(2)); // already answered; nothing new
    expect("a" in useExpiry.getState().results).toBe(false);
  });

  it("Refresh scans anew; a reply from before it never lands", async () => {
    ensureExpiryScans(["a"]);
    refreshExpiryScans(["a"]);
    expect(calls()).toEqual(["a", "a"]);
    await answer("a", 0, ok(5)); // the pre-Refresh reply
    expect(useExpiry.getState().results).toEqual({});
    await answer("a", 1, ok(1));
    expect(useExpiry.getState().results.a?.items).toHaveLength(1);
  });

  it("leave, then rejoin under the same name: the old reply never lands, the new one does (review of c20ef32)", async () => {
    ensureExpiryScans(["a"]);
    ensureExpiryScans([]); // a leaves while its scan is out
    ensureExpiryScans(["a"]); // and rejoins: a new scan
    expect(calls()).toEqual(["a", "a"]);
    await answer("a", 0, ok(5)); // the departed membership's reply
    expect(useExpiry.getState().results).toEqual({});
    await answer("a", 1, ok(1));
    expect(useExpiry.getState().results.a?.items).toHaveLength(1);
  });

  it("a rejected scan is a named failure", async () => {
    vi.mocked(getExpiryScan).mockRejectedValueOnce(new Error("timeout"));
    ensureExpiryScans(["a"]);
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    expect(useExpiry.getState().results.a?.error).toBe("timeout");
  });
});
