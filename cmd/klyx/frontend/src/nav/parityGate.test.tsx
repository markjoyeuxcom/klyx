import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, act, cleanup } from "@testing-library/react";
// The frontend carries no Node types; vitest runs this file in Node.
// @ts-expect-error -- node:fs is untyped here
import fs from "node:fs";
// @ts-expect-error -- node:path is untyped here
import path from "node:path";
declare const process: { env: Record<string, string | undefined> };

// The 1c slice 6 coverage-parity gate, the frontend stage (spec
// docs/superpowers/specs/2026-10-12-klyx-health-1c-slice6-parity.md §3):
// each case the Go stage captured on the parity cluster, loaded into the
// real health store, through the model and the mounted surfaces (the fleet
// indicator replaced the mast's Attention button and the spine in The Line
// slice 4a).
// Skipped unless KLYX_PARITY_OUT names the Go stage's output.
//
//   KLYX_PARITY_OUT=<dir> npx vitest run src/nav/parityGate.test.tsx

const { calls, spy } = vi.hoisted(() => {
  const calls: string[] = [];
  return { calls, spy: (name: string) => vi.fn(async () => { calls.push(name); return {}; }) };
});
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  EventsService: { ListEvents: spy("ListEvents") },
  HelmService: { ListHelmReleases: spy("ListHelmReleases") },
  ArgoService: { ListProjects: spy("ListProjects") },
  GatewayService: { GetGatewayTopology: spy("GetGatewayTopology") },
  MetricsService: { GetClusterMetrics: spy("GetClusterMetrics") },
}));
vi.mock("../bridge/healthRows", () => ({ startSummaries: vi.fn() }));
vi.mock("../bridge/metrics", () => ({ getClusterMetrics: vi.fn(), getClusterSparklines: vi.fn(async () => ({ available: false, cpu: [], mem: [] })) }));
vi.mock("../bridge/events", () => ({ fetchEventsSnapshot: vi.fn(async () => ({ namespaces: [], events: [] })) }));
vi.mock("../bridge/crd", () => ({ listInstancePage: vi.fn(async () => ({ items: [], nextToken: "" })) }));
vi.mock("../bridge/forwards", () => ({ stopForward: vi.fn(), stopAllForwards: vi.fn() }));
vi.mock("../bridge/configsvc", () => ({ openTerminal: vi.fn(async () => {}) }));
vi.mock("../chrome/ThemeToggle", () => ({ ThemeToggle: () => null }));
vi.mock("../chrome/CommandPalette", () => ({ openCommandPalette: vi.fn() }));
import { useFleet, type ClusterDTO } from "../store/fleet";
import { useHealth, resetHealthStore, applySummary, applyRows, addSub, openStarted, openSucceeded, findPart, type HealthSummary, type HealthPart, type RowsDTO } from "../store/health";
import { TASKS, clusterAttention, lineText, mastReading, type Area, type ClusterAttention, type Line } from "./clusterAttention";
import { spineLamp, connectionText } from "./fleetAttention";
import { NAV_GROUPS } from "./registry";
import { StationCell } from "../fleet/LineBoard";
import { stationReading } from "../fleet/stationReading";
import { fleetQueue } from "../fleet/queue";
import { NeedsYou, useQueueFilter } from "../fleet/NeedsYou";
import type { LineDTO, LineStationDTO } from "../store/line";
import { LensIndex } from "../chrome/LensIndex";
import { lensIndex } from "./lensIndex";
import { navCounts } from "./useNavCounts";
import { FleetIndicator } from "../chrome/FleetIndicator";
import { fleetAxes, fleetAxesLabel } from "./fleetAxes";
import { Overview } from "../cluster/Overview";

type Roles = { total: number; fault: number; degraded: number; unknown: number; progressing: number; paused: number; ok: number };
type Member = { role: string; included: boolean };
type PartExpect = { task: string; part: string; classifier: string; counted: string[]; roles: Roles; included: number; members: Record<string, Member>; coverage: Record<string, string> };
type CaseExpect = { case: string; parts: PartExpect[]; controllers: Record<string, string>; state: string };
type Run = { id: string; commit: string; dirty: boolean; startedAt: string; reprobe: string };
type Capture = {
  run: Run; case: string; expect: CaseExpect; summary: HealthSummary; cluster: ClusterDTO; settled: string;
  rows: { cluster: string; incarnation: number; task: string; evaluatedAt: string; part: HealthPart }[];
  dtos: Record<string, RowsDTO<DtoRow>>; // the production sink's emitted messages: the lens DTOs
};
type DtoRow = { uid?: string; rule?: { included: boolean; role: string }; health?: { kind?: string } };
// the parts with a rows transport (Events and Helm are per-fetch snapshots)
const ROW_PARTS = ["workloads/workloads", "pods/pods", "nodes/nodes", "flux/reconciliations", "flux/sources", "flux/fluxinstance", "flux/resourcesets", "flux/images", "flux/composition", "flux/receivers", "argo/applications", "argo/applicationsets",
  "certs/certificates", "certs/issuers", "eso/externalsecrets", "eso/stores", "gateway/gateways", "gateway/routes", "karpenter/nodeclaims", "karpenter/nodepools"];

const OUT = process.env.KLYX_PARITY_OUT;
const CASES = ["B", "1", "2", "3", "4", "5a", "5b", "5c", "6", "6b", "7", "8"];

function captures(): Capture[] {
  const manifest = JSON.parse(fs.readFileSync(path.join(OUT!, "manifest.json"), "utf8")) as { run: Run; cases: string[] };
  expect(manifest.cases).toEqual(CASES); // every case, in order - a missing one never passes
  return manifest.cases.map((name) => {
    const c = JSON.parse(fs.readFileSync(path.join(OUT!, `${name}.json`), "utf8")) as Capture;
    // a capture from another run or commit never passes (spec §3)
    expect(c.run).toEqual(manifest.run);
    expect(c.case).toBe(name);
    return c;
  });
}

// load: the capture into the real store - the summary, and (the open
// cluster) every part's lens rows as the production sink emitted them, at
// the summary's incarnation and Ver.
function load(c: Capture, withRows: boolean) {
  resetHealthStore();
  useFleet.setState({ clusters: [c.cluster], route: { name: "cluster", cluster: c.cluster.name, section: "overview" }, forwards: [] });
  const now = Date.parse(c.summary.evaluatedAt);
  applySummary(c.summary, now);
  if (!withRows) return;
  for (const [h, dto] of Object.entries(c.dtos)) {
    addSub(h, dto.cluster, dto.task, dto.part.id);
    openStarted(h, "t");
    openSucceeded(h, "t", dto.incarnation);
    expect(applyRows(h, dto, now), `${c.case} ${h}`).toBe("applied");
  }
}

const ROLE_COUNT: Record<string, keyof Roles> = { fault: "fault", degraded: "degraded", unknown: "unknown", progressing: "progressing", paused: "paused", ok: "ok" };

// assertRowDTOs: every lens row's own evaluation - its rule, or its
// verdict - equals the object's declaration, and the rows derive the
// summary part's counts (review finding 2: never the copied part counts).
function assertRowDTOs(c: Capture) {
  expect(Object.keys(c.dtos).sort(), c.case).toEqual([...ROW_PARTS].sort());
  for (const pe of c.expect.parts) {
    const key = `${pe.task}/${pe.part}`;
    const dto = c.dtos[key];
    if (!dto) continue;
    const native = pe.classifier === "native";
    const derived: Roles = { total: 0, fault: 0, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 0 };
    let included = 0;
    const seen = new Set<string>();
    for (const row of dto.rows ?? []) {
      const uid = row.uid ?? "";
      const role = native ? row.rule?.role : row.health?.kind;
      expect(role, `${c.case} ${key} ${uid}: no evaluation`).toBeTruthy();
      const want = pe.members[uid];
      expect(want, `${c.case} ${key}: unexpected row ${uid}`).toBeTruthy();
      expect(role, `${c.case} ${key} ${uid} role`).toBe(want.role);
      if (native) expect(row.rule?.included, `${c.case} ${key} ${uid} included`).toBe(want.included);
      derived.total++;
      derived[ROLE_COUNT[role!] ?? "unknown"]++;
      if (native && row.rule?.included) included++;
      seen.add(uid);
    }
    expect([...seen].sort(), `${c.case} ${key} members`).toEqual(Object.keys(pe.members).sort());
    const part = findPart(c.summary, pe.task, pe.part)!;
    const counts = native ? { ...part.native!.roles, total: part.native!.total } : part.counts!;
    expect(derived, `${c.case} ${key} rows vs summary`).toEqual(counts);
    if (native) expect(included, `${c.case} ${key} included`).toBe(part.native!.included);
  }
}

const model = (c: Capture): ClusterAttention => clusterAttention(useHealth.getState(), c.cluster.name, c.cluster);
const lit = (pe: PartExpect) => pe.roles.fault + pe.roles.degraded;
const lineOf = (m: ClusterAttention, task: string, part: string): { a: Area; l: Line } => {
  const a = m.areas.find((x) => x.id === task);
  const l = a?.lines.find((x) => x.part === part);
  if (!a || !l) throw new Error(`${task}/${part} missing from the model`);
  return { a, l };
};
// the areas the oracle lights: a lit part, or a controller not ready
const litAreas = (ex: CaseExpect) => new Set([
  ...ex.parts.filter((p) => p.classifier !== "evidence" && lit(p) > 0).map((p) => p.task),
  ...Object.entries(ex.controllers).filter(([, s]) => s === "not ready").map(([t]) => t),
]);

function assertModel(c: Capture, m: ClusterAttention) {
  const ex = c.expect;
  expect(c.cluster.state, `${c.case} connection`).toBe(ex.state); // exact, never a prefix
  expect(m.ended).toBeNull();
  // every expected part answered: none still checking (formerly "not covered")
  expect(m.areas.flatMap((a) => a.lines.filter((l) => l.state === "checking").map((l) => `${a.id}/${l.part}`)), c.case).toEqual([]);
  // every step 7e area present
  expect(m.areas.map((a) => a.id).sort()).toEqual(TASKS.map((t) => t.task).sort());
  for (const pe of ex.parts) {
    const { a, l } = lineOf(m, pe.task, pe.part);
    const where = `${c.case} ${pe.task}/${pe.part}`;
    if (pe.classifier === "evidence") {
      expect(l.state, where).not.toBe("attention"); // evidence is never attention
      continue;
    }
    if (pe.classifier === "verdict") {
      expect(l.n, where).toBe(lit(pe));
      expect(l.state, where).toBe(lit(pe) > 0 ? "attention" : "quiet");
    } else {
      expect(l.n, where).toBe(pe.included > 0 ? pe.included : lit(pe));
      expect(l.state, where).toBe(lit(pe) > 0 ? "attention" : pe.included > 0 ? "unlit" : "quiet");
    }
    const text = lineText(l);
    const covs = Object.values(pe.coverage);
    for (const cov of covs) {
      if (cov.startsWith("unreadable")) expect(text, where).toContain(`could not be read (${cov.split(":")[1]})`);
      if (cov === "stale") expect(text, where).toMatch(/last good \d/);
    }
    if (covs.every((v) => v === "live")) expect(text, where).not.toMatch(/could not be read|last good/);
    expect(a.controller?.state ?? "", where).toBe(ex.controllers[pe.task] ?? "");
  }
  // the controller-only light: lit with no object count
  for (const [task, s] of Object.entries(ex.controllers)) {
    if (s !== "not ready") continue;
    const a = m.areas.find((x) => x.id === task)!;
    expect(a.state, `${c.case} ${task}`).toBe("attention");
    expect(a.lines.every((l) => l.state !== "attention"), `${c.case} ${task} lines`).toBe(true);
  }
  const want = litAreas(ex);
  const r = mastReading(m);
  expect(new Set(m.areas.filter((a) => a.state === "attention").map((a) => a.id)), c.case).toEqual(want);
  expect(r.areas, c.case).toBe(want.size);
  const gappy = ex.parts.some((p) => Object.values(p.coverage).some((v) => v !== "live"));
  expect(r.gaps > 0, `${c.case} gaps`).toBe(gappy);
  // "cluster ok" only with everything live and no unknowns: B alone
  const lamp = spineLamp(c.cluster, m);
  const ok = c.case === "B";
  expect(lamp.kind === "healthy", `${c.case} lamp ${lamp.kind}`).toBe(ok);
  expect(stationReading(c.cluster, m).text === "nothing raised", `${c.case} station reading`).toBe(ok);
  expect(r.state === "quiet" && r.gaps === 0, `${c.case} mast`).toBe(ok);
}

const suite = OUT ? describe : describe.skip;

suite("the parity gate's frontend stage (slice 6 §3)", () => {
  beforeEach(() => { calls.length = 0; cleanup(); });

  it("the model reads every captured case as the oracle declares - from the summary, and with the open cluster's rows", () => {
    for (const c of captures()) {
      assertRowDTOs(c);
      load(c, false);
      const fromSummary = model(c);
      assertModel(c, fromSummary);
      load(c, true);
      const withRows = model(c);
      assertModel(c, withRows);
      // the one-number rule: rows and summary at one Ver show one number
      for (const a of withRows.areas) {
        for (const l of a.lines) {
          const s = lineOf(fromSummary, a.id, l.part).l;
          expect([l.n, l.state], `${c.case} ${a.id}/${l.part}`).toEqual([s.n, s.state]);
        }
      }
    }
  });

  it("the fleet indicator, the lens index, the station cell, Needs you and Overview render each case - and call no list endpoint", () => {
    for (const c of captures()) {
      load(c, true);
      const m = model(c);
      const r = mastReading(m);
      const litLines = m.areas.flatMap((a) => a.lines.filter((l) => l.state === "attention").map((l) => ({ a, l })));
      const gapLines = m.areas.flatMap((a) => a.lines.filter((l) => l.gaps.length > 0 || l.state === "unreadable").map((l) => ({ a, l })));
      const ctl = m.areas.filter((a) => a.controller?.state === "not ready");

      // the fleet indicator: the case's cluster sits on the axes its model
      // and connection give it, each listing with its reason (The Line
      // slice 4 §3) - B alone on none
      const f = fleetAxes([c.cluster], { [c.cluster.name]: m });
      const ind = render(<FleetIndicator />);
      const chip = ind.getByRole("button", { name: /^Fleet:/ });
      expect(chip.getAttribute("aria-label"), c.case).toBe(fleetAxesLabel(f));
      const on = Object.entries(f.axes).filter(([, e]) => e.length > 0).map(([a]) => a);
      if (c.case === "B") expect(on, c.case).toEqual([]);
      if (r.state === "attention") expect(on, c.case).toContain(r.tone === "danger" ? "fault" : "warning");
      else expect(on.filter((a) => a === "fault" || a === "warning"), c.case).toEqual([]);
      if (r.gaps > 0) expect(on, c.case).toContain("unverified");
      // both axes: the connection is its own fact
      expect(connectionText(c.cluster).startsWith(c.expect.state.toLowerCase()), c.case).toBe(true);
      expect(on.includes("connection"), c.case).toBe(c.cluster.state === "Stale" || c.cluster.state === "Degraded");
      act(() => { chip.click(); });
      const listed = ind.queryAllByRole("menuitem").map((e) => e.textContent ?? "");
      for (const [, entries] of Object.entries(f.axes)) for (const e of entries) expect(listed.some((t) => t.includes(e.reason)), `${c.case} ${e.reason}`).toBe(true);
      // a lit reason is one of the model's lit lines or a not-ready controller
      const litReasons = [...litLines.map(({ a, l }) => `${a.label}${a.lines.length > 1 ? ` ${l.label}` : ""}: ${lineText(l)}`), ...ctl.map((a) => `${a.label}: controller not ready`)];
      for (const e of [...f.axes.fault, ...f.axes.warning]) expect(litReasons.some((x) => e.reason.startsWith(x)), `${c.case} ${e.reason}`).toBe(true);
      ind.unmount();

      // the lens index (slice 4 §4): every lit line's lens, and a
      // controller-only light, is in "Needs you" with its mark; every gap is
      // named on its lens - a tool group's on the tool's one lens
      const st = useFleet.getState();
      const im = lensIndex(c.cluster, navCounts(m), st.route, st.workloads.kindFilter);
      const ix = render(<LensIndex />);
      const needs = ix.container.querySelector('[aria-labelledby="lens-index-needs"]');
      const tool = (g: string) => NAV_GROUPS.find((x) => x.id === g)?.lens === "group";
      // a tool group's lit parts sit under its one lens row (slices 13a.5
      // and 13b.1): the lens row is context, and each lit part is a sub-row
      // with its own tone and mark
      const needsRow = (groupId: string, id: string) => {
        const lens = tool(groupId) ? im.needsYou.find((x) => x.key === `lens:${groupId}`) : undefined;
        const e = lens ? (lens.subs.find((x) => x.id === id) ?? lens) : im.needsYou.find((x) => x.id === id);
        expect(e, `${c.case} needs you ${id}`).toBeTruthy();
        const row = needs?.querySelector(`[data-entry="${e!.key}"]`);
        expect(row, `${c.case} needs-you row ${id}`).toBeTruthy();
        return { e: e!, row: row as HTMLElement };
      };
      for (const { a, l } of litLines) {
        const { e, row } = needsRow(a.groupId, l.destinationId ?? a.destinationId);
        if (l.destinationId && l.tone === "danger") expect([e.tone, row.querySelector('[data-aspect="fault"]') !== null], `${c.case} ${a.id}/${l.part}`).toEqual(["danger", true]);
      }
      for (const a of ctl) {
        const { e, row } = needsRow(a.groupId, a.destinationId);
        expect([e.tone, row.textContent?.includes("controller not ready")], `${c.case} ${a.id} controller`).toEqual(["danger", true]);
      }
      for (const { a, l } of gapLines) {
        const key = tool(a.groupId) ? `lens:${a.groupId}` : `dest:${l.destinationId ?? a.destinationId}`;
        const row = ix.container.querySelector(`[data-entry="${key}"]`);
        expect(row, `${c.case} ${key}`).toBeTruthy();
        expect(row?.textContent, `${c.case} ${a.id}/${l.part}`).toContain(l.state === "unreadable" ? "unreadable" : l.gaps.some((g) => g.includes("last good")) ? "last good" : "unverified");
      }
      ix.unmount();

      // the board's station cell (The Line slice 5a, replacing the card):
      // areas never objects, "nothing raised" for B alone, the connection
      // named apart. The per-line words moved to "Needs you" (5b).
      const station = { cluster: c.cluster.name, track: "fleet", rank: 0, env: "dev", state: "not compared", e: 0, a: [], u: 0, flags: { failing: false }, read: "ok", units: [] } as unknown as LineStationDTO;
      const lineDto = { version: 1, tracks: [{ name: "fleet", kind: "fleet", label: "Fleet" }], stations: [station], siding: [], keys: [] } as unknown as LineDTO;
      const cell = render(<StationCell s={station} dto={lineDto} c={c.cluster} m={m} peers={[]} />);
      const text = cell.container.querySelector(".station-cell-reading")?.textContent ?? "";
      if (r.state === "attention") expect(text, c.case).toContain(`${r.areas} area${r.areas === 1 ? "" : "s"} need${r.areas === 1 ? "s" : ""} attention`);
      else expect(text, c.case).toContain(c.case === "B" ? "nothing raised" : "health partly unverified");
      expect(text.includes("connected with notes"), `${c.case} connection`).toBe(c.expect.state === "Degraded");
      cell.unmount();

      // "Needs you" (slice 5b): each lit line's fault and attention samples
      // are rows with their label, "+N more" past them, each not-ready
      // controller and each gap line is a row - the card's per-line words
      const qm = fleetQueue([c.cluster], { [c.cluster.name]: m });
      for (const { a, l } of litLines) {
        const shown = l.top.filter((t) => t.role === "fault" || t.role === "degraded");
        for (const t of shown) expect(qm.rows.some((row) => row.kind === "object" && row.task === a.id && row.part === l.part && row.ref.name === t.name && row.label === t.label), `${c.case} ${a.id}/${l.part} ${t.name}`).toBe(true);
        // "+N more" is the hidden faults and attention objects, by role
        const more = qm.rows.find((row) => row.kind === "more" && row.task === a.id && row.part === l.part);
        const hf = Math.max(0, l.fault - shown.filter((t) => t.role === "fault").length), hd = Math.max(0, l.degraded - shown.filter((t) => t.role === "degraded").length);
        expect(more?.kind === "more" ? [more.faults, more.attention] : [0, 0], `${c.case} ${a.id}/${l.part} more`).toEqual([hf, hd]);
      }
      for (const a of ctl) expect(qm.rows.some((row) => row.kind === "controller" && row.task === a.id), `${c.case} ${a.id} controller row`).toBe(true);
      for (const { a, l } of gapLines) {
        if (l.gaps.some((g) => /could not be read|last good|not read yet/.test(g)) || l.state === "unreadable") expect(qm.rows.some((row) => row.kind === "coverage" && row.task === a.id && row.part === l.part), `${c.case} ${a.id}/${l.part} coverage row`).toBe(true);
      }
      useQueueFilter.setState({ filter: "all" });
      const qv = render(<NeedsYou clusters={[c.cluster]} models={{ [c.cluster.name]: m }} />);
      expect(qv.container.querySelectorAll(".queue-row").length, `${c.case} rendered rows`).toBe(qm.rows.length);
      qv.unmount();

      // Overview
      useFleet.setState({ route: { name: "cluster", cluster: c.cluster.name, section: "overview" } });
      const ov = render(<Overview c={c.cluster} />);
      for (const a of m.areas) {
        // The Line slice 13d retired the text "▲" for a shape-coded fault
        // mark (Aspect, data-aspect="fault"): a danger line or a not-ready
        // controller carries that mark beside its words.
        const el = ov.getByTestId(`ov-area-${a.id}`);
        const row = el.textContent ?? "";
        const faultMark = el.querySelector('[data-aspect="fault"]');
        for (const l of a.lines) {
          expect(row, `${c.case} ${a.id}/${l.part}`).toContain(lineText(l));
          if (l.state === "attention" && l.tone === "danger") expect(faultMark, `${c.case} ${a.id}/${l.part} fault mark`).not.toBeNull();
        }
        if (a.controller?.state === "not ready") {
          expect(row, `${c.case} ${a.id}`).toContain("controller not ready");
          expect(faultMark, `${c.case} ${a.id} controller fault mark`).not.toBeNull();
        }
      }
      ov.unmount();
    }
    expect(calls).toEqual([]); // no list endpoint is called by any surface
  });
});
