import { Call } from "@wailsio/runtime";
import { NAV_GROUPS, destState, navigate, type Destination, type NavGroup } from "../nav/registry";
import { useFleet } from "../store/fleet";
import { setThemeChoice } from "../theme/ThemeProvider";
import { describeResource } from "../bridge/exec";
import { describeArg } from "../chrome/describe/targets";
import { parseDescribe, printDescribe } from "../chrome/describe/parse";

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
const title = () => document.querySelector(".lens-header h1")?.textContent?.trim() ?? "";
const loading = () => !!document.querySelector('.lens-body-state[data-state="loading"]');
const rows = () => document.querySelectorAll(".lens-row").length;
const $ = <T extends Element = HTMLElement>(sel: string) => document.querySelector<T>(sel);

// Each task destination's own page title: a page that keeps an old or wrong
// header never passes (slice 12 review). A lens's title is its index label
// (slice 13b.1), so a tool lens's parts share one title; their page is
// proven by its part instead (partOf).
export const TITLES: Record<string, string | ((cluster: string) => string)> = {
  overview: (cluster) => cluster, nodes: "Nodes", events: "Events", upgrade: "Upgrade check", workloads: "Workloads", pods: "Pods",
  karpenter: "Karpenter", autoscaling: "Autoscaling", connectivity: "Connectivity check", gateway: "Gateways and routes", flows: "Hubble flows", access: "Access check",
  flux: "Flux CD", "flux-sources": "Flux CD", "flux-operator": "Flux CD", "flux-images": "Flux CD", "flux-composition": "Flux CD", "flux-notifications": "Flux CD", argo: "Argo CD", "argo-projects": "Argo CD",
  "argo-appsets": "Argo CD", certs: "cert-manager", eso: "External Secrets", velero: "Velero", admission: "Admission", dra: "Devices (DRA)", loadbalancers: "Load balancers", alerts: "Alerts", helm: "Helm",
  crds: "API browser", builtins: "Resources",
};

// partOf: the part a page shows and the part its switch has pressed (slice
// 13b.1, review 1). Both must be the destination's: a switch that leaves the
// wrong part on screen - or a pressed button over another part's content -
// never passes. Null when the page has no parts.
export function partOf(): { shown: string | null; pressed: string | null } | null {
  const lens = document.querySelector<HTMLElement>(".lens[data-part]");
  const parts = document.querySelector('[data-testid="lens-parts"]');
  if (!lens && !parts) return null;
  return { shown: lens?.dataset.part ?? null, pressed: parts?.querySelector<HTMLElement>('[aria-pressed="true"]')?.dataset.part ?? null };
}

// gatewayView: the Gateway lens's view (slice 13b review 1). Both views are
// titled "Gateways and routes", so the title proves nothing: the view switch's
// pressed button and the table's identity column must name the same view.
// The identity column is chosen by its id - both tables lead with the row
// mark's empty head (13b review 2). Null while they disagree or either is
// missing.
export function gatewayView(): "gateways" | "listenersets" | "routes" | null {
  const pressed = document.querySelector<HTMLElement>('[data-testid="gateway-views"] [aria-pressed="true"]')?.dataset.view;
  const head = document.querySelector('.lens-table-head > [data-col="name"]')?.textContent?.trim();
  // coverage S7b: the Routes table's identity column is "Route" (every kind)
  const shown = head === "Route" ? "routes" : head === "Gateway" ? "gateways" : head === "ListenerSet" ? "listenersets" : null;
  return shown !== null && pressed === shown ? shown : null;
}

// The DOM's own events: no OS key or pointer, as the note's ruling 2 asks.
function key(target: EventTarget, init: KeyboardEventInit): void {
  target.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init }));
}
function typeInto(input: HTMLInputElement, value: string): void {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}
function chooseOption(select: HTMLSelectElement, value: string): void {
  Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(select, value);
  select.dispatchEvent(new Event("change", { bubbles: true }));
}
function byText<T extends HTMLElement = HTMLElement>(sel: string, text: RegExp): T | undefined {
  return [...document.querySelectorAll<T>(sel)].find((el) => text.test(el.textContent?.trim() ?? ""));
}
const settle = (ms = 300) => new Promise((r) => setTimeout(r, ms));
async function closeDrawer(): Promise<void> {
  if ($(".lens-drawer")) { key(window, { key: "Escape" }); await settle(); }
}

// page: a destination's own page, mounted and settled - its expected title,
// out of loading, with a measurable body - and what it showed.
export async function page(d: Destination, cluster: string): Promise<Record<string, unknown>> {
  const t = d.target;
  // a section target - a task, or a kind shortcut such as Deployments into
  // the Workloads lens - shows the page of the task that owns its section
  const owner = t.kind === "section" ? (d.type === "task" ? d : destinations().find((x) => x.d.type === "task" && x.d.target.kind === "section" && x.d.target.section === t.section)?.d) : undefined;
  const want = owner ? TITLES[owner.id] : undefined;
  if (t.kind === "section" && want === undefined) throw new Error(`no expected title for ${d.id}`);
  const expected = typeof want === "function" ? want(cluster) : want;
  await within(8000, `${d.label}'s page`, () => header() && (t.kind === "resource" ? title().includes(t.ref.kind) || header()?.textContent?.includes(t.ref.kind) : title() === expected));
  // a lens with parts: the content and the pressed part are both this task's
  const groupHasParts = owner && NAV_GROUPS.some((g) => g.lens === "group" && g.destinations.filter((x) => x.type === "task").length > 1 && g.destinations.includes(owner));
  if (owner && groupHasParts) await within(8000, `${d.label}'s part`, () => { const p = partOf(); return p && p.shown === owner.id && p.pressed === owner.id; });
  await within(15000, `${d.label} to settle`, () => !loading());
  sized($(".lens-body, .lens-canvas, [data-testid='overview']"), `${d.label}'s body`);
  return { title: title(), rows: rows(), ...(groupHasParts ? { part: partOf() } : {}) };
}

export async function walk(settings: SmokeSettings): Promise<Step[]> {
  const steps: Step[] = [];
  const cluster = settings.cluster;
  const dests = destinations();
  const dest = (id: string) => dests.find((x) => x.d.id === id)?.d;
  const open = async (id: string) => { const d = dest(id); if (!d) throw new Error(`no ${id} destination`); navigate(cluster, d); return page(d, cluster); };

  await step(steps, "theme and tokens", async () => { setThemeChoice(settings.theme as never); await settle(); checkTokens(settings.theme); });
  await step(steps, "web platform", async () => { checkApis(); });
  await step(steps, "fonts loaded", async () => ({ faces: await checkFonts() }));
  await step(steps, "the lab cluster connects", async () => {
    await within(60_000, `${cluster} to connect`, () => useFleet.getState().clusters.find((c) => c.name === cluster && (c.state === "Synced" || c.state === "Degraded")));
  });

  // ---- the fleet board ----
  await step(steps, "fleet board", async () => {
    useFleet.getState().openFleet();
    sized(await within(8000, "the line verdict", () => $(".line-verdict")), "the line verdict");
    return { verdict: $(".line-verdict h2")?.textContent?.trim() };
  });
  for (const f of ["All", "Prd only", "Faults"]) {
    await step(steps, `queue filter ${f}`, async () => {
      const b = await within(4000, `the ${f} filter`, () => byText(".queue-filters button", new RegExp(`^${f}$`)));
      b.click();
      await within(2000, `${f} pressed`, () => b.getAttribute("aria-pressed") === "true");
      return { rows: document.querySelectorAll(".queue-row").length };
    });
  }
  byText(".queue-filters button", /^All$/)?.click();
  await step(steps, "Ledger and Horizon", async () => {
    await within(8000, "the Ledger and the Horizon", () => [...document.querySelectorAll("aside")].find((a) => /Ledger/.test(a.textContent ?? "") && /Horizon/.test(a.textContent ?? "")));
  });
  await step(steps, "a queue row opens its object, pinned", async () => {
    const row = await within(8000, "a fault row", () => [...document.querySelectorAll<HTMLElement>(".queue-row")].find((r) => r.getAttribute("aria-label")?.startsWith("Fault:")));
    const name = /^Fault: \S+ ([^,]+),/.exec(row.getAttribute("aria-label") ?? "")?.[1] ?? "";
    row.click();
    const identity = await within(10_000, "the opened object's inspector", () => $("#inspector-identity"));
    if (!identity.textContent?.includes(name.split("/").pop() ?? "\u0000")) throw new Error(`the inspector shows ${identity.textContent}, not ${name}`);
    if ($("[data-pinned-gone]")) throw new Error(`${name} opened as gone`);
    return { opened: name };
  });

  // ---- the station page and every destination ----
  await step(steps, "station page", async () => {
    const facts = await open("overview");
    const verdict = await within(8000, "the station verdict", () => $("[data-testid='station-verdict']"));
    if (!verdict.querySelector("h2")?.textContent || !$("[data-testid='station-verdict-line']")?.textContent) throw new Error("the verdict misses a line");
    return { ...facts, queue: document.querySelectorAll("[data-testid='station-queue'] .queue-row").length };
  });
  for (const { g, d } of dests) {
    const c = useFleet.getState().clusters.find((x) => x.name === cluster);
    const state = destState(c, g, d);
    await step(steps, `${g.label} / ${d.label}`, async () => {
      if (state === "absent") return { absent: true }; // named, never skipped silently
      navigate(cluster, d);
      return { state, ...(await page(d, cluster)) };
    });
  }

  // ---- the drawings ----
  await step(steps, "Flux dependencies", async () => {
    await open("flux");
    sized(await within(10_000, "the dependency drawing", () => $("[data-testid='dep-graph']")), "the dependency drawing");
  });
  // Coverage S1a: the Flux lens's Operator view, from the lab's operator
  // fixtures (scratchpad lab-s1a): broken-first, the report as the
  // operator's own account with its two times, a suspension said.
  await step(steps, "Flux Operator view", async () => {
    await open("flux-operator");
    await closeDrawer();
    const first = await within(10_000, "operator rows", () => $<HTMLElement>(".lens-row"));
    if (!/apps-waiting|app-tags/.test(first.textContent ?? "")) throw new Error(`not broken-first: the first row is ${first.textContent}`);
    const head = await within(4000, "the report head", () => $("[data-testid='fluxop-report-head']"));
    if (!/as reported by the operator/.test(head.textContent ?? "")) throw new Error(`the report head: ${head.textContent}`);
    (await within(4000, "the FluxInstance row", () => byText<HTMLElement>(".lens-row", /instance.*flux-system\/flux/))).click();
    const rep = await within(8000, "the report block", () => $("[data-testid='fluxop-report']"));
    if (!/reporting succeeded/.test(rep.textContent ?? "") || !/read by Klyx/.test(rep.textContent ?? "")) throw new Error(`the report block: ${rep.textContent}`);
    (await within(4000, "the paused ResourceSet", () => byText<HTMLElement>(".lens-row", /team-b\/paused/))).click();
    const note = await within(8000, "the suspension note", () => $("[data-testid='fluxop-note']"));
    if (!/^Suspended/.test(note.textContent ?? "")) throw new Error(`the suspension note: ${note.textContent}`);
  });
  // Coverage S1b: the Flux lens's Images view, from the lab's image
  // fixtures (scratchpad lab-s1b): broken-first, a policy's own verdict
  // before its repository block, and an automation's push joined to its
  // source and Kustomizations; and a failing current generation (Stage 0)
  // is a fault with the fault aspect.
  await step(steps, "Flux Images view", async () => {
    await open("flux-images");
    await closeDrawer();
    const first = await within(10_000, "image rows", () => $<HTMLElement>(".lens-row"));
    if (!/private|app-prerelease|api/.test(first.textContent ?? "")) throw new Error(`not broken-first: the first row is ${first.textContent}`);
    (await within(4000, "the failing policy", () => byText<HTMLElement>(".lens-row", /policy.*team-a\/app-prerelease/))).click();
    const panel = await within(8000, "the policy inspector", () => $("[data-testid='images-panel']"));
    const text = panel.textContent ?? "";
    if (text.indexOf("Failed") < 0 || text.indexOf("Failed") > text.indexOf("Repository team-a/app")) throw new Error(`the policy's own verdict is not first: ${text.slice(0, 200)}`);
    if (!/last resolved, before the failure/.test(text)) throw new Error("the retained image is not labelled");
    (await within(4000, "the automation", () => byText<HTMLElement>(".lens-row", /automation.*team-a\/team-a-images/))).click();
    const push = await within(8000, "the push block", () => $("[data-testid='images-push']"));
    await within(8000, "the push joined", () => /the source has fetched this commit/.test(push.textContent ?? "") && /applied this commit/.test(push.textContent ?? ""));
    if (!/pushed to auto, from the controller's event/.test(push.textContent ?? "")) throw new Error(`the bound branch: ${push.textContent}`);
  });
  // Coverage S1b Stage 0: a failure of the current generation, while
  // status.observedGeneration lags, is a fault with the fault aspect -
  // never "Syncing…" (scratchpad lab-s1b/stage0.sh).
  await step(steps, "Flux current failure is a fault", async () => {
    await open("flux");
    await closeDrawer();
    const row = await within(10_000, "the failing-current row", () => byText<HTMLElement>(".lens-row", /flux-system \/ failing-current/));
    if (/Syncing/.test(row.textContent ?? "") || !/Failed/.test(row.textContent ?? "")) throw new Error(`not a fault: ${row.textContent}`);
    if (!row.querySelector('[data-aspect="fault"]')) throw new Error("no fault aspect on the row");
  });
  // Coverage S1c: the Composition and Notifications views and a bound
  // artifact's producer line, from the lab's fixtures (scratchpad
  // lab-s1c): broken-first, an artifact's own verdict before its producer,
  // facts without a verdict, and no protected value on screen.
  const noNeedle = (where: string) => {
    if ((document.body.textContent ?? "").includes("S1cLABneedle")) throw new Error(`a protected value is on screen: ${where}`);
  };
  await step(steps, "Flux Composition view", async () => {
    await open("flux-composition");
    await closeDrawer();
    const first = await within(10_000, "composition rows", () => $<HTMLElement>(".lens-row"));
    if (!/broken|empty/.test(first.textContent ?? "")) throw new Error(`not broken-first: the first row is ${first.textContent}`);
    (await within(4000, "the artifact apps-a", () => byText<HTMLElement>(".lens-row", /artifact.*team-a\/apps-a/))).click();
    const panel = await within(8000, "the artifact inspector", () => $("[data-testid='composition-panel']"));
    const producer = await within(8000, "the producer block", () => $("[data-testid='composition-producer']"));
    const text = panel.textContent ?? "";
    if (text.indexOf("Ready") < 0 || text.indexOf("Ready") > text.indexOf("produced by ArtifactGenerator team-a/apps")) throw new Error(`the artifact's own verdict is not first: ${text.slice(0, 200)}`);
    if (!/produced by ArtifactGenerator team-a\/apps/.test(producer.textContent ?? "")) throw new Error(`the producer: ${producer.textContent}`);
    (await within(4000, "the signed artifact", () => byText<HTMLElement>(".lens-row", /artifact.*team-a\/signed/))).click();
    const signed = await within(8000, "the declared producer", () => byText<HTMLElement>("[data-testid='composition-producer']", /Pipeline team-a\/build/));
    if (!/Klyx does not read that kind's health/.test(signed.textContent ?? "")) throw new Error(`a declared producer: ${signed.textContent}`);
    await within(8000, "the origin only", () => /https:\/\/store\.example/.test($("[data-testid='composition-artifact']")?.textContent ?? ""));
    noNeedle("the Composition view");
  });
  await step(steps, "Flux Notifications view", async () => {
    await open("flux-notifications");
    await closeDrawer();
    const first = await within(10_000, "receiver rows", () => $<HTMLElement>(".lens-row"));
    if (!/broken/.test(first.textContent ?? "")) throw new Error(`not broken-first: the first row is ${first.textContent}`);
    (await within(4000, "the github Receiver", () => byText<HTMLElement>(".lens-row", /team-a\/github/))).click();
    const meaning = await within(8000, "what Ready means", () => $("[data-testid='receiver-meaning']"));
    if (!/configuration accepted, token read/.test(meaning.textContent ?? "")) throw new Error(`Ready's meaning: ${meaning.textContent}`);
    noNeedle("a Receiver");
    (await within(4000, "the Providers tab", () => byText<HTMLElement>("[aria-label='Notification kinds'] button", /^Providers/))).click();
    (await within(10_000, "the slack Provider", () => byText<HTMLElement>(".lens-row", /team-a\/slack/))).click();
    const prov = await within(8000, "the Provider inspector", () => $("[data-testid='provider-panel']"));
    if (!/set \(not shown/.test(prov.textContent ?? "") || !/without a verdict/.test(prov.textContent ?? "")) throw new Error(`the Provider: ${prov.textContent}`);
    (await within(4000, "the Alerts tab", () => byText<HTMLElement>("[aria-label='Notification kinds'] button", /^Alerts/))).click();
    (await within(10_000, "the on-call Alert", () => byText<HTMLElement>(".lens-row", /team-a\/on-call/))).click();
    await within(8000, "dispatch evidence", () => /NotificationDispatchFailed/.test($("[data-testid='alert-dispatch']")?.textContent ?? ""));
    noNeedle("Providers and Alerts");
  });
  await step(steps, "Flux bound artifact", async () => {
    await open("flux");
    await closeDrawer();
    (await within(10_000, "the artifact-bound Kustomization", () => byText<HTMLElement>(".lens-row", /team-a \/ artifact-apps/))).click();
    const line = await within(10_000, "the producer line", () => $("[data-testid='source-producer']"));
    if (!/produced by ArtifactGenerator team-a\/apps/.test(line.textContent ?? "")) throw new Error(`the bound source's producer: ${line.textContent}`);
    noNeedle("the Flux inspector");
  });
  // Coverage S2a: the Velero lens from the lab's fixtures (scratchpad
  // lab-s2a): schedules broken first, a missed schedule's rail and its
  // storage location block, a withheld location message, an unscheduled
  // backup, and no protected value on screen.
  const noVeleroNeedle = (where: string) => {
    if ((document.body.textContent ?? "").includes("S2aLABneedle")) throw new Error(`a protected value is on screen: ${where}`);
  };
  await step(steps, "Velero schedules", async () => {
    await open("velero");
    await closeDrawer();
    const first = await within(10_000, "schedule rows", () => $<HTMLElement>(".lens-row"));
    if (!/daily-missed|nightly-failed/.test(first.textContent ?? "")) throw new Error(`not broken-first: the first row is ${first.textContent}`);
    (await within(4000, "the missed schedule", () => byText<HTMLElement>(".lens-row", /velero\/daily-missed/))).click();
    const panel = await within(8000, "the schedule inspector", () => $("[data-testid='velero-schedule-panel']"));
    if (!/Missed/.test(panel.textContent ?? "")) throw new Error(`the missed verdict: ${(panel.textContent ?? "").slice(0, 200)}`);
    const owed = await within(8000, "the owed station", () => $("[data-testid='velero-owed']"));
    if (!/missed/.test(owed.textContent ?? "") || !owed.querySelector('[data-aspect="fault"]')) throw new Error(`the owed station: ${owed.textContent}`);
    const loc = await within(8000, "the location block", () => $("[data-testid='velero-location-block']"));
    if (!/default/.test(loc.textContent ?? "") || !/Available/.test(loc.textContent ?? "")) throw new Error(`the location block: ${loc.textContent}`);
    (await within(4000, "the regular schedule", () => byText<HTMLElement>(".lens-row", /velero\/regular/))).click();
    const hourly = await within(8000, "the regular inspector", () => byText<HTMLElement>("[data-testid='velero-schedule-panel']", /Completed/));
    await within(8000, "the rail", () => $("[data-testid='velero-rail']"));
    if (!/commands withheld/.test(hourly.textContent ?? "")) throw new Error("the hooks are not withheld");
    if (!$("[data-testid='velero-synced']")) throw new Error("the synced backup is not said");
    noVeleroNeedle("the Velero schedules");
  });
  await step(steps, "Velero storage locations", async () => {
    await open("velero");
    await closeDrawer();
    (await within(4000, "the Storage locations tab", () => byText<HTMLElement>("[aria-label='Velero objects'] button", /^Storage locations/))).click();
    (await within(10_000, "the dr location", () => byText<HTMLElement>(".lens-row", /velero\/dr/))).click();
    await within(8000, "the withheld message", () => $("[data-testid='velero-message-withheld']"));
    noVeleroNeedle("a storage location");
    (await within(4000, "the minio location", () => byText<HTMLElement>(".lens-row", /velero\/minio/))).click();
    await within(8000, "its vocabulary", () => /AccessDenied/.test($("[data-testid='velero-location-panel']")?.textContent ?? ""));
    noVeleroNeedle("a storage location");
  });
  await step(steps, "Velero unscheduled backups", async () => {
    await open("velero");
    await closeDrawer();
    (await within(4000, "the Unscheduled backups tab", () => byText<HTMLElement>("[aria-label='Velero objects'] button", /^Unscheduled backups/))).click();
    (await within(10_000, "the pre-upgrade backup", () => byText<HTMLElement>(".lens-row", /velero\/pre-upgrade/))).click();
    const panel = await within(8000, "the backup inspector", () => $("[data-testid='velero-backup-panel']"));
    if (!/the storage location is Unavailable/.test(panel.textContent ?? "")) throw new Error(`the validation class: ${panel.textContent}`);
    noVeleroNeedle("an unscheduled backup");
  });
  // Coverage S2b (scratchpad lab-s2b): the horizon beside the verdict, every
  // retained Backup as a fact, the Restores read, and the fleet view.
  await step(steps, "Velero horizon", async () => {
    await open("velero");
    await closeDrawer();
    // the lens keeps its tab across visits: the previous step left another
    (await within(4000, "the Schedules tab", () => byText<HTMLElement>("[aria-label='Velero objects'] button", /^Schedules/))).click();
    const cell = await within(10_000, "the horizon cell", () => $<HTMLElement>("[data-testid='velero-success-partial-weekly']"));
    if (!/ago/.test(cell.textContent ?? "") || !/partial/.test(cell.textContent ?? "")) throw new Error(`the horizon cell: ${cell.textContent}`);
    const none = await within(4000, "the synced-only cell", () => $<HTMLElement>("[data-testid='velero-success-synced-only']"));
    if (!/none/.test(none.textContent ?? "")) throw new Error(`a synced success counted: ${none.textContent}`);
    (await within(4000, "the partial schedule", () => byText<HTMLElement>(".lens-row", /velero\/partial-weekly/))).click();
    const hz = await within(8000, "the horizon block", () => $("[data-testid='velero-horizon']"));
    if (!/latest proven retained success/.test(hz.textContent ?? "")) throw new Error(`the horizon: ${hz.textContent}`);
    if (!/a newer run partially failed/.test($("[data-testid='velero-schedule-panel']")?.textContent ?? "")) throw new Error("the newer partial run is not said");
    noVeleroNeedle("the horizon");
  });
  await step(steps, "Velero backups", async () => {
    await open("velero");
    await closeDrawer();
    (await within(4000, "the Backups tab", () => byText<HTMLElement>("[aria-label='Velero objects'] button", /^Backups/))).click();
    await within(8000, "the retained note", () => $("[data-testid='velero-retained-note']"));
    const expired = await within(10_000, "the expired backup", () => byText<HTMLElement>(".lens-row", /velero\/manual-expired/));
    if (!/expired, awaiting deletion/.test(expired.textContent ?? "")) throw new Error(`the expired backup: ${expired.textContent}`);
    (await within(4000, "the synced backup", () => byText<HTMLElement>(".lens-row", /velero\/synced-only-2026/))).click();
    const fact = await within(8000, "the fact inspector", () => $("[data-testid='velero-backup-fact']"));
    if (!/looks synced from object storage/i.test(fact.textContent ?? "")) throw new Error(`the attribution: ${fact.textContent}`);
    noVeleroNeedle("the Backups tab");
  });
  await step(steps, "Velero restores", async () => {
    await open("velero");
    await closeDrawer();
    (await within(4000, "the Restores tab", () => byText<HTMLElement>("[aria-label='Velero objects'] button", /^Restores/))).click();
    await within(10_000, "the restores read", () => $("[data-testid='velero-restores-note']"));
    (await within(8000, "the completed restore", () => byText<HTMLElement>(".lens-row", /velero\/restore-ok/))).click();
    const src = await within(8000, "the restore source", () => $("[data-testid='velero-restore-source']"));
    if (!/regular-/.test(src.textContent ?? "") || !/proven run of regular|matched by name to regular/.test(src.textContent ?? "")) throw new Error(`the source join: ${src.textContent}`);
    (await within(4000, "the failed restore", () => byText<HTMLElement>(".lens-row", /velero\/drill/))).click();
    await within(8000, "its class", () => /the restore failed while running/.test($("[data-testid='velero-restore-panel']")?.textContent ?? ""));
    noVeleroNeedle("the Restores tab");
  });
  await step(steps, "Fleet backups", async () => {
    useFleet.getState().openFleet("backups");
    const row = await within(15_000, "a fleet backups row", () => $<HTMLElement>("[data-testid^='fleet-backups-row-']"));
    if (!/Missed|failed|Partially|Deferred|Not processed|Never/i.test(row.textContent ?? "")) throw new Error(`not broken-first: ${row.textContent}`);
    await within(8000, "the partial schedule", () => $("[data-testid$='-partial-weekly']"));
    noVeleroNeedle("the fleet Backups view");
  });
  // Coverage S3a (scratchpad lab-s3a): configurations broken first, a
  // backend chain and the limits, routing-dependent states, a policy's
  // verdict, and no needle on screen (a URL path, a CEL literal in a
  // type-checking warning).
  const noAdmissionNeedle = (where: string) => {
    if ((document.body.textContent ?? "").includes("S3aLABneedle")) throw new Error(`a protected value is on screen: ${where}`);
  };
  await step(steps, "Admission webhooks", async () => {
    await open("admission");
    await closeDrawer();
    (await within(4000, "the Webhooks tab", () => byText<HTMLElement>("[aria-label='Admission objects'] button", /^Webhooks/))).click();
    const first = await within(10_000, "configuration rows", () => $<HTMLElement>(".lens-row"));
    if (!/lab-fail-missing|lab-fail-noready/.test(first.textContent ?? "")) throw new Error(`not broken-first: the first row is ${first.textContent}`);
    (await within(4000, "the missing backend", () => byText<HTMLElement>(".lens-row", /lab-fail-missing/))).click();
    const panel = await within(8000, "the configuration inspector", () => $("[data-testid='admission-config-panel']"));
    if (!/matching requests may fail/.test(panel.textContent ?? "")) throw new Error(`the fault: ${(panel.textContent ?? "").slice(0, 200)}`);
    await within(8000, "the backend chain", () => $("[data-testid='admission-chain']"));
    const limits = await within(8000, "the limits", () => $("[data-testid='admission-limits']"));
    if (!/routing mode/.test(limits.textContent ?? "")) throw new Error("the routing-mode limit is not said");
    (await within(4000, "the headless backend", () => byText<HTMLElement>(".lens-row", /lab-headless/))).click();
    await within(8000, "routing-dependent", () => /Routing-dependent/.test($("[data-testid='admission-config-panel']")?.textContent ?? ""));
    noAdmissionNeedle("the webhooks");
  });
  await step(steps, "Admission policies", async () => {
    await open("admission");
    await closeDrawer();
    (await within(4000, "the Policies tab", () => byText<HTMLElement>("[aria-label='Admission objects'] button", /^Policies/))).click();
    (await within(10_000, "the params policy", () => byText<HTMLElement>(".lens-row", /lab-params/))).click();
    await within(8000, "paramKind not served", () => /paramKind not served/.test($("[data-testid='admission-policy-panel']")?.textContent ?? ""));
    (await within(4000, "the warning policy", () => byText<HTMLElement>(".lens-row", /lab-warning/))).click();
    const warn = await within(8000, "the warnings", () => $("[data-testid='admission-warnings']"));
    if (!/text is not shown/.test(warn.textContent ?? "")) throw new Error(`the warnings: ${warn.textContent}`);
    noAdmissionNeedle("the policies");
  });
  // Coverage S4 (scratchpad lab-s4: a real MetalLB v0.16.1 on kind-klyx,
  // with Cilium pool and svclb fixtures): Services pending first with a
  // lower-bound start, a UID-joined MetalLB event, the chain and the
  // limits; pools broken first with exact counts; MetalLB's states.
  await step(steps, "Load balancer Services", async () => {
    await open("loadbalancers");
    await closeDrawer();
    (await within(4000, "the Services tab", () => byText<HTMLElement>("[aria-label='Load balancer objects'] button", /^Services/))).click();
    const first = await within(10_000, "service rows", () => $<HTMLElement>(".lens-row"));
    if (!/Pending|No address/.test(first.textContent ?? "")) throw new Error(`not pending first: the first row is ${first.textContent}`);
    (await within(4000, "the exhausted request", () => byText<HTMLElement>(".lens-row", /lab-s4\/one-b/))).click();
    const panel = await within(8000, "the Service inspector", () => $("[data-testid='lb-service-panel']"));
    await within(8000, "MetalLB's own event", () => /AllocationFailed/.test($("[data-testid='lb-evidence']")?.textContent ?? ""));
    await within(8000, "the chain", () => $("[data-testid='lb-chain']"));
    if (!/proves allocation/.test($("[data-testid='lb-limits']")?.textContent ?? "")) throw new Error("the allocation limit is not said");
    if (!/Pending at least|Pending for|No address/.test(panel.textContent ?? "")) throw new Error(`the pending time: ${(panel.textContent ?? "").slice(0, 200)}`);
    (await within(4000, "the unknown class", () => byText<HTMLElement>(".lens-row", /lab-s4\/fresh/))).click();
    await within(8000, "no known controller", () => /no controller Klyx knows serves class lab\.klyx\.dev\/none/.test($("[data-testid='lb-service-panel']")?.textContent ?? ""));
    (await within(4000, "the svclb Service", () => byText<HTMLElement>(".lens-row", /lab-s4\/svclb-demo/))).click();
    await within(8000, "the host port conflict", () => /no node has the Service's ports free/.test($("[data-testid='lb-service-panel']")?.textContent ?? ""));
    (await within(4000, "the Cilium Service", () => byText<HTMLElement>(".lens-row", /lab-s4\/cilium-l2/))).click();
    await within(8000, "Cilium's condition", () => /out_of_ips/.test($("[data-testid='lb-service-panel']")?.textContent ?? ""));
  });
  await step(steps, "Load balancer pools", async () => {
    await open("loadbalancers");
    await closeDrawer();
    (await within(4000, "the Pools tab", () => byText<HTMLElement>("[aria-label='Load balancer objects'] button", /^Pools/))).click();
    // the tab's rows replace the Services rows on the next render: wait for a pool's
    const first = await within(10_000, "pool rows", () => { const r = $<HTMLElement>(".lens-row"); return r && !/lab-s4\//.test(r.textContent ?? "") ? r : null; });
    if (!/lab-s4-cilium-conflict/.test(first.textContent ?? "")) throw new Error(`not broken first: the first row is ${first.textContent}`);
    (await within(4000, "the unadvertised pool", () => byText<HTMLElement>(".lens-row", /lab-s4-silent/))).click();
    await within(8000, "no advertisement", () => /No advertisement/.test($("[data-testid='lb-pool-panel']")?.textContent ?? ""));
    if (!/announcement state/.test($("[data-testid='lb-pool-limits']")?.textContent ?? "")) throw new Error("the advertisement limit is not said");
    (await within(4000, "the IPv6 pool", () => byText<HTMLElement>(".lens-row", /lab-s4-cilium-v6/))).click();
    await within(8000, "exact counts", () => /18446744073709551614 of 18446744073709551616/.test($("[data-testid='lb-counts']")?.textContent ?? ""));
    (await within(4000, "the configuration tab", () => byText<HTMLElement>("[aria-label='Load balancer objects'] button", /^MetalLB configuration/))).click();
    // the name column always shows; the writer column can be hidden at 1024
    await within(8000, "MetalLB's states", () => [...document.querySelectorAll(".lens-row")].some((r) => /speaker-/.test(r.textContent ?? "")));
  });
  // Coverage S5 (scratchpad lab-s5): a ServiceAccount's Identity block (a
  // stale-role pod read as Differs, the limits), that pod's own block in the
  // Pods lens, and an ESO store's subject and handling controller; the lab's
  // needle (a non-identity env value, URL user info and query, static keys)
  // never reaches an identity block.
  const noIdentityNeedle = (where: string) => {
    const text = [...document.querySelectorAll("[data-testid^='identity-']")].map((e) => e.textContent ?? "").join("\n");
    if (text.includes("S5LABneedle")) throw new Error(`a protected value is in an identity block: ${where}`);
  };
  // a block read for its subject: a split inspector can still show the
  // previous selection's block until the new selection lands
  const identityShows = async (sel: string, subject: RegExp, what: string) => {
    const end = Date.now() + 20_000;
    for (;;) {
      const b = $<HTMLElement>(sel);
      const t = b?.textContent ?? "";
      if (b && /Read at/.test(t) && subject.test(t)) return b;
      if (Date.now() > end) throw new Error(`timed out after 20000ms waiting for ${what}; the block reads: ${(t || "none").slice(0, 200)}`);
      await new Promise((r) => setTimeout(r, 100));
    }
  };
  await step(steps, "Identity: a ServiceAccount", async () => {
    await open("crds");
    useFleet.getState().openResource({ group: "", version: "v1", plural: "serviceaccounts", kind: "ServiceAccount", scope: "Namespaced" });
    useFleet.getState().openInstance("klyx-identity-lab", "irsa");
    sized(await identityShows("[data-testid='identity-sa']", /role\/lab-current/, "the Identity block"), "the Identity block");
    const stale = $("[data-testid='identity-reading-differs']")?.textContent ?? "";
    if (!/role\/lab-old/.test(stale)) throw new Error(`the stale-role pod is not read as Differs: ${stale.slice(0, 200)}`);
    if (!/Cloud trust/.test($("[data-testid='identity-limits']")?.textContent ?? "")) throw new Error("the limits are not said");
    noIdentityNeedle("the ServiceAccount");
    useFleet.getState().closeResource();
  });
  await step(steps, "Identity: a pod", async () => {
    await open("pods");
    await closeDrawer();
    useFleet.getState().requestNavIntent({ kind: "select-pod", cluster, namespace: "klyx-identity-lab", name: "irsa-stale" });
    await identityShows("[data-testid='identity-pod']", /irsa-stale[\s\S]*Differs: the pod carries arn:aws:iam::111122223333:role\/lab-old/, "irsa-stale's Differs reading");
    noIdentityNeedle("the pod");
  });
  await step(steps, "Identity: an ESO store's subject", async () => {
    await open("eso");
    await closeDrawer();
    (await within(4000, "the Stores view", () => byText<HTMLElement>("[aria-label='External Secrets view'] button", /^Stores/))).click();
    (await within(8000, "the jwt store", () => byText<HTMLElement>(".lens-row", /aws-jwt/))).click();
    await identityShows("[data-testid='identity-store-auth']", /role\/lab-current → arn:aws:iam::111122223333:role\/lab-hop → arn:aws:iam::111122223333:role\/lab-target/, "the jwt store's principal chain");
    (await within(4000, "the controller store", () => byText<HTMLElement>(".lens-row", /aws-controller/))).click();
    await identityShows("[data-testid='identity-store-auth']", /eso-standin as klyx-identity-lab\/eso-controller/, "the handling controller");
    noIdentityNeedle("the stores");
    (await within(4000, "the ExternalSecrets view", () => byText<HTMLElement>("[aria-label='External Secrets view'] button", /^ExternalSecrets/))).click();
  });
  // Coverage S6 (scratchpad lab-s6, a real kubelet): a completed in-place
  // resize's desired, allocated and actual values and its acknowledged
  // generation; a pod with a failed debug container that is still Running;
  // the debug chooser naming sysadmin's privilege, cancelled.
  const podBlock = async (name: string, sel: string, re: RegExp, what: string) => {
    useFleet.getState().requestNavIntent({ kind: "select-pod", cluster, namespace: "klyx-resize-lab", name });
    const end = Date.now() + 20_000;
    for (;;) {
      const t = $(sel)?.textContent ?? "";
      if ($("#inspector-identity")?.textContent?.includes(name) && re.test(t)) return t;
      if (Date.now() > end) throw new Error(`timed out waiting for ${what}; the block reads: ${(t || "none").slice(0, 200)}`);
      await new Promise((r) => setTimeout(r, 100));
    }
  };
  await step(steps, "Pods: a completed in-place resize", async () => {
    await open("pods");
    await closeDrawer();
    await podBlock("resize-done", "[data-testid='pod-resources']", /desired 150m · allocated 150m · actual 150m[\s\S]*acknowledged generation 2/, "resize-done's Resources");
    if (!/assuming kube-scheduler 1\.\d+/.test($("[data-testid='pod-sched']")?.textContent ?? "")) throw new Error("the scheduled estimate does not name its assumption");
  });
  await step(steps, "Pods: a failed debug container is evidence, not the app's failure", async () => {
    await open("pods");
    await closeDrawer();
    await podBlock("debug-target", "[data-testid='pod-debug-containers']", /dbg-failed[\s\S]*exited 3/, "debug-target's debug containers");
    const row = [...document.querySelectorAll<HTMLElement>(".lens-row")].find((r) => /debug-target/.test(r.textContent ?? ""));
    if (row && !/Running/.test(row.textContent ?? "")) throw new Error(`the pod's verdict changed: ${row.textContent}`);
  });
  await step(steps, "Pods: the debug chooser names sysadmin's privilege", async () => {
    const panel = await within(8000, "the pod inspector", () => $("[data-testid='lens-inspector']"));
    (await within(4000, "More", () => [...panel.querySelectorAll<HTMLElement>("button[aria-haspopup='menu']")].find((b) => /^More/.test(b.textContent ?? "")))).click();
    (await within(4000, "Debug shell", () => byText<HTMLElement>("[role='menuitem']", /Debug shell/))).click();
    const target = await within(1500, "a container choice", () => byText<HTMLElement>("[role='menuitem']", /^app$/)).catch(() => null);
    target?.click();
    await within(10_000, "the kubectl gate", () => /kubectl v1\./.test($("[data-testid='debug-kubectl']")?.textContent ?? ""));
    (await within(4000, "sysadmin", () => [...document.querySelectorAll<HTMLInputElement>("input[name='debug-profile']")].find((i) => i.value === "sysadmin"))).click();
    (await within(4000, "Continue", () => $<HTMLButtonElement>("[data-testid='debug-continue']"))).click();
    await within(4000, "the confirmation naming the privilege", () => /profile sysadmin: privileged/.test($("[data-testid='confirm-detail']")?.textContent ?? ""));
    (await within(4000, "Cancel", () => [...document.querySelectorAll<HTMLButtonElement>("[role='dialog'] button")].find((b) => b.textContent === "Cancel"))).click();
    await within(2000, "the dialog closed", () => !$("[data-testid='confirm-detail']"));
  });
  await step(steps, "cert-manager issuance chain", async () => {
    await open("certs");
    sized(await within(10_000, "the issuance chain", () => $("ol[data-rail][aria-label='Issuance chain']")), "the issuance chain");
  });
  await step(steps, "External Secrets chain", async () => {
    await open("eso");
    sized(await within(10_000, "the ExternalSecret chain", () => $("ol[data-rail][aria-label='Store, ExternalSecret and target Secret']")), "the ExternalSecret chain");
  });
  // Coverage S7a (scratchpad lab-s7: Gateway API 1.6 experimental CRDs, a
  // stand-in controller's status): the ListenerSets view broken first, the
  // NotAllowed one first; a Gateway's ListenerSets in its merge order.
  await step(steps, "ListenerSets: the NotAllowed one first", async () => {
    await open("gateway");
    await closeDrawer();
    (await within(4000, "the ListenerSets switch", () => $<HTMLElement>('[data-testid="gateway-views"] [data-view="listenersets"]'))).click();
    await within(8000, "the ListenerSets view", () => gatewayView() === "listenersets" && !loading());
    const first = await within(8000, "the first row", () => $<HTMLElement>("[data-testid='listenerset-row']"));
    if (!/ls-other/.test(first.textContent ?? "") || !/not accepted: NotAllowed/.test(first.textContent ?? "")) throw new Error(`the first row reads: ${first.textContent}`);
    return { first: first.textContent?.slice(0, 80) };
  });
  await step(steps, "a Gateway's ListenerSets in merge order", async () => {
    (await within(4000, "the Gateways switch", () => $<HTMLElement>('[data-testid="gateway-views"] [data-view="gateways"]'))).click();
    await within(8000, "the Gateways view", () => gatewayView() === "gateways" && !loading());
    (await within(8000, "lab-gw", () => $<HTMLElement>('.lens-row .lens-cell-name[title="klyx-gateway-lab/lab-gw"]'))).click();
    const block = await within(8000, "the ListenerSets block", () => {
      const b = $("[data-testid='gateway-listenersets']");
      return b && b.querySelectorAll("[data-testid='gateway-listenerset']").length === 3 ? b : null;
    });
    const order = [...block.querySelectorAll("[data-testid='gateway-listenerset']")].map((e) => (e.textContent ?? "").replace(/\s+/g, " ").slice(0, 40));
    const want = [/^1st · klyx-gateway-lab\/ls-first/, /^2nd · klyx-gateway-other\/ls-other/, /^3rd · klyx-gateway-lab\/ls-conflict/];
    if (!want.every((re, i) => re.test(order[i] ?? ""))) throw new Error(`the merge order reads: ${order.join(" | ")}`);
    if (!/the controller reports 2 attached/.test($("[data-testid='gateway-listenersets-count']")?.textContent ?? "")) throw new Error("the controller's attached count is not beside the ListenerSets found");
    return { order };
  });
  await step(steps, "Gateway data path", async () => {
    await open("gateway");
    await closeDrawer();
    (await within(4000, "the Routes switch", () => $<HTMLElement>('[data-testid="gateway-views"] [data-view="routes"]'))).click();
    // the routes list, pressed and on screen - not the Gateways list still there
    await within(8000, "the Routes view", () => gatewayView() === "routes" && !loading());
    if (!$("[data-testid='route-row-panel']")) (await within(8000, "a route row", () => $<HTMLElement>(".lens-row"))).click();
    const panel = await within(8000, "the route's inspector", () => $("[data-testid='route-row-panel']"));
    (await within(8000, "Open topology", () => [...panel.querySelectorAll<HTMLElement>("button")].find((b) => /^Open topology/.test(b.textContent ?? "")))).click();
    sized(await within(10_000, "the data path", () => $("[data-testid='data-path']")), "the data path");
  });
  // Coverage S7b (scratchpad lab-s7b): the Routes view's kind filter keeps
  // the TCPRoutes; a TCPRoute's topology lane draws a data path whose rule
  // matches every connection - no match step - and reads no HTTP metrics.
  await step(steps, "Routes: the kind filter shows a TCPRoute", async () => {
    await open("gateway");
    await closeDrawer();
    (await within(4000, "the Routes switch", () => $<HTMLElement>('[data-testid="gateway-views"] [data-view="routes"]'))).click();
    await within(8000, "the Routes view", () => gatewayView() === "routes" && !loading());
    const filter = await within(8000, "the kind filter", () => $<HTMLSelectElement>("[data-testid='route-kind-filter']"));
    if (![...filter.options].some((o) => o.value === "TCPRoute")) throw new Error(`no TCPRoute among the kinds: ${[...filter.options].map((o) => o.value).join(", ")}`);
    chooseOption(filter, "TCPRoute");
    const rows = await within(4000, "TCPRoute rows only", () => {
      const r = [...document.querySelectorAll<HTMLElement>(".lens-row")];
      return r.length > 0 && r.every((x) => x.querySelector("[data-col='kind']")?.textContent === "TCPRoute") ? r : null;
    });
    const names = rows.map((r) => r.querySelector(".lens-cell-name")?.getAttribute("title") ?? "");
    if (!names.includes("TCPRoute klyx-gateway-lab/pg")) throw new Error(`the TCPRoutes read: ${names.join(", ")}`);
    return { tcproutes: names };
  });
  await step(steps, "an L4 data path has no match step", async () => {
    const row = await within(8000, "the pg TCPRoute", () => $<HTMLElement>('.lens-row .lens-cell-name[title="TCPRoute klyx-gateway-lab/pg"]'));
    row.click();
    const panel = await within(8000, "the route's inspector", () => $("[data-testid='route-row-panel']"));
    (await within(8000, "Open topology", () => [...panel.querySelectorAll<HTMLElement>("button")].find((b) => /^Open topology/.test(b.textContent ?? "")))).click();
    const path = await within(10_000, "the TCPRoute's data path", () => {
      const p = $("[data-testid='route-panel'] [data-testid='data-path']");
      return p && /TCPRoute/.test(p.textContent ?? "") ? p : null;
    });
    sized(path, "the L4 data path");
    const text = (path.textContent ?? "").replace(/\s+/g, " ");
    if (!/every connection on pg :5432/.test(text)) throw new Error(`the rule reads: ${text.slice(0, 200)}`);
    if (/every request|PathPrefix/.test(text)) throw new Error(`an L4 rule shows a match step: ${text.slice(0, 200)}`);
    if (!/HTTP request metrics do not apply to TCPRoute/.test(text)) throw new Error("the L4 rule claims HTTP request metrics");
    return { rule: text.match(/rule 1[^·]*/)?.[0]?.trim() };
  });

  // ---- Describe (slice 14) ----
  // From the Flux inspector's More: the sheet reads kubectl's report into
  // sections, the Kustomization's conditions into a table, and the live
  // report round-trips through the parser byte for byte.
  await step(steps, "Describe from the Flux inspector", async () => {
    await open("flux");
    const panel = await within(10_000, "the Flux inspector", () => $("[data-testid='lens-inspector']"));
    const sel = useFleet.getState().gitops.expandedKey ?? "";
    const [kind, ns, name] = sel.split("/");
    const arg = describeArg(kind);
    if (!arg || !ns || !name) throw new Error(`no described Flux object (selection ${sel || "none"})`);
    (await within(4000, "More", () => [...panel.querySelectorAll<HTMLElement>("button[aria-haspopup='menu']")].find((b) => /^More/.test(b.textContent ?? "")))).click();
    (await within(4000, "Describe in More", () => byText<HTMLElement>("[role='menuitem']", /^Describe$/))).click();
    const sheet = await within(30_000, "the formatted report", () => $("[data-testid='describe-formatted']"));
    sized(sheet, "the formatted report");
    const heads = [...sheet.querySelectorAll(".describe-section-head .t-section-label")].map((e) => e.textContent ?? "");
    if (!heads.includes("Status")) throw new Error(`no Status section (sections ${heads.join(", ")})`);
    const row = await within(4000, "a conditions table row", () => sheet.querySelector("table[data-kind='conditions'] tbody tr"));
    const command = $(".describe-command")?.textContent ?? "";
    if (!command.includes(`-- ${name}`)) throw new Error(`the command does not name ${name}: ${command}`);
    const r = await describeResource(cluster, arg, ns, name);
    if (!r.ok) throw new Error(`describe failed: ${r.error}`);
    if (printDescribe(parseDescribe(r.output)) !== r.output) throw new Error("the live report does not round-trip");
    key($("[data-testid='describe-sheet']")!, { key: "Escape" });
    await within(4000, "the sheet to close", () => !$("[data-testid='describe-sheet']"));
    return { object: sel, sections: heads.length, conditions: row.parentElement?.children.length ?? 0, bytes: r.output.length };
  });

  // ---- the generic browser (slice 15) ----
  // A CRD's printer columns, evaluated by the API server: the GitRepository
  // list shows URL and Ready with their cells.
  await step(steps, "a CRD's printer columns", async () => {
    await open("crds");
    useFleet.getState().openResource({ group: "source.toolkit.fluxcd.io", version: "v1", plural: "gitrepositories", kind: "GitRepository", scope: "Namespaced" });
    const head = await within(15_000, "the GitRepository list's printer columns", () => {
      const h = $("[data-testid='instance-list-header']");
      const names = h ? [...h.querySelectorAll("span")].map((s) => s.textContent ?? "") : [];
      return names.includes("URL") && names.includes("Ready") ? h : null;
    });
    sized(head, "the list header");
    const row = await within(8000, "a row with a URL cell", () => [...document.querySelectorAll<HTMLElement>(".lens-row")].find((r) => /:\/\//.test(r.textContent ?? "")));
    useFleet.getState().closeResource();
    return { columns: [...head.querySelectorAll("span")].map((s) => s.textContent).filter(Boolean), row: row.textContent?.slice(0, 80) };
  });

  // The inspector expands over the workspace, and Escape restores it - the
  // layout's capture listener runs ahead of the list's own Escape, so the
  // inspector stays open (slice 16).
  // Embedded payloads stay out (coverage S0b): the lab's ResourceSet, a
  // patched Kustomization and a credential GitRepository carry plain
  // markers; none may reach the page or a Describe report, Describe is
  // withheld for the ResourceSet, and kubectl's real output for the
  // GitRepository (its 138-byte annotation cut included) arrives scrubbed.
  await step(steps, "embedded payloads stay out", async () => {
    const markers = ["lab-only-inline-secret-value", "lab-only-not-a-real-secret", "lab-only-patch-marker", "lab-only-substitute-marker", "lab-only-url-token", "lab-only-annot-token"];
    const leaked = (text: string) => markers.filter((m) => text.includes(m));
    await open("crds");
    useFleet.getState().openResource({ group: "fluxcd.controlplane.io", version: "v1", plural: "resourcesets", kind: "ResourceSet", scope: "Namespaced" });
    useFleet.getState().openInstance("team-a", "with-secret");
    await within(15_000, "the ResourceSet's inspector", () => $("#inspector-identity"));
    await within(10_000, "its detail", () => useFleet.getState().instanceDetail.detail?.name === "with-secret");
    const tab = [...document.querySelectorAll<HTMLButtonElement>("[role='tab']")].find((b) => b.textContent === "Manifest");
    tab?.click();
    await within(5000, "the sanitised manifest", () => $("[data-testid='manifest-sanitised']"));
    if ($("textarea[aria-label='resource yaml']")) throw new Error("the editor is offered for a sanitised object");
    const detailJSON = JSON.stringify(useFleet.getState().instanceDetail.detail);
    const inPage = leaked(document.body.innerText + detailJSON);
    useFleet.getState().closeResource();
    const rs = await describeResource(cluster, "resourcesets.fluxcd.controlplane.io", "team-a", "with-secret");
    if (!rs.withheld || rs.output !== "") throw new Error("Describe was not withheld for a ResourceSet");
    const gr = await describeResource(cluster, "gitrepositories.source.toolkit.fluxcd.io", "flux-system", "creds-repo");
    if (!gr.ok || !gr.scrubbed) throw new Error(`the GitRepository's report was not scrubbed: ${gr.error}`);
    const ks = await describeResource(cluster, "kustomizations.kustomize.toolkit.fluxcd.io", "flux-system", "patched");
    if (!ks.withheld) throw new Error("Describe was not withheld for a Kustomization");
    const all = [...inPage, ...leaked(rs.output + rs.error + gr.output + gr.error + ks.output + ks.error)];
    if (all.length) throw new Error(`markers crossed: ${all.join(", ")}`);
    return { withheld: [rs.withheld.slice(0, 40), ks.withheld.slice(0, 40)], scrubbed: gr.scrubbed, linesWithheld: gr.linesWithheld ?? 0 };
  });

  await step(steps, "the inspector expands and restores", async () => {
    await open("crds");
    useFleet.getState().openResource({ group: "source.toolkit.fluxcd.io", version: "v1", plural: "gitrepositories", kind: "GitRepository", scope: "Namespaced" });
    const row = await within(15_000, "a GitRepository row", () => [...document.querySelectorAll<HTMLElement>(".lens-row")].find((r) => /:\/\//.test(r.textContent ?? "")));
    row.click();
    (await within(10_000, "the expand button", () => $<HTMLButtonElement>("[data-testid='inspector-expand']"))).click();
    const lens = await within(2000, "the expanded layout", () => $("[data-testid='lens-layout'][data-expanded]"));
    const main = lens.querySelector<HTMLElement>(".lens-main");
    if (main && getComputedStyle(main).display !== "none") throw new Error("the list is still drawn behind the expanded inspector");
    const wide = sized($("[data-testid='lens-inspector']"), "the expanded inspector").width;
    const whole = sized(lens, "the lens").width;
    if (wide < whole - 2) throw new Error(`the expanded inspector is ${Math.round(wide)} of ${Math.round(whole)}`);
    key(document.body, { key: "Escape" });
    await within(2000, "restored beside the list", () => !$("[data-testid='lens-layout'][data-expanded]"));
    if (!$("#inspector-identity")) throw new Error("Escape closed the inspector instead of restoring it");
    const restored = sized($("[data-testid='lens-inspector']"), "the restored inspector").width;
    useFleet.getState().closeResource();
    return { expanded: Math.round(wide), lens: Math.round(whole), restored: Math.round(restored) };
  });

  // ---- the console ----
  await step(steps, "console: search the fleet", async () => {
    await open("pods");
    await closeDrawer();
    key(window, { key: "k", metaKey: true });
    const input = await within(3000, "the console", () => $<HTMLInputElement>("[data-testid='command-palette-input']"));
    await within(1000, "focus in the console", () => document.activeElement === input);
    typeInto(input, "web");
    await within(8000, "object hits", () => document.querySelector("[data-testid^='console-hit-']"));
    return { hits: document.querySelectorAll("[data-testid^='console-hit-']").length };
  });
  await step(steps, "console: verbs only", async () => {
    const input = $<HTMLInputElement>("[data-testid='command-palette-input']")!;
    key(input, { key: "v", code: "KeyV", altKey: true });
    await settle();
    const groups = [...document.querySelectorAll("#command-palette-list [role='group']")].map((x) => x.getAttribute("aria-label") ?? "");
    key(input, { key: "v", code: "KeyV", altKey: true });
    if (!groups.every((x) => x.startsWith("Run"))) throw new Error(`groups ${groups.join(", ")}`);
    return { groups };
  });
  await step(steps, "console: open beside", async () => {
    const input = $<HTMLInputElement>("[data-testid='command-palette-input']")!;
    await within(4000, "a hit selected", () => $("[data-testid^='console-hit-'][data-selected='true']"));
    key(input, { key: "Enter", metaKey: true });
    await within(4000, "the console aside", () => $("[data-testid='command-palette']")?.style.justifyContent === "flex-start");
    await within(10_000, "the opened object's inspector", () => $("#inspector-identity"));
    await within(1000, "focus kept in the console", () => document.activeElement === input);
    await settle(1500);
    if ($("[data-pinned-gone]")) throw new Error(`the hit opened as ${$("[data-pinned-gone]")?.getAttribute("data-pinned-gone")}`);
  });
  await step(steps, "console: Escape returns to the inspector", async () => {
    key($("[data-testid='command-palette-input']")!, { key: "Escape" });
    await within(2000, "the console closed", () => !$("[data-testid='command-palette']"));
    await within(2000, "focus on the inspector's identity", () => document.activeElement?.id === "inspector-identity");
  });

  // ---- coverage S8a: alerts from the lab's real Alertmanager (lab-s8a) ----
  // the smoke fleet reads klyx-s8-lab/alertmanager:web with match
  // cluster=kind-klyx: each state real, another cluster's alert kept out
  await step(steps, "Alerts: each state, from a real Alertmanager", async () => {
    await open("alerts");
    await closeDrawer();
    await within(20_000, "the lab's alerts", () => $("[data-testid='alert-row-KlyxLabPodCrashLooping']"));
    const text = (name: string) => $(`[data-testid='alert-row-${name}']`)?.textContent ?? "";
    if (!/active/.test(text("KlyxLabPodCrashLooping"))) throw new Error(`the pod alert: ${text("KlyxLabPodCrashLooping")}`);
    await within(90_000, "the silence, the inhibition, the mute and the partial mute", () =>
      /silenced/.test(text("KlyxLabSilenced")) && /inhibited by KlyxLabCritical/.test(text("KlyxLabWarning")) && /muted by time interval always/.test(text("KlyxLabMuted")) &&
      /active · muted for lab-muted; active for lab/.test(text("KlyxLabPartlyMuted")));
    if ($("[data-testid='alert-row-KlyxLabElsewhere']")) throw new Error("another cluster's alert is listed: match was not applied");
    if (!$("[data-testid='alert-row-KlyxLabClusterWide']")) throw new Error("the cluster alert is not listed");
    const source = $("[data-testid='alerts-source']")?.textContent ?? "";
    if (!/klyx-s8-lab\/alertmanager:web/.test(source)) throw new Error(`the source line: ${source}`);
    return { rows: document.querySelectorAll(".lens-row").length, summary: $("[data-testid='alerts-summary']")?.textContent?.trim() };
  });
  await step(steps, "Alerts: the pod's inspector lists its alert", async () => {
    await open("alerts");
    await closeDrawer();
    (await within(10_000, "the pod alert", () => $<HTMLElement>("[data-testid='alert-row-KlyxLabPodCrashLooping']"))).click();
    const panel = await within(8000, "the alert inspector", () => $("[data-testid='alert-panel']"));
    if (!/matched by label namespace\/pod/.test(panel.textContent ?? "")) throw new Error(`the correlation: ${(panel.textContent ?? "").slice(0, 200)}`);
    (await within(4000, "Open Pod", () => byText<HTMLElement>("button", /^Open Pod$/))).click();
    await within(20_000, "the pod inspector's Alerts block", () => $("[data-testid='insp-alert-KlyxLabPodCrashLooping']"));
  });
  await step(steps, "Needs you counts alerts apart", async () => {
    useFleet.getState().openFleet();
    const counts = await within(20_000, "the alerts count", () => byText<HTMLElement>(".queue-counts", /alert/));
    await within(8000, "an alert row", () => [...document.querySelectorAll<HTMLElement>(".queue-row")].find((r) => /alert: KlyxLab/.test(r.getAttribute("aria-label") ?? "")));
    return { counts: counts.textContent };
  });

  // ---- coverage S8b: log history from the lab's real Loki (lab-s8b) ----
  // the smoke fleet reads klyx-s8b-lab/loki:http, tenant lab, match
  // k8s_cluster_name=kind-klyx (the OTLP shape, which carries pod UIDs):
  // history-db-0's second generation only, by its UID
  await step(steps, "Log history: a pod's lines from Loki, by its UID", async () => {
    await open("pods");
    await closeDrawer();
    useFleet.getState().requestNavIntent({ kind: "logs-pod", cluster, namespace: "klyx-s8b-lab", name: "history-db-0" });
    await within(20_000, "the logs dock", () => $("[data-testid='logs-dock']"));
    (await within(8000, "History", () => byText<HTMLElement>("[data-testid='logs-mode'] button", /^History$/))).click();
    await within(30_000, "the UID selector", () => /selected by this pod's UID/.test($("[data-testid='history-strength']")?.textContent ?? ""));
    const lines = [...document.querySelectorAll("[data-testid='history-line']")].map((l) => l.textContent ?? "");
    if (lines.length === 0 || lines.some((l) => /gen1/.test(l)) || !lines.every((l) => /gen2/.test(l))) {
      throw new Error(`the lines are not gen2's alone: ${lines.length} lines, first ${(lines[0] ?? "").slice(0, 120)}`);
    }
    if (!/k8s_pod_uid=/.test($("[data-testid='history-selector']")?.textContent ?? "")) throw new Error("the selector does not show the UID");
    $<HTMLElement>("button[aria-label='close logs dock']")?.click();
    return { lines: lines.length, strength: $("[data-testid='history-strength']")?.textContent?.slice(0, 80) };
  });

  // ---- coverage S9: KEDA and autoscaling ownership (lab-s9, a real KEDA 2.21.0) ----
  // failing's prometheus trigger reaches nothing: KEDA's fallback holds,
  // and the verdict says so, with the trigger's failure count apart from
  // the HPA's replicas
  await step(steps, "KEDA: a ScaledObject on fallback", async () => {
    await open("autoscaling");
    await closeDrawer();
    (await within(20_000, "failing", () => $<HTMLElement>("[data-testid='as-row-failing']"))).click();
    const panel = await within(8000, "the ScaledObject inspector", () => $("[data-testid='keda-so-panel']"));
    await within(8000, "the fallback verdict", () => /Fallback active \(static\)/.test(panel.textContent ?? ""));
    const failures = (await within(8000, "the failure count", () => /(\d+) failures \(Failing\)/.exec($("[data-testid='keda-triggers']")?.textContent ?? "")))[1];
    const hpa = $("[data-testid='keda-hpa']")?.textContent ?? "";
    if (!/^keda-hpa-failing: \d+ → \d+/.test(hpa)) throw new Error(`the HPA line: ${hpa}`);
    return { failures, hpa };
  });
  // the scale verb names what owns the count; cancelled, never applied
  await step(steps, "Scale: the popover names the KEDA ScaledObject", async () => {
    await open("workloads");
    await closeDrawer();
    useFleet.getState().requestNavIntent({ kind: "scale-workload", cluster, workloadKind: "Deployment", namespace: "klyx-s9-lab", name: "active-web" });
    const note = await within(20_000, "the owner line", () => {
      const t = $("[data-testid='scale-owner']")?.textContent ?? "";
      return /KEDA ScaledObject active-web/.test(t) ? t : null;
    });
    if (!byText<HTMLElement>("button", /^scale anyway$/)) throw new Error("the apply button does not read \"scale anyway\"");
    (await within(4000, "cancel", () => $<HTMLElement>("button[aria-label='cancel scale']"))).click();
    await within(2000, "the popover closed", () => !$("[data-testid='scale-owner']"));
    return { owner: note.slice(0, 160) };
  });

  // ---- coverage S10: node provisioning by provider adapter (lab-s10) ----
  // lab-subnets-a's EC2NodeClass fails SubnetsReady: the pool's headline is
  // the class's failure in Klyx's words, with the link and the class named
  await step(steps, "Karpenter: a NodePool whose EC2NodeClass fails", async () => {
    await open("karpenter");
    await closeDrawer();
    (await within(20_000, "the NodePools view", () => byText<HTMLElement>("button.l-chip", /^NodePools/))).click();
    (await within(20_000, "lab-subnets-a", () => [...document.querySelectorAll<HTMLElement>(".lens-row")].find((r) => /lab-subnets-a/.test(r.textContent ?? "")))).click();
    const panel = await within(8000, "the NodePool inspector", () => $("[data-testid='karp-pool-panel']"));
    const head = await within(8000, "the headline in Klyx's words", () => {
      const t = $("[data-testid='karp-pool-head']")?.textContent ?? "";
      return /^SubnetsReady False: SubnetsNotFound: no subnets matched the selector terms$/.test(t) ? t : null;
    });
    const link = $("[data-testid='karp-pool-class']")?.textContent ?? "";
    if (!/EC2NodeClass\/lab-subnets/.test(link) || !/linked/.test(link)) throw new Error(`the class link: ${link}`);
    if (!/NodeClass lab-subnets not ready/.test(panel.textContent ?? "")) throw new Error("the headline label");
    return { head, link: link.slice(0, 160) };
  });
  // the class is inventory: listed with its reading and both its pools, and
  // counted on neither itself nor twice
  await step(steps, "Karpenter: the NodeClasses view", async () => {
    (await within(8000, "the NodeClasses view", () => byText<HTMLElement>("button.l-chip", /^NodeClasses/))).click();
    // the view has switched only once its own columns show
    await within(8000, "the NodeClasses columns", () => /Reading/.test($(".lens-table-head")?.textContent ?? ""));
    const row = await within(20_000, "lab-subnets", () => [...document.querySelectorAll<HTMLElement>(".lens-row")].find((r) => r.querySelector(".lens-cell-name")?.textContent === "lab-subnets"));
    if (!/not ready/.test(row.textContent ?? "")) throw new Error(`lab-subnets reads: ${row.textContent}`);
    row.click();
    const panel = await within(8000, "the NodeClass inspector", () => $("[data-testid='karp-class-panel']"));
    await within(8000, "its users", () => /lab-subnets-a, lab-subnets-b/.test(panel.textContent ?? ""));
    if (!/counted on each NodePool that uses it/.test(panel.textContent ?? "")) throw new Error("the class is not said as configuration");
    return { rows: document.querySelectorAll(".lens-row").length };
  });

  // coverage S11a: a node's extended resources, from lab-s11a - the real
  // e2e sample device plugin (example.com/resource) and the lab's NVIDIA
  // status fixtures with per-resource GPU Feature Discovery labels
  await step(steps, "Nodes: a node's extended resources", async () => {
    await open("nodes");
    await closeDrawer();
    await within(8000, "the GPU column", () => /GPU/.test($(".lens-table-head")?.textContent ?? ""));
    const row = await within(20_000, "the kind node", () => [...document.querySelectorAll<HTMLElement>(".lens-row")].find((r) => r.querySelector(".lens-cell-name")?.textContent === "klyx-control-plane"));
    const cell = row.querySelector("[data-testid='nodes-gpu-cell']")?.textContent ?? "";
    if (cell !== "2 resources") throw new Error(`the GPU cell reads "${cell}"`);
    row.click();
    const block = await within(20_000, "the Extended resources block", () => {
      const b = $("[data-testid='node-extended']");
      return b && /example\.com\/resource · 1 requested/.test(b.textContent ?? "") ? b : null;
    });
    const text = block.textContent ?? "";
    for (const want of ["nvidia.com/gpu · 3 requested · 8 allocatable · 8 capacity", "shared (time-slicing): each GPU is advertised 4 times",
      "nvidia.com/mig-1g.5gb · 1 requested · 7 allocatable", "example.com/resource · 1 requested · 2 allocatable", "MIG strategy: mixed"]) {
      if (!text.includes(want)) throw new Error(`missing "${want}" in ${text.slice(0, 400)}`);
    }
    const totals = $("[data-testid='nodes-extended-totals']")?.textContent ?? "";
    if (!totals.includes("8 nvidia.com/gpu allocatable (3 requested)")) throw new Error(`the totals: ${totals}`);
    return { cell, totals: totals.slice(0, 160) };
  });

  // coverage S11b: DRA, from lab-s11b - the upstream example driver's
  // node-local pool, a template claim in use, a claim no node satisfies,
  // the fixture pools, and an unhealthy device
  await step(steps, "Devices (DRA): claims, pools and the pending ladder", async () => {
    await open("dra");
    await closeDrawer();
    const stuck = await within(20_000, "the unsatisfiable claim first", () => {
      const r = [...document.querySelectorAll<HTMLElement>(".lens-row")][0];
      return r && /klyx-s11b-lab \/ lab-too-many-gpu-/.test(r.textContent ?? "") && /cannot allocate it/.test(r.textContent ?? "") ? r : null;
    });
    stuck.click();
    const panel = await within(8000, "the claim inspector", () => $("[data-testid='dra-claim-panel']"));
    const text = panel.textContent ?? "";
    // lab-s11b's invalid fixture pool turns the scheduler's reason into
    // "invalid resource pools were encountered" (1.35+); either is quoted
    for (const want of ['The scheduler: "', "the scheduler could not allocate the pod's claims"]) {
      if (!text.includes(want)) throw new Error(`missing "${want}" in ${text.slice(0, 400)}`);
    }
    if (!/invalid resource pools were encountered|cannot allocate all claims/.test(text)) throw new Error(`no DRA reason quoted in ${text.slice(0, 400)}`);
    await closeDrawer();
    (await within(8000, "the Pools tab", () => byText<HTMLButtonElement>("[aria-label='DRA objects'] button", /^Pools/))).click();
    const pools = await within(20_000, "the pools", () => {
      const rows = [...document.querySelectorAll<HTMLElement>(".lens-row")].map((r) => r.textContent ?? "");
      return rows.some((t) => t.includes("fixture.klyx.dev / lab-invalid") && t.includes("Invalid")) && rows.some((t) => t.includes("gpu.example.com / klyx-control-plane")) ? rows : null;
    });
    const incomplete = pools.find((t) => t.includes("lab-incomplete")) ?? "";
    if (!/Being updated|Incomplete/.test(incomplete)) throw new Error(`the incomplete fixture pool reads "${incomplete}"`);
    return { claim: stuck.textContent?.slice(0, 120), pools: pools.length };
  });

  await step(steps, "Devices (DRA): the node's pools and the pod's claims", async () => {
    await open("nodes");
    await closeDrawer();
    const row = await within(20_000, "the kind node", () => [...document.querySelectorAll<HTMLElement>(".lens-row")].find((r) => r.querySelector(".lens-cell-name")?.textContent === "klyx-control-plane"));
    row.click();
    const block = await within(20_000, "the node's DRA block", () => {
      const b = $("[data-testid='node-dra']");
      return b && /gpu\.example\.com \/ klyx-control-plane · 8 devices/.test(b.textContent ?? "") ? b : null;
    });
    await closeDrawer();
    await open("pods");
    await closeDrawer();
    const search = await within(8000, "the pods search", () => $<HTMLInputElement>("input[placeholder='Search pods']"));
    typeInto(search, "lab-template");
    const pod = await within(20_000, "lab-template", () => [...document.querySelectorAll<HTMLElement>(".lens-row")].find((r) => /lab-template$/.test(r.querySelector(".lens-cell-name")?.textContent ?? "")));
    pod.click();
    // relabs marks lab-template's device unhealthy (lab-s11b/unhealthy.sh):
    // the kubelet's report, named by its ID
    const claims = await within(30_000, "the pod's claims, its device unhealthy", () => {
      const b = $("[data-testid='pod-claims']");
      return b && /from a template \(the pod's "gpu"\) · Allocated/.test(b.textContent ?? "") && /1 device unhealthy \(reported by the kubelet\): \S+/.test(b.textContent ?? "") ? b : null;
    });
    typeInto(search, "");
    return { node: block.textContent?.slice(0, 120), pod: claims.textContent?.slice(0, 160) };
  });

  // ---- settings and the chat drawer ----
  await step(steps, "Settings", async () => {
    useFleet.getState().openSettings();
    await within(8000, "the Notifications panel", () => $("[data-testid='notification-settings']"));
    const text = document.body.textContent ?? "";
    for (const s of ["Appearance", "Git", "Notifications", "External endpoints"]) if (!text.includes(s)) throw new Error(`no ${s} section`);
    // coverage C5: Settings > Endpoints reads fleet.yaml's endpoints and
    // their credential states (the smoke fleet configures the lab's
    // Alertmanager); S8a: Test reads its status
    const ep = await within(8000, "the endpoints panel", () => $("[data-testid='endpoints-settings']"));
    if ($("[data-testid='endpoints-error']")) throw new Error(`the endpoints panel could not read fleet.yaml: ${$("[data-testid='endpoints-error']")?.textContent}`);
    (await within(8000, "the Alertmanager Test", () => $<HTMLButtonElement>("button[aria-label='Test the kind-klyx Alertmanager endpoint']"))).click();
    const result = await within(20_000, "the Test's answer", () => byText<HTMLElement>("[data-testid='endpoints-settings'] *", /Available: Alertmanager 0\.34\.1/));
    // S8b: Loki's Test reads its build info and this cluster's label names
    (await within(8000, "the Loki Test", () => $<HTMLButtonElement>("button[aria-label='Test the kind-klyx Loki endpoint']"))).click();
    await within(20_000, "Loki's answer", () => byText<HTMLElement>("[data-testid='endpoints-settings'] *", /Available: Loki 3\.7\.8/));
    return { endpoints: ep.querySelectorAll("[data-testid='endpoint-row']").length, test: result.textContent?.trim().slice(0, 120) };
  });
  await step(steps, "chat drawer opens and closes, asked nothing", async () => {
    await open("overview");
    (await within(4000, "Ask", () => $<HTMLButtonElement>(".locbar-ask"))).click();
    const drawer = await within(4000, "the chat drawer", () => $("[data-testid='chat-drawer']"));
    sized(drawer, "the chat drawer");
    key(drawer, { key: "Escape" });
    await within(2000, "the chat drawer closed", () => !$("[data-testid='chat-drawer']"));
  });

  // ---- the inspector at this width ----
  await step(steps, "inspector layout at this width", async () => {
    const pods = dest("pods")!;
    navigate(cluster, pods);
    await page(pods, cluster);
    let row: HTMLElement | undefined;
    try {
      row = await within(8000, "a measurable pod row", () => [...document.querySelectorAll<HTMLElement>(".lens-row")].find((r) => r.getBoundingClientRect().height > 0));
    } catch {
      throw new Unmeasurable("no pod row has a measurable size");
    }
    await closeDrawer();
    row.click();
    sized(await within(5000, "the inspector", () => $("[data-testid='lens-inspector']")), "the inspector");
    const drawer = !!$(".lens-drawer");
    const narrow = settings.width <= 1100;
    if (drawer !== narrow) throw new Error(`the inspector is ${drawer ? "a drawer" : "split"} at ${settings.width}px`);
    if (drawer && !$(".lens-drawer")?.contains(document.activeElement)) throw new Error("focus did not move into the drawer");
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
