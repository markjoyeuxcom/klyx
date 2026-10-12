import { openRows } from "../bridge/healthRows";
import { perfHooks } from "../health/perfHooks";
import { useFleet } from "../store/fleet";
import { useHealth } from "../store/health";
import { HealthService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// The slice 3 §6a webview gate probe - compiled in only with VITE_KLYX_PERF=1.
// It opens every consumer the gate names (Pods on screen, Workloads and Nodes
// rows beside it), then measures in the real webview (the app runs with
// KLYX_ROWS_TRACE, which stamps emittedAtMs; payload sizes are the Go
// trace's). Timing is anchored to an observed commit (addendum §3.4):
// - per ACCEPTED pods message (a delta or a full that applied; held and
//   ignored ones are counted apart): emit (Go wall clock) to handler entry
//   (transport and decode), the handler (merge into the store), handler to
//   the Pods lens's commit that shows that Ver or a newer one (reported by
//   the lens from a layout effect), and emit to commit in total;
// - bootstraps: the open's and a re-bootstrap's call (decode included) to
//   the commit that shows the reply; a forced lost message (every 90s by default) makes
//   a real wrong-base resync, timed from the held delta to that commit;
// - interaction: a search keystroke or a filter click, counted only when
//   the rendered list changed (the shown count or the first row), to the
//   second animation frame after it;
// - Workloads and Nodes are off screen: transport and handler only.
// Frame intervals are diagnostics. Rolling 120s statistics.

type Sample = { t: number; v: number };
const WINDOW_MS = 120_000;
const series: Record<string, Sample[]> = {};
const add = (k: string, v: number) => { (series[k] ??= []).push({ t: performance.now(), v }); };
const recent = (k: string) => {
  const cut = performance.now() - WINDOW_MS;
  const s = (series[k] ??= []);
  while (s.length && s[0].t < cut) s.shift();
  return s.map((x) => x.v);
};
const pct = (a: number[], p: number) => {
  if (!a.length) return 0;
  const s = [...a].sort((x, y) => x - y);
  return s[Math.min(s.length - 1, Math.ceil(p * s.length) - 1)];
};
const fmt = (v: number) => (v >= 100 ? v.toFixed(0) : v.toFixed(1));
const wall = () => performance.timeOrigin + performance.now();
const verKey = (inc: number, ver: number) => inc * 1e9 + ver;

// an accepted message or a bootstrap reply awaiting the commit that shows it
type Pending = { key: number; at: number; kind: "delta" | "full" | "open" | "rebootstrap"; emitted?: number; end: number; start: number; resyncAt?: number };

export function install(): void {
  const pending: Pending[] = [];
  let resyncSince: number | null = null; // a held pods delta: the resync's start
  let dropPods = false;

  perfHooks.drop = (key) => {
    if (key !== "pods/pods" || !dropPods) return false;
    dropPods = false;
    add("forced-drop", 1);
    return true;
  };
  perfHooks.rows = (key, start, end, dto, receivedAtMs, outcome) => {
    add(`${outcome}:${key}`, 1);
    if (outcome !== "applied" || !dto) {
      if (outcome === "held" && key === "pods/pods" && resyncSince === null) resyncSince = start;
      return;
    }
    const full = dto.full !== false;
    add(`${full ? "full" : "delta"}:${key}`, 1);
    add(`handler:${key}`, end - start);
    if (dto.emittedAtMs) add(`transport:${key}`, Math.max(0, receivedAtMs - dto.emittedAtMs));
    if (key === "pods/pods") {
      pending.push({ key: verKey(dto.incarnation, dto.part.ver), at: performance.now(), kind: full ? "full" : "delta", emitted: dto.emittedAtMs, start, end });
      add(`upserts:${key}`, full ? dto.rows?.length ?? 0 : dto.upserts?.length ?? 0);
    }
  };
  perfHooks.bootstrap = (key, kind, start, received, dto, outcome) => {
    add(`boot-${kind}:${outcome}:${key}`, 1);
    if (!dto) return;
    if (Math.random() < 0.5) add(`boot-bytes:${key}`, JSON.stringify(dto).length);
    if (key === "pods/pods" && outcome === "applied") {
      pending.push({ key: verKey(dto.incarnation, dto.part.ver), at: performance.now(), kind, start, end: received, resyncAt: kind === "rebootstrap" && resyncSince !== null ? resyncSince : undefined });
      add(`boot-call:${kind}:${key}`, received - start);
    }
  };
  perfHooks.commit = (key, inc, ver) => {
    if (key !== "pods/pods") return;
    const now = performance.now();
    const w = wall();
    const shown = verKey(inc, ver);
    for (let i = pending.length - 1; i >= 0; i--) {
      const p = pending[i];
      if (p.key > shown) continue;
      pending.splice(i, 1);
      if (p.key < shown) add("coalesced:pods/pods", 1);
      if (p.kind === "delta" || p.kind === "full") {
        add(`to-commit:${p.kind}`, now - p.end);
        if (p.emitted) add(`total:${p.kind}`, w - p.emitted);
      } else {
        add(`boot-total:${p.kind}`, now - p.start);
        if (p.resyncAt !== undefined) { add("resync-total", now - p.resyncAt); resyncSince = null; }
      }
    }
    // an entry no commit will ever show (superseded and never committed) ages out
    for (let i = pending.length - 1; i >= 0; i--) if (now - pending[i].at > 30_000) { pending.splice(i, 1); add("never-committed", 1); }
  };
  // a forced lost pods message (every 90s unless VITE_KLYX_PERF_DROP_MS
  // says otherwise; 0 is off): the next delta's base is wrong
  const dropMs = Number(import.meta.env.VITE_KLYX_PERF_DROP_MS ?? 90_000);
  if (dropMs > 0) setInterval(() => { dropPods = true; }, dropMs);

  // diagnostics: frame intervals
  let last = performance.now();
  const frame = (now: number) => { add("frame", now - last); last = now; requestAnimationFrame(frame); };
  requestAnimationFrame(frame);

  // open every consumer: the Pods lens on screen, Workloads and Nodes rows beside it
  let opened = false;
  const open = () => {
    const st = useFleet.getState();
    const c = st.clusters.find((x) => x.state === "connected") ?? st.clusters[0];
    if (opened || !c) return;
    opened = true;
    st.openCluster(c.name);
    st.setSection("pods");
    openRows(c.name, "workloads", "workloads");
    openRows(c.name, "nodes", "nodes");
  };
  const waitOpen = setInterval(() => { open(); if (opened) clearInterval(waitOpen); }, 500);
  // session restore may land elsewhere after the probe navigates: hold the
  // Pods lens on screen for the run (no keystrokes needed)
  setInterval(() => {
    const st = useFleet.getState();
    if (opened && (st.route.name !== "cluster" || st.route.section !== "pods")) {
      const c = st.clusters.find((x) => x.state === "connected") ?? st.clusters[0];
      if (c) { st.openCluster(c.name); st.setSection("pods"); }
    }
  }, 1000);

  // interaction: a search keystroke, then a filter click (All and Attention
  // alternate). A sample counts only when the rendered list changed: the
  // footer's shown count or the first row.
  const listSig = () => {
    const shown = [...document.querySelectorAll<HTMLElement>(".t-secondary")].find((e) => / of .*pods shown/.test(e.textContent ?? ""))?.textContent ?? "";
    const first = document.querySelector(".lens-row .lens-cell-name")?.textContent ?? "";
    return `${shown}|${first}`;
  };
  let turn = 0;
  const measure = (act: () => void, key: string) => {
    const before = listSig();
    const t0 = performance.now();
    act();
    requestAnimationFrame(() => requestAnimationFrame(() => {
      if (listSig() !== before) add(key, performance.now() - t0);
      else add(`${key}:nochange`, 1);
    }));
  };
  setInterval(() => {
    const input = document.querySelector<HTMLInputElement>('input[placeholder="Search pods"]');
    if (!input) return;
    turn++;
    if (turn % 2 === 1) {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
      const next = input.value ? "" : "klyx-load-03"; // one namespace of ten: a real list change
      measure(() => { setter?.call(input, next); input.dispatchEvent(new Event("input", { bubbles: true })); }, "interact:search");
    } else {
      // the Pods lens's own chips, never another surface's
      const chips = [...(input.closest(".lens-controls") ?? document).querySelectorAll<HTMLButtonElement>("button.l-chip")];
      const attention = chips.find((b) => b.textContent?.startsWith("Attention"));
      const all = chips.find((b) => b.textContent?.startsWith("All"));
      const target = attention?.getAttribute("aria-pressed") === "true" ? all : attention;
      if (target) measure(() => target.click(), "interact:filter");
    }
  }, 1500);

  // overlay (diagnostic) and the report
  const el = document.createElement("pre");
  el.setAttribute("data-testid", "perf-probe");
  Object.assign(el.style, { position: "fixed", right: "6px", bottom: "6px", zIndex: "99999", margin: "0", padding: "6px 8px", font: "11px/1.35 ui-monospace, monospace", background: "rgba(0,0,0,0.82)", color: "#9f9", borderRadius: "4px", pointerEvents: "none", whiteSpace: "pre" });
  document.body.appendChild(el);
  const t0 = performance.now();
  const stat = (k: string) => { const a = recent(k); return `p50 ${fmt(pct(a, 0.5))} p95 ${fmt(pct(a, 0.95))} max ${fmt(Math.max(0, ...a))} (n ${a.length})`; };
  setInterval(() => {
    const span = Math.min(WINDOW_MS, performance.now() - t0) / 1000;
    const n = (k: string) => recent(k).length;
    const lines = [`perf probe - last ${span.toFixed(0)}s`];
    for (const k of ["pods/pods", "workloads/workloads", "nodes/nodes"]) {
      lines.push(`${k.padEnd(19)} accepted ${n(`applied:${k}`)} (${(n(`applied:${k}`) / span).toFixed(2)}/s: delta ${n(`delta:${k}`)} full ${n(`full:${k}`)})  held ${n(`held:${k}`)}  ignored ${n(`ignored:${k}`)}`);
      lines.push(`  transport+decode ${stat(`transport:${k}`)} | handler ${stat(`handler:${k}`)}`);
    }
    lines.push(`pods emit->commit (delta) ${stat("total:delta")}`);
    lines.push(`pods emit->commit (full)  ${stat("total:full")}`);
    lines.push(`pods handler->commit delta ${stat("to-commit:delta")}  coalesced ${n("coalesced:pods/pods")}  never-committed ${n("never-committed")}`);
    lines.push(`pods upserts/msg ${stat("upserts:pods/pods")}`);
    lines.push(`boot open: call ${stat("boot-call:open:pods/pods")} | call->commit ${stat("boot-total:open")}`);
    lines.push(`boot rebootstrap: call ${stat("boot-call:rebootstrap:pods/pods")} | call->commit ${stat("boot-total:rebootstrap")}`);
    lines.push(`forced drops ${n("forced-drop")}  resync held->commit ${stat("resync-total")}`);
    const bb = recent("boot-bytes:pods/pods");
    lines.push(`boot reply json p95 ${(pct(bb, 0.95) / 1048576).toFixed(2)}MB (n ${bb.length})`);
    for (const k of ["interact:search", "interact:filter"]) lines.push(`${k.padEnd(16)} ${stat(k)}  no-change ${n(`${k}:nochange`)}`);
    const fr = recent("frame");
    lines.push(`diag: frames p95 ${fmt(pct(fr, 0.95))}ms  >50ms ${fr.filter((x) => x > 50).length}/${fr.length}`);
    const held = (task: string) => Math.max(0, ...Object.values(useHealth.getState().subs).filter((x) => x.task === task).map((x) => x.rows?.length ?? 0));
    lines.push(`rows held: pods ${held("pods")}  workloads ${held("workloads")}  nodes ${held("nodes")}`);
    el.textContent = lines.join("\n");
  }, 2000);
  // the report reaches the lab run through the backend, never the screen
  setInterval(() => { void HealthService.LabReport(el.textContent ?? "").catch(() => {}); }, 15000);
}
