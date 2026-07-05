import { useEffect } from "react";
import type { CSSProperties } from "react";
import { IconRefresh } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import { fetchTape } from "../bridge/tape";
import { StatusDot } from "./primitives";

// Gentle re-poll cadence: the tape stays honest about a cluster that breaks
// while you are tailing logs, without hammering seven list endpoints.
export const tapeRepollMs = 60_000;

// TriageTape — the attention ledger under the header on EVERY cluster section.
// Nonzero cells are clickable and jump to the filtered lens; all-zero says
// "everything is quiet" explicitly. Counts the fetch could not read are
// counted in the trailer rather than being implied quiet.

type Chip = {
  key: string;
  count: import("../store/fleet").LensCount;
  label: [string, string]; // [singular, plural]
  tileLabel: string;
  valueLabel: [string, string];
  variant: "danger" | "warning";
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
    { key: "workloads", count: tape.counts.workloads, label: ["unhealthy workload", "unhealthy workloads"], tileLabel: "workload health", valueLabel: ["need attention", "need attention"], variant: "danger", go: () => { nav.setSection("workloads"); nav.setWorkloadsNeedsAttention(true); } },
    { key: "pods", count: tape.counts.pods, label: ["pod not ready", "pods not ready"], tileLabel: "pods", valueLabel: ["not ready", "not ready"], variant: "danger", go: () => { nav.setSection("pods"); nav.setPodsNeedsAttention(true); } },
    { key: "events", count: tape.counts.events, label: ["warning event", "warning events"], tileLabel: "events", valueLabel: ["warning", "warnings"], variant: "warning", go: () => { nav.setSection("events"); nav.setWarningsOnly(true); } },
    { key: "nodes", count: tape.counts.nodes, label: ["node problem", "node problems"], tileLabel: "nodes", valueLabel: ["problem", "problems"], variant: "danger", go: () => nav.setSection("nodes") },
    { key: "helm", count: tape.counts.helm, label: ["failed release", "failed releases"], tileLabel: "helm", valueLabel: ["failed release", "failed releases"], variant: "danger", go: () => nav.setSection("helm") },
    { key: "flux", count: tape.counts.flux, label: ["flux not ready", "flux not ready"], tileLabel: "flux", valueLabel: ["not ready", "not ready"], variant: "danger", go: () => nav.setSection("gitops") },
    { key: "argo", count: tape.counts.argo, label: ["argo app not synced", "argo apps not synced"], tileLabel: "argo cd", valueLabel: ["not synced", "not synced"], variant: "warning", go: () => nav.setSection("argo") },
  ];

  // Absent tools are excluded entirely - a Flux-only cluster has no argo lens,
  // which is expected, not unreadable.
  const present = chips.filter((c) => c.count !== "absent");
  const alerts = present.filter((c) => typeof c.count === "number" && c.count > 0);
  const readable = present.filter((c) => typeof c.count === "number");
  const unreadable = present.length - readable.length;

  let trailer: string;
  if (tape.loading && readable.length === 0) trailer = "checking…";
  else if (readable.length === 0) trailer = "triage unavailable";
  else if (alerts.length === 0) trailer = unreadable > 0 ? `quiet · ${unreadable} lens${unreadable === 1 ? "" : "es"} unreadable` : "everything is quiet";
  else trailer = unreadable > 0 ? `${unreadable} lens${unreadable === 1 ? "" : "es"} unreadable` : "everything else is quiet";

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
  const count = chip.count;
  const isNumber = typeof count === "number";
  const active = isNumber && count > 0;
  const tone = !isNumber ? "muted" : active ? chip.variant : "muted";
  const valueColor = !isNumber
    ? "var(--color-text-tertiary)"
    : active
      ? chip.variant === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)"
      : "var(--color-text-tertiary)";
  const value = !isNumber
    ? String(count)
    : active
      ? `${count} ${count === 1 ? chip.valueLabel[0] : chip.valueLabel[1]}`
      : "quiet";
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
      <span style={{ color: valueColor, fontFamily: "var(--font-mono)", fontSize: 11, fontWeight: active ? 700 : 400 }}>{value}</span>
    </>
  );

  if (!active) return <span style={common}>{body}</span>;

  return (
    <button
      onClick={chip.go}
      aria-label={`${count} ${count === 1 ? chip.label[0] : chip.label[1]}`}
      style={{ ...common, cursor: "pointer" }}
    >
      {body}
    </button>
  );
}
