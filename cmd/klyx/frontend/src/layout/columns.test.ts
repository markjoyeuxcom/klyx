import { describe, expect, it } from "vitest";
import { fitColumns, type ColumnDef } from "./columns";

const cols: ColumnDef[] = [
  { id: "lamp", width: 16, priority: 0 },
  { id: "name", width: 240, priority: 0, flex: true },
  { id: "status", width: 140, priority: 1 },
  { id: "usage", width: 120, priority: 2 },
  { id: "age", width: 44, priority: 5 },
];

describe("fitColumns", () => {
  it("keeps everything when it fits; the floor sum is exact", () => {
    // 16+240+140+120+44 = 560, + 4 gaps x12 = 48, + 16 padding = 624
    const r = fitColumns(cols, 624);
    expect([...r.visible].sort()).toEqual(["age", "lamp", "name", "status", "usage"]);
    expect(r.minWidth).toBe(624);
  });
  it("drops the lowest-priority column first, one px below the sum", () => {
    const r = fitColumns(cols, 623);
    expect(r.visible.has("age")).toBe(false);
    expect(r.visible.has("usage")).toBe(true);
  });
  it("never drops the identity column; overflow becomes the table's minWidth", () => {
    const r = fitColumns(cols, 100);
    expect(r.visible.has("name")).toBe(true);
    expect(r.template).toContain("minmax(240px, 1fr)");
    expect(r.minWidth).toBeGreaterThan(100);
  });
});
