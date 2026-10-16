import { describe, it, expect } from "vitest";
import { belowOf } from "./useBelow";

describe("belowOf", () => {
  it("names every threshold the width is below; a width of 0 is not laid out, below none", () => {
    const T = [1680, 1600, 1440, 1280, 1180] as const;
    expect(belowOf(2000, T)).toBe("");
    expect(belowOf(1440, T)).toBe("1680 1600");
    expect(belowOf(960, T)).toBe("1680 1600 1440 1280 1180");
    expect(belowOf(0, T)).toBe("");
  });
});
