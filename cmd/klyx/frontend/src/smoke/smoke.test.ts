import { describe, it, expect, vi, beforeEach } from "vitest";

// The Line slice 12b: the native smoke's handshake and runner. jsdom lays
// nothing out, which is exactly the hidden-window failure the runner must
// call incomplete, never pass.
const calls = vi.hoisted(() => ({ list: [] as { name: string; args: unknown[] }[] }));
vi.mock("@wailsio/runtime", () => ({
  Call: {
    ByName: vi.fn(async (name: string, ...args: unknown[]) => {
      calls.list.push({ name, args });
      if (name.endsWith(".Settings")) return { theme: "night", width: 1440, height: 900, cluster: "lab" };
      return null;
    }),
  },
  Events: { On: vi.fn(() => () => {}) },
}));

import { startSmokeWhenAsked, _resetSmokeBootForTest } from "./boot";
import { FACES, Unmeasurable, checkFonts, checkTokens, destinations, runSmoke, sized, verdict, within } from "./run";
import { NAV_GROUPS } from "../nav/registry";

beforeEach(() => {
  _resetSmokeBootForTest();
  delete (window as { __KLYX_SMOKE__?: boolean }).__KLYX_SMOKE__;
  calls.list = [];
});

describe("the handshake", () => {
  it("an ordinary launch never loads the runner", () => {
    const load = vi.fn(async () => ({ runSmoke: async () => {} }));
    startSmokeWhenAsked(load);
    expect(load).not.toHaveBeenCalled();
  });

  it("starts once, whether the flag is up before the shell mounts or the event comes after", () => {
    const load = vi.fn(async () => ({ runSmoke: async () => {} }));
    (window as { __KLYX_SMOKE__?: boolean }).__KLYX_SMOKE__ = true;
    startSmokeWhenAsked(load);
    window.dispatchEvent(new Event("klyx-smoke"));
    expect(load).toHaveBeenCalledTimes(1);

    _resetSmokeBootForTest();
    delete (window as { __KLYX_SMOKE__?: boolean }).__KLYX_SMOKE__;
    const later = vi.fn(async () => ({ runSmoke: async () => {} }));
    startSmokeWhenAsked(later);
    window.dispatchEvent(new Event("klyx-smoke"));
    window.dispatchEvent(new Event("klyx-smoke"));
    expect(later).toHaveBeenCalledTimes(1);
  });
});

describe("the runner's checks", () => {
  it("a bounded wait fails with what it waited for", async () => {
    await expect(within(150, "a header", () => null)).rejects.toThrow("timed out after 150ms waiting for a header");
    await expect(within(150, "x", () => 7)).resolves.toBe(7);
  });

  it("an element with no measurable size is unmeasurable, never a pass", () => {
    const el = document.createElement("div");
    document.body.appendChild(el);
    expect(() => sized(el, "the body")).toThrow(Unmeasurable);
    expect(() => sized(null, "the body")).toThrow("no the body");
  });

  it("the theme's tokens must have the spec's values", () => {
    document.documentElement.style.setProperty("--line-ground", "#0E1316");
    document.documentElement.style.setProperty("--line-route", "#79C2E4");
    document.documentElement.style.setProperty("--line-danger", "#ffffff");
    expect(() => checkTokens("night")).toThrow("--line-danger is #ffffff, expected #F0565B");
    document.documentElement.style.setProperty("--line-danger", "#F0565B");
    expect(() => checkTokens("night")).not.toThrow();
  });

  it("a face that does not load fails the font check", async () => {
    const faces = FACES.map(([family, weight]) => ({ family: `"${family}"`, weight: String(weight), status: "loaded" }));
    const set = (status: (i: number) => string) => {
      const list = faces.map((f, i) => ({ ...f, status: status(i) }));
      return { load: vi.fn(async () => []), check: vi.fn(() => true), [Symbol.iterator]: () => list[Symbol.iterator]() } as unknown as FontFaceSet;
    };
    await expect(checkFonts(set(() => "loaded"))).resolves.toMatchObject({ "DM Mono 500": "loaded" });
    await expect(checkFonts(set((i) => (i === 5 ? "error" : "loaded")))).rejects.toThrow("DM Mono 400 did not load (error)");
  });

  it("the verdict passes only when every step passed; unmeasurable is incomplete", () => {
    expect(verdict([{ name: "a", ok: true }])).toBe("pass");
    expect(verdict([{ name: "a", ok: true }, { name: "b", ok: false, why: "console error" }])).toBe("fail");
    expect(verdict([{ name: "a", ok: false, incomplete: true }, { name: "b", ok: false }])).toBe("incomplete");
    expect(verdict([])).toBe("fail");
  });

  it("the step list is every registry destination, so a new one is never skipped", () => {
    expect(destinations().length).toBe(NAV_GROUPS.reduce((n, g) => n + g.destinations.length, 0));
  });
});

describe("a run with nothing laid out", () => {
  it("starts, then reports a result that is not a pass", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      let finished = false;
      const done = runSmoke().then(() => { finished = true; });
      while (!finished) await vi.advanceTimersByTimeAsync(10_000);
      await done;
    } finally {
      vi.useRealTimers();
    }
    expect(calls.list[0].name).toBe("main.SmokeService.Started");
    const report = calls.list.find((c) => c.name === "main.SmokeService.Report");
    const r = JSON.parse(report!.args[0] as string);
    expect(r.result).not.toBe("pass");
    expect(r.steps.some((s: { ok: boolean }) => !s.ok)).toBe(true);
  }, 20_000);
});
