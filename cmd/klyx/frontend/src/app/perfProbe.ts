import { openRows, perfHooks } from "../bridge/healthRows";
import { useFleet } from "../store/fleet";
import { useHealth } from "../store/health";
import { HealthService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// The slice 3 §6a webview gate probe - compiled in only with VITE_KLYX_PERF=1.
// It opens every lens consumer the gate names (Pods on screen, Workloads and
// Nodes rows beside it, the summary), then measures in the real webview, per
// push (the app runs with KLYX_ROWS_TRACE, which stamps emittedAtMs):
// - transport + decode: emit (Go wall clock) to handler entry (JS wall clock,
//   same machine) - the decode happens before the handler runs;
// - merge: the handler (applyRows into the store);
// - render: handler exit to the React commit of the resulting renders (the
//   lens's sort included) - a MessageChannel task runs after React's
//   microtask-flushed sync work;
// - emit to committed: their sum, the gate's "event receipt to store
//   committed (decode, merge, sort)" measured conservatively, transport
//   included;
// - interaction: a search keystroke, or a filter click that changes the
//   filter, to the second animation frame after it.
// Payload size is the Go trace's (the complete UTF-8 RowsDTO); the overlay
// shows a rows-only estimate for orientation. Rolling 120s statistics.

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

export function install(): void {
  const mc = new MessageChannel();
  const afterRender: (() => void)[] = [];
  mc.port1.onmessage = () => { const f = afterRender.shift(); if (f) f(); };
  let pendingPush = false;
  let podsPushes = 0;
  perfHooks.rows = (key, start, end, dto, receivedAtMs) => {
    add(`push:${key}`, 1);
    add(`merge:${key}`, end - start);
    const transport = dto?.emittedAtMs ? Math.max(0, receivedAtMs - dto.emittedAtMs) : null;
    if (transport !== null) add(`transport:${key}`, transport);
    afterRender.push(() => {
      const render = performance.now() - end;
      add(`render:${key}`, render);
      if (transport !== null) add(`total:${key}`, transport + (performance.now() - start));
    });
    mc.port2.postMessage(0);
    const rows = dto?.rows ?? null;
    if (key === "pods/pods") {
      pendingPush = true;
      podsPushes++;
      if (rows && podsPushes % 3 === 0) {
        const txt = JSON.stringify(rows);
        add(`bytes:${key}`, txt.length);
        const t = performance.now();
        JSON.parse(txt);
        add("parse:pods", performance.now() - t);
      }
    } else if (rows && Math.random() < 0.3) {
      add(`bytes:${key}`, JSON.stringify(rows).length);
    }
  };

  // diagnostics: frame intervals, and the one that carried a pods push (not
  // the gate's measure - a frame interval also holds idle time and other work)
  let last = performance.now();
  const frame = (now: number) => {
    const d = now - last;
    last = now;
    add("frame", d);
    if (pendingPush) { add("pushFrame", d); pendingPush = false; }
    requestAnimationFrame(frame);
  };
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

  // interaction: a search keystroke, then a filter click that changes the
  // filter (All and Attention alternate, so every click is a real change)
  let turn = 0;
  const measure = (act: () => void, changed: () => boolean, key: string) => {
    const t0 = performance.now();
    act();
    requestAnimationFrame(() => requestAnimationFrame(() => {
      if (changed()) add(key, performance.now() - t0);
      else add(`${key}:nochange`, 1);
    }));
  };
  setInterval(() => {
    const input = document.querySelector<HTMLInputElement>('input[placeholder="Search pods"]');
    if (!input) return;
    turn++;
    if (turn % 2 === 1) {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
      const next = input.value ? "" : "load";
      measure(() => { setter?.call(input, next); input.dispatchEvent(new Event("input", { bubbles: true })); }, () => input.value === next, "interact:search");
    } else {
      // the Pods lens's own chips, never another surface's
      const chips = [...(input.closest(".lens-controls") ?? document).querySelectorAll<HTMLButtonElement>("button.l-chip")];
      const attention = chips.find((b) => b.textContent?.startsWith("Attention"));
      const all = chips.find((b) => b.textContent?.startsWith("All"));
      const target = attention?.getAttribute("aria-pressed") === "true" ? all : attention;
      const label = target?.textContent?.startsWith("Attention") ? "Attention" : "All";
      // re-query after the paint: the chip may have re-rendered
      const pressed = () => [...(input.closest(".lens-controls") ?? document).querySelectorAll<HTMLButtonElement>("button.l-chip")]
        .find((b) => b.textContent?.startsWith(label))?.getAttribute("aria-pressed") === "true";
      if (target) measure(() => target.click(), pressed, "interact:filter");
    }
  }, 1500);

  // overlay
  const el = document.createElement("pre");
  el.setAttribute("data-testid", "perf-probe");
  Object.assign(el.style, { position: "fixed", right: "6px", bottom: "6px", zIndex: "99999", margin: "0", padding: "6px 8px", font: "11px/1.35 ui-monospace, monospace", background: "rgba(0,0,0,0.82)", color: "#9f9", borderRadius: "4px", pointerEvents: "none", whiteSpace: "pre" });
  document.body.appendChild(el);
  const t0 = performance.now();
  setInterval(() => {
    const span = Math.min(WINDOW_MS, performance.now() - t0) / 1000;
    const lines = [`perf probe - last ${span.toFixed(0)}s`];
    for (const k of ["pods/pods", "workloads/workloads", "nodes/nodes"]) {
      const n = recent(`push:${k}`).length;
      const b = recent(`bytes:${k}`);
      lines.push(`${k.padEnd(19)} pushes ${String(n).padStart(4)} (${(n / span).toFixed(2)}/s)  rows-json p95 ${(pct(b, 0.95) / 1048576).toFixed(2)}MB`);
      lines.push(`  emit->committed p50 ${fmt(pct(recent(`total:${k}`), 0.5))} p95 ${fmt(pct(recent(`total:${k}`), 0.95))} max ${fmt(Math.max(0, ...recent(`total:${k}`)))}ms | transport+decode p95 ${fmt(pct(recent(`transport:${k}`), 0.95))} merge p95 ${fmt(pct(recent(`merge:${k}`), 0.95))} render p95 ${fmt(pct(recent(`render:${k}`), 0.95))}ms`);
    }
    const pf = recent("pushFrame");
    const fr = recent("frame");
    const pa = recent("parse:pods");
    lines.push(`diag: pods push frame  p50 ${fmt(pct(pf, 0.5))}ms  p95 ${fmt(pct(pf, 0.95))}ms  max ${fmt(Math.max(0, ...pf))}ms  (n ${pf.length})`);
    lines.push(`pods JSON.parse  p95 ${fmt(pct(pa, 0.95))}ms  (n ${pa.length})`);
    lines.push(`diag: frames  p95 ${fmt(pct(fr, 0.95))}ms  >50ms ${fr.filter((x) => x > 50).length}/${fr.length}`);
    for (const k of ["interact:search", "interact:filter"]) {
      const a = recent(k);
      lines.push(`${k.padEnd(16)} p50 ${fmt(pct(a, 0.5))}ms  p95 ${fmt(pct(a, 0.95))}ms  (n ${a.length}, no-change ${recent(`${k}:nochange`).length})`);
    }
    const held = (task: string) => Math.max(0, ...Object.values(useHealth.getState().subs).filter((x) => x.task === task).map((x) => x.rows?.length ?? 0));
    lines.push(`rows held: pods ${held("pods")}  workloads ${held("workloads")}  nodes ${held("nodes")}`);
    el.textContent = lines.join("\n");
  }, 2000);
  // the report reaches the lab run through the backend, never the screen
  setInterval(() => { void HealthService.LabReport(el.textContent ?? "").catch(() => {}); }, 15000);
}
