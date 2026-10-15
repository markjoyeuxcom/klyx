import { Call } from "@wailsio/runtime";
import { NAV_GROUPS, destState, navigate, type Destination, type NavGroup } from "../nav/registry";
import { useFleet } from "../store/fleet";
import { setThemeChoice } from "../theme/ThemeProvider";

// The native WKWebView smoke's runner (The Line slice 12b, note §3): loaded
// only by the smoke build's handshake (boot.ts). It walks every registry
// destination through the app's own navigation, asserts an expected value at
// every step - never just records one - and reports through the smoke-only
// SmokeService, by name. A step that times out or cannot measure fails;
// the run passes only when every step passed.

const SVC = "main.SmokeService";

export type Step = { name: string; ok: boolean; why?: string; incomplete?: boolean; facts?: Record<string, unknown> };
export type SmokeResult = { result: "pass" | "fail" | "incomplete"; steps: Step[] };
export type SmokeSettings = { theme: string; width: number; height: number; cluster: string };

// The spec's token values per theme (parent §2.1).
export const TOKENS: Record<string, Record<string, string>> = {
  night: { "--line-ground": "#0E1316", "--line-route": "#79C2E4", "--line-danger": "#F0565B" },
  day: { "--line-ground": "#F1F2EE", "--line-route": "#1A6190", "--line-danger": "#B42A30" },
};

// Every face theme/fonts.css declares.
export const FACES: [string, number][] = [["Barlow", 400], ["Barlow", 500], ["Barlow", 600], ["Barlow Condensed", 500], ["Barlow Condensed", 600], ["DM Mono", 400], ["DM Mono", 500]];

export class Unmeasurable extends Error {}

// within polls until f gives a value, or fails the step after ms.
export async function within<T>(ms: number, what: string, f: () => T | null | undefined | false): Promise<T> {
  const end = Date.now() + ms;
  for (;;) {
    const v = f();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out after ${ms}ms waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 100));
  }
}

// sized: an element with a measurable box. A hidden window that lays nothing
// out is unmeasurable - never a pass.
export function sized(el: Element | null, what: string): DOMRect {
  if (!el) throw new Error(`no ${what}`);
  const r = el.getBoundingClientRect();
  if (!(r.width > 0 && r.height > 0)) throw new Unmeasurable(`${what} has no measurable size (${r.width}×${r.height})`);
  return r;
}

export function checkTokens(theme: string): void {
  const want = TOKENS[theme];
  if (!want) throw new Error(`no expected tokens for theme ${theme}`);
  const cs = getComputedStyle(document.documentElement);
  for (const [k, v] of Object.entries(want)) {
    const got = cs.getPropertyValue(k).trim();
    if (got.toUpperCase() !== v.toUpperCase()) throw new Error(`${k} is ${got || "unset"}, expected ${v}`);
  }
}

export function checkApis(): void {
  if (typeof ResizeObserver !== "function") throw new Error("no ResizeObserver");
  if (typeof structuredClone !== "function") throw new Error("no structuredClone");
  if (typeof CSS === "undefined" || !CSS.supports("container-type: inline-size")) throw new Error("no container queries");
}

// checkFonts loads every declared face and requires it loaded: the computed
// family alone would name a face whose file never arrived.
export async function checkFonts(fonts: FontFaceSet = document.fonts): Promise<Record<string, string>> {
  const seen: Record<string, string> = {};
  for (const [family, weight] of FACES) {
    const spec = `${weight} 16px "${family}"`;
    await fonts.load(spec);
    const face = [...fonts].find((f) => f.family.replace(/["']/g, "") === family && String(f.weight) === String(weight));
    seen[`${family} ${weight}`] = face?.status ?? "absent";
    if (!face || face.status !== "loaded" || !fonts.check(spec)) throw new Error(`${family} ${weight} did not load (${face?.status ?? "no face"})`);
  }
  return seen;
}

// capture errors raised while a step runs
const errors: string[] = [];
function listenErrors(): void {
  window.addEventListener("error", (e) => errors.push(String(e.message)));
  window.addEventListener("unhandledrejection", (e) => errors.push(String((e as PromiseRejectionEvent).reason)));
  const original = console.error;
  console.error = (...a: unknown[]) => { errors.push(a.map(String).join(" ").slice(0, 160)); original(...a); };
}

async function step(steps: Step[], name: string, run: () => Promise<Record<string, unknown> | void>): Promise<void> {
  errors.length = 0;
  try {
    const facts = (await run()) ?? {};
    if (document.documentElement.scrollWidth > document.documentElement.clientWidth) throw new Error("horizontal overflow");
    if (errors.length) throw new Error(`console error: ${errors[0]}`);
    steps.push({ name, ok: true, facts });
  } catch (e) {
    steps.push({ name, ok: false, why: e instanceof Error ? e.message : String(e), incomplete: e instanceof Unmeasurable });
  }
}

// destinations: every registry destination, in order - so a new one is
// never silently skipped.
export function destinations(): { g: NavGroup; d: Destination }[] {
  return NAV_GROUPS.flatMap((g) => g.destinations.map((d) => ({ g, d })));
}

export function verdict(steps: Step[]): SmokeResult["result"] {
  if (steps.some((s) => !s.ok && s.incomplete)) return "incomplete";
  if (steps.length === 0 || steps.some((s) => !s.ok)) return "fail";
  return "pass";
}

const header = () => document.querySelector(".lens-header");
const crumb = () => document.querySelector(".locbar")?.textContent ?? "";

export async function walk(settings: SmokeSettings): Promise<Step[]> {
  const steps: Step[] = [];
  const cluster = settings.cluster;
  await step(steps, "theme and tokens", async () => { setThemeChoice(settings.theme as never); await new Promise((r) => setTimeout(r, 300)); checkTokens(settings.theme); });
  await step(steps, "web platform", async () => { checkApis(); });
  await step(steps, "fonts loaded", async () => ({ faces: await checkFonts() }));
  await step(steps, "the lab cluster connects", async () => {
    await within(60_000, `${cluster} to connect`, () => useFleet.getState().clusters.find((c) => c.name === cluster && (c.state === "Synced" || c.state === "Degraded")));
  });
  await step(steps, "fleet board", async () => {
    useFleet.getState().openFleet();
    const v = await within(8000, "the line verdict", () => document.querySelector(".line-verdict"));
    sized(v, "the line verdict");
  });
  for (const { g, d } of destinations()) {
    const c = useFleet.getState().clusters.find((x) => x.name === cluster);
    const state = destState(c, g, d);
    await step(steps, `${g.label} / ${d.label}`, async () => {
      if (state === "absent") return { absent: true }; // named, never skipped silently
      navigate(cluster, d);
      // a task names itself in the location bar; a raw kind's list names the kind
      const t = d.target;
      await within(8000, `${d.label}'s page`, () => header() && (t.kind === "resource" ? header()?.textContent?.includes(t.ref.kind) : crumb().includes(d.label)));
      sized(document.querySelector(".lens-body, .lens-canvas, [data-testid='overview']"), `${d.label}'s body`);
      return { state };
    });
  }
  await step(steps, "inspector layout at this width", async () => {
    const pods = destinations().find((x) => x.d.id === "pods");
    if (!pods) throw new Error("no Pods destination");
    navigate(cluster, pods.d);
    // the Pods page first - a row still in the DOM from the page before is
    // being unmounted - then a row with a measurable box
    await within(8000, "the Pods page", () => header() && crumb().includes(pods.d.label));
    let row: HTMLElement | undefined;
    try {
      row = await within(8000, "a measurable pod row", () => [...document.querySelectorAll<HTMLElement>(".lens-row")].find((r) => r.getBoundingClientRect().height > 0));
    } catch {
      throw new Unmeasurable("no pod row has a measurable size");
    }
    row.click();
    const panel = await within(5000, "the inspector", () => document.querySelector("[data-testid='lens-inspector']"));
    sized(panel, "the inspector");
    const drawer = !!document.querySelector(".lens-drawer");
    const narrow = settings.width <= 1100;
    if (drawer !== narrow) throw new Error(`the inspector is ${drawer ? "a drawer" : "split"} at ${settings.width}px`);
    if (drawer && !document.querySelector(".lens-drawer")?.contains(document.activeElement)) throw new Error("focus did not move into the drawer");
    return { drawer };
  });
  return steps;
}

export async function runSmoke(): Promise<void> {
  listenErrors();
  await Call.ByName(`${SVC}.Started`);
  const settings = (await Call.ByName(`${SVC}.Settings`)) as SmokeSettings;
  let steps: Step[] = [];
  try {
    steps = await walk(settings);
  } catch (e) {
    steps.push({ name: "the walk", ok: false, why: e instanceof Error ? e.message : String(e) });
  }
  const result: SmokeResult = { result: verdict(steps), steps };
  await Call.ByName(`${SVC}.Report`, JSON.stringify(result));
}
