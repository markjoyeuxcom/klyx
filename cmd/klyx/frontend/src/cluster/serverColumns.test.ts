import { describe, it, expect } from "vitest";
import { columnTitle, fleetColumns, rowMark, splitColumns } from "./serverColumns";
import { columnsSignature, type InstanceColumnDTO } from "../store/fleet";

const col = (o: Partial<InstanceColumnDTO>): InstanceColumnDTO => ({ name: "C", type: "string", format: "", description: "", jsonPath: "", priority: 0, numeric: false, ...o });
const READY = col({ name: "Ready", jsonPath: `.status.conditions[?(@.type=="Ready")].status`, conditionType: "Ready",
  description: `Custom resource definition column (in JSONPath format): .status.conditions[?(@.type=="Ready")].status` });

describe("server columns (slice 15a)", () => {
  it("splits priority 0 into the list and finds the proven condition column", () => {
    const cols = [col({ name: "URL" }), READY, col({ name: "Wide", priority: 1 })];
    const { list, mark } = splitColumns(cols);
    expect(list.map((c) => c.col.name)).toEqual(["URL", "Ready"]);
    expect(mark?.idx).toBe(1);
  });

  it("marks a row only from a proven condition column, by its polarity (design review 1)", () => {
    const { mark } = splitColumns([READY]);
    expect(rowMark({ namespace: "a", name: "b", created: "", cells: ["False"] }, mark)).toEqual({ aspect: "fault", label: "Ready False (from .status.conditions[Ready])" });
    expect(rowMark({ namespace: "a", name: "b", created: "", cells: ["True"] }, mark)).toBeNull();
    expect(rowMark({ namespace: "a", name: "b", created: "", cells: ["Unknown"] }, mark)?.aspect).toBe("checking");
    // a column NAMED Ready that reads .spec.mode proves nothing
    const named = splitColumns([col({ name: "Ready", jsonPath: ".spec.mode" })]);
    expect(named.mark).toBeNull();
    // a column named Ready that reads the Stalled condition has Stalled's polarity
    const stalled = splitColumns([col({ name: "Ready", conditionType: "Stalled" })]);
    expect(rowMark({ namespace: "a", name: "b", created: "", cells: ["True"] }, stalled.mark)?.aspect).toBe("fault");
    expect(rowMark({ namespace: "a", name: "b", created: "", cells: ["False"] }, stalled.mark)).toBeNull();
  });

  it("takes the mark from the first condition column of known polarity - a neutral one never hides it (implementation review 1)", () => {
    const vendor = col({ name: "VendorState", conditionType: "VendorState" });
    const { mark } = splitColumns([vendor, READY]);
    expect(mark?.idx).toBe(1);
    expect(rowMark({ namespace: "a", name: "b", created: "", cells: ["True", "False"] }, mark)?.aspect).toBe("fault");
    expect(splitColumns([vendor]).mark).toBeNull();
  });

  it("titles a generated column by its JSONPath, an author's by its words", () => {
    expect(columnTitle(READY)).toBe(`JSONPath .status.conditions[?(@.type=="Ready")].status`);
    expect(columnTitle(col({ description: "How fast it goes." }))).toBe("How fast it goes.");
  });

  it("draws fleet columns only when every answer agrees (design review 1)", () => {
    const a = [col({ name: "URL" }), READY];
    expect(fleetColumns([a, [...a]]).columns).toEqual(a);
    expect(fleetColumns([a, [READY, col({ name: "URL" })]])).toEqual({ columns: null, mismatch: true });
    expect(fleetColumns([a, null])).toEqual({ columns: null, mismatch: true });
    expect(fleetColumns([null, null])).toEqual({ columns: null, mismatch: false });
    expect(columnsSignature(null)).toBe("none");
  });
});
