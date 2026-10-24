import type { ReactNode } from "react";
import type { ChatCheck, ChatRead, Finding, Findings } from "../bridge/chat";
import type { Cite } from "./SafeMarkdown";

// FindingsPanel is an investigation turn's findings (AI A1 §5): Klyx's own
// checks first, grouped by subject; then the model's findings, each labelled
// reading, suspected or unsupported by Klyx, with the checks it cites shown
// beside it as checked; then missing evidence; then the remedy, proposed and
// never run. A model finding is never itself checked. Statements are plain
// text. Nothing here acts on the cluster.

const LABELS: Record<Finding["label"], string> = { reading: "reading", suspected: "suspected", unsupported: "unsupported" };

function subjectText(s: { kind: string; namespace?: string; name: string }): string {
  return `${s.kind} ${s.namespace ? `${s.namespace}/` : ""}${s.name}`;
}

function windowText(w: string): string {
  if (w === "no failure to date") return "No failure to date: the object was not failing when it was read.";
  if (w === "unknown" || !w) return "When the failure began is unknown.";
  return `The failure was reported at ${w}; evidence observed before then does not establish it.`;
}

// checksBySubject groups the turn's checks by what they are about, in read
// order.
function checksBySubject(reads: ChatRead[]): [string, ChatCheck[]][] {
  const groups = new Map<string, ChatCheck[]>();
  for (const r of reads) {
    for (const c of r.checks ?? []) {
      const k = `${subjectText(c.subject)}${c.subject.identity === "name" ? " (by name)" : ""}`;
      groups.set(k, [...(groups.get(k) ?? []), c]);
    }
  }
  return [...groups.entries()];
}

function citeChip(id: string, cite: Cite | undefined, key: string): ReactNode {
  return cite?.(id, key) ?? <span key={key} className="t-legend">{id}</span>;
}

export function FindingsPanel({ findings, reads, cite }: { findings: Findings; reads: ChatRead[]; cite?: Cite }) {
  const groups = checksBySubject(reads);
  const nChecks = groups.reduce((n, [, cs]) => n + cs.length, 0);
  return (
    <section className="inv-findings" data-testid="inv-findings" aria-label="Findings">
      <div className="t-section-label">Findings</div>
      {findings.scope && <div className="chat-small" data-testid="inv-scope">{findings.scope}</div>}
      <div className="chat-small" data-testid="inv-window">{windowText(findings.window)}</div>

      {nChecks > 0 && (
        <details className="inv-checks" data-testid="inv-checks">
          <summary className="chat-small">Klyx's checks · {nChecks}</summary>
          {groups.map(([subject, cs]) => (
            <div key={subject} className="inv-group">
              <div className="t-legend">{subject}</div>
              <ul>
                {cs.map((c) => (
                  <li key={c.id}><span className="inv-label inv-label-checked">checked</span>{citeChip(c.id, cite, c.id)} {c.statement}</li>
                ))}
              </ul>
            </div>
          ))}
        </details>
      )}

      {findings.findings.length > 0 && (
        <ol className="inv-list">
          {findings.findings.map((f, i) => (
            <li key={i} className={`inv-finding inv-${f.label}`} data-testid="inv-finding">
              <div className="inv-head">
                <span className={`inv-label inv-label-${f.label}`}>{LABELS[f.label] ?? f.label}</span>
                <span className="t-legend">{f.type === "cause" ? "cause" : "observation"} · about {subjectText(f.subject)}</span>
              </div>
              <div className="inv-statement">{f.statement}</div>
              {f.cites.length > 0 && <div className="chat-small inv-cites">cites {f.cites.map((id, j) => citeChip(id, cite, `${i}-${j}`))}</div>}
              {(f.checked ?? []).map((c) => (
                <div key={c.id} className="chat-small inv-checked" data-testid="inv-checked">
                  <span className="inv-label inv-label-checked">checked</span>{c.id} {c.statement}
                  {c.before && <em> · observed before the failure was reported</em>}
                </div>
              ))}
              {(f.notes ?? []).map((n) => <div key={n} className="chat-small inv-note">{n}</div>)}
            </li>
          ))}
        </ol>
      )}
      {findings.note && <div className="chat-small" data-testid="inv-note">{findings.note}</div>}

      {findings.missing.length > 0 && (
        <div className="inv-missing" data-testid="inv-missing">
          <div className="t-legend">Missing evidence</div>
          <ul>
            {findings.missing.map((m, i) => (
              <li key={i} className="chat-small">
                <span className="t-legend">{m.from === "klyx" ? "Klyx" : "the model's note"}</span>{" "}
                {m.read && citeChip(m.read, cite, `m${i}`)} {m.text}
              </li>
            ))}
          </ul>
        </div>
      )}

      {findings.remedy && (
        <div className="inv-remedy" data-testid="inv-remedy">
          <div><span className="inv-label inv-label-proposed">{findings.remedy.label}</span><span className="t-legend">remedy, as text</span></div>
          {findings.remedy.git && <div className="chat-small"><span className="t-legend">Git change</span><div className="inv-pre">{findings.remedy.git}</div></div>}
          {findings.remedy.mitigation && <div className="chat-small"><span className="t-legend">Mitigation</span><div className="inv-pre">{findings.remedy.mitigation}</div></div>}
        </div>
      )}
      <div className="chat-small" style={{ color: "var(--color-text-tertiary)" }}>
        Labels are Klyx's: an observation is the model's reading, a cause is suspected, never proven. Nothing here acts on the cluster.
      </div>
    </section>
  );
}
