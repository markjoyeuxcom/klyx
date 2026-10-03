import { describe, it, expect } from "vitest";
import { newStreamId } from "./streams";

describe("newStreamId", () => {
  it("mints unique ids within the backend's id alphabet", () => {
    const a = newStreamId("logs");
    const b = newStreamId("logs");
    expect(a).not.toBe(b);
    for (const id of [a, b, newStreamId("drain")]) {
      expect(id).toMatch(/^[A-Za-z0-9._:-]{1,128}$/);
    }
  });
});
