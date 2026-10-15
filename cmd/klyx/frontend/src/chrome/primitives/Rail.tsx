import type { CSSProperties, ReactNode } from "react";
import { Aspect, type AspectKind } from "./Aspect";

// Rail is The Line's vertical track of steps (spec 2026-10-14 §2.4, §5):
// each step names the segment that leads into it, and the segment's style
// follows its type. A style never carries meaning alone: every segment type
// also has words, spoken in the step's accessible name and shown when the
// type says something (a reported blocker, an incomplete stub, a failed or
// cancelled read). Branches are rails of their own, nested under their step.
//
//   source, dependsOn   declared edges: the neutral track
//   chain               a chain read in order: route
//   reported            a blocker the downstream controller reports: solid danger
//   incomplete          evidence that stops here: dashed grey
//   completed           a read that completed: route
//   failed              a read that failed: danger
//   cancelled           a read that was cancelled: dashed grey
export type RailSegment = "source" | "dependsOn" | "chain" | "reported" | "incomplete" | "completed" | "failed" | "cancelled";

export type RailStep = {
  key: string;
  segment: RailSegment; // the segment leading into this step (ignored on a rail's root without leadIn)
  segmentWords?: string; // overrides the type's words, e.g. "reported blocker"
  name: string; // the step in plain words
  label?: ReactNode; // what is drawn (its text is what is read); name when absent
  aspect?: AspectKind;
  tags?: string[];
  body?: ReactNode;
  branches?: RailStep[][];
};

const words: Record<RailSegment, string> = {
  source: "source",
  dependsOn: "depends on",
  chain: "",
  reported: "reported blocker",
  incomplete: "incomplete",
  completed: "completed",
  failed: "failed",
  cancelled: "cancelled",
};

// Shown with the step, not only spoken: the types whose words carry meaning.
const shown: Partial<Record<RailSegment, true>> = { reported: true, incomplete: true, failed: true, cancelled: true };

function lineStyle(seg: RailSegment): CSSProperties {
  switch (seg) {
    case "reported":
      return { borderLeft: "2px solid var(--line-danger, var(--color-text-danger))" };
    case "failed":
      return { borderLeft: "1px solid var(--line-danger, var(--color-text-danger))" };
    case "incomplete":
    case "cancelled":
      return { borderLeft: "1px dashed var(--color-text-tertiary)" };
    case "chain":
    case "completed":
      return { borderLeft: "1px solid var(--line-route, var(--color-text-info))" };
    default:
      return { borderLeft: "1px solid var(--line-track, var(--color-border-secondary))" };
  }
}

function segColor(seg: RailSegment): string {
  switch (seg) {
    case "reported":
    case "failed":
      return "var(--line-danger, var(--color-text-danger))";
    default:
      return "var(--color-text-tertiary)";
  }
}

const MARK = 8; // the marker box, and so the line's x
const LINE_X = MARK / 2;

// leadIn keeps the root step's segment: a ledger whose every step has its
// own outcome (the chat's reads) starts with a segment too.
export function Rail({ steps, label, className, leadIn = false }: { steps: RailStep[]; label: string; className?: string; leadIn?: boolean }) {
  return (
    <ol aria-label={label} className={className} data-rail style={{ listStyle: "none", margin: 0, padding: 0 }}>
      <RailSteps steps={steps} root={!leadIn} />
    </ol>
  );
}

function RailSteps({ steps, root }: { steps: RailStep[]; root: boolean }) {
  return (
    <>
      {steps.map((s, i) => {
        const incoming = root && i === 0 ? null : s.segment;
        const next = steps[i + 1];
        const w = incoming ? s.segmentWords ?? words[incoming] : "";
        const show = !!incoming && !!shown[incoming] && !!w;
        return (
          <li key={s.key} data-step={s.key} data-segment={incoming ?? undefined} style={{ position: "relative", paddingLeft: MARK + 8, paddingBottom: next ? 6 : 0 }}>
            {incoming && <span aria-hidden data-seg-in style={{ position: "absolute", left: LINE_X - 0.5, top: 0, height: 5, ...lineStyle(incoming) }} />}
            {next && <span aria-hidden data-seg-out style={{ position: "absolute", left: LINE_X - 0.5, top: 5 + MARK, bottom: 0, ...lineStyle(next.segment) }} />}
            <span aria-hidden style={{ position: "absolute", left: 0, top: 5 }}>
              {s.aspect && s.aspect !== "none" ? <Aspect kind={s.aspect} size={MARK} /> : <span style={{ display: "inline-block", width: MARK, height: MARK, boxSizing: "border-box", borderRadius: 1, border: "1px solid var(--line-track, var(--color-border-secondary))" }} />}
            </span>
            <div style={{ display: "flex", alignItems: "baseline", gap: 6, flexWrap: "wrap", minHeight: 18, fontSize: 12 }}>
              {w && !show && <span className="sr-only">{w}: </span>}
              {show && incoming && <span data-seg-words style={{ fontSize: 10, color: segColor(incoming), flexShrink: 0 }}>{w}<span className="sr-only">: </span></span>}
              <span style={{ minWidth: 0, overflowWrap: "anywhere" }}>{s.label ?? s.name}</span>
              {s.tags?.map((t) => (
                <span key={t} data-tag style={{ fontSize: 10, padding: "0 5px", borderRadius: 3, boxShadow: "inset 0 0 0 1px var(--line-hair, var(--color-border-tertiary))", color: "var(--color-text-secondary)", flexShrink: 0 }}>{t}</span>
              ))}
            </div>
            {s.body && <div style={{ marginTop: 3 }}>{s.body}</div>}
            {s.branches?.map((b, bi) => (
              <ol key={bi} data-branch style={{ listStyle: "none", margin: "6px 0 0", padding: 0 }}>
                <RailSteps steps={b} root={false} />
              </ol>
            ))}
          </li>
        );
      })}
    </>
  );
}
