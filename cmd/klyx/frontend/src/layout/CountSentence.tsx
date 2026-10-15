import type { ReactNode } from "react";
import { Aspect } from "../chrome/primitives/Aspect";

// CountSentence is a lens header's count sentence (The Line slice 11b,
// parent §9): the total said with its noun, then the attention part in the
// tool's own words - led by its Aspect, fault when any counted object is a
// fault - or the lens's all-clear or not-known words, then its secondary
// facts. It changes no count: each lens passes its existing numbers and
// words. The header's ▲ is retired; the Aspect carries the shape.

export type CountAttention = {
  count: number;
  marker?: string; // the lens's lower-bound or stale marker ("+", "*")
  phrase: string; // "not ready", "need attention", "out of sync or unhealthy"
  fault: boolean; // any counted object is a fault
  detail?: string; // the breakdown, in brackets: "2 fault, 1 degraded"
  text?: string; // a lens whose words are a list says them whole: "1 failed · 2 pending, appear stuck"
  testid?: string;
};

export function CountSentence({ total, marker = "", noun, totalTestid, attention, clear, children }: {
  total: number;
  marker?: string;
  noun: readonly [string, string]; // singular, plural
  totalTestid?: string;
  attention?: CountAttention | null;
  clear?: ReactNode; // shown, in ink-2, when nothing needs attention
  children?: ReactNode; // the secondary facts, each its own span
}) {
  return (
    <>
      <span className="t-number" data-testid={totalTestid} style={{ color: "var(--color-text-secondary)" }}>
        {total}{marker} {total === 1 && !marker ? noun[0] : noun[1]}
      </span>
      {attention && attention.count > 0 ? (
        <MarkedPhrase fault={attention.fault} testid={attention.testid}>
          {attention.text ?? `${attention.count}${attention.marker ?? ""} ${attention.phrase}`}{attention.detail ? ` (${attention.detail})` : ""}
        </MarkedPhrase>
      ) : clear ? (
        <span className="t-secondary">{clear}</span>
      ) : null}
      {children}
    </>
  );
}

// MarkedPhrase is a header phrase that needs attention, led by its Aspect
// in place of the retired ▲: fault in the danger colour, else look-at.
export function MarkedPhrase({ fault, testid, children }: { fault: boolean; testid?: string; children: ReactNode }) {
  return (
    <span className="t-status" data-testid={testid}
      style={{ color: fault ? "var(--color-text-danger)" : "var(--color-text-warning)", display: "inline-flex", alignItems: "center", gap: 6 }}>
      <Aspect kind={fault ? "fault" : "attention"} size={8} />
      <span>{children}</span>
    </span>
  );
}
