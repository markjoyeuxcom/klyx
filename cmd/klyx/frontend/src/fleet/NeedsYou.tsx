import { create } from "zustand";
import type { ClusterDTO } from "../store/fleet";
import type { ClusterAttention } from "../nav/clusterAttention";
import { NAV_GROUPS, navigate } from "../nav/registry";
import { reveal } from "../nav/reveal";
import { Aspect } from "../chrome/primitives";
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
  else goDestination(row.cluster, row.destinationId);
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

function Row({ row }: { row: QueueRow }) {
  const where = <span className="queue-where"><span>{row.place || "no environment"}</span><span className="queue-cluster">{clusterDisplayName(row.cluster)}</span><span>{row.area}</span></span>;
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
  const text = row.kind === "more" ? moreText(row) : row.text;
  return (
    <button type="button" className="queue-row" data-kind={row.kind} data-tone={row.tone} onClick={() => open(row)} aria-label={`${text}, on ${row.cluster}`}>
      <span className="queue-mark">{row.kind !== "more" && <Mark row={row} />}</span>
      <span className="queue-main"><span className="queue-what">{text}</span></span>
      <span className="queue-meta">{where}</span>
    </button>
  );
}

export function NeedsYou({ clusters, models }: { clusters: ClusterDTO[]; models: Record<string, ClusterAttention> }) {
  const filter = useQueueFilter((s) => s.filter);
  const q = fleetQueue(clusters, models, filter);
  // the whole queue's counts, never the samples; "+" on a lower bound
  const plus = q.lower ? "+" : "";
  const counts = [q.faults > 0 ? `${q.faults}${plus} fault${q.faults === 1 && !plus ? "" : "s"}` : "", q.attention > 0 ? `${q.attention}${plus} to look at` : ""].filter(Boolean).join(" · ");
  return (
    <section className="queue" aria-labelledby="queue-h">
      <div className="queue-head">
        <h2 id="queue-h" className="queue-title">Needs you</h2>
        {counts && <span className="queue-counts">{counts}</span>}
        <div role="group" aria-label="Filter" className="queue-filters">
          {FILTERS.map((f) => (
            <button key={f.id} type="button" className="l-chip" aria-pressed={filter === f.id} onClick={() => useQueueFilter.setState({ filter: f.id })}>{f.label}</button>
          ))}
        </div>
      </div>
      <div className="queue-rows">
        {q.rows.map((row) => <Row key={row.key} row={row} />)}
        {q.rows.length === 0 && <p className="queue-empty">{filter === "all" ? "Nothing in the clusters read needs attention." : "Nothing in this filter, in the clusters read."}</p>}
      </div>
      {q.notRead.length > 0 && (
        <p className="queue-notread">
          Not read: {q.notRead.map((n, i) => (
            <span key={n.cluster} title={n.why}>{i > 0 ? ", " : ""}{clusterDisplayName(n.cluster)} ({n.why.split(":")[0]})</span>
          ))}
        </p>
      )}
    </section>
  );
}
