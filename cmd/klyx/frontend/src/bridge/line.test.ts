import { describe, it, expect, vi, beforeEach } from "vitest";

// The Line slice 4 §5.1: subscribe, then read; keep only newer versions.
const handlers: ((ev: { data: unknown }) => void)[] = [];
let resolveLine: (v: unknown) => void = () => {};
const order: string[] = [];
vi.mock("@wailsio/runtime", () => ({
  Events: { On: (name: string, h: (ev: { data: unknown }) => void) => { order.push(`on:${name}`); handlers.push(h); return () => {}; } },
}));
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  LineService: { Line: () => { order.push("Line()"); return new Promise((r) => { resolveLine = r; }); } },
}));
import { _resetLineBridgeForTest, startLine } from "./line";
import { applyLine, resetLineStore, useLine, type LineDTO } from "../store/line";

const dto = (version: number) => ({ version, stations: [], siding: [], tracks: [], keys: [] } as unknown as LineDTO);
const emit = (d: LineDTO) => handlers.forEach((h) => h({ data: d }));

beforeEach(() => {
  handlers.length = 0;
  order.length = 0;
  resetLineStore();
  _resetLineBridgeForTest();
});

describe("the line bridge", () => {
  it("subscribes to line:dto before it reads the current line; starting twice is one start", () => {
    startLine();
    startLine();
    expect(order).toEqual(["on:line:dto", "Line()"]);
  });

  it("a reply older than an event never replaces it", async () => {
    startLine();
    emit(dto(5));
    resolveLine(dto(3));
    await Promise.resolve(); await Promise.resolve();
    expect(useLine.getState().dto?.version).toBe(5);
  });

  it("a reply newer than every event is kept; duplicate and older events are ignored", async () => {
    startLine();
    emit(dto(2));
    resolveLine(dto(4));
    await Promise.resolve(); await Promise.resolve();
    expect(useLine.getState().dto?.version).toBe(4);
    const held = useLine.getState().dto;
    emit(dto(4));
    expect(useLine.getState().dto).toBe(held);
    emit(dto(3));
    expect(useLine.getState().dto).toBe(held);
    emit(dto(6));
    expect(useLine.getState().dto?.version).toBe(6);
  });

  it("an empty event or reply is ignored", () => {
    expect(applyLine(null)).toBe(false);
    expect(applyLine(undefined)).toBe(false);
    expect(useLine.getState().dto).toBeNull();
  });
});
