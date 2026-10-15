import { Browser } from "@wailsio/runtime";
import type { ClusterDTO, ExpiryItemDTO } from "../store/fleet";
import type { LineDTO } from "../store/line";
import type { LineGitCommitDTO, LineGitDTO } from "../store/lineGit";
import { commitHref, historyNote } from "../line/history";
import { short } from "../line/stationStrip";
import { useExpiryScans } from "../store/expiry";
import { lineLedger } from "../line/ledger";
import { stationLabel } from "../nav/stationOrder";
import { Aspect } from "../chrome/primitives";
import { UpdatedAt } from "../chrome/Age";
import { clusterDisplayName } from "./clusterName";

// The fleet screen's aside (The Line slice 5c): the Ledger - the revisions
// on the line now, or with Git history the commits not yet everywhere
// (slice 6d) - and the Horizon - what expires in the next 30 days, from the
// session's shared expiry scans.

const hhmm = (iso: string) => { const d = new Date(iso); const p = (n: number) => String(n).padStart(2, "0"); return `${p(d.getHours())}:${p(d.getMinutes())}`; };

export function Ledger({ dto, git = null }: { dto: LineDTO | null; git?: LineGitDTO | null }) {
  if (dto && git?.state === "ready") return <CommitLedger dto={dto} git={git} />;
  const revs = dto ? lineLedger(dto) : [];
  const note = historyNote(git);
  return (
    <section className="aside-section" aria-labelledby="ledger-h">
      <div className="aside-head">
        <h2 id="ledger-h" className="aside-title">Ledger</h2>
        <span className="aside-sub">{dto?.key ? "revisions on the line now" : "no key compared"}</span>
      </div>
      {note && <p className="aside-note" data-testid="ledger-history-note">{note}</p>}
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

const when = (iso: string) => {
  const d = new Date(iso);
  return `${d.toLocaleDateString([], { month: "short", day: "numeric" })} ${hhmm(iso)}`;
};

// The Ledger with Git history (The Line slice 6d, parent §3.7): "commits
// not yet everywhere" - newest first, each with dots for the ordered
// stations that contain it - and, apart, the commits only on an ahead or
// diverged station. Subjects and authors are plain text.
function CommitLedger({ dto, git }: { dto: LineDTO; git: LineGitDTO }) {
  const ordered = new Set(git.stations.filter((s) => s.state !== "unknown" && s.state !== "unrelated").map((s) => s.cluster));
  const all = [...(dto.stations ?? []), ...(dto.siding ?? [])];
  const tracks = [
    ...(dto.tracks ?? []).map((t) => ({ label: t.label, stops: (dto.stations ?? []).filter((s) => s.track === t.name && ordered.has(s.cluster)).sort((a, b) => a.rank - b.rank) })),
    { label: "siding", stops: (dto.siding ?? []).filter((s) => ordered.has(s.cluster)) },
  ].filter((t) => t.stops.length > 0);
  const place = (cluster: string) => {
    const s = all.find((x) => x.cluster === cluster);
    return s ? `${s.env || "siding"} · ${clusterDisplayName(cluster)}` : clusterDisplayName(cluster);
  };
  const t = git.target?.name ?? "the target";
  const row = (c: LineGitCommitDTO, has: string[]) => {
    const href = commitHref(git, c.sha);
    return (
      <li key={c.sha} className="ledger-commit" aria-label={`${short(c.sha)} ${c.subject}, by ${c.author}, ${when(c.time)}; on ${has.length} of ${ordered.size} ordered station${ordered.size === 1 ? "" : "s"}`}>
        <span className="ledger-id">
          {href
            ? <button type="button" className="ledger-sha ledger-link" title="Open in Git" onClick={() => void Browser.OpenURL(href)}>{short(c.sha)}</button>
            : <span className="ledger-sha">{short(c.sha)}</span>}
          <span className="ledger-subject" title={c.subject}>{c.subject}</span>
        </span>
        <span className="ledger-at">{c.author} · {when(c.time)}</span>
        <span className="ledger-tracks" aria-hidden>
          {tracks.map((tr) => (
            <span key={tr.label} className="ledger-track">
              <span className="ledger-track-label">{tr.label}</span>
              {tr.stops.map((s) => <span key={s.cluster} className="ledger-dot" data-holds={has.includes(s.cluster) ? "" : undefined} title={`${place(s.cluster)}${has.includes(s.cluster) ? " has it" : " does not have it yet"}`} />)}
            </span>
          ))}
        </span>
      </li>
    );
  };
  return (
    <section className="aside-section" aria-labelledby="ledger-h">
      <div className="aside-head">
        <h2 id="ledger-h" className="aside-title">Ledger</h2>
        <span className="aside-sub">commits not yet everywhere · {t}</span>
      </div>
      {git.ledger.length === 0
        ? <p className="aside-note">Every ordered station has every commit up to {t}.</p>
        : <ul className="ledger" data-testid="ledger-commits">{git.ledger.map((c) => row(c, c.has ?? []))}</ul>}
      {git.ledgerMore && <p className="aside-note">Older commits not shown.</p>}
      {git.only.map((o) => (
        <div key={o.sha} className="ledger-only" data-testid="ledger-only">
          <h3 className="ledger-only-head">Only on {o.clusters.map(place).join(", ")} · {o.state === "ahead" ? `ahead of ${t}` : "diverged"}</h3>
          <ul className="ledger">{o.commits.map((c) => row(c, c.has ?? []))}</ul>
          {(o.more ?? 0) > 0 && <p className="aside-note">+{o.more} more</p>}
        </div>
      ))}
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
