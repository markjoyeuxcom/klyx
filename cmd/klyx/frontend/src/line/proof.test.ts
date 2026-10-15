import { describe, it, expect } from "vitest";
import { proofText } from "./proof";

// The Line slice 5 §3.3 (after parent §8.2): a proof's words never read a
// first sighting, or a capped proof, as an age.
const at = (hh: number, mm: number) => new Date(2026, 9, 15, hh, mm).toISOString();

describe("proofText", () => {
  it("each basis in its own words", () => {
    expect(proofText(null)).toBeNull();
    expect(proofText({ basis: "rule", since: at(20, 21) })).toBe("since 20:21");
    expect(proofText({ basis: "observed", notBefore: at(20, 19), since: at(20, 21) })).toBe("between 20:19 and 20:21 (observed)");
    expect(proofText({ basis: "observed", since: at(20, 21) })).toBe("by 20:21 (observed)");
    expect(proofText({ basis: "sampled", notBefore: at(20, 19), since: at(20, 21) })).toBe("between 20:19 and 20:21 (between two reads)");
    expect(proofText({ basis: "derived" })).toBe("start unknown");
    expect(proofText({ basis: "unknown", firstSeen: at(20, 21) })).toBe("first observed 20:21; start unknown");
    expect(proofText({ basis: "unknown", firstSeen: at(20, 21), afterGap: true })).toBe("observed 20:21, after a gap; start unknown");
  });

  it("a capped proof carries no time and claims none (design review d25f7275)", () => {
    expect(proofText({ basis: "unknown", capped: true })).toBe("start unknown (proof capped)");
  });

  it("a rule or window basis without its time is start unknown, never an age", () => {
    expect(proofText({ basis: "rule" })).toBe("start unknown");
    expect(proofText({ basis: "observed" })).toBe("start unknown");
    expect(proofText({ basis: "something-new" })).toBe("start unknown");
  });
});
