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

// elbowStyle is lineStyle turned to run across: a branch's first step joins
// its parent's trunk with it.
function elbowStyle(seg: RailSegment): CSSProperties {
  const l = lineStyle(seg).borderLeft as string;
  return { borderTop: l };
}

function lineStyle(seg: RailSegment): CSSProperties {
  switch (seg) {
    case "reported":
    case "failed":
      return { borderLeft: "2px solid var(--line-danger, var(--color-text-danger))" };
    case "incomplete":
    case "cancelled":
      return { borderLeft: "2px dashed var(--color-text-tertiary)" };
    case "chain":
    case "completed":
      return { borderLeft: "2px solid var(--line-route, var(--color-text-info))" };
    default:
      return { borderLeft: "2px solid var(--line-track, var(--color-border-secondary))" };
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

// The mockup's cause chain (slice 13c.6): 12px marks on 2px lines, a step's
// name at 14px with its line beneath.
const MARK = 12; // the marker box, and so the line's x
const LINE_X = MARK / 2;
const TOP = 4; // the mark's offset: centred on the 20px name line

// leadIn keeps the root step's segment: a ledger whose every step has its
// own outcome (the chat's reads) starts with a segment too.
export function Rail({ steps, label, className, leadIn = false }: { steps: RailStep[]; label: string; className?: string; leadIn?: boolean }) {
  return (
    <ol aria-label={label} className={className} data-rail style={{ listStyle: "none", margin: 0, padding: 0 }}>
      <RailSteps steps={steps} root={!leadIn} branch={false} />
    </ol>
  );
}

// INDENT is how far a branch sits in from its parent's line.
const INDENT = MARK + 12;

function RailSteps({ steps, root, branch }: { steps: RailStep[]; root: boolean; branch: boolean }) {
  return (
    <>
      {steps.map((s, i) => {
        const incoming = root && i === 0 ? null : s.segment;
        const next = steps[i + 1];
        const w = incoming ? s.segmentWords ?? words[incoming] : "";
        const show = !!incoming && !!shown[incoming] && !!w;
        return (
          <li key={s.key} data-step={s.key} data-segment={incoming ?? undefined} style={{ position: "relative", paddingLeft: INDENT, paddingBottom: next ? 16 : 0 }}>
            {incoming && (branch && i === 0
              ? <span aria-hidden data-seg-in data-elbow style={{ position: "absolute", left: LINE_X - INDENT, top: TOP + MARK / 2, width: INDENT - LINE_X, ...elbowStyle(incoming) }} />
              : <span aria-hidden data-seg-in style={{ position: "absolute", left: LINE_X - 1, top: 0, height: TOP, ...lineStyle(incoming) }} />)}
            {next && <span aria-hidden data-seg-out style={{ position: "absolute", left: LINE_X - 1, top: TOP + MARK + 3, bottom: 0, ...lineStyle(next.segment) }} />}
            {!next && s.branches?.length ? <span aria-hidden data-trunk style={{ position: "absolute", left: LINE_X - 1, top: TOP + MARK + 3, bottom: 0, ...lineStyle("source") }} /> : null}
            <span aria-hidden style={{ position: "absolute", left: 0, top: TOP }}>
              {s.aspect && s.aspect !== "none" ? <Aspect kind={s.aspect} size={MARK} /> : <span style={{ display: "inline-block", width: MARK, height: MARK, boxSizing: "border-box", borderRadius: 1, border: "2px solid var(--line-track, var(--color-border-secondary))" }} />}
            </span>
            <div className="rail-head" style={{ display: "flex", alignItems: "baseline", gap: 8, flexWrap: "wrap", minHeight: 20 }}>
              {w && !show && <span className="sr-only">{w}: </span>}
              {show && incoming && <span data-seg-words className="rail-seg-words" style={{ color: segColor(incoming) }}>{w}<span className="sr-only">: </span></span>}
              <span style={{ minWidth: 0, overflowWrap: "anywhere" }}>{s.label ?? s.name}</span>
              {s.tags?.map((t) => (
                <span key={t} data-tag className="rail-tag">{t}</span>
              ))}
            </div>
            {s.body && <div className="rail-body" style={{ marginTop: 4 }}>{s.body}</div>}
            {s.branches?.map((b, bi) => (
              <ol key={bi} data-branch style={{ listStyle: "none", margin: "12px 0 0", padding: 0 }}>
                <RailSteps steps={b} root={false} branch />
              </ol>
            ))}
          </li>
        );
      })}
    </>
  );
}
