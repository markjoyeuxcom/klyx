import { trackName } from "../line/place";
import { Fragment } from "react";
import { useFleet, type ClusterDTO } from "../store/fleet";
import type { LineDTO, LineStationDTO } from "../store/line";
import type { ClusterAttention } from "../nav/clusterAttention";
import { lineVerdict, trackLabel } from "../line/verdict";
import { gitStation } from "../line/history";
import { comparisonText, stationTimes } from "../line/station";
import type { LineGitDTO } from "../store/lineGit";
import { lineBoard, RANK_LABEL, type Board } from "../line/board";
import { markOf } from "../line/stationStrip";
import { Aspect } from "../chrome/primitives";
import { clusterDisplayName } from "./clusterName";
import { stationReading } from "./stationReading";

// The line on the fleet screen (The Line slice 5a): the verdict sentence and
// legend, then the board - tracks × ranks in a sunk well, the siding below a
// hairline, and the mesh the backend drew. Every word comes from LineDTO and
// each cluster's attention model; nothing claims a cause, a delivery path or
// that the line is live.

export function LineVerdictView({ dto, git = null }: { dto: LineDTO; git?: LineGitDTO | null }) {
  const v = lineVerdict(dto, git);
  return (
    <section className="line-verdict" aria-labelledby="line-verdict-h">
      <div className="line-verdict-text">
        {v.key && <div className="line-verdict-key">{v.key}</div>}
        {v.target && <div className="line-verdict-target" data-testid="line-target">{v.target}</div>}
        <h2 id="line-verdict-h" className="line-verdict-headline">{v.headline}{v.asOf && <span className="line-verdict-asof"> · {v.asOf}</span>}</h2>
        {v.clauses.map((c) => <p key={c} className="line-verdict-clause">{c}</p>)}
        {v.history && <p className="line-verdict-history" data-testid="line-history">{v.history}</p>}
      </div>
      <div className="line-legend" aria-label="Legend">
        <span className="line-legend-lit"><span className="line-legend-seg" data-lit="" aria-hidden />Lit: matches the comparison revision. A comparison, not a delivery path.</span>
        <span><Aspect kind="fault" /> fault</span>
        <span><Aspect kind="attention" /> look at</span>
        <span><Aspect kind="checking" /> checking</span>
        <span><Aspect kind="syncing" /> syncing</span>
        <span><span className="line-legend-mesh" aria-hidden /> mesh</span>
        <span><span className="line-legend-mesh" data-oneway="" aria-hidden /> one-way</span>
      </div>
    </section>
  );
}


export function StationCell({ s, dto, git = null, c, m, peers }: { s: LineStationDTO; dto: LineDTO; git?: LineGitDTO | null; c: ClusterDTO | undefined; m: ClusterAttention | undefined; peers: string[] }) {
  const reading = stationReading(c, m);
  const heldBehind = !!gitStation(git, s.cluster)?.held;
  const own = markOf(s);
  const mark = reading.unreachable ? "none" : own === "none" && heldBehind ? "attention" : own;
  const comparison = reading.unreachable ? null : comparisonText(s, git);
  const times = reading.unreachable ? [] : stationTimes(s);
  const track = trackLabel(s, dto);
  const prot = c?.protected ? (c.protectedInferred ? "protected (inferred)" : "protected") : null;
  // "aks-prd-neu, prd on westeurope, protected, 9f2c11d · differs, failing since 20:21; 2 areas need attention"
  const label = [s.cluster, track ? `${s.env || "no environment"} on ${track}` : s.env || "no environment", prot, comparison, ...times]
    .filter(Boolean).join(", ") + `; ${reading.text}`;
  return (
    <button type="button" className="station-cell" data-unreachable={reading.unreachable ? "" : undefined} data-mark={mark}
      aria-label={label} title={label} onClick={() => useFleet.getState().openCluster(s.cluster)}>
      <span className="station-cell-head">
        {mark !== "none" && <Aspect kind={mark} />}
        <span className="station-cell-env">{s.env || "no environment"}</span>
        {prot && <span className="locbar-protected">{prot}</span>}
      </span>
      <span className="station-cell-name">{clusterDisplayName(s.cluster)}</span>
      {comparison && <span className="station-cell-rev">{comparison}</span>}
      {reading.unreachable && c ? (
        // its connection word, then the reason on one line - the cell's
        // name and title hold it whole (slice 13d.8)
        <>
          <span className="station-cell-reading" data-unreachable-word="">{c.state === "Unconnected" ? "not connected" : "unreachable"}</span>
          {c.reason && <span className="station-cell-reason">{c.reason}</span>}
        </>
      ) : <span className="station-cell-reading" data-tone={reading.tone}>{reading.text}</span>}
      {times.map((t) => <span key={t} className="station-cell-time">{t}</span>)}
      {peers.length > 0 && <span className="station-cell-mesh">mesh: {peers.map(clusterDisplayName).join(", ")}</span>}
    </button>
  );
}

const col = (i: number) => 2 + i * 2; // the label is column 1; a segment column sits between ranks

export function LineBoard({ dto, git = null, clusters, models }: { dto: LineDTO; git?: LineGitDTO | null; clusters: ClusterDTO[]; models: Record<string, ClusterAttention> }) {
  const b: Board = lineBoard(dto);
  const byName = new Map(clusters.map((c) => [c.name, c]));
  const peersOf = (name: string) => (b.card[name] ?? []).map((l) => (l.a === name ? l.b : l.a));
  const cell = (s: LineStationDTO) => <StationCell key={s.cluster} s={s} dto={dto} git={git} c={byName.get(s.cluster)} m={models[s.cluster]} peers={peersOf(s.cluster)} />;
  const template = ["minmax(96px, max-content)", ...b.ranks.flatMap((_, i) => (i === 0 ? ["minmax(184px, 1fr)"] : ["28px", "minmax(184px, 1fr)"]))].join(" ");
  return (
    <section className="line-board-section" aria-label="The line">
      <div className="line-board-well">
        {b.fallback ? (
          <div className="line-board-fallback">
            <p className="line-board-note">Fewer than two environment ranks: the stations are one row, and nothing is lit.</p>
            <div className="line-board-row">{b.tracks[0]?.cells[0]?.stations.map(cell)}</div>
          </div>
        ) : (
          <div className="line-board" style={{ gridTemplateColumns: template }}>
            {b.ranks.map((r, i) => <div key={r} className="line-board-rank" style={{ gridColumn: col(i), gridRow: 1 }}>{RANK_LABEL[r] ?? `rank ${r}`}</div>)}
            {b.tracks.map((t, ti) => (
              <Fragment key={t.name || `track-${ti}`}>
                <div className="line-board-track" style={{ gridColumn: 1, gridRow: 2 + ti * 2 }}>{trackName(t)}</div>
                {t.cells.map((c, ci) => (
                  <Fragment key={c.rank}>
                    {ci > 0 && <div className="line-board-seg" aria-hidden data-lit={t.lit[ci - 1] ? "" : undefined} style={{ gridColumn: `${col(t.cells[ci - 1].col) + 1} / ${col(c.col)}`, gridRow: 2 + ti * 2 }} />}
                    <div className="line-board-cell" style={{ gridColumn: col(c.col), gridRow: 2 + ti * 2 }}>{c.stations.map(cell)}</div>
                  </Fragment>
                ))}
                {b.links.filter((l) => l.upper === ti).map((l) => (
                  <div key={`${l.a}-${l.b}`} className="line-board-link" data-oneway={l.mutual ? undefined : ""} style={{ gridColumn: col(b.ranks.indexOf(l.rank)), gridRow: 3 + ti * 2 }}
                    title={l.mutual ? `${l.a} and ${l.b} peer each other` : `one-way: configured on ${l.from || l.a}`}>
                    <span className="line-board-link-line" aria-hidden />
                    <span className="line-board-link-text">{l.mutual ? "mesh" : `one-way · ${clusterDisplayName(l.from || l.a)}`}</span>
                  </div>
                ))}
              </Fragment>
            ))}
          </div>
        )}
        {b.siding.length > 0 && (
          <div className="line-board-siding">
            <div className="line-board-siding-head">Siding · no environment</div>
            <div className="line-board-row">{b.siding.map(cell)}</div>
          </div>
        )}
      </div>
      {(b.offFleet.length > 0 || b.unknown.length > 0 || b.meshError) && (
        <div className="line-board-notes">
          {b.offFleet.length > 0 && <p>Off-fleet mesh peers: {b.offFleet.map((l) => `${clusterDisplayName(l.a)} ↔ ${clusterDisplayName(l.b)}${l.mutual ? "" : " (one-way)"}`).join(", ")}</p>}
          {b.unknown.length > 0 && <p>Mesh unverified for {b.unknown.map(clusterDisplayName).join(", ")}</p>}
          {b.meshError && <p>Mesh could not be read: {b.meshError}</p>}
        </div>
      )}
    </section>
  );
}
