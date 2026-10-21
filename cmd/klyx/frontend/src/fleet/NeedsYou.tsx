import { create } from "zustand";
import type { ClusterDTO } from "../store/fleet";
import type { ClusterAttention } from "../nav/clusterAttention";
import { NAV_GROUPS, navigate } from "../nav/registry";
import { reveal } from "../nav/reveal";
import { useMemo } from "react";
import { Aspect, LinePos } from "../chrome/primitives";
import { useLine } from "../store/line";
import { clusterCodes } from "../line/codes";
import { linePosOf, type LinePosModel } from "../line/linePos";
import { fleetAxes } from "../nav/fleetAxes";
import { clusterDisplayName } from "./clusterName";
import { fleetQueue, type QueueFilter, type QueueRow } from "./queue";

// "Needs you" on the fleet screen (The Line slice 5b): the queue, its counts
// and its filters. The filter lasts the session.
export const useQueueFilter = create<{ filter: QueueFilter }>(() => ({ filter: "all" }));

const FILTERS: { id: QueueFilter; label: string }[] = [{ id: "all", label: "All" }, { id: "prd", label: "Prd only" }, { id: "faults", label: "Faults" }];

function goDestination(cluster: string, destinationId: string) {
  const d = NAV_GROUPS.flatMap((g) => g.destinations).find((x) => x.id === destinationId);
  if (d) navigate(cluster, d);
}

function open(row: QueueRow) {
  if (row.kind === "object") reveal(row.cluster, row.task, row.part, { gvr: row.ref.gvr, namespace: row.ref.namespace, name: row.ref.name, uid: row.ref.uid, parent: row.ref.parent });
  // an alert row opens the alert in the Alerts lens, which links its subject
  else if (row.kind === "alert") reveal(row.cluster, row.task, row.part, { gvr: row.ref.gvr, namespace: row.ref.namespace, name: row.ref.name, uid: row.ref.uid, fingerprint: row.ref.fingerprint });
  else goDestination(row.cluster, row.destinationId);
}

// alertMoreText: "+3 more alerts in Alerts"; a lower bound reads "or more".
export function alertMoreText(row: Extract<QueueRow, { kind: "alert-more" }>): string {
  return `+${row.count} ${row.lower ? "or more" : "more"} alert${row.count === 1 ? "" : "s"} in ${row.lens}`;
}

function Mark({ row }: { row: QueueRow }) {
  if (row.kind === "coverage") return <Aspect kind="checking" />;
  return <Aspect kind={row.tone === "danger" ? "fault" : "attention"} />;
}

// moreText: "+3 more in Pods (1 fault, 2 to look at)"; a lower bound reads
// "+3 or more in Pods".
export function moreText(row: Extract<QueueRow, { kind: "more" }>): string {
  const total = row.faults + row.attention;
  const split = row.faults > 0 && row.attention > 0 ? ` (${row.faults} fault${row.faults === 1 ? "" : "s"}, ${row.attention} to look at)` : "";
  return `+${total} ${row.lower ? "or more" : "more"} in ${row.lens}${split}`;
}

// Where a row is: its LinePos station tag (parent §3.7, slice 8a) and its
// area; a cluster the line does not place keeps its place and name. On the
// station page every row is here: the area alone.
function Row({ row, pos, here }: { row: QueueRow; pos: LinePosModel | null; here: boolean }) {
  const station = here ? null : pos ? <LinePos pos={pos} /> : <><span>{row.place || "no environment"}</span><span className="queue-cluster">{clusterDisplayName(row.cluster)}</span></>;
  const where = <span className="queue-where">{station}<span>{row.area}</span></span>;
  if (row.kind === "object") {
    const name = `${row.ref.namespace ? `${row.ref.namespace}/` : ""}${row.ref.name}`;
    return (
      <button type="button" className="queue-row" data-tone={row.tone} onClick={() => open(row)}
        aria-label={`${row.tone === "danger" ? "Fault" : "Look at"}: ${row.objectKind} ${name}, ${row.label}, on ${row.cluster}${row.time ? `, ${row.time}` : ""}`}>
        <span className="queue-mark"><Mark row={row} /></span>
        <span className="queue-main">
          <span className="queue-what"><span className="queue-kind">{row.objectKind}</span> <span className="queue-name">{name}</span> <span className="queue-label">{row.label}</span></span>
          {row.reason && <span className="queue-reason">{row.reason}</span>}
        </span>
        <span className="queue-meta">{where}{row.time && <span className="queue-time">{row.time}</span>}</span>
      </button>
    );
  }
  if (row.kind === "alert") {
    const what = row.subject ?? "the cluster";
    return (
      <button type="button" className="queue-row" data-kind="alert" data-tone={row.tone} onClick={() => open(row)}
        aria-label={`${row.tone === "danger" ? "Critical" : "Warning"} alert: ${row.label}, ${what}, on ${row.cluster}${row.time ? `, ${row.time}` : ""}`}>
        <span className="queue-mark"><Mark row={row} /></span>
        <span className="queue-main">
          <span className="queue-what"><span className="queue-kind">Alert</span> <span className="queue-name">{row.label}</span> <span className="queue-label">{what}</span></span>
          {row.reason && <span className="queue-reason">{row.reason}</span>}
        </span>
        <span className="queue-meta">{where}{row.time && <span className="queue-time">{row.time}</span>}</span>
      </button>
    );
  }
  const text = row.kind === "more" ? moreText(row) : row.kind === "alert-more" ? alertMoreText(row) : row.text;
  return (
    <button type="button" className="queue-row" data-kind={row.kind} data-tone={row.tone} onClick={() => open(row)} aria-label={`${text}, on ${row.cluster}`}>
      <span className="queue-mark">{row.kind !== "more" && row.kind !== "alert-more" && <Mark row={row} />}</span>
      <span className="queue-main"><span className="queue-what">{text}</span></span>
      <span className="queue-meta">{where}</span>
    </button>
  );
}

// station: the station page's queue (The Line slice 11c) - one cluster, no
// filters (All, Prd only and Faults are fleet questions), no station tag.
export function NeedsYou({ clusters, models, station = false }: { clusters: ClusterDTO[]; models: Record<string, ClusterAttention>; station?: boolean }) {
  const sessionFilter = useQueueFilter((s) => s.filter);
  const filter: QueueFilter = station ? "all" : sessionFilter;
  const q = fleetQueue(clusters, models, filter);
  const line = useLine((s) => s.dto);
  const codes = useMemo(() => clusterCodes(clusters.map((c) => c.name)), [clusters]);
  const faults = useMemo(() => new Set(fleetAxes(clusters, models).axes.fault.map((e) => e.cluster.name)), [clusters, models]);
  // the whole queue's counts, never the samples; "+" on a lower bound
  const plus = q.lower ? "+" : "";
  const aplus = q.alertsLower ? "+" : "";
  // the third count (S8a §6): alerts apart from the objects' faults
  const counts = [q.faults > 0 ? `${q.faults}${plus} fault${q.faults === 1 && !plus ? "" : "s"}` : "", q.attention > 0 ? `${q.attention}${plus} to look at` : "",
    q.alerts > 0 ? `${q.alerts}${aplus} alert${q.alerts === 1 && !aplus ? "" : "s"}` : ""].filter(Boolean).join(" · ");
  return (
    <section className="queue" aria-labelledby={station ? "station-queue-h" : "queue-h"} data-testid={station ? "station-queue" : undefined}>
      <div className="queue-head">
        <h2 id={station ? "station-queue-h" : "queue-h"} className="queue-title">Needs you</h2>
        {counts && <span className="queue-counts">{counts}</span>}
        {!station && (
          <div role="group" aria-label="Filter" className="queue-filters l-seg">
            {FILTERS.map((f) => (
              <button key={f.id} type="button" className="l-chip" aria-pressed={filter === f.id} onClick={() => useQueueFilter.setState({ filter: f.id })}>{f.label}</button>
            ))}
          </div>
        )}
      </div>
      <div className="queue-rows">
        {q.rows.map((row) => <Row key={row.key} row={row} here={station} pos={station ? null : linePosOf(line, row.cluster, codes, faults.has(row.cluster))} />)}
        {/* an unread station claims nothing: its not-read line stands alone */}
        {q.rows.length === 0 && !(station && q.notRead.length > 0) && (
          <p className="queue-empty">{station ? "Nothing on this station needs attention, in what was read." : filter === "all" ? "Nothing in the clusters read needs attention." : "Nothing in this filter, in the clusters read."}</p>
        )}
      </div>
      {q.notRead.length > 0 && station && (
        <p className="queue-notread" data-testid="station-queue-notread">Not read yet: {q.notRead[0].why}. Nothing is claimed about what needs you here.</p>
      )}
      {q.notRead.length > 0 && !station && (
        <p className="queue-notread">
          Not read: {q.notRead.map((n, i) => (
            <span key={n.cluster} title={n.why}>{i > 0 ? ", " : ""}{clusterDisplayName(n.cluster)} ({n.why.split(":")[0]})</span>
          ))}
        </p>
      )}
    </section>
  );
}
