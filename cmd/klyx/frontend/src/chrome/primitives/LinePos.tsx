import type { CSSProperties } from "react";
import type { LinePosModel } from "../../line/linePos";

// LinePos is a cluster's position glyph (The Line slice 8a, parent §5.4): its
// short code, then one square per rank on its track with its own filled
// (danger when it has a fault), or one hollow square and "siding". One image
// with one name: the full cluster name and its position.
const SQ = 6;

function square(filled: boolean, fault: boolean): CSSProperties {
  const ink = fault ? "var(--line-danger, var(--color-text-danger))" : "var(--color-text-secondary)";
  return filled
    ? { display: "inline-block", width: SQ, height: SQ, borderRadius: 1, background: ink }
    : { display: "inline-block", width: SQ, height: SQ, borderRadius: 1, boxSizing: "border-box", border: "1px solid var(--line-track, var(--color-border-secondary))" };
}

export function LinePos({ pos }: { pos: LinePosModel }) {
  return (
    <span role="img" aria-label={pos.name} title={pos.name} data-linepos={pos.cluster} style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
      <span aria-hidden className="t-identifier" data-code>{pos.code}</span>
      <span aria-hidden style={{ display: "inline-flex", alignItems: "center", gap: 2 }}>
        {pos.siding
          ? <><span data-sq style={square(false, false)} /><span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>siding</span></>
          : pos.squares.map((s) => <span key={s.rank} data-sq data-here={s.here || undefined} style={square(s.here, s.here && pos.fault)} />)}
      </span>
    </span>
  );
}
