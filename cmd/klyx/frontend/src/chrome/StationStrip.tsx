import { useLine } from "../store/line";
import { stationStrip } from "../line/stationStrip";
import { Aspect } from "./primitives";

// The station strip (The Line slice 4 §5): the open cluster's position on
// its track, from the LineDTO alone. Nothing renders before the first DTO
// or for a cluster the line does not place; a siding says so. Its words
// are one accessible phrase; the drawing is hidden from assistive tech.
// The protected tag is drawn once, on the station crumb; the phrase says it.
export function StationStrip({ cluster, protection }: { cluster: string; protection?: string }) {
  const dto = useLine((s) => s.dto);
  const strip = stationStrip(dto, cluster, protection);
  if (!strip) return null;
  if (strip.kind === "siding") {
    return (
      <div role="group" aria-label="Position on the line" className="strip">
        <span className="strip-siding">siding</span>
      </div>
    );
  }
  return (
    <div role="group" aria-label="Position on the line" className="strip" title={strip.text}>
      <span className="sr-only">{strip.text}</span>
      {strip.stations.map((s, i) => (
        <span key={s.cluster} className="strip-stop" data-here={s.here ? "" : undefined} aria-hidden="true">
          {i > 0 && <span className="strip-seg" data-lit={strip.lit[i - 1] ? "" : undefined} />}
          {s.mark !== "none" && <Aspect kind={s.mark} />}
          {s.here ? (
            <span className="strip-box" data-mark={s.mark}>
              <span className="strip-label">{s.label}</span>
              <span className="strip-state">{s.box}</span>
            </span>
          ) : <span className="strip-label">{s.label}</span>}
        </span>
      ))}
    </div>
  );
}
