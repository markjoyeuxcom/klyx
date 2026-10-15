import { describe, it, expect, beforeEach, vi } from "vitest";
import type { LineDTO } from "./line";
import { applyLineGit, lineGitFor, resetLineGitStore, useLineGit, type LineGitDTO } from "./lineGit";

// The Line slice 6d: the store keeps only newer orderings, and an ordering
// is shown only beside the LineDTO version it answers.
const handlers: ((ev: { data: unknown }) => void)[] = [];
const order: string[] = [];
let resolve: (v: unknown) => void = () => {};
vi.mock("@wailsio/runtime", () => ({
  Events: { On: (name: string, h: (ev: { data: unknown }) => void) => { order.push(`on:${name}`); handlers.push(h); return () => {}; } },
}));
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  GitService: { LineGit: () => { order.push("LineGit()"); return new Promise((r) => { resolve = r; }); }, FetchDue: () => { order.push("FetchDue()"); return Promise.resolve(); } },
}));
import { _resetLineGitBridgeForTest, fetchDue, startLineGit } from "../bridge/lineGit";

const g = (version: number, lineVersion: number) => ({ version, lineVersion, state: "ready", stations: [], ledger: [], only: [] } as unknown as LineGitDTO);

beforeEach(() => {
  handlers.length = 0;
  order.length = 0;
  resetLineGitStore();
  _resetLineGitBridgeForTest();
});

describe("the line:git store and bridge", () => {
  it("keeps only newer versions", () => {
    expect(applyLineGit(g(2, 1))).toBe(true);
    expect(applyLineGit(g(1, 1))).toBe(false);
    expect(applyLineGit(g(2, 1))).toBe(false);
    expect(useLineGit.getState().dto?.version).toBe(2);
  });

  it("fences an ordering to the LineDTO version it answers", () => {
    const line = { version: 5 } as LineDTO;
    expect(lineGitFor(g(3, 5), line)?.version).toBe(3);
    expect(lineGitFor(g(3, 4), line)).toBeNull();
    expect(lineGitFor(g(3, 5), null)).toBeNull();
  });

  it("subscribes before it reads; starting twice is one start; an older reply never wins", async () => {
    startLineGit();
    startLineGit();
    expect(order).toEqual(["on:line:git", "LineGit()"]);
    handlers.forEach((h) => h({ data: g(4, 2) }));
    resolve(g(3, 2));
    await Promise.resolve();
    await Promise.resolve();
    expect(useLineGit.getState().dto?.version).toBe(4);
    fetchDue();
    expect(order).toContain("FetchDue()");
  });
});
