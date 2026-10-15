import { useFleet } from "../store/fleet";
import { navigate } from "../nav/registry";
import { useNavCounts } from "../nav/useNavCounts";
import { FLEET_VIEWS, absentLine, lensIndex, type IndexEntry } from "../nav/lensIndex";
import { useLensIndexOpen } from "../nav/lensIndexOpen";
import { toneColor } from "./primitives/styles";
import { Aspect } from "./primitives";

// The lens index (The Line slice 4 §4): the open cluster's lenses as two
// sections, "Needs you" and "Everything else", with one quiet line for what
// is not installed; on fleet routes, the fleet views. It replaces NavBand.
// Every number is navCounts'; capability checking or unknown stays listed,
// disabled; only a confirmed absence hides a lens.
export const LENS_INDEX_ID = "lens-index";

export function LensIndex() {
  const open = useLensIndexOpen((s) => s.open);
  const route = useFleet((s) => s.route);
  if (!open) return null;
  return (
    // data-list-keys="ignore": a focused index entry owns its keys - Enter,
    // j/k and lens verbs never reach the list beside it.
    <nav id={LENS_INDEX_ID} aria-label="Lenses" className="lens-index" data-list-keys="ignore">
      {route.name === "cluster" ? <ClusterIndex cluster={route.cluster} /> : <FleetIndex />}
    </nav>
  );
}

function FleetIndex() {
  const route = useFleet((s) => s.route);
  return (
    <section className="lens-index-section" aria-labelledby="lens-index-fleet">
      <h2 id="lens-index-fleet" className="lens-index-head">Fleet</h2>
      {FLEET_VIEWS.map((v) => (
        <button key={v.label} type="button" className="lens-entry" title={v.title}
          aria-current={route.name === "fleet" && route.view === v.view ? "page" : undefined}
          onClick={() => useFleet.getState().openFleet(v.view)}>
          <span className="lens-entry-label">{v.label}</span>
        </button>
      ))}
    </section>
  );
}

function ClusterIndex({ cluster }: { cluster: string }) {
  const route = useFleet((s) => s.route);
  const kindFilter = useFleet((s) => s.workloads.kindFilter);
  const c = useFleet((s) => s.clusters.find((x) => x.name === cluster));
  const counts = useNavCounts(cluster);
  const m = lensIndex(c, counts, route, kindFilter);
  const absent = absentLine(m.absent);
  return (
    <>
      {m.needsYou.length > 0 && (
        <section className="lens-index-section" aria-labelledby="lens-index-needs">
          <h2 id="lens-index-needs" className="lens-index-head">Needs you</h2>
          {m.needsYou.map((e) => <Entry key={e.key} e={e} cluster={cluster} />)}
        </section>
      )}
      <section className="lens-index-section" aria-labelledby="lens-index-rest">
        <h2 id="lens-index-rest" className="lens-index-head">Everything else</h2>
        {m.rest.map((e) => <Entry key={e.key} e={e} cluster={cluster} />)}
      </section>
      {absent && <p className="lens-index-absent">{absent}</p>}
    </>
  );
}

function Entry({ e, cluster }: { e: IndexEntry; cluster: string }) {
  const disabled = e.state !== "present";
  const n = e.badge && e.badge.n > 0 ? e.badge.n : null;
  return (
    <>
      <button type="button" className="lens-entry" data-kind={e.kind} data-entry={e.key} data-marked={e.tone ? "" : undefined} aria-current={e.current ? "page" : undefined} disabled={disabled}
        title={[e.badge?.title, e.note ? `${e.label} - ${e.note}` : undefined].filter(Boolean).join("\n") || undefined}
        onClick={() => navigate(cluster, e.destination)}>
        {e.tone && <Aspect kind={e.tone === "danger" ? "fault" : "attention"} />}
        <span className="lens-entry-label">{e.label}</span>
        {e.note && <span className="lens-entry-note">{e.note}</span>}
        {n !== null && <span className="lens-entry-count" style={{ color: e.badge?.tone ? toneColor[e.badge.tone] : undefined }}>{n}</span>}
      </button>
      {e.subs.map((s) => (
        <button key={s.key} type="button" className="lens-entry lens-entry-sub" data-kind="sub" data-entry={s.key} aria-current={s.current ? "page" : undefined} disabled={s.state !== "present"}
          title={[s.badge?.title, s.note ? `${s.label} - ${s.note}` : undefined].filter(Boolean).join("\n") || undefined} onClick={() => navigate(cluster, s.destination)}>
          <span className="lens-entry-label">{s.label}</span>
          {s.note && <span className="lens-entry-note">{s.note}</span>}
          {s.badge && s.badge.n > 0 && <span className="lens-entry-count" style={{ color: s.badge.tone ? toneColor[s.badge.tone] : undefined }}>{s.badge.n}</span>}
        </button>
      ))}
    </>
  );
}
