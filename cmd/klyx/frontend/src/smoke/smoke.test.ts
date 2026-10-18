import { describe, it, expect, vi, beforeEach } from "vitest";

// The Line slice 12b: the native smoke's handshake and runner. jsdom lays
// nothing out, which is exactly the hidden-window failure the runner must
// call incomplete, never pass.
const calls = vi.hoisted(() => ({ list: [] as { name: string; args: unknown[] }[] }));
// the Describe step (slice 14) runs kubectl through the exec bridge
vi.mock("../bridge/exec", () => ({ describeResource: vi.fn() }));
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
import { FACES, TITLES, Unmeasurable, checkFonts, checkTokens, destinations, gatewayView, page, runSmoke, sized, verdict, walk, within } from "./run";
import { NAV_GROUPS } from "../nav/registry";
import { useFleet } from "../store/fleet";

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

  it("the step list is every registry destination, so a new one is never skipped, and every task has its expected title", () => {
    expect(destinations().length).toBe(NAV_GROUPS.reduce((n, g) => n + g.destinations.length, 0));
    for (const { d } of destinations()) if (d.type === "task") expect(TITLES[d.id], d.id).toBeTruthy();
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

// The slice 12 review: a page that keeps a wrong header, or never leaves
// loading, never passes as its destination.
const advance = async <T,>(p: Promise<T>) => {
  let done = false;
  const out = p.finally(() => { done = true; });
  out.catch(() => {}); // the caller's expect handles it; never unhandled meanwhile
  while (!done) await vi.advanceTimersByTimeAsync(5_000);
  return out;
};
const pods = () => destinations().find((x) => x.d.id === "pods")!.d;

describe("a destination's page", () => {
  beforeEach(() => vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ width: 800, height: 40, x: 0, y: 0, top: 0, left: 0, bottom: 40, right: 800, toJSON: () => ({}) } as DOMRect));

  it("is its own title, settled, with a measurable body", async () => {
    document.body.innerHTML = '<div class="lens-header"><h1>Pods</h1></div><div class="lens-body"><div class="lens-row">a</div></div>';
    await expect(page(pods(), "lab")).resolves.toEqual({ title: "Pods", rows: 1 });
  });

  it("a lens with parts shows the destination's part, pressed and on screen (slice 13b.1)", async () => {
    const sources = destinations().find((x) => x.d.id === "flux-sources")!.d;
    document.body.innerHTML = '<div class="lens" data-part="flux-sources"><div class="lens-header"><h1>Flux CD</h1></div><div data-testid="lens-parts"><button data-part="flux" aria-pressed="false"></button><button data-part="flux-sources" aria-pressed="true"></button></div><div class="lens-body"></div></div>';
    await expect(page(sources, "lab")).resolves.toMatchObject({ title: "Flux CD", part: { shown: "flux-sources", pressed: "flux-sources" } });
  });

  it("a part switch that leaves the wrong part on screen fails (review 1's negative case)", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const sources = destinations().find((x) => x.d.id === "flux-sources")!.d;
      // the switch says Sources, the content is still Reconciliation
      document.body.innerHTML = '<div class="lens" data-part="flux"><div class="lens-header"><h1>Flux CD</h1></div><div data-testid="lens-parts"><button data-part="flux" aria-pressed="false"></button><button data-part="flux-sources" aria-pressed="true"></button></div><div class="lens-body"></div></div>';
      await expect(advance(page(sources, "lab"))).rejects.toThrow("waiting for Sources's part");
      // the content is Sources, the switch still says Reconciliation
      document.body.innerHTML = '<div class="lens" data-part="flux-sources"><div class="lens-header"><h1>Flux CD</h1></div><div data-testid="lens-parts"><button data-part="flux" aria-pressed="true"></button><button data-part="flux-sources" aria-pressed="false"></button></div><div class="lens-body"></div></div>';
      await expect(advance(page(sources, "lab"))).rejects.toThrow("waiting for Sources's part");
    } finally {
      vi.useRealTimers();
    }
  });

  it("the Gateway lens's view is its pressed switch and its own table, never the shared title (13b review 1)", () => {
    const at = (pressed: string, head: string) => {
      document.body.innerHTML = `<div class="lens-header"><h1>Gateways and routes</h1></div><div data-testid="gateway-views"><button data-view="gateways" aria-pressed="${pressed === "gateways"}"></button><button data-view="routes" aria-pressed="${pressed === "routes"}"></button></div><div class="lens-table-head"><span data-col="mark"></span><span data-col="name">${head}</span><span data-col="status">Status</span></div>`;
      return gatewayView();
    };
    expect(at("routes", "Route")).toBe("routes");
    expect(at("gateways", "Gateway")).toBe("gateways");
    // the switch says Routes over the Gateways table, and the reverse
    expect(at("routes", "Gateway")).toBeNull();
    expect(at("gateways", "Route")).toBeNull();
    document.body.innerHTML = '<div class="lens-header"><h1>Gateways and routes</h1></div>';
    expect(gatewayView()).toBeNull();
  });

  it("a kind shortcut shows the page of the task that owns its section", async () => {
    document.body.innerHTML = '<div class="lens-header"><h1>Workloads</h1></div><div class="lens-body"></div>';
    const deployments = destinations().find((x) => x.d.id === "workloads:Deployment")!.d;
    await expect(page(deployments, "lab")).resolves.toMatchObject({ title: "Workloads" });
  });

  it("a wrong header, or a body that never leaves loading, fails", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      document.body.innerHTML = '<div class="lens-header"><h1>WRONG SCREEN</h1></div><div class="lens-body">Loading forever</div>';
      await expect(advance(page(pods(), "lab"))).rejects.toThrow("waiting for Pods's page");
      document.body.innerHTML = '<div class="lens-header"><h1>Pods</h1></div><div class="lens-body"><div class="lens-body-state" data-state="loading"></div></div>';
      await expect(advance(page(pods(), "lab"))).rejects.toThrow("waiting for Pods to settle");
    } finally {
      vi.useRealTimers();
    }
  });

  it("a walk over a wrong, permanently loading screen never passes (the review's probe)", async () => {
    vi.stubGlobal("ResizeObserver", class {});
    vi.stubGlobal("CSS", { supports: () => true });
    const faces = FACES.map(([family, weight]) => ({ family, weight: String(weight), status: "loaded" }));
    Object.defineProperty(document, "fonts", { configurable: true, value: { load: async () => faces, check: () => true, [Symbol.iterator]: () => faces[Symbol.iterator]() } });
    for (const [k, v] of Object.entries({ "--line-ground": "#0E1316", "--line-route": "#79C2E4", "--line-danger": "#F0565B" })) document.documentElement.style.setProperty(k, v);
    document.body.innerHTML = '<div class="locbar"></div><div class="line-verdict">fake</div><div class="lens-header">WRONG SCREEN</div><div class="lens-body">Loading forever</div><div class="lens-row">old row</div><div data-testid="lens-inspector">old inspector</div>';
    useFleet.setState({ clusters: [{ name: "lab", state: "Synced" } as never], openFleet: () => {} } as never);
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const steps = await advance(walk({ theme: "night", width: 1440, height: 900, cluster: "lab" }));
      expect(steps.find((x) => x.name.endsWith(" / Pods"))?.ok).toBe(false);
      expect(verdict(steps)).not.toBe("pass");
    } finally {
      vi.useRealTimers();
      vi.unstubAllGlobals();
    }
  }, 30_000);
});
