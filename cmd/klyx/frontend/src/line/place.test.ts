import { describe, expect, it } from "vitest";
import { placeName, trackName } from "./place";

describe("placeName", () => {
  it("reads Azure region ids as their display names", () => {
    expect(placeName("westeurope")).toBe("West Europe");
    expect(placeName("northeurope")).toBe("North Europe");
    expect(placeName("uksouth")).toBe("UK South");
    expect(placeName("eastus2")).toBe("East US 2");
    expect(placeName("southeastasia")).toBe("Southeast Asia");
    expect(placeName("germanywestcentral")).toBe("Germany West Central");
    expect(placeName("southafricanorth")).toBe("South Africa North");
    expect(placeName("newzealandnorth")).toBe("New Zealand North");
  });

  it("shows anything else exactly as given", () => {
    for (const id of ["eu-west-1", "europe-west4", "homelab", "us", "Europe", "westeuropa", "", "lab 2"]) expect(placeName(id)).toBe(id.trim());
  });

  it("names only region tracks as places", () => {
    expect(trackName({ kind: "region", label: "westeurope" })).toBe("West Europe");
    expect(trackName({ kind: "group", label: "group: westeurope" })).toBe("group: westeurope");
    expect(trackName({ kind: "region", label: "no region" })).toBe("no region");
  });
});
