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
vi.mock("../bridge/mesh", () => ({ getMeshGraph: vi.fn(async () => {}) }));
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
import { clusterDiagnosticScore, ClusterCard } from "../fleet/ClusterCard";
import { NavBand } from "../chrome/NavBand";
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
const ROW_PARTS = ["workloads/workloads", "pods/pods", "nodes/nodes", "flux/reconciliations", "flux/sources", "argo/applications", "argo/applicationsets",
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
  useFleet.setState({ clusters: [c.cluster], route: { name: "cluster", cluster: c.cluster.name, section: "overview" }, forwards: [], mesh: null });
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
  expect(clusterDiagnosticScore(c.cluster, m) === 0, `${c.case} band`).toBe(ok);
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

  it("the fleet indicator, the tree, the card and Overview render each case - and call no list endpoint", () => {
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

      // the tree: each lit group shows its count, a controller-only light and
      // a gap their words
      const tree = render(<NavBand />);
      act(() => { window.dispatchEvent(new Event("klyx:open-nav-tree")); });
      const dialog = tree.getByRole("dialog", { name: "All views" });
      // a group's own header row (its label, count and note) - never the list
      const group = (id: string) => {
        const label = NAV_GROUPS.find((g) => g.id === id)?.label;
        const row = Array.from(dialog.querySelectorAll(".navtree-group")).find((e) => e.querySelector("span")?.textContent === label);
        expect(row, `${c.case} tree group ${id}`).toBeTruthy();
        return row?.textContent ?? "";
      };
      for (const { a, l } of litLines) expect(group(a.groupId), `${c.case} ${a.id}`).toMatch(new RegExp(`${l.tone === "danger" ? "▲ " : ""}\\d`));
      for (const a of ctl) expect(group(a.groupId), `${c.case} ${a.id}`).toContain("controller not ready");
      for (const { a, l } of gapLines) expect(group(a.groupId), `${c.case} ${a.id}`).toContain(l.state === "unreadable" ? "unreadable" : l.gaps.some((g) => g.includes("last good")) ? "last good" : "unverified");
      tree.unmount();

      // the card
      const card = render(<ClusterCard c={c.cluster} />);
      const text = card.container.textContent ?? "";
      const conn = card.getByText("connection").nextElementSibling?.textContent;
      expect(conn, `${c.case} connection row`).toBe(c.expect.state === "Degraded" ? "connected · notes" : "connected");
      if (r.state === "attention") expect(text, c.case).toContain(`${r.areas} area${r.areas === 1 ? "" : "s"} need${r.areas === 1 ? "s" : ""} attention`);
      else expect(text, c.case).toContain(c.case === "B" ? "nothing raised" : "health partly unverified");
      for (const { l } of [...litLines, ...gapLines]) expect(text, `${c.case} ${l.part}`).toContain(lineText(l));
      if (ctl.length > 0) expect(text, c.case).toContain("controller not ready");
      card.unmount();

      // Overview
      useFleet.setState({ route: { name: "cluster", cluster: c.cluster.name, section: "overview" } });
      const ov = render(<Overview c={c.cluster} />);
      for (const a of m.areas) {
        const row = ov.getByTestId(`ov-area-${a.id}`).textContent ?? "";
        for (const l of a.lines) expect(row, `${c.case} ${a.id}/${l.part}`).toContain(`${l.state === "attention" && l.tone === "danger" ? "▲ " : ""}${lineText(l)}`);
        if (a.controller?.state === "not ready") expect(row, `${c.case} ${a.id}`).toContain("▲ controller not ready");
      }
      ov.unmount();
    }
    expect(calls).toEqual([]); // no list endpoint is called by any surface
  });
});
