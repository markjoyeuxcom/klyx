import { describe, it, expect } from "vitest";
import { clusterCodes } from "./codes";

// The retired spine's spineCodes tests, carried over unchanged (slice 8a).
describe("clusterCodes", () => {
  it("uses segment initials when distinct", () => {
    expect(clusterCodes(["homelab-blue", "homelab-nelli", "prd-weu", "prd-neu"])).toEqual({
      "homelab-blue": "hb", "homelab-nelli": "hn", "prd-weu": "pw", "prd-neu": "pn",
    });
  });
  it("falls back to last-segment letters on collision", () => {
    expect(clusterCodes(["homelab-blue", "homelab-bee"])).toEqual({ "homelab-blue": "bl", "homelab-bee": "be" });
  });
  it("six names that differ only in middle segments get distinct codes", () => {
    const names = ["dev-weu", "dev-neu", "stg-weu", "stg-neu", "prd-weu", "prd-neu"].map((x) => `aks-${x}-platform-01`);
    expect(Object.values(clusterCodes(names))).toEqual(["dw", "dn", "sw", "sn", "pw", "pn"]);
  });
  it("names differing inside a segment get distinguishing prefixes", () => {
    expect(clusterCodes(["aks-dev-weu-platform-01", "aks-dev-wus-platform-01"])).toEqual({
      "aks-dev-weu-platform-01": "we", "aks-dev-wus-platform-01": "wu",
    });
  });
  it("codes are always unique: identical-looking names get a numbered suffix", () => {
    const names = ["a-x", "a.x", "a_x", "ax"];
    const codes = clusterCodes(names);
    expect(new Set(Object.values(codes)).size).toBe(names.length);
    expect(codes["a-x"]).toBe("ax");
    const mixed = clusterCodes(["homelab-blue", "homelab-bee", "bl", "be"]);
    expect(new Set(Object.values(mixed)).size).toBe(4);
  });
  it("single-segment names use their first two letters", () => {
    expect(clusterCodes(["nelli"])).toEqual({ nelli: "ne" });
  });
});
