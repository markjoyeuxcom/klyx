import { SummaryCell } from "./SummaryCell";
import type { Tone } from "./styles";

export type Meter = { label: string; value: string | number; tone?: Tone };

// MetersStrip — the mockup's meters row (Signal Bench), made first class: a
// left-aligned run of counter wells plus the engraved right-hand note naming
// the list's ordering contract ("broken first · severity order"). One
// component so every lens reads the same way; counts must come from loaded
// data - never fake a zero for a signal the store doesn't have.
export function MetersStrip({ meters, note }: { meters: ReadonlyArray<Meter | null | false>; note?: string }) {
  const cells = meters.filter((m): m is Meter => Boolean(m));
  if (cells.length === 0) return null;
  return (
    <div data-testid="meters-strip" style={{ display: "flex", gap: 0, alignItems: "stretch", flexWrap: "wrap", flexShrink: 0 }}>
      {cells.map((m) => (
        <SummaryCell key={m.label} label={m.label} value={String(m.value)} tone={m.tone ?? "neutral"} />
      ))}
      {note && (
        <span style={{ marginLeft: "auto", alignSelf: "flex-end", fontFamily: "var(--font-mono)", fontSize: 9, letterSpacing: "0.1em", textTransform: "uppercase", color: "var(--color-text-tertiary)", paddingBottom: 12, paddingRight: 4, whiteSpace: "nowrap" }}>
          {note}
        </span>
      )}
    </div>
  );
}
