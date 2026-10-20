import { describe, it, expect } from "vitest";
import { big, figure, gapOf, gpuCell, markWords, requestedText, totalsText } from "./extended";
import { markOf, marksOf } from "../health/useExtendedRows";
import type { ExtendedDTO, ReservationDTO } from "../store/fleet";
import type { HealthPart } from "../store/health";

// Coverage S11a §2.0, §2.1, §6: figures, marks and the lens's words.

const live = { nodes: "" as const, requested: "" as const, dra: "" as const };
const gpu = (capacity: string, allocatable: string): ExtendedDTO => ({ entries: [{ name: "nvidia.com/gpu", capacity, allocatable, accelerator: true, unit: "GPU", facts: [] }] });
const res = (requested: string, extra: Partial<ReservationDTO> = {}): ReservationDTO => ({ uid: "u", node: "n", entries: [{ name: "nvidia.com/gpu", advertised: true, requested, servedByDra: "0", notAdvertised: "0" }], ...extra });

describe("extended figures", () => {
  it("compares and subtracts past 2^53 exactly", () => {
    expect(big("9007199254740993")).toBe(9007199254740993n);
    expect(gapOf({ name: "x", capacity: "9007199254740995", allocatable: "9007199254740993", facts: [] })).toBe(2n);
    expect(big("-")).toBeNull();
    expect(big("")).toBeNull();
    expect(figure("-")).toBe("not established");
    expect(figure("")).toBe("not reported");
  });
  it("the GPU cell: one accelerator, its gap, 0 advertised, several", () => {
    expect(gpuCell(gpu("8", "6"), res("3"), live, false).text).toBe("3 / 6 · 2 not allocatable");
    expect(gpuCell(gpu("0", "0"), res("0"), live, false).text).toBe("0 advertised");
    const two: ExtendedDTO = { entries: [...gpu("8", "8").entries, { name: "nvidia.com/mig-1g.5gb", capacity: "7", allocatable: "7", accelerator: true, facts: [] }] };
    const cell = gpuCell(two, res("3"), live, false);
    expect(cell.text).toBe("2 resources");
    expect(cell.title).toContain("nvidia.com/mig-1g.5gb: 0 requested / 7 allocatable");
    expect(gpuCell({ entries: [{ name: "example.com/x", capacity: "2", allocatable: "2", facts: [] }] }, undefined, live, true).text).toBe("");
  });
  it("requested is ? without a row, an entry cut by the row's bound, or an unestablished read", () => {
    expect(requestedText(undefined, "nvidia.com/gpu", "", true)).toBe("?");
    expect(requestedText(res("3"), "nvidia.com/gpu", "?", false)).toBe("?");
    expect(requestedText({ uid: "u", node: "n", entries: [], omitted: 3 }, "nvidia.com/gpu", "", false)).toBe("?");
    expect(requestedText({ uid: "u", node: "n", entries: [] }, "nvidia.com/gpu", "*", false)).toBe("0*");
    expect(requestedText(res("-"), "nvidia.com/gpu", "", false)).toBe("?");
    expect(requestedText(res("4"), "nvidia.com/gpu", "+*", false)).toBe("4+*");
  });
  it("the footer's totals: each qualified by its own reads, omitted names counted", () => {
    const t = totalsText([{ name: "nvidia.com/gpu", capacity: "16", allocatable: "16", requested: "12" }], { nodes: "*", requested: "?", dra: "" }, 3);
    expect(t.text).toBe("16* nvidia.com/gpu allocatable (? requested) · totals for 3 more resources not shown");
    expect(totalsText([], live, 0).text).toBe("");
  });
});

describe("extended marks (S11a §2.1)", () => {
  const part = (nodes: string, pods: string, podsTruncated = false): HealthPart => ({ id: "reservations", entity: "", scope: "", classifier: "evidence", coverage: "live", obs: 1, ver: 1,
    kinds: [{ kind: "nodes", role: "counted", scope: "", coverage: nodes as never, objects: 1 }, { kind: "pods", role: "dependency", scope: "", coverage: pods as never, objects: 1, truncated: podsTruncated }] });
  it("requested depends on both reads; served by DRA on the pods read alone", () => {
    expect(marksOf(part("stale", "live"))).toEqual({ nodes: "*", requested: "*", dra: "" });
    expect(marksOf(part("live", "stale"))).toEqual({ nodes: "", requested: "*", dra: "*" });
    expect(marksOf(part("unreadable", "live"))).toEqual({ nodes: "?", requested: "?", dra: "" });
    expect(marksOf(part("live", "stale", true))).toEqual({ nodes: "", requested: "+*", dra: "+*" });
    expect(marksOf(null)).toEqual({ nodes: "?", requested: "?", dra: "?" });
  });
  it("markOf: unestablished beats everything; capped and stale are kept together", () => {
    expect(markOf([{ kind: "a", coverage: "checking" }, { kind: "b", coverage: "stale" }])).toBe("?");
    expect(markOf([undefined])).toBe("?");
    expect(markOf([{ kind: "a", coverage: "live", truncated: true }])).toBe("+");
    expect(markWords("+*", "requested")).toBe("requested is a lower bound, from the last observed read");
  });
});
