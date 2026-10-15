import type { ClusterDTO } from "../store/fleet";
import type { ClusterAttention } from "../nav/clusterAttention";
import { useLine } from "../store/line";
import { useLineGitFor } from "../store/lineGit";
import { stationReading } from "../fleet/stationReading";
import { markOf } from "../line/stationStrip";
import { gitStation } from "../line/history";
import { trackLabel } from "../line/verdict";
import { comparisonText, stationTimes } from "../line/station";
import { Aspect, type AspectKind } from "../chrome/primitives/Aspect";

// The station's verdict (The Line slice 11c): the first thing on the
// station page. Two facts, each with its own mark - its health, in the
// fleet board's words, and its place on the line - and no word the board's
// station cell does not already say.

// healthMark: a fault or look-at reading marks as such; a cluster that
// cannot be read, or is still checking, is never marked healthy.
function healthMark(r: ReturnType<typeof stationReading>): AspectKind {
  if (r.unreachable) return "checking";
  if (r.tone === "danger") return "fault";
  if (r.tone === "warning") return "attention";
  return /checking|unverified|ended/.test(r.text) ? "checking" : "none";
}

export function StationVerdict({ c, m }: { c: ClusterDTO; m: ClusterAttention | null }) {
  const dto = useLine((s) => s.dto);
  const git = useLineGitFor(dto);
  const reading = stationReading(c, m ?? undefined);
  const hMark = healthMark(reading);

  let lineMark: AspectKind = "none";
  let lineText: string;
  const station = dto?.stations?.find((s) => s.cluster === c.name);
  if (!dto) lineText = "the line is being read…";
  else if ((dto.siding ?? []).some((s) => s.cluster === c.name)) lineText = "on the siding: not compared on a track";
  else if (!station) lineText = "not on the line";
  else {
    const own = markOf(station);
    lineMark = reading.unreachable ? "none" : own === "none" && gitStation(git, station.cluster)?.held ? "attention" : own;
    const track = trackLabel(station, dto);
    const place = track ? `${station.env || "no environment"} on ${track}` : station.env || "no environment";
    lineText = [place, reading.unreachable ? null : comparisonText(station, git), ...(reading.unreachable ? [] : stationTimes(station))].filter(Boolean).join(" · ");
  }

  return (
    <section className="station-verdict" aria-label="Station verdict" data-testid="station-verdict">
      <h2 className="station-verdict-health" data-tone={reading.tone}>
        {hMark !== "none" && <Aspect kind={hMark} size={12} />}
        <span>{reading.text}</span>
      </h2>
      <p className="station-verdict-line" data-testid="station-verdict-line">
        {lineMark !== "none" && <Aspect kind={lineMark} size={10} />}
        <span>{lineText}</span>
      </p>
    </section>
  );
}
