import { describe, expect, it } from "vitest";
import { attentionTone, isAttention, kindOf, labelOf, matchesFilter, summarize, VERDICT_ORDER } from "./verdict";

describe("kindOf fails closed", () => {
  it("passes known kinds through", () => {
    for (const k of VERDICT_ORDER) expect(kindOf({ kind: k })).toBe(k);
  });
  it.each([undefined, null, {}, { kind: "" }, { kind: "healthy" }, { kind: "OK" }])("treats %j as unknown", (h) => {
    expect(kindOf(h as never)).toBe("unknown");
  });
  it("never reads a legacy rank as health", () => {
    expect(kindOf({ rank: 3 } as never)).toBe("unknown");
  });
});

describe("labelOf", () => {
  it("uses the backend label", () => {
    expect(labelOf({ kind: "fault", label: "Exited 1" })).toBe("Exited 1");
  });
  it("never invents a healthy word for a missing verdict", () => {
    expect(labelOf(undefined)).toBe("Unknown");
    expect(labelOf({ kind: "garbage" })).toBe("Unknown");
  });
  it("never shows a healthy label carried by an invalid or default kind", () => {
    expect(labelOf({ kind: "", label: "Available" })).toBe("Unknown");
    expect(labelOf({ kind: "healthy", label: "Ready" })).toBe("Unknown");
  });
  it("keeps a legitimate backend unknown label", () => {
    expect(labelOf({ kind: "unknown", label: "Unverified" })).toBe("Unverified");
  });
});

describe("summarize", () => {
  const rows = [
    { h: { kind: "fault" } },
    { h: { kind: "fault" } },
    { h: { kind: "degraded" } },
    { h: { kind: "ok" } },
    { h: { kind: "paused" } },
    { h: undefined },
  ];
  it("counts each kind and attention = fault + degraded", () => {
    const c = summarize(rows, (r) => r.h);
    expect(c).toEqual({ total: 6, fault: 2, degraded: 1, unknown: 1, progressing: 0, paused: 1, ok: 1, attention: 3 });
  });
  it("paused and progressing are never attention", () => {
    expect(isAttention("paused")).toBe(false);
    expect(isAttention("progressing")).toBe(false);
  });
});

describe("attentionTone keeps zero neutral", () => {
  it("is undefined for zero", () => expect(attentionTone({ fault: 0, degraded: 0 })).toBeUndefined());
  it("is danger with any fault", () => expect(attentionTone({ fault: 1, degraded: 5 })).toBe("danger"));
  it("is warning with only degraded", () => expect(attentionTone({ fault: 0, degraded: 2 })).toBe("warning"));
});

describe("matchesFilter", () => {
  it("attention excludes unknown, progressing, paused, ok", () => {
    expect(VERDICT_ORDER.filter((k) => matchesFilter(k, "attention"))).toEqual(["fault", "degraded"]);
  });
  it("unknown filter matches only unknown", () => {
    expect(VERDICT_ORDER.filter((k) => matchesFilter(k, "unknown"))).toEqual(["unknown"]);
  });
});

