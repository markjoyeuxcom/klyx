import { useFleet } from "../store/fleet";
import { navigate } from "../nav/registry";
import { useNavCounts } from "../nav/useNavCounts";
import { FLEET_VIEWS, absentLine, coverageOf, lensIndex, type IndexEntry } from "../nav/lensIndex";
import { LENS_INDEX_ID, useLensIndexOpen } from "../nav/lensIndexOpen";
import { toneColor } from "./primitives/styles";
import { Aspect } from "./primitives";
import { SettingsRail } from "../settings/SettingsRail";

// The lens index (The Line slice 4 §4): the open cluster's lenses as two
// sections, "Needs you" and "Everything else", with one quiet line for what
// is not installed; on fleet routes, the fleet views; on Settings, its
// sections and search (Settings S1 §1). It replaces NavBand.
// Every number is navCounts'; capability checking or unknown stays listed,
// disabled; only a confirmed absence hides a lens.
export { LENS_INDEX_ID };

export function LensIndex() {
  const open = useLensIndexOpen((s) => s.open);
  const route = useFleet((s) => s.route);
  if (!open) return null;
  return (
    // data-list-keys="ignore": a focused index entry owns its keys - Enter,
    // j/k and lens verbs never reach the list beside it.
    <nav id={LENS_INDEX_ID} aria-label={route.name === "settings" ? "Settings sections" : "Lenses"} className="lens-index" data-list-keys="ignore">
      {route.name === "cluster" ? <ClusterIndex cluster={route.cluster} /> : route.name === "settings" ? <SettingsRail /> : <FleetIndex />}
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

function Row({ e, cluster, sub }: { e: IndexEntry; cluster: string; sub?: boolean }) {
  const n = e.badge && e.badge.n > 0 ? e.badge.n : null;
  const cover = coverageOf(e.note);
  return (
    <button type="button" className={`lens-entry${sub ? " lens-entry-sub" : ""}`} data-kind={sub ? "sub" : e.kind} data-entry={e.key} data-marked={e.tone ? "" : undefined}
      data-context={e.context ? "" : undefined} aria-current={e.current ? "page" : undefined} disabled={e.state !== "present"}
      title={[e.badge?.title, e.note ? `${e.label} - ${e.note}` : undefined].filter(Boolean).join("\n") || undefined}
      onClick={() => navigate(cluster, e.destination)}>
      {e.tone && <Aspect kind={e.tone === "danger" ? "fault" : "attention"} />}
      <span className="lens-entry-text">
        <span className="lens-entry-label">{e.label}</span>
        {cover && <span className="lens-entry-note">{cover}</span>}
      </span>
      {n !== null && <span className="lens-entry-count" style={{ color: e.badge?.tone ? toneColor[e.badge.tone] : undefined }}>{n}</span>}
    </button>
  );
}

function Entry({ e, cluster }: { e: IndexEntry; cluster: string }) {
  return (
    <>
      <Row e={e} cluster={cluster} />
      {e.subs.map((s) => <Row key={s.key} e={s} cluster={cluster} sub />)}
    </>
  );
}
