import { useEffect } from "react";
import type { CSSProperties } from "react";
import { IconRefresh } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import { fetchTape } from "../bridge/tape";
import { StatusDot } from "./primitives";
import { asOfText, describeObservation, type VerdictObservation } from "../health/verdict";

// Shown is what one chip says, normalised across legacy number counts and
// verdict observations (health model 1b):
//   alert      - lit, clickable (attention / failures)
//   evidence   - clickable but never lit (Warning events)
//   unverified - read, but capped or partly unclassified: never "quiet"
//   quiet / unreadable
type Shown =
  | { state: "alert"; n: number; text: string; tone: "danger" | "warning" }
  | { state: "evidence"; n: number; text: string }
  | { state: "unverified"; text: string }
  | { state: "quiet" }
  | { state: "unreadable"; text: string };

function shownOf(chip: Chip): Shown {
  const c = chip.count;
  if (c === "absent" || c === "unreadable") return { state: "unreadable", text: String(c) };
  if (typeof c === "object") {
    const d = describeObservation(c);
    if (d.attention > 0) return { state: "alert", n: d.attention, text: d.text, tone: d.tone === "danger" ? "danger" : "warning" };
    return d.quiet ? { state: "quiet" } : { state: "unverified", text: d.text };
  }
  if (c <= 0) return { state: "quiet" };
  const text = `${c} ${c === 1 ? chip.valueLabel[0] : chip.valueLabel[1]}`;
  if (chip.variant === "evidence") return { state: "evidence", n: c, text };
  return { state: "alert", n: c, text, tone: chip.variant };
}

// Gentle re-poll cadence: the tape stays honest about a cluster that breaks
// while you are tailing logs, without hammering seven list endpoints.
export const tapeRepollMs = 60_000;

// TriageTape — the attention ledger under the header on EVERY cluster section.
// Nonzero cells are clickable and jump to the filtered lens; all-zero says
// "everything is quiet" explicitly. Counts the fetch could not read are
// counted in the trailer rather than being implied quiet.

type Chip = {
  key: string;
  count: import("../store/fleet").LensCount | VerdictObservation;
  label: [string, string]; // [singular, plural]
  tileLabel: string;
  valueLabel: [string, string];
  // "evidence" is a count worth a click that is NOT a fault (Warning events):
  // it never lights a lamp or reads as an alert.
  variant: "danger" | "warning" | "evidence";
  go: () => void;
};

export function TriageTape({ cluster }: { cluster: string }) {
  const tape = useFleet((s) => s.tape);

  // One fetch per cluster entry plus a gentle re-poll; section switches do
  // not refetch. Re-polls keep the previous counts on screen (no flicker).
  useEffect(() => {
    void fetchTape(cluster);
    const t = setInterval(() => void fetchTape(cluster), tapeRepollMs);
    return () => {
      clearInterval(t);
      useFleet.getState().clearTape();
    };
  }, [cluster]);

  if (tape.cluster !== cluster) return null;

  const nav = useFleet.getState();
  const chips: Chip[] = [
    { key: "workloads", count: tape.counts.workloads, label: ["workload needs attention", "workloads need attention"], tileLabel: "workloads", valueLabel: ["need attention", "need attention"], variant: "danger", go: () => { nav.setSection("workloads"); nav.setWorkloadsNeedsAttention(true); } },
    { key: "pods", count: tape.counts.pods, label: ["pod needs attention", "pods need attention"], tileLabel: "pods", valueLabel: ["need attention", "need attention"], variant: "danger", go: () => { nav.setSection("pods"); nav.setPodsNeedsAttention(true); } },
    { key: "events", count: tape.counts.events, label: ["warning event", "warning events"], tileLabel: "events", valueLabel: ["warning", "warnings"], variant: "evidence", go: () => { nav.setSection("events"); nav.setWarningsOnly(true); } },
    { key: "nodes", count: tape.counts.nodes, label: ["node needs attention", "nodes need attention"], tileLabel: "nodes", valueLabel: ["need attention", "need attention"], variant: "danger", go: () => nav.setSection("nodes") },
    { key: "helm", count: tape.counts.helm, label: ["failed release", "failed releases"], tileLabel: "helm", valueLabel: ["failed release", "failed releases"], variant: "danger", go: () => nav.setSection("helm") },
    { key: "flux", count: tape.counts.flux, label: ["flux not ready", "flux not ready"], tileLabel: "flux", valueLabel: ["not ready", "not ready"], variant: "danger", go: () => nav.setSection("gitops") },
    { key: "argo", count: tape.counts.argo, label: ["argo app not synced", "argo apps not synced"], tileLabel: "argo cd", valueLabel: ["not synced", "not synced"], variant: "warning", go: () => nav.setSection("argo") },
  ];

  // Absent tools are excluded entirely - a Flux-only cluster has no argo lens,
  // which is expected, not unreadable.
  const present = chips.filter((c) => c.count !== "absent");
  const alerts = present.filter((c) => shownOf(c).state === "alert");
  const readable = present.filter((c) => c.count !== "unreadable");
  const unreadable = present.length - readable.length;
  // Gaps are counted from coverage, independently of alerts: a lens with
  // known attention AND unknown/capped rows is both lit and a gap.
  const unverified = present.filter((c) => typeof c.count === "object" && describeObservation(c.count).gap).length;
  const lenses = (n: number) => `${n} lens${n === 1 ? "" : "es"}`;
  // Never "quiet" while any lens is unreadable or unverified.
  const gaps = [unreadable > 0 ? `${lenses(unreadable)} unreadable` : "", unverified > 0 ? `${lenses(unverified)} unverified` : ""].filter(Boolean).join(" · ");

  let trailer: string;
  if (tape.loading && readable.length === 0) trailer = "checking…";
  else if (readable.length === 0) trailer = "triage unavailable";
  else if (alerts.length === 0) trailer = gaps ? `no attention seen · ${gaps}` : "everything is quiet";
  else trailer = gaps || "everything else is quiet";

  const preferredKeys = new Set(["workloads", "pods", "events", "nodes", "helm"]);
  const preferred = present.filter((c) => preferredKeys.has(c.key));
  const extraAlerts = alerts.filter((c) => !preferredKeys.has(c.key));
  const cells = extraAlerts.length > 0
    ? [...preferred.slice(0, Math.max(0, 5 - extraAlerts.length)), ...extraAlerts]
    : preferred.slice(0, 5);

  // Signal-strip rendering (Signal Bench): one compact row, a lamp per lens,
  // and only faults are lit - a quiet cluster is a row of unlit glass, not
  // five boxes shouting "quiet". No band background, no column dividers; the
  // tape is part of the module it sits in.
  return (
    <div
      data-testid="triage-tape"
      style={{
        display: "flex",
        alignItems: "center",
        gap: 18,
        flexWrap: "wrap",
        padding: "7px 16px",
        borderBottom: "0.5px solid var(--color-border-tertiary)",
        flexShrink: 0,
        minHeight: 30,
        boxSizing: "border-box",
      }}
    >
      <span style={{ fontSize: 9, letterSpacing: "0.14em", textTransform: "uppercase", fontWeight: 600, color: "var(--color-text-tertiary)" }}>triage</span>
      {cells.map((c) => <TriageSignal key={c.key} chip={c} />)}
      <span style={{ marginLeft: "auto", display: "inline-flex", alignItems: "center", gap: 6, fontFamily: "var(--font-mono)", fontSize: 9, letterSpacing: "0.08em", textTransform: "uppercase", color: "var(--color-text-tertiary)", whiteSpace: "nowrap" }}>
        {trailer}
        <button
          onClick={() => void fetchTape(cluster)}
          aria-label="refresh triage"
          title="refresh triage"
          disabled={tape.loading}
          style={{
            display: "inline-flex",
            alignItems: "center",
            padding: 2,
            border: "none",
            background: "transparent",
            cursor: tape.loading ? "default" : "pointer",
            color: "var(--color-text-tertiary)",
            opacity: tape.loading ? 0.4 : 1,
          }}
        >
          <IconRefresh size={11} stroke={1.5} />
        </button>
      </span>
    </div>
  );
}

function TriageSignal({ chip }: { chip: Chip }) {
  const shown = shownOf(chip);
  const lit = shown.state === "alert";
  const active = shown.state === "alert" || shown.state === "evidence";
  const tone = lit ? shown.tone : "muted";
  const valueColor = lit
    ? shown.tone === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)"
    : shown.state === "evidence" || shown.state === "unverified" ? "var(--color-text-secondary)" : "var(--color-text-tertiary)";
  const value = shown.state === "quiet" ? "quiet" : shown.text;
  const asOf = typeof chip.count === "object" ? asOfText(chip.count) : "";
  const count = active ? shown.n : 0;
  const common: CSSProperties = {
    display: "inline-flex",
    alignItems: "center",
    gap: 7,
    padding: 0,
    border: "none",
    background: "transparent",
    color: "var(--color-text-secondary)",
    whiteSpace: "nowrap",
  };
  const body = (
    <>
      <StatusDot size={7} tone={tone} />
      <span style={{ fontSize: 9, letterSpacing: "0.1em", textTransform: "uppercase", color: "var(--color-text-tertiary)" }}>{chip.tileLabel}</span>
      <span style={{ color: valueColor, fontFamily: "var(--font-mono)", fontSize: 11, fontWeight: lit ? 700 : 400 }}>{value}</span>
    </>
  );

  if (!active) return <span style={common} title={asOf || undefined}>{body}</span>;

  return (
    <button
      onClick={chip.go}
      aria-label={`${count} ${count === 1 ? chip.label[0] : chip.label[1]}`}
      title={asOf || undefined}
      style={{ ...common, cursor: "pointer" }}
    >
      {body}
    </button>
  );
}
