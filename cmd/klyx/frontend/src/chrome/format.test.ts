import { describe, it, expect } from "vitest";
import { compactCount } from "./format";

describe("compactCount", () => {
  it("passes small counts through exactly", () => {
    expect(compactCount(0)).toBe("0");
    expect(compactCount(7)).toBe("7");
    expect(compactCount(999)).toBe("999");
  });

  it("abbreviates thousands with one decimal below 10k", () => {
    expect(compactCount(1000)).toBe("1k");
    expect(compactCount(1234)).toBe("1.2k");
    expect(compactCount(9949)).toBe("9.9k");
    expect(compactCount(9960)).toBe("10k"); // rounds up across the decimal cutoff
    expect(compactCount(42_000)).toBe("42k");
    expect(compactCount(526_400)).toBe("526k");
    expect(compactCount(999_499)).toBe("999k");
  });

  it("rounding across a unit boundary promotes the unit", () => {
    expect(compactCount(999_500)).toBe("1M"); // never "1000k"
    expect(compactCount(999_500_000)).toBe("1B");
  });

  it("the flux-at-a-million case stays five characters", () => {
    expect(compactCount(1_000_000)).toBe("1M");
    expect(compactCount(1_140_000)).toBe("1.1M");
    expect(compactCount(28_000_000)).toBe("28M");
    expect(compactCount(2_100_000_000)).toBe("2.1B"); // int32 count ceiling territory
  });

  it("never returns more than 4 characters for any plausible count", () => {
    for (const n of [1, 999, 1000, 9949, 9960, 99_999, 999_499, 1_000_000, 2_147_483_647]) {
      expect(compactCount(n).length).toBeLessThanOrEqual(4);
    }
  });

  it("tolerates garbage without throwing", () => {
    expect(compactCount(NaN)).toBe("0");
    expect(compactCount(-3)).toBe("-3");
  });
});
