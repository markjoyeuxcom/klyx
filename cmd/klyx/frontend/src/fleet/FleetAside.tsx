import type { ClusterDTO, ExpiryItemDTO } from "../store/fleet";
import type { LineDTO } from "../store/line";
import { useExpiryScans } from "../store/expiry";
import { lineLedger } from "../line/ledger";
import { stationLabel } from "../nav/stationOrder";
import { Aspect } from "../chrome/primitives";
import { UpdatedAt } from "../chrome/Age";
import { clusterDisplayName } from "./clusterName";

// The fleet screen's aside (The Line slice 5c): the Ledger - the revisions
// on the line now - and the Horizon - what expires in the next 30 days, from
// the session's shared expiry scans.

const hhmm = (iso: string) => { const d = new Date(iso); const p = (n: number) => String(n).padStart(2, "0"); return `${p(d.getHours())}:${p(d.getMinutes())}`; };

export function Ledger({ dto }: { dto: LineDTO | null }) {
  const revs = dto ? lineLedger(dto) : [];
  return (
    <section className="aside-section" aria-labelledby="ledger-h">
      <div className="aside-head">
        <h2 id="ledger-h" className="aside-title">Ledger</h2>
        <span className="aside-sub">{dto?.key ? "revisions on the line now" : "no key compared"}</span>
      </div>
      {!dto ? <p className="aside-note">Reading the line…</p>
        : revs.length === 0 ? <p className="aside-note">{dto.key ? "No station has a single applied revision." : "Nothing is compared, so no revision is on the line."}</p>
        : (
          <ul className="ledger">
            {revs.map((r) => (
              <li key={r.id} className="ledger-rev" aria-label={`${r.short}${r.isR ? ", the comparison revision" : ""}, on ${r.stations} station${r.stations === 1 ? "" : "s"}${r.fetchedAt ? `, fetched ${hhmm(r.fetchedAt)}` : ""}`}>
                <span className="ledger-id">
                  <span className="ledger-sha">{r.short}</span>
                  {r.isR && <span className="ledger-r" title="The comparison revision: a comparison, not a target">R</span>}
                  {r.fetchedAt && <span className="ledger-at">fetched {hhmm(r.fetchedAt)}</span>}
                </span>
                <span className="ledger-tracks" aria-hidden>
                  {r.tracks.map((t) => (
                    <span key={t.label} className="ledger-track">
                      <span className="ledger-track-label">{t.label}</span>
                      {t.stops.map((s) => <span key={s.cluster} className="ledger-dot" data-holds={s.holds ? "" : undefined} title={`${s.env} · ${clusterDisplayName(s.cluster)}${s.holds ? " runs it" : ""}`} />)}
                    </span>
                  ))}
                </span>
              </li>
            ))}
          </ul>
        )}
    </section>
  );
}

const SOURCE: Record<string, string> = { "tls-secret": "TLS secret", "webhook-ca": "webhook CA", "cert-manager": "Certificate" };
const HORIZON_DAYS = 30;

export function Horizon({ clusters }: { clusters: ClusterDTO[] }) {
  const scans = useExpiryScans();
  const now = Math.floor(Date.now() / 1000);
  const byName = new Map(clusters.map((c) => [c.name, c]));
  const rows: (ExpiryItemDTO & { cluster: string })[] = [];
  for (const m of scans.members) for (const it of scans.results[m.name]?.items ?? []) {
    if (it.notAfterUnix - now <= HORIZON_DAYS * 86400) rows.push({ ...it, cluster: m.name });
  }
  rows.sort((a, b) => a.notAfterUnix - b.notAfterUnix);
  const failed = scans.members.filter((m) => scans.results[m.name]?.error);
  const notYet = scans.members.filter((m) => !(m.name in scans.results));
  const days = (unix: number) => Math.floor((unix - now) / 86400);
  return (
    <section className="aside-section" aria-labelledby="horizon-h">
      <div className="aside-head">
        <h2 id="horizon-h" className="aside-title">Horizon</h2>
        <span className="aside-sub">next {HORIZON_DAYS} days</span>
        <span className="aside-end"><UpdatedAt at={scans.readAt} /><button type="button" className="l-chip" onClick={scans.refresh}>Refresh</button></span>
      </div>
      {rows.length > 0 && (
        <ul className="horizon">
          {rows.map((r) => {
            const d = days(r.notAfterUnix);
            const c = byName.get(r.cluster);
            const place = c ? stationLabel(c).place : "";
            return (
              <li key={`${r.cluster}/${r.source}/${r.namespace}/${r.name}/${r.detail}`} className="horizon-row" data-soon={d <= 14 ? "" : undefined}>
                <span className="horizon-days">{d <= 14 && <Aspect kind="attention" />}{d < 0 ? `expired ${-d}d ago` : `${d}d`}</span>
                <span className="horizon-what">{SOURCE[r.source] ?? r.source} <span className="horizon-name">{r.namespace ? `${r.namespace}/` : ""}{r.name}</span>{place ? ` · ${place}` : ""} <span className="horizon-cluster">{clusterDisplayName(r.cluster)}</span></span>
              </li>
            );
          })}
        </ul>
      )}
      {rows.length === 0 && notYet.length === 0 && failed.length < scans.members.length && (
        <p className="aside-note">Nothing found expiring in the next {HORIZON_DAYS} days, on the clusters scanned.</p>
      )}
      {(notYet.length > 0 || failed.length > 0) && (
        <p className="aside-note">
          {notYet.map((m) => <span key={m.name}>{clusterDisplayName(m.name)}: not scanned yet. </span>)}
          {failed.map((m) => <span key={m.name} title={scans.results[m.name]?.error}>{clusterDisplayName(m.name)}: could not be scanned. </span>)}
        </p>
      )}
    </section>
  );
}
