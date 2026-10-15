import { describe, it, expect } from "vitest";
import type { LineDTO, LineStationDTO } from "../store/line";
import type { LineGitDTO, LineGitStationDTO } from "../store/lineGit";
import { commitHref, gitStation, heldText, historyNote, ordering, orderedText, targetText } from "./history";
import { lineVerdict } from "./verdict";
import { stationStrip } from "./stationStrip";

// The Line slice 6d: the line's words with Git history - ordered states, T
// and its freshness, held behind T, and why there is no ordering.

const T = "a1b9e3c0a1b2c3d4e5f60718293a4b5c6d7e8f90";
const OLD = "9f2c11d0a1b2c3d4e5f60718293a4b5c6d7e8f90";
const MB = "4c1d2e00a1b2c3d4e5f60718293a4b5c6d7e8f90";
const st = (cluster: string, env: string, rank: number, over: Partial<LineStationDTO> = {}): LineStationDTO =>
  ({ cluster, env, track: "westeurope", rank, state: "differs", single: OLD, e: 1, a: [OLD], u: 0, read: "ok", units: [], flags: { failing: false }, ...over } as unknown as LineStationDTO);
const line = (stations: LineStationDTO[], over: Partial<LineDTO> = {}): LineDTO => ({
  version: 4, key: { kind: "git", repo: "github.com/acme/fleet", refKind: "branch", refName: "main", label: "github.com/acme/fleet @ branch main" },
  keyHow: "most stations", keyStations: stations.length, keys: [], r: OLD, rStations: 2, applied: 2, compared: stations.length, notCompared: 0, checking: 0,
  tracks: [{ name: "westeurope", kind: "region", label: "westeurope" }], fallback: false, stations, siding: [], mesh: { drawn: [], card: {}, offFleet: [] }, ...over,
} as unknown as LineDTO);
const gs = (cluster: string, state: string, over: Partial<LineGitStationDTO> = {}): LineGitStationDTO => ({ cluster, sha: OLD, state, ...over } as LineGitStationDTO);
const git = (stations: LineGitStationDTO[], over: Partial<LineGitDTO> = {}): LineGitDTO => ({
  version: 9, lineVersion: 4, state: "ready", repo: "git@github.com:acme/fleet.git", commitLink: "https://github.com/acme/fleet/commit/{sha}",
  target: { ref: "refs/heads/main", name: "main", sha: T, fetchedAt: new Date(2026, 9, 15, 20, 31).toISOString() },
  stations, ledger: [], only: [], ...over,
} as unknown as LineGitDTO);

describe("ordered words", () => {
  it("each ordered state in the parent's words", () => {
    const g = git([]);
    expect(orderedText(gs("a", "at", { sha: T }), g)).toBe("a1b9e3c · at main");
    expect(orderedText(gs("a", "behind", { n: 3 }), g)).toBe("9f2c11d · 3 behind main");
    expect(orderedText(gs("a", "behind", { n: 3, held: { kind: "attempted", text: "attempted a1b9e3c, failing", unit: "" } }), g)).toBe("9f2c11d · held · 3 behind");
    expect(orderedText(gs("a", "ahead", { n: 2 }), g)).toBe("9f2c11d · 2 ahead of main");
    expect(orderedText(gs("a", "diverged", { mergeBase: MB }), g)).toBe("9f2c11d · diverged at 4c1d2e0");
    expect(orderedText(gs("a", "unrelated"), g)).toBe("9f2c11d · unrelated history");
    expect(orderedText(gs("a", "unknown"), g)).toBe("9f2c11d · not in fetched history");
  });

  it("T names its freshness, and the last good history beside a failed fetch", () => {
    expect(targetText(git([]))).toBe("main at a1b9e3c · fetched 20:31");
    expect(targetText(git([], { target: { ref: "refs/heads/main", name: "main", sha: T, fetchedAt: new Date(2026, 9, 15, 20, 31).toISOString(), error: "Permission denied (publickey)" } as never })))
      .toBe("main at a1b9e3c · history as of 20:31; fetch failed: Permission denied (publickey)");
    expect(targetText(git([], { state: "unfetched" }))).toBeNull();
    expect(targetText(null)).toBeNull();
  });

  it("says why there is no ordering, and nothing when there is one or no Git key", () => {
    expect(historyNote(git([], { state: "unapproved", approve: "github.com/acme/fleet", reason: "x" }))).toBe("Git history: approve github.com/acme/fleet in Settings > Git to order the line by its commits");
    expect(historyNote(git([], { state: "notarget", reason: "a semver range has no single target" }))).toBe("Git history: a semver range has no single target");
    expect(historyNote(git([], { state: "checking" }))).toBe("Git history: checking…");
    expect(historyNote(git([], { state: "none", reason: "nothing is compared" }))).toBeNull();
    expect(historyNote(git([]))).toBeNull();
  });

  it("held behind T carries its evidence", () => {
    const g = git([]);
    expect(heldText(gs("p", "behind", { n: 1, held: { kind: "dependency", text: "blocked by dependency flux-system/infra", unit: "" } }), g)).toBe("held, 1 commit behind main: blocked by dependency flux-system/infra");
    expect(heldText(gs("p", "behind", { n: 3 }), g)).toBeNull();
    expect(heldText(gs("p", "diverged", { held: { kind: "source", text: "x", unit: "" } }), g)).toBeNull();
  });

  it("counts the ordering only when every compared station is ordered", () => {
    const dto = line([st("d", "dev", 0, { state: "matches", single: T }), st("s", "stg", 2), st("p", "prd", 3)]);
    expect(ordering(dto, git([gs("d", "at", { sha: T }), gs("s", "behind", { n: 1 }), gs("p", "diverged", { mergeBase: MB })])))
      .toEqual({ at: 1, behind: 1, ahead: 0, diverged: 1, unrelated: 0, compared: 3 });
    expect(ordering(dto, git([gs("d", "at"), gs("s", "behind"), gs("p", "unknown")]))).toBeNull();
    expect(ordering(dto, git([gs("d", "at"), gs("s", "behind")]))).toBeNull();
    expect(ordering(line([st("d", "dev", 0, { state: "mixed", single: undefined })]), git([]))).toBeNull();
  });

  it("an ordering for a stopped state shows no station", () => {
    expect(gitStation(git([gs("a", "at")], { state: "unfetched" }), "a")).toBeNull();
    expect(commitHref(git([]), T)).toBe(`https://github.com/acme/fleet/commit/${T}`);
    expect(commitHref(git([], { commitLink: "" }), T)).toBeNull();
  });
});

describe("the verdict with Git history", () => {
  const dto = line([st("d", "dev", 0, { state: "matches", single: T }), st("s", "stg", 2), st("p", "prd", 3, { held: true, heldWhy: "attempted 9f2c11d, failing" })]);
  const ordered = git([gs("d", "at", { sha: T }), gs("s", "behind", { n: 1 }),
    gs("p", "behind", { n: 3, held: { kind: "attempted", text: "attempted a1b9e3c, failing", unit: "Kustomization flux-system/apps" } })]);

  it("speaks of T when every compared station is ordered, and names T's freshness", () => {
    const v = lineVerdict(dto, ordered);
    expect(v.headline).toBe("1 of 3 compared stations at main · 2 behind");
    expect(v.target).toBe("main at a1b9e3c · fetched 20:31");
    // held behind T, with evidence tied to T, replaces held at R
    expect(v.clauses).toEqual(["prd · westeurope is held, 3 commits behind main: attempted a1b9e3c, failing"]);
    expect(v.history).toBeNull();
  });

  it("stays on R when a compared station is not ordered, and never without the fence", () => {
    const partial = git([gs("d", "at", { sha: T }), gs("s", "unknown"), gs("p", "behind", { n: 3 })]);
    expect(lineVerdict(dto, partial).headline).toMatch(/^9f2c11d is applied on 2 of 3 compared stations/);
    expect(lineVerdict(dto, null).target).toBeNull();
    expect(lineVerdict(dto, git([], { state: "unapproved", approve: "github.com/acme/fleet" })).history).toContain("approve github.com/acme/fleet");
  });

  it("the station strip boxes where this station stands against T", () => {
    const strip = stationStrip(dto, "s", undefined, ordered);
    expect(strip?.kind).toBe("track");
    const me = strip?.kind === "track" ? strip.stations.find((s) => s.here) : null;
    expect(me?.box).toBe("9f2c11d · 1 behind main");
    expect(me?.text).toContain("9f2c11d · 1 behind main");
    // without history the strip keeps §3.3's words
    const plain = stationStrip(dto, "s");
    expect(plain?.kind === "track" ? plain.stations.find((s) => s.here)?.box : null).toBe("differs at 9f2c11d");
  });
});
