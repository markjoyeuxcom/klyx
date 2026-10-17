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
  karpenter: "Karpenter", connectivity: "Connectivity check", gateway: "Gateways and routes", flows: "Hubble flows", access: "Access check",
  flux: "Flux CD", "flux-sources": "Flux CD", "flux-operator": "Flux CD", "flux-images": "Flux CD", argo: "Argo CD", "argo-projects": "Argo CD",
  "argo-appsets": "Argo CD", certs: "cert-manager", eso: "External Secrets", helm: "Helm",
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
export function gatewayView(): "gateways" | "routes" | null {
  const pressed = document.querySelector<HTMLElement>('[data-testid="gateway-views"] [aria-pressed="true"]')?.dataset.view;
  const head = document.querySelector('.lens-table-head > [data-col="name"]')?.textContent?.trim();
  const shown = head === "HTTPRoute" ? "routes" : head === "Gateway" ? "gateways" : null;
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
  await step(steps, "cert-manager issuance chain", async () => {
    await open("certs");
    sized(await within(10_000, "the issuance chain", () => $("ol[data-rail][aria-label='Issuance chain']")), "the issuance chain");
  });
  await step(steps, "External Secrets chain", async () => {
    await open("eso");
    sized(await within(10_000, "the ExternalSecret chain", () => $("ol[data-rail][aria-label='Store, ExternalSecret and target Secret']")), "the ExternalSecret chain");
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

  // ---- settings and the chat drawer ----
  await step(steps, "Settings", async () => {
    useFleet.getState().openSettings();
    await within(8000, "the Notifications panel", () => $("[data-testid='notification-settings']"));
    const text = document.body.textContent ?? "";
    for (const s of ["Appearance", "Git", "Notifications"]) if (!text.includes(s)) throw new Error(`no ${s} section`);
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
