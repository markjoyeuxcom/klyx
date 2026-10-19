import { useMemo } from "react";
import { IconDatabaseExport } from "@tabler/icons-react";
import { getBackupHorizon, type VeleroFleetHorizonDTO, type VeleroScheduleDTO } from "../bridge/velero";
import { ellipsis } from "../chrome/primitives";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { UpdatedAt, useNow } from "../chrome/Age";
import { LensLayout, LensTable, type DatasetState } from "../layout/LensLayout";
import { VerdictMark } from "../health/VerdictView";
import { clock } from "../layout/coverage";
import { horizonCell, when } from "../cluster/VeleroView";
import { reveal } from "../nav/reveal";
import { useFleetBatch } from "./useFleetBatch";
import { MembershipNote } from "./MembershipNote";

// FleetBackups is the fleet's backup horizon (coverage S2b §2): every Velero
// schedule on every connected cluster, with its S2a verdict and its latest
// retained success, broken first and then the schedule longest without a
// success. Each cluster is read once, from its own Observer, as of that
// read; a cluster that could not be read, or has no Velero, is named, never
// silently missing. "Latest retained successful backup", never "restorable".

type Row = VeleroScheduleDTO & { cluster: string };

const RANK: Record<string, number> = { fault: 0, degraded: 1, unknown: 2, progressing: 3, paused: 4, ok: 5 };
const ROLE_TEXT: Record<string, string> = {
  fault: "var(--color-text-danger)", degraded: "var(--color-text-warning)", progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)", unknown: "var(--color-text-tertiary)", paused: "var(--color-text-tertiary)",
};
const roleKind = (r: string) => (r in RANK ? r : "unknown") as "fault" | "degraded" | "progressing" | "ok" | "unknown" | "paused";
const atOf = (s: VeleroScheduleDTO) => (s.horizon.at ? Date.parse(s.horizon.at) : 0);

const failed = (e: unknown): VeleroFleetHorizonDTO & { error?: string } => ({ cluster: "", state: "no-observer", kinds: [], schedules: [], error: e instanceof Error ? e.message : "the read failed" });

// coverageNote is what a cluster's read could not see (S2b §2).
export function coverageNote(r: VeleroFleetHorizonDTO): string {
  if (r.state === "no-observer") return "not read: Klyx has no live view of this cluster";
  if (r.state === "no-velero") return "no Velero";
  const notes: string[] = [];
  // the Observer names a kind by its resource
  const kind = (k: string) => r.kinds.find((x) => x.kind === k);
  const sch = kind("schedules");
  const bk = kind("backups");
  if (sch?.coverage === "stale") notes.push(`schedules as of ${sch.lastGoodAt ? clock(sch.lastGoodAt) : "the last good read"}`);
  else if (sch && sch.coverage !== "live") notes.push("schedules unreadable");
  if (bk?.truncated) notes.push("backups capped");
  if (bk?.coverage === "stale") notes.push("backups stale");
  else if (bk && bk.coverage !== "live") notes.push("backups unreadable");
  return notes.join(" · ");
}

export function FleetBackups() {
  const batch = useFleetBatch<VeleroFleetHorizonDTO & { error?: string }>(getBackupHorizon, failed);
  const nowS = Math.floor(useNow() / 1000);
  const clusters = batch.members;
  const replies = batch.results;
  const rows: Row[] = useMemo(() => {
    const out: Row[] = [];
    for (const c of clusters) for (const s of replies[c.name]?.schedules ?? []) out.push({ ...s, cluster: c.name });
    // broken first, then the oldest latest success first: none retained leads
    out.sort((a, b) => (RANK[a.rule.role] ?? RANK.unknown) - (RANK[b.rule.role] ?? RANK.unknown) || atOf(a) - atOf(b)
      || a.cluster.localeCompare(b.cluster) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name));
    return out;
  }, [replies, clusters]);
  const pending = clusters.filter((c) => !replies[c.name]).length;
  const noted = clusters.map((c) => ({ name: c.name, note: replies[c.name] ? coverageNote(replies[c.name]) : "" })).filter((c) => c.note);
  const withVelero = clusters.filter((c) => replies[c.name]?.state === "read").length;
  // known none, none in a capped history, and unknown history are three
  // facts (Codex's S2b implementation review, finding 4)
  const noSuccess = rows.filter((r) => r.horizon.state === "none" && !r.horizon.incomplete).length;
  const noneHeld = rows.filter((r) => r.horizon.state === "none" && r.horizon.incomplete).length;
  const unknownRuns = rows.filter((r) => r.horizon.state === "unreadable").length;
  const broken = rows.filter((r) => r.rule.role === "fault" || r.rule.role === "degraded").length;
  const grid = "24px minmax(150px, 1.2fr) minmax(110px, 0.6fr) minmax(150px, 1fr) minmax(130px, 0.8fr) 110px";

  let state: DatasetState = "ready";
  if (clusters.length === 0) state = "empty";
  else if (rows.length === 0 && pending > 0) state = "loading";
  else if (rows.length === 0) state = "empty";
  const icon = <IconDatabaseExport size={28} stroke={1.2} />;
  const scope = `${withVelero} of ${clusters.length} cluster${clusters.length === 1 ? "" : "s"} with Velero read`;
  const stateView = state === "loading" ? <SkeletonRows rows={5} label="reading Velero schedules" />
    : clusters.length === 0 ? <EmptyState icon={icon} title="No clusters to read." hint="The fleet had no clusters when this read started." action={{ label: "Refresh", onClick: batch.refresh }} />
    : <EmptyState icon={icon} title="No Velero schedules found." hint={`On the ${scope}. Clusters that could not be read are named above.`} />;

  return (
    <LensLayout
      label="Backups"
      title="Backups"
      summary={state === "loading" ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{rows.length}</span>
          <span className="t-secondary">schedule{rows.length === 1 ? "" : "s"} · {scope}</span>
          {broken > 0 && <span className="t-status" style={{ color: "var(--color-text-danger)" }}>▲ {broken} missed, failed or not running</span>}
          {noSuccess > 0 && <span className="t-status" style={{ color: "var(--color-text-warning)" }}>{noSuccess} with no retained success</span>}
          {noneHeld > 0 && <span className="t-secondary">{noneHeld} with none in the history Klyx holds</span>}
          {unknownRuns > 0 && <span className="t-secondary">{unknownRuns} with runs unknown</span>}
        </>
      )}
      headerEnd={
        <>
          <span className="t-secondary">{pending > 0 ? `${pending} cluster${pending === 1 ? "" : "s"} reading… · ` : ""}broken first, then the longest without a success · read-only</span>
          <UpdatedAt at={batch.readAt} />
          <button className="l-btn" onClick={batch.refresh}>Refresh</button>
        </>
      }
      notice={noted.length > 0 || batch.joined.length > 0 || batch.left.length > 0 ? (
        <div className="t-body" role="status" data-testid="fleet-backups-coverage" style={{ display: "flex", flexDirection: "column", gap: 2 }}>
          <MembershipNote joined={batch.joined} left={batch.left} />
          {noted.map((c) => <span key={c.name} style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>{c.name}: {c.note}</span>)}
        </div>
      ) : undefined}
      state={state}
      stateView={stateView}
      body={
        <LensTable minWidth={720}>
          <div className="lens-table-head" role="row" style={{ gridTemplateColumns: grid }}>
            <span /><span>Schedule</span><span>Cluster</span><span>Verdict</span><span>Last success</span><span>Run due</span>
          </div>
          <div style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
            {rows.map((r) => {
              const hz = horizonCell(r.horizon, nowS);
              const k = roleKind(r.rule.role);
              const asOf = replies[r.cluster]?.asOf;
              return (
                <div key={`${r.cluster}/${r.uid || r.namespace + "/" + r.name}`} role="button" tabIndex={0} className="lens-row" style={{ gridTemplateColumns: grid }}
                  data-testid={`fleet-backups-row-${r.cluster}-${r.name}`}
                  onClick={() => reveal(r.cluster, "velero", "schedules", { gvr: r.gvr, namespace: r.namespace, name: r.name, uid: r.uid })}
                  onKeyDown={(e) => { if (e.key === "Enter") reveal(r.cluster, "velero", "schedules", { gvr: r.gvr, namespace: r.namespace, name: r.name, uid: r.uid }); }}>
                  <span><VerdictMark health={{ kind: k, label: r.rule.label }} /></span>
                  <span style={{ ...ellipsis }} title={`${r.namespace}/${r.name}`}><span style={{ color: "var(--color-text-tertiary)" }}>{r.namespace}/</span>{r.name}</span>
                  <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={asOf ? `as of ${asOf}` : r.cluster}>{r.cluster}</span>
                  <span style={{ ...ellipsis, color: ROLE_TEXT[k] }} title={r.reason}>{r.rule.label}</span>
                  <span style={{ ...ellipsis, color: r.horizon.state === "found" ? "var(--color-text-secondary)" : "var(--color-text-warning)" }} title={hz.title}>
                    {hz.text}{hz.partial ? <span style={{ color: "var(--color-text-warning)" }}> · partial</span> : null}
                  </span>
                  <span style={{ ...ellipsis, color: "var(--color-text-secondary)" }}>{r.paused ? "paused" : r.dueZero ? "every check" : r.due ? when(r.due) : "-"}</span>
                </div>
              );
            })}
          </div>
        </LensTable>
      }
      footerEnd={<span className="t-secondary">Each cluster as of its own read · "latest retained successful backup": Completed is Velero's phase, not a restore test</span>}
    />
  );
}
