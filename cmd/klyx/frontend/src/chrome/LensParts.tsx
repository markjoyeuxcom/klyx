import { useFleet } from "../store/fleet";
import { NAV_GROUPS, activeLocation, destState, navigate } from "../nav/registry";
import { useNavCounts } from "../nav/useNavCounts";
import { coverageOf } from "../nav/lensIndex";
import { toneColor } from "./primitives/styles";
import { Aspect } from "./primitives";

// LensParts is a tool lens's part switch (The Line slice 13b.1): one
// segmented control in the lens's controls, a button per task of its group -
// "Reconciliation | Sources 1" - each with navCounts' own attention count and
// coverage words, never a sum. It is the one switch: the lens index lists the
// lens alone. The pressed part is the route's; a lens says which part it
// shows through LensLayout's `part`, so a switch that leaves the wrong part on
// screen is caught (the smoke checks both).
//
// It owns its keys (data-list-keys="ignore", as the index did): Enter on a
// focused part presses it, and j/k or a row verb never reach the list behind
// (slice 13b review 1). A count that needs you carries the index's shape, and
// the button's name says it - "Reconciliation, 1 fault", "Sources, 1 to look
// at" - so fault and look-at never differ by colour alone; a neutral count
// stays bare.
export function LensParts({ cluster, groupId }: { cluster: string; groupId: string }) {
  const route = useFleet((s) => s.route);
  const kindFilter = useFleet((s) => s.workloads.kindFilter);
  const c = useFleet((s) => s.clusters.find((x) => x.name === cluster));
  const counts = useNavCounts(cluster);
  const g = NAV_GROUPS.find((x) => x.id === groupId);
  if (!g) return null;
  const tasks = g.destinations.filter((d) => d.type === "task" && destState(c, g, d) !== "absent");
  if (tasks.length < 2) return null;
  const shown = activeLocation(route, kindFilter).destination?.id;
  return (
    <div role="group" aria-label={`${g.label} parts`} className="l-seg" data-testid="lens-parts" data-list-keys="ignore">
      {tasks.map((d) => {
        const b = counts.destination[d.id];
        const cover = coverageOf(counts.destinationNote[d.id]);
        const needs = b && b.n > 0 && (b.tone === "danger" || b.tone === "warning") ? b.tone : null;
        const said = b && b.n > 0 ? `${b.n}${needs === "danger" ? (b.n === 1 ? " fault" : " faults") : needs ? " to look at" : ""}` : "";
        return (
          <button key={d.id} type="button" className="l-chip" data-part={d.id} aria-pressed={shown === d.id}
            aria-label={[d.label, said, cover].filter(Boolean).join(", ")}
            disabled={destState(c, g, d) !== "present"} title={[b?.title, cover ? `${d.label} - ${cover}` : ""].filter(Boolean).join("\n") || undefined}
            onClick={() => { if (shown !== d.id) navigate(cluster, d); }}>
            {d.label}
            {b && b.n > 0 && (
              <span className="lens-part-count" style={{ color: b.tone ? toneColor[b.tone] : "var(--color-text-tertiary)" }}>
                {needs && <Aspect kind={needs === "danger" ? "fault" : "attention"} />}
                {b.n}
              </span>
            )}
            {cover && <span className="t-secondary">{cover}</span>}
          </button>
        );
      })}
    </div>
  );
}
