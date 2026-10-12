import { openRows, perfHooks } from "../bridge/healthRows";
import { useFleet } from "../store/fleet";
import { useHealth } from "../store/health";

// The slice 3 §6a webview gate probe - compiled in only with VITE_KLYX_PERF=1.
// It opens every lens consumer the gate names (Pods on screen, Workloads and
// Nodes rows beside it, the summary), then measures in the real webview:
// - pushes per part and their size (the stringified rows),
// - the main-thread cost per push: the handler (merge) and the frame that
//   carried the push (decode + merge + render), with a JSON.parse estimate,
// - interaction: a search keystroke or a filter click to the next paint,
// and shows rolling 120s statistics in a corner overlay.

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
  let pendingPush = false;
  let podsPushes = 0;
  perfHooks.rows = (key, start, end, rows) => {
    add(`push:${key}`, 1);
    add(`handler:${key}`, end - start);
    if (key === "pods/pods") {
      pendingPush = true;
      podsPushes++;
      if (rows && podsPushes % 3 === 0) {
        const txt = JSON.stringify(rows);
        add("bytes:pods", txt.length);
        const t = performance.now();
        JSON.parse(txt);
        add("parse:pods", performance.now() - t);
      }
    } else if (rows && Math.random() < 0.3) {
      add(`bytes:${key}`, JSON.stringify(rows).length);
    }
  };

  // frame intervals: the one that carried a pods push bounds its main-thread cost
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

  // interaction: alternate a search keystroke and an attention-filter click
  let turn = 0;
  const measure = (act: () => void, key: string) => {
    const t0 = performance.now();
    act();
    requestAnimationFrame(() => requestAnimationFrame(() => add(key, performance.now() - t0)));
  };
  setInterval(() => {
    const input = document.querySelector<HTMLInputElement>('input[placeholder="Search pods"]');
    if (!input) return;
    turn++;
    if (turn % 2 === 1) {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
      const next = input.value ? "" : "load";
      measure(() => { setter?.call(input, next); input.dispatchEvent(new Event("input", { bubbles: true })); }, "interact:search");
    } else {
      const chip = [...document.querySelectorAll<HTMLButtonElement>("button.l-chip")].find((b) => b.textContent?.startsWith("Attention"));
      if (chip) measure(() => chip.click(), "interact:filter");
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
      const h = recent(`handler:${k}`);
      lines.push(`${k.padEnd(19)} pushes ${String(n).padStart(4)} (${(n / span).toFixed(2)}/s)  bytes p95 ${(pct(b, 0.95) / 1048576).toFixed(2)}MB max ${(Math.max(0, ...b) / 1048576).toFixed(2)}MB  handler p95 ${fmt(pct(h, 0.95))}ms`);
    }
    const pf = recent("pushFrame");
    const fr = recent("frame");
    const pa = recent("parse:pods");
    lines.push(`pods push frame  p50 ${fmt(pct(pf, 0.5))}ms  p95 ${fmt(pct(pf, 0.95))}ms  max ${fmt(Math.max(0, ...pf))}ms  (n ${pf.length})`);
    lines.push(`pods JSON.parse  p95 ${fmt(pct(pa, 0.95))}ms  (n ${pa.length})`);
    lines.push(`frames           p95 ${fmt(pct(fr, 0.95))}ms  >50ms ${fr.filter((x) => x > 50).length}/${fr.length}`);
    for (const k of ["interact:search", "interact:filter"]) {
      const a = recent(k);
      lines.push(`${k.padEnd(16)} p50 ${fmt(pct(a, 0.5))}ms  p95 ${fmt(pct(a, 0.95))}ms  (n ${a.length})`);
    }
    const held = (task: string) => Math.max(0, ...Object.values(useHealth.getState().subs).filter((x) => x.task === task).map((x) => x.rows?.length ?? 0));
    lines.push(`rows held: pods ${held("pods")}  workloads ${held("workloads")}  nodes ${held("nodes")}`);
    el.textContent = lines.join("\n");
  }, 2000);
}
