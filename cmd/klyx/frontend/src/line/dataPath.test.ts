import { describe, it, expect } from "vitest";
import { appliedText, fmtShare, onset, onsetText, proximityText, weightShares } from "./dataPath";
import type { RouteUnit } from "../bridge/ruleMetrics";

// The Line slice 8d (parent §5.2): onset as a typed result; revision
// proximity only beside a witnessed crossing, an observed change at most 10
// minutes before it, and continuous coverage; each value type its own words.

const T0 = 1792080000; // a whole minute
const pts = (...vs: (number | null)[]) => vs.flatMap((v, i) => (v === null ? [] : [{ t: T0 + i * 30, v }]));

describe("onset", () => {
  it("a crossing inside one segment", () => {
    expect(onset(pts(0, 0.005, 0.02, 0.03), 30)).toEqual({ kind: "crossing", at: T0 + 60 });
  });
  it("already above at the window's first sample: no crossing time", () => {
    expect(onset(pts(0.05, 0.06), 30)).toEqual({ kind: "above-at-first", at: T0, afterGap: false });
  });
  it("a gap restarts the search; above at the first sample after it", () => {
    expect(onset(pts(0, 0.02, null, null, 0.04, 0.05), 30)).toEqual({ kind: "above-at-first", at: T0 + 120, afterGap: true });
    expect(onset(pts(0.05, null, null, 0, 0, 0.02), 30)).toEqual({ kind: "crossing", at: T0 + 150 });
  });
  it("none above, or nothing attributable", () => {
    expect(onset(pts(0, 0.01, 0.005), 30)).toEqual({ kind: "none" }); // 1% itself is not above
    expect(onset([], 30)).toEqual({ kind: "no-samples" });
  });
  it("is worded with its rate and step", () => {
    expect(onsetText({ kind: "crossing", at: T0 }, 60, 30)).toMatch(/^first sampled crossing of 1% at \d\d:\d\d \(1-minute rate, 30-second step\)$/);
    expect(onsetText({ kind: "above-at-first", at: T0, afterGap: true }, 60, 30)).toMatch(/^already above 1% at \d\d:\d\d, the first sample after a gap/);
  });
});

describe("revision proximity", () => {
  const iso = (u: number) => new Date(u * 1000).toISOString();
  const unit = (over: Partial<NonNullable<RouteUnit["applied"]>> = {}): RouteUnit => ({
    kind: "Kustomization", namespace: "flux-system", name: "apps", uid: "u1",
    applied: { kind: "git", value: "a1b9e3c0d4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9", basis: "observed", observedAt: iso(T0 - 60), liveSince: iso(T0 - 3600), ...over },
  });
  const crossing = { kind: "crossing" as const, at: T0 };
  it("names the unit beside a witnessed crossing", () => {
    expect(proximityText(unit(), crossing)).toMatch(/^Kustomization flux-system\/apps \(manages this route now\): applied a1b9e3c, change observed \d\d:\d\d; first sampled crossing \d\d:\d\d$/);
  });
  it("is withheld without a crossing, an observed change, closeness or coverage", () => {
    expect(proximityText(unit(), { kind: "above-at-first", at: T0, afterGap: false })).toBeNull();
    expect(proximityText(unit({ basis: "unknown", observedAt: undefined }), crossing)).toBeNull();
    expect(proximityText(unit({ observedAt: iso(T0 - 11 * 60) }), crossing)).toBeNull(); // too far
    expect(proximityText(unit({ observedAt: iso(T0 + 30) }), crossing)).toBeNull(); // after the crossing
    expect(proximityText(unit({ liveSince: iso(T0 - 30) }), crossing)).toBeNull(); // coverage began after the change
    expect(proximityText(undefined, crossing)).toBeNull();
  });
  it("words each value type its own way", () => {
    expect(appliedText({ kind: "artifact", value: "latest@sha256:" + "f".repeat(64), basis: "observed", liveSince: "" })).toBe("artifact sha256:ffffffff");
    expect(appliedText({ kind: "chart", value: "podinfo@6.7.1", basis: "observed", liveSince: "" })).toBe("chart podinfo 6.7.1");
  });
});

describe("weights and shares", () => {
  it("configured weight shares within a rule", () => {
    expect(weightShares([90, 10])).toEqual([0.9, 0.1]);
    expect(weightShares([0, 0])).toEqual([0, 0]);
  });
  it("shares never read as zero when missing", () => {
    expect(fmtShare(undefined)).toBe("no samples");
    expect(fmtShare(0.0312)).toBe("3.1%");
  });
});
